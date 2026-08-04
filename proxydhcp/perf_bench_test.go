package proxydhcp

import (
	"log/slog"
	"net"
	"testing"
	"time"
)

func benchServer(b *testing.B) *Server {
	b.Helper()
	s, err := New(Config{ServerIP: "192.168.1.10", Logger: quiet()})
	if err != nil {
		b.Fatal(err)
	}
	return s
}

// discardConn is a net.PacketConn whose WriteTo does nothing, so a handler
// benchmark measures parse + build without touching the network.
type discardConn struct{ n int }

func (c *discardConn) WriteTo(p []byte, _ net.Addr) (int, error) { c.n += len(p); return len(p), nil }
func (*discardConn) ReadFrom([]byte) (int, net.Addr, error)      { return 0, nil, nil }
func (*discardConn) Close() error                                { return nil }
func (*discardConn) LocalAddr() net.Addr                         { return &net.UDPAddr{} }
func (*discardConn) SetDeadline(time.Time) error                 { return nil }
func (*discardConn) SetReadDeadline(time.Time) error             { return nil }
func (*discardConn) SetWriteDeadline(time.Time) error            { return nil }

func benchDiscover() []byte {
	return craftRequestBench(msgDISCOVER, 0x0007, true, []byte{
		0, 0x44, 0x45, 0x4c, 0x4c, 0, 0x10, 0x37, 0x80, 0x44, 0xb1, 0xc0, 0x4f, 0x4b, 0x44, 0x32, 0,
	})
}

// craftRequestBench mirrors craftRequest from the test file without *testing.T.
func craftRequestBench(msgType byte, arch uint16, pxe bool, guid []byte) []byte {
	b := make([]byte, 240, 320)
	b[0] = opBOOTREQUEST
	b[1] = 1
	b[2] = 6
	b[4], b[5], b[6], b[7] = 0xde, 0xad, 0xbe, 0xef
	b[10] = 0x80
	copy(b[28:34], net.HardwareAddr{0xd0, 0x50, 0x99, 0xb3, 0x4c, 0x50})
	b[236], b[237], b[238], b[239] = 0x63, 0x82, 0x53, 0x63

	w := newOptionWriter()
	w.write(optMessageType, []byte{msgType})
	if pxe {
		w.write(optVendorClass, []byte("PXEClient:Arch:00007:UNDI:003016"))
	} else {
		w.write(optVendorClass, []byte("udhcp 1.0"))
	}
	w.write(optClientArch, []byte{byte(arch >> 8), byte(arch)})
	if guid != nil {
		w.write(optClientGUID, guid)
	}
	w.end()
	return append(b, w.bytes()...)
}

func BenchmarkParsePacket(b *testing.B) {
	raw := benchDiscover()
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	for b.Loop() {
		p, err := parsePacket(raw)
		if err != nil {
			b.Fatal(err)
		}
		sinkPacket = p
	}
}

func BenchmarkParsePacketNonPXE(b *testing.B) {
	// The common case on a busy DHCP segment: ordinary traffic we must parse
	// far enough to reject.
	raw := craftRequestBench(msgDISCOVER, 0, false, nil)
	b.ReportAllocs()
	for b.Loop() {
		p, err := parsePacket(raw)
		if err != nil {
			b.Fatal(err)
		}
		sinkPacket = p
	}
}

// BenchmarkHandleDHCP is the whole per-datagram unit of work the serve loop
// hands to a goroutine: parse, build the offer, write it.
func BenchmarkHandleDHCP(b *testing.B) {
	s := benchServer(b)
	raw := benchDiscover()
	conn := &discardConn{}
	src := &net.UDPAddr{IP: net.IPv4(0, 0, 0, 0), Port: 68}
	b.ReportAllocs()
	for b.Loop() {
		s.handleDHCP(conn, raw, src)
	}
	if conn.n == 0 {
		b.Fatal("no offer written")
	}
}

func BenchmarkHandleBINL(b *testing.B) {
	s := benchServer(b)
	raw := craftRequestBench(msgREQUEST, 0x0007, true, nil)
	conn := &discardConn{}
	src := &net.UDPAddr{IP: net.IPv4(192, 168, 1, 42), Port: 4011}
	b.ReportAllocs()
	for b.Loop() {
		s.handleBINL(conn, raw, src)
	}
	if conn.n == 0 {
		b.Fatal("no ack written")
	}
}

// BenchmarkHandleDHCPWithLogging measures the same work with a real (JSON,
// discarded) handler rather than the quiet one, since production runs at
// slog Info and every handled packet logs a line.
func BenchmarkHandleDHCPWithLogging(b *testing.B) {
	s, err := New(Config{
		ServerIP: "192.168.1.10",
		Logger:   slog.New(slog.NewJSONHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelInfo})),
	})
	if err != nil {
		b.Fatal(err)
	}
	raw := benchDiscover()
	conn := &discardConn{}
	src := &net.UDPAddr{IP: net.IPv4(0, 0, 0, 0), Port: 68}
	b.ReportAllocs()
	for b.Loop() {
		s.handleDHCP(conn, raw, src)
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// BenchmarkGoroutinePerDatagram measures the dispatch overhead the serve loop
// adds per packet (copy + goroutine spawn + WaitGroup), for comparison against
// the handler cost above.
func BenchmarkGoroutinePerDatagram(b *testing.B) {
	raw := benchDiscover()
	b.ReportAllocs()
	for b.Loop() {
		pkt := make([]byte, len(raw))
		copy(pkt, raw)
		done := make(chan struct{})
		go func() { sinkBytes = pkt; close(done) }()
		<-done
	}
}

var (
	sinkPacket *packet
	sinkBytes  []byte
)
