// Command booty is the reference consumer of the booty library (ADR-0002): a
// standalone network-boot service serving the iPXE binary over TFTP and boot
// scripts/assets/config over HTTP. The main package is deliberately thin — it
// parses flags, wires dependencies, and calls into the library packages
// (catalog, render, httpsrv, tftp, proxydhcp), where everything testable lives.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/donaldgifford/booty/catalog"
	"github.com/donaldgifford/booty/httpsrv"
	"github.com/donaldgifford/booty/proxydhcp"
	"github.com/donaldgifford/booty/render"
	"github.com/donaldgifford/booty/tftp"
)

// Build metadata, injected via -ldflags at release time (see CLAUDE.md).
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

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

func usage(w io.Writer) {
	fmt.Fprint(w, `booty — network-boot service

Usage:
  booty <command> [flags]

Commands:
  serve      Run the HTTP and TFTP servers
  validate   Load a catalog directory and report errors (CI gate)
  version    Print version information
  help       Show this help

Run "booty serve -h" for serve flags.
`)
}

// cmdValidate loads a catalog and exits non-zero on any error. It is the gate a
// config repo runs in CI: "does this catalog parse and resolve?".
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

// serveConfig is the parsed --serve flag set. It exists so cmdServe reads as
// wiring rather than as a wall of flag declarations, and so the flags can be
// validated in one place before anything binds a port.
type serveConfig struct {
	httpAddr          string
	tftpAddr          string
	bootDir           string
	catalogDir        string
	baseURL           string
	logFormat         string
	templatesDir      string
	enableProxyDHCP   bool
	proxyDHCPAddr     string
	proxyDHCPBINLAddr string
	serverIP          string
	proxmoxToken      string
}

// parseServeFlags parses args into a serveConfig. A parse failure has already
// been reported by the flag package, so the error is only a signal to exit.
func parseServeFlags(args []string) (*serveConfig, error) {
	var c serveConfig
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.StringVar(&c.httpAddr, "http-addr", "0.0.0.0:8080", "HTTP listen address")
	fs.StringVar(&c.tftpAddr, "tftp-addr", "0.0.0.0:69", "TFTP listen address")
	fs.StringVar(&c.bootDir, "boot-dir", "./boot", "directory of boot assets served over TFTP and HTTP")
	fs.StringVar(&c.catalogDir, "catalog", "", "catalog directory of *.hcl files")
	fs.StringVar(&c.baseURL, "url", "", "booty's externally reachable base URL (default: derived from request Host)")
	fs.StringVar(&c.logFormat, "log-format", "text", "log format: text or json")
	fs.StringVar(
		&c.templatesDir,
		"templates-dir",
		"",
		"directory of template overrides layered over the embedded set (family subdirs, e.g. talos/worker.yaml.tmpl)",
	)
	fs.BoolVar(
		&c.enableProxyDHCP,
		"proxydhcp",
		false,
		"run a PXE proxyDHCP responder (coexists with an existing DHCP server)",
	)
	fs.StringVar(&c.proxyDHCPAddr, "proxydhcp-addr", "0.0.0.0:67", "proxyDHCP listen address")
	fs.StringVar(
		&c.proxyDHCPBINLAddr,
		"proxydhcp-binl-addr",
		"0.0.0.0:4011",
		"proxyDHCP boot-server (BINL) listen address",
	)
	fs.StringVar(
		&c.serverIP,
		"server-ip",
		"",
		"booty's IPv4 advertised to PXE clients as the boot server (required with --proxydhcp)",
	)
	fs.StringVar(
		&c.proxmoxToken,
		"proxmox-token",
		"",
		"name:secret bearer token required on POST /proxmox/answer (match prepare-iso --answer-auth-token)",
	)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return &c, nil
}

func cmdServe(args []string) int {
	c, err := parseServeFlags(args)
	if err != nil {
		return 2
	}

	logger := newLogger(c.logFormat)
	slog.SetDefault(logger)

	if err := checkBaseURL(c.baseURL); err != nil {
		logger.Error("invalid --url", "err", err,
			"hint", "give an absolute URL machines can reach, e.g. http://192.168.1.10:8080")
		return 2
	}

	// A SIGINT/SIGTERM cancels ctx, which each server observes to shut down.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cat, ok := loadCatalog(ctx, c.catalogDir, logger)
	if !ok {
		return 1
	}

	renderer, err := newRenderer(c.templatesDir)
	if err != nil {
		logger.Error("render init failed", "err", err)
		return 1
	}

	proxy, ok := newProxyDHCP(c.enableProxyDHCP, c.serverIP, logger)
	if !ok {
		return 1
	}

	logger.Info("booty starting",
		"version", version,
		"http_addr", c.httpAddr,
		"tftp_addr", c.tftpAddr,
		"boot_dir", c.bootDir,
		"proxydhcp", c.enableProxyDHCP,
	)

	var wg sync.WaitGroup
	errc := make(chan error, 3)

	httpServer := httpsrv.New(httpsrv.Options{
		Logger:           logger,
		Catalog:          cat,
		Renderer:         renderer,
		BootDir:          c.bootDir,
		BaseURL:          c.baseURL,
		ProxmoxAuthToken: c.proxmoxToken,
	})
	wg.Go(func() {
		if err := httpServer.ListenAndServe(ctx, c.httpAddr); err != nil {
			errc <- fmt.Errorf("http: %w", err)
		}
	})
	wg.Go(func() {
		if err := tftp.New(c.bootDir, logger).ListenAndServe(ctx, c.tftpAddr); err != nil {
			errc <- fmt.Errorf("tftp: %w", err)
		}
	})
	if proxy != nil {
		wg.Go(func() {
			if err := proxy.ListenAndServe(ctx, c.proxyDHCPAddr, c.proxyDHCPBINLAddr); err != nil {
				errc <- fmt.Errorf("proxydhcp: %w", err)
			}
		})
	}

	// Wake on either a shutdown signal or the first fatal server error; in the
	// error case cancel ctx so the healthy server also drains.
	var runErr error
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case runErr = <-errc:
		logger.Error("server error", "err", runErr)
		stop()
	}

	wg.Wait()
	if runErr != nil {
		return 1
	}
	logger.Info("booty stopped")
	return 0
}

// loadCatalog loads and validates the catalog at boot so a broken catalog fails
// fast rather than at the first request. An empty dir is fine: booty still
// serves health, the chain script, and boot assets — just not per-machine
// scripts (httpsrv gates those routes on a non-nil catalog).
func loadCatalog(ctx context.Context, dir string, logger *slog.Logger) (*catalog.Catalog, bool) {
	if dir == "" {
		return nil, true
	}
	src := catalog.DirSource{Root: dir}
	cat, err := src.Load(ctx)
	if err != nil {
		logger.Error("catalog load failed", "source", src.String(), "err", err)
		return nil, false
	}
	logger.Info("catalog loaded", "source", src.String(),
		"profiles", len(cat.Profiles), "groups", len(cat.Groups))
	return cat, true
}

// newProxyDHCP builds the opt-in proxyDHCP responder, which needs booty's own
// IP to advertise as the boot server. It is built before anything starts so a
// misconfiguration fails fast.
func newProxyDHCP(enabled bool, serverIP string, logger *slog.Logger) (*proxydhcp.Server, bool) {
	if !enabled {
		return nil, true
	}
	proxy, err := proxydhcp.New(proxydhcp.Config{ServerIP: serverIP, Logger: logger})
	if err != nil {
		logger.Error("proxydhcp init failed", "err", err,
			"hint", "set --server-ip to booty's reachable IPv4")
		return nil, false
	}
	return proxy, true
}

// checkBaseURL rejects a --url that machines cannot chain to.
//
// Everything booty generates — the iPXE chain script, kernel and initrd paths,
// the config endpoints — is this string with a path glued on. A value that is
// not an absolute URL still starts the server and still answers 200; the damage
// only shows up as machines failing to boot, with nothing in booty's log to
// connect it to a typo made days earlier.
//
// Parsing alone is not enough to catch that. url.Parse does reject
// "192.168.1.10:8080", but a bare "boot.example.com" parses happily as a
// relative path — empty scheme, empty host — which is why the scheme and host
// are checked explicitly rather than trusting err.
//
// Empty is fine and is the default — it means "derive the base from each
// request's Host header", which is what a single-subnet setup wants.
func checkBaseURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parsing %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%q needs an http:// or https:// scheme", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("%q has no host", raw)
	}
	return nil
}

// newRenderer builds the renderer, layering an operator template-override
// directory (if given) over the embedded set.
func newRenderer(templatesDir string) (*render.Renderer, error) {
	var opts []render.Option
	if templatesDir != "" {
		opts = append(opts, render.WithTemplates(os.DirFS(templatesDir)))
	}
	return render.New(opts...)
}

func newLogger(format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
