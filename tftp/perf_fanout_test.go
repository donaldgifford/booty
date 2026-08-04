package tftp

import (
	"context"
	"net"
	"os"
	"runtime"
	"testing"
	"time"
)

// TestFanoutHoldsSocketsPerUnansweredRRQ measures what one RRQ from a client
// that never ACKs costs the server, and for how long.
func TestFanoutHoldsSocketsPerUnansweredRRQ(t *testing.T) {
	if os.Getenv("BOOTY_FANOUT") == "" {
		t.Skip("set BOOTY_FANOUT=1")
	}
	dir := t.TempDir()
	writeBootFile(t, dir, "ipxe.efi", payload(1<<20))

	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = New(Config{BootDir: dir, Logger: quietLogger()}).Serve(ctx, conn); close(done) }()
	addr := conn.LocalAddr()

	base := runtime.NumGoroutine()
	cl, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	const n = 200
	rrq := buildRRQ("ipxe.efi", "octet", nil)
	start := time.Now()
	for range n {
		if _, err := cl.WriteTo(rrq, addr); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(500 * time.Millisecond)
	peak := runtime.NumGoroutine()
	t.Logf("after %d unanswered RRQs: goroutines %d -> %d (+%d)", n, base, peak, peak-base)

	// Wait for the server to give up on them all.
	for runtime.NumGoroutine() > base+5 {
		if time.Since(start) > 60*time.Second {
			t.Fatalf("still %d goroutines after 60s", runtime.NumGoroutine())
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Logf("server released them after %s", time.Since(start).Round(time.Second))
	_ = cl.Close()
	cancel()
	<-done
}
