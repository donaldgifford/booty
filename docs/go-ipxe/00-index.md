# Forge: A Ground-Up Guide to Network Booting

## How PXE, iPXE, TFTP, and cloud-init Actually Work

---

This guide is not primarily about Go. Go is the vehicle — the language we'll use to implement each piece — but the goal is to understand the protocols and systems deeply enough that you could implement them in any language, debug them at the wire level, and reason about failures without guessing.

By the end, you will have built `booty`: a real, tested network-boot service you
can drive a machine from. Along the way:

- A TFTP server from raw UDP sockets (`tftp`)
- A catalog + matcher that maps a booting machine to its desired state, authored
  in HCL (`catalog`)
- An iPXE HTTP boot endpoint that understands what iPXE is actually asking for
- A Talos-first render pipeline (machineconfig), with cloud-init and Proxmox
  automated-install answers as the other paths
- A Go *library* of public packages, with `cmd/booty` as its first consumer — one
  static binary, stdlib serving core, HCL for config

More importantly, you'll understand *why* each piece is the way it is — the design decisions, the historical context, the failure modes.

> **Rewrite complete.** This guide began as a `forge`/cloud-init walkthrough with
> illustrative, non-compiling snippets. Every chapter has now been rebuilt so it
> produces the real, compiling, tested `booty` code (Talos-first, catalog-based):
> Chapters 3–9 each build a public library package (`tftp`, `catalog`, `render`,
> `httpsrv`, `proxydhcp`) or `test/e2e`; Chapters 1, 2, and 10 are the map, the
> DHCP handoff, and the field guide. What the guide assembles is a *library* —
> `cmd/booty` is its first consumer, not the product
> ([ADR-0002](../adr/0002-booty-is-a-library-with-cmdbooty-as-reference-consumer.md)).
> The table below is the authoritative reading order; the Status column notes
> which chapters ship code.

---

## Why Build From Scratch?

You could reach for `pin/tftp`, wrap it in a handler, and have a working server in an afternoon. That's the right call in production. But doing it once from scratch is how you get the mental model that makes debugging fast: when a node hangs after DHCP and never requests `ipxe.efi`, you'll know whether to look at TFTP, at the DHCP `filename` option, or at the node firmware. When cloud-init fails silently, you'll know which endpoint it hit and what it expected to find there.

---

## Prerequisites

```bash
# Go 1.21+
go version

# Packet capture for debugging (you'll want this)
sudo apt install tcpdump wireshark
# or
brew install wireshark

# DHCP/TFTP test tools
sudo apt install tftp-hpa isc-dhcp-client
```

You need to know Go basics — goroutines, interfaces, error handling. The guide won't explain those. What it will explain is every protocol decision and wire format.

---

## Guide Structure

| Chapter | File | Status | What you'll understand / build |
|---------|------|--------|--------------------------------|
| 1. Boot sequence | `01-boot-sequence.md` | ✅ rewritten | the whole firmware→config ladder as a map, each step linked to its chapter |
| 2. DHCP & PXE | `02-dhcp-and-pxe.md` | ✅ rewritten + code | DHCP options, arch detection, and the spec-correct proxyDHCP + port-4011 handshake; builds `proxydhcp` |
| 3. TFTP from scratch | `03-tftp-from-scratch.md` | ✅ rewritten + code | TFTP wire format; builds & tests `tftp` |
| 4. iPXE deep dive | `04-ipxe-deep-dive.md` | ✅ rewritten + code | chain vs boot scripts, the `/ipxe` contract; builds `render` + `httpsrv` handlers |
| 5. Catalog & matcher | `05-catalog-and-matcher.md` | ✅ new + code | identity→group→profile matching; builds `catalog` (HCL) |
| 6. Render pipeline (Talos-first) | `06-render-pipeline.md` | ✅ new + code | machineconfig / cloud-init / Proxmox-answer renderers extend `render`; builds the `/machine-config`, `/cloud-init/*` + `/proxmox/answer` handlers |
| 7. HTTP serving core | `06-http-server-stdlib.md` | ✅ rewritten + code | the stdlib serving host: dependency-gated routing, middleware, context-driven lifecycle & timeouts; `httpsrv` |
| 8. Assembly & CLI | `07-forge-complete.md` | ✅ rewritten + code | thin `cmd/booty`: subcommand dispatch, `validate` CI gate, one context driving TFTP + HTTP + opt-in proxyDHCP, ldflags versioning |
| 9. QEMU / OVMF end-to-end | `09-qemu-e2e.md` | ✅ new + code | two-tier `test/e2e`: an always-on protocol tier + a real UEFI-VM QEMU tier (skips unequipped) |
| 10. Debugging field guide | `08-debugging-field-guide.md` | ✅ rewritten | validate→curl→logs→tcpdump loop; booty's real status/log diagnostic map & failure catalog |
| — | `05-cloud-init-spec.md` | legacy → folds into Ch 6 | cloud-init NoCloud model (kept until the render chapter absorbs it) |

---

## The Domain Model (Before We Start)

Read this diagram carefully. Every chapter implements one box or one arrow.

```
┌──────────────────────────────────────────────────────────────────────────┐
│                     Physical Server (powering on)                         │
│                                                                            │
│  ┌─────────────────────────────────────────────────────────────────────┐  │
│  │                        NIC Firmware (UEFI/BIOS)                      │  │
│  │                                                                      │  │
│  │  1. Broadcasts DHCP DISCOVER on UDP 68→67                           │  │
│  │     "I am MAC 52:54:00:ab:cd:ef, I need an IP and boot instructions" │  │
│  │                                                                      │  │
│  │  2. Receives DHCP OFFER                                              │  │
│  │     "Your IP is 192.168.1.103, get ipxe.efi from 192.168.1.10"      │  │
│  │                                                                      │  │
│  │  3. Downloads ipxe.efi via TFTP from 192.168.1.10:69                │  │
│  │     "512 bytes at a time, ACK each block, stop when block < 512"     │  │
│  │                                                                      │  │
│  │  4. Executes ipxe.efi — firmware hands control to iPXE              │  │
│  └─────────────────────────────────────────────────────────────────────┘  │
│                                                                            │
│  ┌─────────────────────────────────────────────────────────────────────┐  │
│  │                          iPXE (running)                              │  │
│  │                                                                      │  │
│  │  5. HTTP GET /boot.ipxe — the chain script, which reads iPXE's      │  │
│  │     built-in settings (${mac}, ${uuid}, ${buildarch}…)               │  │
│  │                                                                      │  │
│  │  6. HTTP GET /ipxe?mac=52:54:00:ab:cd:ef&arch=…                     │  │
│  │     "Here's who I am — give me a boot script"                        │  │
│  │     booty matches identity → group → profile, renders the script     │  │
│  │                                                                      │  │
│  │  7. HTTP GET /boot/vmlinuz (kernel, ~12MB)                          │  │
│  │     HTTP GET /boot/initrd.xz (initramfs, ~80MB)                     │  │
│  │                                                                      │  │
│  │  8. Executes kernel — iPXE exits, kernel takes over                 │  │
│  └─────────────────────────────────────────────────────────────────────┘  │
│                                                                            │
│  ┌─────────────────────────────────────────────────────────────────────┐  │
│  │                     Linux Kernel (running)                           │  │
│  │                                                                      │  │
│  │  9. Mounts initramfs, runs /init                                     │  │
│  │  10. Configures network (from kernel cmdline ip=dhcp)               │  │
│  │  11. The OS pulls its per-machine config from booty — one of:       │  │
│  │                                                                      │  │
│  │      Talos:      GET /machine-config?mac=…                          │  │
│  │                  "You are talos-cp-1 in cluster homelab"             │  │
│  │      cloud-init: GET /cloud-init/{meta-data,user-data,vendor-data}  │  │
│  │                  (sends no identity — booty matches by source IP)    │  │
│  │      Proxmox:    POST /proxmox/answer (installer sends DMI + NICs   │  │
│  │                  as JSON; booty returns the node's answer.toml)      │  │
│  │                                                                      │  │
│  │  12. The OS applies its config. Node is provisioned. Boot complete. │  │
│  └─────────────────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────────────┘

What booty (your server) provides:
  UDP :67/:4011 →  proxyDHCP: boot instructions alongside your existing DHCP
  UDP :69       →  TFTP: serves ipxe.efi
  HTTP :8080    →  GET  /boot.ipxe        chain script (collects identity)
                   GET  /ipxe?mac=…       per-machine iPXE boot script
                   GET  /boot/*           kernel, initrd, boot assets
                   GET  /machine-config   Talos machineconfig
                   GET  /cloud-init/*     NoCloud meta-/user-/vendor-data
                   POST /proxmox/answer   Proxmox automated-install answer
```

---

## A Note on Standards

Each technology in this stack has a spec. We'll reference them throughout:

- **TFTP**: [RFC 1350](https://datatracker.ietf.org/doc/html/rfc1350) (1992) — short, readable, worth reading in full
- **DHCP PXE extensions**: [RFC 4578](https://datatracker.ietf.org/doc/html/rfc4578)
- **iPXE scripting**: [ipxe.org/scripting](https://ipxe.org/scripting)
- **cloud-init NoCloud source**: [cloudinit.readthedocs.io](https://cloudinit.readthedocs.io/en/latest/reference/datasources/nocloud.html)
- **cloud-init config reference**: [cloudinit.readthedocs.io/reference](https://cloudinit.readthedocs.io/en/latest/reference/modules.html)

Start with **[Chapter 1: The Boot Sequence →](./01-boot-sequence.md)**
