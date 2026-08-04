package httpsrv

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/donaldgifford/booty/catalog"
	"github.com/donaldgifford/booty/render"
)

// Config configures a Server. Catalog/Renderer enable the iPXE endpoints;
// BootDir enables the /boot asset endpoint. Any of them may be zero, in which
// case the corresponding routes are simply not registered (health always is).
type Config struct {
	Logger   *slog.Logger
	Catalog  *catalog.Catalog
	Renderer *render.Renderer
	BootDir  string
	// BaseURL is booty's externally reachable base (e.g. http://192.168.1.10:8080).
	// If empty, it is derived per-request from the Host header.
	BaseURL string
	// ProxmoxAuthToken, when set, is required as `Authorization: Bearer <token>`
	// on POST /proxmox/answer. Use the same name:secret value passed to
	// `proxmox-auto-install-assistant prepare-iso --answer-auth-token`.
	ProxmoxAuthToken string
}

// Server owns the HTTP listener and its dependencies. Construct with New: a
// zero Server is not usable and its handlers panic on a nil logger.
type Server struct {
	logger       *slog.Logger
	catalog      *catalog.Catalog
	renderer     *render.Renderer
	bootDir      string
	baseURL      string
	proxmoxToken string
}

// ErrInvalidBaseURL is returned by New when [Config.BaseURL] is set to
// something that cannot be used as a URL prefix.
var ErrInvalidBaseURL = errors.New("invalid BaseURL")

// New returns a Server. A nil logger falls back to slog.Default.
//
// It errors only on an unusable BaseURL. That is worth an error return because
// BaseURL is not consumed here — it is glued to a path and handed to machines
// that are mid-boot with no way to report back, so a bad value produces a
// server that starts, answers 200, and quietly emits scripts nothing can
// follow. Every other field is either optional by design (a nil Catalog just
// means those routes are not registered) or fails visibly at first use.
func New(cfg Config) (*Server, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if err := checkBaseURL(cfg.BaseURL); err != nil {
		return nil, err
	}
	return &Server{
		logger:       logger,
		catalog:      cfg.Catalog,
		renderer:     cfg.Renderer,
		bootDir:      cfg.BootDir,
		baseURL:      strings.TrimRight(cfg.BaseURL, "/"),
		proxmoxToken: cfg.ProxmoxAuthToken,
	}, nil
}

// checkBaseURL rejects a BaseURL that machines cannot fetch from. Empty is
// valid and is the default: it means "derive the base from each request's Host
// header", which is what a single-subnet deployment wants.
//
// Parsing alone is not enough. url.Parse rejects "192.168.1.10:8080" for the
// colon in its first path segment, but a bare "boot.example.com" parses
// happily as a relative path with no scheme and no host — so both are checked
// explicitly rather than trusting err.
func checkBaseURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w %q: %w", ErrInvalidBaseURL, raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w %q: needs an http:// or https:// scheme", ErrInvalidBaseURL, raw)
	}
	if u.Host == "" {
		return fmt.Errorf("%w %q: no host", ErrInvalidBaseURL, raw)
	}
	return nil
}

// Handler builds the routed, middleware-wrapped http.Handler. Routes light up
// based on which dependencies are configured, so a health-only server and a full
// boot server share one construction path.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleHealthz)

	if s.renderer != nil {
		// The chainload script only needs the renderer; it carries no per-machine
		// state, so it works even before a catalog is loaded.
		mux.HandleFunc("GET /boot.ipxe", s.handleChain)
	}
	if s.catalog != nil && s.renderer != nil {
		mux.HandleFunc("GET /ipxe", s.handleIPXE)
		// Talos fetches its machineconfig here (talos.config= in the boot cmdline).
		mux.HandleFunc("GET /machine-config", s.handleMachineConfig)
		// cloud-init NoCloud data source (the secondary, non-Talos path).
		mux.HandleFunc("GET /cloud-init/meta-data", s.handleCloudInitMetaData)
		mux.HandleFunc("GET /cloud-init/user-data", s.handleCloudInitUserData)
		mux.HandleFunc("GET /cloud-init/vendor-data", s.handleCloudInitVendorData)
		// The Proxmox automated installer POSTs its system info here and gets the
		// machine's answer.toml back.
		mux.HandleFunc("POST /proxmox/answer", s.handleProxmoxAnswer)
	}
	if s.bootDir != "" {
		mux.HandleFunc("GET /boot/{path...}", s.handleBoot)
	}
	return s.logRequests(mux)
}

// ListenAndServe serves on addr until ctx is cancelled, then drains in-flight
// requests within a bounded grace period. The large WriteTimeout accommodates
// multi-hundred-megabyte kernel/initrd downloads on /boot.
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
		s.logger.Info("HTTP listening", "addr", addr)
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

func (*Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

// handleChain serves the chainloading iPXE script (the DHCP option-175 target /
// the script embedded in ipxe.efi). It is what makes /ipxe?mac=... work.
func (s *Server) handleChain(w http.ResponseWriter, r *http.Request) {
	script, err := s.renderer.ChainScript(s.effectiveBaseURL(r))
	if err != nil {
		s.logger.Error("chain render failed", "err", err)
		writeIPXE(w, errorScript("could not render chain script"))
		return
	}
	writeIPXE(w, script)
}

// handleIPXE is the core boot handler: derive identity from the query the chain
// script supplied, match it to a profile, and render that profile's boot script.
func (s *Server) handleIPXE(w http.ResponseWriter, r *http.Request) {
	id, err := identityFromQuery(r.URL.Query())
	if err != nil {
		// Still a script, not a status code: iPXE handles non-200 badly.
		s.logger.Warn("ipxe: rejected identity", "remote", r.RemoteAddr, "err", err)
		writeIPXE(w, errorScript("invalid identity"))
		return
	}
	s.logger.Info("ipxe request", "mac", id.MAC, "ip", id.IP, "arch", id.Arch, "remote", r.RemoteAddr)

	res, err := s.catalog.Match(id)
	if err != nil {
		// Always a shell script, never a non-200: iPXE handles non-200 /
		// non-#!ipxe responses poorly on some firmware. But the two failures
		// need opposite remedies, so they must not share a message.
		if errors.Is(err, catalog.ErrUnknownProfile) {
			s.logger.Error("catalog is broken", "mac", id.MAC, "err", err)
			writeIPXE(w, errorScript(err.Error()))
			return
		}
		s.logger.Warn("no catalog match", "mac", id.MAC, "err", err)
		writeIPXE(w, noMatchScript(id))
		return
	}

	script, err := s.renderer.IPXEScript(id, res, s.effectiveBaseURL(r))
	if err != nil {
		s.logger.Error("ipxe render failed", "err", err, "profile", res.Profile.Name, "group", res.Group)
		writeIPXE(w, errorScript("render failed for profile "+res.Profile.Name))
		return
	}
	s.logger.Info("ipxe resolved", "mac", id.MAC, "group", res.Group, "profile", res.Profile.Name)
	writeIPXE(w, script)
}

// handleBoot serves kernel/initrd (and any other boot asset) from bootDir. Path
// traversal is neutralized the same way the TFTP server does it.
func (s *Server) handleBoot(w http.ResponseWriter, r *http.Request) {
	rel := filepath.Clean("/" + r.PathValue("path"))
	full := filepath.Join(s.bootDir, rel)

	bootAbs, err := filepath.Abs(s.bootDir)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	fullAbs, err := filepath.Abs(full)
	if err != nil || (fullAbs != bootAbs && !strings.HasPrefix(fullAbs, bootAbs+string(filepath.Separator))) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// Boot assets are files. Without this, GET /boot/ resolves to bootDir itself
	// and http.ServeFile falls through to its HTML directory index, enumerating
	// every asset and subdirectory on the server. 404 rather than 403 so the
	// response does not confirm what is a directory.
	info, err := os.Stat(fullAbs)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	// http.ServeFile gives us Range requests, ETag/Last-Modified, HEAD, and
	// sendfile-backed streaming for the large files — everything a boot needs.
	http.ServeFile(w, r, fullAbs)
}

// handleMachineConfig serves a machine's Talos machineconfig. Talos fetches it
// from the talos.config= URL in the boot cmdline, substituting ${mac} etc., so
// identity arrives as query parameters (same as /ipxe). Unlike /ipxe, ordinary
// HTTP status codes are fine here — Talos retries on error.
func (s *Server) handleMachineConfig(w http.ResponseWriter, r *http.Request) {
	id, err := identityFromQuery(r.URL.Query())
	if err != nil {
		s.logger.Warn("machine-config: rejected identity", "remote", r.RemoteAddr, "err", err)
		http.Error(w, "invalid identity", http.StatusBadRequest)
		return
	}
	res, err := s.catalog.Match(id)
	if err != nil {
		if errors.Is(err, catalog.ErrUnknownProfile) {
			// The machine is fine; the catalog is not. 500, not 404, so the
			// operator sees a server fault rather than an unknown machine.
			s.logger.Error("machine-config: catalog is broken", "mac", id.MAC, "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.logger.Warn("machine-config: no match", "mac", id.MAC, "err", err)
		http.Error(w, "no machine config for this machine", http.StatusNotFound)
		return
	}
	if res.Profile.Render == nil || res.Profile.Render.Kind != "talos-machineconfig" {
		http.Error(w, "profile "+res.Profile.Name+" is not a talos machineconfig", http.StatusConflict)
		return
	}
	out, err := s.renderer.Config(id, res, s.effectiveBaseURL(r))
	if err != nil {
		s.logger.Error("machine-config render failed", "err", err, "profile", res.Profile.Name)
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	s.logger.Info("machine-config served", "mac", id.MAC, "group", res.Group, "profile", res.Profile.Name)
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_, _ = io.WriteString(w, out)
}

// proxmoxSysInfo is the subset of the JSON the Proxmox automated installer
// POSTs that booty matches on: DMI system identity plus every NIC's MAC.
type proxmoxSysInfo struct {
	DMI struct {
		System struct {
			UUID         string `json:"uuid"`
			Serial       string `json:"serial"`
			Name         string `json:"name"`
			Manufacturer string `json:"manufacturer"`
		} `json:"system"`
	} `json:"dmi"`
	NICs []struct {
		Name string `json:"name"`
		MAC  string `json:"mac"`
	} `json:"network_interfaces"`
}

// handleProxmoxAnswer serves a machine's Proxmox answer.toml. Unlike /ipxe
// (identity in the query) and /cloud-init (source IP only), the Proxmox
// installer sends identity in the POST body — the richest of the three models:
// full DMI plus all NICs. Ordinary status codes are fine; a failed fetch aborts
// the install visibly on the machine's console.
func (s *Server) handleProxmoxAnswer(w http.ResponseWriter, r *http.Request) {
	if s.proxmoxToken != "" {
		want := "Bearer " + s.proxmoxToken
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "reading body", http.StatusBadRequest)
		return
	}
	var info proxmoxSysInfo
	if err := json.Unmarshal(body, &info); err != nil {
		http.Error(w, "invalid system info JSON", http.StatusBadRequest)
		return
	}

	// A host has several NICs and the catalog selector typically names one MAC,
	// so try each NIC as the identity's MAC and keep the most specific match —
	// otherwise a catch-all group would win on whichever NIC happens to be first.
	base := catalog.Identity{
		UUID:         info.DMI.System.UUID,
		Serial:       info.DMI.System.Serial,
		Product:      info.DMI.System.Name,
		Manufacturer: info.DMI.System.Manufacturer,
	}
	if err := validateIdentity(base); err != nil {
		s.logger.Warn("proxmox: rejected identity", "remote", r.RemoteAddr, "err", err)
		http.Error(w, "invalid identity", http.StatusBadRequest)
		return
	}
	macs := make([]string, 0, len(info.NICs))
	for _, nic := range info.NICs {
		if strings.ContainsFunc(nic.MAC, isControl) {
			s.logger.Warn("proxmox: rejected NIC MAC", "remote", r.RemoteAddr)
			http.Error(w, "invalid identity", http.StatusBadRequest)
			return
		}
		macs = append(macs, nic.MAC)
	}
	if len(macs) == 0 {
		macs = []string{""} // DMI-only identity still gets one match attempt
	}
	id, res := s.mostSpecificMatch(base, macs)
	if res == nil {
		s.logger.Warn("proxmox-answer: no match", "uuid", base.UUID, "serial", base.Serial, "macs", macs)
		http.Error(w, "no answer file for this machine", http.StatusNotFound)
		return
	}
	if res.Profile.Render == nil || res.Profile.Render.Kind != "proxmox-answer" {
		http.Error(w, "profile "+res.Profile.Name+" is not a proxmox answer", http.StatusConflict)
		return
	}
	out, err := s.renderer.Config(id, res, s.effectiveBaseURL(r))
	if err != nil {
		s.logger.Error("proxmox-answer render failed", "err", err, "profile", res.Profile.Name)
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	s.logger.Info("proxmox-answer served", "mac", id.MAC, "group", res.Group, "profile", res.Profile.Name)
	w.Header().Set("Content-Type", "application/toml; charset=utf-8")
	_, _ = io.WriteString(w, out)
}

// mostSpecificMatch tries base with each candidate MAC and returns the
// resolution whose matched group has the most selector terms. This keeps a
// multi-NIC host matching its pinned group even when a catch-all exists.
func (s *Server) mostSpecificMatch(base catalog.Identity, macs []string) (catalog.Identity, *catalog.Resolution) {
	var bestID catalog.Identity
	var best *catalog.Resolution
	bestTerms := -1
	for _, mac := range macs {
		id := base
		id.MAC = mac
		res, err := s.catalog.Match(id)
		if err != nil {
			continue
		}
		if res.Specificity > bestTerms {
			bestID, best, bestTerms = id, res, res.Specificity
		}
	}
	return bestID, best
}

// cloud-init NoCloud identifies a machine only by its source IP — it sends no
// identifiers in the request. So these handlers match on the client address,
// which means cloud-init profiles need ip selectors in the catalog.
func (s *Server) handleCloudInitMetaData(w http.ResponseWriter, r *http.Request) {
	id, res, ok := s.cloudInitResolve(w, r)
	if !ok {
		return
	}
	out, err := s.renderer.CloudInitMetaData(id, res, s.effectiveBaseURL(r))
	if err != nil {
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	_, _ = io.WriteString(w, out)
}

func (s *Server) handleCloudInitUserData(w http.ResponseWriter, r *http.Request) {
	id, res, ok := s.cloudInitResolve(w, r)
	if !ok {
		return
	}
	if res.Profile.Render == nil || res.Profile.Render.Kind != "cloud-init" {
		http.Error(w, "profile "+res.Profile.Name+" is not cloud-init", http.StatusConflict)
		return
	}
	out, err := s.renderer.Config(id, res, s.effectiveBaseURL(r))
	if err != nil {
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	// user-data must start with a recognized header (#cloud-config, #!, ...).
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, out)
}

func (*Server) handleCloudInitVendorData(w http.ResponseWriter, _ *http.Request) {
	// Optional layer; a 200 with an empty body is the least surprising answer.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
}

// cloudInitResolve matches the requesting machine by source IP and writes a 404
// on no match, returning ok=false so the caller stops.
func (s *Server) cloudInitResolve(w http.ResponseWriter, r *http.Request) (catalog.Identity, *catalog.Resolution, bool) {
	id := catalog.Identity{IP: clientIP(r)}
	res, err := s.catalog.Match(id)
	if err != nil {
		if errors.Is(err, catalog.ErrUnknownProfile) {
			s.logger.Error("cloud-init: catalog is broken", "ip", id.IP, "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return id, nil, false
		}
		s.logger.Warn("cloud-init: no match", "ip", id.IP, "err", err)
		http.Error(w, "no data for this instance", http.StatusNotFound)
		return id, nil, false
	}
	return id, res, true
}

// clientIP extracts the source IP from RemoteAddr (host:port).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// effectiveBaseURL returns the configured base URL, or one derived from the
// request when none was configured, so booty works without mandatory config.
func (s *Server) effectiveBaseURL(r *http.Request) string {
	if s.baseURL != "" {
		return s.baseURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// identityFromQuery maps the chain script's query parameters onto an Identity.
// identityFromQuery reads a machine's identity attributes from the query string.
//
// It rejects values containing control characters. These strings are
// interpolated into the YAML and TOML documents render produces, text/template
// performs no escaping, and an embedded newline therefore lets a machine append
// keys the operator never authored — root_ssh_keys in a Proxmox answer file, or
// an install block in a Talos machineconfig. Since booty exists to decide
// centrally what a machine installs, letting the machine edit its own answer
// defeats the point. No legitimate MAC, UUID, serial, hostname or product
// string contains a control character.
func identityFromQuery(q url.Values) (catalog.Identity, error) {
	id := catalog.Identity{
		MAC:          q.Get("mac"),
		UUID:         q.Get("uuid"),
		Serial:       q.Get("serial"),
		Hostname:     q.Get("hostname"),
		IP:           q.Get("ip"),
		Arch:         normalizeArch(q.Get("arch")),
		Product:      q.Get("product"),
		Manufacturer: q.Get("manufacturer"),
	}
	return id, validateIdentity(id)
}

// validateIdentity rejects identity values containing control characters. See
// identityFromQuery for why. It applies to every source of identity, not just
// the query string: the Proxmox installer supplies its DMI strings and NIC MACs
// in a POST body, which is no more trustworthy.
func validateIdentity(id catalog.Identity) error {
	for _, f := range []struct{ name, value string }{
		{"mac", id.MAC},
		{"uuid", id.UUID},
		{"serial", id.Serial},
		{"hostname", id.Hostname},
		{"ip", id.IP},
		{"product", id.Product},
		{"manufacturer", id.Manufacturer},
	} {
		if strings.ContainsFunc(f.value, isControl) {
			return fmt.Errorf("identity field %q contains a control character", f.name)
		}
	}
	return nil
}

// isControl reports whether r is a C0/C1 control character — the class that lets
// a value escape the line it was rendered on.
func isControl(r rune) bool {
	return r < 0x20 || (r >= 0x7f && r <= 0x9f)
}

// normalizeArch maps iPXE's ${buildarch} spellings to the values catalog
// selectors use.
func normalizeArch(a string) string {
	switch strings.ToLower(strings.TrimSpace(a)) {
	case "x86_64", "amd64":
		return "x86_64"
	case "arm64", "aarch64":
		return "arm64"
	case "i386", "i686", "x86":
		return "i386"
	default:
		return strings.ToLower(a)
	}
}

func writeIPXE(w http.ResponseWriter, script string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK) // iPXE expects 200 even for our "error" scripts
	_, _ = io.WriteString(w, script)
}

func noMatchScript(id catalog.Identity) string {
	return "#!ipxe\n" +
		"echo booty: no catalog match for mac=" + id.MAC + "\n" +
		"echo Add a group for this machine, then reboot.\n" +
		"shell\n"
}

func errorScript(reason string) string {
	return "#!ipxe\n" +
		"echo booty: internal error: " + reason + "\n" +
		"shell\n"
}

// logRequests logs one structured line per request, capturing the status code
// via a wrapped ResponseWriter (net/http does not expose it otherwise).
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
