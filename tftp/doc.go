// Package tftp implements a minimal, read-only TFTP server (RFC 1350).
//
// It negotiates the blksize, tsize, and timeout options (RFC 2347–2349) and
// uses only the Go standard library — the wire format is built from raw UDP.
//
// TFTP is the one protocol booty must speak at the firmware stage of a network
// boot: a UEFI/BIOS PXE client that has just learned "next-server + filename"
// from DHCP can only fetch that file over TFTP — its firmware has no TCP/HTTP
// stack. booty serves the iPXE binary (and, in constrained environments,
// kernels/initrds) here, then gets out of the way once iPXE takes over on HTTP.
//
// The design follows the TFTP transfer-identifier (TID) model: the main socket
// on :69 receives only the initial RRQ, and every transfer then moves to its
// own ephemeral socket.
//
// Serving is read-only. Write requests are rejected, and paths are resolved
// under the configured root with a traversal guard.
//
// # Usage
//
//	srv := tftp.New(tftp.Config{BootDir: "/var/lib/booty/boot"})
//	if err := srv.ListenAndServe(ctx, ":69"); err != nil {
//		return err
//	}
//
// Port 69 is privileged. Bind a high port and redirect, or grant
// CAP_NET_BIND_SERVICE — the distroless image runs as UID 65532 and cannot bind
// it unaided.
//
// [Server.Serve] takes an already-bound net.PacketConn instead, which is what
// makes the server testable: bind 127.0.0.1:0, read the port back from
// conn.LocalAddr, and cancel ctx to shut down.
//
// Wire-level walkthrough:
// https://github.com/donaldgifford/booty/blob/main/docs/go-ipxe/03-tftp-from-scratch.md
package tftp
