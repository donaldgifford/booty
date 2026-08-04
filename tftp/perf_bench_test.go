package tftp

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// benchServer starts a server on loopback and returns its address.
func benchServer(b *testing.B, bootDir string) string {
	b.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = New(Config{BootDir: bootDir, Logger: quietLogger()}).Serve(ctx, conn)
		close(done)
	}()
	b.Cleanup(func() {
		cancel()
		<-done
	})
	return conn.LocalAddr().String()
}

// benchFile writes a file of n bytes into dir and returns its base name.
func benchFile(b *testing.B, dir string, n int) string {
	b.Helper()
	name := fmt.Sprintf("initrd-%d.bin", n)
	buf := make([]byte, 1<<20)
	for i := range buf {
		buf[i] = byte(i)
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		b.Fatal(err)
	}
	for w := 0; w < n; w += len(buf) {
		chunk := min(len(buf), n-w)
		if _, err := f.Write(buf[:chunk]); err != nil {
			b.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}
	return name
}

// benchGet is tftpGet without the reassembly: it counts bytes and discards
// them, so the measurement is the server's throughput, not the client's append.
func benchGet(serverAddr, filename string, blksize int) (int64, error) {
	raddr, err := net.ResolveUDPAddr("udp", serverAddr)
	if err != nil {
		return 0, err
	}
	cl, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = cl.Close() }()

	opts := map[string]string{}
	if blksize != defaultBlockSize {
		opts["blksize"] = strconv.Itoa(blksize)
	}
	rrq := buildRRQBench(filename, opts)
	if _, err := cl.WriteTo(rrq, raddr); err != nil {
		return 0, err
	}

	negotiated := defaultBlockSize
	var total int64
	var tid net.Addr
	buf := make([]byte, maxBlockSize+4)
	ackbuf := make([]byte, 4)
	binary.BigEndian.PutUint16(ackbuf[0:2], opACK)

	for {
		if err := cl.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return total, err
		}
		n, addr, err := cl.ReadFrom(buf)
		if err != nil {
			return total, err
		}
		tid = addr
		switch binary.BigEndian.Uint16(buf[:2]) {
		case opERROR:
			return total, fmt.Errorf("tftp error %d", binary.BigEndian.Uint16(buf[2:4]))
		case opOACK:
			if bs, ok := parseOACKOpts(buf[2:n])["blksize"]; ok {
				negotiated, _ = strconv.Atoi(bs)
			}
			binary.BigEndian.PutUint16(ackbuf[2:4], 0)
			if _, err := cl.WriteTo(ackbuf, tid); err != nil {
				return total, err
			}
		case opDATA:
			block := binary.BigEndian.Uint16(buf[2:4])
			payload := n - 4
			total += int64(payload)
			binary.BigEndian.PutUint16(ackbuf[2:4], block)
			if _, err := cl.WriteTo(ackbuf, tid); err != nil {
				return total, err
			}
			if payload < negotiated {
				return total, nil
			}
		}
	}
}

func buildRRQBench(filename string, opts map[string]string) []byte {
	b := make([]byte, 0, 64)
	b = binary.BigEndian.AppendUint16(b, opRRQ)
	b = append(b, filename...)
	b = append(b, 0)
	b = append(b, "octet"...)
	b = append(b, 0)
	for k, v := range opts {
		b = append(b, k...)
		b = append(b, 0)
		b = append(b, v...)
		b = append(b, 0)
	}
	return b
}

const benchFileSize = 32 << 20 // 32 MiB — a scale model of a 200 MB initrd

// BenchmarkTransferThroughput measures a full loopback transfer at each block
// size a real client might negotiate. Loopback RTT is far below a LAN's, so
// this is the server-side CPU/syscall ceiling, not what a rack will see.
func BenchmarkTransferThroughput(b *testing.B) {
	dir := b.TempDir()
	name := benchFile(b, dir, benchFileSize)
	addr := benchServer(b, dir)

	for _, blk := range []int{512, 1468, 8192, 65464} {
		b.Run(fmt.Sprintf("blksize=%d", blk), func(b *testing.B) {
			b.SetBytes(benchFileSize)
			b.ReportAllocs()
			for b.Loop() {
				got, err := benchGet(addr, name, blk)
				if err != nil {
					b.Fatal(err)
				}
				if got != benchFileSize {
					b.Fatalf("transferred %d bytes, want %d", got, benchFileSize)
				}
			}
		})
	}
}

// BenchmarkTransferConcurrent runs n transfers at once, the rack-boot shape.
func BenchmarkTransferConcurrent(b *testing.B) {
	dir := b.TempDir()
	const size = 8 << 20
	name := benchFile(b, dir, size)
	addr := benchServer(b, dir)

	for _, n := range []int{1, 8, 32} {
		b.Run(fmt.Sprintf("clients=%d", n), func(b *testing.B) {
			b.SetBytes(int64(size) * int64(n))
			b.ReportAllocs()
			for b.Loop() {
				errs := make(chan error, n)
				for range n {
					go func() {
						got, err := benchGet(addr, name, 1468)
						if err == nil && got != size {
							err = fmt.Errorf("transferred %d, want %d", got, size)
						}
						errs <- err
					}()
				}
				for range n {
					if err := <-errs; err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

// BenchmarkSendFileLocal isolates the server's per-block cost with a client
// that ACKs from a tight loop, so file reads, packet building and the ACK wait
// dominate rather than the client's own work.
func BenchmarkSendFileParts(b *testing.B) {
	b.Run("buildDATA", func(b *testing.B) {
		payload := make([]byte, 1468)
		b.ReportAllocs()
		for b.Loop() {
			sinkBytes = buildDATA(1, payload)
		}
	})
	b.Run("addrStringCompare", func(b *testing.B) {
		a := &net.UDPAddr{IP: net.IPv4(192, 168, 1, 42), Port: 55123}
		c := &net.UDPAddr{IP: net.IPv4(192, 168, 1, 42), Port: 55123}
		b.ReportAllocs()
		for b.Loop() {
			sinkBool = a.String() != c.String()
		}
	})
	b.Run("fileRead512", func(b *testing.B) {
		benchFileRead(b, 512)
	})
	b.Run("fileRead1468", func(b *testing.B) {
		benchFileRead(b, 1468)
	})
}

func benchFileRead(b *testing.B, blockSize int) {
	b.Helper()
	dir := b.TempDir()
	const size = 4 << 20
	name := benchFile(b, dir, size)
	path := filepath.Join(dir, name)
	buf := make([]byte, blockSize)
	b.SetBytes(size)
	b.ReportAllocs()
	for b.Loop() {
		f, err := os.Open(path)
		if err != nil {
			b.Fatal(err)
		}
		for {
			n, err := f.Read(buf)
			if n < blockSize || err != nil {
				break
			}
		}
		_ = f.Close()
	}
}

var (
	sinkBytes []byte
	sinkBool  bool
)
