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

func cmdServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	httpAddr := fs.String("http-addr", "0.0.0.0:8080", "HTTP listen address")
	tftpAddr := fs.String("tftp-addr", "0.0.0.0:69", "TFTP listen address")
	bootDir := fs.String("boot-dir", "./boot", "directory of boot assets served over TFTP and HTTP")
	catalogDir := fs.String("catalog", "", "catalog directory of *.hcl files")
	baseURL := fs.String("url", "", "booty's externally reachable base URL (default: derived from request Host)")
	logFormat := fs.String("log-format", "text", "log format: text or json")
	templatesDir := fs.String(
		"templates-dir",
		"",
		"directory of template overrides layered over the embedded set (family subdirs, e.g. talos/worker.yaml.tmpl)",
	)
	enableProxyDHCP := fs.Bool("proxydhcp", false, "run a PXE proxyDHCP responder (coexists with an existing DHCP server)")
	proxyDHCPAddr := fs.String("proxydhcp-addr", "0.0.0.0:67", "proxyDHCP listen address")
	proxyDHCPBINLAddr := fs.String("proxydhcp-binl-addr", "0.0.0.0:4011", "proxyDHCP boot-server (BINL) listen address")
	serverIP := fs.String("server-ip", "", "booty's IPv4 advertised to PXE clients as the boot server (required with --proxydhcp)")
	proxmoxToken := fs.String(
		"proxmox-token",
		"",
		"name:secret bearer token required on POST /proxmox/answer (match prepare-iso --answer-auth-token)",
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	logger := newLogger(*logFormat)
	slog.SetDefault(logger)

	// A SIGINT/SIGTERM cancels ctx, which each server observes to shut down.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cat, ok := loadCatalog(ctx, *catalogDir, logger)
	if !ok {
		return 1
	}

	renderer, err := newRenderer(*templatesDir)
	if err != nil {
		logger.Error("render init failed", "err", err)
		return 1
	}

	proxy, ok := newProxyDHCP(*enableProxyDHCP, *serverIP, logger)
	if !ok {
		return 1
	}

	logger.Info("booty starting",
		"version", version,
		"http_addr", *httpAddr,
		"tftp_addr", *tftpAddr,
		"boot_dir", *bootDir,
		"proxydhcp", *enableProxyDHCP,
	)

	var wg sync.WaitGroup
	errc := make(chan error, 3)

	httpServer := httpsrv.New(httpsrv.Options{
		Logger:           logger,
		Catalog:          cat,
		Renderer:         renderer,
		BootDir:          *bootDir,
		BaseURL:          *baseURL,
		ProxmoxAuthToken: *proxmoxToken,
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
	if proxy != nil {
		wg.Go(func() {
			if err := proxy.ListenAndServe(ctx, *proxyDHCPAddr, *proxyDHCPBINLAddr); err != nil {
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
