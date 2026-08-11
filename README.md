# booty

[![Go Reference](https://pkg.go.dev/badge/github.com/donaldgifford/booty.svg)](https://pkg.go.dev/github.com/donaldgifford/booty)
[![CI](https://github.com/donaldgifford/booty/actions/workflows/ci.yml/badge.svg)](https://github.com/donaldgifford/booty/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/donaldgifford/booty/branch/main/graph/badge.svg)](https://codecov.io/gh/donaldgifford/booty)
[![Go Report Card](https://goreportcard.com/badge/github.com/donaldgifford/booty)](https://goreportcard.com/report/github.com/donaldgifford/booty)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

A Go library for building network-boot services — proxyDHCP, TFTP, iPXE,
identity matching, and config rendering — plus `booty`, the reference binary
built from it.

booty answers the whole chain a bare machine walks from power-on to a running,
configured OS:

```text
NIC ROM ──DHCP──▶ proxyDHCP :67/:4011 ──▶ TFTP :69 (ipxe.efi) ──▶ iPXE ──▶ HTTP :8080
                  (boot steering only;      (RFC 1350 + options)      (scripts, kernels,
                   your DHCP keeps leases)                             per-machine config)
```

It is **Talos-first** — the primary path is a machine pulling its Talos
machineconfig — with cloud-init (NoCloud) and Proxmox VE automated installs as
the other supported config paths.

## The library

Public packages live at the top level; external consumers import them directly
([ADR-0002](docs/adr/0002-booty-is-a-library-with-cmdbooty-as-reference-consumer.md)).
v0 semver: the API may still move.

| Package      | What it does                                                                                                    |
| ------------ | --------------------------------------------------------------------------------------------------------------- |
| `catalog/`   | Identity → group → profile matching, authored in HCL (variables, expressions, functions) via `DirSource`                 |
| `render/`    | `text/template` pipeline for every output: iPXE scripts, Talos machineconfig, cloud-init, Proxmox `answer.toml` |
| `httpsrv/`   | Stdlib-only HTTP serving core: boot scripts, boot assets, and all config endpoints, dependency-gated routing    |
| `tftp/`      | Read-only TFTP server from raw UDP (RFC 1350 + blksize/tsize/timeout negotiation, traversal guard)              |
| `proxydhcp/` | Spec-correct two-phase PXE proxyDHCP + port-4011 (BINL) responder — coexists with your existing DHCP server     |

`cmd/booty` is the reference consumer: thin flag parsing and wiring, nothing
else. Everything testable lives in the packages above; `internal/` is reserved
for genuinely private helpers.

```go
cat, err := catalog.DirSource{Root: "catalog/"}.Load(ctx)
renderer, err := render.New() // render.WithTemplates(os.DirFS(dir)) overlays the embedded templates
srv, err := httpsrv.New(httpsrv.Config{Catalog: cat, Renderer: renderer, BootDir: "boot/"})
err = srv.ListenAndServe(ctx, ":8080")
```

## The guide

The whole service is built from scratch — wire formats, protocol history,
failure modes — in a ten-chapter walkthrough:
**[docs/go-ipxe](docs/go-ipxe/00-index.md)**. The guide and this codebase are
the same thing: every chapter produces the real, compiling, tested packages
above, and ends with commands you can run.

## Install

Pick whichever fits; all three are validated against each release.

```sh
VERSION=0.2.0

# Release archive (see "Verifying a release" below before trusting it)
curl -fsSLO "https://github.com/donaldgifford/booty/releases/download/v${VERSION}/booty_${VERSION}_linux_amd64.tar.gz"
tar xzf "booty_${VERSION}_linux_amd64.tar.gz" && install booty ~/.local/bin/

# From source, by version
go install github.com/donaldgifford/booty/cmd/booty@latest

# Container
docker pull "ghcr.io/donaldgifford/booty:${VERSION}"
```

Archives are published for linux and darwin on amd64 and arm64. Container tags
are unprefixed — `:0.2.0`, not `:v0.2.0` — while git tags carry the `v`.

The archive URL names a tag rather than going through `/releases/latest/`.
Both are pinned to a version either way, because goreleaser stamps the version
into the filename — so a `/latest/download/booty_0.1.1_…` URL starts returning
404 the moment 0.1.2 ships, which is worse than being visibly out of date. To
always get the newest, resolve the tag first:

```sh
VERSION=$(curl -fsSL https://api.github.com/repos/donaldgifford/booty/releases/latest | jq -r .tag_name)
VERSION=${VERSION#v}
```

A `go install` binary knows its version but not its commit or build date: the
module proxy serves a source zip, not a checkout, so there is no VCS stamp to
embed. The release archives and the container image report all three.

## Quickstart

```sh
mise install                        # pinned toolchain (go, golangci-lint, just, …)
just build                          # binary at build/bin/booty
just test                           # unit tests, race detector

./build/bin/booty validate --catalog examples/catalog
# ok: dir://examples/catalog — 4 profiles, 5 groups

./build/bin/booty serve --catalog examples/catalog --boot-dir ./boot \
  --url http://192.168.1.10:8080
```

Opt-in extras on `serve`: `--proxydhcp` + `--server-ip` (PXE boot steering
without touching your DHCP server), `--templates-dir` (operator template
overrides), `--proxmox-token` (bearer auth for Proxmox answer requests).

### What it serves

| Endpoint                                            | Consumer                                             |
| --------------------------------------------------- | ---------------------------------------------------- |
| `GET /healthz`, `GET /readyz`                       | probes                                               |
| `GET /boot.ipxe`                                    | iPXE chain script (collects identity)                |
| `GET /ipxe?mac=…`                                   | per-machine iPXE boot script                         |
| `GET /boot/{path...}`                               | kernels, initrds, boot assets                        |
| `GET /machine-config?mac=…`                         | Talos machineconfig                                  |
| `GET /cloud-init/{meta-data,user-data,vendor-data}` | cloud-init NoCloud (matched by source IP)            |
| `POST /proxmox/answer`                              | Proxmox automated installer (DMI + NICs in the body) |

Plus TFTP on `:69` and, with `--proxydhcp`, the PXE handshake on `:67`/`:4011`.

## Development

```sh
just check          # pre-commit gate: lint + test
just test-e2e       # e2e harness: protocol tier runs anywhere; the QEMU/OVMF
                    # full-boot tier skips unless BOOTY_E2E_QEMU,
                    # BOOTY_E2E_OVMF_CODE and BOOTY_E2E_IPXE are set
just ci             # full CI gate: lint + test + build + license-check
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the workflow, and `CLAUDE.md` for
repo conventions. Design decisions are recorded as ADRs in [docs/adr](docs/adr/)
([HCL for the catalog](docs/adr/0001-hcl-for-catalog-configuration.md),
[library + reference consumer](docs/adr/0002-booty-is-a-library-with-cmdbooty-as-reference-consumer.md)).

## Release

Releases are label-driven: merging a PR with a `major`/`minor`/`patch` label
bumps the version, tags, runs goreleaser, and pushes the multi-arch container
image to GHCR (label `dont-release` to skip). Multi-arch (linux+darwin ×
amd64+arm64) archives with SBOMs and signed checksums land on the release page.
Version metadata (`version`, `commit`, `date`) is embedded via `-ldflags`;
`booty version` prints it.

### Verifying a release

`checksums.txt` is signed with the release key, whose public half is committed
as [`docs/booty-release.pub.asc`](docs/booty-release.pub.asc):

```text
C47D 59D8 6FC2 4BAE C5BB  3271 2E2C EA0B C2BD 8D59
```

Verify the signature, then the archive against the checksum it covers — the
signature only attests to `checksums.txt`, so skipping the second step verifies
nothing about what you downloaded:

```sh
gpg --import docs/booty-release.pub.asc
gpg --verify checksums.txt.sig checksums.txt
sha256sum --ignore-missing -c checksums.txt
```

Importing the key from this repo means you are trusting the repo. To do better,
confirm the fingerprint above through a channel that isn't GitHub.

## Container

```sh
just docker-build             # local image via docker buildx bake
```

`docker-bake.hcl` defines the targets: a local single-arch build, a fast
linux/amd64 CI build, and a multi-arch (amd64+arm64) release push with SBOM +
provenance attestations. The image is distroless (`static-debian12:nonroot`)
with `booty` as the entrypoint. Two runtime notes: the nonroot user (UID 65532)
can't bind the privileged UDP ports (69, 67, 4011) without
`CAP_NET_BIND_SERVICE` or remapping them via `--tftp-addr`/`--proxydhcp-addr`,
and the rootfs is read-only — mount the catalog and boot assets as volumes.

## Layout

```text
catalog/        identity→group→profile model, matcher, HCL source
render/         template pipeline + embedded templates (ipxe, talos, cloud-init, proxmox)
httpsrv/        HTTP serving core (boot + config endpoints)
tftp/           read-only TFTP server
proxydhcp/      PXE proxyDHCP + BINL responder
cmd/booty/      reference consumer — flags + wiring only
test/e2e/       build-tagged e2e harness (protocol tier + QEMU tier)
examples/       example HCL catalog (the one the guide and e2e tests use)
docs/go-ipxe/   the ground-up walkthrough that builds all of the above
docs/adr/       architecture decision records
```

## License

Apache-2.0
