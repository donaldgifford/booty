---
id: ADR-0002
title: "booty is a library with cmd/booty as reference consumer"
status: Accepted
author: Donald Gifford
created: 2026-07-19
---

<!-- markdownlint-disable-file MD025 MD041 -->

# ADR-0002: booty is a library with cmd/booty as reference consumer

**Status:** Accepted **Author:** Donald **Date:** 2026-07-19

## Context

booty was built (via the `docs/go-ipxe/` walkthrough) as a standalone binary:
five packages under `internal/`, wired together by a thin `cmd/booty`. That shape
was right for the walkthrough, and CLAUDE.md codified it: *"`internal/` is a hard
wall — use it liberally; promote only when something outside this repo actually
needs it."*

Something outside this repo now needs it. booty is becoming part of a larger
homelab platform that must both **initialize** the platform (PXE-boot Proxmox
hosts and Talos nodes from bare metal) and later **run inside** it (serving boot
and config from platform state, alongside `proxmox-go-sdk` and other components).
A binary-only booty would force that platform to shell out to a CLI or fork the
code; the opinions baked into the binary (HCL files on disk, fixed flag surface)
are right for the standalone consumer and wrong as a hard boundary.

Two facts make the promotion cheap rather than speculative:

- The packages are already library-shaped, because PLAN-0001 P3 (owned
  interfaces) forced it: `catalog.Catalog` is plain data with a pure
  `Match(Identity)`; `Source` is an interface with `DirSource` as one impl (the
  `platform://` source was deferred, not rejected); `httpsrv.New(Options).Handler()`
  returns a plain `http.Handler`; `tftp`/`proxydhcp` expose `Serve(conn)` seams.
  The only barrier to external use is the `internal/` path element.
- The second consumer is named and real (the platform), with concrete needs:
  programmatic catalogs from live state, dynamic per-request answer/config
  rendering, and embedding the boot endpoints in its own HTTP server.

## Decision

**booty is a Go library; `cmd/booty` is its reference consumer** — the binary the
walkthrough builds remains fully supported, but it consumes the same public API
any other program does.

Mechanically:

- Promote `internal/{catalog,render,httpsrv,tftp,proxydhcp}` to **top-level
  packages** in the same module: `github.com/donaldgifford/booty/catalog`, etc.
  No separate module, no `pkg/` nesting. `internal/` remains available for any
  genuinely private helpers that appear later.
- Close the one real API gap: `render.New` accepts an optional caller-supplied
  `fs.FS` of templates layered over the embedded set (same-name overrides). This
  serves both known consumers — the platform's dynamic templates and the
  standalone binary's `--templates-dir` operator override.
- **No speculative API surface.** Extension points are added only when a real
  consumer needs them; the platform's future requirements drive future changes.
  The module is v0: the API may move.

CLAUDE.md's layout and `internal/`-wall guidance are amended to match.

## Consequences

### Positive

- The platform can construct catalogs programmatically (as the tests always
  have), implement `Source` over its own state store, mount `Handler()` in its
  own mux, and call `Renderer.Config` per-request with computed vars — no fork,
  no CLI shelling.
- The walkthrough gains, rather than loses, coherence: every "testable seam" it
  taught is now visibly an API boundary, and the guide reads as "build the
  reference consumer of this library."
- One module serves both consumers; release/goreleaser flow for the binary is
  unchanged.

### Negative / costs

- Public API means compatibility pressure. Mitigated by v0 semver (breaking
  changes allowed, flagged in release notes) until the platform consumer
  stabilizes the surface.
- Import-path churn: one mechanical rewrite of `booty/internal/X` → `booty/X`
  across code, tests, and guide prose.

### Neutral

- The binary's behavior, flags, and CI are unchanged; `examples/catalog` and the
  e2e tiers keep working as-is.

## Alternatives Considered

- **Keep `internal/`, expose a facade package.** A single public `booty` package
  re-exporting curated types. Rejected: it duplicates every type or forces
  aliasing, and the per-package seams are already the right API.
- **Separate library module (`booty-lib`) + binary module.** Clean semver story,
  but two go.mods, two release flows, and cross-module churn during the exact
  period the API is expected to move. Reconsider if/when external consumers
  beyond the platform appear.
- **Stay binary-only; platform shells out to the CLI.** Works for `validate`,
  collapses for serving (the platform would run a second process and proxy to
  it) and for dynamic catalogs (would require file round-trips). Rejected.

## References

- ADR-0001 — the owned-interface (P3) discipline that made this promotion cheap
- PLAN-0001 — dependency posture and the deferred `platform://` source
- `docs/go-ipxe/00-index.md` — the walkthrough, reframed as building the
  reference consumer
