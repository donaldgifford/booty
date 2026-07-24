# Chapter 7: The HTTP Serving Core — Standard Library Only

← [Chapter 6](./06-render-pipeline.md) | [Chapter 8: Assembly & CLI →](./07-forge-complete.md)

---

The last three chapters built *handlers*: the iPXE chain and boot scripts (Chapter
4), the boot-asset file server (Chapter 4), the Talos machineconfig and cloud-init
endpoints (Chapter 6). Each was written as a method on `*Server`, and each assumed
something was there to route requests to it, wrap it in logging, apply timeouts,
and shut it down cleanly on a signal.

This chapter is that something — the *host*. It's the part of `httpsrv`
that isn't any one endpoint: the construction pattern, the router, the one piece of
middleware, and the lifecycle. It's deliberately small, and its smallness is the
point. This is where PLAN-0001's **P1 — a stdlib-first serving core** — is cashed
out: no web framework, no router library, no middleware chain package. Just
`net/http`, wired the way the standard library intends.

Source: [`httpsrv/httpsrv.go`](../../httpsrv/httpsrv.go) — the
whole serving core is one file.

## Why `net/http` is genuinely enough

The reflex, coming from other ecosystems, is to reach for a framework: a router
for path parameters, a middleware chain, maybe a DI container to wire it. booty
uses none of them, and it isn't a purity stunt — it's that since Go 1.22 the
standard library closed the one gap that used to justify a router:

| Need | Old gap | Since 1.22 / stdlib |
|------|---------|---------------------|
| Method + path routing | `ServeMux` matched paths only | `mux.HandleFunc("GET /ipxe", …)` — method-qualified |
| Path wildcards | Manual `strings.TrimPrefix` | `GET /boot/{path...}` + `r.PathValue("path")` |
| Large-file serving | — | `http.ServeFile` (Range, ETag, sendfile) |
| Graceful shutdown | — | `srv.Shutdown(ctx)` |
| Middleware | — | a `Handler` is an interface; wrap it |
| Structured logs | `log` was unstructured | `log/slog` (1.21) |

Everything booty serves fits inside that table. A framework would add a dependency
(and an ADR to justify it, per P5) to solve problems booty doesn't have. The
serving core stays framework-free; the *only* third-party dependency in the whole
binary is HCL, in the catalog (Chapter 5, [ADR-0001](../adr/0001-hcl-for-catalog-configuration.md)),
and it never touches this layer.

## Construction: options in, routes light up

The server is built from an `Options` struct, not a config file or a builder
chain. Every dependency is a field, and every field is optional:

```go
type Options struct {
	Logger   *slog.Logger
	Catalog  *catalog.Catalog
	Renderer *render.Renderer
	BootDir  string
	BaseURL  string
	// Required as `Authorization: Bearer <token>` on POST /proxmox/answer
	// when set — the same name:secret baked into the Proxmox ISO.
	ProxmoxAuthToken string
}

func New(opts Options) *Server { … }
```

The interesting move is in `Handler()`, which builds the mux. Routes aren't
registered unconditionally — each route is **gated on the dependencies it needs**:

```go
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleHealthz)

	if s.renderer != nil {
		mux.HandleFunc("GET /boot.ipxe", s.handleChain)
	}
	if s.catalog != nil && s.renderer != nil {
		mux.HandleFunc("GET /ipxe", s.handleIPXE)
		mux.HandleFunc("GET /machine-config", s.handleMachineConfig)
		mux.HandleFunc("GET /cloud-init/meta-data", s.handleCloudInitMetaData)
		mux.HandleFunc("GET /cloud-init/user-data", s.handleCloudInitUserData)
		mux.HandleFunc("GET /cloud-init/vendor-data", s.handleCloudInitVendorData)
		mux.HandleFunc("POST /proxmox/answer", s.handleProxmoxAnswer)
	}
	if s.bootDir != "" {
		mux.HandleFunc("GET /boot/{path...}", s.handleBoot)
	}
	return s.logRequests(mux)
}
```

This gating buys three things:

1. **Health always works.** A booty started with no catalog — say, in a Kubernetes
   pod that's still waiting for its config volume — still answers `/healthz`. The
   liveness probe never depends on the boot surface being ready.
2. **One construction path serves every shape.** A health-only server, a
   chain-script-only server (renderer but no catalog), and a full boot server are
   the *same code* with different `Options`. There's no separate "minimal server"
   type.
3. **404 is honest.** `GET /ipxe` on a server with no catalog isn't a stub that
   returns an error — the route simply isn't registered, so the mux returns a real
   404. `TestRoutesGatedByDeps` pins exactly that.

Recall from Chapter 4 that `handleChain` needs only the renderer (the chain script
carries no per-machine state), which is why it's gated on `renderer != nil` alone,
while `/ipxe` and friends need both a catalog to match against *and* a renderer to
render with.

## Routing with the 1.22 ServeMux

The patterns above use two features that landed in Go 1.22:

- **Method matching.** `"GET /ipxe"` matches `GET` (and `HEAD`) but not `POST`. A
  `POST /ipxe` gets a `405 Method Not Allowed` from the mux itself — no per-handler
  method check. Almost every booty route is read-only `GET`; the one exception is
  `POST /proxmox/answer`, where the Proxmox installer *sends* its identity as a
  JSON body (Chapter 6). The mux enforces that too: a `GET /proxmox/answer` gets
  the same automatic 405.
- **The `{path...}` wildcard.** `"GET /boot/{path...}"` matches `/boot/` followed by
  any number of segments, captured as `r.PathValue("path")`. That's what lets
  `handleBoot` serve `talos/v1.7.6/vmlinuz` as a single nested path without the
  server doing its own prefix stripping.

Pattern precedence is longest-match-wins and is decided by the mux, so the fixed
routes (`/boot.ipxe`, `/machine-config`) never collide with the `/boot/{path...}`
wildcard even though `/boot.ipxe` and `/boot/` share a prefix — they're distinct
patterns. (`.ipxe` is not under `/boot/`, so there was never a real ambiguity, but
it's worth knowing the mux would resolve one correctly if there were.)

## Middleware is a function that wraps a Handler

booty has exactly one piece of middleware — request logging — and it's the
textbook shape: a method that takes the `next` handler and returns a new one:

```go
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		s.logger.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration", time.Since(start).Round(time.Millisecond),
			"remote_addr", r.RemoteAddr,
			"user_agent", r.UserAgent(),
		)
	})
}
```

One structured line per request, and because it's `slog`, the operator greps
`status=404` or `path=/machine-config` directly. In `--log-format json` those
become real JSON fields.

There's a subtlety hiding in `statusRecorder`. `http.ResponseWriter` gives you no
way to read back the status code you wrote — so to log it, we have to intercept it:

```go
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.status = status
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(status)
}
```

The `wroteHeader` guard matters more than it looks. A handler (or `http.ServeFile`)
can call `WriteHeader` and then, on some paths, effectively try again; HTTP only
sends the *first* status, so the logger must record the first one too. Without the
guard, a later call would overwrite `status` and the log would disagree with what
the client actually received. It also defaults to `200`, because a handler that
just calls `Write` without `WriteHeader` — like `handleHealthz` — implicitly sends
a 200, and the recorder has to know that.

This wrapper is also what makes the two different status-code contracts from the
earlier chapters *observable*. `/ipxe` deliberately never returns non-200 (iPXE
firmware hangs on errors — Chapter 4); `/machine-config` uses real 404/409/500
codes (Talos retries — Chapter 6). The middleware logs both faithfully, so a
`status=200` on `/ipxe` after a no-match and a `status=404` on `/machine-config`
after the same no-match both show up truthfully in one log stream.

## Lifecycle: driven by context, not by signals

Here's a design choice that looks small and isn't. The original sketch of this
server (the draft this chapter replaces) handled `SIGINT`/`SIGTERM` *inside* the
serving package — `signal.Notify` right next to `ListenAndServe`. booty doesn't.
`ListenAndServe` takes a `context.Context` and shuts down when that context is
cancelled, full stop:

```go
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      300 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	// ctx is already cancelled here; WithoutCancel keeps its values but gives
	// the drain its own 30-second deadline instead of an instant timeout.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
```

The reason is P1/`main()`-owns-wiring discipline (the same rule that bans behavior
in `init()`): a *library* package should not decide the process's signal policy.
Who says `SIGTERM` should stop the HTTP server? Maybe the process also runs the
TFTP server, and both should drain together on one signal. That's a decision for
`cmd/booty`, and that's exactly where it lives (Chapter 8):

```go
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer stop()
…
wg.Go(func() { httpServer.ListenAndServe(ctx, *httpAddr) })
wg.Go(func() { tftp.New(...).ListenAndServe(ctx, *tftpAddr) })
```

One `ctx`, every server. A single `SIGTERM` cancels it, and the HTTP and TFTP
servers (plus proxyDHCP, when enabled — Chapter 8) all observe the cancellation
and drain in parallel. The serving package stays a library — it reacts to a
cancelled context, which is trivially testable, instead of installing a global
signal handler, which is not.

The shutdown itself is graceful: `srv.Shutdown` stops accepting new connections
and waits — up to a bounded 30 seconds — for in-flight requests to finish. That
bound matters because of what booty serves: a node halfway through pulling a 200 MB
initrd should be allowed to finish, but a wedged connection shouldn't hold the
process open forever. The goroutine-plus-`errc` pattern means a fatal listen error
(port already bound, say) also unblocks the `select` and propagates out, rather
than being swallowed in the background goroutine.

## Timeouts: the tension of a serving core that streams

Those four timeout values aren't boilerplate; each defends a different failure
mode, and the odd one out (`WriteTimeout: 300s`) exists specifically because of
the boot workload:

- **`ReadHeaderTimeout: 10s`** — the cheap slowloris guard. A client that opens a
  connection and dribbles request headers gets cut at 10 seconds. This is the one
  timeout you always want and it costs nothing.
- **`ReadTimeout: 30s`** — the whole request must arrive within 30s. booty's
  requests are tiny (`GET`s with query strings), so this is generous.
- **`WriteTimeout: 300s`** — the response must be *written* within five minutes.
  For a health check that's absurd overkill; for a `/boot/initrd.xz` transfer of a
  hundred-plus megabytes to a node on a slow provisioning VLAN, 30 seconds would
  cut the download mid-flight and the boot would fail mysteriously. The serving
  core has to host both the 3-byte `ok\n` and the 200 MB initrd on the same
  listener, and `WriteTimeout` is set for the worst case.
- **`IdleTimeout: 120s`** — how long a keep-alive connection may sit unused before
  the server closes it.

This is the real tension in a one-listener boot server: it is simultaneously a
low-latency API (scripts, config) and a bulk file server (kernels, initrds). The
timeouts are tuned for the file server because getting *that* wrong breaks boots,
and the small requests never come close to the ceilings anyway. (A finer design
would put a tight `http.TimeoutHandler` on the API routes and leave `/boot/`
unbounded — noted under *what's deferred*.)

## The testable seam

Notice the split: `Handler()` returns a plain `http.Handler`, and
`ListenAndServe` is a thin lifecycle wrapper around it. That's deliberate, and
it's the same seam the TFTP server has (`Serve(conn)` vs `ListenAndServe` in
Chapter 3). Every handler test drives `Handler()` directly through
`httptest` — no socket, no port, no goroutine:

```go
func newTestServer(t *testing.T, opts Options) http.Handler {
	if opts.Logger == nil {
		opts.Logger = quiet()      // discard logs in tests
	}
	if opts.Renderer == nil {
		r, _ := render.New()
		opts.Renderer = r
	}
	return New(opts).Handler()
}
```

Because construction is dependency-gated, each test builds exactly the surface it
needs and nothing more: `TestHealthz` passes no catalog at all;
`TestRoutesGatedByDeps` asserts `/ipxe` is a 404 without one; the Chapter 6 tests
pass a `talosConfigCatalog()` to light up `/machine-config`. The lifecycle path
(`ListenAndServe`, signals, graceful drain) is exercised end-to-end by the manual
smoke tests and the QEMU e2e harness (Chapter 9), not by unit tests that would
have to bind real ports and race on shutdown.

## Try it yourself

```bash
go build -o bin/booty ./cmd/booty
./bin/booty serve --catalog examples/catalog --boot-dir /tmp \
  --url http://boot.home.local:8080 --log-format json &
BOOTY=$!

# Health answers regardless of the boot surface:
curl -s localhost:8080/healthz          # -> ok

# Each request emits one structured JSON log line (status, path, duration…):
curl -s "localhost:8080/machine-config?mac=d0:50:99:b3:4c:50" >/dev/null
# … {"level":"INFO","msg":"http request","method":"GET","path":"/machine-config","status":200,…}

# A method the routes don't allow is a 405 from the mux, not a handler:
curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8080/ipxe   # -> 405

# SIGTERM drains in-flight requests, then exits cleanly:
kill -TERM $BOOTY
# … {"msg":"shutdown signal received"} … {"msg":"booty stopped"}
```

The 405 is worth pausing on: nowhere in booty's code is there a method check on
`/ipxe`. That response comes entirely from the 1.22 mux recognizing the pattern was
registered `GET`-only. That's the whole argument of this chapter in one line — the
standard library already does the job a router would have been imported for.

## What's deferred

- **Per-route timeouts.** One `WriteTimeout` for both a 3-byte health check and a
  200 MB initrd is a compromise. `http.TimeoutHandler` on the API routes (with
  `/boot/` left unbounded) is the refinement; it's a small change on top of this
  structure.
- **A real readiness check.** `/readyz` currently aliases `/healthz`. A true
  readiness probe would report "catalog loaded and non-empty," distinguishing
  *live* from *ready* — useful once booty runs under an orchestrator.
- **TLS.** Plain HTTP is correct on a trusted provisioning VLAN; `ListenAndServeTLS`
  (or a reverse proxy) is the story for anything less trusted, and pairs with the
  signed-config note from Chapter 6.
- **A metrics endpoint.** One structured log line per request is enough to debug a
  homelab; a `/metrics` endpoint (request counts, boot outcomes) is the obvious
  next observability layer and, like everything here, needs no framework.
- **Operator template overrides (`--templates-dir`).** Templates are embedded today
  (single-binary, P4). Letting an operator override them from disk without
  recompiling is a natural extension of the renderer from Chapters 4 and 6.

---

← [Chapter 6](./06-render-pipeline.md) | [Chapter 8: Assembly & CLI →](./07-forge-complete.md)
