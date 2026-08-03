package render

import (
	"testing"

	"github.com/donaldgifford/booty/catalog"
)

func benchRenderer(b *testing.B) *Renderer {
	b.Helper()
	r, err := New()
	if err != nil {
		b.Fatal(err)
	}
	return r
}

func benchTalosResolution() (catalog.Identity, *catalog.Resolution) {
	id := catalog.Identity{MAC: "d0:50:99:b3:4c:50", Arch: "x86_64", IP: "192.168.1.42"}
	res := &catalog.Resolution{
		Group: "worker-01",
		Profile: catalog.Profile{
			Name: "talos-worker",
			Boot: &catalog.Boot{
				Kernel: "talos/v1.7.6/vmlinuz",
				Initrd: "talos/v1.7.6/initramfs.xz",
				Cmdline: []string{
					"console=ttyS0,115200n8", "console=tty0", "talos.platform=metal",
					"talos.config=http://boot.home.local:8080/machine-config?mac=${mac}",
				},
			},
			Render: &catalog.Render{Kind: "talos-machineconfig", Template: "talos/worker.yaml.tmpl"},
		},
		Vars: map[string]string{
			"role": "worker", "cluster": "home", "talos_version": "v1.7.6",
			"cluster_endpoint": "https://192.168.1.100:6443", "hostname": "worker-01",
		},
	}
	return id, res
}

func BenchmarkIPXEScript(b *testing.B) {
	r := benchRenderer(b)
	id, res := benchTalosResolution()
	b.ReportAllocs()
	for b.Loop() {
		s, err := r.IPXEScript(id, res, "http://192.168.1.10:8080")
		if err != nil {
			b.Fatal(err)
		}
		sink = s
	}
}

func BenchmarkIPXEScriptParallel(b *testing.B) {
	r := benchRenderer(b)
	id, res := benchTalosResolution()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s, err := r.IPXEScript(id, res, "http://192.168.1.10:8080")
			if err != nil {
				b.Fatal(err)
			}
			sink = s
		}
	})
}

func BenchmarkConfigTalos(b *testing.B) {
	r := benchRenderer(b)
	id, res := benchTalosResolution()
	b.ReportAllocs()
	for b.Loop() {
		s, err := r.Config(id, res, "http://192.168.1.10:8080")
		if err != nil {
			b.Fatal(err)
		}
		sink = s
	}
}

func BenchmarkConfigTalosParallel(b *testing.B) {
	r := benchRenderer(b)
	id, res := benchTalosResolution()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s, err := r.Config(id, res, "http://192.168.1.10:8080")
			if err != nil {
				b.Fatal(err)
			}
			sink = s
		}
	})
}

// BenchmarkNew measures construction — the cost a caller pays once, or on every
// catalog reload if a consumer rebuilds the renderer alongside the catalog.
func BenchmarkNew(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		r, err := New()
		if err != nil {
			b.Fatal(err)
		}
		sinkRenderer = r
	}
}

var (
	sink         string
	sinkRenderer *Renderer
)
