package proxydhcp

import (
	"bytes"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Config{ServerIP: "192.168.1.10", Logger: quiet()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// craftRequest builds a minimal BOOTP/DHCP request with the given message type,
// arch, and (optionally) a PXEClient vendor class and a GUID. It mirrors what a
// PXE NIC ROM sends.
func craftRequest(msgType byte, arch uint16, pxe bool, guid []byte) []byte {
	b := make([]byte, 240, 320) // 240-byte BOOTP header + room for the options below
	b[0] = opBOOTREQUEST
	b[1] = 1                                       // htype ethernet
	b[2] = 6                                       // hlen
	binary.BigEndian.PutUint32(b[4:8], 0xdeadbeef) // xid
	binary.BigEndian.PutUint16(b[10:12], 0x8000)   // broadcast flag
	copy(b[28:34], net.HardwareAddr{0xd0, 0x50, 0x99, 0xb3, 0x4c, 0x50})
	binary.BigEndian.PutUint32(b[236:240], magicCookie)

	w := newOptionWriter()
	w.write(optMessageType, []byte{msgType})
	if pxe {
		w.write(optVendorClass, []byte("PXEClient:Arch:00007:UNDI:003016"))
	} else {
		w.write(optVendorClass, []byte("udhcp 1.0"))
	}
	archBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(archBytes, arch)
	w.write(optClientArch, archBytes)
	if guid != nil {
		w.write(optClientGUID, guid)
	}
	w.end()
	return append(b, w.bytes()...)
}

// findSubOption walks an option-43 payload and returns the data for tag.
func findSubOption(t *testing.T, opt43 []byte, tag byte) ([]byte, bool) {
	t.Helper()
	for i := 0; i < len(opt43); {
		code := opt43[i]
		i++
		if code == pxeSubEnd {
			break
		}
		if i >= len(opt43) {
			t.Fatalf("truncated sub-option %d", code)
		}
		length := int(opt43[i])
		i++
		if i+length > len(opt43) {
			t.Fatalf("sub-option %d overruns", code)
		}
		if code == tag {
			return opt43[i : i+length], true
		}
		i += length
	}
	return nil, false
}

func TestNewRejectsBadIP(t *testing.T) {
	if _, err := New(Config{ServerIP: "not-an-ip"}); err == nil {
		t.Fatal("want error for invalid ServerIP")
	}
	if _, err := New(Config{ServerIP: "2001:db8::1"}); err == nil {
		t.Fatal("want error for IPv6 ServerIP (proxyDHCP is IPv4)")
	}
}

func TestParsePacketPXE(t *testing.T) {
	raw := craftRequest(msgDISCOVER, 0x0007, true, nil)
	p, err := parsePacket(raw)
	if err != nil {
		t.Fatalf("parsePacket: %v", err)
	}
	if !p.isPXE {
		t.Error("PXEClient vendor class not detected")
	}
	if p.msgType != msgDISCOVER {
		t.Errorf("msgType = %d, want DISCOVER", p.msgType)
	}
	if p.arch != 0x0007 {
		t.Errorf("arch = %#x, want 0x0007", p.arch)
	}
	if p.mac.String() != "d0:50:99:b3:4c:50" {
		t.Errorf("mac = %s", p.mac)
	}
}

func TestParsePacketRejectsBad(t *testing.T) {
	if _, err := parsePacket(make([]byte, 100)); err == nil {
		t.Error("want error for short packet")
	}
	bad := craftRequest(msgDISCOVER, 0x0007, true, nil)
	binary.BigEndian.PutUint32(bad[236:240], 0) // corrupt magic cookie
	if _, err := parsePacket(bad); err == nil {
		t.Error("want error for bad magic cookie")
	}
}

func TestProxyOfferSteersToBINL(t *testing.T) {
	s := testServer(t)
	guid := bytes.Repeat([]byte{0xAB}, 17) // type byte + 16-byte GUID
	req, _ := parsePacket(craftRequest(msgDISCOVER, 0x0007, true, guid))
	offer := s.buildProxyOffer(req)

	p, err := parsePacket(offer)
	if err != nil {
		t.Fatalf("offer does not parse: %v", err)
	}
	if p.op != opBOOTREPLY {
		t.Error("offer op must be BOOTREPLY")
	}
	// The offer must NOT name a boot file — that is the whole point of forcing the
	// BINL exchange.
	if got := bytes.Trim(offer[108:236], "\x00"); len(got) != 0 {
		t.Errorf("offer file field must be empty, got %q", got)
	}
	// yiaddr/siaddr must be zero: we assign no IP and defer the boot server.
	if !bytes.Equal(offer[16:20], []byte{0, 0, 0, 0}) || !bytes.Equal(offer[20:24], []byte{0, 0, 0, 0}) {
		t.Error("offer yiaddr/siaddr must be 0.0.0.0")
	}

	opts, _ := parseOptions(offer[240:])
	if mt := opts[optMessageType]; len(mt) != 1 || mt[0] != msgOFFER {
		t.Error("offer message type must be OFFER")
	}
	if !bytes.Equal(opts[optServerID], s.serverIP) {
		t.Errorf("server-id = %v, want %v", opts[optServerID], s.serverIP)
	}
	if !bytes.Equal(opts[optClientGUID], guid) {
		t.Error("client GUID (opt 97) must be echoed")
	}

	// option 43: discovery control forces BINL (bit 3 clear), and the boot server
	// list must point at us.
	opt43 := opts[optVendorSpecific]
	if dc, ok := findSubOption(t, opt43, pxeDiscoveryControl); !ok || len(dc) != 1 {
		t.Fatal("missing PXE_DISCOVERY_CONTROL")
	} else if dc[0]&0x08 != 0 {
		t.Errorf("discovery control %#x sets bit 3 (download-immediately); must be clear for the 4011 flow", dc[0])
	} else if dc[0] != discoveryControl4011 {
		t.Errorf("discovery control = %#x, want %#x", dc[0], discoveryControl4011)
	}
	servers, ok := findSubOption(t, opt43, pxeBootServers)
	if !ok || len(servers) < 7 {
		t.Fatalf("missing/short PXE_BOOT_SERVERS: %v", servers)
	}
	// {type u16}{count u8=1}{ip 4}
	if binary.BigEndian.Uint16(servers[0:2]) != pxeBootServerType || servers[2] != 1 {
		t.Errorf("boot server entry malformed: %v", servers)
	}
	if !net.IP(servers[3:7]).Equal(net.ParseIP("192.168.1.10")) {
		t.Errorf("boot server IP = %v, want 192.168.1.10", net.IP(servers[3:7]))
	}
	if _, ok := findSubOption(t, opt43, pxeMenuPrompt); !ok {
		t.Error("missing PXE_MENU_PROMPT")
	}
}

func TestBootAckNamesFile(t *testing.T) {
	s := testServer(t)
	req, _ := parsePacket(craftRequest(msgREQUEST, 0x0007, true, nil))
	ack := s.buildBootAck(req)

	if _, err := parsePacket(ack); err != nil {
		t.Fatalf("ack does not parse: %v", err)
	}
	// siaddr must be booty (next-server / TFTP).
	if !net.IP(ack[20:24]).Equal(net.ParseIP("192.168.1.10")) {
		t.Errorf("ack siaddr = %v, want 192.168.1.10", net.IP(ack[20:24]))
	}
	// The boot file finally appears — in the `file` field and option 67.
	if got := string(bytes.Trim(ack[108:236], "\x00")); got != "ipxe.efi" {
		t.Errorf("ack file field = %q, want ipxe.efi", got)
	}
	opts, _ := parseOptions(ack[240:])
	if mt := opts[optMessageType]; len(mt) != 1 || mt[0] != msgACK {
		t.Error("ack message type must be ACK")
	}
	if string(opts[optBootfileName]) != "ipxe.efi" {
		t.Errorf("opt 67 = %q, want ipxe.efi", opts[optBootfileName])
	}
	if item, ok := findSubOption(t, opts[optVendorSpecific], pxeBootItem); !ok || len(item) != 4 {
		t.Errorf("ack must echo PXE_BOOT_ITEM (4 bytes), got %v", item)
	}
}

func TestBootFileByArch(t *testing.T) {
	s := testServer(t)
	cases := map[uint16]string{
		0x0000: "undionly.kpxe", // legacy BIOS x86
		0x0006: "ipxe.efi",      // IA32 UEFI
		0x0007: "ipxe.efi",      // x64 UEFI
		0x000b: "ipxe.efi",      // arm64 UEFI
	}
	for arch, want := range cases {
		if got := s.bootFile(arch); got != want {
			t.Errorf("arch %#x -> %q, want %q", arch, got, want)
		}
	}
}

func TestHandleDHCPIgnoresNonPXE(t *testing.T) {
	// A plain (non-PXE) DISCOVER must be ignored, so booty never disturbs ordinary
	// DHCP traffic owned by the real server.
	req, _ := parsePacket(craftRequest(msgDISCOVER, 0x0000, false, nil))
	if req.isPXE {
		t.Fatal("non-PXE request should not be flagged PXE")
	}
}

func TestRoundTripDiscoverToBoot(t *testing.T) {
	// End-to-end at the packet level: DISCOVER -> offer steers to BINL; a REQUEST
	// on BINL -> ack names the file. This is the whole two-phase handshake.
	s := testServer(t)

	offer := s.buildProxyOffer(mustParse(t, craftRequest(msgDISCOVER, 0x0007, true, nil)))
	opts, _ := parseOptions(offer[240:])
	servers, _ := findSubOption(t, opts[optVendorSpecific], pxeBootServers)
	bootServer := net.IP(servers[3:7])
	if !bootServer.Equal(s.serverIP) {
		t.Fatalf("phase 1 pointed at %v, not booty", bootServer)
	}

	ack := s.buildBootAck(mustParse(t, craftRequest(msgREQUEST, 0x0007, true, nil)))
	if got := string(bytes.Trim(ack[108:236], "\x00")); got != "ipxe.efi" {
		t.Fatalf("phase 2 did not deliver ipxe.efi, got %q", got)
	}
}

// TestServeBINLSocket drives the Serve seam over a real UDP socket: a Boot Server
// Request goes in, a Boot Server ACK naming ipxe.efi comes back to the sender.
// The BINL reply is unicast, so this needs no broadcast privilege.
func TestServeBINLSocket(t *testing.T) {
	s := testServer(t)

	srv, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	// t.Context() is cancelled at test end; the deferred srv.Close() also unblocks
	// Serve's ReadFrom, so the goroutine never leaks.
	go func() { _ = s.Serve(t.Context(), srv, true) }()

	client, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if _, err := client.WriteTo(craftRequest(msgREQUEST, 0x0007, true, nil), srv.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := client.ReadFrom(buf)
	if err != nil {
		t.Fatalf("no BINL ACK received: %v", err)
	}
	ack := buf[:n]
	if got := string(bytes.Trim(ack[108:236], "\x00")); got != "ipxe.efi" {
		t.Fatalf("BINL ACK file = %q, want ipxe.efi", got)
	}
}

func mustParse(t *testing.T, raw []byte) *packet {
	t.Helper()
	p, err := parsePacket(raw)
	if err != nil {
		t.Fatalf("parsePacket: %v", err)
	}
	return p
}
