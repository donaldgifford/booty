// Package proxydhcp implements a PXE proxyDHCP service, also known as BINL.
//
// It answers a PXE client's boot questions WITHOUT handing out IP addresses,
// so it coexists with an existing DHCP server (a router, a homelab appliance)
// that owns the leases. booty runs it so a bare NIC ROM can find and load
// ipxe.efi with no changes to the network's DHCP.
//
// It implements the spec-correct two-phase PXE Boot Server Discovery from the
// Intel PXE 2.1 specification, not the eager shortcut:
//
//  1. Port 67 — the client broadcasts a DHCPDISCOVER tagged option 60 =
//     "PXEClient". The real DHCP server answers with an IP; booty answers with a
//     proxy DHCPOFFER (yiaddr = 0, no bootfile) whose option 43 carries
//     PXE_DISCOVERY_CONTROL + a PXE_BOOT_SERVERS list pointing at booty. The
//     discovery-control byte deliberately does NOT set the "download immediately"
//     bit, so the client is required to go to phase 2 rather than boot from the
//     offer.
//  2. Port 4011 (BINL) — the client unicasts a Boot Server Request to booty. booty
//     replies with a Boot Server ACK that finally names the boot file (arch-picked
//     ipxe.efi) in the DHCP "file" field, with siaddr = booty's IP. The client
//     then fetches that file over TFTP and runs it.
//
// The socket and broadcast handling lives in ListenAndServe; the packet logic
// lives in side-effect-free helpers (parsePacket and the buildProxyOffer and
// buildBootAck builders) that are unit tested byte-for-byte against the spec,
// since real-firmware interop is exercised by the QEMU tier of the e2e harness.
//
// # Usage
//
//	srv, err := proxydhcp.New(proxydhcp.Config{ServerIP: "192.168.1.10"})
//	if err != nil {
//		return err // ServerIP must be a routable IPv4 the client can reach
//	}
//	if err := srv.ListenAndServe(ctx, ":67", ":4011"); err != nil {
//		return err
//	}
//
// Both ports are privileged, and the :67 socket needs SO_BROADCAST — a client
// with no address yet can only be reached by broadcast, and Go does not set
// that flag by default. ListenAndServe handles it; a caller binding its own
// sockets and driving [Server.ServeDHCP] / [Server.ServeBINL] must do it
// itself, or the reply fails with EACCES.
//
// Running this at all means answering DHCPDISCOVER on a network someone else's
// DHCP server owns. booty never offers an address — it only steers PXE clients
// — but a second responder on the segment is still an operational decision, not
// a default. That is why cmd/booty gates it behind --proxydhcp.
//
// Wire-level walkthrough:
// https://github.com/donaldgifford/booty/blob/main/docs/go-ipxe/02-dhcp-and-pxe.md
package proxydhcp
