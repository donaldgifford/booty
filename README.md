# booty

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
| `catalog/`   | Identity → group → profile matching, authored in HCL (variables, expressions, functions) via a `Source` seam    |
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
srv := httpsrv.New(httpsrv.Options{Catalog: cat, Renderer: renderer, BootDir: "boot/"})
err = srv.ListenAndServe(ctx, ":8080")
```

## The guide

The whole service is built from scratch — wire formats, protocol history,
failure modes — in a ten-chapter walkthrough:
**[docs/go-ipxe](docs/go-ipxe/00-index.md)**. The guide and this codebase are
the same thing: every chapter produces the real, compiling, tested packages
above, and ends with commands you can run.

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

```sh
just release v0.1.0           # tags + pushes; CI runs goreleaser on the tag
```

Multi-arch (linux+darwin × amd64+arm64) archives land on the release page.
Version metadata (`version`, `commit`, `date`) is embedded via `-ldflags`;
`booty version` prints it.

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
