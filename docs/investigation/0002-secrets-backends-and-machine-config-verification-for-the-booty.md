---
id: INV-0002
title: "Secrets backends and machine-config verification for the booty CLI"
status: Open
author: Donald Gifford
created: 2026-08-16
---
<!-- markdownlint-disable-file MD025 MD041 -->

# INV 0002: Secrets backends and machine-config verification for the booty CLI

**Status:** Open
**Author:** Donald Gifford
**Date:** 2026-08-16

<!--toc:start-->
- [Question](#question)
- [Hypothesis](#hypothesis)
- [Context](#context)
  - [Decisions already made (2026-08-16)](#decisions-already-made-2026-08-16)
- [Approach](#approach)
- [Environment](#environment)
- [Findings](#findings)
  - [Observation 1: build tags do not isolate dependencies](#observation-1-build-tags-do-not-isolate-dependencies)
- [Conclusion](#conclusion)
- [Recommendation](#recommendation)
- [References](#references)
<!--toc:end-->

## Question

Three questions, in dependency order:

1. **Interface shape.** What does the library expose for secret resolution — a
   `SecretResolver` interface plus a `secret()` HCL function in the catalog —
   and can it be added without breaking the `DirSource`/`Load` API?
2. **Module posture.** Where do backend implementations (and eventually the
   whole CLI) live so the library's `go.mod` stays clean: a nested module for
   `cmd/booty`, a nested module for the resolvers only, or accepted bloat in
   the main module? Build tags are *not* a candidate — see
   [Observation 1](#observation-1-build-tags-do-not-isolate-dependencies).
3. **Backend priority.** For the docker-service deployment that motivates
   this, which backend comes first — 1Password (service accounts) or
   Vault/OpenBao — and what does its auth bootstrap look like inside a
   container?

## Hypothesis

1. The interface is small (`Resolve(ctx, ref) (string, error)`) and slots in
   as an HCL function made available during catalog evaluation; the env and
   file resolvers need zero new dependencies and become the reference
   implementations.
2. A nested module for `cmd/booty` is the clean end state — the CLI gets to be
   opinionated and heavy while the library stays two-dependency lean — but the
   plumbing cost (goreleaser `dir` builds, Docker build context, e2e imports,
   `go.work` for local dev) needs measuring before committing. A
   resolvers-only nested module is the smaller intermediate step.
3. Vault/OpenBao first: one client covers both (OpenBao is API-compatible),
   and token/AppRole auth maps cleanly onto container env injection.
   1Password service accounts are a close second and may win on operator
   ergonomics (`op://` refs).

## Context

Grew out of INV-0001's secrets question (gap 1): once machineconfigs carry
real secrets, both *where secret inputs come from* and *who may fetch the
rendered result* need answers. Discussion on 2026-08-16 settled the
architecture direction and rejected the heavier options; this INV evaluates
what remains open.

**Triggered by:** [INV-0001](0001-talos-boot-chain-gaps-machineconfig-secrets-ipxe-chainload-loop.md)

### Decisions already made (2026-08-16)

Recorded here so the investigation doesn't relitigate them:

- **Extension mechanism is a Go interface, full stop.** No
  `hashicorp/go-plugin`-style out-of-process plugins. booty is a library
  (ADR-0002); a consumer who wants Doppler or SOPS implements one interface in
  their own `main()`. We owe seams, not a plugin runtime.
- **Default posture is parity (tier 0).** Plaintext HTTP, identity-matched,
  fully logged — feature coverage equal to Matchbox/dnsmasq/netboot.xyz. A
  boot server whose default can fail to boot a node has its priorities
  backwards. Everything above parity is opt-in flags in the CLI.
- **Tier 1 ships first and naively: a static bearer token from an env var**
  (or config), operator-managed, mirroring `--proxmox-token` — same
  constant-time comparison, extended to `/machine-config` as a query
  parameter on the `talos.config` URL (Talos fetches the URL as given). PVE
  already has this via the token baked into the prepared ISO.
- **Tier 2 (per-boot one-time tokens minted into the rendered `/ipxe`
  script) is deferred, not designed.** Attractive — single-use + TTL +
  MAC-bound, replay becomes an audit signal, no iPXE/Talos changes — but
  later.
- **Tier 3 is rejected.** Verified transport (site CA / client cert embedded
  in the iPXE binary) requires booty to own iPXE builds, and booty will not
  be a build service. INV-0001's binary-provenance question is narrowed
  accordingly: pinning/verifying an upstream binary is in scope, building one
  is not.
- **Tier 4 is rejected.** Talos-native OAuth2 device flow and TPM/measured
  boot: noted as existing, not pursued.
- **Honesty constraint for any docs or flag help that ship from this:**
  tiers 1–2 ride the same plaintext channel they protect. They narrow the
  window and add auditability; they are not confidentiality against an
  on-path attacker. Nothing may describe a query-param token as making the
  channel secure.

## Approach

1. **Interface + env resolver spike.** Draft `SecretResolver` in the library;
   wire a `secret("env://NAME")` HCL function into catalog evaluation
   (`DirSource` currently evaluates variables → locals → blocks; find the
   seam). Prove a catalog can reference `secret()` in `vars` without breaking
   `booty validate` for catalogs that don't use it. Zero new dependencies.
2. **Tier-1 token on `/machine-config`.** Implement
   `--machine-config-token` / `BOOTY_MACHINE_CONFIG_TOKEN` mirroring
   `--proxmox-token` (flag overrides env; env is what the docker service
   uses). Constant-time compare, 401 on mismatch, logged. This lands
   regardless of the rest of the investigation.
3. **Module-posture measurement.** On a branch, split `cmd/booty` into a
   nested module. Record everything that breaks and its fix: goreleaser
   (`builds[].dir`), Dockerfile build context, `test/e2e` imports, CI
   workflows, `go.work` for local dev, release tagging for two modules.
   Separately measure the do-nothing cost: `go mod graph | wc -l`, `go.sum`
   line delta, and govulncheck noise from adding `openbao/api` (or
   `hashicorp/vault/api`) + the 1Password SDK to the main module.
4. **Backend auth-in-container sketch.** For Vault/OpenBao and 1Password:
   what credential does the container start with, where does it live in a
   compose file, and does the resolver re-fetch or cache for the process
   lifetime? (Catalog loads once at boot — `DirSource.Load` — so resolution
   frequency is currently "once per serve", which simplifies caching but
   means rotation requires restart. Note this as a property, decide if it's
   acceptable.)
5. Conclude per question; the module-posture answer feeds PLAN-0001, the
   interface shape becomes a DESIGN doc if adopted.

## Environment

| Component | Version / Value |
|-----------|----------------|
| booty | v0.2.0 (`main` @ post-#19) |
| Go | 1.26.5 |
| Candidate SDKs | `openbao/api` (Vault-compatible), 1Password Go SDK |
| Deployment target | booty CLI as a docker service (compose/systemd) |

## Findings

### Observation 1: build tags do not isolate dependencies

Recorded up front because it eliminates a candidate that keeps coming up:
build tags control what compiles into a binary, not what appears in `go.mod`.
The `require` lines for the SDKs remain regardless of tags, so library
importers still inherit them into their module graphs, security scanners
still flag them, and MVS conflicts remain possible. Tags would only shrink
the *binary* — which was never the concern. The isolation options are module
boundaries or nothing.

## Conclusion

**Answer:** <!-- pending — one conclusion per question after the Approach runs -->

## Recommendation

Pending. Independent of the conclusion, Approach step 2 (the tier-1 token) is
already decided and can ship on its own — it needs no interface, no new
dependencies, and no module split.

## References

- [INV-0001](0001-talos-boot-chain-gaps-machineconfig-secrets-ipxe-chainload-loop.md)
  — parent: the machineconfig-secrets gap this grew from
- [PLAN-0001](../plan/0001-v100-dependency-posture-and-standalone-consumer.md)
  — dependency posture; the module-split question lands there
- [ADR-0002](../adr/0002-booty-is-a-library-with-cmdbooty-as-reference-consumer.md)
  — library-first; the CLI is the reference consumer
- `httpsrv/httpsrv.go` — `--proxmox-token` bearer-auth pattern
  (constant-time compare) that tier 1 mirrors
- [OpenBao](https://openbao.org/) — Vault-API-compatible fork
- [1Password Go SDK](https://github.com/1Password/onepassword-sdk-go) —
  service-account auth, `op://` references
