package tftp

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TFTP opcodes (RFC 1350 §5).
const (
	opRRQ   = 1 // read request
	opWRQ   = 2 // write request (rejected — we are read-only)
	opDATA  = 3 // data block
	opACK   = 4 // acknowledgement
	opERROR = 5 // error
	opOACK  = 6 // option acknowledgement (RFC 2347)
)

// TFTP error codes (RFC 1350 §5).
const (
	errNotDefined      uint16 = 0
	errFileNotFound    uint16 = 1
	errAccessViolation uint16 = 2
	errIllegalOp       uint16 = 4
)

const (
	// defaultBlockSize is the RFC 1350 block size used when the client does not
	// negotiate a larger one. A final block shorter than the block size (0..blk-1
	// bytes) signals end-of-file.
	defaultBlockSize = 512
	// minBlockSize / maxBlockSize bound a negotiated blksize (RFC 2348).
	minBlockSize = 8
	maxBlockSize = 65464
	// maxRequestSize caps the initial RRQ/WRQ read. Requests are small even with
	// many options; anything larger is malformed.
	maxRequestSize = 1500

	transferTimeout = 5 * time.Second
	maxRetries      = 3
)

// Server serves files from a boot directory over TFTP. A zero Server is not
// usable; construct one with New. It is safe for concurrent transfers: each
// request is handled on its own goroutine and its own socket.
type Server struct {
	bootDir string
	logger  *slog.Logger
}

// New returns a Server that serves files rooted at bootDir. A nil logger falls
// back to slog.Default so library callers are never forced to thread one.
func New(bootDir string, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{bootDir: bootDir, logger: logger}
}

// ListenAndServe binds a UDP socket to addr and serves until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("tftp listen %s: %w", addr, err)
	}
	return s.Serve(ctx, conn)
}

// Serve reads requests from an already-bound conn until ctx is cancelled or a
// fatal read error occurs. Taking a net.PacketConn (rather than only an address)
// is what makes the server drive-testable: a test can bind 127.0.0.1:0, learn
// the port from conn.LocalAddr, and cancel ctx to shut it down cleanly.
//
// Cancelling ctx stops Serve accepting new requests, but transfers already in
// progress run to completion before it returns: a client booting from a 200 MB
// initrd must not have the file truncated because the operator restarted the
// service.
func (s *Server) Serve(ctx context.Context, conn net.PacketConn) error {
	s.logger.Info("TFTP listening", "addr", conn.LocalAddr(), "boot_dir", s.bootDir)

	// Cancelling ctx closes the conn, which unblocks the ReadFrom below with a
	// net.ErrClosed that we translate into a clean return.
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	// Each transfer owns a separate ephemeral socket (the TID), so closing the
	// listening conn above does not disturb one mid-flight. Waiting here is what
	// makes the drain real. It cannot hang on a client that vanishes: every
	// block is bounded by maxRetries retransmits of transferTimeout each, after
	// which the transfer gives up.
	var transfers sync.WaitGroup
	defer func() {
		s.logger.Info("TFTP draining in-flight transfers")
		transfers.Wait()
	}()

	buf := make([]byte, maxRequestSize)
	for {
		n, clientAddr, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				// A cancelled ctx closed the conn: clean shutdown, not an error.
				return nil
			}
			return fmt.Errorf("tftp read: %w", err)
		}
		if n < 4 {
			continue // too short to carry opcode + payload
		}
		// ReadFrom reuses buf; copy before handing to a goroutine.
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		transfers.Go(func() { s.handleRequest(pkt, clientAddr) })
	}
}

// handleRequest dispatches a single initial packet by opcode.
func (s *Server) handleRequest(pkt []byte, clientAddr net.Addr) {
	switch binary.BigEndian.Uint16(pkt[:2]) {
	case opRRQ:
		s.handleRRQ(pkt[2:], clientAddr)
	case opWRQ:
		s.sendError(clientAddr, errAccessViolation, "server is read-only")
	default:
		s.sendError(clientAddr, errIllegalOp, "unexpected opcode")
	}
}

// handleRRQ resolves the requested file and runs the full transfer on a fresh
// ephemeral socket (the transfer's TID).
func (s *Server) handleRRQ(data []byte, clientAddr net.Addr) {
	filename, mode, opts, err := parseRRQ(data)
	if err != nil {
		s.sendError(clientAddr, errIllegalOp, "malformed RRQ")
		return
	}
	if !strings.EqualFold(mode, "octet") {
		s.sendError(clientAddr, errIllegalOp, "only octet mode supported")
		return
	}

	safePath, err := s.resolvePath(filename)
	if err != nil {
		s.logger.Warn("TFTP access denied", "file", filename, "client", clientAddr)
		s.sendError(clientAddr, errAccessViolation, "access denied")
		return
	}

	f, err := os.Open(safePath)
	if err != nil {
		s.logger.Info("TFTP file not found", "file", filename, "client", clientAddr)
		s.sendError(clientAddr, errFileNotFound, "file not found")
		return
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		s.sendError(clientAddr, errNotDefined, "cannot stat file")
		return
	}
	fileSize := info.Size()

	blockSize := defaultBlockSize
	if bs, ok := opts["blksize"]; ok {
		if n, err := strconv.Atoi(bs); err == nil && n >= minBlockSize && n <= maxBlockSize {
			blockSize = n
		}
	}

	s.logger.Info("TFTP transfer starting",
		"file", filename, "client", clientAddr, "size", fileSize, "blksize", blockSize)

	// Every transfer gets its own socket on an OS-chosen ephemeral port. This is
	// the TFTP TID model, and it is why stateful firewalls need a TFTP ALG.
	xferConn, err := net.ListenPacket("udp", ":0")
	if err != nil {
		s.sendError(clientAddr, errNotDefined, "internal error")
		return
	}
	defer func() { _ = xferConn.Close() }()

	// If the client asked for any options, we must answer with an OACK (and only
	// the options we actually honor) before the first DATA block. The client
	// confirms by ACKing block 0.
	if len(opts) > 0 {
		oack := buildOACK(opts, blockSize, fileSize)
		if err := sendWithRetry(xferConn, clientAddr, oack, 0); err != nil {
			s.logger.Warn("TFTP OACK not acknowledged", "client", clientAddr, "err", err)
			return
		}
	}

	start := time.Now()
	if err := s.sendFile(xferConn, clientAddr, f, blockSize); err != nil {
		s.logger.Error("TFTP transfer failed", "file", filename, "client", clientAddr, "err", err)
		return
	}
	elapsed := time.Since(start)

	s.logger.Info("TFTP transfer complete",
		"file", filename, "client", clientAddr, "bytes", fileSize,
		"duration", elapsed.Round(time.Millisecond),
		"throughput_mbps", fmt.Sprintf("%.2f", throughputMBps(fileSize, elapsed)))
}

// sendFile streams the reader as DATA blocks, waiting for each ACK. A short
// final block (including a zero-length one when the size is an exact multiple of
// the block size) signals EOF.
func (*Server) sendFile(conn net.PacketConn, client net.Addr, r io.Reader, blockSize int) error {
	buf := make([]byte, blockSize)
	blockNum := uint16(1)

	for {
		n, err := io.ReadFull(r, buf)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return fmt.Errorf("reading file: %w", err)
		}

		isLast := n < blockSize // a partial (or empty) block is the EOF marker
		if err := sendWithRetry(conn, client, buildDATA(blockNum, buf[:n]), blockNum); err != nil {
			return err
		}
		if isLast {
			return nil
		}
		blockNum++ // wraps 65535 -> 0, which is legal for files > ~32 MB at blk 512
	}
}

// sendWithRetry writes pkt and waits for an ACK of expectedBlock, retransmitting
// on timeout up to maxRetries times. It ignores datagrams from any peer other
// than client (a wandering packet must not be mistaken for our ACK) and surfaces
// a client ERROR immediately.
func sendWithRetry(conn net.PacketConn, client net.Addr, pkt []byte, expectedBlock uint16) error {
	// Large enough to hold an ACK or a short ERROR message without truncation.
	buf := make([]byte, 516)

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if _, err := conn.WriteTo(pkt, client); err != nil {
			return fmt.Errorf("send: %w", err)
		}
		if err := conn.SetReadDeadline(time.Now().Add(transferTimeout)); err != nil {
			return fmt.Errorf("set deadline: %w", err)
		}

		// Drain packets until we see the ACK we want or the deadline fires.
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					break // retransmit
				}
				return fmt.Errorf("awaiting ACK %d: %w", expectedBlock, err)
			}
			if addr.String() != client.String() || n < 4 {
				continue // stray or runt packet
			}
			switch binary.BigEndian.Uint16(buf[:2]) {
			case opERROR:
				return fmt.Errorf("client error %d: %s",
					binary.BigEndian.Uint16(buf[2:4]), errMessage(buf[4:n]))
			case opACK:
				if binary.BigEndian.Uint16(buf[2:4]) == expectedBlock {
					return nil
				}
				// Duplicate/old ACK (e.g. the Sorcerer's Apprentice case); keep
				// waiting within this deadline rather than retransmitting.
			}
		}
	}
	return fmt.Errorf("timeout waiting for ACK of block %d after %d retries", expectedBlock, maxRetries)
}

// resolvePath maps a requested filename to an absolute path guaranteed to sit
// under bootDir. Prefixing with "/" before Clean collapses any ".." so a request
// like "../../etc/passwd" resolves under bootDir (and thus 404s) instead of
// escaping it; the explicit prefix check is a second line of defense.
func (s *Server) resolvePath(filename string) (string, error) {
	clean := filepath.Clean("/" + strings.TrimPrefix(filename, "/"))
	bootDirAbs, err := filepath.Abs(s.bootDir)
	if err != nil {
		return "", err
	}
	fullAbs, err := filepath.Abs(filepath.Join(bootDirAbs, clean))
	if err != nil {
		return "", err
	}
	if fullAbs != bootDirAbs && !strings.HasPrefix(fullAbs, bootDirAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal: %q", filename)
	}
	return fullAbs, nil
}

// sendError opens a throwaway socket to deliver a single ERROR packet. Used for
// failures detected on the main listener, before a transfer socket exists.
func (*Server) sendError(client net.Addr, code uint16, msg string) {
	conn, err := net.ListenPacket("udp", ":0")
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_, _ = conn.WriteTo(buildERROR(code, msg), client) //nolint:errcheck // best-effort ERROR delivery; there is no recovery path
}

// --- packet builders ---

func buildDATA(block uint16, data []byte) []byte {
	pkt := make([]byte, 4+len(data))
	binary.BigEndian.PutUint16(pkt[0:2], opDATA)
	binary.BigEndian.PutUint16(pkt[2:4], block)
	copy(pkt[4:], data)
	return pkt
}

func buildERROR(code uint16, msg string) []byte {
	pkt := make([]byte, 4+len(msg)+1) // trailing NUL already zero
	binary.BigEndian.PutUint16(pkt[0:2], opERROR)
	binary.BigEndian.PutUint16(pkt[2:4], code)
	copy(pkt[4:], msg)
	return pkt
}

// buildOACK echoes back only the options we support, with the values we chose.
// Omitting an option the client offered tells it we declined that one.
func buildOACK(opts map[string]string, negotiatedBlockSize int, fileSize int64) []byte {
	var b strings.Builder
	_ = binary.Write(&b, binary.BigEndian, uint16(opOACK))
	writeOpt := func(k, v string) {
		b.WriteString(k)
		b.WriteByte(0)
		b.WriteString(v)
		b.WriteByte(0)
	}
	if _, ok := opts["blksize"]; ok {
		writeOpt("blksize", strconv.Itoa(negotiatedBlockSize))
	}
	if _, ok := opts["tsize"]; ok {
		writeOpt("tsize", strconv.FormatInt(fileSize, 10))
	}
	if v, ok := opts["timeout"]; ok {
		writeOpt("timeout", v)
	}
	return []byte(b.String())
}

// parseRRQ parses an RRQ payload (everything after the opcode):
// filename\0mode\0[opt\0val\0]... All strings are NUL-terminated ASCII.
func parseRRQ(data []byte) (filename, mode string, opts map[string]string, err error) {
	parts := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	if len(parts) < 2 {
		return "", "", nil, errors.New("malformed RRQ: need filename and mode")
	}
	opts = make(map[string]string)
	for i := 2; i+1 < len(parts); i += 2 {
		if parts[i] != "" {
			opts[strings.ToLower(parts[i])] = parts[i+1]
		}
	}
	return parts[0], parts[1], opts, nil
}

// errMessage extracts the human-readable message from an ERROR packet body,
// trimming the trailing NUL.
func errMessage(b []byte) string {
	return strings.TrimRight(string(b), "\x00")
}

func throughputMBps(bytes int64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(bytes) / d.Seconds() / (1024 * 1024)
}
