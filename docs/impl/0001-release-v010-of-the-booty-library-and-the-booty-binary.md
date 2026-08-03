---
id: IMPL-0001
title: "Release v0.1.0 of the booty library and the booty binary"
status: Draft
author: Donald Gifford
created: 2026-07-29
---

<!-- markdownlint-disable-file MD025 MD041 -->

# IMPL 0001: Release v0.1.0 of the booty library and the booty binary

**Status:** Draft **Author:** Donald Gifford **Date:** 2026-07-29

<!--toc:start-->
- [Objective](#objective)
- [Scope](#scope)
  - [In Scope](#in-scope)
  - [Out of Scope](#out-of-scope)
- [Implementation Phases](#implementation-phases)
  - [Phase 1: Release plumbing prerequisites](#phase-1-release-plumbing-prerequisites)
    - [Tasks (Phase 1)](#tasks-phase-1)
    - [Success Criteria (Phase 1)](#success-criteria-phase-1)
  - [Phase 2: Package documentation (doc.go per package)](#phase-2-package-documentation-docgo-per-package)
    - [Tasks (Phase 2)](#tasks-phase-2)
    - [Success Criteria (Phase 2)](#success-criteria-phase-2)
    - [Notes (Phase 2)](#notes-phase-2)
  - [Phase 3: CI green on GitHub runners + required checks](#phase-3-ci-green-on-github-runners--required-checks)
    - [Tasks (Phase 3)](#tasks-phase-3)
    - [Success Criteria (Phase 3)](#success-criteria-phase-3)
    - [Blocked: the Docs workflow gates every PR on an unbuilt site](#blocked-the-docs-workflow-gates-every-pr-on-an-unbuilt-site)
  - [Pre-release audit (2026-08-03)](#pre-release-audit-2026-08-03)
    - [OQ-7: How much of the API reshaping lands before v0.1.0](#oq-7-how-much-of-the-api-reshaping-lands-before-v010)
    - [OQ-8: TFTP amplification and socket exhaustion](#oq-8-tftp-amplification-and-socket-exhaustion)
  - [Phase 4: Cut v0.1.0](#phase-4-cut-v010)
    - [Tasks (Phase 4)](#tasks-phase-4)
    - [Success Criteria (Phase 4)](#success-criteria-phase-4)
  - [Phase 5: Consumer-path validation](#phase-5-consumer-path-validation)
    - [Tasks (Phase 5)](#tasks-phase-5)
    - [Success Criteria (Phase 5)](#success-criteria-phase-5)
- [File Changes](#file-changes)
- [Testing Plan](#testing-plan)
- [Dependencies](#dependencies)
- [Open Questions](#open-questions)
  - [OQ-1: How Renovate PRs satisfy the PR Label Check](#oq-1-how-renovate-prs-satisfy-the-pr-label-check)
  - [OQ-2: Scope of the # Usage examples in doc.go](#oq-2-scope-of-the--usage-examples-in-docgo)
  - [OQ-3: Branch protection mechanism](#oq-3-branch-protection-mechanism)
  - [OQ-4: Which commit the v0.0.0 seed tag points at](#oq-4-which-commit-the-v000-seed-tag-points-at)
  - [OQ-5: Linux validation environment for Phase 5](#oq-5-linux-validation-environment-for-phase-5)
  - [OQ-6: Where the scratch consumer module lives](#oq-6-where-the-scratch-consumer-module-lives)
- [References](#references)
<!--toc:end-->

## Objective

Execute
[DESIGN-0001](../design/0001-first-release-of-the-booty-library-and-the-booty-binary.md)
(Approved): make the wired release pipeline real and cut `v0.1.0` — the first
public release of the booty library and its reference consumer, the booty binary
— then validate both from released artifacts only. All of DESIGN-0001's open
questions are decided; this doc turns those decisions into concrete, checkable
steps with exact commands. Open questions here are _implementation-level_
choices the design left unspecified.

**Implements:** DESIGN-0001

## Scope

### In Scope

- Repo plumbing: semver labels, labeler labels, release-signing and Codecov
  secrets, Renovate verification, branch protection.
- Package documentation: `doc.go` per library package with final-state comments
  (DESIGN-0001 OQ-5: "both"), proofread for pkg.go.dev.
- CI proven green on GitHub runners and promoted to required checks.
- The `v0.0.0` seed tag, the `minor`-labeled release PR, and the `v0.1.0`
  release (archives, SBOMs, signed checksums, GHCR image).
- Consumer-path validation on macOS, Linux, the container image, `go install`,
  and a scratch importing module.

### Out of Scope

- Everything DESIGN-0001 rules out: v1.0.0 API commitments, Homebrew/nix/apt
  (OQ-6: defer), QEMU tier in CI (OQ-4: local-only), machinery-based Talos
  config generation.
- The docs site — that is
  [DESIGN-0002](../design/0002-starlight-docs-site-on-cloudflare-pages.md) and
  will get its own impl doc.
- Any Go API changes. If validation surfaces one, it is a pre-tag bug fix, not
  scope.

## Implementation Phases

Each phase builds on the previous one. A phase is complete when all its tasks
are checked off and its success criteria are met. Phases map 1:1 to
DESIGN-0001's phases; the PR batching follows OQ-1's decision.

---

### Phase 1: Release plumbing prerequisites

One-time repo configuration. The semver labels come first — the `PR Label Check`
workflow fails every PR until they exist, so nothing else can merge.

#### Tasks (Phase 1)

- [x] Create the four semver labels (`major`, `minor`, `patch`, `dont-release`)
      — done by running `./scripts/labels.sh`, which derives every label from
      `.github/labeler.yml` + `.github/workflows/pr-labels.yml` and owns the
      canonical colors/descriptions. Prefer the script over ad-hoc
      `gh label create` so label definitions stay checked in.
- [x] Verify the ten labels `.github/labeler.yml` references exist (`go`,
      `dependencies`, `documentation`, `ci`, `repo`, `feature`, `fix`, `chore`,
      `docs`, `security`) — `./scripts/labels.sh --dry-run` reports 14/14
      present, 0 to create.
- [ ] **(Donald)** Generate the dedicated release-signing GPG key (suggested:
      `gpg --quick-generate-key "booty release <dgifford06@gmail.com>" ed25519 sign 2y`),
      then set the secrets: `gh secret set GPG_PRIVATE_KEY` (armored
      `--export-secret-keys`) and `gh secret set GPG_FINGERPRINT`.
- [ ] **(Donald)** Export the public key to `docs/booty-release.pub.asc`, commit
      it, and note the fingerprint in the README Release section.
- [ ] **(Donald)** Enable the repo on Codecov and `gh secret set CODECOV_TOKEN`.
- [x] Confirm Dependabot alerts are enabled (the `dependabot-severity-label`
      workflow depends on them) —
      `gh api /repos/donaldgifford/booty/vulnerability-alerts` returns 204.
- [ ] **(Donald)** Confirm the Renovate app covers the repo: no onboarding PR
      and no Dependency Dashboard issue exist yet, so either the app is not
      installed on `booty` or it has not run (the shared preset schedules
      "before 6am on monday"). Install/verify at
      <https://github.com/apps/renovate>.
- [x] Add `addLabels: ["patch"]` to `renovate.json5` (OQ-1) so Renovate PRs
      arrive pre-labeled and pass the label check; the rare major-worthy bump
      gets relabeled by hand before merge. **Corrected mechanism:** the shared
      preset already sets `labels: ["dependencies"]`, and Renovate's `labels`
      _replaces_ rather than appends — a local `labels` key would clobber it.
      `addLabels` is mergeable, so Renovate PRs carry `dependencies` + `patch`
      (plus `security` on vulnerability alerts), which is still exactly one
      semver label for the check. Validated with `renovate-config-validator`.
- [x] Open the plumbing-test PR — the pending docs (DESIGN-0001/0002 + this doc)
      — labeled `dont-release`; confirm `PR Label Check` passes, the labeler
      applies `docs`/`documentation`, and every CI job triggers. Opened as
      [PR #2](https://github.com/donaldgifford/booty/pull/2): the label check
      passes, and the labeler applied `documentation`, `docs`, `ci`, `repo`, and
      `dependencies` alongside `dont-release`. Every workflow triggered.

#### Success Criteria (Phase 1)

- `gh label list` shows all four semver labels plus every label `labeler.yml`
  references.
- `gh secret list` shows `GPG_PRIVATE_KEY`, `GPG_FINGERPRINT`, and
  `CODECOV_TOKEN`; `docs/booty-release.pub.asc` is committed.
- The test PR passes `PR Label Check`, gets auto-labeled by path, and triggers
  every CI job (outcomes fixed in Phase 3).
- Renovate PRs arrive pre-labeled `patch` and pass the label check without
  manual intervention.

---

### Phase 2: Package documentation (doc.go per package)

DESIGN-0001 OQ-5 decided "both": rewrite every package comment in final-state
terms **and** house it in a dedicated `doc.go`. Verified starting point: the
package comments live in the lead files (`catalog/catalog.go`,
`render/render.go`, `httpsrv/httpsrv.go`, `tftp/tftp.go`,
`proxydhcp/proxydhcp.go`); `render` and `httpsrv` are stale ("Chapter 6 adds…");
the guide does **not** quote any `// Package` header verbatim (verified by
grep), so the guide==code ripple is prose references only.

#### Tasks (Phase 2)

- [x] For each of the five library packages: create `doc.go`, move the package
      comment there, delete it from the lead file, and rewrite it to describe
      the final state (no "Chapter N", no "At this stage").
- [x] Rewrite `render` and `httpsrv` comments (confirmed stale); proofread the
      other three (`tftp`, `proxydhcp` are strong — mostly a move).
- [x] Add a prose `# Usage` section with a short fenced snippet to the
      consumer-facing packages (`catalog`, `render`, `httpsrv`) — no
      `example_test.go` for v0.1.0 (OQ-2).
- [x] File the deferred runnable-examples issue (`example_test.go` for
      `catalog`/`render`/`httpsrv`, per OQ-2) —
      [#1](https://github.com/donaldgifford/booty/issues/1).
- [x] Keep `cmd/booty/main.go`'s `// Command booty` comment where it is (already
      final-state; commands conventionally document in `main.go`) — verified
      unchanged and rendering correctly.
- [x] Sweep for stragglers:
      `grep -rn "Chapter\|At this stage" catalog/ render/ httpsrv/ tftp/ proxydhcp/ cmd/`
      → zero hits. Three were symbol-level docs in `catalog/catalog.go`
      (`Profile`, `Render`, `Identity`), not just package comments.
- [x] Guide==code pass: the guide quotes no `// Package` header verbatim, so the
      only ripple was `07-forge-complete.md`'s "the layout, as it actually is"
      tree, which now lists `*/doc.go`. Chapters that _create_ the lead files
      stay as-is (they show the historical build; `doc.go` is the final layout).
- [x] `just check` green — revive's `exported` rule re-verifies every exported
      symbol still has its doc.
- [x] Proofread rendered output:
      `go doc -all ./catalog ./render ./httpsrv ./tftp ./proxydhcp | less`.
      Reviewed by the `go-style` agent; five defects found and fixed — see the
      Phase 2 notes below.
- [x] Add README badges: Go Reference (pkg.go.dev), CI workflow, Codecov, Go
      Report Card, and license. (Go Reference stays "unknown" until the module
      is indexed in Phase 4, and Codecov until the first upload — expected.)

#### Success Criteria (Phase 2)

- Five `doc.go` files exist; no lead file carries the package comment; no doc
  comment references chapters or in-progress state.
- `go doc <pkg>` output reads as a standalone pkg.go.dev landing page for all
  five packages.
- `just check` passes; the guide contains no stale references to package
  comments living in lead files.

#### Notes (Phase 2)

Godoc-rendering defects found by review and fixed — worth knowing before writing
the next package comment:

- **`a.k.a.` truncates the pkg.go.dev summary.** `go/doc.Synopsis` ends the
  synopsis at the first period-space, so
  `"Package proxydhcp implements a PXE proxyDHCP (a.k.a. BINL) service."`
  rendered as the fragment
  `"Package proxydhcp implements a PXE proxyDHCP (a.k.a."` — with an unclosed
  paren — as the package's one-line summary. Avoid abbreviation periods in the
  first sentence.
- **Go doc comments have no Markdown.** `*proxy*` and `` `file` `` rendered as
  literal asterisks and backticks. Use plain words or quotes.
- **Bare repo-relative paths are inert on pkg.go.dev.** The `docs/go-ipxe/*.md`
  pointers are now full `https://github.com/...` URLs, so an external consumer
  arriving from `go get` can actually follow them.
- **A doc claim was factually wrong.** The inherited `proxydhcp` comment called
  `parsePacket`, `buildProxyOffer`, and `buildBootAck` "pure functions"; the
  latter two are `(*Server)` methods reading `s.serverIP` / `s.bootFile`.
  Reworded to "side-effect-free helpers".
- Doc links (`[Source]`, `[net/http]`, `[net/http.Handler]`, and the full import
  path `[github.com/donaldgifford/booty/catalog]`) were verified to resolve, and
  every `# Usage` snippet was checked against the real signatures.

---

### Phase 3: CI green on GitHub runners + required checks

The Phase 2 PR doubles as the matrix-exercising PR (it touches Go code + docs,
so every job triggers). Opened as
[PR #2](https://github.com/donaldgifford/booty/pull/2).

Three classes of bug surfaced on the very first run, none reproducible locally —
exactly what this phase exists to catch. All are fixed on the branch:

1. **`trufflesecurity/trufflehog@v3` does not resolve.** That project publishes
   no floating major tag, only exact versions, so the Secret Scan job failed at
   action-resolution time on every run this repo has ever had. Pinned to
   `v3.96.0`; Renovate will bump it. The workflow also had no top-level `name:`
   and a job called `test`, which rendered as a check named "test" that looked
   like the Go tests — renamed to `Secret Scan` / `trufflehog`.
2. **The changelog drift check could never pass.** `changelog.yml` regenerates
   with git-cliff and fails on any byte difference from the committed
   `CHANGELOG.md`, but the committed file's header prose had been re-wrapped by
   Prettier to a wider column than git-cliff emits — so the check failed
   independent of content. Regenerated the file and added `.prettierignore` so
   Prettier stops re-wrapping it. Prettier is not in CI, so this drift came from
   a local/editor run.
3. **Two reachable CVEs that would have shipped in v0.1.0.** `govulncheck` on
   the runner flagged **GO-2026-5970**, an infinite loop on invalid input in
   `golang.org/x/text`, reachable from `catalog.decodeCatalog` and
   `catalog.evalLocals` through HCL expression evaluation — a malformed catalog
   file could hang the loader. Fixed in x/text v0.39.0. Bumping it surfaced
   **GO-2026-5856**, an Encrypted Client Hello privacy leak in `crypto/tls`
   reachable via `httpsrv.ListenAndServe`, fixed in go1.26.5; `go.mod`,
   `mise.toml`, and the Dockerfile builder were bumped together per CLAUDE.md.
   `govulncheck` is now clean.

Operational note: regenerate the changelog as the **last** commit before
pushing, with a `chore(changelog):` subject — `cliff.toml` skips those from the
body (`^chore.*[Cc]hangelog`), so the commit does not invalidate the file it
just wrote.

#### Tasks (Phase 3)

- [x] Push the Phase 2 PR (labeled `dont-release`) and drive every job green:
      golangci-lint, test-coverage + coverage-gate, e2e protocol tier,
      govulncheck, Trivy, CodeQL, goreleaser snapshot + SBOM locate/scan + SARIF
      upload, docker bake `booty-ci`. **All green** except the Docs jobs, which
      are blocked on DESIGN-0002 (see below). Getting there took three fixes:
      trufflehog's unresolvable tag, the changelog drift check, and two
      reachable CVEs.
- [x] Verify the SBOM-locate glob (`dist/booty_*_linux_amd64.tar.gz.spdx.json`)
      resolves on the runner — the snapshot version string differs from local.
      Confirmed: syft emitted `booty_v0.0.0-dev_linux_amd64.tar.gz.spdx.json`
      and the glob matched it.
- [x] Verify the e2e protocol tier's UDP loopback sockets behave on
      `ubuntu-latest` — `TestE2EProtocolReachability` passes; the QEMU tier
      skips as designed.
- [x] Verify `ghcr.io/donaldgifford/booty:dev-ci` exists after the PR run and
      both Anchore SARIF categories appear in the Security tab alongside CodeQL.
      Confirmed: the manifest pushed, and code-scanning reports four analyses —
      `CodeQL /language:go`, `govulncheck`, `Grype anchore-archive-sbom`, and
      `Grype anchore-dev-image`.
- [x] Turn the `Lint Markdown` Docs job green — borrowed from DESIGN-0002 Phase
      1/3 as no-regret work, since `docs/` must be lint-clean whichever way the
      owner resolves `docs.yml`. Added the `lint-md` recipe (markdownlint-cli2 +
      an anchored MkDocs-admonition guard) and fixed all 49 violations across 13
      files. Now **0 errors across 24 files**. See the Blocked section below for
      the full accounting and why `Build Starlight` was left alone.
- [ ] **Blocked (Donald).** Verify the Codecov upload succeeds with the new
      token and the PR gets a coverage comment. The step runs today but no-ops:
      `CODECOV_TOKEN` is unset and the action has `CC_FAIL_ON_ERROR: false`, so
      it cannot fail the build or report.
- [ ] **(Donald)** Merge; verify post-merge jobs on `main`: the `ci` bake
      validation and the changelog workflow run clean. Held for an owner
      decision — `Build Starlight` is red, so merging means either accepting it
      or resolving the DESIGN-0002 collision first.
- [ ] **(Donald)** Create a repository ruleset for `main` (OQ-3) requiring the
      proven checks — include `PR Label Check` and `Lint Markdown`, exclude
      post-merge-only jobs and `Build Starlight` until DESIGN-0002 lands — plus
      PR-before-merge and no force-pushes.
- [ ] Confirm a follow-up trivial PR cannot merge with a failing required check
      (flip one intentionally or rely on the label check pre-label).

#### Success Criteria (Phase 3)

- One PR cycle with every check green on GitHub runners, and a clean post-merge
  run on `main`.
- `main` is protected: required checks enforced, no direct pushes, no
  force-pushes.
- Security tab shows CodeQL + both Anchore SARIF categories; Codecov shows the
  first uploaded report.

#### Blocked: the Docs workflow gates every PR on an unbuilt site

`.github/workflows/docs.yml` is already on `main`, but the site it builds does
not exist yet — that is
[DESIGN-0002](../design/0002-starlight-docs-site-on-cloudflare-pages.md),
explicitly out of scope here. Its `paths` filter includes `docs/**`, so it runs
on any docs-touching PR and both jobs fail:

- **Build Starlight** — there is no `site/` directory. Unfixable without
  DESIGN-0002 Phase 1. Still red; still an owner decision.
- **Lint Markdown** — runs `just lint-md`, a recipe the justfile did not define
  (DESIGN-0002 Phase 1 adds it via `docs.just`). Adding the recipe alone would
  not have turned the job green: `markdownlint-cli2 'docs/**/*.md'` reported
  **49 errors** in the guide (mostly MD040 fences with no language, plus
  MD031/MD032/MD036), which is DESIGN-0002 Phase 3's cleanup task. **Resolved
  here** — see below.

`Lint Markdown` was fixed inside IMPL-0001 because it is no-regret work: `docs/`
has to be lint-clean under every option the owner might pick for `docs.yml`
(implement DESIGN-0002, gate the jobs on `site/` existing, or revert the
workflow), and the cleanup is mechanical and reversible. What landed:

- The `lint-md` recipe in the root `justfile` rather than `docs.just` —
  DESIGN-0002 can move it when it creates that file. It runs
  `markdownlint-cli2` and then greps for MkDocs-only admonitions (`!!! note`,
  `??? note`), which render as literal text in Starlight and must stay out of
  `docs/` given DESIGN-0002's dual-output decision. The grep is anchored to line
  start with a trailing space so prose _about_ the syntax — DESIGN-0002 itself
  discusses it — does not trip the guard.
- All 49 violations fixed across 13 files: 33 MD040 fence languages, 6 MD036
  emphasis-labels promoted to `###` headings, and the MD031/MD032/MD009/MD022
  blank-line rules auto-fixed. **0 errors across 24 files.**
- `docs/*/README.md` added to `.prettierignore`. docz rewrites the index tables
  and ToCs in those files on every `docz update`; Prettier re-aligns the pipes
  and pads the generated blocks, so the two tools were undoing each other.
  markdownlint is the gate and it passes on docz's native output.

`Build Starlight` remains the single red check and is deliberately not absorbed:
scaffolding another design doc's Phase 1 here would hide the scope collision
rather than resolve it. So Phase 3's "every check green" criterion still cannot
be fully met from inside IMPL-0001 — see the note in Dependencies.

---

### Pre-release audit (2026-08-03)

Three review passes over the public packages — API shape, Uber style, and
security — while Phase 4 was blocked on the signing secrets. Every finding below
was re-verified against the code before being recorded; the security pass
confirmed its findings by driving the running servers with hostile input.

Fixed in this branch, each with a test that fails against the previous code:

| Defect                                                                  | Where                     |
| ----------------------------------------------------------------------- | ------------------------- |
| `tftp.Serve` truncated in-flight transfers on SIGTERM (guide promised otherwise) | `tftp/tftp.go`      |
| `proxydhcp.Serve` ignored ctx entirely — leaked a goroutine per call     | `proxydhcp/proxydhcp.go`  |
| `GET /boot/` returned an HTML directory index of every boot asset        | `httpsrv/httpsrv.go`      |
| Identity strings reached templates unescaped — newline injection         | `httpsrv/httpsrv.go`      |
| HCL diagnostics stringified with `%s`, breaking `errors.As`              | `catalog/source.go`       |
| `TestHandleDHCPIgnoresNonPXE` never called `handleDHCP`                  | `proxydhcp/proxydhcp_test.go` |
| A broken catalog reported as "unknown machine", giving the wrong remedy  | `catalog/catalog.go`      |
| TFTP `timeout` option reflected to a spoofable source unvalidated        | `tftp/tftp.go`            |
| `DirSource{}` with no `Root` silently loaded `*.hcl` from the cwd        | `catalog/source.go`       |
| A gosec exclusion silenced the injection finding on wrong grounds        | `.golangci.yml`           |
| `httpsrv` rescanned `Catalog.Groups` to recover a number `Match` had     | `catalog/catalog.go`      |

Not acted on — these need an owner decision, and the API ones are **breaking
after v0.1.0**, so they belong before Phase 4 rather than after:

- **Design smell — done.** `Resolution` now carries `Specificity`, the selector
  term count `Match` always computed and discarded. `httpsrv.mostSpecificMatch`
  was recovering it by rescanning `Catalog.Groups` by name on every candidate
  MAC — the library's own reference consumer working around its own API, and
  one of the reasons `Catalog.Groups` has to stay exported. Additive, so it does
  not prejudge OQ-7. All four server types now document that their zero value is
  unusable; previously only `tftp` did.
- **API shape.** `proxydhcp.Serve(ctx, conn, binl bool)` is a naked-bool seam
  (`ServeDHCP`/`ServeBINL` would read better); `catalog.Source` is an interface
  with one implementation and no in-repo use, which CLAUDE.md's
  no-speculative-extension-points rule forbids; `PortDHCP`/`PortBoot`/`PortBINL`
  are the library's only exported constants while `tftp` keeps all of its
  unexported; `httpsrv.New` returns no error and silently accepts an unparseable
  `BaseURL`; `tftp.New` is positional while its three peers take a struct; and
  `httpsrv.Options` vs `proxydhcp.Config` are two names for one concept.
- **Missing sentinels — done.** Both cases are fixed, and adding a sentinel is
  additive so neither prejudges OQ-7. `catalog.ErrUnknownProfile` is exported
  and all three `httpsrv` call sites branch on it. `DirSource.Load` now
  distinguishes an unset `Root` ([ErrEmptyRoot], which previously globbed the
  process working directory), a missing directory (wraps `os.ErrNotExist`), a
  non-directory ([ErrRootNotDirectory]), and a directory holding no catalog
  ([ErrNoCatalogFiles]).
- **Security posture.** TFTP is a measured 121x UDP amplifier bound to
  `0.0.0.0:69` by default, and each unauthenticated datagram holds a socket for
  the full 20s retry budget with no concurrency bound; `proxydhcp` honours the
  unauthenticated `giaddr` field, letting any host on the segment aim booty's
  reply at an arbitrary off-subnet IP; `--proxmox-token` is passed as a command
  line argument and is therefore visible in `ps`; and both path
  guards are lexical, so a symlink in the boot dir escapes it — accepted
  explicitly in `docs/go-ipxe/03-tftp-from-scratch.md`, but `tftp.go`'s comment
  still claims the path is "guaranteed" to sit under `bootDir`, which is false.
- **Performance — first measured data.** The repo had no benchmarks at all, so
  nothing here was ever measured. A profile of the `/ipxe` boot path shows cost
  growing linearly with catalog size — 22 µs and 158 allocations against 8
  groups, 209 µs and 1,732 allocations against 128 — and
  `Match` → `matchSelector` → `NormalizeMAC` accounts for **19.9% of all bytes
  allocated** in that path. The cause is that `matchSelector` re-normalizes each
  group's selector MAC on every request, so a 128-group catalog repeats that
  work 128 times per booting machine. Two fixes, both cheap: hoist the
  `strings.NewReplacer` out of `NormalizeMAC` (it compiles a trie per call), and
  normalize selector MACs once at load instead of per match. The second is a
  visible change to `Catalog.Groups` data, so it is deliberately left for OQ-7
  rather than slipped in. Nothing else is hot: `parsePacket` is 219 ns / 5
  allocs and `handleDHCP` 1.9 µs / 20 allocs, which no realistic PXE burst will
  notice.
- **Tooling — correction.** An earlier revision of this section said `gosec` was
  not enabled. That was wrong, and it is worth recording how: the security pass
  reported it, and it was written down here without being checked. `gosec` has
  been enabled in `.golangci.yml` all along and runs in CI through
  golangci-lint. What actually happened is more interesting — `gosec` **did**
  flag the template-injection sinks (G705), and an exclusion suppressed them on
  the grounds that "httpsrv responses are … never browser HTML, so the XSS taint
  analysis does not apply." That is true and beside the point: the risk was
  never XSS, it was injection into the rendered YAML/TOML, which is exactly the
  defect fixed above. A correct-sounding rationale addressing the wrong threat
  silenced a true positive for as long as it stood. The exclusion comments for
  G705 and G304 now state what is actually being accepted and what invalidates
  it. A dead `G104` exclusion scoped to `cmd/(compare|diff).go` — files that
  have never existed in this repo — was removed as template residue.

#### OQ-7: How much of the API reshaping lands before v0.1.0

- **a. Take the breaking ones now (recommended).** v0 semver permits movement,
  but every one of these costs a major-version conversation later. The list is
  bounded and mechanical, and `cmd/booty` is the only in-tree consumer.
- b. Ship v0.1.0 as-is and batch the changes into v0.2.0 — faster to a release,
  but the homelab platform starts importing the shapes we already know are wrong.
- c. Take only the two with behavioural consequences (`httpsrv.New` validating
  `BaseURL`, the `Match` sentinel) and leave the naming to v0.2.0.

#### OQ-8: TFTP amplification and socket exhaustion

- **a. Bound in-flight transfers and require the first ACK before retransmitting
  (recommended).** A concurrency cap plus not blind-retransmitting to a peer
  that has never answered removes both the 121x amplification and the ~51 pkt/s
  fd exhaustion, without a config surface.
- b. Document the exposure and tell operators to bind `--tftp-addr` to a
  provisioning VLAN — matches the trust model the guide already states, but
  ships a known amplifier on a default of `0.0.0.0:69`.
- c. Add a rate limiter with operator-tunable knobs — most control, most new
  public surface for a v0.1.0.

### Phase 4: Cut v0.1.0

Label flow end-to-end, per DESIGN-0001 OQ-1: seed `v0.0.0`, release PR labeled
`minor`.

#### Tasks (Phase 4)

- [ ] Seed the baseline tag at the merge commit of the Phase 2/3 PR (OQ-4):
      `git tag -a v0.0.0 -m "Baseline for pr-semver-bump; not a release" <merge-commit> && git push origin v0.0.0`
      (no workflow fires on tag push — verified in `.github/workflows/`).
- [ ] Pre-flight at the release commit: `just ci` and `just release-local` both
      clean. **Cannot be checked off until that commit exists**, but everything
      it depends on has been verified from this branch, so the step should be a
      formality rather than a discovery: `just ci` completes (it needed the
      `GOTOOLCHAIN` fix first), `just release-local` produces 4 archives + 4
      SBOMs + `checksums.txt`, `goreleaser check` validates `.goreleaser.yml`
      against the v2 schema, and the Go version agrees across all three places
      it is pinned — `go.mod`, `mise.toml` and the `Dockerfile` builder stage
      are all on 1.26.5. That last one is the CLAUDE.md gotcha that bites when
      only `go.mod` gets bumped.
- [ ] Open the release PR (a trivial docs/README touch is fine), label it
      `minor`, merge it.
- [ ] Watch `release.yml`: pr-semver-bump tags `v0.1.0` → goreleaser publishes
      the GitHub release → docker job pushes GHCR.
- [ ] Verify the release page: **4** archives (linux+darwin × amd64+arm64) each
      with an `.spdx.json` SBOM — 10 assets total with `checksums.txt` +
      `checksums.txt.sig` — and notes grouped by conventional-commit type.
      (Corrected from "8 archives" on 2026-08-02: `.goreleaser.yml` declares
      `goos: [linux, darwin] × goarch: [amd64, arm64]` and one tar.gz archive
      per target, so 4. A `just release-local` snapshot emitted exactly
      4 `.tar.gz` + 4 `.spdx.json` + `checksums.txt`; 8 was the archive+SBOM
      file count, not the archive count.)
- [ ] Verify the signature from a clean keyring:
      `gpg --import docs/booty-release.pub.asc && gpg --verify checksums.txt.sig checksums.txt`.
- [ ] Verify GHCR: tags `0.1.0`, `0.1`, `v0`, `latest`; OCI annotations render
      on the package page; SBOM + provenance attestations attached
      (`docker buildx imagetools inspect`).
- [ ] Trigger pkg.go.dev indexing:
      `GOPROXY=proxy.golang.org go list -m github.com/donaldgifford/booty@v0.1.0`;
      confirm <https://pkg.go.dev/github.com/donaldgifford/booty> renders the
      Phase 2 docs for all five packages.
- [ ] Confirm the regenerated `CHANGELOG.md` on `main` includes `v0.1.0`.

#### Success Criteria (Phase 4)

- `v0.1.0` exists as tag + GitHub release with all expected assets and a
  signature that verifies against the committed public key.
- `docker pull ghcr.io/donaldgifford/booty:0.1.0` works; `docker run … version`
  prints `booty v0.1.0` + commit + date.
- pkg.go.dev shows `booty@v0.1.0` with clean docs and the Apache-2.0 license.

---

### Phase 5: Consumer-path validation

Released artifacts only — no repo checkout in any validation step (the example
catalog is fetched from the tag, not a working tree).

#### Tasks (Phase 5)

- [ ] macOS/arm64: download `booty_0.1.0_darwin_arm64.tar.gz` from the release
      page, `shasum -a 256 -c` against `checksums.txt`, extract, and run the
      README quickstart (`validate`, `serve`, curl `/healthz`,
      `/machine-config?mac=…`, `POST /proxmox/answer`) against the example
      catalog fetched at `v0.1.0`.
- [ ] Linux: repeat with the `linux_arm64` archive inside a plain
      `debian:stable-slim` container on the mac (OQ-5).
- [ ] `go install github.com/donaldgifford/booty/cmd/booty@v0.1.0` in a clean
      environment (empty `GOBIN`/`GOMODCACHE` or a container); `booty version`
      prints `v0.1.0`.
- [ ] Container: `docker run` `ghcr.io/donaldgifford/booty:0.1.0 serve` with the
      catalog + boot dir mounted, `--tftp-addr`/`--proxydhcp-addr` remapped per
      the nonroot caveat; same curl smoke.
- [ ] Library consumer smoke in a throwaway local directory, e.g.
      `/tmp/booty-consumer` (OQ-6): a scratch module importing `catalog`,
      `render`, `httpsrv` that loads the example catalog and serves `/ipxe` —
      compiles and runs against `@v0.1.0` using only the public API; record the
      exact commands in this doc when executed.
- [ ] File an issue for every friction point found (doc gap, flag surprise,
      unclear error); mark each fix-now (→ `patch` release) or defer with
      rationale.
- [ ] Update the README if anything proved unclear during validation.
- [ ] Flip DESIGN-0001 → Implemented and this doc → Completed; record the
      release date in both.

#### Success Criteria (Phase 5)

- The README quickstart works verbatim from released artifacts on macOS, Linux,
  and the container image.
- `go install …@v0.1.0` and the scratch-module import both work with no
  reference to a repo checkout.
- Zero unresolved release-blocking issues; every deferred item is filed with a
  rationale.

---

## File Changes

| File                                  | Action | Description                                        |
| ------------------------------------- | ------ | -------------------------------------------------- |
| `catalog/doc.go`                      | Create | Package comment moved from `catalog.go`, rewritten |
| `render/doc.go`                       | Create | Rewritten final-state comment (stale text dropped) |
| `httpsrv/doc.go`                      | Create | Rewritten final-state comment (stale text dropped) |
| `tftp/doc.go`                         | Create | Package comment moved from `tftp.go`               |
| `proxydhcp/doc.go`                    | Create | Package comment moved from `proxydhcp.go`          |
| `catalog/catalog.go` (+ 4 lead files) | Modify | Package comment removed                            |
| `docs/booty-release.pub.asc`          | Create | Release-signing public key                         |
| `renovate.json5`                      | Modify | Add `addLabels: ["patch"]` (OQ-1)                  |
| `.github/workflows/trufflehog.yml`    | Modify | Pin to a resolvable tag; name the workflow/job     |
| `.prettierignore`                     | Create | Keep Prettier off `CHANGELOG.md` + docz's indexes  |
| `justfile`                            | Modify | Add the `lint-md` recipe the Docs workflow calls   |
| `go.mod` / `mise.toml` / `Dockerfile` | Modify | x/text v0.39.0; Go 1.26.5 (GO-2026-5970/5856)      |
| `README.md`                           | Modify | Badges; key fingerprint; validation-driven fixes   |
| `docs/adr/*`, `docs/go-ipxe/*`        | Modify | Moved-comment refs; markdownlint cleanup (49 fixes) |
| `CHANGELOG.md`                        | —      | Regenerated by workflow, not hand-edited           |

## Testing Plan

- [ ] `just check` / `just ci` locally before every push (Phases 2–4).
- [ ] The full CI matrix green on GitHub runners is itself the Phase 3 test.
- [ ] `just release-local` snapshot before tagging (Phase 4 pre-flight).
- [ ] Consumer-path smoke tests (Phase 5) are the release's acceptance tests:
      archive + checksum + quickstart on two OSes, `go install`, container run,
      scratch-module compile.

## Dependencies

- **(Donald)** GPG key generation, Codecov enablement, and the resulting secrets
  — blocks Phase 1 completion, and `GPG_PRIVATE_KEY`/`GPG_FINGERPRINT`
  hard-block Phase 4: goreleaser's `signs` block cannot produce
  `checksums.txt.sig` without them, so no release can be cut.
- **(Donald) Decision needed — the Docs workflow's `Build Starlight` job.**
  `docs.yml` gates every docs-touching PR on a Starlight site that DESIGN-0002
  has not built yet. Its `Lint Markdown` job is now green (see Phase 3), but
  `Build Starlight` fails on all PRs including this one, because there is no
  `site/`. Options: (a) implement DESIGN-0002 (it needs its own impl doc first —
  recommended, since the workflow is already committed and expects it); (b) gate
  the `build` job on `site/` existing, so it stops blocking unrelated work until
  the site lands; (c) revert the `build` job from `main` and re-add it with
  DESIGN-0002. The markdown cleanup already done here is valid under all three.
- GitHub-side state: Renovate app installation, Dependabot alerts,
  branch-protection permissions.
- No new Go dependencies.

## Open Questions

All resolved — every question was decided **a** (OQ-1–5 on 2026-07-29, OQ-6 on
2026-08-02), recorded per question below. Format: **a** was the recommendation;
later letters were alternatives.

### OQ-1: How Renovate PRs satisfy the PR Label Check

`renovate.json5` only `extends` the upstream `donaldgifford/renovate-config`
preset — no `labels` — and the label check requires exactly one semver label on
every PR, so Renovate PRs will sit red until labeled.

- **a. Add `"labels": ["patch"]` to this repo's `renovate.json5`
  (recommended).** Dependency bumps are patch-by-default; a rare major-worthy
  bump gets manually relabeled before merge. One line, no upstream change,
  self-serve forever.
- b. Add the default in the upstream `renovate-config` preset so every repo
  inherits it — right long-term home, but changes shared config for a single
  repo's need and couples this release to another repo's PR cycle.
- c. Manually label each Renovate PR at review time — zero config, but every
  update stalls red until a human intervenes, forever.

**Decision (2026-07-29): a** — a `patch` label set in this repo's
`renovate.json5`; relabel the rare major-worthy bump by hand.

**Implementation note (2026-08-02):** implemented as `addLabels: ["patch"]`, not
`labels: ["patch"]`. The shared preset (`github>donaldgifford/renovate-config`)
already sets `labels: ["dependencies"]`; Renovate's `labels` replaces rather
than appends, so a local `labels` key would silently drop `dependencies`.
`addLabels` is mergeable and preserves both. Same intent, non-destructive
mechanism.

### OQ-2: Scope of the `# Usage` examples in doc.go

- **a. Prose `# Usage` with a short fenced code snippet in `catalog`, `render`,
  and `httpsrv` only; no `example_test.go` for v0.1.0 (recommended).** These
  three are what an external consumer wires together (the README snippet proves
  the shape); `tftp`/`proxydhcp` are protocol servers whose comments already
  explain usage. Runnable `Example` functions are better long-term but add
  compile-checked surface right before a release — defer to a filed issue.
- b. Add runnable `example_test.go` `Example` functions for the three
  consumer-facing packages now — pkg.go.dev renders them interactively and
  they're compile-checked, at the cost of more pre-release churn.
- c. No usage sections at all — package comments only, ship the minimum.

**Decision (2026-07-29): a** — prose `# Usage` in `catalog`/`render`/ `httpsrv`
only; runnable `Example` functions deferred to a filed issue.

### OQ-3: Branch protection mechanism

- **a. A repository ruleset (recommended).** The current-generation mechanism:
  layerable, supports required checks + PR-before-merge + block-force-push,
  manageable via `gh api /repos/…/rulesets`, and what GitHub is investing in.
- b. Classic branch protection on `main` — the familiar option, same practical
  effect at this repo's scale, but legacy.

**Decision (2026-07-29): a** — repository ruleset.

### OQ-4: Which commit the `v0.0.0` seed tag points at

- **a. The merge commit of the Phase 2/3 PR — the last commit before the release
  PR (recommended).** Keeps `v0.0.0..v0.1.0` a truthful, tiny changelog range
  (just the release PR), and the tag lands on fully-CI-proven history.
- b. The repo's root commit — makes `v0.0.0..v0.1.0` span the whole history, so
  the first release notes/changelog enumerate everything ever merged (noisy, but
  a complete record).

**Decision (2026-07-29): a** — tag the merge commit of the Phase 2/3 PR.

### OQ-5: Linux validation environment for Phase 5

- **a. Docker on the mac — run the `linux_arm64` archive inside a plain
  `debian:stable-slim` container (recommended).** Zero infrastructure, fully
  scriptable, and it validates the archive (the distroless image is validated
  separately in the same phase).
- b. A homelab VM/host — closest to the real deployment target (and could double
  as a real PXE smoke), but couples release validation to homelab state.
- c. An ad-hoc GitHub Actions workflow job that downloads the release asset and
  runs the smoke — repeatable for every future release, but CI code written for
  a one-time gate.

**Decision (2026-07-29): a** — the `linux_arm64` archive in a
`debian:stable-slim` container on the mac.

### OQ-6: Where the scratch consumer module lives

- **a. A throwaway local directory (e.g. `/tmp/booty-consumer`), with the exact
  commands recorded in this doc when executed (recommended).** The point is
  proving the public API from outside the repo; committing it would create a
  second consumer to maintain.
- b. A tiny separate repo (`donaldgifford/booty-consumer-smoke`) — rerunnable
  for future releases and a public API example, at the cost of another repo to
  keep compiling.
- c. In-repo under `examples/consumer/` as a separate Go module — visible to
  users, but an in-repo module needs care to stay excluded from the main
  build/test/lint surface.

**Decision (2026-08-02): a** — throwaway local directory; commands recorded here
when Phase 5 runs.

## References

- [DESIGN-0001 — First release of the booty library and the booty binary](../design/0001-first-release-of-the-booty-library-and-the-booty-binary.md)
  — the approved design this implements (all design OQs decided 2026-07-29)
- [ADR-0002 — booty is a library, cmd/booty the reference consumer](../adr/0002-booty-is-a-library-with-cmdbooty-as-reference-consumer.md)
- `.github/workflows/ci.yml`, `.github/workflows/release.yml`,
  `.github/workflows/pr-labels.yml` — the pipeline being proven
- [pr-semver-bump](https://github.com/jefflinse/pr-semver-bump) — label-driven
  version bump action
- [goreleaser docs](https://goreleaser.com/) — archives, SBOMs, signs
- [Go doc comments](https://go.dev/doc/comment) — `doc.go` and `# Usage` section
  conventions
- [pkg.go.dev about](https://pkg.go.dev/about) — indexing behavior for new
  modules
