package catalog

import (
	"fmt"
	"strings"
	"testing"
)

// benchCatalog builds a catalog shaped like a real rack: one catch-all group,
// one arch group, and n-2 groups pinned to a specific MAC.
func benchCatalog(n int) *Catalog {
	c := &Catalog{
		Profiles: map[string]Profile{
			"talos-worker": {
				Name:   "talos-worker",
				Boot:   &Boot{Kernel: "talos/vmlinuz", Initrd: "talos/initramfs.xz", Cmdline: []string{"a", "b", "c"}},
				Render: &Render{Kind: "talos-machineconfig", Template: "talos/worker.yaml.tmpl"},
				Vars: map[string]string{
					"role": "worker", "cluster": "home", "talos_version": "v1.7.6",
					"cluster_endpoint": "https://192.168.1.100:6443",
				},
			},
		},
	}
	c.Groups = append(c.Groups,
		Group{Name: "default", Profile: "talos-worker"},
		Group{Name: "arch-x86", Profile: "talos-worker", Selector: map[string]string{"arch": "x86_64"}},
	)
	for i := range n - 2 {
		c.Groups = append(c.Groups, Group{
			Name:     fmt.Sprintf("node-%03d", i),
			Profile:  "talos-worker",
			Selector: map[string]string{"mac": fmt.Sprintf("52:54:00:00:%02x:%02x", i>>8, i&0xff)},
			Vars:     map[string]string{"hostname": fmt.Sprintf("node-%03d", i)},
		})
	}
	return c
}

// benchIdentity is the identity of the last MAC-pinned node, so Match has to
// scan every group before it finds the winner.
func benchIdentity(n int) Identity {
	i := n - 3
	return Identity{
		MAC:          fmt.Sprintf("52:54:00:00:%02X:%02X", i>>8, i&0xff), // uppercase, as iPXE sends it
		UUID:         "4c4c4544-0037-3010-8044-b1c04f4b4432",
		Arch:         "x86_64",
		IP:           "192.168.1.42",
		Product:      "PowerEdge R640",
		Manufacturer: "Dell Inc.",
	}
}

func BenchmarkMatch(b *testing.B) {
	for _, n := range []int{8, 32, 128, 512} {
		c, id := benchCatalog(n), benchIdentity(n)
		if _, err := c.Match(id); err != nil {
			b.Fatalf("Match(%d groups) = %v, want a resolution", n, err)
		}
		b.Run(fmt.Sprintf("groups=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := c.Match(id); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkMatchNoMACSelectors isolates how much of Match's cost is MAC
// normalization: same shape, but selectors key on hostname instead of mac.
func BenchmarkMatchNoMACSelectors(b *testing.B) {
	n := 128
	c := benchCatalog(n)
	for i := range c.Groups {
		if mac, ok := c.Groups[i].Selector["mac"]; ok {
			delete(c.Groups[i].Selector, "mac")
			c.Groups[i].Selector["hostname"] = mac
		}
	}
	id := benchIdentity(n)
	id.Hostname = strings.ToLower(id.MAC)
	id.MAC = ""
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Match(id); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNormalizeMAC(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		sinkString = NormalizeMAC("52:54:00:AB:CD:EF")
	}
}

var sinkString string

// BenchmarkMatchNoMatch measures the miss path (unknown machine, catch-all
// wins) — the same scan, but it is also what a rack of unenrolled machines hits.
func BenchmarkMatchMissAllGroups(b *testing.B) {
	c := benchCatalog(128)
	id := benchIdentity(128)
	id.MAC = "aa:bb:cc:dd:ee:ff" // matches nothing but the catch-all
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Match(id); err != nil {
			b.Fatal(err)
		}
	}
}
