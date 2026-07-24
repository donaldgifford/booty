# Chapter 8: Assembly and the CLI

← [Chapter 7](./06-http-server-stdlib.md) | [Chapter 9: QEMU / OVMF End-to-End →](./09-qemu-e2e.md)

---

Earlier chapters built five packages, each tested in isolation: `tftp`,
`proxydhcp`, `catalog`, `render`, and `httpsrv`. None of them is a program. This
chapter is the ~200 lines that make one — `cmd/booty/main.go` — and the
discipline that keeps it small.

The rule, stated in booty's `CLAUDE.md` and inherited from PLAN-0001, is that
`main` parses flags, wires dependencies, and calls into the library — nothing
else. No business logic, no `init()` doing work at import time, no globals holding
state. Everything worth testing already lives in the library packages, where the
tests are. `main` is the part you *can't* unit-test (it reads `os.Args`, it calls
`os.Exit`), so the design goal is to make that untestable part as close to zero as
possible.

Source: [`cmd/booty/main.go`](../../cmd/booty/main.go) — the whole program entry
point is one file.

## The layout, as it actually is

```
cmd/booty/main.go          # thin: flags, wiring, subcommand dispatch
tftp/tftp.go               # Ch 3 — read-only TFTP, raw UDP
proxydhcp/proxydhcp.go     # Ch 2 — proxyDHCP + BINL (port 4011)
catalog/{catalog,source}.go  # Ch 5 — identity→group→profile, HCL loader
render/render.go           # Ch 4 & 6 — iPXE / Talos / cloud-init / Proxmox templating
httpsrv/httpsrv.go         # Ch 4, 6 & 7 — the stdlib serving core
examples/catalog/*.hcl     # a runnable example catalog
justfile · .goreleaser.yml · Dockerfile · mise.toml   # build & release
```

Two structural facts do a lot of work here. The packages are booty's **public
library API** ([ADR-0002](../adr/0002-booty-is-a-library-with-cmdbooty-as-reference-consumer.md)):
`cmd/booty` is the *reference consumer*, importing exactly the same surface any
external program (the homelab platform) does — the owned-interface discipline
(P3) that kept HCL types out of the catalog API is what made those seams safe to
publish. And the whole thing compiles to a **single static binary**
(`CGO_ENABLED=0`, P4) — even with HCL pulled in at the catalog layer, every
dependency is pure Go, so deploying booty is copying one file.

## Dispatch: a function that returns an exit code

`main` itself is one line, and that's the entire trick:

```go
func main() {
	os.Exit(run(os.Args))
}

func run(args []string) int {
	if len(args) < 2 {
		usage(os.Stderr)
		return 2
	}
	switch args[1] {
	case "serve":
		return cmdServe(args[2:])
	case "validate":
		return cmdValidate(args[2:])
	case "version", "--version", "-v":
		fmt.Printf("booty %s (commit %s, built %s)\n", version, commit, date)
		return 0
	case "help", "-h", "--help":
		usage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "booty: unknown command %q\n\n", args[1])
		usage(os.Stderr)
		return 2
	}
}
```

`os.Exit` can't be tested — it terminates the test binary — so it's isolated to
the one place it's unavoidable. Everything else lives in `run`, which takes its
args as a parameter and *returns* the exit code instead of calling `os.Exit`. That
one indirection makes the whole command surface testable: a future `main_test.go`
can call `run([]string{"booty", "version"})` and assert on the return value,
because `run` touches no global process state.

booty is subcommand-shaped (`booty serve`, `booty validate`), not a single flag
soup, so dispatch is a `switch` on `args[1]` and each subcommand owns its own
`flag.FlagSet`. The exit-code convention is the Unix one, and it's deliberate:

| Code | Meaning | Cases |
|------|---------|-------|
| `0` | success | `serve` exited cleanly, `validate` passed, `version`, `help` |
| `1` | runtime failure | catalog load failed, render init failed, a server errored |
| `2` | usage error | no subcommand, unknown subcommand, bad flags |

The `2`-vs-`1` split matters for scripting: a CI job can tell "you invoked me
wrong" (2) from "your config is broken" (1) from "it worked" (0) without parsing
stderr.

## `validate`: the config admission test

`validate` is the smallest subcommand and arguably the most important, because
it's the **admission test** (PLAN-0001 P2) for configuration. A booting machine is
a bad place to discover that a catalog has a typo. So a config repo runs `booty
validate` in CI, and a catalog that doesn't parse and resolve never merges:

```go
func cmdValidate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	dir := fs.String("catalog", "./catalog", "catalog directory of *.hcl files")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	src := catalog.DirSource{Root: *dir}
	cat, err := src.Load(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid catalog %s:\n  %v\n", src, err)
		return 1
	}
	fmt.Printf("ok: %s — %d profiles, %d groups\n", src, len(cat.Profiles), len(cat.Groups))
	return 0
}
```

It reuses the exact `DirSource.Load` path `serve` uses, so "validates in CI" and
"loads at boot" can't drift apart. And the errors it surfaces are the real
semantic ones from Chapter 5 — a group pointing at a profile that doesn't exist
comes out as:

```
$ booty validate --catalog ./broken
invalid catalog dir://broken:
  group "x": references unknown profile "nope"
# exit 1
```

That's the dangling-reference check from the catalog's `validate()` firing through
the CLI gate, exactly where you want it: at review time, not at boot time.

## `serve`: wiring the servers together

`serve` is where the packages meet. Flags first — each with a sane default so
booty runs with no config at all:

```go
httpAddr   := fs.String("http-addr", "0.0.0.0:8080", "HTTP listen address")
tftpAddr   := fs.String("tftp-addr", "0.0.0.0:69",   "TFTP listen address")
bootDir    := fs.String("boot-dir", "./boot", "boot assets served over TFTP and HTTP")
catalogDir := fs.String("catalog", "", "catalog directory of *.hcl files")
baseURL    := fs.String("url", "", "externally reachable base URL (default: derived from Host)")
logFormat  := fs.String("log-format", "text", "log format: text or json")
```

Plus the opt-in extras: `--templates-dir` (operator template overrides, layered
over the embedded set via `render.WithTemplates(os.DirFS(dir))`), `--proxmox-token`
(bearer auth on `/proxmox/answer`, Chapter 6), and the `--proxydhcp` /
`--server-ip` pair that enables the Chapter 2 responder.

Then the logger is built once and installed as the process default:

```go
logger := newLogger(*logFormat)
slog.SetDefault(logger)
```

That `slog.SetDefault` is the payoff of the "structured logs via `slog`, set the
handler in `main`" rule from `CLAUDE.md`: library code that reaches for
`slog.Default()` gets the configured handler without anyone threading a `*Logger`
through every constructor. booty mostly passes the logger explicitly (it's a field
on both servers), but the default is set so nothing logs to a stray handler.

The catalog is loaded eagerly (`cat, ok := loadCatalog(ctx, *catalogDir, logger)`),
and the helper encodes a deliberate choice about *degraded operation*:

```go
func loadCatalog(ctx context.Context, dir string, logger *slog.Logger) (*catalog.Catalog, bool) {
	if dir == "" {
		return nil, true // no catalog is fine; a broken one is not
	}
	src := catalog.DirSource{Root: dir}
	cat, err := src.Load(ctx)
	if err != nil {
		logger.Error("catalog load failed", "source", src.String(), "err", err)
		return nil, false // fail fast, at boot, not at first request
	}
	return cat, true
}
```

A *broken* catalog is fatal (exit 1) — fail fast rather than serve wrong configs.
But *no* catalog is fine: `cat` stays nil, and recall from Chapter 7 that
`httpsrv.Handler()` gates its routes on `cat != nil`. So `booty serve` with no
`--catalog` still answers health, serves the chain script, and serves boot assets
— it just won't resolve per-machine scripts. That's a genuinely useful mode (a
plain TFTP+asset server) and it falls out of the dependency-gated construction for
free.

### The concurrency pattern: one context, three servers

This is the heart of assembly, and it's where Chapter 7's decision to make
lifecycles *context-driven* pays off. One context, cancelled by either signal,
drives every server — HTTP and TFTP always, proxyDHCP when enabled:

```go
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer stop()

var wg sync.WaitGroup
errc := make(chan error, 3)

httpServer := httpsrv.New(httpsrv.Options{
	Logger: logger, Catalog: cat, Renderer: renderer,
	BootDir: *bootDir, BaseURL: *baseURL, ProxmoxAuthToken: *proxmoxToken,
})
wg.Go(func() {
	if err := httpServer.ListenAndServe(ctx, *httpAddr); err != nil {
		errc <- fmt.Errorf("http: %w", err)
	}
})
wg.Go(func() {
	if err := tftp.New(*bootDir, logger).ListenAndServe(ctx, *tftpAddr); err != nil {
		errc <- fmt.Errorf("tftp: %w", err)
	}
})
if proxy != nil { // --proxydhcp: built earlier so a bad --server-ip fails fast
	wg.Go(func() {
		if err := proxy.ListenAndServe(ctx, *proxyDHCPAddr, *proxyDHCPBINLAddr); err != nil {
			errc <- fmt.Errorf("proxydhcp: %w", err)
		}
	})
}

var runErr error
select {
case <-ctx.Done():
	logger.Info("shutdown signal received")
case runErr = <-errc:
	logger.Error("server error", "err", runErr)
	stop()               // one server died — cancel ctx so the others drain too
}
wg.Wait()
```

Four details are load-bearing:

- **`signal.NotifyContext`** turns a `SIGINT`/`SIGTERM` into a cancelled `ctx` —
  the process's signal policy lives here in `main`, not buried in a library
  (Chapter 7). Every server observes the *same* `ctx`, so one Ctrl-C drains them
  all in parallel.
- **`wg.Go`** is the Go 1.25 `WaitGroup.Go` method — it does the `Add(1)` /
  `defer Done()` bookkeeping internally, so the launch site is just "run this
  function in the group." (booty's `go.mod` is on 1.26, so this is available;
  before 1.25 you'd write the `wg.Add(1); go func(){ defer wg.Done(); … }()` dance
  the old draft of this chapter used.)
- **The buffered `errc` (cap 3)** means a server that errors can post and return
  even if nobody is selecting yet — no goroutine leaks on a fast multi-failure.
- **`stop()` in the error branch** is the subtle one. If the HTTP server dies (say
  its port was taken), we cancel `ctx` ourselves so the *healthy* servers also
  shut down. Without it, one failed server would leave the others running and the
  process half-alive. `wg.Wait()` then blocks until all have fully drained before
  `run` returns its exit code.

The result: `SIGTERM` → every server stops accepting, each finishes in-flight
transfers (that 200 MB initrd gets to complete), all return, process exits 0. A
fatal error in any → all drain, process exits 1. There is no path where one
server is up and another is wedged.

## Version metadata via `-ldflags`

`main` declares three variables that are empty-ish by default and filled at build
time:

```go
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)
```

Nothing sets them in source. The linker does, via `-X main.<var>=<value>`. A local
build through the `justfile` injects the git-derived version and commit:

```
go build -ldflags "-X main.version=$(git describe --tags --always --dirty) \
                   -X main.commit=$(git rev-parse --short HEAD)" ./cmd/booty
$ booty version
booty v0.1.0-3-gabc1234 (commit abc1234, built unknown)
```

Note `built unknown` — the local recipe injects version and commit but not date;
only the release path fills all three. `.goreleaser.yml` sets `version`, `commit`,
*and* `date` (plus `-s -w -trimpath` and `CGO_ENABLED=0`) and cross-compiles the
matrix — linux+darwin × amd64+arm64 — from that one `main` package:

```yaml
ldflags:
  - -s -w
  - -X main.version={{ .Version }}
  - -X main.commit={{ .Commit }}
  - -X main.date={{ .Date }}
```

So `booty version` on a released binary reports the exact tag, commit, and build
timestamp — the provenance a homelab operator needs to answer "which build is
actually running on that node?" A plain `go build` with no ldflags honestly
reports `booty dev (commit none, built unknown)`, which is the correct answer for
an un-stamped build.

## Try it yourself

```bash
# Build (either works; just adds the version/commit ldflags)
just build            # -> build/bin/booty
go build -o bin/booty ./cmd/booty

booty version                              # booty dev (commit none, built unknown)
booty help                                 # subcommand list, exit 0
booty frobnicate; echo $?                  # unknown command → usage on stderr, exit 2

# The CI gate — passes on the example catalog, fails closed on a broken one:
booty validate --catalog examples/catalog  # ok: … — 4 profiles, 5 groups   (exit 0)

# The whole service, both servers under one signal:
booty serve --catalog examples/catalog --boot-dir /tmp/boot \
  --url http://boot.home.local:8080 --log-format json
# ^C  →  "shutdown signal received"  →  both drain  →  "booty stopped"
```

Everything in the previous five chapters is reachable from those commands. `booty
validate` is the catalog and its HCL loader; `booty serve` is the render pipeline
and the stdlib serving core hosting TFTP, HTTP, and (opt-in) proxyDHCP under a
single cancellable context. The binary is one file, depends on one third-party
library (HCL), and starts in milliseconds.

## What's deferred

- **Server config in HCL.** Server flags (`--http-addr`, …) are plain `flag`s
  today. Moving them into an HCL config file reuses the Chapter 5 loader and is a
  small, later change — the flags stay as overrides.
- **Graceful catalog reload.** A `SIGHUP` that re-runs `DirSource.Load` and swaps
  the catalog atomically (no restart) is a natural addition now that load is a pure
  function returning a fresh `*Catalog`.
- **The heavier PLAN-0001 surface** — the boot-funnel UI, a SQLite-backed
  inventory, the `machinery` secrets bundle, the bundle/asset pipeline — all sit
  *after* the walkthrough. What this chapter assembles is the tested, bootable
  core they build on.

With `serve` wired, booty is a real program. The remaining question is whether it
actually boots a machine — which is what Chapter 9 answers, by driving the whole
stack from a QEMU VM with UEFI firmware, no hardware required.

---

← [Chapter 7](./06-http-server-stdlib.md) | [Chapter 9: QEMU / OVMF End-to-End →](./09-qemu-e2e.md)
