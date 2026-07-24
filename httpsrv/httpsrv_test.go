package httpsrv

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/donaldgifford/booty/catalog"
	"github.com/donaldgifford/booty/render"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// bootCatalog: one bootable worker profile pinned to a MAC, no catch-all.
func bootCatalog() *catalog.Catalog {
	return &catalog.Catalog{
		Profiles: map[string]catalog.Profile{
			"talos-worker": {
				Name: "talos-worker",
				Boot: &catalog.Boot{
					Kernel:  "talos/v1.7.6/vmlinuz",
					Initrd:  "talos/v1.7.6/initramfs.xz",
					Cmdline: []string{"talos.platform=metal"},
				},
			},
		},
		Groups: []catalog.Group{
			{Name: "worker-01", Profile: "talos-worker", Selector: map[string]string{"mac": "d0:50:99:b3:4c:50"}},
		},
	}
}

func newTestServer(t *testing.T, opts Options) http.Handler {
	t.Helper()
	if opts.Logger == nil {
		opts.Logger = quiet()
	}
	if opts.Renderer == nil {
		r, err := render.New()
		if err != nil {
			t.Fatalf("render.New: %v", err)
		}
		opts.Renderer = r
	}
	return New(opts).Handler()
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, http.NoBody))
	return rec
}

func TestHealthz(t *testing.T) {
	rec := get(t, New(Options{Logger: quiet()}).Handler(), "/healthz")
	if rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}

func TestChainEndpoint(t *testing.T) {
	h := newTestServer(t, Options{BaseURL: "http://booty.test:8080"})
	rec := get(t, h, "/boot.ipxe")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "#!ipxe") {
		t.Error("chain script must start with #!ipxe")
	}
	for _, want := range []string{"http://booty.test:8080/ipxe?", "mac=${mac}", "arch=${buildarch}"} {
		if !strings.Contains(body, want) {
			t.Errorf("chain script missing %q", want)
		}
	}
}

func TestIPXEMatch(t *testing.T) {
	h := newTestServer(t, Options{Catalog: bootCatalog(), BaseURL: "http://booty.test:8080"})
	// Non-canonical MAC in the query must still match the canonical selector.
	rec := get(t, h, "/ipxe?mac=D0-50-99-B3-4C-50&arch=x86_64")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "#!ipxe") {
		t.Fatalf("not an iPXE script:\n%s", body)
	}
	if !strings.Contains(body, "http://booty.test:8080/boot/talos/v1.7.6/vmlinuz") {
		t.Errorf("resolved script missing kernel URL:\n%s", body)
	}
}

func TestIPXENoMatchServesShell(t *testing.T) {
	// No catch-all: an unknown MAC must still get a 200 iPXE shell script, not a
	// 404, because iPXE handles non-200 poorly.
	h := newTestServer(t, Options{Catalog: bootCatalog()})
	rec := get(t, h, "/ipxe?mac=00:00:00:00:00:01")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.HasPrefix(rec.Body.String(), "#!ipxe") {
		t.Error("no-match response must still be an iPXE script")
	}
	if !strings.Contains(rec.Body.String(), "no catalog match") {
		t.Error("no-match script should explain itself")
	}
}

func TestIPXEBaseURLFromHost(t *testing.T) {
	// With no configured BaseURL, the handler derives it from the request Host.
	h := newTestServer(t, Options{Catalog: bootCatalog()})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ipxe?mac=d0:50:99:b3:4c:50", http.NoBody)
	req.Host = "10.0.0.5:8080"
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "http://10.0.0.5:8080/boot/") {
		t.Errorf("base URL not derived from Host:\n%s", rec.Body.String())
	}
}

func TestBootFileServed(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "talos", "v1.7.6"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "talos", "v1.7.6", "vmlinuz"), []byte("KERNELBYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newTestServer(t, Options{BootDir: dir})

	rec := get(t, h, "/boot/talos/v1.7.6/vmlinuz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Body.String() != "KERNELBYTES" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestBootFileTraversalBlocked(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(filepath.Dir(dir), "secret")
	_ = os.WriteFile(secret, []byte("nope"), 0o600)
	t.Cleanup(func() { _ = os.Remove(secret) })

	h := newTestServer(t, Options{BootDir: dir})
	rec := get(t, h, "/boot/../secret")
	// The cleaned path resolves under bootDir (a 404) rather than escaping it;
	// either way the secret's contents must not appear.
	if strings.Contains(rec.Body.String(), "nope") {
		t.Fatal("traversal leaked a file outside bootDir")
	}
}

// talosConfigCatalog: a profile that both boots and serves a machineconfig.
func talosConfigCatalog() *catalog.Catalog {
	return &catalog.Catalog{
		Profiles: map[string]catalog.Profile{
			"talos-worker": {
				Name:   "talos-worker",
				Boot:   &catalog.Boot{Kernel: "talos/v1.7.6/vmlinuz", Initrd: "talos/v1.7.6/initramfs.xz"},
				Render: &catalog.Render{Kind: "talos-machineconfig", Template: "talos/worker.yaml.tmpl"},
				Vars: map[string]string{
					"role": "worker", "cluster": "home", "talos_version": "v1.7.6",
					"cluster_endpoint": "https://10.0.0.1:6443", "hostname": "w1",
				},
			},
		},
		Groups: []catalog.Group{
			{Name: "w", Profile: "talos-worker", Selector: map[string]string{"mac": "d0:50:99:b3:4c:50"}},
		},
	}
}

func TestMachineConfig(t *testing.T) {
	h := newTestServer(t, Options{Catalog: talosConfigCatalog()})
	rec := get(t, h, "/machine-config?mac=d0:50:99:b3:4c:50")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "yaml") {
		t.Errorf("content-type = %q, want yaml", ct)
	}
	if !strings.Contains(rec.Body.String(), "type: worker") {
		t.Errorf("machineconfig body wrong:\n%s", rec.Body.String())
	}
}

func TestMachineConfigNoMatch(t *testing.T) {
	h := newTestServer(t, Options{Catalog: talosConfigCatalog()})
	if rec := get(t, h, "/machine-config?mac=00:00:00:00:00:02"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestMachineConfigWrongKind(t *testing.T) {
	// bootCatalog's profile boots but declares no machineconfig render → 409.
	h := newTestServer(t, Options{Catalog: bootCatalog()})
	if rec := get(t, h, "/machine-config?mac=d0:50:99:b3:4c:50"); rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

// cloudInitCatalog: an IP-selected cloud-init profile.
func cloudInitCatalog() *catalog.Catalog {
	return &catalog.Catalog{
		Profiles: map[string]catalog.Profile{
			"ubuntu": {
				Name:   "ubuntu",
				Render: &catalog.Render{Kind: "cloud-init", Template: "cloud_init/ubuntu.yaml.tmpl"},
				Vars:   map[string]string{"hostname": "ubuntu-01"},
			},
		},
		Groups: []catalog.Group{
			{Name: "u", Profile: "ubuntu", Selector: map[string]string{"ip": "192.168.1.50"}},
		},
	}
}

func fromIP(t *testing.T, h http.Handler, target, ip string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, http.NoBody)
	req.RemoteAddr = ip + ":54321"
	h.ServeHTTP(rec, req)
	return rec
}

func TestCloudInitUserData(t *testing.T) {
	h := newTestServer(t, Options{Catalog: cloudInitCatalog()})
	rec := fromIP(t, h, "/cloud-init/user-data", "192.168.1.50")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.HasPrefix(rec.Body.String(), "#cloud-config") {
		t.Errorf("user-data must start with #cloud-config:\n%s", rec.Body.String())
	}
}

func TestCloudInitMetaData(t *testing.T) {
	h := newTestServer(t, Options{Catalog: cloudInitCatalog()})
	rec := fromIP(t, h, "/cloud-init/meta-data", "192.168.1.50")
	if !strings.Contains(rec.Body.String(), "instance-id: booty-ubuntu-01") {
		t.Errorf("meta-data wrong:\n%s", rec.Body.String())
	}
}

func TestCloudInitVendorDataEmpty(t *testing.T) {
	h := newTestServer(t, Options{Catalog: cloudInitCatalog()})
	rec := fromIP(t, h, "/cloud-init/vendor-data", "192.168.1.50")
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("vendor-data = %d, %d bytes; want 200 empty", rec.Code, rec.Body.Len())
	}
}

func TestCloudInitNoMatch(t *testing.T) {
	h := newTestServer(t, Options{Catalog: cloudInitCatalog()})
	if rec := fromIP(t, h, "/cloud-init/user-data", "10.9.9.9"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for unknown IP", rec.Code)
	}
}

// proxmoxCatalog: a Proxmox host pinned by MAC, plus a catch-all — so the tests
// exercise the multi-NIC "most specific match" path, not just a lucky first NIC.
func proxmoxCatalog() *catalog.Catalog {
	return &catalog.Catalog{
		Profiles: map[string]catalog.Profile{
			"proxmox-host": {
				Name:   "proxmox-host",
				Render: &catalog.Render{Kind: "proxmox-answer", Template: "proxmox/answer.toml.tmpl"},
				Vars: map[string]string{
					"fqdn": "pve-01.home.local", "root_password_hashed": "$6$S$H",
				},
			},
			"rescue": {
				Name:   "rescue",
				Render: &catalog.Render{Kind: "ipxe", Template: "ipxe/rescue.ipxe"},
			},
		},
		Groups: []catalog.Group{
			{Name: "pve-01", Profile: "proxmox-host", Selector: map[string]string{"mac": "d0:50:99:d5:6e:72"}},
			{Name: "unknown", Profile: "rescue"}, // catch-all
		},
	}
}

// proxmoxSysInfoJSON mimics the installer's POST body: DMI system block plus a
// NIC list where the catalog-pinned MAC is deliberately NOT first.
const proxmoxSysInfoJSON = `{
  "dmi": {"system": {"uuid": "aaaa-bbbb", "serial": "PVE123", "name": "NUC13", "manufacturer": "Intel"}},
  "network_interfaces": [
    {"name": "eno1", "mac": "aa:bb:cc:00:11:22"},
    {"name": "eno2", "mac": "d0:50:99:d5:6e:72"}
  ]
}`

func postProxmox(t *testing.T, h http.Handler, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/proxmox/answer", strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(rec, req)
	return rec
}

func TestProxmoxAnswer(t *testing.T) {
	h := newTestServer(t, Options{Catalog: proxmoxCatalog()})
	rec := postProxmox(t, h, proxmoxSysInfoJSON, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "toml") {
		t.Errorf("content-type = %q, want toml", ct)
	}
	// The pinned MAC was the SECOND NIC; the catch-all must not have won.
	if !strings.Contains(rec.Body.String(), `fqdn = "pve-01.home.local"`) {
		t.Errorf("answer body wrong:\n%s", rec.Body.String())
	}
}

func TestProxmoxAnswerNoMatch(t *testing.T) {
	// Without the catch-all, unknown NICs are a clean 404.
	cat := proxmoxCatalog()
	cat.Groups = cat.Groups[:1] // drop the catch-all
	h := newTestServer(t, Options{Catalog: cat})
	body := `{"network_interfaces": [{"name": "eno1", "mac": "00:00:00:00:00:01"}]}`
	if rec := postProxmox(t, h, body, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestProxmoxAnswerWrongKind(t *testing.T) {
	// With ONLY the catch-all matching, the resolved profile is the iPXE rescue —
	// not an answer file — and that must be a 409, not rescue bytes as TOML.
	h := newTestServer(t, Options{Catalog: proxmoxCatalog()})
	body := `{"network_interfaces": [{"name": "eno1", "mac": "00:00:00:00:00:01"}]}`
	if rec := postProxmox(t, h, body, nil); rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestProxmoxAnswerAuth(t *testing.T) {
	h := newTestServer(t, Options{Catalog: proxmoxCatalog(), ProxmoxAuthToken: "booty:s3cret"})
	if rec := postProxmox(t, h, proxmoxSysInfoJSON, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", rec.Code)
	}
	if rec := postProxmox(t, h, proxmoxSysInfoJSON,
		map[string]string{"Authorization": "Bearer wrong"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status = %d, want 401", rec.Code)
	}
	if rec := postProxmox(t, h, proxmoxSysInfoJSON,
		map[string]string{"Authorization": "Bearer booty:s3cret"}); rec.Code != http.StatusOK {
		t.Fatalf("right token: status = %d, want 200", rec.Code)
	}
}

func TestProxmoxAnswerBadJSON(t *testing.T) {
	h := newTestServer(t, Options{Catalog: proxmoxCatalog()})
	if rec := postProxmox(t, h, "not json", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestProxmoxAnswerGETNotAllowed(t *testing.T) {
	// The installer POSTs; the route is registered POST-only, so GET is a 405
	// from the mux (the classic static-file-server mistake, inverted).
	h := newTestServer(t, Options{Catalog: proxmoxCatalog()})
	if rec := get(t, h, "/proxmox/answer"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", rec.Code)
	}
}

func TestRoutesGatedByDeps(t *testing.T) {
	// A health-only server (no catalog/renderer/bootDir) must not expose /ipxe.
	h := New(Options{Logger: quiet()}).Handler()
	if rec := get(t, h, "/ipxe?mac=x"); rec.Code != http.StatusNotFound {
		t.Fatalf("/ipxe without catalog = %d, want 404", rec.Code)
	}
}
