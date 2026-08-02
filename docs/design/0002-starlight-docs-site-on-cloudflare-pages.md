---
id: DESIGN-0002
title: "Starlight docs site on Cloudflare"
status: Approved
author: Donald Gifford
created: 2026-07-25
---

<!-- markdownlint-disable-file MD025 MD041 -->

# DESIGN 0002: Starlight docs site on Cloudflare

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
  - [Phase 1 — Scaffold the Starlight project](#phase-1--scaffold-the-starlight-project)
    - [Tasks (Phase 1)](#tasks-phase-1)
    - [Success Criteria (Phase 1)](#success-criteria-phase-1)
  - [Phase 2 — Wire the shared docs/ tree + walkthrough fixes](#phase-2--wire-the-shared-docs-tree--walkthrough-fixes)
    - [Tasks (Phase 2)](#tasks-phase-2)
    - [Success Criteria (Phase 2)](#success-criteria-phase-2)
  - [Phase 3 — Markdown audit + lint enforcement](#phase-3--markdown-audit--lint-enforcement)
    - [Tasks (Phase 3)](#tasks-phase-3)
    - [Success Criteria (Phase 3)](#success-criteria-phase-3)
  - [Phase 4 — MkDocs/TechDocs dual output](#phase-4--mkdocstechdocs-dual-output)
    - [Tasks (Phase 4)](#tasks-phase-4)
    - [Success Criteria (Phase 4)](#success-criteria-phase-4)
  - [Phase 5 — Docs CI workflow](#phase-5--docs-ci-workflow)
    - [Tasks (Phase 5)](#tasks-phase-5)
    - [Success Criteria (Phase 5)](#success-criteria-phase-5)
  - [Phase 6 — Cloudflare Workers deploy + PR previews](#phase-6--cloudflare-workers-deploy--pr-previews)
    - [Tasks (Phase 6)](#tasks-phase-6)
    - [Success Criteria (Phase 6)](#success-criteria-phase-6)
  - [Phase 7 — booty.sh + launch polish](#phase-7--bootysh--launch-polish)
    - [Tasks (Phase 7)](#tasks-phase-7)
    - [Success Criteria (Phase 7)](#success-criteria-phase-7)
- [Migration / Rollout Plan](#migration--rollout-plan)
- [Open Questions](#open-questions)
  - [OQ-1: Cloudflare product / deploy mechanism](#oq-1-cloudflare-product--deploy-mechanism)
  - [OQ-2: Domain](#oq-2-domain)
  - [OQ-3: MkDocs/TechDocs dual-output](#oq-3-mkdocstechdocs-dual-output)
  - [OQ-4: Guide chapter titles + ordering mechanism](#oq-4-guide-chapter-titles--ordering-mechanism)
  - [OQ-5: Site content scope](#oq-5-site-content-scope)
- [References](#references)
<!--toc:end-->

## Overview

Build the public booty docs site — the ten-chapter `docs/go-ipxe/` walkthrough
plus the ADR/design/impl doc tree — with
[Starlight](https://starlight.astro.build/) (Astro) in a new `site/` directory,
reading the existing `docs/` tree as its single source, and deploy it as
**Cloudflare Workers static assets** via a `wrangler-action` CI workflow, with
per-PR preview URLs and `https://booty.sh` as the production domain. The same
`docs/` tree also feeds a **MkDocs/TechDocs strict build** so Backstage can
consume it. This replays the proven
[claudelint](https://github.com/donaldgifford/claudelint) pattern (its
DESIGN-0003 / IMPL-0003), reusing its hard-won mechanics — diverging only on the
deploy mechanism (Workers instead of Pages, per OQ-1).

## Goals and Non-Goals

### Goals

- One `docs/` tree, one set of `.md` files: authors (and `docz`) keep writing
  where they write today; the site build adapts, content does not move — and the
  guide files themselves are **not touched at all** (no frontmatter, per OQ-4).
- The go-ipxe walkthrough is the star content: correct chapter ordering, working
  cross-chapter links, and working links to source files.
- Dual output from day one (OQ-3): Starlight renders the site; MkDocs with
  `techdocs-core` builds `--strict` in CI so Backstage TechDocs can consume
  `docs/` the moment an instance exists (booty already ships
  `catalog-info.yaml`).
- Local authoring loop: `just docs-dev` serves the site with live reload.
- CI builds and lints on every PR that touches the docs surface; PRs get a
  Workers preview URL; merges to `main` deploy production.
- Search that works with zero ops (Pagefind, Starlight's default).
- Production at `https://booty.sh` (OQ-2).

### Non-Goals

- Moving or renaming anything under `docs/` (the guide's file names are
  load-bearing: chapters cross-link by filename and the repo history refers to
  them).
- Versioned docs, a blog, or MDX/React components in `.md` files (same non-goals
  as claudelint's DESIGN-0003).
- Landing-page visual polish beyond a serviceable homepage — a hero/CardGrid
  pass is a follow-up, not part of this design.
- Rendering Go API docs on the site — pkg.go.dev owns that
  ([DESIGN-0001](./0001-first-release-of-the-booty-library-and-the-booty-binary.md)).
- Standing up a Backstage instance — this design makes booty's docs
  TechDocs-_ready_ (strict build green, `techdocs-ref` annotated), not
  TechDocs-_served_.

## Background

claudelint shipped this stack: a `site/` Astro + Starlight project, the shared
root `docs/` source, a `docs.yml` CI workflow (markdownlint job + build job),
dual-output MkDocs for Backstage TechDocs, Pagefind search, and a custom-domain
cutover. Four mechanics from that work carry over directly:

1. **Content wiring**: Starlight's bundled `docsLoader()` hardcodes
   `src/content/docs/` and its path math drives sidebar autogeneration. A
   `glob({ base: '../docs' })` loader serves pages but silently breaks the
   sidebar. The working shape is a **symlink**
   `site/src/content/docs -> ../../../docs` plus `docsLoader()`, and a
   `docsSchema` extended with docz's frontmatter fields (`id`, `status`,
   `author`, `created`) as optional.
2. **Link rewriting**: a small remark plugin (`remark-md-link-rewriter.mjs`)
   rewrites relative `.md` links like `[ADR-0002](../adr/0002-….md)` into
   absolute site routes. Off-the-shelf plugins assume content lives under
   `src/content/` and break on the shared-source layout.
3. **Toolchain**: Node pinned in `mise.toml` (with a `# renovate:` annotation),
   `jdx/mise-action` in CI as the single source of Node truth, npm cache keyed
   on `site/package-lock.json`.
4. **Dual output**: a root `mkdocs.yml` with the `techdocs-core` plugin reads
   the same `docs/` tree; `mkdocs build --strict` in CI plus lint-time regex
   guards against MkDocs-only syntax keep the one source renderable by both
   engines.

One mechanic deliberately **diverges**: claudelint deploys via the Cloudflare
_Pages_ Git integration (dashboard-configured builds). booty deploys as
Cloudflare **Workers static assets** driven from CI with `wrangler-action`
(OQ-1) — Workers is Cloudflare's stated forward path for static hosting, and
CI-driven deploys keep the whole pipeline in the repo (at the cost of API token
secrets and hand-rolled PR preview comments).

booty also adds three content problems claudelint did not have, all in the
walkthrough:

- **No frontmatter**: guide chapters open with a bare `# Chapter N:` H1.
  Starlight's `docsSchema` requires a `title` in frontmatter. Per OQ-4 the
  chapters stay frontmatter-free: titles are derived from each file's first H1
  by a build shim. (This also keeps the files MkDocs-native — MkDocs derives
  page titles from H1s by design.)
- **Filename order ≠ chapter order**: the guide kept draft-era filenames, so
  lexicographic sidebar autogeneration misorders it — `06-http-server-stdlib.md`
  is Chapter 7 but sorts before `06-render-pipeline.md` (Chapter 6), and
  `08-debugging-field-guide.md` (Chapter 10) sorts before `09-qemu-e2e.md`
  (Chapter 9). Per OQ-4 the Walkthrough sidebar is a hand-ordered explicit list
  in `astro.config.mjs` (and the MkDocs nav orders the same way).
- **Out-of-tree source links**: chapters link to source files
  (`[httpsrv/httpsrv.go](../../httpsrv/httpsrv.go)`) and example catalogs. These
  resolve on GitHub but leave the content root, so on the site they 404. The
  link-rewriter needs a second rule: links escaping `docs/` are rewritten to
  `https://github.com/donaldgifford/booty/blob/main/<path>`.

## Detailed Design

```text
             ┌────────────────────────────────┐
             │        docs/ (one tree)        │
             │ go-ipxe/ adr/ design/ impl/ …  │
             └───────────────┬────────────────┘
                             │  symlink: site/src/content/docs → ../../../docs
                             │  (root mkdocs.yml reads the same tree for TechDocs)
                             ▼
             ┌────────────────────────────────┐
             │  site/  (Astro + Starlight)    │
             │  astro.config.mjs              │
             │  src/content.config.ts         │
             │  src/plugins/remark-md-link-   │
             │    rewriter.mjs (+ GitHub      │
             │    fallback + H1-title shim)   │
             └───────────────┬────────────────┘
                CI: docs.yml │ lint-md + mkdocs build --strict
                             │ + astro check/build; deploy via
                             │ cloudflare/wrangler-action
                             ▼
             ┌────────────────────────────────┐
             │  Cloudflare Workers (static    │
             │  assets via wrangler-action)   │
             │  main → https://booty.sh       │
             │  PRs  → version preview URLs   │
             └────────────────────────────────┘
```

Key choices (mirroring claudelint unless noted):

- **`site/` layout**: `package.json` (scripts: `dev`, `build`, `preview`,
  `check`), `astro.config.mjs` (with `site: 'https://booty.sh'`),
  `src/content.config.ts`, `src/plugins/remark-md-link-rewriter.mjs`,
  `wrangler.jsonc` (assets-only Worker: `name`, `compatibility_date`,
  `assets.directory: "./dist"` — no Worker script), committed
  `package-lock.json`; `node_modules/`, `dist/`, `.astro/` gitignored.
- **Titles without frontmatter (OQ-4)**: `docsSchema` extended so `title` is
  optional with a placeholder default; the displayed title is derived from each
  file's first H1 at build time (Starlight route-data middleware or a thin
  loader shim), and the duplicate H1 is stripped from the rendered body so pages
  don't show two titles. Contingency: if this proves brittle against Starlight
  internals, fall back to minimal frontmatter (the rejected OQ-4a) — the design
  otherwise doesn't change.
- **Sidebar**: the Walkthrough is a hand-ordered explicit list (11 entries, the
  index plus Ch1→Ch10) in `astro.config.mjs`; the docz groups (ADRs, Design,
  Implementation, …) use `autogenerate` so new docs appear without config edits.
- **Homepage**: a landing page derived from the README pitch (what booty is, the
  boot-chain diagram, install pointer, link into Chapter 1). claudelint started
  the same way and polished later.
- **editLink**: `https://github.com/donaldgifford/booty/edit/main/docs/` so
  every page has "Edit on GitHub".
- **Search**: Pagefind (bundled). Verified in claudelint with no extra build
  step.
- **MkDocs/TechDocs (OQ-3)**: root `mkdocs.yml` with `techdocs-core`,
  `docs_dir: docs`, nav maintained by `docz wiki update`;
  `mkdocs build --strict` runs in CI as the second-renderer proof;
  `catalog-info.yaml` gains `backstage.io/techdocs-ref: dir:.`.
- **Deploy (OQ-1)**: `cloudflare/wrangler-action` in the docs workflow —
  `wrangler deploy` on `main`, `wrangler versions upload` on PRs (the uploaded
  version gets a stable preview URL, posted as a PR comment). Rollback is
  `wrangler rollback` to any prior version, independent of git.
- **Recipes**: a new `docs.just` imported by the justfile (same pattern as
  `docker.just`): `docs-install`, `docs-dev`, `docs-build`, `docs-check`,
  `docs-mkdocs-check`, `lint-md`.
- **Lint**: `just lint-md` = `markdownlint-cli2 'docs/**/*.md'` plus
  claudelint's regex greps blocking MkDocs-only syntax (`!!!`, `???`,
  `pymdownx`) — with `mkdocs build --strict` as the belt to that suspenders.

## API / Interface Changes

No Go surface changes. Repo-level changes:

- New `site/` directory (including `wrangler.jsonc`), root `mkdocs.yml`, and
  `docs.just`; `justfile` gains the import.
- `mise.toml` gains pinned Node and the Python-side MkDocs toolchain (via `uv`),
  each with `# renovate:` annotations.
- New `.github/workflows/docs.yml` (lint + builds + deploy);
  `.github/labeler.yml` gains a `site/**` → `documentation` mapping.
- New repo secrets: `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`.
- `catalog-info.yaml` gains the `backstage.io/techdocs-ref: dir:.` annotation.
- Guide chapters are **unchanged** (OQ-4: no frontmatter).
- README gains a Documentation section linking `https://booty.sh` once live.

## Data Model

Not applicable — static site generation only.

## Testing Strategy

- **Per-PR CI**: `lint-md`, `mkdocs build --strict`, and `astro check` +
  `astro build` on any PR touching `docs/**`, `site/**`, `mkdocs.yml`,
  `mise.toml`, the justfiles, or the workflow; the built `site/dist` is uploaded
  as an artifact.
- **Two-renderer proof**: every PR that passes has rendered the same `docs/`
  tree through both Starlight and MkDocs strict mode — divergence-prone syntax
  fails fast.
- **Link integrity**: the build fails on broken internal links (the rewriter
  errors on unresolvable targets); out-of-tree GitHub links are spot-checked
  after the first deploy.
- **Preview deploys**: every PR gets a Workers preview URL via
  `wrangler versions upload` — reviewing rendered output is part of PR review
  for docs changes.
- **Search smoke**: Pagefind queries ("proxyDHCP", "answer.toml", "TFTP")
  against the deployed preview return the expected chapters.
- **Rollback drill**: once two production versions exist, `wrangler rollback` is
  exercised once so the escape hatch is proven, not assumed.

## Implementation Phases

Each phase builds on the previous one. A phase is complete when all its tasks
are checked off and its success criteria are met.

### Phase 1 — Scaffold the Starlight project

A working default Starlight site in `site/`, runnable locally. No booty content
yet; this proves the toolchain.

#### Tasks (Phase 1)

- [ ] Pin Node LTS in `mise.toml` with a `# renovate:` annotation (match
      claudelint's current LTS pin).
- [ ] `npm create astro@latest site -- --template starlight` (no install), then
      trim to the minimal shell: `astro.config.mjs`, `package.json`, `src/`,
      `public/favicon.svg`, `tsconfig.json`.
- [ ] Add `.gitignore` entries: `site/node_modules/`, `site/dist/`,
      `site/.astro/`.
- [ ] `cd site && npm install`; commit `package-lock.json` (Renovate's npm
      manager picks it up with no extra config).
- [ ] npm scripts: `dev`, `build`, `preview`, `check` (add `@astrojs/check` +
      `typescript` devDeps).
- [ ] Create `docs.just` (`docs-install`, `docs-dev`, `docs-build`,
      `docs-check`) and `import 'docs.just'` from the justfile.
- [ ] `just docs-dev` → default Starlight site at `http://localhost:4321`.

#### Success Criteria (Phase 1)

- `just docs-dev` serves the default site with no errors; `just docs-build`
  emits `site/dist/`; `just docs-check` passes.
- No Node artifacts (`node_modules/`, `dist/`, `.astro/`) are committed.
- `just --list` shows the docs recipe group.

### Phase 2 — Wire the shared `docs/` tree + walkthrough fixes

Point Starlight at the real content and solve the three booty-specific problems
(titles, ordering, out-of-tree links) — without touching the guide files (OQ-4).

#### Tasks (Phase 2)

- [ ] Create the symlink `site/src/content/docs -> ../../../docs` and use the
      bundled `docsLoader()` in `src/content.config.ts` (claudelint's approach —
      do **not** use `glob({ base: '../docs' })`; it breaks sidebar
      autogeneration).
- [ ] Extend `docsSchema`: docz fields (`id`, `status`, `author`, `created`)
      optional, and `title` optional with a placeholder default (guide files
      carry no frontmatter).
- [ ] Build the H1-title shim (OQ-4): derive each page's displayed title from
      the file's first H1 (Starlight route-data middleware or loader shim) and
      strip that H1 from the rendered body so no page shows a double title. If
      this fights Starlight internals, stop and fall back to minimal frontmatter
      (OQ-4a contingency).
- [ ] Port `remark-md-link-rewriter.mjs` from claudelint; extend it: links
      resolving **outside** the content root (e.g. `../../httpsrv/httpsrv.go`,
      `../../examples/catalog`) rewrite to
      `https://github.com/donaldgifford/booty/blob/main/<repo-path>`.
- [ ] Sidebar config: hand-ordered explicit Walkthrough list (the index plus
      Ch1→Ch10, correcting the `06-*`/`08-*`/`09-*` filename-order traps), plus
      autogenerated groups for each docz doc type.
- [ ] Set `site: 'https://booty.sh'` in `astro.config.mjs` (OQ-2) and `editLink`
      base → `.../edit/main/docs/`.
- [ ] Landing page from the README pitch (boot-chain diagram, install pointer,
      "start at Chapter 1").
- [ ] Confirm docz `<!--toc:start-->` blocks render invisibly (they are HTML
      comments, but verify no artifact).
- [ ] Spot-check every chapter plus one doc per docz type in the local dev
      server; fix render issues.

#### Success Criteria (Phase 2)

- Sidebar shows the Walkthrough in true chapter order (1→10) and every docz
  doc-type group with all docs listed.
- Every page's title comes from its H1 with no double-H1 rendering; the guide
  files are byte-identical to before the phase.
- All cross-doc links work on the rendered site; source-file links resolve to
  the correct file on GitHub `main`.
- The homepage renders the landing content (not a 404, not a stub).

### Phase 3 — Markdown audit + lint enforcement

Keep `docs/` renderable by CommonMark+GFM engines (Starlight and MkDocs), and
block regressions at lint time.

#### Tasks (Phase 3)

- [ ] Audit: `grep -RE '^\s*!!!|^\s*\?\?\?|pymdownx' docs/` — convert any hits
      to GFM (`> [!NOTE]`) or plain Markdown (the guide was written
      CommonMark-first, so expect zero, but prove it).
- [ ] Verify the guide's heavy GFM surface renders in Starlight: tables,
      box-drawing/ASCII diagrams in fenced blocks, `> [!NOTE]` alerts, long
      `text`-language code fences.
- [ ] Add the `lint-md` recipe to `docs.just` (markdownlint-cli2 + the regex
      greps); keep it standalone like claudelint (the docs workflow runs it).
- [ ] `markdownlint-cli2` pass over `docs/**/*.md`; fix violations in source
      (the known backlog: MD040 missing fence languages and MD060 table spacing
      in several guide chapters).
- [ ] Note Mermaid status: no Mermaid blocks exist in booty docs today; if one
      lands later, wire the Starlight plugin then (claudelint deferred the same
      way).

#### Success Criteria (Phase 3)

- The audit greps return zero hits; `just lint-md` exits 0 over the whole
  `docs/` tree.
- Every chapter renders in the dev server without visible syntax artifacts
  (diagrams intact, alerts styled, tables aligned).

### Phase 4 — MkDocs/TechDocs dual output

The same `docs/` tree builds clean under MkDocs with `techdocs-core` (OQ-3),
making booty Backstage-ready.

#### Tasks (Phase 4)

- [ ] Pin the Python-side toolchain in `mise.toml` (`uv`, with a `# renovate:`
      annotation); the MkDocs invocation runs via
      `uvx --with mkdocs-techdocs-core mkdocs …` so no venv is committed.
- [ ] `docz wiki init` → root `mkdocs.yml` scaffold; confirm `docs_dir: docs`,
      the `techdocs-core` plugin, and site metadata.
- [ ] Nav: `docz wiki update` maintains the docz sections; add the guide section
      hand-ordered Ch1→Ch10 (MkDocs takes page titles from H1s natively — OQ-4's
      no-frontmatter decision needs nothing extra here).
- [ ] Add the `docs-mkdocs-check` recipe to `docs.just`:
      `mkdocs build --strict`.
- [ ] Add `backstage.io/techdocs-ref: dir:.` to `catalog-info.yaml`.
- [ ] Run the strict build; fix anything it flags that Starlight tolerated.

#### Success Criteria (Phase 4)

- `just docs-mkdocs-check` exits 0 (strict mode — warnings are failures).
- The MkDocs nav shows the walkthrough in chapter order and every docz group.
- `catalog-info.yaml` carries the `techdocs-ref` annotation.

### Phase 5 — Docs CI workflow

Prove lint + both builds on every relevant PR, before any deploy is wired.

#### Tasks (Phase 5)

- [ ] Create `.github/workflows/docs.yml` with three verification jobs:
      `lint-md` (mise-action + `just lint-md`), `mkdocs` (mise-action +
      `just docs-mkdocs-check`), and `build` (mise-action for Node, npm cache
      keyed on `site/package-lock.json`, `npm ci`, `npm run check`,
      `npm run build`, upload `site/dist` artifact with short retention).
- [ ] `paths` filters on both triggers: `docs/**`, `site/**`, `mkdocs.yml`,
      `catalog-info.yaml`, the markdownlint config, `justfile`, `docs.just`,
      `mise.toml`, `.github/workflows/docs.yml`.
- [ ] Add `site/**` → `documentation` to `.github/labeler.yml`.
- [ ] Verify: a docs-touching PR runs all three jobs green; a Go-only PR skips
      the workflow (paths filter proven both ways).
- [ ] After the first clean PR cycle, add the jobs to the required checks on
      `main`.

#### Success Criteria (Phase 5)

- Docs PRs run the lint, mkdocs, and build jobs; code-only PRs skip them.
- All three jobs are required checks on `main`.

### Phase 6 — Cloudflare Workers deploy + PR previews

Stand up hosting as Workers static assets, deployed from CI (OQ-1). No
dashboard-configured builds — the pipeline lives in the repo.

#### Tasks (Phase 6)

- [ ] Add `site/wrangler.jsonc`: assets-only Worker (`name: "booty-docs"`,
      `compatibility_date`, `assets: { directory: "./dist" }` — no Worker
      script).
- [ ] **(Donald)** Create a Cloudflare API token scoped to Workers Scripts: Edit
      and set the `CLOUDFLARE_API_TOKEN` + `CLOUDFLARE_ACCOUNT_ID` repo secrets.
- [ ] Add the deploy jobs to `docs.yml` using `cloudflare/wrangler-action`
      (working directory `site/`, gated on the build job): on push to `main` →
      `wrangler deploy`; on PRs → `wrangler versions upload`.
- [ ] Post the PR preview URL as a sticky PR comment from the
      `wrangler versions upload` output; enable preview URLs on the Worker.
- [ ] First production deploy; verify the `workers.dev` URL serves the site.
- [ ] Open a docs-touching test PR; verify the preview URL comment appears and
      the preview serves that PR's content.
- [ ] Verify Pagefind on the deployed preview (`/pagefind/pagefind.js` serves
      200; queries return chapters).
- [ ] Rollback drill: after a second production deploy exists,
      `wrangler rollback` to the prior version and back.

#### Success Criteria (Phase 6)

- The `workers.dev` URL serves the current `main` build, deployed by CI (no
  dashboard build configuration anywhere).
- Every docs PR gets a working preview URL comment; production redeploys on
  merge.
- Search returns correct results on the deployed site; rollback proven.

### Phase 7 — booty.sh + launch polish

Attach the production domain and make the site the documented front door.

#### Tasks (Phase 7)

- [ ] **(Donald)** Register `booty.sh` — Cloudflare Registrar if it supports
      `.sh`, otherwise an external registrar with DNS delegated to a Cloudflare
      zone (a Workers custom domain requires the zone to be on Cloudflare).
- [ ] Attach `booty.sh` as a custom domain on the Worker; add a `www.booty.sh` →
      `booty.sh` redirect rule.
- [ ] Verify HTTPS (auto-provisioned cert), HTTP→HTTPS redirect, and that
      canonical URLs/sitemap use `https://booty.sh` (the Phase 2 `site`
      setting).
- [ ] README: add a Documentation section linking `https://booty.sh` (the
      in-repo guide links keep working — the site augments, not replaces).
- [ ] CONTRIBUTING: document the docs pipeline (source = `docs/`,
      `just docs-dev` for preview, no-frontmatter rule for guide chapters, the
      lint-md + mkdocs-strict gates).
- [ ] CLAUDE.md: add `site/`, `mkdocs.yml`, the docs recipes, and a pointer to
      this design.
- [ ] Update this doc's checkboxes as phases complete; flip status Approved →
      Implemented per the docz lifecycle.

#### Success Criteria (Phase 7)

- `https://booty.sh` serves the site with a valid certificate.
- README, CONTRIBUTING, and CLAUDE.md document the site and its workflow.
- This design's status is Implemented with all phase checkboxes resolved.

## Migration / Rollout Plan

Additive throughout — `docs/` content is never moved (and per OQ-4 the guide
files are never edited), so GitHub rendering and the guide's in-repo links keep
working at every phase. Rollback at any point before launch is deleting `site/`,
`mkdocs.yml`, and the workflow; the docs tree is untouched. Once live, a bad
deploy rolls back via `wrangler rollback` to any prior Worker version (instant,
independent of git). The domain attaches last, so nothing user-facing exists
until Phase 7 flips it on.

## Open Questions

All resolved 2026-07-29 — each question below records its **Decision** line.
Format: **a** was the recommendation; later letters were alternatives.

### OQ-1: Cloudflare product / deploy mechanism

- a. Cloudflare Pages with the Git integration — exactly what claudelint runs in
  production: zero deploy code in the repo, free per-PR preview URLs with status
  checks, per-deploy rollback.
- **b. Cloudflare Workers static assets deployed by a `wrangler-action`
  workflow.** Cloudflare's stated forward path (Pages is de-emphasized), and
  full CI control over when deploys happen, at the cost of API-token secrets,
  deploy workflow code, and hand-rolled PR preview comments.
- c. Pages now with a planned migration to Workers if Cloudflare pushes a Pages
  deprecation.

**Decision (2026-07-29): b** — Workers static assets via `wrangler-action`; it
is Cloudflare's preferred/forward option, and the deploy pipeline stays in the
repo. This is the one deliberate divergence from the claudelint pattern.

### OQ-2: Domain

- a. Launch on the default Cloudflare subdomain; decide a custom domain
  separately.
- b. Register a dedicated domain now and cut over in the final phase.
- c. Serve it from a subdomain of a domain you already own.

**Decision (2026-07-29): Other — `booty.sh`.** Registered and attached in Phase
7; `astro.config.mjs` sets `site: 'https://booty.sh'` from Phase 2 so canonical
URLs are right from the first deploy.

### OQ-3: MkDocs/TechDocs dual-output

booty ships `catalog-info.yaml` (Backstage) but had no `mkdocs.yml` or TechDocs
pipeline — claudelint already had one before its Starlight work, so dual-output
was preservation there, not an addition.

- a. Starlight-only now; keep `docs/` CommonMark-clean so TechDocs can be added
  later.
- **b. Full dual-output from day one like claudelint** — `docz wiki` init, root
  `mkdocs.yml` with `techdocs-core`, a strict-build recipe and CI job.

**Decision (2026-07-29): b** — dual output from day one (Phase 4). The strict
MkDocs build doubles as a second-renderer regression net for the shared tree.

### OQ-4: Guide chapter titles + ordering mechanism

Starlight requires a frontmatter `title`; the walkthrough's filename order
disagrees with its chapter order in two spots (`06-http-server-stdlib.md` is
Chapter 7; `08-debugging-field-guide.md` is Chapter 10).

- a. Add minimal frontmatter to the eleven guide files (`title` +
  `sidebar.order`), keeping the H1s — one mechanical PR, explicit in source, but
  touches every guide file.
- **b. No frontmatter: derive titles from each file's H1 via a build shim and
  hand-order the Walkthrough sidebar as an explicit list in
  `astro.config.mjs`.** Zero guide churn and the files stay MkDocs-native; the
  cost is two custom mechanisms to maintain and a nav list that must be edited
  when chapters change.
- c. Rename the guide files to match chapter numbers — breaks in-repo links,
  external links, and the guide's own cross-references.

**Decision (2026-07-29): b** — guide files stay untouched; H1-derived titles
plus an explicit sidebar list. Contingency recorded in Phase 2: if the title
shim fights Starlight internals, fall back to option a.

### OQ-5: Site content scope

- **a. Everything under `docs/`: walkthrough + all docz doc-type groups.**
  Matches claudelint; the design/impl docs are honest engineering-log content
  for a public homelab project, and excluding them means a second content-filter
  mechanism to maintain.
- b. Walkthrough + ADRs only — the "polished" subset; design/impl/plan/
  investigation docs stay GitHub-only. Requires an exclude filter in the content
  collection.

**Decision (2026-07-29): a** — publish everything under `docs/`.

## References

- [claudelint DESIGN-0003 — dual-output docs site](https://github.com/donaldgifford/claudelint/blob/main/docs/design/0003-dual-output-docs-site-shared-source-mkdocs-for-techdocs.md)
  — the pattern this design replays
- [claudelint IMPL-0003 — Starlight site implementation](https://github.com/donaldgifford/claudelint/blob/main/docs/impl/0003-phase-3-dual-output-docs-site-with-starlight.md)
  — phase-by-phase prior art, including the symlink/docsLoader finding reused
  here
- [claudelint `site/`](https://github.com/donaldgifford/claudelint/tree/main/site)
  — `astro.config.mjs`, `content.config.ts`, `remark-md-link-rewriter.mjs`
- [claudelint `docs.yml` workflow](https://github.com/donaldgifford/claudelint/blob/main/.github/workflows/docs.yml)
- [Starlight docs](https://starlight.astro.build/) — sidebar config, route data
  middleware, editLink
- [Astro content collections](https://docs.astro.build/en/guides/content-collections/)
- [Pagefind](https://pagefind.app/) — bundled search
- [Cloudflare Workers static assets](https://developers.cloudflare.com/workers/static-assets/)
  — the deploy target
- [wrangler-action](https://github.com/cloudflare/wrangler-action) — CI deploys
  and version uploads
- [Backstage TechDocs](https://backstage.io/docs/features/techdocs/) —
  `techdocs-core`, `techdocs-ref` annotation
- [DESIGN-0001 — First release of the booty library and the booty binary](./0001-first-release-of-the-booty-library-and-the-booty-binary.md)
- `docs/go-ipxe/00-index.md` — the walkthrough this site exists to publish
