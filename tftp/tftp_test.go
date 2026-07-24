package tftp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// quietLogger discards output so test runs stay readable.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startServer binds the server to a loopback ephemeral port and returns its
// address. It is torn down when the test ends.
func startServer(t *testing.T, bootDir string) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind server: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = New(bootDir, quietLogger()).Serve(ctx, conn)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return conn.LocalAddr().String()
}

// buildRRQ constructs a read request: opcode, filename\0mode\0[opt\0val\0]...
func buildRRQ(filename, mode string, opts map[string]string) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint16(opRRQ))
	b.WriteString(filename)
	b.WriteByte(0)
	b.WriteString(mode)
	b.WriteByte(0)
	for k, v := range opts {
		b.WriteString(k)
		b.WriteByte(0)
		b.WriteString(v)
		b.WriteByte(0)
	}
	return b.Bytes()
}

func parseOACKOpts(body []byte) map[string]string {
	out := map[string]string{}
	parts := bytes.Split(bytes.TrimRight(body, "\x00"), []byte{0})
	for i := 0; i+1 < len(parts); i += 2 {
		out[string(parts[i])] = string(parts[i+1])
	}
	return out
}

// tftpGet is a minimal correct TFTP client: it performs the RRQ, follows the
// server's TID (port) change, negotiates options via OACK, ACKs every block, and
// reassembles the file. It returns the bytes, the negotiated block size, and any
// TFTP-level error the server reported.
func tftpGet(t *testing.T, serverAddr, filename string, opts map[string]string) ([]byte, int, error) {
	t.Helper()
	raddr, err := net.ResolveUDPAddr("udp", serverAddr)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	cl, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("client bind: %v", err)
	}
	defer func() { _ = cl.Close() }()

	if _, err := cl.WriteTo(buildRRQ(filename, "octet", opts), raddr); err != nil {
		t.Fatalf("send RRQ: %v", err)
	}

	blksize := defaultBlockSize
	var out []byte
	var tid net.Addr // server's transfer port, learned from the first reply
	want := uint16(1)
	buf := make([]byte, maxBlockSize+4)

	ack := func(block uint16) {
		p := make([]byte, 4)
		binary.BigEndian.PutUint16(p[0:2], opACK)
		binary.BigEndian.PutUint16(p[2:4], block)
		_, _ = cl.WriteTo(p, tid)
	}

	for {
		_ = cl.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, addr, err := cl.ReadFrom(buf)
		if err != nil {
			return nil, blksize, err
		}
		tid = addr
		switch binary.BigEndian.Uint16(buf[:2]) {
		case opERROR:
			return nil, blksize, &tftpError{
				code: binary.BigEndian.Uint16(buf[2:4]),
				msg:  errMessage(buf[4:n]),
			}
		case opOACK:
			if bs, ok := parseOACKOpts(buf[2:n])["blksize"]; ok {
				blksize, _ = strconv.Atoi(bs)
			}
			ack(0)
		case opDATA:
			block := binary.BigEndian.Uint16(buf[2:4])
			if block != want {
				t.Fatalf("out-of-order block: want %d got %d", want, block)
			}
			payload := buf[4:n]
			out = append(out, payload...)
			ack(block)
			if len(payload) < blksize {
				return out, blksize, nil
			}
			want++
		}
	}
}

type tftpError struct {
	code uint16
	msg  string
}

func (e *tftpError) Error() string { return "tftp error " + strconv.Itoa(int(e.code)) + ": " + e.msg }

// writeBootFile creates bootDir/name with the given contents.
func writeBootFile(t *testing.T, bootDir, name string, data []byte) {
	t.Helper()
	p := filepath.Join(bootDir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// deterministic pseudo-random payload of length n (no reliance on math/rand).
func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((i*31 + 7) % 251)
	}
	return b
}

func TestTransferSizes(t *testing.T) {
	// Cover: shorter than one block, exactly one block (forces a trailing empty
	// block), and several blocks with a partial tail.
	cases := map[string]int{
		"empty":         0,
		"short":         100,
		"exact_512":     512,
		"exact_1024":    1024,
		"partial_multi": 1500,
	}
	dir := t.TempDir()
	addr := startServer(t, dir)

	for name, size := range cases {
		t.Run(name, func(t *testing.T) {
			want := payload(size)
			writeBootFile(t, dir, name+".bin", want)

			got, _, err := tftpGet(t, addr, name+".bin", nil)
			if err != nil {
				t.Fatalf("transfer: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("payload mismatch: got %d bytes, want %d", len(got), len(want))
			}
		})
	}
}

func TestBlksizeNegotiation(t *testing.T) {
	dir := t.TempDir()
	addr := startServer(t, dir)
	want := payload(5000)
	writeBootFile(t, dir, "big.bin", want)

	got, blksize, err := tftpGet(t, addr, "big.bin", map[string]string{"blksize": "1024", "tsize": "0"})
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if blksize != 1024 {
		t.Fatalf("negotiated blksize = %d, want 1024", blksize)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("payload mismatch: got %d bytes, want %d", len(got), len(want))
	}
}

func TestFileNotFound(t *testing.T) {
	addr := startServer(t, t.TempDir())
	_, _, err := tftpGet(t, addr, "nope.bin", nil)
	var te *tftpError
	if !asTFTP(err, &te) || te.code != errFileNotFound {
		t.Fatalf("want file-not-found error, got %v", err)
	}
}

func TestPathTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	// A secret sitting next to (outside) the boot dir.
	secret := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(secret) })

	addr := startServer(t, dir)
	got, _, err := tftpGet(t, addr, "../secret.txt", nil)
	if err == nil {
		t.Fatalf("expected traversal to fail, but got %q", got)
	}
	if bytes.Contains(got, []byte("top secret")) {
		t.Fatalf("traversal leaked the secret file")
	}
}

func TestWRQRejected(t *testing.T) {
	addr := startServer(t, t.TempDir())
	raddr, _ := net.ResolveUDPAddr("udp", addr)
	cl, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer func() { _ = cl.Close() }()

	wrq := make([]byte, 0, 32)
	wrq = binary.BigEndian.AppendUint16(wrq, opWRQ)
	wrq = append(wrq, "upload.bin\x00octet\x00"...)
	if _, err := cl.WriteTo(wrq, raddr); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 128)
	_ = cl.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := cl.ReadFrom(buf)
	if err != nil {
		t.Fatalf("no reply to WRQ: %v", err)
	}
	if op := binary.BigEndian.Uint16(buf[:2]); op != opERROR {
		t.Fatalf("want ERROR opcode, got %d", op)
	}
	if code := binary.BigEndian.Uint16(buf[2:4]); code != errAccessViolation {
		t.Fatalf("want access-violation, got code %d (%q)", code, errMessage(buf[4:n]))
	}
}

// --- unit tests for the wire-format helpers ---

func TestParseRRQ(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		wantFile string
		wantMode string
		wantOpts map[string]string
		wantErr  bool
	}{
		{
			name:     "no options",
			payload:  "ipxe.efi\x00octet\x00",
			wantFile: "ipxe.efi",
			wantMode: "octet",
			wantOpts: map[string]string{},
		},
		{
			name:     "with options",
			payload:  "ipxe.efi\x00octet\x00blksize\x001468\x00tsize\x000\x00",
			wantFile: "ipxe.efi",
			wantMode: "octet",
			wantOpts: map[string]string{"blksize": "1468", "tsize": "0"},
		},
		{
			name:    "missing mode",
			payload: "ipxe.efi\x00",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, mode, opts, err := parseRRQ([]byte(tt.payload))
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if file != tt.wantFile || mode != tt.wantMode {
				t.Fatalf("got (%q,%q), want (%q,%q)", file, mode, tt.wantFile, tt.wantMode)
			}
			if len(opts) != len(tt.wantOpts) {
				t.Fatalf("opts = %v, want %v", opts, tt.wantOpts)
			}
			for k, v := range tt.wantOpts {
				if opts[k] != v {
					t.Fatalf("opt %q = %q, want %q", k, opts[k], v)
				}
			}
		})
	}
}

func TestResolvePathTraversal(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, quietLogger())
	abs, _ := filepath.Abs(dir)

	tests := []struct {
		in      string
		wantErr bool
	}{
		{"ipxe.efi", false},
		{"talos/v1.7.0/vmlinuz", false},
		{"/ipxe.efi", false},
		{"../../etc/passwd", false}, // collapses under bootDir → later 404s, not an escape
		{"..", false},               // resolves to bootDir itself
	}
	for _, tt := range tests {
		got, err := s.resolvePath(tt.in)
		if (err != nil) != tt.wantErr {
			t.Fatalf("resolvePath(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
		if err == nil && got != abs && !hasPrefix(got, abs) {
			t.Fatalf("resolvePath(%q) = %q escaped bootDir %q", tt.in, got, abs)
		}
	}
}

func TestBuildOACK(t *testing.T) {
	// Client offered blksize + tsize; server must echo blksize with its chosen
	// value and tsize with the real file size, and must NOT invent options.
	oack := buildOACK(map[string]string{"blksize": "1468", "tsize": "0"}, 1468, 4096)
	if op := binary.BigEndian.Uint16(oack[:2]); op != opOACK {
		t.Fatalf("opcode = %d, want OACK", op)
	}
	opts := parseOACKOpts(oack[2:])
	if opts["blksize"] != "1468" {
		t.Fatalf("blksize = %q, want 1468", opts["blksize"])
	}
	if opts["tsize"] != "4096" {
		t.Fatalf("tsize = %q, want 4096", opts["tsize"])
	}
	if _, ok := opts["timeout"]; ok {
		t.Fatal("server offered timeout it was never asked for")
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func asTFTP(err error, target **tftpError) bool {
	return errors.As(err, target)
}
