// Package proxydhcp implements a PXE proxyDHCP (a.k.a. BINL) service: it answers
// a PXE client's boot questions WITHOUT handing out IP addresses, so it coexists
// with an existing DHCP server (a router, a homelab appliance) that owns the
// leases. booty runs it so a bare NIC ROM can find and load ipxe.efi with no
// changes to the network's DHCP.
//
// It implements the spec-correct two-phase PXE Boot Server Discovery from the
// Intel PXE 2.1 specification, not the eager shortcut:
//
//  1. Port 67 — the client broadcasts a DHCPDISCOVER tagged option 60 =
//     "PXEClient". The real DHCP server answers with an IP; booty answers with a
//     *proxy* DHCPOFFER (yiaddr = 0, no bootfile) whose option 43 carries
//     PXE_DISCOVERY_CONTROL + a PXE_BOOT_SERVERS list pointing at booty. The
//     discovery-control byte deliberately does NOT set the "download immediately"
//     bit, so the client is required to go to phase 2 rather than boot from the
//     offer.
//  2. Port 4011 (BINL) — the client unicasts a Boot Server Request to booty. booty
//     replies with a Boot Server ACK that finally names the boot file (arch-picked
//     ipxe.efi) in the DHCP `file` field, with siaddr = booty's IP. The client
//     then TFTPs that file (Chapter 3) and runs it (Chapter 4).
//
// The socket/broadcast handling lives in ListenAndServe; the packet logic lives
// in pure functions (parsePacket, buildProxyOffer, buildBootAck) that are unit
// tested byte-for-byte against the spec, since real-firmware interop is exercised
// by the QEMU e2e tier (Chapter 9).
package proxydhcp

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"syscall"
)

// UDP ports in the exchange.
const (
	PortDHCP = 67   // proxy OFFER is sent here (client broadcast destination)
	PortBoot = 68   // client listens here for replies
	PortBINL = 4011 // Boot Server Request/ACK (BINL) port
)

// BOOTP op codes and DHCP message types (RFC 2131 §2, §3).
const (
	opBOOTREQUEST = 1
	opBOOTREPLY   = 2

	msgDISCOVER = 1
	msgOFFER    = 2
	msgREQUEST  = 3
	msgACK      = 5
)

// DHCP option codes (RFC 2132 + PXE options RFC 4578).
const (
	optPad            = 0
	optMessageType    = 53
	optServerID       = 54
	optVendorClass    = 60 // "PXEClient:Arch:xxxxx:UNDI:yyyzzz"
	optVendorSpecific = 43 // PXE sub-options live here
	optClientArch     = 93 // RFC 4578
	optClientGUID     = 97 // RFC 4578 (echoed back to the client)
	optBootfileName   = 67
	optEnd            = 255
)

// PXE option-43 sub-option tags (Intel PXE 2.1 spec).
const (
	pxeDiscoveryControl = 6  // 1-byte bitmask
	pxeBootServers      = 8  // list of {type u16, ipcount u8, ip[]}
	pxeBootMenu         = 9  // list of {type u16, desclen u8, desc}
	pxeMenuPrompt       = 10 // {timeout u8, prompt}
	pxeBootItem         = 71 // {type u16, layer u16}
	pxeSubEnd           = 255
)

// discoveryControl4011 forces the port-4011 exchange: bit0 (0x01) disable
// broadcast discovery, bit1 (0x02) disable multicast discovery, bit2 (0x04) use
// only servers in PXE_BOOT_SERVERS. Bit3 ("download bootfile immediately") is
// intentionally CLEAR — that is the difference between this and the eager
// shortcut, and it is what makes the client send a Boot Server Request to :4011.
const discoveryControl4011 = 0x07

// pxeBootServerType is the opaque boot-server "type" tag. It must be identical
// across PXE_BOOT_SERVERS (8), PXE_BOOT_MENU (9), and the client's PXE_BOOT_ITEM
// (71); the value itself is arbitrary for a single-service proxy. If a specific
// firmware rejects it, this is the one knob to turn.
const pxeBootServerType uint16 = 0

const magicCookie uint32 = 0x63825363

// PXE client system architectures (RFC 4578 §2.1) that boot via UEFI and should
// receive the EFI boot file rather than the legacy BIOS one.
var uefiArch = map[uint16]bool{
	0x0006: true, // x86 UEFI (IA32)
	0x0007: true, // x64 UEFI
	0x0008: true, // xscale UEFI
	0x0009: true, // EBC
	0x000a: true, // arm 32 UEFI
	0x000b: true, // arm 64 UEFI
}

// Config configures a Server. ServerIP is booty's own address, advertised to the
// client as the boot/TFTP server; it must be a routable IPv4 the client can reach.
type Config struct {
	ServerIP     string // booty's IPv4, e.g. "192.168.1.10"
	BootFileEFI  string // default "ipxe.efi"
	BootFileBIOS string // default "undionly.kpxe"
	Logger       *slog.Logger
}

// Server answers PXE proxyDHCP (port 67) and BINL (port 4011). Construct with New.
type Server struct {
	serverIP     net.IP
	bootFileEFI  string
	bootFileBIOS string
	logger       *slog.Logger
}

// New validates the config and returns a Server. It errors if ServerIP is not a
// usable IPv4, because a proxyDHCP that advertises an unreachable boot server is
// worse than none.
func New(cfg Config) (*Server, error) {
	ip := net.ParseIP(cfg.ServerIP).To4()
	if ip == nil {
		return nil, fmt.Errorf("proxydhcp: ServerIP %q is not a valid IPv4 address", cfg.ServerIP)
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	efi, bios := cfg.BootFileEFI, cfg.BootFileBIOS
	if efi == "" {
		efi = "ipxe.efi"
	}
	if bios == "" {
		bios = "undionly.kpxe"
	}
	return &Server{serverIP: ip, bootFileEFI: efi, bootFileBIOS: bios, logger: logger}, nil
}

// ListenAndServe binds the proxyDHCP (dhcpAddr, normally 0.0.0.0:67) and BINL
// (binlAddr, normally 0.0.0.0:4011) sockets and serves until ctx is cancelled.
// The DHCP socket has SO_BROADCAST enabled so the proxy OFFER can be broadcast to
// a client that has no IP yet — Go does not set that by default, and without it
// WriteTo a broadcast address fails with EACCES.
func (s *Server) ListenAndServe(ctx context.Context, dhcpAddr, binlAddr string) error {
	dhcpConn, err := listenBroadcastUDP(dhcpAddr)
	if err != nil {
		return fmt.Errorf("proxydhcp listen %s: %w", dhcpAddr, err)
	}
	defer dhcpConn.Close()

	binlConn, err := net.ListenPacket("udp4", binlAddr)
	if err != nil {
		return fmt.Errorf("proxydhcp BINL listen %s: %w", binlAddr, err)
	}
	defer binlConn.Close()

	s.logger.Info("proxyDHCP listening", "dhcp_addr", dhcpConn.LocalAddr(),
		"binl_addr", binlConn.LocalAddr(), "server_ip", s.serverIP)

	// Cancelling ctx closes both sockets, unblocking the ReadFrom loops.
	go func() {
		<-ctx.Done()
		_ = dhcpConn.Close()
		_ = binlConn.Close()
	}()

	errc := make(chan error, 2)
	go func() { errc <- s.serve(ctx, dhcpConn, s.handleDHCP) }()
	go func() { errc <- s.serve(ctx, binlConn, s.handleBINL) }()

	err = <-errc
	if ctx.Err() != nil {
		return nil // clean shutdown
	}
	return err
}

// Serve reads packets off conn and dispatches each to handle until ctx is done.
// It is the testable seam: a caller can pass any net.PacketConn.
func (s *Server) Serve(ctx context.Context, conn net.PacketConn, binl bool) error {
	if binl {
		return s.serve(ctx, conn, s.handleBINL)
	}
	return s.serve(ctx, conn, s.handleDHCP)
}

func (*Server) serve(ctx context.Context, conn net.PacketConn, handle func(net.PacketConn, []byte, net.Addr)) error {
	buf := make([]byte, 1500) // one Ethernet MTU; DHCP packets fit easily
	for {
		n, src, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil //nolint:nilerr // a cancelled ctx closed the conn: clean shutdown, not an error
			}
			return fmt.Errorf("proxydhcp read: %w", err)
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		go handle(conn, pkt, src)
	}
}

// handleDHCP answers a broadcast DHCPDISCOVER from a PXE client with a proxy
// OFFER (phase 1). It ignores anything that is not a PXEClient DISCOVER, leaving
// ordinary DHCP traffic to the real server.
func (s *Server) handleDHCP(conn net.PacketConn, raw []byte, _ net.Addr) {
	req, err := parsePacket(raw)
	if err != nil || req.op != opBOOTREQUEST || !req.isPXE || req.msgType != msgDISCOVER {
		return
	}
	s.logger.Info("proxyDHCP offer", "mac", req.mac, "arch", req.arch, "bootfile", s.bootFile(req.arch))

	reply := s.buildProxyOffer(req)
	// The client has no IP yet: broadcast to 255.255.255.255:68 (or the relay).
	dst := &net.UDPAddr{IP: net.IPv4bcast, Port: PortBoot}
	if req.giaddr != nil && !req.giaddr.Equal(net.IPv4zero) {
		dst = &net.UDPAddr{IP: req.giaddr, Port: PortDHCP}
	}
	if _, err := conn.WriteTo(reply, dst); err != nil {
		s.logger.Error("proxyDHCP offer send failed", "mac", req.mac, "err", err)
	}
}

// handleBINL answers the client's Boot Server Request on port 4011 with a Boot
// Server ACK naming the boot file (phase 2). The reply is unicast back to the
// request's source, which by now holds the IP the real DHCP server assigned.
func (s *Server) handleBINL(conn net.PacketConn, raw []byte, src net.Addr) {
	req, err := parsePacket(raw)
	// The Boot Server Request is a DHCPREQUEST (Intel PXE 2.1); ignore anything
	// else that lands on the BINL port.
	if err != nil || req.op != opBOOTREQUEST || !req.isPXE || req.msgType != msgREQUEST {
		return
	}
	bootFile := s.bootFile(req.arch)
	s.logger.Info("proxyDHCP boot-ack", "mac", req.mac, "arch", req.arch,
		"bootfile", bootFile, "next_server", s.serverIP, "client", src)

	if _, err := conn.WriteTo(s.buildBootAck(req), src); err != nil {
		s.logger.Error("proxyDHCP boot-ack send failed", "mac", req.mac, "err", err)
	}
}

func (s *Server) bootFile(arch uint16) string {
	if uefiArch[arch] {
		return s.bootFileEFI
	}
	return s.bootFileBIOS
}

// packet is the parsed subset of a BOOTP/DHCP message this server cares about.
type packet struct {
	op      byte
	xid     uint32
	flags   uint16
	mac     net.HardwareAddr
	giaddr  net.IP
	msgType byte
	arch    uint16
	guid    []byte
	isPXE   bool
}

// parsePacket parses the BOOTP header and DHCP options (RFC 2131 layout). It sets
// isPXE when option 60 begins with "PXEClient".
func parsePacket(b []byte) (*packet, error) {
	if len(b) < 240 {
		return nil, fmt.Errorf("short packet: %d bytes", len(b))
	}
	if binary.BigEndian.Uint32(b[236:240]) != magicCookie {
		return nil, errors.New("bad DHCP magic cookie")
	}
	p := &packet{
		op:     b[0],
		xid:    binary.BigEndian.Uint32(b[4:8]),
		flags:  binary.BigEndian.Uint16(b[10:12]),
		mac:    net.HardwareAddr(append([]byte(nil), b[28:34]...)),
		giaddr: net.IP(append([]byte(nil), b[24:28]...)),
	}
	opts, err := parseOptions(b[240:])
	if err != nil {
		return nil, err
	}
	if mt := opts[optMessageType]; len(mt) == 1 {
		p.msgType = mt[0]
	}
	if vc := opts[optVendorClass]; len(vc) >= 9 && string(vc[:9]) == "PXEClient" {
		p.isPXE = true
	}
	if a := opts[optClientArch]; len(a) == 2 {
		p.arch = binary.BigEndian.Uint16(a)
	}
	p.guid = opts[optClientGUID]
	return p, nil
}

// buildProxyOffer builds the phase-1 proxy DHCPOFFER (sent on port 67). It carries
// NO boot file — the boot file arrives in the phase-2 ACK — and its option 43
// steers the client to the BINL port.
func (s *Server) buildProxyOffer(req *packet) []byte {
	b := s.baseReply(req, msgOFFER)
	// yiaddr and siaddr stay 0 in the offer: we assign no IP, and the boot server
	// is named in the phase-2 ACK.
	opt := newOptionWriter()
	opt.write(optMessageType, []byte{msgOFFER})
	opt.write(optServerID, s.serverIP)
	opt.write(optVendorClass, []byte("PXEClient"))
	if len(req.guid) > 0 {
		opt.write(optClientGUID, req.guid)
	}
	opt.write(optVendorSpecific, s.buildPXEOffer43())
	opt.end()
	return append(b, opt.bytes()...)
}

// buildBootAck builds the phase-2 Boot Server ACK (sent from port 4011). This is
// the packet that finally names the boot file: in the `file` field, with siaddr
// set to booty so the client TFTPs from the right place.
func (s *Server) buildBootAck(req *packet) []byte {
	b := s.baseReply(req, msgACK)
	copy(b[20:24], s.serverIP)                   // siaddr = next-server (TFTP/boot)
	copyString(b[108:236], s.bootFile(req.arch)) // file (128 bytes, null-padded)

	opt := newOptionWriter()
	opt.write(optMessageType, []byte{msgACK})
	opt.write(optServerID, s.serverIP)
	opt.write(optVendorClass, []byte("PXEClient"))
	if len(req.guid) > 0 {
		opt.write(optClientGUID, req.guid)
	}
	// Echo the boot item so the client can correlate the ACK with its request.
	item := make([]byte, 4)
	binary.BigEndian.PutUint16(item[0:2], pxeBootServerType)
	binary.BigEndian.PutUint16(item[2:4], 0) // layer 0 = the bootstrap
	opt.write(optVendorSpecific, encodeSubOptions(subOption{pxeBootItem, item}))
	opt.write(optBootfileName, []byte(s.bootFile(req.arch)))
	opt.end()
	return append(b, opt.bytes()...)
}

// baseReply builds the 240-byte BOOTP reply header shared by both phases.
func (*Server) baseReply(req *packet, _ byte) []byte {
	b := make([]byte, 240)
	b[0] = opBOOTREPLY
	b[1] = 1 // htype: Ethernet
	b[2] = 6 // hlen
	binary.BigEndian.PutUint32(b[4:8], req.xid)
	binary.BigEndian.PutUint16(b[10:12], req.flags) // echo broadcast flag
	copy(b[24:28], req.giaddr)                      // echo relay
	copy(b[28:34], req.mac)                         // chaddr
	binary.BigEndian.PutUint32(b[236:240], magicCookie)
	return b
}

// buildPXEOffer43 encodes the option-43 payload for the phase-1 offer: discovery
// control that forces the BINL exchange, a boot-server list pointing at booty, and
// a single auto-selected menu item.
func (s *Server) buildPXEOffer43() []byte {
	// PXE_BOOT_SERVERS: one entry {type, ipcount=1, our IP}.
	servers := make([]byte, 0, 7)
	servers = binary.BigEndian.AppendUint16(servers, pxeBootServerType)
	servers = append(servers, 1) // one IP address follows
	servers = append(servers, s.serverIP...)

	// PXE_BOOT_MENU: one item {type, desclen, "booty"}.
	desc := []byte("booty")
	menu := make([]byte, 0, 3+len(desc))
	menu = binary.BigEndian.AppendUint16(menu, pxeBootServerType)
	menu = append(menu, byte(len(desc))) //nolint:gosec // desc is the fixed string "booty", len 5
	menu = append(menu, desc...)

	// PXE_MENU_PROMPT: timeout 0 = boot the first item immediately, no prompt.
	prompt := append([]byte{0}, "booty"...)

	return encodeSubOptions(
		subOption{pxeDiscoveryControl, []byte{discoveryControl4011}},
		subOption{pxeBootServers, servers},
		subOption{pxeBootMenu, menu},
		subOption{pxeMenuPrompt, prompt},
	)
}

// --- option encoding helpers ---

type subOption struct {
	tag  byte
	data []byte
}

// encodeSubOptions serializes PXE sub-options (tag, len, data)… terminated by the
// PXE sub-option End (255).
func encodeSubOptions(subs ...subOption) []byte {
	var out []byte
	for _, s := range subs {
		out = append(out, s.tag, byte(len(s.data))) //nolint:gosec // sub-option payloads are built above from fixed-size fields, always < 256 bytes
		out = append(out, s.data...)
	}
	return append(out, pxeSubEnd)
}

type optionWriter struct{ buf []byte }

func newOptionWriter() *optionWriter { return &optionWriter{} }

func (w *optionWriter) write(code byte, data []byte) {
	w.buf = append(w.buf, code, byte(len(data))) //nolint:gosec // option payloads (bootfile names, option-43 blobs) are always < 256 bytes
	w.buf = append(w.buf, data...)
}
func (w *optionWriter) end()          { w.buf = append(w.buf, optEnd) }
func (w *optionWriter) bytes() []byte { return w.buf }

// parseOptions parses a DHCP options section into a code→value map, honoring Pad
// (0) and End (255).
func parseOptions(data []byte) (map[byte][]byte, error) {
	opts := make(map[byte][]byte)
	for i := 0; i < len(data); {
		code := data[i]
		i++
		if code == optEnd {
			break
		}
		if code == optPad {
			continue
		}
		if i >= len(data) {
			return nil, fmt.Errorf("truncated option %d", code)
		}
		length := int(data[i]) //nolint:gosec // i < len(data) is checked just above
		i++
		if i+length > len(data) {
			return nil, fmt.Errorf("option %d length %d overruns packet", code, length)
		}
		opts[code] = data[i : i+length] //nolint:gosec // i+length <= len(data) is checked just above
		i += length
	}
	return opts, nil
}

func copyString(dst []byte, s string) {
	for i := range dst {
		dst[i] = 0
	}
	copy(dst, s)
}

// listenBroadcastUDP opens a UDP4 socket with SO_BROADCAST enabled so a proxy
// OFFER can be broadcast to a still-IP-less client.
func listenBroadcastUDP(addr string) (net.PacketConn, error) {
	pc, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return nil, err
	}
	udp, ok := pc.(*net.UDPConn)
	if !ok {
		return pc, nil
	}
	rc, err := udp.SyscallConn()
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	var setErr error
	if err := rc.Control(func(fd uintptr) {
		//nolint:gosec // fd is a real socket descriptor, well within int range
		setErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
	}); err != nil {
		_ = pc.Close()
		return nil, err
	}
	if setErr != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("enable SO_BROADCAST: %w", setErr)
	}
	return pc, nil
}
