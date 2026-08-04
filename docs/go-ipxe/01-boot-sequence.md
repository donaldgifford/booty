# Chapter 1: The Boot Sequence

← [Index](./00-index.md) | [Chapter 2: DHCP and PXE →](./02-dhcp-and-pxe.md)

---

This chapter is the map. Before we build any one piece, you need the whole
sequence in your head — what code is running at each step, what it knows, and what
it needs from the network — because every later chapter implements exactly one box
or one arrow of it. Where a step names a booty component, it links to the chapter
that builds it.

## Before the OS exists

When a server powers on, no operating system is running. The CPU executes from a
fixed ROM address — the UEFI firmware. The firmware's whole job is to find
something bootable and jump to it. On a machine with no OS on disk (or set to
network-boot first), that something arrives over the wire, and the firmware
bootstraps itself up a ladder of increasingly capable environments:

```text
NIC firmware  →  iPXE  →  Linux kernel  →  the provisioned OS
 (tiny, TFTP)   (HTTP,    (real drivers,   (Talos / Ubuntu / …)
                scripts)   networking)
```

Each rung exists because the one below it is too limited to do the next step. That
laddering — and *why* each rung is necessary — is the real subject of this guide.

## Step 1: firmware and the NIC option ROM

UEFI enumerates boot devices. When the NIC is next, it runs the NIC's **option
ROM** — a small PXE client baked into the card. It knows exactly one thing about
itself: its MAC address. It knows no IP, no DNS, no boot server. Its only move is
to ask the network, via DHCP.

The architecture matters for what booty must serve:

- **Legacy BIOS/PXE** — a 16-bit real-mode ROM, speaks only original PXE + TFTP.
  Wants a `undionly.kpxe`-style boot file.
- **UEFI PXE** — a 64-bit UEFI driver, can load `.efi` binaries directly. Wants
  `ipxe.efi`. This is booty's primary target.
- **UEFI HTTP Boot** — some firmware boots straight from HTTP, no TFTP. The future,
  but TFTP remains the baseline booty guarantees.

booty picks the right boot file from the client's advertised architecture
([Chapter 2](./02-dhcp-and-pxe.md)).

## Step 2: DHCP — an IP, and where to boot

The client broadcasts a **DHCP DISCOVER** from `0.0.0.0:68` to `255.255.255.255:67`
— no IP yet, so it shouts to the whole LAN. It tags itself with option 60
(`PXEClient:Arch:00007:UNDI:…`) and its architecture in option 93.

Two things must come back: an **IP address**, and **boot instructions**
(`next-server` = which server, `bootfile` = which file). They can come from one
server or two:

- The existing DHCP server supplies both (you add the boot options to it), **or**
- The DHCP server supplies just the IP, and **booty's proxyDHCP** supplies the boot
  instructions alongside it — without owning DHCP.

That second path is how booty drops into a network without touching the router,
and it's a proper PXE handshake with a second exchange on port 4011, not a
shortcut — the whole of [Chapter 2](./02-dhcp-and-pxe.md).

```text
DHCP result the client ends up with:
  Your IP:      192.168.1.111
  Next server:  192.168.1.10     ← booty
  Boot file:    ipxe.efi         ← what to TFTP
```

Why two fields (`next-server` + `filename`) instead of one URL? Because TFTP
predates URLs: you address a TFTP server by IP (DNS isn't up yet) and then name a
file. That split is a fossil we live with.

## Step 3: TFTP — the one protocol the firmware can speak

With a server IP and a filename, the NIC ROM makes a **TFTP** read request (UDP
port 69). TFTP is deliberately tiny — RFC 1350 is ten pages — because it has to fit
in a few KB of firmware:

```text
Client → :69   RRQ "ipxe.efi"
Server → Client DATA block 1 (512 bytes)   Client → ACK 1
Server → Client DATA block 2 (512 bytes)   Client → ACK 2
… until a block < 512 bytes, which signals end-of-file …
```

Two details that cause real bugs: the transfer moves to a fresh server port after
the first packet (the transfer ID), and a file that's an exact multiple of 512
needs a final zero-byte block to signal EOF. booty implements all of this — the
wire format, option negotiation, the traversal guard — from raw UDP in
[Chapter 3](./03-tftp-from-scratch.md).

The firmware loads `ipxe.efi` into memory and jumps to it. **The NIC ROM's job is
now done; iPXE is running.**

## Step 4: iPXE takes over — and the two-script model

iPXE is a real boot environment: HTTP(S), a scripting language, menus, signature
checks. It's how a 1981 protocol bootstraps a 2020s capability.

Here's the step everyone gets wrong the first time. iPXE does **not** magically
tell booty who it is. A stock `ipxe.efi` fetches one URL and sends no identity. So
a booting machine actually runs **two** scripts:

1. A **chain script** (served at `/boot.ipxe`) — tiny, static, run first. Its only
   job is to read iPXE's built-in settings (`${mac}`, `${uuid}`, `${buildarch}`…)
   and pass them to booty as query parameters.
2. A **per-machine boot script** (served at `/ipxe?mac=…`) — dynamic, rendered from
   whatever profile booty's catalog matches for that identity.

```text
iPXE → GET /boot.ipxe               (chain script: collects identity)
iPXE → GET /ipxe?mac=…&arch=…       (booty matches identity → boot script)
```

Getting this split right — and understanding that `${…}` is iPXE's and `{{…}}` is
booty's template engine — is [Chapter 4](./04-ipxe-deep-dive.md). *Which* profile a
machine resolves to is the catalog and matcher of
[Chapter 5](./05-catalog-and-matcher.md).

## Step 5: kernel and initrd over HTTP

The boot script points iPXE at a kernel (`vmlinuz`) and an initramfs
(`initramfs.xz`), fetched over **HTTP** from booty's `/boot/…` endpoint. These are
big (tens to hundreds of MB), and HTTP's full TCP throughput is why the boot staged
through TFTP only long enough to get iPXE, then switched. iPXE jumps to the kernel,
passing the command line the script set. **The firmware and iPXE are now gone; the
CPU is running Linux.** booty serves these files in
[Chapter 4](./04-ipxe-deep-dive.md).

## Step 6: the kernel, and the config pull

The kernel decompresses, brings up hardware, mounts the initramfs, and reads its
command line — which booty put there. The key arguments decide what happens next:

- `console=ttyS0,115200n8` — send output to serial (essential on headless metal).
- `ip=dhcp` — bring up networking.
- **`talos.config=http://booty/machine-config?mac=${mac}`** — Talos's config URL.
- `ds=nocloud-net;s=http://booty/cloud-init/` — cloud-init's data source.
- `proxmox-start-auto-installer` — puts the Proxmox installer in automated mode
  (its answer URL is baked into the initrd at prepare time, not the cmdline).

booty is **Talos-first**: the primary path is Talos fetching its machine
configuration, with cloud-init and the Proxmox automated installer as the other
paths.

## Step 7: the machine gets its identity

Now the booted OS phones home for its per-machine configuration — the last arrow:

- **Talos** pulls its machineconfig from `/machine-config?mac=…`. iPXE already
  substituted the MAC into that URL, so identity arrives as a query parameter, and
  booty renders the node's Talos config (hostname, cluster, install disk).
- **cloud-init** (NoCloud) instead hits `/cloud-init/meta-data`, `/user-data`,
  `/vendor-data` — sending *no* identity, so booty must recognize it by source IP.
- **Proxmox** (automated install) goes furthest: it **POSTs** its full system
  description — DMI identity plus every NIC's MAC — to `/proxmox/answer` and gets
  its install answer file back.

All three renderers, and why Talos leads, are [Chapter 6](./06-render-pipeline.md); the
HTTP server hosting every endpoint above is [Chapter 7](./06-http-server-stdlib.md),
assembled into the `booty` binary in [Chapter 8](./07-forge-complete.md).

## The whole sequence as a state machine

Debugging a boot is finding which transition didn't happen. This is the frame the
field guide ([Chapter 10](./08-debugging-field-guide.md)) is built on:

```text
State            Who's running     What it needs           booty endpoint
──────────────────────────────────────────────────────────────────────────
POWER ON         NIC firmware      —                       —
DHCP             NIC firmware      IP + boot instructions  proxyDHCP (:67/:4011)
TFTP             NIC firmware      ipxe.efi                TFTP :69
iPXE CHAIN       iPXE              the chain script        GET /boot.ipxe
iPXE RESOLVE     iPXE              a per-machine script    GET /ipxe?mac=…
KERNEL DOWNLOAD  iPXE              vmlinuz + initramfs      GET /boot/…
KERNEL BOOT      Linux            (network)                —
CONFIG PULL      Talos/cloud-init machine config           GET /machine-config
                 /Proxmox                                  GET /cloud-init/*
                                                           POST /proxmox/answer
COMPLETE         your OS          the workload             —
```

Prove the whole chain works — hermetically and in a real VM — in
[Chapter 9](./09-qemu-e2e.md).

## Why the architecture is shaped this way

The staged ladder — firmware → TFTP → iPXE → HTTP → config — isn't arbitrary; each
rung answers a constraint of the one below:

- **Why TFTP first?** The NIC ROM is too small for a TCP/HTTP stack. TFTP over UDP
  fits in a few KB. It's the largest capability that fits in the smallest place.
- **Why iPXE in the middle?** The NIC ROM is read-only and vendor-specific.
  Chainloading open-source iPXE gives one consistent, capable boot environment
  (HTTP, scripts) across wildly different hardware.
- **Why HTTP for the big files?** TFTP is stop-and-wait, one block per round trip;
  an 80 MB initrd over it takes minutes. HTTP is full TCP throughput.
- **Why pull config as a separate step?** One generic kernel/initramfs boots every
  node; the per-machine identity (hostname, cluster, keys) is fetched afterward.
  You maintain one image, not one per machine — and the thing that maps a machine
  to its config is booty's whole reason to exist.

With the map in hand, we start at the bottom of the ladder: the DHCP handoff that
turns "a MAC on the wire" into "an IP and a boot file."

---

← [Index](./00-index.md) | [Chapter 2: DHCP and PXE →](./02-dhcp-and-pxe.md)
