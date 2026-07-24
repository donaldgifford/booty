---
id: PLAN-0001
title: "v1.0.0 Dependency Posture and Standalone Consumer"
status: Draft
author: Donald Gifford
created: 2026-07-12
---

<!-- markdownlint-disable-file MD025 MD041 -->

# PLAN XXXX: v1.0.0 Dependency Posture and Standalone Consumer

**Status:** Draft **Author:** Donald **Date:** 2026-07-12

<!--toc:start-->
- [Goal](#goal)
- [Context](#context)
- [Approach](#approach)
  - [Part 1: Dependency admission principles](#part-1-dependency-admission-principles)
  - [Part 2: Candidate evaluation](#part-2-candidate-evaluation)
  - [Part 3: Standalone as first-class consumer](#part-3-standalone-as-first-class-consumer)
- [Components](#components)
- [File Changes](#file-changes)
- [Verification](#verification)
- [Dependencies](#dependencies)
- [Open Questions](#open-questions)
- [References](#references)
<!--toc:end-->

## Goal

v1.0.0 ships with a deliberate, documented dependency posture and a formally
first-class standalone consumer. Concretely, at release:

- `go.mod` contains only dependencies accepted through this plan, each with its
  own ADR recording the decision and the owned interface that wraps it.
- The serving core (HTTP endpoints, matching, catalog, rendering pipeline)
  remains stdlib-only.
- The standalone CLI is the reference consumer of the library: `cmd/` is thin,
  the binary is single-static (no cgo, runs in `scratch`), and it has its own
  release, test, and DR-bundle story.

```console
CGO_ENABLED=0 go build ./cmd/...        # must succeed, no cgo anywhere
./bootsvc validate ./catalog            # CI gate on the config repo
./bootsvc render --machine r640a --kind talos-machineconfig
./bootsvc serve --preset bootstrap --source dir:///mnt/bundle/catalog
```

## Context

The service was built from scratch on stdlib. That was initially framed as a
learning trade-off, but the ecosystem survey confirms it was the correct
engineering call: there is no bulletproof netboot framework to build on.

- **matchbox** — not consumable as a library; Ignition/CLC is baked into its
  core render path, and generic configs are second-class. Its group/profile/
  label matching model is the part worth learning from, and we did.
- **pixiecore / `go.universe.tf/netboot`** — effectively unmaintained.
- **Tinkerbell** — a platform, not a package; individual pieces (smee, ipxedust)
  are extractable references but not a foundation.

The open question is therefore not "framework or scratch" but: which _targeted_
packages earn a place at the protocol and format edges, where correctness is
fiddly and someone else has already eaten the quirks. This plan defines the
admission criteria, lists the candidates, and records the evaluation each must
pass before an accepting ADR is written.

Separately, prior discussion settled that the standalone CLI is **not a demo** —
it is the standalone distribution of the product and the DR path runs through it
(bundle → netboot PVE via `answer.toml` → netboot Talos → platform up). That
principle gets codified here so it constrains v1 design rather than being
retrofitted.

Talos-specific pressure: the current cluster was bootstrapped once via the Talos
Terraform provider and has run a single version for ~a year, with machineconfig
changes applied directly via `talosctl`. This service will own machineconfig
generation for netbooted nodes going forward, which makes the `machinery`
adoption question the highest-stakes item in the candidate list.

## Approach

### Part 1: Dependency admission principles

These are the requirements every candidate is evaluated against. They should be
restated in each accepting ADR.

**P1 — stdlib-first serving core.** `net/http` with 1.22+ `ServeMux` routing,
`log/slog`, `go:embed`, `text/template`/`html/template`, `database/sql`-shaped
access to local state. The serving core takes no third-party dependencies.
Routers, middleware stacks, logging frameworks, DI containers, config frameworks
(viper et al.), and ORMs are rejected categorically — they are convenience
layers, and convenience layers are where Go services go to bloat.

**P2 — the admission test.** A dependency is admissible only if it encodes
protocol or format knowledge we would otherwise have to reverse-engineer:
wire-level quirks (DHCP option encoding, TFTP block-size negotiation, UEFI PXE
client behavior) or schema semantics (Talos machineconfig structure and
validation). "Saves typing" is not admissible. "Saves packet captures" is.

**P3 — owned-interface wrapping.** Every accepted dependency sits behind a small
interface we own. Third-party types never cross into the matcher, catalog, or
renderer domain. This is a replaceability requirement, not style: the netboot
ecosystem's maintenance record says some of these dependencies _will_ go stale,
and the wrap is what makes swapping them a localized change. (Same rule already
applied to gofish in the fleet-tooling context; it is a general rule here.)

**P4 — single static binary.** `CGO_ENABLED=0`, runs in `scratch`, UI embedded
via `go:embed`. Any candidate that drags in cgo is rejected on that basis alone.
This constraint exists because the DR story is "one artifact on a NAS/USB stick
runs anywhere."

**P5 — one ADR per accepted dependency.** Records: role, admission-test
justification, transitive-graph review, version-pinning policy, and the owned
interface it lives behind.

### Part 2: Candidate evaluation

| Candidate                              | Role                                                                        | Why not stdlib                                                                   | Primary risks                                                                                       | Status                        |
| -------------------------------------- | --------------------------------------------------------------------------- | -------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------- | ----------------------------- |
| `pin/tftp`                             | TFTP serving (iPXE binary handoff; Pi serial-prefix lane later)             | stdlib has no TFTP                                                               | Maintenance cadence; single-maintainer                                                              | Evaluate                      |
| `insomniacslk/dhcp`                    | DHCPv4 packet encode/decode for in-process ProxyDHCP                        | stdlib has no DHCP                                                               | Scope creep — pulls DHCP responder into v1                                                          | Evaluate / likely defer       |
| `tinkerbell/ipxedust`                  | Pinned, embedded iPXE binaries (`undionly.kpxe`, `ipxe.efi`, `snponly.efi`) | Building iPXE in our CI is the alternative, not stdlib                           | Consuming assets without their server wiring; provenance trust                                      | Evaluate                      |
| `siderolabs/talos/pkg/machinery`       | Machineconfig types, generation, secrets bundle, validation                 | Templating raw YAML forfeits schema correctness and `talosctl`-parity validation | Heavy transitive graph; version-skew policy vs. cluster versions                                    | Evaluate — highest priority   |
| `pelletier/go-toml/v2` (or BurntSushi) | `answer.toml` rendering                                                     | stdlib has no TOML                                                               | Minimal — pick one, ADR the choice                                                                  | Evaluate                      |
| `goccy/go-yaml`                        | YAML for anything not machinery-typed                                       | stdlib has no YAML; `gopkg.in/yaml.v3` is archived                               | Behavioral differences from yaml.v3                                                                 | Evaluate                      |
| `modernc.org/sqlite`                   | Runtime-state store (arming flags, boot funnel, callbacks)                  | Pure-Go, satisfies P4; `mattn/go-sqlite3` fails P4 (cgo)                         | Performance vs. cgo driver (irrelevant at this write volume); consider bbolt as simpler alternative | Evaluate                      |
| `go-git/go-git` vs. `exec git`         | Git source sync loop                                                        | stdlib has no git                                                                | go-git: heavy, partial protocol support. exec: needs git in image, breaks `scratch`                 | Evaluate — see Open Questions |

Per-candidate evaluation notes and accept criteria:

**`pin/tftp`.** The de facto Go TFTP library (used broadly, including in the
Tinkerbell stack). Accept criteria: correct block-size/option negotiation
against real UEFI PXE clients (test against OVMF and a physical Dell NIC),
context cancellation support, and handler types that wrap cleanly behind our
asset-serving interface. TFTP is the one protocol where the alternative to this
library is reading RFCs 1350/2347/2348 against packet captures.

**`insomniacslk/dhcp`.** The serious DHCP packet library (CoreDHCP, smee, u-root
all build on it). Admission is not in question; _scope_ is. v1 can run external
dnsmasq in ProxyDHCP mode per netboot VLAN, keeping DHCP out of the binary
entirely. Accepting this library means committing to an in-process ProxyDHCP
responder — strictly a v1.x/v2 decision unless a concrete v1 requirement forces
it. Recommendation: record as accepted-in- principle, deferred; do not add to
`go.mod` for v1.0.0.

**`tinkerbell/ipxedust`.** Solves iPXE binary provenance: versioned, prebuilt,
embedded via `go:embed`. Accept criteria: we can consume the embedded binaries
without importing their TFTP/HTTP server wiring (or we vendor only the build
approach), license compatibility, and a documented story for which iPXE commit
the binaries pin. Fallback if it fails evaluation: build iPXE in our own CI with
pinned commit + embedded script, which is more control for more maintenance.

**`siderolabs/talos/pkg/machinery`.** The biggest win and the biggest
commitment. Official, separately-versioned module carrying the real
machineconfig types, config/secrets-bundle generation, and validation. Adopting
it means `render` and `validate` get `talosctl`-equivalent correctness instead
of "the YAML template parsed."

Accept criteria / required INV outputs:

1. **Version-skew policy.** machinery version must be ≥ the newest Talos version
   we render for. Define the supported render matrix (e.g. "current cluster
   version through current stable") and the upgrade cadence for the machinery
   pin. The existing cluster's year-on-one-version history says the matrix can
   be narrow — but it must be _stated_, because the day we netboot a new node at
   a newer Talos than the pin, rendering silently produces configs missing newer
   fields.
2. **Transitive-graph review.** machinery pulls a nontrivial graph (protobuf et
   al.). Confirm it does not violate P4 and that the size cost is acceptable for
   what it buys (it almost certainly is).
3. **Boundary.** machinery types stay inside the Talos renderer package. The
   catalog stores our own declarative node spec; the renderer maps spec →
   machinery types → serialized config. machinery never leaks into matcher or
   catalog (P3).
4. **Ownership division.** Record (likely as its own ADR): the Terraform
   provider remains the bootstrap/ownership mechanism for the _existing_
   cluster; this service is the machineconfig source of truth for _netbooted_
   nodes and clusters. No dual-writer situation for any single node.

**TOML / YAML.** Small decisions, but ADR them so the choice is recorded once.
TOML accept criterion: round-trip fidelity sufficient for `answer.toml` (we only
serialize, so either library passes; pick `pelletier/go-toml/v2` unless
evaluation surprises). YAML: `goccy/go-yaml` over the archived `yaml.v3` for any
hand-templated YAML; note that machinery handles its own serialization,
shrinking this library's blast radius.

**`modernc.org/sqlite` vs. bbolt.** Runtime state is low-volume, single-writer,
local. sqlite buys queryability (boot-funnel timeline in the UI is a query) at
the cost of a large pure-Go translation layer; bbolt is tiny but pushes indexing
into app code. Accept criteria: WAL/locking behavior under the single-writer
pattern, crash-recovery behavior, and P4 compliance (both pass). Recommendation
to test first: `modernc.org/sqlite`, because the observability UI is
query-shaped.

**Git source.** Three options: `go-git` (pure Go, P4-clean, but heavy and
historically incomplete protocol support), `exec git` (correct by definition,
but requires git in the image — breaks `scratch` unless the git-sync loop is
deployment-optional), or **dir-only for v1** with git handled by an external
checkout/sidecar. The bootstrap preset already mandates a zero-network `dir://`
source, so git support is a steady-state convenience, not a v1 requirement.
Recommendation: dir + external checkout for v1.0.0; run the go-git INV before
v1.x.

### Part 3: Standalone as first-class consumer

**R1 — Not a demo.** The CLI is the standalone distribution of the product. It
is versioned, tested, and released as the artifact the DR story depends on.
"Demo" implies allowed-to-rot; this artifact is what stands the lab back up from
zero.

**R2 — Thin `cmd/`.** Flag parsing, config load, signal handling, then
`server.Run(ctx, cfg)`. If `main` grows logic, the library API is wrong — `cmd/`
thinness is the continuous test that the platform embed can do everything the
CLI can, because both call the same surface.

**R3 — Subcommands, not just `serve`.**

| Verb                  | Purpose                                                                                                                                                       |
| --------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `serve`               | Full system; embedded UI; preset flags select source/auth posture (bootstrap = dir/git sources + local token/mTLS; steady-state = platform source + OIDC)     |
| `validate`            | Lint a catalog dir; nonzero exit on any error; the CI gate on the config repo                                                                                 |
| `render`              | Render any machine's iPXE script / machineconfig / `answer.toml` / cloud-init offline; golden-file test harness and the "why did node X get that config" tool |
| `snapshot` / `import` | Materialize / load the catalog for bundle builds and platform handoff                                                                                         |

`validate` and `render` are this project's analogue of PegaProx's recordings and
mock server: they make the primitives testable without booting anything.

**R4 — Library documentation lives elsewhere.** The CLI accumulates operational
wiring (metrics, reload, presets) that obscures minimal usage. The
how-to-consume-the-library story is runnable godoc `Example`s plus a small
`examples/` consumer. Two audiences, two front doors, one binary underneath.

**R5 — Bootstrap bundle as a built artifact.** CI produces a versioned bundle:
static binary, catalog checkout, and pinned boot assets (kernels/ initramfs
cached, not fetched from factory.talos.dev at DR time). Stored off-cluster
(NAS/USB), runnable from a laptop or a Pi.

## Components

| Component             | Purpose                                                                                              |
| --------------------- | ---------------------------------------------------------------------------------------------------- |
| Serving core (stdlib) | HTTP endpoints: iPXE scripts, assets, config render, answer POST handler                             |
| Matcher / catalog     | Normalized identity attributes → group → profile; source-fed desired state                           |
| Renderers             | Per-format: Talos (machinery), Proxmox `answer.toml` (TOML), cloud-init (YAML), iPXE (text/template) |
| Runtime state store   | sqlite/bbolt: arming flags, boot-funnel events, callbacks                                            |
| Source loops          | `dir://`, later `git://`, later `platform://` — populate the catalog                                 |
| TFTP adjunct          | `pin/tftp`-backed iPXE binary handoff                                                                |
| CLI (`cmd/`)          | Thin reference consumer; verbs above                                                                 |
| Bundle pipeline       | CI job assembling the DR artifact                                                                    |

## File Changes

| File                                                    | Action        | Description                                      |
| ------------------------------------------------------- | ------------- | ------------------------------------------------ |
| `docs/adr/ADR-XXXX-*.md`                                | Create        | One per accepted dependency (P5)                 |
| `docs/investigation/INV-XXXX-machinery-version-skew.md` | Create        | machinery support-matrix INV                     |
| `docs/investigation/INV-XXXX-go-git-vs-exec.md`         | Create        | Git source INV (pre-v1.x)                        |
| `go.mod`                                                | Modify        | Only post-ADR additions                          |
| `cmd/`                                                  | Modify        | Verb structure per R3                            |
| `internal/render/talos/`                                | Create/Modify | machinery-backed renderer behind owned interface |

## Verification

```bash
# P4: static binary from scratch
CGO_ENABLED=0 go build ./cmd/... && docker build -f Dockerfile.scratch .

# go.mod audit: every non-stdlib module maps to an accepted ADR
go mod graph | ./hack/audit-deps.sh docs/adr/

# Renderer correctness
./bootsvc validate ./testdata/catalog-good     # exit 0
./bootsvc validate ./testdata/catalog-bad      # exit != 0, actionable errors
go test ./internal/render/... -run Golden      # golden files per machine/kind

# machinery validation parity: rendered config passes talosctl validate
./bootsvc render --machine tt01 --kind talos-machineconfig | talosctl validate --mode metal -f /dev/stdin
```

## Dependencies

- INV: machinery version-skew / support matrix (blocks the machinery ADR)
- INV: go-git vs. exec git vs. dir-only (blocks git-source work, not v1)
- Decision: ProxyDHCP in-process vs. external dnsmasq for v1 (leaning external)
- Catalog/`Source` interface design (next design conversation; this plan
  constrains it but does not define it)

## Open Questions

- Service and module naming — name, org path, license posture relative to the
  PegaProx SDK/service split.
- machinery pin cadence: track Talos stable, or pin to cluster-max and bump
  deliberately?
- ProxyDHCP scope: is there any v1 requirement external dnsmasq can't meet?
- Runtime store: sqlite vs. bbolt (default: sqlite, pending eval).
- Git source for v1: recommendation is dir-only + external checkout — confirm or
  override.
- Pi lane (serial-prefixed TFTP layout, EDK2-UEFI convergence trick): out of
  v1.0.0 scope, but does the asset-layout design need to reserve room now?

## References

- poseidon/matchbox — matching model reference; Ignition-centric core
- Tinkerbell smee / ipxedust — DHCP + embedded-iPXE references
- `go.universe.tf/netboot` (pixiecore) — prior art, unmaintained
- `siderolabs/talos/pkg/machinery` — official machineconfig module
- Proxmox automated installation (`answer.toml` HTTP POST w/ system-info JSON)
- Talos kernel param `talos.config=` with `${mac}`/`${uuid}`/`${serial}`
  substitution
- Prior conversations: modes/presets design, standalone-as-reference-consumer,
  dependency survey
