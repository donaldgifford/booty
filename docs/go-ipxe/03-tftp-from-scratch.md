# Chapter 3: TFTP From Scratch

← [Chapter 2](./02-dhcp-and-pxe.md) |
[Chapter 4: iPXE Deep Dive →](./04-ipxe-deep-dive.md)

---

This is the first chapter where we build a real, running piece of `booty`. By
the end you will have `tftp` — a read-only TFTP server that a genuine
UEFI PXE client (or the `tftp` binary on your laptop) can pull a file from —
plus a test suite that drives it over a real UDP socket. Everything here is
standard library only.

The full source is [`tftp/tftp.go`](../../tftp/tftp.go) and
its tests are [`tftp/tftp_test.go`](../../tftp/tftp_test.go).
This chapter is the annotated walk-through; the code is the source of truth, and
the two are meant to be read side by side.

## Where TFTP sits in the boot

Recall the state machine from Chapter 1:

```text
POWER ON → DHCP → TFTP → IPXE_RUNNING → KERNEL_DOWNLOAD → CLOUD_INIT → DONE
                   ▲
                   └── you are here
```

TFTP is the _only_ protocol the NIC firmware can speak to fetch a file. After
DHCP hands it `next-server` + `filename`, the firmware has an IP and a target
but no TCP stack — TCP (and therefore HTTP) is far too much code to bake into a
NIC option ROM. TFTP over UDP is small enough to fit. So the firmware's entire
job is: TFTP-download one file — the iPXE binary — and jump to it. From that
point on iPXE takes over and everything else moves to HTTP (Chapters 4–7).

`booty` therefore only ever needs a **read-only** TFTP server, and in practice
it serves exactly one kind of file: the iPXE binary (`ipxe.efi`, `snponly.efi`,
`undionly.kpxe`). We reject writes outright.

> **Why build this from scratch?** In production you might reach for
> [`pin/tftp`](https://github.com/pin/tftp) — and PLAN-0001 evaluates exactly
> that (its accept criterion is correct blksize negotiation against real UEFI
> clients). The point of building it once by hand is the mental model: when a
> node hangs after DHCP and never requests `ipxe.efi`, you need to know whether
> to suspect the DHCP `filename`, the TFTP port, or the firewall — and that
> intuition comes from having implemented the wire format, not from having
> imported it.

## RFC 1350 in full

TFTP is tiny. A read-only server needs five packet types:

| Type  | Opcode | Direction       | Use                                |
| ----- | ------ | --------------- | ---------------------------------- |
| RRQ   | 1      | client → server | Read request — "send me this file" |
| DATA  | 3      | server → client | One block of file data             |
| ACK   | 4      | client → server | Acknowledge a block (or an OACK)   |
| ERROR | 5      | either          | Terminal error; ends the transfer  |
| OACK  | 6      | server → client | Option acknowledgement (RFC 2347)  |

WRQ (opcode 2, write request) exists but we answer it with an ERROR. All
integers are big-endian (network byte order).

**RRQ** — the request. Filename and mode are NUL-terminated ASCII; options are
optional NUL-terminated key/value pairs appended after the mode:

```text
 2 bytes    string   1 byte   string   1 byte   (string 1byte string 1byte)*
┌────────┬──────────┬──────┬────────┬──────┬────────────────────────────────┐
│ 0x0001 │ filename │  \0  │  mode  │  \0  │ blksize \0 1468 \0 tsize \0 0… │
└────────┴──────────┴──────┴────────┴──────┴────────────────────────────────┘
```

`mode` is always `octet` (raw binary). The historical `netascii` and `mail`
modes are dead; we reject anything that isn't `octet`.

**DATA** — opcode, 16-bit block number (starts at 1), then 0–`blksize` bytes:

```text
 2 bytes    2 bytes    n bytes
┌────────┬──────────┬──────────┐
│ 0x0003 │  block#  │   data   │
└────────┴──────────┴──────────┘
```

**ACK** — opcode and the block number being acknowledged. Block 0 acknowledges
an OACK.

```text
 2 bytes    2 bytes
┌────────┬──────────┐
│ 0x0004 │  block#  │
└────────┴──────────┘
```

**ERROR** — a code and a NUL-terminated human message. Codes: 0 not-defined, 1
file-not-found, 2 access-violation, 3 disk-full, 4 illegal-operation.

```text
 2 bytes    2 bytes    string   1 byte
┌────────┬──────────┬─────────┬──────┐
│ 0x0005 │  errcode │ message │  \0  │
└────────┴──────────┴─────────┴──────┘
```

Our packet builders are one-liners that map straight onto these diagrams:

```go
func buildDATA(block uint16, data []byte) []byte {
	pkt := make([]byte, 4+len(data))
	binary.BigEndian.PutUint16(pkt[0:2], opDATA)
	binary.BigEndian.PutUint16(pkt[2:4], block)
	copy(pkt[4:], data)
	return pkt
}
```

## The transfer, and the TID model

The single most important — and most surprising — thing about TFTP is that a
transfer uses **two different server ports**. The exchange:

```text
client:X → server:69   RRQ "ipxe.efi" octet          (the well-known port)
server:Y → client:X    DATA block 1                   (Y is a NEW ephemeral port!)
client:X → server:Y    ACK 1
server:Y → client:X    DATA block 2
client:X → server:Y    ACK 2
   … stop-and-wait, one block in flight at a time …
server:Y → client:X    DATA block N  (< blksize)      (the short block = EOF)
client:X → server:X    ACK N
```

Port 69 receives _only_ the initial RRQ. The server then allocates a fresh
ephemeral socket (port Y) and runs the whole transfer from there. The pair
`(client IP+port, server IP+port)` is the transfer's **TID** (Transfer
Identifier). This is not an optimization — it's how the protocol lets port 69
stay free to accept new requests while transfers are in flight, without any
connection table.

Two consequences fall out of this that bite people constantly:

1. **Stateful firewalls need a TFTP ALG.** The reply comes from a port the
   firewall never saw the client talk to. Without a TFTP-aware helper, the
   return DATA looks like unsolicited inbound traffic and gets dropped. This is
   the #1 cause of "DHCP works, TFTP silently doesn't."
2. **The client must accept the port change.** It sends the RRQ to `:69` but
   must latch onto whatever port the first reply comes _from_ and send all
   subsequent ACKs there. Our test client does exactly this (`tid = addr` on the
   first read).

In our code, the port change is this, inside `handleRRQ`:

```go
// Every transfer gets its own socket on an OS-chosen ephemeral port. This is
// the TFTP TID model, and it is why stateful firewalls need a TFTP ALG.
xferConn, err := net.ListenPacket("udp", ":0")   // :0 = OS picks the port
```

and the main accept loop in `Serve` spawns a goroutine per request so port 69
never blocks:

```go
n, clientAddr, err := conn.ReadFrom(buf)
// … copy buf[:n] because ReadFrom reuses it …
go s.handleRequest(pkt, clientAddr)
```

## How end-of-file is signaled

There is no length field and no explicit FIN. **A DATA block shorter than the
negotiated block size is the EOF marker.** The receiver keeps reading blocks
until one arrives with fewer than `blksize` bytes.

The subtle case: what if the file is an _exact multiple_ of the block size? Then
the last "real" block is full-sized and looks like more is coming — so the
server must send one final **zero-length** DATA block to terminate. Our
`sendFile` gets this right for free because the loop condition tests the block
it just read, not the file position:

```go
isLast := n < blockSize            // a partial OR empty block ends the transfer
if err := sendWithRetry(conn, client, buildDATA(blockNum, buf[:n]), blockNum); err != nil {
	return err
}
if isLast {
	return nil
}
```

For a 512-byte file at `blksize=512`: block 1 carries 512 bytes (`isLast`
false), then `io.ReadFull` returns `io.EOF` with `n == 0`, we send an empty
block 2 (`isLast` true), done. `TestTransferSizes` pins this with an `exact_512`
and an `exact_1024` case precisely because it's the classic off-by-one.

## Option negotiation (RFC 2347/2348/2349)

Default TFTP is brutal for large files: 512-byte blocks, one round trip each. An
80 MB initrd is 163,840 round trips of stop-and-wait. Three options fix this:

- **`blksize`** (RFC 2348) — negotiate a larger block, 8…65464 bytes. At
  `blksize=1468` (Ethernet MTU minus IP+UDP+TFTP headers, so no fragmentation)
  that 80 MB initrd is ~57,000 blocks; at 65464 it's ~1,280. This is why iPXE
  always asks for a big block.
- **`tsize`** (RFC 2349) — the client sends `tsize 0` to ask the server to fill
  in the real file size, so it can show a progress bar and pre-allocate.
- **`timeout`** — negotiate the retransmit timeout.

When an RRQ carries _any_ options, the server must reply with an **OACK** — not
a DATA block — listing only the options it accepts (omitting one means
"declined"). The client confirms with `ACK 0`, and only then does DATA block 1
flow:

```text
client → server   RRQ "ipxe.efi" octet blksize 1468 tsize 0
server → client   OACK blksize 1468 tsize 1138688
client → server   ACK 0
server → client   DATA block 1   (up to 1468 bytes)
```

`buildOACK` echoes back only what we honor, and never invents an option the
client didn't ask for — `TestBuildOACK` asserts exactly that:

```go
if _, ok := opts["blksize"]; ok {
	writeOpt("blksize", strconv.Itoa(negotiatedBlockSize))
}
if _, ok := opts["tsize"]; ok {
	writeOpt("tsize", strconv.FormatInt(fileSize, 10))
}
```

## The server, method by method

**Construction and the testable seam.** `New` takes the boot directory and a
logger (nil → `slog.Default`, so library callers aren't forced to thread one).
The important design choice is that `Serve` takes an already-bound
`net.PacketConn`, and `ListenAndServe` is a thin wrapper that binds and calls
it:

```go
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("tftp listen %s: %w", addr, err)
	}
	return s.Serve(ctx, conn)
}
```

That split is what makes the server drive-testable: a test binds `127.0.0.1:0`,
reads the real port back from `conn.LocalAddr()`, and hands the conn to `Serve`.
No fixed ports, no `time.Sleep`-and-hope.

**Shutdown via context.** UDP `ReadFrom` blocks with no built-in cancellation,
so we cancel by closing the socket out from under it:

```go
go func() {
	<-ctx.Done()
	_ = conn.Close()          // unblocks ReadFrom with net.ErrClosed
}()
// …
if err != nil {
	if ctx.Err() != nil {
		return nil            // cancelled → clean shutdown, not an error
	}
	return fmt.Errorf("tftp read: %w", err)
}
```

**`sendWithRetry` — the part the toy version gets wrong.** This sends a packet
and waits for the matching ACK, retransmitting on timeout. Three details matter,
and the original from-scratch sketch missed all three:

1. **Verify the sender.** UDP is connectionless; _any_ host can send a datagram
   to our transfer port. If we blindly treated the next packet as "the ACK," an
   off-path (or on-path) packet could desync or hijack the transfer. We compare
   the source address to the client's TID and drop mismatches:

   ```go
   if addr.String() != client.String() || n < 4 {
       continue // stray or runt packet
   }
   ```

2. **A buffer big enough for an ERROR.** The client can answer with an ERROR
   carrying a message, not just a 4-byte ACK. A 4-byte read buffer truncates it
   and loses the reason. We use 516 bytes and surface the message:

   ```go
   case opERROR:
       return fmt.Errorf("client error %d: %s",
           binary.BigEndian.Uint16(buf[2:4]), errMessage(buf[4:n]))
   ```

3. **Don't retransmit on a duplicate ACK.** If we resent DATA every time a
   stale/duplicate ACK arrived, one duplicate would trigger a resend, whose ACK
   is another duplicate, and so on — the _Sorcerer's Apprentice_ bug (RFC 1123
   §4.2), a self-sustaining packet storm. We ignore non-matching ACKs and keep
   waiting within the current deadline; only a real timeout retransmits.

**Path safety.** `resolvePath` prefixes the request with `/` before
`filepath.Clean`, which collapses any `..` so a request like `../../etc/passwd`
resolves _under_ the boot dir (and thus 404s) instead of escaping it. An
explicit prefix check backs it up as defense in depth:

```go
clean := filepath.Clean("/" + strings.TrimPrefix(filename, "/"))
fullAbs, _ := filepath.Abs(filepath.Join(bootDirAbs, clean))
if fullAbs != bootDirAbs && !strings.HasPrefix(fullAbs, bootDirAbs+string(filepath.Separator)) {
	return "", fmt.Errorf("path traversal: %q", filename)
}
```

(Note: this guards path _string_ traversal, not symlink escape — if the boot dir
contains attacker-controlled symlinks you have a bigger problem. For `booty`'s
threat model the boot dir is operator-curated.)

## Testing it — a real client over a real socket

The tests don't mock the network; they open a UDP socket and speak TFTP. The
helper `tftpGet` is a minimal-but-correct client: it sends the RRQ, latches onto
the server's TID from the first reply, honors an OACK, ACKs every block, and
reassembles the file. That single helper exercises the port change, option
negotiation, and the stop-and-wait loop in one path.

The suite covers the behaviors that actually break in the field:

| Test                        | What it proves                                                            |
| --------------------------- | ------------------------------------------------------------------------- |
| `TestTransferSizes`         | empty / short / exact-multiple / multi-block all round-trip byte-for-byte |
| `TestBlksizeNegotiation`    | OACK path: 5000 bytes at `blksize=1024` negotiates and transfers          |
| `TestFileNotFound`          | missing file → ERROR code 1                                               |
| `TestPathTraversalRejected` | `../secret.txt` cannot read a file outside the boot dir                   |
| `TestWRQRejected`           | write request → access-violation ERROR                                    |
| `TestParseRRQ`              | RRQ parsing with and without options, and malformed input                 |
| `TestBuildOACK`             | server echoes only requested options, with correct values                 |

Run them:

```bash
go test ./tftp/...           # fast
go test -race ./tftp/...     # what CI runs (`just test`)
```

## Try it yourself

This is the payoff — a working server you can pull a file from with a stock TFTP
client:

```bash
# Build the binary
go build -o bin/booty ./cmd/booty

# A boot dir with something to serve
mkdir -p /tmp/boot && echo 'hello from booty tftp' > /tmp/boot/hello.txt

# Serve on a non-privileged port (port 69 needs root)
./bin/booty serve --tftp-addr 127.0.0.1:6969 --boot-dir /tmp/boot &

# Fetch it with your OS's tftp client — note it targets :6969
cd /tmp && printf 'binary\nget hello.txt\nquit\n' | tftp 127.0.0.1 6969
cat /tmp/hello.txt          # -> hello from booty tftp
```

Watch the TID port change on the wire while you do it:

```bash
sudo tcpdump -i lo0 -n -v 'udp port 6969 or (udp and src port 6969)'
# RRQ goes to :6969; DATA comes back from a different, ephemeral port.
```

For production you'll bind `:69` (needs root or `CAP_NET_BIND_SERVICE`) and
point your DHCP `next-server`/`filename` at it, as configured in Chapter 2.

## What we deliberately left out

- **WRQ / uploads** — `booty` is read-only by design; there is nothing a booting
  node needs to write back over TFTP.
- **`netascii` / `mail` modes** — historical; `octet` is universal.
- **Windowsize (RFC 7440)** — a real throughput win (multiple blocks in flight
  before an ACK), but not universally supported by PXE firmware, and moot for
  `booty` because we only serve the small iPXE binary over TFTP and everything
  large moves to HTTP.
- **Multicast TFTP** — for booting many identical nodes at once; out of scope.

If a future requirement makes TFTP throughput matter, this is one of the places
PLAN-0001's admission test says to lean on `pin/tftp` rather than grow our own —
it has already eaten the quirks of windowsize and odd client behavior. For now,
having built it, we own the mental model, which was the point.

---

← [Chapter 2](./02-dhcp-and-pxe.md) |
[Chapter 4: iPXE Deep Dive →](./04-ipxe-deep-dive.md)
