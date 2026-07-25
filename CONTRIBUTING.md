# Contributing to booty

Thank you for your interest in contributing. This document covers how to report
issues, propose changes, and submit pull requests.

## Quick start

```bash
mise install                      # toolchain pinned in mise.toml
just check                        # pre-commit gate: lint + test
```

`just --list` enumerates every recipe.

## Reporting Issues

Use [GitHub Issues](https://github.com/donaldgifford/booty/issues) for:

- **Bug reports** — include the `booty version` output, the command you ran
  (with flags), and the relevant log lines (`--log-format json` makes them easy
  to paste). For boot failures, the field guide in
  [docs/go-ipxe/08-debugging-field-guide.md](docs/go-ipxe/08-debugging-field-guide.md)
  shows which endpoint/log to look at — including that output narrows things
  fast.
- **Feature requests** — describe the problem you are trying to solve, not just
  the solution you have in mind. Note that the top-level packages are a public
  library API
  ([ADR-0002](docs/adr/0002-booty-is-a-library-with-cmdbooty-as-reference-consumer.md)):
  a real consumer's need drives every addition, so say who needs it and for
  what.
- **Template changes** — open an issue before changing the embedded render
  templates (`render/templates/`), as their output feeds real machines.
  Operators can already override them at runtime via `--templates-dir` without a
  code change.
- For larger proposals, open an RFC document via `docz create rfc` and link it
  from the issue. Architecture decisions are recorded as ADRs in
  [docs/adr](docs/adr/).

## Development Setup

### Prerequisites

- [mise](https://mise.jdx.dev/) — installs the pinned toolchain (Go,
  golangci-lint, `just`, `docz`, and the rest of `mise.toml`)

```bash
git clone https://github.com/donaldgifford/booty.git
cd booty
mise install
just test    # unit tests with the race detector
just lint    # golangci-lint
```

### End-to-end tests

```bash
just test-e2e
```

The e2e harness (`test/e2e/`, build tag `e2e`) has two tiers: the **protocol
tier** boots booty's real TFTP/HTTP servers in-process and runs anywhere; the
**QEMU tier** boots a real UEFI VM and skips itself unless `BOOTY_E2E_QEMU`,
`BOOTY_E2E_OVMF_CODE`, and `BOOTY_E2E_IPXE` are set (see `test/e2e/e2e_test.go`
for what each points at).

## Making Changes

### 1. Create a branch

Branch names follow the pattern `<type>/<short-description>`:

```bash
git checkout -b feat/proxmox-answer-kind
git checkout -b fix/tftp-block-wraparound
git checkout -b docs/chapter-6-render-pipeline
```

Types: `feat`, `fix`, `docs`, `chore`, `refactor`

### 2. Make your changes

- Keep changes focused. One logical change per PR.
- Add or update tests for any code you change. Tests live next to the code
  (`foo_test.go` alongside `foo.go`); rendered outputs are pinned with golden
  files (`go test ./render -update` refreshes them — review the diff).
- **The guide is the code.** The walkthrough in `docs/go-ipxe/` quotes the real
  source. If your change alters code or behavior a chapter shows — a snippet, a
  flag, a status code, a log message — update the chapter in the same PR.
- Run `just check` (lint + test) before pushing. `just ci` runs the full gate CI
  will apply.

### 3. Commit

Follow [Conventional Commits](https://www.conventionalcommits.org/) — the
changelog is generated from them via `git-cliff`:

```text
feat(render): add proxmox-answer render kind
fix(tftp): send final zero-length block for exact-multiple files
docs(guide): update chapter 6 for the fourth render kind
test(proxydhcp): pin option-43 sub-option encoding
```

Format:

```text
<type>(<scope>): <imperative subject>

<optional body explaining why, not what>
```

Scopes generally match the package: `catalog`, `render`, `httpsrv`, `tftp`,
`proxydhcp`, `cmd`, `e2e`, `guide`.

### 4. Open a pull request

Push your branch and open a PR against `main`. The PR description should:

- Explain what changed and why
- Link to the related issue (if any) with `Fixes #123` or `Refs #123`
- Include a test plan or describe how you verified the change

Releases are label-driven: a maintainer applies a `major`/`minor`/`patch` label
before merge to cut a release on merge (or `dont-release` to skip), so there's
nothing release-related for you to do in the PR itself.

## License

By contributing you agree that your contributions will be licensed under the
[Apache 2.0 License](LICENSE).
