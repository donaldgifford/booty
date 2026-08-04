# CLAUDE.md

Per-repo orientation for `donaldgifford/booty`. This file is a
Go-shaped overlay on top of the universal homelab `CLAUDE.md` (see
[homelab/docs](https://github.com/donaldgifford/docs)); the universals
apply here too — only Go-specific guidance is captured below.

## What this is

`booty` is a Go **library** for building network-boot services, plus its
reference consumer, the `booty` binary (ADR-0002):

- Public library packages at the top level (`catalog/`, `render/`,
  `httpsrv/`, `tftp/`, `proxydhcp/`); external consumers (the homelab
  platform) import these directly. v0 semver — the API may move.
- `cmd/booty/` is the reference consumer: the standalone binary built in
  the `docs/go-ipxe/` walkthrough. It uses only the public API.
- Built into a distroless container via `Dockerfile` + `docker-bake.hcl`
  (buildx bake); released as multi-arch (linux+darwin × amd64+arm64)
  archives via `goreleaser`.
- Lives on GitHub (`github.com/donaldgifford/booty`); CI is GitHub
  Actions under `.github/workflows/`.

## Layout

```text
cmd/booty/    # reference consumer — keep thin, parse flags + call the library
catalog/                # identity→group→profile model, matcher, HCL DirSource
render/                 # text/template pipeline (iPXE, Talos, cloud-init, Proxmox)
httpsrv/                # stdlib HTTP serving core (boot + config endpoints)
tftp/                   # read-only TFTP server (RFC 1350 + options)
proxydhcp/              # PXE proxyDHCP + BINL (port 4011) responder
test/e2e/               # build-tagged e2e harness (protocol tier + QEMU tier)
examples/catalog/       # example HCL catalog (used by the guide and e2e tests)
docs/go-ipxe/           # the walkthrough that builds the packages (guide == code)
Dockerfile              # multi-stage distroless build, cached layers
docker-bake.hcl         # buildx bake targets (local / ci / multi-arch release)
docker.just             # docker recipes, imported by the justfile
.goreleaser.yml         # release config (multi-arch archives + checksums)
mise.toml               # pinned go + golangci-lint + universal tools
justfile                # `just` task runner — `just` for the menu
.github/workflows/      # CI (GitHub Actions)
```

## Workflows

### Build + run

- `just build` — binary at `build/bin/booty` (version/commit via ldflags)
- `just run` — build then run the binary
- `just test` — all tests with the race detector
- `just test-e2e` — e2e harness; QEMU tier skips unless `BOOTY_E2E_*` set
- `just coverage-gate` — fails if any library package drops below 60%

### Lint + format

- `just lint` — `golangci-lint run` (config in `.golangci.yml`)
- `just fmt` — gofmt + goimports
- `just check` — pre-commit gate (lint + test); `just ci` — full gate

### Release

- Releases are **label-driven**: merge a PR to `main` with a `major`/
  `minor`/`patch` label and `.github/workflows/release.yml` bumps the
  version (pr-semver-bump), tags, runs `goreleaser release --clean`
  (archives + SBOMs, GPG-signed checksums), then bakes + pushes the
  multi-arch image to GHCR. A `dont-release` label skips it.
- `just release v0.1.0` creates and pushes a tag manually, but no
  workflow fires on tag push — the label flow above is the release path.
- Version metadata is injected into the binary via `-ldflags`:
  `main.version`, `main.commit`, `main.date`. `booty version` prints it.

### Container build

- `just docker-build` — local single-arch image via `docker buildx bake`
  (feeds `VERSION`/`COMMIT`/`DATE` from git into the build args).
- `docker-bake.hcl` targets: `default` (local), `ci` (linux/amd64,
  fast PR validation), `release` (amd64+arm64 push with SBOM +
  provenance; tags come from docker/metadata-action in CI).

The Dockerfile uses BuildKit `--mount=type=cache` for `/go/pkg/mod` and
`/root/.cache/go-build` — first build is cold, subsequent builds reuse
the cache layers.

## Go-specific conventions

- **`go.mod` go directive matches `mise.toml`** (currently `go 1.26.4`).
  Bump both together — Renovate's Go updater handles `go.mod`; bump
  `mise.toml` in the same commit.
- **No `vendor/`**. Modules are resolved at build time; the Docker cache
  mount handles offline-ish builds.
- **Library packages are public; `internal/` for genuinely private helpers
  only** (ADR-0002). The top-level packages are consumed externally, so
  exported API changes are API changes — no speculative extension points;
  a real consumer's need drives every addition. Anything only the library
  itself needs still goes under `internal/`.
- **`slog` for structured logs**, not `log` or third-party loggers. Set
  the default handler in `main()` so library code doesn't have to
  thread loggers.
- **No `init()` for behavior**. `init()` runs at import time — it breaks
  test isolation and surprises future-you. Wire dependencies in `main()`.
- **Tests live next to the code** (`foo_test.go` alongside `foo.go`).
  Integration tests that need external services go under a `// +build
  integration` (or `//go:build integration`) tag and run via
  `go test -tags=integration ./...`.
- **Errors wrap with `%w`**: `fmt.Errorf("loading config: %w", err)`.
  Top of the call stack handles via `errors.Is` / `errors.As`.

## CI matrix

- `.github/workflows/ci.yml` runs on every push/PR — golangci-lint,
  `just test-coverage` + `just coverage-gate` + the e2e protocol tier
  (Codecov upload), govulncheck + Trivy, a goreleaser snapshot with
  SBOM scan, and a bake of the CI image (pushed as `:dev-ci` on PRs).
- `.github/workflows/release.yml` runs on merge to `main` — label-driven
  version bump + tag, goreleaser release, multi-arch GHCR image push.
- Changelog workflows regenerate `CHANGELOG.md` via git-cliff from
  conventional commits.

## Gotchas

- **`go mod tidy` on first scaffold**: the post-create hook runs it
  automatically. If you skip hooks (`--no-hooks`), run it manually
  before the first `just build` or imports will be unresolved.
- **`goreleaser` v2 config**: the v1 → v2 migration moved
  `archives[].format` to `archives[].formats` (slice). If you copy a
  pre-v2 `.goreleaser.yml` from elsewhere, validate with
  `goreleaser check`.
- **Distroless `nonroot` UID is 65532**. If the binary needs to write
  state, mount a writable volume — the rootfs is read-only. Binding the
  privileged UDP ports (69, 67, 4011) in a container needs
  `CAP_NET_BIND_SERVICE` or remapped `--tftp-addr`/`--proxydhcp-addr`.
- **`GOTOOLCHAIN=auto` breaks `go-licenses`**. If the `go` on PATH is
  older than `go.mod`'s directive, Go silently switches to a downloaded
  toolchain and `go list` then reports stdlib packages under
  `golang.org/toolchain@…` instead of GOROOT. go-licenses v1.6.0 reads
  that as "does not have module info" and dies — so `just ci` fails
  locally while CI passes, because `actions/setup-go` sets
  `GOTOOLCHAIN=local`. The `license-check`/`license-report` recipes pin
  it too. The real trigger is `mise.toml` and `go.mod` drifting apart;
  run `mise install` after bumping either.
- **Markdown in `docs/` must stay CommonMark + GFM.** `just lint-md`
  (the `Lint Markdown` CI job) runs markdownlint-cli2 and rejects
  MkDocs-only admonitions (`!!! note`, `??? note`), which render as
  literal text everywhere but MkDocs — `docs/` is the shared source for
  both the Starlight site and a future MkDocs build (DESIGN-0002).
  Prettier and docz fight over `docs/*/README.md`, so those are in
  `.prettierignore`; docz owns them.

## Renovate

- `go.mod` updates are PR'd by Renovate's Go module manager.
- Container base images in `Dockerfile` are PR'd by the Docker manager.
- `mise.toml` versions are handled by a custom regex manager configured
  upstream in `donaldgifford/renovate-config`.
