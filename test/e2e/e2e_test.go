//go:build e2e

// Package e2e drives booty end-to-end. It is build-tagged `e2e` so it never runs
// in the default `go test ./...` / CI unit pass; run it explicitly:
//
//	go test -tags=e2e ./test/e2e            # both tiers (QEMU tier skips if unequipped)
//	go test -tags=e2e -run Protocol ./test/e2e -v
//
// There are two tiers:
//
//   - TestE2EProtocolReachability starts booty's REAL TFTP and HTTP servers in
//     process (via the same Serve/Handler seams cmd/booty wires) and drives the
//     whole boot request chain over real sockets — TFTP-load ipxe.efi, fetch the
//     chain script, resolve /ipxe, download the kernel, pull the machineconfig.
//     No VM required; this tier runs anywhere.
//
//   - TestE2EQEMUBoot boots an actual UEFI virtual machine with qemu + OVMF and a
//     prebuilt ipxe.efi, and asserts on the request sequence booty observes from
//     the booting guest. It SKIPS cleanly unless the tooling is present (see the
//     env vars in the test). This is the true full-boot proof.
package e2e

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/donaldgifford/booty/catalog"
	"github.com/donaldgifford/booty/httpsrv"
	"github.com/donaldgifford/booty/render"
	"github.com/donaldgifford/booty/tftp"
)

// A known worker from examples/catalog: MAC -> talos-worker profile, hostname
// talos-worker-01, booting talos/v1.7.6/{vmlinuz,initramfs.xz}.
const (
	workerMAC   = "d0:50:99:b3:4c:50"
	proxmoxMAC  = "d0:50:99:d5:6e:72" // pve-01 in examples/catalog
	kernelPath  = "talos/v1.7.6/vmlinuz"
	initrdPath  = "talos/v1.7.6/initramfs.xz"
	ipxeEFIName = "ipxe.efi"
)

func catalogDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("BOOTY_E2E_CATALOG")
	if dir == "" {
		dir = filepath.Join("..", "..", "examples", "catalog") // CWD is test/e2e
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("catalog dir %q not found (set BOOTY_E2E_CATALOG): %v", dir, err)
	}
	return dir
}

// stageBootDir builds a boot directory with the assets a booting machine fetches:
// a stand-in ipxe.efi (multi-block, to exercise TFTP EOF) and fake kernel/initrd.
// It returns the dir plus the kernel bytes so callers can assert on the download.
func stageBootDir(t *testing.T) (dir string, kernel []byte) {
	t.Helper()
	dir = t.TempDir()
	// ipxe.efi: 1100 bytes -> 512 + 512 + 76, so the TFTP client sees two full
	// blocks then a short final block (the wire signal for end-of-file).
	ipxe := bytes.Repeat([]byte("IPXE"), 275) // 1100 bytes
	kernel = bytes.Repeat([]byte("KERNEL\n"), 200)
	initrd := bytes.Repeat([]byte("INITRD\n"), 300)

	write := func(rel string, b []byte) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(ipxeEFIName, ipxe)
	write(kernelPath, kernel)
	write(initrdPath, initrd)
	return dir, kernel
}

// recorder captures the path of every HTTP request booty serves, so a test can
// assert on the sequence a booting machine produced.
type recorder struct {
	mu    sync.Mutex
	paths []string
}

func (rec *recorder) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.paths = append(rec.paths, r.URL.Path)
		rec.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (rec *recorder) snapshot() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]string(nil), rec.paths...)
}

func (rec *recorder) sawAll(paths ...string) bool {
	have := map[string]bool{}
	for _, p := range rec.snapshot() {
		have[p] = true
	}
	for _, want := range paths {
		if !have[want] {
			return false
		}
	}
	return true
}

// booty holds a running in-process instance and its reachable addresses.
type booty struct {
	httpBase string // e.g. http://127.0.0.1:54321
	httpHost string // host:port
	tftpAddr string // host:port (UDP)
	rec      *recorder
}

// startBooty loads the example catalog, builds the renderer, and runs booty's
// real TFTP and HTTP servers on the given addresses (":0" = ephemeral loopback).
// It mirrors exactly how cmd/booty wires the two servers, using the public seams.
func startBooty(t *testing.T, bootDir, httpAddr, tftpAddr string) *booty {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	cat, err := catalog.DirSource{Root: catalogDir(t)}.Load(context.Background())
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	renderer, err := render.New()
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}

	rec := &recorder{}
	bootSrv, err := httpsrv.New(httpsrv.Config{
		Logger: logger, Catalog: cat, Renderer: renderer, BootDir: bootDir,
	})
	if err != nil {
		t.Fatalf("httpsrv.New: %v", err)
	}
	handler := rec.wrap(bootSrv.Handler())

	// HTTP: our own listener so we can wrap the handler and learn the real port.
	ln, err := net.Listen("tcp", httpAddr)
	if err != nil {
		t.Fatalf("http listen %s: %v", httpAddr, err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()

	// TFTP: booty's real server on a UDP socket via its Serve(conn) seam.
	pc, err := net.ListenPacket("udp", tftpAddr)
	if err != nil {
		t.Fatalf("tftp listen %s: %v", tftpAddr, err)
	}
	tctx, tcancel := context.WithCancel(context.Background())
	go func() { _ = tftp.New(tftp.Config{BootDir: bootDir, Logger: logger}).Serve(tctx, pc) }()

	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		tcancel()
		_ = pc.Close()
	})

	host := ln.Addr().String()
	return &booty{
		httpBase: "http://" + host,
		httpHost: host,
		tftpAddr: pc.LocalAddr().String(),
		rec:      rec,
	}
}

func httpGet(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// tftpReadFile is a minimal RFC 1350 read client: RRQ in octet mode, ACK every
// DATA block, stop on the first block shorter than 512 bytes. It follows the
// server's transfer ID (the new port the server replies from).
func tftpReadFile(t *testing.T, serverAddr, filename string) []byte {
	t.Helper()
	srvUDP, err := net.ResolveUDPAddr("udp", serverAddr)
	if err != nil {
		t.Fatalf("resolve %s: %v", serverAddr, err)
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("client socket: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	// RRQ: opcode 1 | filename | 0 | "octet" | 0
	rrq := []byte{0, 1}
	rrq = append(rrq, filename...)
	rrq = append(rrq, 0)
	rrq = append(rrq, "octet"...)
	rrq = append(rrq, 0)
	if _, err := conn.WriteToUDP(rrq, srvUDP); err != nil {
		t.Fatalf("send RRQ: %v", err)
	}

	var out []byte
	var tid *net.UDPAddr
	buf := make([]byte, 4+512)
	for block := uint16(1); ; block++ {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("read block %d: %v", block, err)
		}
		if tid == nil {
			tid = from // lock onto the server's transfer ID
		}
		if n < 4 {
			t.Fatalf("short packet (%d bytes)", n)
		}
		switch op := binary.BigEndian.Uint16(buf[0:2]); op {
		case 3: // DATA
			got := binary.BigEndian.Uint16(buf[2:4])
			if got != block {
				t.Fatalf("expected block %d, got %d", block, got)
			}
			out = append(out, buf[4:n]...)
			ack := []byte{0, 4, byte(block >> 8), byte(block)}
			if _, err := conn.WriteToUDP(ack, tid); err != nil {
				t.Fatalf("send ACK %d: %v", block, err)
			}
			if n-4 < 512 { // short block => EOF
				return out
			}
		case 5: // ERROR
			t.Fatalf("TFTP ERROR: %q", string(buf[4:n]))
		default:
			t.Fatalf("unexpected opcode %d", op)
		}
		if len(out) > 64<<20 { // 64 MiB: a stand-in ipxe.efi is never this big
			t.Fatal("runaway transfer")
		}
	}
}

// TestE2EProtocolReachability exercises the assembled service the way a booting
// machine does — but without a VM, so it runs anywhere. This is the composition
// test the per-package unit tests can't be: TFTP + HTTP + catalog + render wired
// exactly as `booty serve` wires them.
func TestE2EProtocolReachability(t *testing.T) {
	bootDir, kernel := stageBootDir(t)
	b := startBooty(t, bootDir, "127.0.0.1:0", "127.0.0.1:0")

	// 1. NIC ROM step: TFTP-load ipxe.efi off booty's real UDP server.
	got := tftpReadFile(t, b.tftpAddr, ipxeEFIName)
	wantIPXE, _ := os.ReadFile(filepath.Join(bootDir, ipxeEFIName))
	if !bytes.Equal(got, wantIPXE) {
		t.Fatalf("ipxe.efi over TFTP: got %d bytes, want %d", len(got), len(wantIPXE))
	}

	// 2. iPXE runs the chain script; it must carry the ${mac} placeholder.
	if code, body := httpGet(t, b.httpBase+"/boot.ipxe"); code != 200 ||
		!bytes.Contains([]byte(body), []byte("#!ipxe")) ||
		!bytes.Contains([]byte(body), []byte("${mac}")) {
		t.Fatalf("/boot.ipxe = %d, body:\n%s", code, body)
	}

	// 3. iPXE asks /ipxe with the identity the chain script supplied; the worker
	//    MAC must resolve to a boot script pointing at the kernel + initrd basename.
	code, boot := httpGet(t, b.httpBase+"/ipxe?mac="+workerMAC+"&arch=x86_64")
	if code != 200 {
		t.Fatalf("/ipxe = %d", code)
	}
	for _, want := range []string{"#!ipxe", "/boot/" + kernelPath, "initrd=initramfs.xz"} {
		if !bytes.Contains([]byte(boot), []byte(want)) {
			t.Fatalf("/ipxe missing %q:\n%s", want, boot)
		}
	}

	// 4. iPXE downloads the kernel over HTTP.
	if code, body := httpGet(t, b.httpBase+"/boot/"+kernelPath); code != 200 || body != string(kernel) {
		t.Fatalf("/boot kernel = %d, %d bytes", code, len(body))
	}

	// 5. Talos boots and pulls its machineconfig; it must be the worker's.
	code, mc := httpGet(t, b.httpBase+"/machine-config?mac="+workerMAC)
	if code != 200 {
		t.Fatalf("/machine-config = %d", code)
	}
	for _, want := range []string{"type: worker", "hostname: talos-worker-01"} {
		if !bytes.Contains([]byte(mc), []byte(want)) {
			t.Fatalf("machineconfig missing %q:\n%s", want, mc)
		}
	}

	// 6. The Proxmox automated installer POSTs its system info; the pve-01 MAC is
	//    deliberately the second NIC to exercise the most-specific-match path.
	sysInfo := `{"dmi":{"system":{"uuid":"e2e-uuid","serial":"E2E1"}},` +
		`"network_interfaces":[{"name":"eno1","mac":"aa:aa:aa:aa:aa:aa"},{"name":"eno2","mac":"` + proxmoxMAC + `"}]}`
	resp, err := http.Post(b.httpBase+"/proxmox/answer", "application/json", strings.NewReader(sysInfo))
	if err != nil {
		t.Fatalf("POST /proxmox/answer: %v", err)
	}
	answer, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/proxmox/answer = %d: %s", resp.StatusCode, answer)
	}
	for _, want := range []string{"[global]", `fqdn = "pve-01.home.local"`, `source = "from-dhcp"`} {
		if !bytes.Contains(answer, []byte(want)) {
			t.Fatalf("answer.toml missing %q:\n%s", want, answer)
		}
	}

	// The recorder must have observed the full HTTP boot chain.
	if !b.rec.sawAll("/boot.ipxe", "/ipxe", "/boot/"+kernelPath, "/machine-config", "/proxmox/answer") {
		t.Fatalf("missing requests; saw: %v", b.rec.snapshot())
	}
}

// qemuTooling resolves the QEMU tier's dependencies from the environment, or
// returns a reason to skip. A machine without qemu/OVMF/ipxe.efi skips cleanly.
type qemuTooling struct {
	qemu, ovmfCode, ipxe string
	httpPort             string
}

func resolveQEMU(t *testing.T) (qemuTooling, string) {
	t.Helper()
	qemu := os.Getenv("BOOTY_E2E_QEMU")
	if qemu == "" {
		qemu = "qemu-system-x86_64"
	}
	path, err := exec.LookPath(qemu)
	if err != nil {
		return qemuTooling{}, "qemu not found (set BOOTY_E2E_QEMU or install qemu-system-x86_64)"
	}
	ovmf := os.Getenv("BOOTY_E2E_OVMF_CODE")
	if ovmf == "" {
		return qemuTooling{}, "BOOTY_E2E_OVMF_CODE unset (path to an edk2/OVMF code .fd)"
	}
	if _, err := os.Stat(ovmf); err != nil {
		return qemuTooling{}, fmt.Sprintf("OVMF code %q not readable: %v", ovmf, err)
	}
	ipxe := os.Getenv("BOOTY_E2E_IPXE")
	if ipxe == "" {
		return qemuTooling{}, "BOOTY_E2E_IPXE unset (path to ipxe.efi whose embedded script chains to http://10.0.2.2:$PORT/boot.ipxe)"
	}
	if _, err := os.Stat(ipxe); err != nil {
		return qemuTooling{}, fmt.Sprintf("ipxe.efi %q not readable: %v", ipxe, err)
	}
	port := os.Getenv("BOOTY_E2E_HTTP_PORT")
	if port == "" {
		port = "8080" // must match the URL embedded in the ipxe.efi above
	}
	return qemuTooling{qemu: path, ovmfCode: ovmf, ipxe: ipxe, httpPort: port}, ""
}

// TestE2EQEMUBoot boots a real UEFI VM against booty and asserts that the guest
// walked booty's HTTP boot chain. The guest reaches the host at 10.0.2.2 (QEMU
// user-net gateway); its PXE ROM TFTP-loads ipxe.efi from QEMU's built-in TFTP
// (serving the provided binary), and that ipxe.efi's embedded script chains to
// http://10.0.2.2:$PORT/boot.ipxe — booty. We bind booty's HTTP to that fixed
// port so the embedded URL resolves, watch the recorder for the boot requests,
// and tear the VM down as soon as they arrive.
func TestE2EQEMUBoot(t *testing.T) {
	tool, skip := resolveQEMU(t)
	if skip != "" {
		t.Skip("QEMU tier skipped: " + skip)
	}

	bootDir, _ := stageBootDir(t)
	// QEMU's built-in TFTP serves ipxe.efi from bootDir; use the real binary.
	ipxeBytes, err := os.ReadFile(tool.ipxe)
	if err != nil {
		t.Fatalf("read ipxe.efi: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bootDir, ipxeEFIName), ipxeBytes, 0o644); err != nil {
		t.Fatalf("stage ipxe.efi: %v", err)
	}

	// Bind HTTP to the fixed port the embedded ipxe script targets; 0.0.0.0 so the
	// guest can reach it via the 10.0.2.2 gateway. TFTP is ephemeral (unused here —
	// QEMU serves ipxe.efi itself).
	b := startBooty(t, bootDir, "0.0.0.0:"+tool.httpPort, "127.0.0.1:0")

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	args := []string{
		"-machine", "q35",
		"-m", "512M",
		"-display", "none",
		"-no-reboot",
		"-serial", "stdio",
		"-drive", "if=pflash,format=raw,readonly=on,file=" + tool.ovmfCode,
		"-netdev", "user,id=net0,tftp=" + bootDir + ",bootfile=" + ipxeEFIName,
		"-device", "e1000,netdev=net0",
		"-boot", "order=n",
	}
	if vars := os.Getenv("BOOTY_E2E_OVMF_VARS"); vars != "" {
		args = append(args, "-drive", "if=pflash,format=raw,file="+vars)
	}

	cmd := exec.CommandContext(ctx, tool.qemu, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start qemu: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	// Poll until the guest has chained to booty AND begun downloading the kernel —
	// proof iPXE ran booty's script — then kill the VM and pass.
	deadline := time.Now().Add(115 * time.Second)
	for time.Now().Before(deadline) {
		if b.rec.sawAll("/boot.ipxe", "/ipxe", "/boot/"+kernelPath) {
			_ = cmd.Process.Kill()
			// The chain must precede the boot-script fetch.
			seq := b.rec.snapshot()
			if idx(seq, "/boot.ipxe") > idx(seq, "/ipxe") {
				t.Fatalf("chain script fetched after /ipxe; sequence: %v", seq)
			}
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("guest did not complete booty boot chain in time.\nrequests seen: %v\nqemu output tail:\n%s",
		b.rec.snapshot(), tailString(out.String(), 2000))
}

func idx(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
