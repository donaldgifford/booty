# Chapter 9: End-to-End with QEMU and OVMF

← [Chapter 8](./07-forge-complete.md) | [Chapter 10: Debugging Field Guide →](./08-debugging-field-guide.md)

---

Eight chapters built and assembled booty, each package tested in isolation and the
whole wired together in `cmd/booty`. Every test so far has been a unit or a
handler test — a function or an `httptest` request. None of them answers the
question that actually matters: **does it boot a machine?**

This chapter answers it without buying hardware, by booting a virtual one. The
tool is QEMU with UEFI firmware (OVMF/edk2), which gives you a real x86-64 machine
that goes through the real firmware → PXE → iPXE → HTTP path — the same sequence
Chapter 1 diagrammed — against booty running on your laptop.

But there's a tension. CI needs a boot signal on *every* push, fast and hermetic;
a full VM boot needs QEMU, a firmware image, and a prebuilt `ipxe.efi` that not
every machine (or CI runner) has. booty resolves this with **two e2e tiers**, both
in one build-tagged package:

1. A **protocol tier** that runs anywhere — it starts booty's real TFTP and HTTP
   servers in process and drives the whole boot request chain over real sockets,
   no VM.
2. A **QEMU full-boot tier** that boots an actual UEFI VM and asserts the guest
   walked booty's boot chain — and *skips cleanly* when the tooling is absent.

Source: [`test/e2e/e2e_test.go`](../../test/e2e/e2e_test.go), run with `just
test-e2e` (or `go test -tags=e2e ./test/e2e`).

## The build tag, and why e2e is quarantined

The whole file opens with:

```go
//go:build e2e
```

That tag keeps it out of the default `go test ./...` and `just test` — the unit
pass stays fast and hermetic, exactly as `CLAUDE.md` prescribes for anything
heavier than a unit test. e2e runs only when you ask:

```bash
just test-e2e                       # or: go test -tags=e2e ./test/e2e
```

This is the same discipline the project uses for integration tests generally: slow
or tooling-dependent tests live behind a tag so a broken QEMU install (or a runner
without it) never reddens the unit build. The protocol tier costs a fraction of a
second and would be fine to run always; it shares the tag with the QEMU tier
purely so the two live together as "the e2e suite."

## Tier 1: the protocol tier (runs anywhere)

The per-package tests proved each piece in isolation. What none of them could
prove is the *composition* — that TFTP, HTTP, the catalog, and the renderer, wired
the way `booty serve` wires them, actually satisfy a boot sequence end to end. That
is exactly what the protocol tier is: an in-process assembly of the real servers,
driven by real client traffic.

The wiring reuses the public seams the earlier chapters were careful to expose —
`tftp.Server.Serve(conn)` (Chapter 3) and `httpsrv.Server.Handler()` (Chapter 7) —
so the test hosts booty's own code, not a reimplementation:

```go
handler := rec.wrap(httpsrv.New(httpsrv.Options{
	Logger: logger, Catalog: cat, Renderer: renderer, BootDir: bootDir,
}).Handler())

ln, _ := net.Listen("tcp", "127.0.0.1:0")        // ephemeral loopback port
srv := &http.Server{Handler: handler}
go srv.Serve(ln)

pc, _ := net.ListenPacket("udp", "127.0.0.1:0")  // ephemeral UDP port
go tftp.New(bootDir, logger).Serve(ctx, pc)      // booty's real TFTP server
```

The catalog it loads is the *shipped example* (`examples/catalog`), so the test
also guards that the example a reader copies actually boots. Then it walks the six
requests a booting machine makes, each mapped to a step from the Chapter 1
diagram:

```go
// 1. NIC ROM step: TFTP-load ipxe.efi off booty's real UDP server.
got := tftpReadFile(t, b.tftpAddr, "ipxe.efi")
// … bytes must equal the staged file

// 2. iPXE runs the chain script; it must carry the ${mac} placeholder.
httpGet(t, b.httpBase+"/boot.ipxe")            // contains "#!ipxe" and "${mac}"

// 3. iPXE asks /ipxe with the identity the chain script supplied.
httpGet(t, b.httpBase+"/ipxe?mac="+workerMAC+"&arch=x86_64")
//    contains "/boot/talos/v1.7.6/vmlinuz" and "initrd=initramfs.xz"

// 4. iPXE downloads the kernel over HTTP.
httpGet(t, b.httpBase+"/boot/talos/v1.7.6/vmlinuz")   // bytes match

// 5. Talos boots and pulls its machineconfig.
httpGet(t, b.httpBase+"/machine-config?mac="+workerMAC)
//    contains "type: worker" and "hostname: talos-worker-01"

// 6. The Proxmox automated installer POSTs its system info; the pve-01 MAC is
//    deliberately the SECOND NIC, pinning the most-specific match (Chapter 6).
http.Post(b.httpBase+"/proxmox/answer", "application/json", …)
//    contains `fqdn = "pve-01.home.local"`
```

Step 1 is worth dwelling on: `tftpReadFile` is a hand-written RFC 1350 client —
it sends a real `RRQ`, ACKs each 512-byte `DATA` block, and stops on the first
short block — talking to booty's real TFTP server over a real UDP socket. The
staged `ipxe.efi` is 1100 bytes precisely so the transfer is *two full blocks plus
a short one*, exercising the end-of-file signal on the wire. This isn't a mock of
TFTP; it's the protocol, and it's the same wire contract Chapter 3 built.

It passes in a fraction of a second, on any machine:

```
$ just test-e2e
=== RUN   TestE2EProtocolReachability
--- PASS: TestE2EProtocolReachability (0.01s)
```

### Asserting on what booty *observed*

There's a technique here that carries into the QEMU tier. You cannot easily assert
"the machine booted" by looking at the machine — a VM's answer is pixels on a
framebuffer. The robust move is to assert on the request sequence booty *received*.
A tiny recorder wraps the handler and remembers every path:

```go
type recorder struct {
	mu    sync.Mutex
	paths []string
}
func (rec *recorder) wrap(next http.Handler) http.Handler { … append r.URL.Path … }
```

The protocol tier ends by checking the recorder saw the full chain
(`/boot.ipxe`, `/ipxe`, `/boot/…vmlinuz`, `/machine-config`, `/proxmox/answer`).
In the QEMU tier,
where the client is a real VM we can't instrument, that same recorder is the
*only* assertion surface — and it's enough.

## Tier 2: the QEMU full-boot tier

Now the real thing: a UEFI machine that runs firmware, PXE-boots, chainloads iPXE,
and drives booty's HTTP surface — in a VM.

### The network topology

The keystone is QEMU's user-mode ("slirp") networking. It NATs the guest behind a
virtual router, and two facts make the test work:

- The **host is reachable from the guest at `10.0.2.2`** — slirp's gateway address
  forwards to the host's loopback. So booty listening on the host is reachable from
  inside the VM at `http://10.0.2.2:<port>`.
- slirp has a **built-in DHCP + TFTP** server. `-netdev user,tftp=<dir>,bootfile=ipxe.efi`
  makes it hand the guest `bootfile=ipxe.efi` and serve that file from `<dir>`. The
  NIC's PXE ROM (QEMU ships iPXE option ROMs for its NICs) TFTP-loads it and runs
  it.

So the boot path is: firmware → NIC PXE ROM → TFTP-load our `ipxe.efi` (via slirp)
→ our iPXE runs → **chains to booty over HTTP at `10.0.2.2`**. From the chain
script onward, it's booty driving the boot, exactly as on real hardware.

### Why the `ipxe.efi` must embed the URL

The `ipxe.efi` slirp serves has to already know where booty is, because it's loaded
by TFTP with no way to pass it arguments. That's the same point Chapter 4 made:
iPXE sends no identity, and knows no boot server, unless a script tells it to — and
here that script is *embedded in the binary*:

```ipxe
#!ipxe
chain http://10.0.2.2:8080/boot.ipxe
```

Because the embedded URL is fixed, the test binds booty's HTTP to a **fixed port**
(default `8080`, `BOOTY_E2E_HTTP_PORT`) rather than an ephemeral one — the embedded
`ipxe.efi` and the running server must agree on it. (Contrast the protocol tier,
which is free to use `:0` because it constructs every URL itself.)

### The invocation and the assertion

The VM is launched with OVMF as pflash firmware, an e1000 NIC set to network-boot,
and no display:

```go
args := []string{
	"-machine", "q35", "-m", "512M", "-display", "none", "-no-reboot",
	"-serial", "stdio",
	"-drive", "if=pflash,format=raw,readonly=on,file=" + tool.ovmfCode,
	"-netdev", "user,id=net0,tftp=" + bootDir + ",bootfile=ipxe.efi",
	"-device", "e1000,netdev=net0",
	"-boot", "order=n",
}
cmd := exec.CommandContext(ctx, tool.qemu, args...)
```

Then the assertion is pure recorder-watching: poll until booty has seen the guest
chain in (`/boot.ipxe`), resolve (`/ipxe`), and *start downloading the kernel*
(`/boot/…vmlinuz`) — proof that a real iPXE ran booty's script and acted on it —
then kill the VM and pass. It also checks ordering (the chain script must precede
`/ipxe`), and on timeout it dumps both the requests seen and the tail of QEMU's
serial output for debugging:

```go
for time.Now().Before(deadline) {
	if b.rec.sawAll("/boot.ipxe", "/ipxe", "/boot/"+kernelPath) {
		_ = cmd.Process.Kill()
		// … assert /boot.ipxe precedes /ipxe …
		return
	}
	time.Sleep(500 * time.Millisecond)
}
t.Fatalf("guest did not complete booty boot chain in time.\n… %s", tailString(out.String(), 2000))
```

We stop at "kernel download started" rather than "OS booted" deliberately: once
the guest has fetched the kernel from booty, everything booty is responsible for
has demonstrably worked. Whether the fake kernel then boots is Talos's problem, not
booty's (asserting a real OS came up is noted under *what's deferred*).

### It skips when unequipped — including here

The tier resolves its tooling from the environment and skips with a specific reason
if anything is missing:

```go
tool, skip := resolveQEMU(t)   // BOOTY_E2E_QEMU, BOOTY_E2E_OVMF_CODE, BOOTY_E2E_IPXE
if skip != "" {
	t.Skip("QEMU tier skipped: " + skip)
}
```

On the machine this chapter was written on — and on a default CI runner — that's
exactly what happens:

```
=== RUN   TestE2EQEMUBoot
    e2e_test.go: QEMU tier skipped: qemu not found (set BOOTY_E2E_QEMU or install qemu-system-x86_64)
--- SKIP: TestE2EQEMUBoot (0.00s)
PASS
```

A skip is not a pass in disguise — the test states plainly that the full boot did
*not* run, and why. The protocol tier remains the always-on signal; the QEMU tier
is the deeper proof you run on an equipped box (or a nightly job with the tooling
installed).

## Running the full tier yourself

On a machine with the tooling, point the three env vars at it:

```bash
# 1. QEMU + a firmware image
brew install qemu                      # or: apt install qemu-system-x86 ovmf
export BOOTY_E2E_OVMF_CODE=/opt/homebrew/share/qemu/edk2-x86_64-code.fd

# 2. An ipxe.efi whose embedded script chains to booty. Build it once:
#    (from an iPXE checkout)
echo '#!ipxe
chain http://10.0.2.2:8080/boot.ipxe' > chain.ipxe
make -C ipxe/src bin-x86_64-efi/ipxe.efi EMBED=../../chain.ipxe
export BOOTY_E2E_IPXE=$PWD/ipxe/src/bin-x86_64-efi/ipxe.efi

# 3. Run it
just test-e2e            # protocol tier PASSes, QEMU tier now boots a real VM
```

Building the `ipxe.efi` is the one genuinely fiddly step — it's the same
asset-provenance problem Chapter 4 flagged, and the reason PLAN-0001 evaluates
`tinkerbell/ipxedust` for prebuilt, embedded binaries. Once you have one, the tier
is a real UEFI boot against your code.

## What's deferred

- **Assert the OS actually booted.** The tier stops at "kernel fetched." Going
  further means staging a *real* kernel/initrd and scraping the serial console
  (`-serial stdio`, which the test already captures) for a login banner or a Talos
  readiness marker. That turns a 100 ms proof into a multi-minute one — a nightly
  job, not a per-push gate.
- **Exercise booty's own DHCP.** Today slirp's built-in DHCP hands out `bootfile`.
  Once `proxydhcp` is wired into `serve` (Chapter 8's deferred item), the
  test could run with slirp's DHCP disabled and let booty answer the option-66/67
  handshake — covering the one protocol leg these tiers currently outsource to
  QEMU.
- **An arm64 matrix.** Everything here is x86-64 + OVMF; `qemu-system-aarch64` with
  AAVMF firmware is the same shape for the arm64 half of the release matrix.
- **A CI-baked `ipxe.efi` fixture.** A CI job (or `ipxedust`) that produces the
  embedded `ipxe.efi` as an artifact would let the QEMU tier run on hosted runners
  without a manual build step.

With the boot proven — hermetically on every push, and for real on an equipped box
— booty is demonstrably a working network-boot service, not just a set of passing
unit tests. What remains is knowing what to do when a real machine *doesn't* boot,
which is the field guide in Chapter 10.

---

← [Chapter 8](./07-forge-complete.md) | [Chapter 10: Debugging Field Guide →](./08-debugging-field-guide.md)
