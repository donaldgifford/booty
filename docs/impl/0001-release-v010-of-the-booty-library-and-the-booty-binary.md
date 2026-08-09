---
id: IMPL-0001
title: "Release v0.1.0 of the booty library and the booty binary"
status: Completed
author: Donald Gifford
created: 2026-07-29
---

<!-- markdownlint-disable-file MD025 MD041 -->

# IMPL 0001: Release v0.1.0 of the booty library and the booty binary

**Status:** Completed **Author:** Donald Gifford **Date:** 2026-07-29
**Completed:** 2026-08-04 — v0.1.0 released and validated from published
artifacts; v0.1.1 carries the two version-reporting fixes Phase 5 surfaced.

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
  - [Phase 3b: Settle the API and close the TFTP exposure (OQ-7a, OQ-8a)](#phase-3b-settle-the-api-and-close-the-tftp-exposure-oq-7a-oq-8a)
    - [Tasks (Phase 3b)](#tasks-phase-3b)
    - [Success Criteria (Phase 3b)](#success-criteria-phase-3b)
  - [Phase 4: Cut v0.1.0](#phase-4-cut-v010)
    - [Tasks (Phase 4)](#tasks-phase-4)
      - [Two findings from the pre-flight and verification](#two-findings-from-the-pre-flight-and-verification)
    - [Success Criteria (Phase 4)](#success-criteria-phase-4)
  - [Phase 5: Consumer-path validation](#phase-5-consumer-path-validation)
    - [Tasks (Phase 5)](#tasks-phase-5)
      - [Friction found during validation](#friction-found-during-validation)
    - [Success Criteria (Phase 5)](#success-criteria-phase-5)
- [Remaining owner-gated work](#remaining-owner-gated-work)
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
- [x] **(Donald)** Generate the dedicated release-signing GPG key, then set the
      secrets: `gh secret set GPG_PRIVATE_KEY` (armored `--export-secret-keys`)
      and `gh secret set GPG_FINGERPRINT`. Done — `gh secret list` shows both.
      The key used is the existing RSA-4096 "Donald Gifford (Github Package
      Signing)" key rather than a freshly generated ed25519 one; the suggestion
      above was only a suggestion, and reusing an established signing key means
      anyone who already trusts it needs no new trust decision.
- [x] **(Donald)** Export the public key to `docs/booty-release.pub.asc`, commit
      it, and note the fingerprint in the README Release section. The README now
      carries the fingerprint plus the two-step verify (signature over
      `checksums.txt`, then archive against checksum) — verifying only the
      signature attests to nothing about the archive you downloaded.
- [x] **(Donald)** Enable the repo on Codecov and `gh secret set CODECOV_TOKEN`.
      Done 2026-08-06; `gh secret list` now shows all three secrets.
- [x] Confirm Dependabot alerts are enabled (the `dependabot-severity-label`
      workflow depends on them) —
      `gh api /repos/donaldgifford/booty/vulnerability-alerts` returns 204.
- [x] **(Donald)** Confirm the Renovate app covers the repo. Installed
      2026-08-06. No onboarding PR or Dependency Dashboard issue has appeared
      yet, which is expected rather than wrong: the shared preset schedules
      "before 6am on monday", so the first run is pending its window.
      `renovate.json5` validates against the schema and already carries the
      `addLabels: ["patch"]` fix, so the first PR should satisfy `PR Label
      Check` without intervention — that is the thing to watch when it lands.
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
- [x] Verify the Codecov upload succeeds with the new token and the PR gets a
      coverage comment. Before the secret existed this step ran but no-opped:
      `CODECOV_TOKEN` was unset and the action has `CC_FAIL_ON_ERROR: false`, so
      it could neither report nor fail the build — the quiet kind of broken.
      Verified end to end on PR #9's own CI run (commit `c919da9`): the action
      logged `Token length: 36` and `CLI integrity verified`, uploaded
      `coverage.out`, and the Codecov API now returns `"state": "complete"` for
      that commit — 7 files, 1208 lines, **70.03%** aggregate. `codecov-commenter`
      posted on the PR. The aggregate sits below every per-package figure because
      it includes `cmd/booty` at 18.5%; the 60% floor is per-package and is
      enforced by `just coverage-gate`, not by Codecov.
- [x] Merge; verify post-merge jobs on `main`: the `ci` bake validation and the
      changelog workflow run clean. The `Build Starlight` blocker was removed
      first by gating that job on `site/package.json` existing, so it skips
      until DESIGN-0002 lands instead of failing every PR — a permanently red
      check is worse than no check, because it teaches everyone to merge past
      CI. Four PRs have since merged (#2–#5) with post-merge `CI`, `Changelog
      Regen`, `govulncheck`, `CodeQL`, `Secret Scan`, `License Check` and
      `Release` all green on `main`.
- [x] **(Donald)** Create a repository ruleset for `main` (OQ-3). Done, active,
      blocking `deletion` and `non_fast_forward` — the two irreversible ones.
      `main` cannot be deleted and its history cannot be rewritten.
- [ ] **(Donald, optional)** Add required status checks and PR-before-merge to
      that ruleset. Deliberately not done, and worth being clear about the gap:
      a PR can still merge with CI red, and anyone with push access can commit
      straight to `main`. The check names are settled across six PRs if this is
      wanted later — `Lint`, `Test Go`, `Build`, `Docker Build`, `Analyze Go
      (go)`, `Security Scan`, `Secret Scan`, `Check Dependency Licenses`, `Check
      Required Labels`, `Lint Markdown`, `Detect site`, `check`, `grype` —
      excluding `Build Starlight` until DESIGN-0002 lands. Required checks match
      by exact name, so a typo makes every PR unmergeable until the ruleset is
      edited; that is the whole reason to add them deliberately rather than
      early.
- [ ] Confirm a follow-up trivial PR cannot merge with a failing required check.
      Not applicable until required checks exist.

#### Success Criteria (Phase 3)

- One PR cycle with every check green on GitHub runners, and a clean post-merge
  run on `main`.
- `main` is protected: no force-pushes, no deletion — both enforced by an active
  ruleset. Required status checks and PR-before-merge are **not** enforced; see
  "Remaining owner-gated work" for what that does and does not buy.
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
- **Performance — fixed.** The repo had no benchmarks, so nothing here had ever
  been measured. One line of accidental per-request work was costing roughly
  90% of every boot request: `NormalizeMAC` built its `strings.NewReplacer`
  inline, and because the replacements are empty strings that falls to the
  generic trie replacer, which allocates ~6.7 kB **at construction**. `Match`
  called it twice per group, so a request against a 128-group catalog built
  ~250 tries and allocated 1.69 MiB.

  Fixed by hoisting the replacer to package scope and normalizing the
  identity's MAC once per `Match` instead of once per group. Measured:

  | Path                      | Before   | After   |
  | ------------------------- | -------- | ------- |
  | `/ipxe`, 128 groups       | 216 µs   | 20 µs   |
  | `/ipxe`, 128 groups B/op  | 1686 KiB | 16 KiB  |
  | `/machine-config`         | 247 µs   | 19 µs   |
  | `/ipxe` parallel          | 239 µs   | 10 µs   |
  | `NormalizeMAC`            | 656 ns   | 81 ns   |

  The parallel number is the tell: before the fix, concurrent requests were
  _slower_ than serial (239 µs vs 216 µs), because every core producing 1.7 MB
  of garbage kept the process in continuous GC. It scales properly now.

  This cost was invisible for the entire life of the code — nothing failed, and
  benchmarks only help if somebody runs and compares them. `catalog/alloc_test.go`
  now makes the class of regression loud: `testing.AllocsPerRun` budgets on
  `Match` and `NormalizeMAC` that run in the ordinary suite, set well above what
  the code does today and well below what it did per-call. Verified by reverting
  the hoist — both fail with the reason named.

  Everything else was checked and is right for this workload, so no release
  time should go into it. TFTP per-block allocation was implemented and A/B'd:
  it removes 95% of the garbage and buys 0–5% throughput, because stop-and-wait
  TFTP is round-trip bound (`syscall` 71% of profile, `mallocgc` not in the top
  25) — reverted as clarity traded for nothing. TFTP is not the bulk path
  anyway; the initrd comes over HTTP, where `http.ServeFile` already gets
  sendfile and ranges and sustains 2.8–7.1 GB/s. `render` is 3.6 µs per script,
  `parsePacket` 136 ns, and the O(n) group scan is 30 ns/group once
  normalization is fixed — a map index cannot express most-specific-wins
  semantics anyway. Normalizing selector MACs at load would save a further
  ~6 ms across an entire rack boot, which does not justify mutating
  user-visible `Group.Selector` data; left for OQ-7.
- **Robustness — a failed TFTP transfer used to hang the client.** Found while
  benchmarking. On darwin a negotiated `blksize` above `net.inet.udp.maxdgram`
  (9216) makes every DATA write fail with EMSGSIZE; the server accepts and
  OACKs any size up to `maxBlockSize` (65464), logged the failure, and sent
  nothing, so the client waited out its own timeout with no explanation on the
  wire. booty ships darwin binaries. Fixed: a failed transfer now sends an
  ERROR packet on the transfer's own socket (any other port is a different TID
  and the client would discard it, RFC 1350 §4). Capping the accepted `blksize`
  is the belt-and-braces version and is not done — iPXE asks for 1468 anyway,
  which is correct on a real LAN since anything above ~1472 IP-fragments.
- **Robustness — unanswered RRQs pin sockets.** Measured, not inferred: 200
  unanswered RRQs take the process from 3 goroutines to 204, each holding a UDP
  socket for exactly 20 s (`maxRetries+1` × `transferTimeout`). At 1000 spoofed
  RRQ/s that is 20,000 file descriptors. It does not bite a real rack (200
  machines is fine, and concurrent transfers scale to 200 MB/s aggregate), but
  it is the same unauthenticated-UDP exposure as OQ-8 and belongs with that
  decision. `tftp/perf_fanout_test.go` reproduces it; it is env-guarded and
  skips unless `BOOTY_FANOUT` is set.
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

**Answered: a — take the breaking ones now.** Tracked as Phase 3b below.

- **a. Take the breaking ones now (recommended).** v0 semver permits movement,
  but every one of these costs a major-version conversation later. The list is
  bounded and mechanical, and `cmd/booty` is the only in-tree consumer.
- b. Ship v0.1.0 as-is and batch the changes into v0.2.0 — faster to a release,
  but the homelab platform starts importing the shapes we already know are wrong.
- c. Take only the two with behavioural consequences (`httpsrv.New` validating
  `BaseURL`, the `Match` sentinel) and leave the naming to v0.2.0.

#### OQ-8: TFTP amplification and socket exhaustion

**Answered: a — bound in-flight transfers and require the first ACK.** Tracked
as Phase 3b below.

- **a. Bound in-flight transfers and require the first ACK before retransmitting
  (recommended).** A concurrency cap plus not blind-retransmitting to a peer
  that has never answered removes both the 121x amplification and the ~51 pkt/s
  fd exhaustion, without a config surface.
- b. Document the exposure and tell operators to bind `--tftp-addr` to a
  provisioning VLAN — matches the trust model the guide already states, but
  ships a known amplifier on a default of `0.0.0.0:69`.
- c. Add a rate limiter with operator-tunable knobs — most control, most new
  public surface for a v0.1.0.

### Phase 3b: Settle the API and close the TFTP exposure (OQ-7a, OQ-8a)

The pre-release audit surfaced two classes of problem that are cheap now and
expensive after v0.1.0 ships: six API shapes that are wrong, and a TFTP server
that is a usable reflector on its default bind. v0 semver permits moving the
API, but the homelab platform starts importing these packages the moment they
are tagged, and "we already know that shape is wrong" is a bad thing to say to
your only consumer. Both answered **a**; this phase is the work.

These changes are deliberately breaking. `cmd/booty` is the only in-tree
consumer, so the blast radius is one directory plus the `docs/go-ipxe/`
walkthrough, which must stay guide==code.

#### Tasks (Phase 3b)

- [x] **API: one name for one concept.** `httpsrv.Options` and
      `proxydhcp.Config` name the same thing. Standardise on `Config` across
      `httpsrv`, `proxydhcp`, and `tftp`; `render` keeps `Option` because its
      functional options are a genuinely different thing.
- [x] **API: `tftp.New` takes a struct.** It is positional
      (`New(bootDir, logger)`) while its three peers take a struct, so it is the
      one constructor that cannot gain a field without breaking callers — and
      OQ-8 needs it to gain one.
- [x] **API: split `proxydhcp.Serve`'s naked bool.** `Serve(ctx, conn, binl bool)`
      forces every call site to encode a protocol distinction as `true`.
      `ServeDHCP` / `ServeBINL` say which is which at the call site.
- [x] **API: `httpsrv.New` returns an error.** It silently accepts a `BaseURL`
      it cannot use. `cmd/booty` now validates its own `--url` flag, but a
      library that accepts unusable input and fails much later, in someone
      else's rack, is the wrong default for every other consumer.
- [x] **API: drop the `catalog.Source` interface.** One implementation, no
      in-repo use of the abstraction — exactly the speculative extension point
      CLAUDE.md forbids. Keep `DirSource`; the interface can come back when a
      second source exists.
- [x] **API: unexport `PortDHCP`/`PortBoot`/`PortBINL`.** The library's only
      exported constants, for values already expressed as address-string
      defaults in `cmd/booty`. `tftp` keeps port 69 unexported and is fine.
- [x] **TFTP: bound in-flight transfers.** 200 unanswered RRQs take the process
      from 3 goroutines to 204, each pinning a socket for the full 20s retry
      budget — roughly 51 pkt/s to exhaust the fd table. A cap sheds load
      instead of falling over, with no new config surface.
- [x] **TFTP: require the first ACK before retransmitting.** The measured 121x
      amplification comes from blind-retransmitting DATA to a peer that has
      never spoken. A spoofed source address gets one datagram, not the whole
      retry budget.
- [x] Update `docs/go-ipxe/` for every signature change — the walkthrough is the
      code (ADR-0002), so a stale snippet is a broken build for anyone
      following it.

#### Success Criteria (Phase 3b)

All met.

- [x] `just ci` clean; every library package still meets the 60% coverage gate.
      Coverage after: catalog 87.8%, render 90.6%, httpsrv 82.7%, tftp 84.2%
      (up from 75.3%), proxydhcp 73.9%. Note `just ci` needs the pinned
      toolchain on `PATH` — see the `GOTOOLCHAIN` gotcha in `CLAUDE.md`; a shell
      holding a stale `mise` path fails `license-check` with a version mismatch
      that has nothing to do with licenses.
- [x] The amplification factor and the goroutine-per-RRQ growth are both
      measured again and shown to be bounded, by tests that fail against the old
      code. Amplification: one 29-byte RRQ from a silent peer now draws 1 packet
      / 15 bytes (0.5x), against `maxRetries+1` packets and ~121x before;
      reverting the fix fails the test at 2 packets. Concurrency: 768 unanswered
      RRQs produce 257 goroutines against a cap of 256; removing the cap fails
      the test at 769. A third test pins that a peer which _has_ answered still
      gets the full retry budget, so the fix cannot regress into "never
      retransmit", which would break booty on a lossy link.
- [x] `docs/go-ipxe/` compiles as written; no snippet references a removed name.
      Verified by grepping every doc for the removed identifiers — the only hits
      were the two ADRs, handled below.
- [x] `go doc` for each changed package reads correctly. `proxydhcp` gained the
      `# Usage` block it was missing, and `tftp` too.
- [x] ADR-0001 and ADR-0002 both described `catalog.Source` as current fact.
      Each gained a dated note rather than being rewritten: the decisions they
      record are unaffected, since neither depended on the boundary being
      spelled as an interface.

### Phase 4: Cut v0.1.0

Label flow end-to-end, per DESIGN-0001 OQ-1: seed `v0.0.0`, release PR labeled
`minor`.

#### Tasks (Phase 4)

- [x] Seed the baseline tag at the merge commit of the Phase 2/3 PR (OQ-4).
      Tagged `v0.0.0` at `71cbc9b`, the merge of [PR #2](https://github.com/donaldgifford/booty/pull/2).
      Confirmed first that no workflow triggers on tag push: `changelog.yml` and
      `release.yml` both mention `tags:`, but neither under `on.push`.
- [x] Pre-flight at the release commit: `just ci` and `just release-local` both
      clean. It was not quite a formality — see the archive-contents finding
      below. `goreleaser check` validates, and Go 1.26.5 agrees across `go.mod`,
      `mise.toml` and the Dockerfile.
- [x] Open the release PR, label it `minor`, merge it.
      [PR #3](https://github.com/donaldgifford/booty/pull/3): 13 checks pass,
      merged as `89a1e3e`. Fewer checks than PR #2 because the Docs workflow is
      path-filtered and this PR touched no docs.
- [x] Watch `release.yml`: pr-semver-bump tagged `v0.1.0`, goreleaser published
      the release, the docker job pushed GHCR. All green, including the GPG
      signing step on its first real run.
- [x] Verify the release page: 10 assets — 4 archives (linux+darwin ×
      amd64+arm64), 4 `.spdx.json` SBOMs, `checksums.txt`, `checksums.txt.sig`.
- [x] Verify the signature from a clean keyring. Done with an isolated
      `GNUPGHOME` holding nothing but `docs/booty-release.pub.asc`: good
      signature, fingerprint matches the README, and the archive checks out
      against `checksums.txt`. The "not certified with a trusted signature"
      warning is expected — that key is in no web of trust, which is exactly
      why the README says to confirm the fingerprint out of band.
- [x] Verify GHCR: tags `0.1.0`, `0.1`, `v0`, `latest` (plus `dev-ci` from PR
      builds). The `v0.1.0` manifest is an OCI image index carrying
      `linux/amd64`, `linux/arm64`, and two attestation manifests (SBOM +
      provenance). Note the image tags are unprefixed (`0.1.0`) while the git
      tag is `v0.1.0`; `docker pull ghcr.io/donaldgifford/booty:v0.1.0` will
      not resolve.
- [x] Trigger pkg.go.dev indexing. `go list -m github.com/donaldgifford/booty@v0.1.0`
      against `proxy.golang.org` resolves, and the proxy lists both `v0.0.0`
      and `v0.1.0`.
- [x] Confirm the regenerated `CHANGELOG.md` on `main` includes `v0.1.0`. It
      does — but see below, because what it contained was wrong.

##### Two findings from the pre-flight and verification

- **The archives shipped no licence.** `.goreleaser.yml` carried goreleaser's
  `none*` idiom in `archives.files`, which disables its default of bundling
  LICENSE and README — so every tarball would have held nothing but the binary.
  booty is Apache-2.0, and §4(a) requires that recipients of the work receive a
  copy of the licence; someone downloading a tarball is exactly that, a
  recipient who may never see the repo. Fixed in the release PR itself, and
  confirmed in the published artifact: each archive holds `LICENSE`,
  `README.md`, `booty`.
- **The release was attributed to the baseline tag.** This one only became
  visible after publishing. Seeding `v0.0.0` at the merge commit (OQ-4) put an
  ordinary-looking release boundary _after_ all the substantive work, so
  git-cliff filed the entire pre-release audit, the API settling, the TFTP
  hardening and the performance fix under a version nobody can install — and
  both `CHANGELOG.md` and the GitHub release page presented v0.1.0 as shipping
  one packaging fix. OQ-4's choice was reasonable for pr-semver-bump's sake;
  this consequence simply was not anticipated. Fixed with `ignore_tags` in
  `cliff.toml` (`skip_tags` was tried first and is wrong — it discards the
  commits instead of reattributing them), and the published release notes were
  rewritten by hand, since goreleaser's are generated once at publish time. The
  tag itself was left where it is.

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

- [x] macOS/arm64: downloaded `booty_0.1.0_darwin_arm64.tar.gz`, checksum
      verified, extracted, and ran the README quickstart against the example
      catalog fetched from the `v0.1.0` tarball — no working tree involved.
      `validate` reports 4 profiles / 5 groups; `/healthz`, `/boot.ipxe`,
      `/ipxe`, `/machine-config` and `POST /proxmox/answer` all behave. The
      Proxmox endpoint returned 409 for a MAC bound to a Talos profile, with
      `profile talos-control is not a proxmox answer` — correct, and the message
      says why. Shutdown ran the TFTP drain path.
- [x] Linux: same archive flow with `linux_arm64` inside `debian:stable-slim`
      (OQ-5). Runs with no installed dependencies; every endpoint 200s.
- [x] `go install github.com/donaldgifford/booty/cmd/booty@v0.1.0` in a clean
      container — **this failed the stated criterion**, see below. Fixed and
      re-verified against v0.1.1, which reports `booty v0.1.1`.
- [x] Container: `docker run ghcr.io/donaldgifford/booty:0.1.0 serve` with the
      catalog and boot dir mounted and the ports remapped. Serves correctly,
      but **also failed to report its own version** — see below. Fixed and
      re-verified: `booty 0.1.1 (commit 1e29dd27…, built 2026-08-04T14:10:55Z)`.
- [x] Library-consumer smoke in `/tmp/booty-consumer` (OQ-6). A scratch module
      requiring `github.com/donaldgifford/booty v0.1.0` and nothing else:

      ```sh
      cd /tmp/booty-consumer
      go mod init example.com/bootyconsumer
      go get github.com/donaldgifford/booty@v0.1.0
      go build -o consumer . && ./consumer examples/catalog
      ```

      It loads a catalog through `DirSource`, calls `Match` directly, builds a
      `Renderer`, constructs an `httpsrv.Server`, and mounts `Handler()` in its
      own `http.Server` — the whole ADR-0002 promise, using only exported API.
      Output: `match: group=cp-01 profile=talos-control specificity=1`, then
      `GET /ipxe: 200 OK`.
- [x] File the friction points and mark each fix-now or defer. Five found; all
      five are recorded below and four are already fixed and released.
- [x] Update the README. It had no install instructions for a _release_ at all —
      only `mise install` for the dev toolchain, which serves a different reader.
      Added an Install section covering the archive, `go install`, and the
      container, with every command run before it was written down.
- [x] Flip DESIGN-0001 → Implemented and this doc → Completed; record the
      release date in both.

##### Friction found during validation

Phase 5 exists to be the first honest look at the released artifacts, and it
earned its place: two of the five findings were defects in the release itself
that no amount of testing from a working tree could have surfaced.

| # | Finding | Disposition |
| --- | --- | --- |
| 1 | `go install …@v0.1.0` reported `booty dev (commit none, built unknown)` — goreleaser's `-ldflags` never run for that path | **Fixed**, v0.1.1 |
| 2 | `ghcr.io/…/booty:0.1.0` reported `0.0.0-dev` — `release.yml` never passed the build args bake declares | **Fixed**, v0.1.1 |
| 3 | Archives shipped no LICENSE (Apache-2.0 §4(a)) | **Fixed** before v0.1.0 |
| 4 | Release attributed to the `v0.0.0` baseline tag | **Fixed**, PR #4 |
| 5 | Release notes filed every fix under "Others" — goreleaser's `^fix:` patterns do not match scoped commits like `fix(ci):` | **Fixed** for the next release |

Two things are known and deliberately not fixed:

- **A `go install` binary reports no commit or build date.** The module proxy
  serves a source zip, not a checkout, so there is no VCS stamp to embed. The
  version — the part the user actually asked for — is now correct, and the
  README says what the other two fields will and will not tell you.
- **GHCR tags are unprefixed** (`:0.1.1`) while git tags carry the `v`. That is
  docker/metadata-action's `{{version}}` convention and matches common practice;
  changing it now would orphan the tags already published. Documented instead.

#### Success Criteria (Phase 5)

All met.

- [x] The README quickstart works verbatim from released artifacts on macOS,
      Linux, and the container image. Verified on all three; the catalog was
      fetched from the `v0.1.0` tarball so no step touched a working tree.
- [x] `go install …@v0.1.0` and the scratch-module import both work with no
      reference to a repo checkout. The scratch module worked first time; `go
      install` worked but misreported its version, which is finding #1 and is
      fixed in v0.1.1.
- [x] Zero unresolved release-blocking issues; every deferred item is filed with
      a rationale. Five findings, four fixed and released, one fixed for the
      next release; two known limitations documented rather than fixed, each
      with the reason it is not worth fixing.

---

## Remaining owner-gated work

Everything in Phases 2–5 is done and v0.1.1 is released and validated. What is
left needs an account or a policy call that is the owner's to make. None of it
blocks the release; all of it is hardening.

- **Renovate's first run.** The app is installed as of 2026-08-06 but has not
  run yet — the shared preset schedules "before 6am on monday", so nothing is
  wrong, it is simply waiting for its window. `renovate.json5` validates and
  carries the `addLabels: ["patch"]` fix, so the first PR should pass `PR Label
  Check` unaided; that is the thing to confirm when it appears.

  Codecov itself is no longer pending — PR #9's run uploaded, Codecov processed
  the report, and the PR got a comment (see Phase 3). Note the coverage gate
  never depended on Codecov and has been enforced in CI throughout: catalog
  87.8%, render 90.6%, httpsrv 82.7%, tftp 84.2%, proxydhcp 73.9% against a 60%
  floor. Codecov adds reporting and PR comments, not the gate.
- **The `main` ruleset is active but partial.** It blocks deletion and
  force-pushes — the two things that cannot be undone. It does not require
  status checks or a PR before merge, so Phase 3's "required checks enforced, no
  direct pushes" is met in spirit for the irreversible cases and not at all for
  the reversible ones. That is a defensible place to stop for a one-maintainer
  repo; it is recorded here so nobody later mistakes green CI on a PR for
  something that was actually enforced.

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

- [x] `just check` / `just ci` locally before every push (Phases 2–4). Note
      `just ci` needs the pinned toolchain actually on `PATH`; a stale `mise`
      shim fails `license-check` with a Go version mismatch that reads like a
      licensing problem (see the `GOTOOLCHAIN` gotcha in `CLAUDE.md`).
- [x] The full CI matrix green on GitHub runners is itself the Phase 3 test.
      15 checks pass, 2 skip by design (`Build Starlight` until the scaffold
      exists, `label` on `dont-release`).
- [x] `just release-local` snapshot before tagging (Phase 4 pre-flight). This
      is what caught the archives shipping with no LICENSE.
- [x] Consumer-path smoke tests (Phase 5) are the release's acceptance tests:
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
