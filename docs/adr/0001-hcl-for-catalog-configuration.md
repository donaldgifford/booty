---
id: ADR-0001
title: "HCL for catalog and configuration authoring"
status: Accepted
author: Donald Gifford
created: 2026-07-13
---

<!-- markdownlint-disable-file MD025 MD041 -->

# ADR-0001: HCL for catalog and configuration authoring

**Status:** Accepted **Author:** Donald **Date:** 2026-07-13

## Context

booty's serving core is stdlib-only by policy (PLAN-0001 P1). But the catalog —
the desired-state description of profiles and the group rules that bind machines
to them — is authored by humans and needs an on-disk format. The standard library
ships no structured-config parser beyond `encoding/json`, and JSON is a poor
authoring surface: no comments, no variables, no expressions, heavy quoting.

The catalog is exactly the place where authoring ergonomics matter. The matchbox
pain point PLAN-0001 calls out is rigidity: repeating the same boot settings
across many near-identical nodes. A format with variables, locals, functions and
interpolation collapses that duplication; a flat data format does not.

Every non-stdlib dependency must clear PLAN-0001's admission bar (P2), sit behind
an owned interface (P3), keep the binary static (P4), and get an ADR (P5). This
is that ADR.

## Decision

Adopt **`github.com/hashicorp/hcl/v2`** (and its evaluation engine
`github.com/zclconf/go-cty`) as the authoring format for the catalog and, later,
for booty's own server configuration.

The catalog is evaluated as a small typed configuration language, not merely
parsed: a Terraform-lite `EvalContext` exposes `variable` blocks, `locals`, a
curated subset of the cty standard-library functions, and one booty-specific
function (`mac_bare`). This is implemented in `catalog/source.go`.

Scope is bounded deliberately:

- **HCL is the input/authoring format only.** Output formats are dictated by
  their consumers and are produced by `text/template`, not HCL: Talos
  machineconfig (YAML), Proxmox `answer.toml` (TOML), cloud-init user-data (YAML),
  iPXE scripts (their own text). A useful consequence is that booty needs **no
  YAML or TOML emitter dependency** — HCL is the only config-format dependency.
- **Owned interface (P3).** The `catalog.Source` interface returns plain domain
  types (`Catalog`, `Profile`, `Group`, `Identity`). No `hcl` or `cty` type
  appears in any exported catalog API or crosses into the matcher/renderer;
  `catalog/catalog.go` imports neither package. `DirSource` is one
  implementation; `git://` and `platform://` sources will be others.

## Consequences

### Positive

- Variables/locals/functions/interpolation eliminate per-node duplication (the
  matchbox rigidity problem) directly in the authoring layer.
- Consistency with the rest of the fleet, which already standardizes on HCL (this
  repo already carried `.forge-lock.hcl`).
- Typed evaluation catches errors (undefined reference, type mismatch, cycles) at
  load time; `booty validate` surfaces them as a CI gate before any boot.
- Net-simpler dependency graph than the YAML-plus-TOML alternative, since outputs
  are text-templated.

### Negative / costs

- Heavier transitive graph than a TOML/YAML library: pulls `zclconf/go-cty`,
  `agext/levenshtein`, `apparentlymart/go-textseg`, `mitchellh/go-wordwrap`, and
  some `golang.org/x/*`. All pure Go.
- Two-phase evaluation (variables/locals → EvalContext → decode blocks) is more
  code than a straight unmarshal; contained in `source.go`.

**P4 compliance:** verified. `CGO_ENABLED=0 go build ./cmd/...` succeeds; the
transitive graph is pure Go, so the binary still links static and runs in
`scratch`.

**Version pinning:** `hcl/v2 v2.24.0`, `go-cty v1.16.3` (see `go.mod`). Bumped via
Renovate's Go-module manager like any other dependency.

## Alternatives considered

- **`encoding/json` (stdlib).** P1-purest, zero dependencies, but no comments, no
  variables, no expressions — an unacceptable authoring surface for the catalog.
- **TOML (`pelletier/go-toml/v2` or BurntSushi).** Pleasant for flat config, but
  no expression/variable layer, so the duplication problem remains. Likely still
  adopted later for *emitting* `answer.toml`, which is a separate decision.
- **YAML (`goccy/go-yaml`).** The obvious default, and explicitly rejected: anchors
  are a weak substitute for variables/functions, and the fleet standard is HCL,
  not YAML.
- **Plain `gohcl` struct-decode (no EvalContext).** Simpler, but forgoes the
  variables/locals/functions that are the entire reason to choose HCL. Rejected in
  favor of the typed-config-language route.

## References

- PLAN-0001 — dependency posture (P1–P5) and the standalone consumer
- `catalog/` — the owned interface and the HCL loader
- `docs/go-ipxe/05-catalog-and-matcher.md` — the walkthrough chapter
