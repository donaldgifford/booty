package httpsrv

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/donaldgifford/booty/catalog"
	"github.com/donaldgifford/booty/render"
)

// rackCatalog is a rack-shaped catalog: one catch-all, one arch group, and n-2
// MAC-pinned groups.
func rackCatalog(n int) *catalog.Catalog {
	c := &catalog.Catalog{
		Profiles: map[string]catalog.Profile{
			"talos-worker": {
				Name: "talos-worker",
				Boot: &catalog.Boot{
					Kernel:  "talos/v1.7.6/vmlinuz",
					Initrd:  "talos/v1.7.6/initramfs.xz",
					Cmdline: []string{"console=ttyS0,115200n8", "console=tty0", "talos.platform=metal"},
				},
				Render: &catalog.Render{Kind: "talos-machineconfig", Template: "talos/worker.yaml.tmpl"},
				Vars: map[string]string{
					"role": "worker", "cluster": "home", "talos_version": "v1.7.6",
					"cluster_endpoint": "https://192.168.1.100:6443",
				},
			},
		},
		Groups: []catalog.Group{
			{Name: "default", Profile: "talos-worker"},
			{Name: "arch-x86", Profile: "talos-worker", Selector: map[string]string{"arch": "x86_64"}},
		},
	}
	for i := range n - 2 {
		c.Groups = append(c.Groups, catalog.Group{
			Name:     fmt.Sprintf("node-%03d", i),
			Profile:  "talos-worker",
			Selector: map[string]string{"mac": fmt.Sprintf("52:54:00:00:%02x:%02x", i>>8, i&0xff)},
			Vars:     map[string]string{"hostname": fmt.Sprintf("node-%03d", i)},
		})
	}
	return c
}

func rackHandler(b *testing.B, n int, logger *slog.Logger) http.Handler {
	b.Helper()
	r, err := render.New()
	if err != nil {
		b.Fatal(err)
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return New(Config{
		Logger:   logger,
		Catalog:  rackCatalog(n),
		Renderer: r,
		BaseURL:  "http://192.168.1.10:8080",
	}).Handler()
}

// lastMACTarget is the URL for the last MAC-pinned node, so Match scans every
// group before finding it — the worst case for an enrolled machine.
func lastMACTarget(path string, n int) string {
	i := n - 3
	return fmt.Sprintf("%s?mac=52:54:00:00:%02X:%02X&arch=x86_64&uuid=4c4c4544-0037-3010-8044-b1c04f4b4432&hostname=node&ip=192.168.1.42",
		path, i>>8, i&0xff)
}

func BenchmarkIPXEEndpoint(b *testing.B) {
	for _, n := range []int{8, 32, 128} {
		h := rackHandler(b, n, nil)
		target := lastMACTarget("/ipxe", n)
		req := httptest.NewRequest(http.MethodGet, target, http.NoBody)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !contains(rec.Body.String(), "kernel http") {
			b.Fatalf("setup: %d %q", rec.Code, rec.Body.String())
		}
		b.Run(fmt.Sprintf("groups=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				w := &nullWriter{h: make(http.Header)}
				h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, http.NoBody))
			}
		})
	}
}

func BenchmarkMachineConfigEndpoint(b *testing.B) {
	n := 128
	h := rackHandler(b, n, nil)
	target := lastMACTarget("/machine-config", n)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, http.NoBody))
	if rec.Code != http.StatusOK {
		b.Fatalf("setup: %d %q", rec.Code, rec.Body.String())
	}
	b.ReportAllocs()
	for b.Loop() {
		w := &nullWriter{h: make(http.Header)}
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, http.NoBody))
	}
}

// BenchmarkIPXEEndpointParallel is the rack-boot shape: many machines hitting
// /ipxe at once.
func BenchmarkIPXEEndpointParallel(b *testing.B) {
	n := 128
	h := rackHandler(b, n, nil)
	target := lastMACTarget("/ipxe", n)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			w := &nullWriter{h: make(http.Header)}
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, http.NoBody))
		}
	})
}

// BenchmarkIPXEEndpointJSONLog swaps the discard-text logger for a JSON one, to
// price the three log lines each /ipxe request emits.
func BenchmarkIPXEEndpointJSONLog(b *testing.B) {
	n := 128
	h := rackHandler(b, n, slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo})))
	target := lastMACTarget("/ipxe", n)
	b.ReportAllocs()
	for b.Loop() {
		w := &nullWriter{h: make(http.Header)}
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, http.NoBody))
	}
}

// nullWriter is an http.ResponseWriter that discards the body, so the benchmark
// measures the handler rather than httptest's buffer growth.
type nullWriter struct {
	h http.Header
}

func (w *nullWriter) Header() http.Header       { return w.h }
func (*nullWriter) Write(p []byte) (int, error) { return len(p), nil }
func (*nullWriter) WriteHeader(int)             {}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// BenchmarkBootAssetDownload measures the path the large files actually take:
// GET /boot/<file> over a real loopback listener. booty's TFTP server moves
// only the iPXE binary; the kernel and initrd are fetched over HTTP by the
// chainloaded iPXE (see render/templates/ipxe/boot.ipxe).
func BenchmarkBootAssetDownload(b *testing.B) {
	const size = 64 << 20
	dir := b.TempDir()
	f, err := os.Create(filepath.Join(dir, "initramfs.xz"))
	if err != nil {
		b.Fatal(err)
	}
	chunk := make([]byte, 1<<20)
	for w := 0; w < size; w += len(chunk) {
		if _, err := f.Write(chunk); err != nil {
			b.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}

	h := New(Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), BootDir: dir}).Handler()
	srv := httptest.NewServer(h)
	b.Cleanup(srv.Close)

	for _, clients := range []int{1, 8, 32} {
		b.Run(fmt.Sprintf("clients=%d", clients), func(b *testing.B) {
			b.SetBytes(int64(size) * int64(clients))
			for b.Loop() {
				errs := make(chan error, clients)
				for range clients {
					go func() {
						resp, err := http.Get(srv.URL + "/boot/initramfs.xz")
						if err != nil {
							errs <- err
							return
						}
						n, err := io.Copy(io.Discard, resp.Body)
						_ = resp.Body.Close()
						if err == nil && n != size {
							err = fmt.Errorf("got %d bytes, want %d", n, size)
						}
						errs <- err
					}()
				}
				for range clients {
					if err := <-errs; err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
