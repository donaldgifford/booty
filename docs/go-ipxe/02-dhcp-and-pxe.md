# Chapter 2: DHCP and PXE — The Handoff Protocol

← [Chapter 1](./01-boot-sequence.md) | [Chapter 3: TFTP From Scratch →](./03-tftp-from-scratch.md)

---

A booting machine's very first question is "who am I and what do I boot?" — and it
asks it over DHCP before it has an IP, a stack, or any idea booty exists. This
chapter is about that handoff: the DHCP options that carry boot instructions, and
how booty answers them **without running a DHCP server**, via a spec-correct PXE
proxyDHCP that includes the port-4011 boot-server handshake most naive
implementations skip.

Unlike the earlier drafts, the code here is the real, wired, tested
`proxydhcp` — `booty serve --proxydhcp` runs it, and its wire encoding is
pinned byte-for-byte by unit tests.

Source: [`proxydhcp/proxydhcp.go`](../../proxydhcp/proxydhcp.go).

## booty doesn't own DHCP — it proxies it

Almost every network already has a DHCP server: a router, a Synology, dnsmasq, an
Infoblox appliance. You rarely want to replace it, and in an enterprise you often
*can't* (a different team owns it). So booty uses **proxyDHCP**: it listens
alongside the real DHCP server and answers *only* the PXE boot questions, leaving
IP leasing entirely to the incumbent. The real server hands out the address;
booty hands out the boot instructions.

That coexistence is the whole point, and it's what makes booty deployable into a
homelab without reconfiguring the router.

## The DHCP options that matter

DHCP options are Type-Length-Value triplets after the fixed header. The ones in
play for PXE:

| Option | Name | Role |
|--------|------|------|
| 53 | Message Type | DISCOVER / OFFER / REQUEST / ACK |
| 60 | Vendor Class | client announces `PXEClient:Arch:…` |
| 93 | Client Arch | UEFI vs BIOS vs arm64 (RFC 4578) |
| 97 | Client GUID | machine identifier, echoed back |
| 54 | Server Identifier | who sent this reply |
| 43 | Vendor-Specific | **the PXE sub-options — where the 4011 dance lives** |
| 66 / 67 | TFTP server / Bootfile | the eager shortcut's boot target |
| 175 | iPXE Encapsulated | hand a script URL straight to an already-running iPXE |

### Option 60 + 93: knowing it's PXE, and which flavor

The client puts its type in option 60 (`PXEClient:Arch:00007:UNDI:003016`) and its
architecture in option 93. Arch `0x0007` is x64 UEFI, `0x0000` is legacy BIOS,
`0x000b` is arm64 UEFI — and they need different boot files (`ipxe.efi` vs
`undionly.kpxe`). booty maps this directly:

```go
var uefiArch = map[uint16]bool{
	0x0006: true, 0x0007: true, 0x0008: true, // IA32 / x64 / xscale UEFI
	0x0009: true, 0x000a: true, 0x000b: true, // EBC / arm32 / arm64 UEFI
}

func (s *Server) bootFile(arch uint16) string {
	if uefiArch[arch] {
		return s.bootFileEFI // "ipxe.efi"
	}
	return s.bootFileBIOS // "undionly.kpxe"
}
```

## The eager shortcut vs. the real handshake

Here is the correction the earlier draft got wrong. There are **two** ways a
proxyDHCP can answer, and they differ in one bit.

**The eager shortcut.** Put the bootfile straight into the OFFER — set `siaddr`,
`file`, options 66/67 — and set PXE_DISCOVERY_CONTROL bit 3 ("download the boot
file immediately, skip discovery"). dnsmasq's working single-service config does
exactly this: `dhcp-option=vendor:PXEClient,6,2b`, where `0x2b` has bit 3 set. It
works with many clients, and it's what booty's first cut did. But it isn't the PXE
spec's boot-server discovery, and some firmware insists on the full exchange.

**The spec-correct two-phase handshake (what booty now does).** The offer does
*not* name a file and deliberately **leaves bit 3 clear**. Instead it tells the
client "I'm a PXE boot service; go ask a boot server." The client then performs a
second exchange on UDP port **4011** (the BINL port) to actually get the file:

```text
Phase 1 (port 67):  client DISCOVER (PXEClient)
                    → real DHCP OFFER:  yiaddr = 192.168.1.111  (the lease)
                    → booty proxy OFFER: yiaddr = 0.0.0.0, no bootfile,
                                         option 43 → "use boot server 192.168.1.10"
Phase 2 (port 4011): client Boot Server Request  (unicast to booty:4011)
                    → booty Boot Server ACK: siaddr = 192.168.1.10, file = ipxe.efi
Then: client TFTPs ipxe.efi from 192.168.1.10   (Chapter 3)
```

The single bit — PXE_DISCOVERY_CONTROL bit 3, `0x08` — is the entire difference
between the shortcut and the handshake. booty sets discovery control to `0x07`
(bits 0,1,2: disable broadcast discovery, disable multicast discovery, use only
the servers in the list) and pointedly **not** `0x08`. A unit test asserts exactly
that, so the 4011 behavior can't silently regress:

```go
} else if dc[0]&0x08 != 0 {
	t.Errorf("discovery control %#x sets bit 3 (download-immediately); must be clear for the 4011 flow", dc[0])
}
```

## The DHCP packet, and what booty sets in each phase

DHCP rides UDP; the fixed header is 236 bytes, then a 4-byte magic cookie
(`0x63825363`), then options.

```text
op(1) htype(1) hlen(1) hops(1)
xid(4)                              ← transaction id, echoed
secs(2) flags(2)                    ← flags bit 15 = broadcast
ciaddr(4) yiaddr(4) siaddr(4) giaddr(4)
chaddr(16)                          ← client MAC in the first 6
sname(64)
file(128)                           ← boot filename
magic cookie(4) = 0x63825363
options…
```

booty writes different fields in each phase — this table is the crux:

| Field | Phase 1: proxy OFFER (:67) | Phase 2: Boot Server ACK (:4011) |
|-------|----------------------------|----------------------------------|
| `yiaddr` | `0.0.0.0` (we lease no IP) | `0.0.0.0` |
| `siaddr` | `0.0.0.0` (deferred) | **booty's IP** (the TFTP/boot server) |
| `file` | empty | **`ipxe.efi`** (arch-picked) |
| option 53 | OFFER | ACK |
| option 43 | discovery control + boot-server list + menu | echoes PXE_BOOT_ITEM |
| option 97 | client GUID echoed | client GUID echoed |

The offer withholding `file` and `siaddr` is what forces phase 2; the ACK
supplying them is what finally sends the client to TFTP.

## Option 43: the PXE sub-options that steer the client

Option 43's payload is itself a mini-TLV stream of PXE sub-options (Intel PXE 2.1).
booty's phase-1 offer encodes four of them:

| Sub-option | Name | booty's value |
|-----------|------|---------------|
| 6 | PXE_DISCOVERY_CONTROL | `0x07` (force the 4011 unicast) |
| 8 | PXE_BOOT_SERVERS | `{type, count=1, booty's IP}` |
| 9 | PXE_BOOT_MENU | `{type, len, "booty"}` |
| 10 | PXE_MENU_PROMPT | `{timeout=0, "booty"}` (auto-boot, no wait) |

```go
func (s *Server) buildPXEOffer43() []byte {
	// PXE_BOOT_SERVERS: one entry {type u16, ipcount=1, our IP}.
	servers := binary.BigEndian.AppendUint16(nil, pxeBootServerType)
	servers = append(servers, 1)
	servers = append(servers, s.serverIP...)
	// … PXE_BOOT_MENU {type, len, "booty"}, PXE_MENU_PROMPT {timeout 0, "booty"} …
	return encodeSubOptions(
		subOption{pxeDiscoveryControl, []byte{discoveryControl4011}},
		subOption{pxeBootServers, servers},
		subOption{pxeBootMenu, menu},
		subOption{pxeMenuPrompt, prompt},
	)
}
```

The "boot server type" is an opaque 16-bit tag that must be identical across
sub-options 8, 9, and the client's later PXE_BOOT_ITEM (71); its value is arbitrary
for a single-service proxy, so booty uses one constant. The menu prompt timeout of
`0` means "boot the first (only) item immediately, no prompt" — the operator never
sees a menu.

## The Go implementation

The package splits, like `tftp` and `httpsrv`, into socket handling and pure
packet logic. Construction validates the advertised IP up front, because a
proxyDHCP that points clients at an unreachable boot server is worse than none:

```go
func New(cfg Config) (*Server, error) {
	ip := net.ParseIP(cfg.ServerIP).To4()
	if ip == nil {
		return nil, fmt.Errorf("proxydhcp: ServerIP %q is not a valid IPv4 address", cfg.ServerIP)
	}
	// … defaults: BootFileEFI="ipxe.efi", BootFileBIOS="undionly.kpxe" …
}
```

Two handlers, one per phase. Phase 1 answers a PXEClient DISCOVER by broadcasting
the steering offer (the client has no IP yet, so it must be broadcast):

```go
func (s *Server) handleDHCP(conn net.PacketConn, raw []byte, _ net.Addr) {
	req, err := parsePacket(raw)
	if err != nil || req.op != opBOOTREQUEST || !req.isPXE || req.msgType != msgDISCOVER {
		return // leave ordinary DHCP traffic to the real server
	}
	dst := &net.UDPAddr{IP: net.IPv4bcast, Port: portBoot}
	conn.WriteTo(s.buildProxyOffer(req), dst)
}
```

Phase 2 answers the Boot Server Request on port 4011 — a DHCPREQUEST — with the
ACK that finally names the file, unicast back to the source (which now holds the
real DHCP lease):

```go
func (s *Server) handleBINL(conn net.PacketConn, raw []byte, src net.Addr) {
	req, err := parsePacket(raw)
	if err != nil || req.op != opBOOTREQUEST || !req.isPXE || req.msgType != msgREQUEST {
		return
	}
	conn.WriteTo(s.buildBootAck(req), src) // siaddr=booty, file=ipxe.efi
}
```

### The SO_BROADCAST gotcha

The phase-1 offer broadcasts to `255.255.255.255:68`, and this bites everyone once:
**Go does not set `SO_BROADCAST` on UDP sockets by default**, so `WriteTo` a
broadcast address fails with `EACCES`. booty sets it explicitly by reaching through
the socket:

```go
func listenBroadcastUDP(addr string) (net.PacketConn, error) {
	pc, _ := net.ListenPacket("udp4", addr)
	rc, _ := pc.(*net.UDPConn).SyscallConn()
	rc.Control(func(fd uintptr) {
		syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
	})
	return pc, nil
}
```

The BINL socket needs no such thing — its ACK is unicast.

## How it's tested

The `Serve(ctx, conn, binl)` seam lets tests drive real UDP without privilege. The
BINL path is unicast, so there's an end-to-end socket test — a Boot Server Request
in, an ACK naming `ipxe.efi` back:

```go
go s.Serve(t.Context(), srv, true)         // BINL responder on 127.0.0.1:0
client.WriteTo(craftRequest(msgREQUEST, 0x0007, true, nil), srv.LocalAddr())
// … read reply, assert file field == "ipxe.efi"
```

Everything else — offer structure, the bit-3-clear discovery control, the
boot-server list pointing at us, the ACK's `siaddr`/`file`, arch selection — is
pinned by byte-level unit tests against crafted packets. Real *firmware* interop
(does a given NIC ROM actually complete the 4011 dance?) is the job of the QEMU
e2e tier from Chapter 9; the unit tests lock the wire encoding to the spec.

## Wiring it into `booty serve`

proxyDHCP is opt-in — it's disruptive to enable by accident on a network you don't
intend it for — and it needs to know booty's own IP to advertise:

```bash
booty serve --proxydhcp --server-ip 192.168.1.10 \
  --catalog ./catalog --boot-dir ./boot
```

It joins HTTP and TFTP as a third server under the same cancellable context
(Chapter 8), so one `SIGTERM` drains all three. Misconfiguration fails fast:

```console
$ booty serve --proxydhcp                 # no --server-ip
ERROR proxydhcp init failed err="ServerIP \"\" is not a valid IPv4 address"
      hint="set --server-ip to booty's reachable IPv4"
# exit 1
```

`--proxydhcp-addr` (default `0.0.0.0:67`) and `--proxydhcp-binl-addr` (default
`0.0.0.0:4011`) are overridable — handy for testing on high ports, since binding
`:67` needs privilege (`CAP_NET_BIND_SERVICE` or root; recall the distroless
nonroot caveat from Chapter 10).

## Reading the exchange with tcpdump

A healthy proxyDHCP boot now has **two** request/reply legs — watch both 67/68 and
4011:

```bash
sudo tcpdump -i eth0 -n -v "port 67 or port 68 or port 4011"
```

```text
1. DISCOVER  0.0.0.0.68        > 255.255.255.255.67   (PXEClient)
2. OFFER     192.168.1.1.67    > …                     (real DHCP: the lease)
3. OFFER     192.168.1.10.67   > …                     (booty proxy: option 43, no file)
4. REQUEST   0.0.0.0.68        > 255.255.255.255.67    (accept lease from real DHCP)
5. ACK       192.168.1.1.67    > …
6. BSReq     192.168.1.111.xxx > 192.168.1.10.4011     (Boot Server Request — the 4011 leg)
7. BSAck     192.168.1.10.4011 > 192.168.1.111.xxx     (file = ipxe.efi, siaddr = booty)
8. TFTP RRQ  192.168.1.111     > 192.168.1.10.69       "ipxe.efi"   (Chapter 3)
```

If you see legs 1–5 but the boot stalls, the classic symptom is **PXE-E55:
"ProxyDHCP service did not reply to request on port 4011"** — the client did its
part (leg 6) and got no ACK. That means booty's BINL listener isn't reachable:
firewall on 4011, or booty not running with `--proxydhcp`.

## If you'd rather configure the real DHCP

You don't have to use proxyDHCP — you can put the two options on the existing
server instead (the eager path, no 4011). For reference:

```ini
# dnsmasq — point PXE clients straight at booty over TFTP
dhcp-match=set:efi,option:client-arch,7
dhcp-boot=tag:efi,ipxe.efi,booty,192.168.1.10
dhcp-match=set:bios,option:client-arch,0
dhcp-boot=tag:bios,undionly.kpxe,booty,192.168.1.10
# Nodes already running iPXE: skip TFTP, hand them the script URL (option 175)
dhcp-match=set:ipxe,175
dhcp-boot=tag:ipxe,http://192.168.1.10:8080/boot.ipxe
```

That option-175 line is the same two-script iPXE model from Chapter 4: a machine
already running iPXE is handed booty's **chain script** URL directly, skipping the
TFTP-load of `ipxe.efi` entirely.

## Gotchas and what's deferred

- **Same host as the real DHCP.** booty's proxyDHCP assumes it runs on a
  *different* host from the DHCP server, so both can bind `:67` and both hear the
  broadcast. Co-locating proxyDHCP with DHCP on one host needs the single-process
  trick dnsmasq uses; that's out of scope.
- **UEFI clients that can't parse option 43.** A few UEFI targets ignore option 43;
  for those the fallback is the eager path (options 66/67). booty's proxy currently
  commits to the 4011 flow — supporting both from one responder is a natural
  extension.
- **DHCP relays (giaddr).** booty honors a non-zero `giaddr` by unicasting the offer
  to the relay, but cross-subnet PXE via relays isn't exercised by the tests.

With the handoff understood — and booty able to supply it without touching your
DHCP — the client now has a boot server and a filename. Next it has to actually
*fetch* that file, over the one protocol its firmware can speak: TFTP.

---

← [Chapter 1](./01-boot-sequence.md) | [Chapter 3: TFTP From Scratch →](./03-tftp-from-scratch.md)
