package tftp

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// serveTestdir starts a server on an ephemeral loopback port and returns its
// address. The server stops when the test ends.
func serveTestdir(t *testing.T, dir string) *net.UDPAddr {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("LocalAddr is %T, want *net.UDPAddr", conn.LocalAddr())
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = New(Config{BootDir: dir, Logger: quietLogger()}).Serve(ctx, conn)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return addr
}

// bigFile writes a file large enough that a transfer would span many blocks, so
// an unbounded retransmit would be obvious in the byte count.
func bigFile(t *testing.T, name string, size int) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return dir
}

// A TFTP server is a natural UDP reflector: a small request from a forged source
// address makes it send a much larger reply to whoever that address names. What
// turned that from a nuisance into a weapon here was retransmission — booty
// re-sent the first block maxRetries times to a peer that had never answered,
// multiplying one forged ~50-byte datagram into four full blocks.
//
// This measures it the way an attacker would: send one RRQ, never reply, and
// count what comes back. The bound is bytes-out over bytes-in, and the point is
// that it stays around 1x rather than growing with the retry budget.
func TestAmplificationBoundedForSilentPeer(t *testing.T) {
	const blkSize = 1400
	dir := bigFile(t, "big.bin", 1<<20)
	srvAddr := serveTestdir(t, dir)

	victim, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("victim listen: %v", err)
	}
	defer func() { _ = victim.Close() }()

	// The victim asks for a large blksize and then goes silent — exactly what a
	// spoofed source address looks like from the server's side.
	rrq := buildRRQ("big.bin", map[string]string{"blksize": fmt.Sprint(blkSize)})
	if _, err := victim.WriteTo(rrq, srvAddr); err != nil {
		t.Fatalf("send RRQ: %v", err)
	}

	// One transferTimeout plus slack. That is deliberately short of the full
	// maxRetries budget: the old code sent block 1 again at t=transferTimeout, so
	// a single retransmit interval is all it takes to catch a regression, and
	// waiting out all maxRetries would add 15 seconds to every CI run.
	deadline := time.Now().Add(transferTimeout + 2*time.Second)
	if err := victim.SetReadDeadline(deadline); err != nil {
		t.Fatalf("set deadline: %v", err)
	}

	buf := make([]byte, 65536)
	var bytesOut, packets int
	for {
		n, _, err := victim.ReadFrom(buf)
		if err != nil {
			break // deadline: the server has stopped talking
		}
		bytesOut += n
		packets++
	}

	factor := float64(bytesOut) / float64(len(rrq))
	t.Logf("one %d-byte RRQ drew %d packet(s), %d bytes (%.1fx)", len(rrq), packets, bytesOut, factor)

	// Before the fix this was maxRetries+1 packets and ~121x.
	if packets > 1 {
		t.Errorf("server sent %d packets to a peer that never answered, want at most 1: "+
			"retransmitting to an unconfirmed address is the amplification", packets)
	}

	// Be precise about what this does and does not fix. Removing the
	// retransmits removes the multiplier, not reflection itself: booty still
	// answers one request with one reply, and an RRQ carrying no options is
	// answered with a 516-byte DATA block, which is ~26x a 20-byte request. A
	// server that refused to answer requests would not be a server. The residual
	// is why the guide tells operators to bind --tftp-addr to a provisioning
	// VLAN; the bound here exists to catch the multiplier coming back.
	if factor > 40 {
		t.Errorf("amplification %.1fx exceeds the 40x single-reply bound (%d bytes out for %d in)",
			factor, bytesOut, len(rrq))
	}
}

// A confirmed peer must still get the full retry budget: dropping retransmission
// altogether would make booty useless on a lossy link, which is the situation
// TFTP's stop-and-wait exists for. This pins that the fix is conditional on the
// peer having answered, not a blanket removal.
func TestConfirmedPeerStillGetsRetries(t *testing.T) {
	const blkSize = 512
	dir := bigFile(t, "big.bin", 8*blkSize)
	srvAddr := serveTestdir(t, dir)

	client, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("client listen: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Ask with an option so the exchange opens with an OACK we can ACK; that ACK
	// is what confirms the address.
	rrq := buildRRQ("big.bin", map[string]string{"blksize": fmt.Sprint(blkSize)})
	if _, err := client.WriteTo(rrq, srvAddr); err != nil {
		t.Fatalf("send RRQ: %v", err)
	}

	buf := make([]byte, 65536)
	if err := client.SetReadDeadline(time.Now().Add(transferTimeout)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	_, xferAddr, err := client.ReadFrom(buf)
	if err != nil {
		t.Fatalf("awaiting OACK: %v", err)
	}
	if op := binary.BigEndian.Uint16(buf[:2]); op != opOACK {
		t.Fatalf("first reply opcode = %d, want OACK (%d)", op, opOACK)
	}

	// ACK block 0 to confirm the address, then go silent. The server should now
	// retransmit block 1, because this peer has proven it exists.
	if _, err := client.WriteTo([]byte{0, opACK, 0, 0}, xferAddr); err != nil {
		t.Fatalf("send ACK 0: %v", err)
	}

	// One retransmit interval plus slack is enough to see the first retry.
	if err := client.SetReadDeadline(time.Now().Add(transferTimeout + 2*time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	var dataPackets int
	for dataPackets < 2 {
		n, _, err := client.ReadFrom(buf)
		if err != nil {
			break
		}
		if n >= 4 && binary.BigEndian.Uint16(buf[:2]) == opDATA {
			dataPackets++
		}
	}
	t.Logf("confirmed peer received %d DATA packets for block 1", dataPackets)
	if dataPackets < 2 {
		t.Errorf("confirmed peer got %d DATA packets, want the retransmits (>=2): "+
			"the retry budget must survive for peers that have answered", dataPackets)
	}

	// Abort rather than falling silent. Serve drains in-flight transfers before
	// returning, so leaving this one to run out its full retry budget would make
	// the test's cleanup wait maxRetries*transferTimeout for nothing. A client
	// ERROR ends the transfer at once — sendWithRetry surfaces it immediately.
	if _, err := client.WriteTo(buildERROR(errNotDefined, "test done"), xferAddr); err != nil {
		t.Fatalf("send abort: %v", err)
	}
}

// Every in-flight transfer holds an ephemeral socket for the length of its retry
// budget, so unanswered RRQs used to convert straight into held file descriptors
// and goroutines — 200 of them took the process from 3 goroutines to 204. The cap
// is what makes a flood shed instead of exhausting the fd table.
func TestConcurrentTransfersBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("floods the server with RRQs; skipped under -short")
	}
	dir := bigFile(t, "big.bin", 1<<20)
	srvAddr := serveTestdir(t, dir)

	sender, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("sender listen: %v", err)
	}
	defer func() { _ = sender.Close() }()

	before := runtime.NumGoroutine()

	// Comfortably more than the cap, and none of them will ever be answered.
	const flood = maxConcurrentTransfers * 3
	rrq := buildRRQ("big.bin", nil)
	for range flood {
		if _, err := sender.WriteTo(rrq, srvAddr); err != nil {
			t.Fatalf("send RRQ: %v", err)
		}
	}

	// Let the server take up as many as it is going to.
	time.Sleep(500 * time.Millisecond)
	peak := runtime.NumGoroutine() - before

	t.Logf("%d unanswered RRQs produced %d extra goroutines (cap %d)", flood, peak, maxConcurrentTransfers)

	// Each accepted transfer is one goroutine plus a little test/runtime noise.
	// The check that matters is that it tracks the cap rather than the flood.
	if peak > maxConcurrentTransfers*2 {
		t.Errorf("%d extra goroutines for %d requests exceeds the %d cap: "+
			"in-flight transfers are not bounded", peak, flood, maxConcurrentTransfers)
	}
}
