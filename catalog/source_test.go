package catalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadHCL writes the given files into a temp dir and loads them as a catalog.
func loadHCL(t *testing.T, files map[string]string) (*Catalog, error) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return DirSource{Root: dir}.Load(context.Background())
}

func TestLoadResolvesVarsLocalsFunctions(t *testing.T) {
	cat, err := loadHCL(t, map[string]string{
		"catalog.hcl": `
variable "talos_version" {
  default = "v1.7.6"
}

locals {
  boot_base = "talos/${var.talos_version}"
  common    = ["console=ttyS0,115200n8", "console=tty0"]
}

profile "talos-worker" {
  boot {
    kernel  = "${local.boot_base}/vmlinuz"
    initrd  = "${local.boot_base}/initramfs.xz"
    cmdline = concat(local.common, ["talos.platform=metal"])
  }
  render {
    kind     = "talos-machineconfig"
    template = "talos/worker.yaml.tmpl"
  }
  vars = {
    role = upper("worker")
  }
}

group "workers" {
  profile = "talos-worker"
  selector = {
    arch = "x86_64"
  }
}
`,
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	p, ok := cat.Profiles["talos-worker"]
	if !ok {
		t.Fatal("profile talos-worker missing")
	}
	if p.Boot.Kernel != "talos/v1.7.6/vmlinuz" { // var -> local -> interpolation
		t.Errorf("kernel = %q, want talos/v1.7.6/vmlinuz", p.Boot.Kernel)
	}
	if got := strings.Join(p.Boot.Cmdline, " "); got != "console=ttyS0,115200n8 console=tty0 talos.platform=metal" {
		t.Errorf("cmdline = %q (concat of local + literal failed)", got)
	}
	if p.Vars["role"] != "WORKER" { // upper() function
		t.Errorf("role = %q, want WORKER", p.Vars["role"])
	}
	if p.Render.Kind != "talos-machineconfig" {
		t.Errorf("render.kind = %q", p.Render.Kind)
	}

	// The loaded catalog matches like any other.
	r, err := cat.Match(Identity{Arch: "x86_64", MAC: "aa:bb:cc:dd:ee:ff"})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if r.Group != "workers" {
		t.Errorf("group = %q, want workers", r.Group)
	}
}

func TestLoadCustomMacBareFunction(t *testing.T) {
	cat, err := loadHCL(t, map[string]string{
		"c.hcl": `
profile "p" { vars = { tag = mac_bare("D0:50:99:A2:3B:40") } }
group "g" { profile = "p" }
`,
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cat.Profiles["p"].Vars["tag"]; got != "d05099a23b40" {
		t.Fatalf("mac_bare produced %q, want d05099a23b40", got)
	}
}

func TestLoadVariableOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "c.hcl"), []byte(`
variable "cluster" { default = "dev" }
profile "p" { vars = { cluster = var.cluster } }
group "g" { profile = "p" }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := DirSource{Root: dir, Overrides: map[string]string{"cluster": "prod"}}.Load(context.Background())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cat.Profiles["p"].Vars["cluster"]; got != "prod" {
		t.Fatalf("cluster = %q, want prod (override beat default)", got)
	}
}

func TestLoadMultipleFilesMerge(t *testing.T) {
	// A variable in one file, a profile referencing it in another.
	cat, err := loadHCL(t, map[string]string{
		"vars.hcl":     `variable "v" { default = "x86_64" }`,
		"profiles.hcl": `profile "p" {}`,
		"groups.hcl": `
group "g" {
  profile  = "p"
  selector = { arch = var.v }
}`,
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := cat.Match(Identity{Arch: "x86_64"}); err != nil {
		t.Fatalf("match after cross-file merge: %v", err)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := map[string]struct {
		files   map[string]string
		wantSub string
	}{
		"malformed hcl": {
			files:   map[string]string{"c.hcl": `profile "p" {`},
			wantSub: "parsing",
		},
		"dangling profile": {
			files:   map[string]string{"c.hcl": `group "g" { profile = "ghost" }`},
			wantSub: "unknown profile",
		},
		"locals cycle": {
			files: map[string]string{"c.hcl": `
locals {
  a = local.b
  b = local.a
}
profile "p" {}
group "g" { profile = "p" }
`},
			wantSub: "cannot resolve locals",
		},
		"duplicate profile": {
			files: map[string]string{"c.hcl": `
profile "p" {}
profile "p" {}
group "g" { profile = "p" }
`},
			wantSub: "duplicate profile",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := loadHCL(t, tt.files)
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.wantSub)
			}
		})
	}
}

func TestLoadNoFiles(t *testing.T) {
	_, err := DirSource{Root: t.TempDir()}.Load(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no .hcl files") {
		t.Fatalf("want no-files error, got %v", err)
	}
}
