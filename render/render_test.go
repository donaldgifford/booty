package render

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/donaldgifford/booty/catalog"
)

var update = flag.Bool("update", false, "update golden files")

// assertGolden compares got against testdata/<name>, or rewrites it when -update
// is passed. Golden files make "what exactly does node X boot?" a reviewable diff.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read golden (run: go test ./internal/render -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("output differs from %s:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func newRenderer(t *testing.T) *Renderer {
	t.Helper()
	r, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func talosWorkerResolution() (catalog.Identity, *catalog.Resolution) {
	id := catalog.Identity{MAC: "d0:50:99:b3:4c:50", Arch: "x86_64"}
	res := &catalog.Resolution{
		Group: "worker-01",
		Profile: catalog.Profile{
			Name: "talos-worker",
			Boot: &catalog.Boot{
				Kernel: "talos/v1.7.6/vmlinuz",
				Initrd: "talos/v1.7.6/initramfs.xz",
				Cmdline: []string{
					"console=ttyS0,115200n8",
					"console=tty0",
					"talos.platform=metal",
					"talos.config=http://boot.home.local:8080/machine-config?mac=${mac}",
				},
			},
		},
		Vars: map[string]string{"hostname": "talos-worker-01", "role": "worker"},
	}
	return id, res
}

func TestIPXEBootScript(t *testing.T) {
	r := newRenderer(t)
	id, res := talosWorkerResolution()

	got, err := r.IPXEScript(id, res, "http://192.168.1.10:8080")
	if err != nil {
		t.Fatalf("IPXEScript: %v", err)
	}
	assertGolden(t, "boot-talos-worker.ipxe.golden", got)

	// Invariants that must hold regardless of golden churn:
	if !strings.HasPrefix(got, "#!ipxe\n") {
		t.Error("script must start with #!ipxe")
	}
	if !strings.Contains(got, "http://192.168.1.10:8080/boot/talos/v1.7.6/vmlinuz") {
		t.Error("kernel URL missing or wrong")
	}
	if !strings.Contains(got, "initrd=initramfs.xz") {
		t.Error("kernel cmdline should carry initrd basename for Talos")
	}
	if !strings.Contains(got, "mac=${mac}") {
		t.Error("literal iPXE ${mac} must pass through untouched")
	}
}

func TestIPXERescueScript(t *testing.T) {
	r := newRenderer(t)
	id := catalog.Identity{MAC: "aa:bb:cc:dd:ee:ff", Product: "PowerEdge R640", Manufacturer: "Dell Inc.", Serial: "ABC123"}
	res := &catalog.Resolution{
		Group:   "unknown",
		Profile: catalog.Profile{Name: "rescue", Render: &catalog.Render{Kind: "ipxe", Template: "ipxe/rescue.ipxe"}},
		Vars:    map[string]string{},
	}

	got, err := r.IPXEScript(id, res, "http://192.168.1.10:8080")
	if err != nil {
		t.Fatalf("IPXEScript: %v", err)
	}
	assertGolden(t, "rescue.ipxe.golden", got)
	if !strings.Contains(got, "PowerEdge R640") {
		t.Error("rescue script should surface identity for the operator")
	}
}

func TestChainScript(t *testing.T) {
	r := newRenderer(t)
	got, err := r.ChainScript("http://192.168.1.10:8080")
	if err != nil {
		t.Fatalf("ChainScript: %v", err)
	}
	assertGolden(t, "chain.ipxe.golden", got)

	// The whole point: the chain script is what carries identity to /ipxe.
	for _, want := range []string{"mac=${mac}", "uuid=${uuid}", "arch=${buildarch}", "/ipxe?"} {
		if !strings.Contains(got, want) {
			t.Errorf("chain script missing %q", want)
		}
	}
}

func talosResolution(name, role, tmpl, hostname string) *catalog.Resolution {
	return &catalog.Resolution{
		Group:   name + "-grp",
		Profile: catalog.Profile{Name: name, Render: &catalog.Render{Kind: "talos-machineconfig", Template: tmpl}},
		Vars: map[string]string{
			"role":             role,
			"cluster":          "home",
			"talos_version":    "v1.7.6",
			"cluster_endpoint": "https://192.168.1.100:6443",
			"hostname":         hostname,
		},
	}
}

func TestTalosWorkerConfig(t *testing.T) {
	r := newRenderer(t)
	res := talosResolution("talos-worker", "worker", "talos/worker.yaml.tmpl", "talos-worker-01")
	got, err := r.Config(catalog.Identity{MAC: "d0:50:99:b3:4c:50"}, res, "http://booty")
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	assertGolden(t, "talos-worker.yaml.golden", got)

	for _, want := range []string{
		"type: worker", "hostname: talos-worker-01",
		"installer:v1.7.6", "clusterName: home", "endpoint: https://192.168.1.100:6443",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("machineconfig missing %q", want)
		}
	}
	// The shared machine: partial must have been composed in.
	if !strings.Contains(got, "kubelet:") {
		t.Error("shared talos-machine partial not composed")
	}
}

func TestTalosControlPlaneConfig(t *testing.T) {
	r := newRenderer(t)
	res := talosResolution("talos-control", "controlplane", "talos/controlplane.yaml.tmpl", "talos-cp-01")
	got, err := r.Config(catalog.Identity{MAC: "d0:50:99:a2:3b:40"}, res, "http://booty")
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	assertGolden(t, "talos-controlplane.yaml.golden", got)
	if !strings.Contains(got, "type: controlplane") {
		t.Error("machine.type should be controlplane")
	}
	if !strings.Contains(got, "etcd:") {
		t.Error("control-plane config should carry the etcd section")
	}
}

func TestCloudInitUserData(t *testing.T) {
	r := newRenderer(t)
	res := &catalog.Resolution{
		Group:   "ubuntu",
		Profile: catalog.Profile{Name: "ubuntu-worker", Render: &catalog.Render{Kind: "cloud-init", Template: "cloud_init/ubuntu.yaml.tmpl"}},
		Vars:    map[string]string{"hostname": "ubuntu-01", "user": "ops"},
	}
	got, err := r.Config(catalog.Identity{IP: "192.168.1.50"}, res, "http://booty")
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	assertGolden(t, "cloud-init-user-data.golden", got)
	if !strings.HasPrefix(got, "#cloud-config\n") {
		t.Error("user-data must start with #cloud-config")
	}
}

func TestCloudInitMetaData(t *testing.T) {
	r := newRenderer(t)
	res := &catalog.Resolution{
		Profile: catalog.Profile{Name: "ubuntu-worker"},
		Vars:    map[string]string{"hostname": "ubuntu-01"},
	}
	got, err := r.CloudInitMetaData(catalog.Identity{IP: "192.168.1.50"}, res, "http://booty")
	if err != nil {
		t.Fatalf("CloudInitMetaData: %v", err)
	}
	if !strings.Contains(got, "instance-id: booty-ubuntu-01") {
		t.Errorf("meta-data missing stable instance-id:\n%s", got)
	}
	if !strings.Contains(got, "local-hostname: ubuntu-01") {
		t.Errorf("meta-data missing hostname:\n%s", got)
	}
}

func TestProxmoxAnswer(t *testing.T) {
	r := newRenderer(t)
	res := &catalog.Resolution{
		Group: "pve-01",
		Profile: catalog.Profile{
			Name:   "proxmox-host",
			Render: &catalog.Render{Kind: "proxmox-answer", Template: "proxmox/answer.toml.tmpl"},
		},
		Vars: map[string]string{
			"fqdn":                 "pve-01.home.local",
			"country":              "us",
			"timezone":             "America/New_York",
			"mailto":               "root@home.local",
			"root_password_hashed": "$6$rounds=656000$SALT$HASH",
			"install_disk":         "nvme0n1",
		},
	}
	got, err := r.Config(catalog.Identity{MAC: "d0:50:99:d5:6e:72"}, res, "http://booty")
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	assertGolden(t, "proxmox-answer.toml.golden", got)

	for _, want := range []string{
		"[global]", `fqdn = "pve-01.home.local"`,
		`disk_list = ["nvme0n1"]`, `source = "from-dhcp"`,
		`root_password_hashed = "$6$rounds=656000$SALT$HASH"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("answer.toml missing %q:\n%s", want, got)
		}
	}
}

func TestConfigNoTemplate(t *testing.T) {
	r := newRenderer(t)
	res := &catalog.Resolution{Profile: catalog.Profile{Name: "bare"}} // no Render
	if _, err := r.Config(catalog.Identity{}, res, "http://x"); err == nil {
		t.Fatal("want error when profile declares no render template")
	}
}

func TestIPXEScriptUnbootableProfile(t *testing.T) {
	r := newRenderer(t)
	// A profile with neither a boot kernel nor an ipxe render template can't
	// produce a script — that must be an error, not a silent empty response.
	res := &catalog.Resolution{Profile: catalog.Profile{Name: "broken"}}
	if _, err := r.IPXEScript(catalog.Identity{}, res, "http://x"); err == nil {
		t.Fatal("want error for unbootable profile")
	}
}

func writeOverlay(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestTemplateOverlayReplacesEmbedded(t *testing.T) {
	// An overlay file with the same base name as an embedded template replaces
	// it — this is the --templates-dir / platform-consumer seam.
	dir := writeOverlay(t, map[string]string{
		"ipxe/boot.ipxe": "#!ipxe\necho OVERRIDDEN {{ .Identity.MAC }}\n",
	})
	r, err := New(WithTemplates(os.DirFS(dir)))
	if err != nil {
		t.Fatalf("New with overlay: %v", err)
	}
	id, res := talosWorkerResolution()
	got, err := r.IPXEScript(id, res, "http://x")
	if err != nil {
		t.Fatalf("IPXEScript: %v", err)
	}
	if !strings.Contains(got, "OVERRIDDEN d0:50:99:b3:4c:50") {
		t.Errorf("overlay template not used:\n%s", got)
	}
}

func TestTemplateOverlayAddsNew(t *testing.T) {
	// An overlay can add a whole new template family a profile then names.
	dir := writeOverlay(t, map[string]string{
		"custom/motd.txt.tmpl": "hello {{ index .Vars \"hostname\" }}\n",
	})
	r, err := New(WithTemplates(os.DirFS(dir)))
	if err != nil {
		t.Fatalf("New with overlay: %v", err)
	}
	res := &catalog.Resolution{
		Profile: catalog.Profile{Name: "p", Render: &catalog.Render{Kind: "custom", Template: "custom/motd.txt.tmpl"}},
		Vars:    map[string]string{"hostname": "node-1"},
	}
	got, err := r.Config(catalog.Identity{}, res, "http://x")
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if got != "hello node-1\n" {
		t.Errorf("got %q", got)
	}
}

func TestTemplateOverlayEmptyErrors(t *testing.T) {
	// Pointing the overlay at a dir with no */* templates is a misconfiguration,
	// not a silent no-op.
	if _, err := New(WithTemplates(os.DirFS(t.TempDir()))); err == nil {
		t.Fatal("want error for empty overlay dir")
	}
}
