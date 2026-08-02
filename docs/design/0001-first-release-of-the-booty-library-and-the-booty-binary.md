---
id: DESIGN-0001
title: "First release of the booty library and the booty binary"
status: Approved
author: Donald Gifford
created: 2026-07-25
---

<!-- markdownlint-disable-file MD025 MD041 -->

# DESIGN 0001: First release of the booty library and the booty binary

**Status:** Approved **Author:** Donald Gifford **Date:** 2026-07-25

<!--toc:start-->
- [Overview](#overview)
- [Goals and Non-Goals](#goals-and-non-goals)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Background](#background)
- [Detailed Design](#detailed-design)
- [API / Interface Changes](#api--interface-changes)
- [Data Model](#data-model)
- [Testing Strategy](#testing-strategy)
- [Implementation Phases](#implementation-phases)
  - [Phase 1 — Release plumbing prerequisites](#phase-1--release-plumbing-prerequisites)
    - [Tasks (Phase 1)](#tasks-phase-1)
    - [Success Criteria (Phase 1)](#success-criteria-phase-1)
  - [Phase 2 — Library doc generation](#phase-2--library-doc-generation)
    - [Tasks (Phase 2)](#tasks-phase-2)
    - [Success Criteria (Phase 2)](#success-criteria-phase-2)
  - [Phase 3 — CI verified green on GitHub runners](#phase-3--ci-verified-green-on-github-runners)
    - [Tasks (Phase 3)](#tasks-phase-3)
    - [Success Criteria (Phase 3)](#success-criteria-phase-3)
  - [Phase 4 — Cut v0.1.0](#phase-4--cut-v010)
    - [Tasks (Phase 4)](#tasks-phase-4)
    - [Success Criteria (Phase 4)](#success-criteria-phase-4)
  - [Phase 5 — Validate the binary as the library's first consumer](#phase-5--validate-the-binary-as-the-librarys-first-consumer)
    - [Tasks (Phase 5)](#tasks-phase-5)
    - [Success Criteria (Phase 5)](#success-criteria-phase-5)
- [Migration / Rollout Plan](#migration--rollout-plan)
- [Open Questions](#open-questions)
  - [OQ-1: First version number and how the first bump lands](#oq-1-first-version-number-and-how-the-first-bump-lands)
  - [OQ-2: Codecov](#oq-2-codecov)
  - [OQ-3: Release signing key](#oq-3-release-signing-key)
  - [OQ-4: QEMU e2e tier in CI](#oq-4-qemu-e2e-tier-in-ci)
  - [OQ-5: Package doc convention](#oq-5-package-doc-convention)
  - [OQ-6: Distribution beyond archives + GHCR](#oq-6-distribution-beyond-archives--ghcr)
- [References](#references)
<!--toc:end-->

## Overview

Cut `v0.1.0`: the first public release of the booty **library** (the top-level
packages, per
[ADR-0002](../adr/0002-booty-is-a-library-with-cmdbooty-as-reference-consumer.md))
and of its first consumer, the **booty binary**. The release covers four
surfaces: the release plumbing itself (labels, secrets, branch protection),
library doc generation for pkg.go.dev, CI testing verified green on real GitHub
runners, and the released artifacts (archives, SBOMs, signed checksums, GHCR
image) validated end-to-end as a consumer would use them.

## Goals and Non-Goals

### Goals

- A labeled PR merged to `main` produces `v0.1.0` with zero manual steps: tag,
  GitHub release (8 archives + SBOMs + GPG-signed checksums), and a multi-arch
  GHCR image.
- The library is consumable: `go get github.com/donaldgifford/booty@v0.1.0`
  works, pkg.go.dev renders clean package docs for all five packages, and an
  external module importing `catalog` + `render` + `httpsrv` compiles.
- Every CI job (lint, test + coverage gate + e2e protocol tier, security,
  goreleaser snapshot + SBOM scan, docker bake) is verified green on GitHub
  runners and promoted to required status checks.
- The binary is validated **from released artifacts** — not from a local build —
  on at least macOS/arm64 and Linux, plus the container image.

### Non-Goals

- A `v1.0.0` API commitment. This is v0 semver; the API may still move
  (ADR-0002).
- Distribution beyond archives + GHCR (Homebrew tap, nix, apt). See OQ-6.
- The public docs site — that is
  [DESIGN-0002](./0002-starlight-docs-site-on-cloudflare-pages.md).
- The QEMU e2e tier in CI (see OQ-4) and `siderolabs/machinery`-based Talos
  config generation (deferred per PLAN-0001; hand-templated YAML ships as-is,
  clearly marked).

## Background

The repo is public on GitHub with the full pipeline already authored: `ci.yml`
(lint, test-coverage + coverage-gate + e2e protocol tier, Codecov upload,
govulncheck + Trivy, goreleaser snapshot with SBOM scan, docker bake of the CI
image), `release.yml` (label-driven: `pr-semver-bump` → tag → goreleaser with
GPG-signed checksums → multi-arch GHCR push via docker/metadata-action + bake),
changelog regeneration via git-cliff, CodeQL, and Trufflehog.
`just release-local` and a local `docker buildx bake` have both been verified.

What has **never happened yet**: a green CI run on GitHub's runners, a real
release, or an external consumer of the library. Verified gaps as of 2026-07-25:

- The `major` / `minor` / `patch` / `dont-release` labels **do not exist** in
  the repo (`gh label list` shows only GitHub defaults). The `PR Label Check`
  workflow requires exactly one of them on **every PR**, so until they are
  created every PR fails that check — this is the hard blocker.
- `gh secret list` is empty: `GPG_PRIVATE_KEY` / `GPG_FINGERPRINT` (release
  signing) and `CODECOV_TOKEN` (coverage upload) are unset.
- Two package doc comments still carry walkthrough-relative phrasing written
  mid-guide — `render` says "At this stage it produces iPXE scripts; Chapter 6
  adds the Talos machineconfig and cloud-init renderers" and `httpsrv` says
  "Chapter 6 adds…" — which is stale (those renderers exist) and reads wrong on
  pkg.go.dev, where "Chapter 6" means nothing without the guide.
- No branch protection / required checks on `main`.
- `pr-semver-bump` derives the next version from the latest existing tag, and
  the repo has no tags — first-release behavior needs verification (OQ-1).

## Detailed Design

The release flow is already designed and wired; this document sequences the work
to make it real and defines what "released" means. The four surfaces:

1. **Release plumbing** — labels, secrets, branch protection, Codecov. All
   one-time repo configuration, mostly via `gh` CLI.
2. **Doc generation** — the library's public documentation is godoc. Package
   comments are the product here: they must describe the final state of each
   package, stand alone without the guide, and render correctly on pkg.go.dev.
   The guide quotes some package comments, so edits follow the guide==code rule
   (chapter updated in the same PR). Indexing on pkg.go.dev is triggered after
   tagging by requesting the module from the Go proxy.
3. **CI testing** — nothing new is authored; the existing jobs are verified
   against real runners, runner-specific failures fixed, and the proven jobs
   promoted to required status checks.
4. **Release + consumer validation** — the label flow cuts `v0.1.0`; validation
   then deliberately takes the _consumer's_ path: download the archive,
   `go install` the binary, `go get` the library from a fresh module, pull the
   GHCR image, and run the README quickstart verbatim from those artifacts.

## API / Interface Changes

None intended. The v0.1.0 API surface is what exists today. Package doc comments
change (documentation only); any incidental API change discovered during
validation is a bug to fix before tagging, not after.

## Data Model

Not applicable.

## Testing Strategy

- **Per-phase**: each phase's success criteria below are the test.
- **CI**: the existing suite (race tests, coverage gate ≥60% per library
  package, e2e protocol tier, golangci-lint, govulncheck, Trivy, SBOM scan) is
  the regression net; Phase 3 proves it on real runners.
- **Release validation**: consumer-path smoke tests in Phase 5 — archive install
  on two platforms, `go install @v0.1.0`, external-module compile, container
  serve smoke (`/healthz`, `/machine-config`, `POST /proxmox/answer` against
  `examples/catalog`).

## Implementation Phases

Each phase builds on the previous one. A phase is complete when all its tasks
are checked off and its success criteria are met.

### Phase 1 — Release plumbing prerequisites

One-time repo configuration so PRs can merge and releases can sign and publish.
The semver labels come first: the `PR Label Check` workflow fails every PR until
they exist.

#### Tasks (Phase 1)

- [ ] Create the four semver labels:
      `gh label create major|minor|patch|dont-release` (with descriptions and
      colors; e.g. major=red, minor=yellow, patch=green, dont-release=gray).
- [ ] **(Donald)** Generate the dedicated release-signing GPG key (OQ-3) and set
      `GPG_PRIVATE_KEY` + `GPG_FINGERPRINT` repo secrets; commit the public key
      as `docs/booty-release.pub.asc`.
- [ ] **(Donald)** Set up Codecov for `donaldgifford/booty` and add the
      `CODECOV_TOKEN` secret (OQ-2).
- [ ] Confirm the Renovate app is enabled for this repo (`renovate.json5` is
      committed; the onboarding PR should appear) and that Dependabot alerts are
      on (dependabot-severity-label workflow depends on them).
- [ ] Verify `.github/labeler.yml` path-label mappings reference labels that
      exist; create any missing ones.
- [ ] Open a trivial test PR (docs nit) labeled `dont-release`, and confirm the
      label check passes and all CI jobs trigger.

#### Success Criteria (Phase 1)

- `gh label list` shows all four semver labels; the test PR's `PR Label Check`
  job passes.
- `gh secret list` shows `GPG_PRIVATE_KEY`, `GPG_FINGERPRINT`, and
  `CODECOV_TOKEN`.
- The test PR runs every CI job (whatever their outcome — Phase 3 makes them
  green).

### Phase 2 — Library doc generation

The library's public docs are godoc. Make every package comment describe the
package's final state, standing alone without the walkthrough, and stage the
README for consumers.

#### Tasks (Phase 2)

- [ ] Sweep all five package comments (plus `cmd/booty`) for
      walkthrough-relative phrasing:
      `grep -rn "Chapter\|At this stage"     catalog/ render/ httpsrv/ tftp/ proxydhcp/ cmd/`
      — rewrite `render` and `httpsrv` (confirmed stale); keep guide pointers
      only as stable "see docs/go-ipxe" references.
- [ ] Apply the OQ-5 decision (both a and b): rewrite each package comment in
      final-state terms **and** house it in a dedicated `doc.go` per package
      (lead files keep only their own code; room for `# Usage` sections and
      examples in `doc.go`).
- [ ] Guide==code pass: update any chapter that quotes a changed package comment
      in the same PR.
- [ ] Verify every exported symbol has a doc comment (`golangci-lint` revive
      `exported` rule already enforces this — confirm zero findings).
- [ ] Render locally with
      `go doc -all ./catalog ./render ./httpsrv ./tftp     ./proxydhcp` and/or
      `godoc -http` and proofread each package page.
- [ ] Add badges to README: Go Reference (pkg.go.dev), CI, Codecov, Go Report
      Card (optional).
- [ ] Confirm LICENSE detection prerequisites for pkg.go.dev (Apache-2.0 file at
      repo root — already present; no action expected).

#### Success Criteria (Phase 2)

- No package comment references chapters or in-progress state; each one reads
  correctly as a standalone pkg.go.dev landing page.
- `just check` green after the sweep (lint enforces exported-symbol docs).
- README carries working badge markup (badges may 404 until first tag/coverage
  upload — that resolves in Phases 4–5).

### Phase 3 — CI verified green on GitHub runners

The suite has only ever run locally. Prove every job on real runners, fix
runner-specific issues, then make the proven jobs required.

#### Tasks (Phase 3)

- [ ] Push a PR exercising the full matrix (code + docs touch) and get every job
      green: golangci-lint, test-coverage + coverage-gate, e2e protocol tier,
      govulncheck, Trivy, CodeQL, goreleaser snapshot + SBOM locate/scan + SARIF
      upload, docker bake `booty-ci` push of `:dev-ci` + image scan.
- [ ] Verify the SBOM-locate glob resolves on the runner (snapshot version
      string differs from local).
- [ ] Verify the e2e protocol tier's UDP loopback sockets behave on
      ubuntu-latest (expected fine; fix if not).
- [ ] Verify `ghcr.io/donaldgifford/booty:dev-ci` exists after the PR run and
      the Anchore image scan SARIF appears in the Security tab.
- [ ] Verify post-merge: the `ci` bake validation job and the changelog workflow
      run clean on `main`.
- [ ] Promote the proven jobs to required status checks on `main` branch
      protection (include `PR Label Check`; exclude jobs that only run
      post-merge).

#### Success Criteria (Phase 3)

- One PR cycle with **every** check green on GitHub runners, and a clean
  post-merge run on `main`.
- Branch protection on `main` requires the proven checks; a PR cannot merge red.
- Security tab shows CodeQL + both Anchore SARIF categories.

### Phase 4 — Cut v0.1.0

The label flow produces the release. First-release version mechanics per OQ-1.

#### Tasks (Phase 4)

- [ ] Seed the `v0.0.0` baseline tag (OQ-1) so `pr-semver-bump` produces exactly
      `v0.1.0` from a `minor`-labeled release PR.
- [ ] Final pre-flight: `just ci` + `just release-local` clean at the release
      commit.
- [ ] Open the release PR (can be the Phase 2/3 finishing PR or a trivial one),
      label it `minor`, merge it.
- [ ] Watch `release.yml`: bump-version tags `v0.1.0`; goreleaser publishes the
      GitHub release; docker job pushes GHCR.
- [ ] Verify the release page: 8 archives (linux+darwin × amd64+arm64, plus SBOM
      per archive), `checksums.txt` + `.sig`, release notes grouped by
      conventional-commit type.
- [ ] Verify the GPG signature: `gpg --verify checksums.txt.sig checksums.txt`
      with the public key.
- [ ] Verify GHCR: tags `0.1.0`, `0.1`, `v0`, `latest`; OCI annotations (source,
      description, license) render on the GHCR package page; SBOM + provenance
      attestations attached.
- [ ] Trigger pkg.go.dev indexing:
      `GOPROXY=proxy.golang.org go list -m github.com/donaldgifford/booty@v0.1.0`,
      then confirm <https://pkg.go.dev/github.com/donaldgifford/booty> renders.
- [ ] Confirm CHANGELOG.md regenerated on `main` includes v0.1.0.

#### Success Criteria (Phase 4)

- `v0.1.0` exists as tag + GitHub release with all expected assets and a valid
  GPG signature.
- `docker pull ghcr.io/donaldgifford/booty:0.1.0` succeeds and
  `docker run … version` prints `booty v0.1.0` with commit and date.
- pkg.go.dev shows `booty@v0.1.0` with the Phase 2 package docs and the
  Apache-2.0 license badge.

### Phase 5 — Validate the binary as the library's first consumer

Everything in this phase uses **released artifacts only** — the point is to walk
the consumer's path, not the maintainer's.

#### Tasks (Phase 5)

- [ ] macOS/arm64: download the darwin_arm64 archive from the release page,
      verify its checksum, extract, run the README quickstart against
      `examples/catalog` (`validate`, `serve`, curl `/healthz`,
      `/machine-config`, `POST /proxmox/answer`).
- [ ] Linux: repeat inside a container or VM with the linux archive (linux_amd64
      or arm64).
- [ ] `go install github.com/donaldgifford/booty/cmd/booty@v0.1.0` from a clean
      environment; `booty version` prints v0.1.0.
- [ ] Container: `docker run ghcr.io/donaldgifford/booty:0.1.0 serve` with the
      example catalog mounted; same curl smoke.
- [ ] Library consumer smoke: a fresh scratch module (outside this repo) that
      imports `catalog`, `render`, and `httpsrv`, loads `examples/catalog`, and
      serves `/ipxe` — must compile and run against `@v0.1.0` using only the
      public API.
- [ ] File issues for any friction found (doc gaps, flag surprises, missing
      errors); fix-or-defer each explicitly.
- [ ] Add a "Documentation"/install pointer to the release in README if anything
      proved unclear during validation.
- [ ] Flip this design's status to Implemented; record the release date.

#### Success Criteria (Phase 5)

- The README quickstart works verbatim from released artifacts on macOS and
  Linux, and from the GHCR image.
- `go install …@v0.1.0` and the scratch-module library import both work with no
  reference to the repo checkout.
- Zero unresolved release-blocking issues; anything deferred is filed with a
  rationale.

## Migration / Rollout Plan

Covered by the phases above. Rollback: a bad release is followed by a fixed
`patch` release — tags are never deleted or force-moved once published (module
proxies and GHCR caches make un-publishing unreliable). If `release.yml` fails
mid-flow (tag created, no release), re-run the workflow from the tag rather than
re-merging.

## Open Questions

All resolved 2026-07-29 — each question below records its **Decision** line.
Format: **a** was the recommendation; later letters were alternatives.

### OQ-1: First version number and how the first bump lands

`pr-semver-bump` computes the next version from the latest existing tag, and the
repo has no tags yet.

- **a. Seed a `v0.0.0` baseline tag manually, then label the release PR `minor`
  → `v0.1.0` (recommended).** Deterministic, matches ADR-0002's "v0 semver"
  framing, and exercises the exact label flow every future release will use. The
  seed tag is plumbing, not a release (no workflow fires on tags).
- b. Rely on the action's no-tag default (its docs say it assumes `v0.0.0` when
  none exists) — same result if it works, but unverified behavior on the most
  visible release of the project.
- c. Start at `v0.0.1` (label `patch`) and treat 0.0.x as pre-release scratch
  space, releasing `v0.1.0` only after DESIGN-0002's docs site ships.

**Decision (2026-07-29): a** — seed `v0.0.0`, label the release PR `minor`.

### OQ-2: Codecov

`ci.yml` uploads `coverage.out` with `continue-on-error: true`, so an unset
token silently no-ops today.

- **a. Set it up (recommended).** Free for public repos; one login + one
  `CODECOV_TOKEN` secret; the coverage badge and PR coverage comments are worth
  it for a library, and the local `coverage-gate` stays the hard gate
  regardless.
- b. Drop the upload step and rely solely on `just coverage-gate` — one less
  external service, no badge/trending.

**Decision (2026-07-29): a** — Codecov stays; Donald sets up the account, token,
and CI wiring himself.

### OQ-3: Release signing key

- **a. Generate a dedicated release-signing GPG key for booty, publish the
  public key in the repo (e.g. `docs/booty-release.pub.asc` + keyservers),
  private key only in GitHub secrets (recommended).** Scoped blast radius
  (revoke without touching your personal identity), and the workflow is already
  wired for GPG.
- b. Use your existing personal GPG key — fewer keys to manage, but the private
  key lands in repo secrets and revocation is entangled with everything else it
  signs.
- c. Switch to cosign keyless (Sigstore) — no key custody at all and arguably
  the modern path, but it means reworking the wired goreleaser `signs` block and
  the verification story before the first release.

**Decision (2026-07-29): a** — dedicated release-signing GPG key; Donald
generates it and sets the repo secrets himself.

### OQ-4: QEMU e2e tier in CI

- **a. Keep it local-only for v0.1.0 (recommended).** The protocol tier already
  runs in CI; the QEMU tier needs OVMF + a custom-embedded `ipxe.efi` staged as
  artifacts and adds minutes of runtime. Documented skip behavior already
  handles this cleanly.
- b. Add a nightly (`schedule:`) workflow that caches OVMF/iPXE artifacts and
  runs the full VM boot — real firmware coverage, deferred from PR latency.
- c. Run it in PR CI — maximum coverage, slowest feedback.

**Decision (2026-07-29): a** — QEMU tier stays local-only for v0.1.0.

### OQ-5: Package doc convention

- **a. Rewrite package comments in place, in each package's lead file
  (recommended).** The comments already live there (`tftp`, `proxydhcp` are
  strong as-is), the guide quotes some of them (smaller guide==code ripple), and
  no new files.
- b. Move every package comment to a dedicated `doc.go` per package (`go-doc`
  skill convention) — cleaner separation and room for `# Usage`
  sections/examples, at the cost of touching every package and the chapters that
  quote the current headers.

**Decision (2026-07-29): both** — rewrite every package comment in final-state
terms (a) and house the rewritten comment in a dedicated `doc.go` per package
(b). Guide==code: chapters quoting the current lead-file headers update in the
same PR.

### OQ-6: Distribution beyond archives + GHCR

- **a. Defer (recommended).** Archives, `go install`, and GHCR cover the v0.1.0
  audience (you + the homelab platform). Add channels when someone asks.
- b. Add a Homebrew tap now — goreleaser's `brews` block makes it cheap (needs a
  `homebrew-tap` repo + token), and macOS is a primary dev platform.

**Decision (2026-07-29): a** — defer; archives, `go install`, and GHCR cover the
v0.1.0 audience.

## References

- [ADR-0002 — booty is a library, cmd/booty the reference consumer](../adr/0002-booty-is-a-library-with-cmdbooty-as-reference-consumer.md)
- [ADR-0001 — HCL for catalog configuration](../adr/0001-hcl-for-catalog-configuration.md)
- [DESIGN-0002 — Starlight docs site on Cloudflare](./0002-starlight-docs-site-on-cloudflare-pages.md)
- `.github/workflows/ci.yml`, `.github/workflows/release.yml` — the wired
  pipeline this design makes real
- [pr-semver-bump](https://github.com/jefflinse/pr-semver-bump) — label-driven
  version bump action
- [goreleaser docs](https://goreleaser.com/) — archives, SBOMs, signs, brews
- [pkg.go.dev about page](https://pkg.go.dev/about) — indexing behavior for new
  modules
- `docs/go-ipxe/` — the walkthrough; guide==code applies to package-comment
  edits
