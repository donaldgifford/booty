---
id: INV-0001
title: "Talos boot-chain gaps: machineconfig secrets, iPXE chainload loop, iPXE binary provenance"
status: Open
author: Donald Gifford
created: 2026-08-16
---
<!-- markdownlint-disable-file MD025 MD041 -->

# INV 0001: Talos boot-chain gaps: machineconfig secrets, iPXE chainload loop, iPXE binary provenance

**Status:** Open
**Author:** Donald Gifford
**Date:** 2026-08-16

<!--toc:start-->
- [Question](#question)
- [Hypothesis](#hypothesis)
- [Context](#context)
- [Approach](#approach)
- [Environment](#environment)
- [Findings](#findings)
  - [Observation 1: the embedded Talos templates cannot form a cluster](#observation-1-the-embedded-talos-templates-cannot-form-a-cluster)
  - [Observation 2: proxyDHCP does not break the iPXE chainload loop](#observation-2-proxydhcp-does-not-break-the-ipxe-chainload-loop)
  - [Observation 3: the iPXE binaries are the operator's problem](#observation-3-the-ipxe-binaries-are-the-operators-problem)
  - [Observation 4: proxy offers egress the default-route interface on multi-homed hosts](#observation-4-proxy-offers-egress-the-default-route-interface-on-multi-homed-hosts)
  - [Observation 5: boot-item type 0 means "boot local" to UEFI firmware](#observation-5-boot-item-type-0-means-boot-local-to-uefi-firmware)
- [Conclusion](#conclusion)
- [Recommendation](#recommendation)
- [References](#references)
<!--toc:end-->

## Question

booty's boot chain was validated against the Sidero Labs [Matchbox] and [PXE]
bare-metal guides (2026-08-16): the flow works end to end, but three gaps stand
between "the e2e test passes" and "an operator builds a real Talos cluster with
`booty serve` and nothing else". For each, what is the right fix, and is it
worth its dependency cost?

1. **Machineconfig secrets.** Can `siderolabs/talos/pkg/machinery` generate
   complete machineconfigs (secrets bundle, PKI, join tokens) inside `render`,
   replacing the deliberately secret-free embedded templates — without dragging
   in a dependency graph that swamps a library whose whole surface is five
   packages?
2. **Chainload loop.** Should `proxydhcp` detect an already-running iPXE
   (DHCP option 77 user-class `"iPXE"`) and answer with the `/boot.ipxe` HTTP
   URL instead of the binary, removing the manual EMBED / option-175 step?
3. **Binary provenance.** Should booty ship or fetch pinned `ipxe.efi` /
   `undionly.kpxe` binaries (via `tinkerbell/ipxedust` or a CI build) instead
   of telling the operator to `wget boot.ipxe.org`?

## Hypothesis

1. machinery can do it, but the transitive graph is the real question — the
   library's `go.mod` currently carries two direct dependencies (`hcl`,
   `go-cty`) and PLAN-0001 already flags machinery as "heavy". Expected
   outcome: adopt it behind its own package (or build tag) so importers who
   don't render Talos configs don't pay for it.
2. Yes, and cheaply — it is a small packet-inspection branch in
   `handleDHCP`/`handleBINL`, the same trick every dnsmasq+Matchbox setup
   encodes as `dhcp-match=set:ipxe,175` config. Expected outcome: implement,
   with the embedded-script path remaining supported for setups where the real
   DHCP server owns the decision.
3. Unclear. ipxedust solves provenance but brings server wiring booty doesn't
   want; building iPXE in CI means owning a submodule + cross-compile matrix.
   Expected outcome: this hinges on whether ipxedust's binaries are consumable
   as a data-only dependency.

## Context

**Triggered by:** the 2026-08-16 validation of booty against the Sidero Labs
Matchbox and PXE guides, on the back of [IMPL-0001] (v0.1.0/v0.2.0 shipped).
[PLAN-0001] already tables `siderolabs/talos/pkg/machinery` ("Evaluate —
highest priority") and `tinkerbell/ipxedust` ("Evaluate"); this INV is the
evaluation those rows call for, plus the chainload-loop question the validation
surfaced. The quick fix from the same validation — the mandatory
`init_on_alloc=1 slab_nomerge pti=on` kernel args missing from
`examples/catalog` — landed alongside this document and is *not* part of the
investigation.

## Approach

1. **machinery spike.** Branch; `go get siderolabs/talos/pkg/machinery` pinned
   to a current Talos minor. Measure: `go mod graph | wc -l` delta, `go build`
   time delta, binary size delta. Generate a controlplane + worker config with
   a secrets bundle; feed both to `talosctl validate --mode metal` and diff
   against `talosctl gen config` output. Decide package shape (new
   `render/talos` vs inside `render`) per ADR-0002's "a real consumer's need
   drives every addition".
2. **Secrets-at-rest design sketch — spun out to
   [INV-0002](0002-secrets-backends-and-machine-config-verification-for-the-booty.md)
   (2026-08-16).** The questions this step listed (secret inputs, serving
   posture, `/machine-config` bearer auth) grew their own decision surface:
   secret-backend interface, module posture, and a decided tier model. The
   short version relevant here: `/machine-config` gets a `--proxmox-token`-style
   static token (tier 1), and the default stays Matchbox-parity plaintext.
3. **user-class loop-break.** Packet-capture a QEMU boot (the e2e QEMU tier
   already drives OVMF): confirm the re-DHCP DISCOVER carries option 77
   `"iPXE"` and option 175. Prototype the branch in `handleDHCP`/`handleBINL`:
   user-class iPXE → bootfile = `{BaseURL}/boot.ipxe` (HTTP URL in the
   bootfile field is standard iPXE behaviour). Extend the e2e QEMU tier to
   boot a *stock* ipxe.efi with no embedded script — the loop-break is proven
   when that VM still reaches `/ipxe`.
4. **ipxedust consumption test.** Can its embedded binaries be imported as
   `embed.FS` data without its server packages? Check licence, update cadence
   vs iPXE upstream, and binary size added to booty's own embed. Compare
   against a `docker run` CI build of pinned iPXE with `EMBED=chain.ipxe`.
   *Narrowed 2026-08-16 (INV-0002 decisions): booty will not be a build
   service — owning an iPXE build pipeline is out of scope, so the CI-build
   comparison is a baseline to measure against, not a candidate outcome.
   Pinning and verifying an upstream-built binary (ipxedust or checksummed
   release) is the space of acceptable answers. This also caps the loop-break
   options in step 3: solutions requiring a custom EMBED build are out, which
   makes the user-class DHCP answer the primary path rather than one of two.*
5. Write up per-question conclusions; spin out DESIGN docs for whatever
   survives.

## Environment

| Component | Version / Value |
|-----------|----------------|
| booty | v0.2.0 (`main` @ post-#19) |
| Talos guides | v1.13 (Matchbox + PXE bare-metal) |
| Go | 1.26.5 |
| e2e reference | `test/e2e` QEMU tier (OVMF, UEFI) |

## Findings

Observations 1–3 are from the validation read of the code and guides; the
experiments in [Approach](#approach) have not run yet. Observations 4–5 are
from the first live field test (2026-08-16): a Proxmox OVMF (EDK2) VM
PXE-booted against booty v0.2.0 on a dual-NIC docker host, through a real
UniFi-gateway DHCP environment, to a bootstrapped single-node Talos v1.13.8
cluster (`kubectl get nodes` → `Ready`). The full chain — proxyDHCP offer,
BINL boot-ack, TFTP of an embedded-script `ipxe.efi`, HTTP boot script and
assets, overlay-template `/machine-config` — worked end to end *after* the
two bugs below were found and dealt with; neither is reachable by the e2e
protocol tier, because both live in the gap between booty's packets being
well-formed and a real firmware/kernel acting on them.

### Observation 1: the embedded Talos templates cannot form a cluster

`render/templates/talos/_common.yaml.tmpl` says it outright: *"secrets omitted
— the machinery secrets bundle supplies machine.token, PKI, cluster.id/secret
in production (deferred)"*. A node booted from the embedded templates gets a
syntactically valid machineconfig with no join token and no CA — it cannot
join anything. The guides' flow works because `talosctl gen config` emits
complete configs; booty's equivalent today is the operator serving those files
as static boot assets (`talos.config=…/boot/configs/controlplane.yaml`) or
overriding the templates wholesale via `--templates-dir`. Both work; neither
is "booty renders per-machine Talos configs", which is the library's stated
primary path.

### Observation 2: proxyDHCP does not break the iPXE chainload loop

`proxydhcp.handleDHCP` answers every PXE DISCOVER with the arch-appropriate
binary (`uefiArch` map → `ipxe.efi` / `undionly.kpxe`) unconditionally. A
stock iPXE, once loaded, re-DHCPs — and is handed itself again, forever. The
documented escape hatches (chapter 04): embed `chain.ipxe` into the binary at
build time, or have the *real* DHCP server serve option 175 to iPXE clients.
Both push per-site work onto the operator that dnsmasq-based Matchbox setups
express in two config lines, and the second assumes the operator controls the
DHCP server at all — the one assumption proxyDHCP exists to avoid.

Concrete instance (2026-08-16): the reference deployment's DHCP is a UniFi
UCG Fiber, whose UI exposes at most a static boot server/file per network —
it cannot express "iPXE user-class gets the script URL". For that class of
gateway the DHCP-side workaround does not exist, leaving only the
embedded-script binary (netboot.xyz's model). booty answering the user-class
itself is therefore the zero-config path for the hardware this project
actually runs on, not merely a convenience.

### Observation 3: the iPXE binaries are the operator's problem

booty serves whatever is in `--boot-dir` but ships no iPXE binaries, and the
guide's interim advice is `wget https://boot.ipxe.org/ipxe.efi` — an unpinned,
unverified binary that every machine on the network subsequently executes.
Observation 2 compounds this: the clean loop-free path today is an *embedded*
build, which `wget` cannot provide. PLAN-0001's row for `tinkerbell/ipxedust`
("Consuming assets without their server wiring; provenance trust") is exactly
this question and remains unevaluated.

### Observation 4: proxy offers egress the default-route interface on multi-homed hosts

`handleDHCP` sends the phase-1 proxy OFFER to `255.255.255.255:68` on the
socket bound to `0.0.0.0:67`. On Linux, a limited-broadcast send from an
unbound socket follows the routing table, and the route for
`255.255.255.255/32` resolves via the *default route* — so on a host with
two NICs, every proxy offer leaves through the default-route interface
regardless of which interface heard the DISCOVER. Field symptom (maximally
misleading): booty logs a healthy `proxyDHCP offer` burst while the client
reports `PXE-E16: No valid offer received` — the log records the *send call*,
not delivery, and the packet is exiting the wrong port. Phase-2 BINL, TFTP,
and HTTP are unicast replies and route correctly; only the phase-1 broadcast
is affected, so the failure is invisible to every later stage.

Workaround (host-level, not persistent across reboots unless added to the
host's network config): `ip route add 255.255.255.255/32 dev <pxe-iface>`.

Proper fix (open): reply out the interface the DISCOVER arrived on via
`IP_PKTINFO` control messages (`golang.org/x/net/ipv4`), or an explicit
`--interface` bind. The user-class loop-break prototype (Approach step 3)
touches the same read/write path and should land on top of whichever shape
this takes.

### Observation 5: boot-item type 0 means "boot local" to UEFI firmware

`pxeBootServerType` was `0`, used consistently across `PXE_BOOT_SERVERS`,
`PXE_BOOT_MENU`, and the `PXE_BOOT_ITEM` echo — the comment called the value
arbitrary. It is not: in the PXE menu convention, boot-server type 0 is the
"local boot" sentinel (dnsmasq documents `pxe-service` type 0 as "abort net
boot and continue from local media"), and EDK2 enforces it literally —
`PxeBcBoot.c` returns `EFI_ABORTED` when the selected menu item has type
`EFI_PXE_BASE_CODE_BOOT_TYPE_BOOTSTRAP` (0). Field symptom: DHCP completes
("Station IP address is …"), then `PXE-E21: Remote boot cancelled` — booty's
own offer instructed the firmware to cancel booty. Fixed alongside this
observation: the constant is now a non-zero type (`1`); the semantic names in
the spec's type table are ignored by clients, only the zero/non-zero
distinction has behaviour attached. BIOS ROM path unaffected (the arch split
in `bootFile` never keyed off the type).

## Conclusion

**Answer:** <!-- pending — one conclusion per question after the Approach runs -->

## Recommendation

Pending the conclusion. Expected shape: a DESIGN doc per adopted change
(machinery integration being the largest), an ADR if the dependency-posture
call in PLAN-0001 is overturned, and prototype branches for the loop-break and
ipxedust experiments feeding the e2e QEMU tier.

## References

- [Matchbox]: Sidero Labs, Talos v1.13 — Matchbox bare-metal guide
- [PXE]: Sidero Labs, Talos v1.13 — PXE bare-metal guide
- [PLAN-0001](../plan/0001-v100-dependency-posture-and-standalone-consumer.md)
  — dependency posture; machinery + ipxedust evaluation rows
- [IMPL-0001](../impl/0001-release-v010-of-the-booty-library-and-the-booty-binary.md)
  — the release this validation ran against
- [ADR-0002](../adr/0002-booty-is-a-library-with-cmdbooty-as-reference-consumer.md)
  — "a real consumer's need drives every addition"
- Chapter 04, [iPXE deep dive](../go-ipxe/04-ipxe-deep-dive.md) — chainload
  script deployment options
- `render/templates/talos/_common.yaml.tmpl` — the secrets-omitted note
- `proxydhcp/proxydhcp.go` — `handleDHCP` / `uefiArch` /
  `pxeBootServerType` (Observations 4–5)
- EDK2 `NetworkPkg/UefiPxeBcDxe/PxeBcBoot.c` — the type-0 abort
  (Observation 5)

[Matchbox]: https://docs.siderolabs.com/talos/v1.13/platform-specific-installations/bare-metal-platforms/matchbox
[PXE]: https://docs.siderolabs.com/talos/v1.13/platform-specific-installations/bare-metal-platforms/pxe
