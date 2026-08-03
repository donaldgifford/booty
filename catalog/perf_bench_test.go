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

// normalizeMACShared is NormalizeMAC with the Replacer hoisted to a package
// level var, to measure what the per-call strings.NewReplacer costs.
var macCleaner = strings.NewReplacer(":", "", "-", "", ".", "")

func normalizeMACShared(mac string) string {
	clean := strings.ToLower(macCleaner.Replace(strings.TrimSpace(mac)))
	if len(clean) != 12 || !isHex(clean) {
		return strings.ToLower(strings.TrimSpace(mac))
	}
	var b strings.Builder
	b.Grow(17)
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(clean[i : i+2])
	}
	return b.String()
}

func BenchmarkNormalizeMACSharedReplacer(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		sinkString = normalizeMACShared("52:54:00:AB:CD:EF")
	}
}

func TestNormalizeMACSharedMatchesOriginal(t *testing.T) {
	for _, in := range []string{
		"52:54:00:AB:CD:EF", "52-54-00-ab-cd-ef", "5254.00ab.cdef", "525400ABCDEF",
		"", "  52:54:00:ab:cd:ef  ", "not-a-mac", "zz:zz:zz:zz:zz:zz",
	} {
		if got, want := normalizeMACShared(in), NormalizeMAC(in); got != want {
			t.Errorf("normalizeMACShared(%q) = %q, want %q", in, got, want)
		}
	}
}

var sinkString string

// --- optimized Match variant, for measurement only ---

// matchOpt is Match with two changes: the strings.Replacer is package-level
// (macCleaner) rather than rebuilt per call, and the identity's MAC is
// normalized once per Match instead of once per group.
func (c *Catalog) matchOpt(id Identity) (*Resolution, error) {
	normMAC := normalizeMACShared(id.MAC)
	bestScore := -1
	var best *Group
	for i := range c.Groups {
		g := &c.Groups[i]
		score, ok := matchSelectorOpt(g.Selector, id, normMAC)
		if !ok {
			continue
		}
		if score > bestScore || (score == bestScore && (best == nil || g.Name < best.Name)) {
			bestScore, best = score, g
		}
	}
	if best == nil {
		return nil, ErrNoMatch
	}
	prof, ok := c.Profiles[best.Profile]
	if !ok {
		return nil, ErrUnknownProfile
	}
	vars := make(map[string]string, len(prof.Vars)+len(best.Vars))
	for k, v := range prof.Vars {
		vars[k] = v
	}
	for k, v := range best.Vars {
		vars[k] = v
	}
	return &Resolution{Group: best.Name, Profile: prof, Vars: vars, Specificity: bestScore}, nil
}

func matchSelectorOpt(sel map[string]string, id Identity, normMAC string) (int, bool) {
	for k, want := range sel {
		var got string
		if strings.EqualFold(k, "mac") {
			want, got = normalizeMACShared(want), normMAC
		} else {
			got = id.attr(k)
		}
		if got == "" || got != want {
			return 0, false
		}
	}
	return len(sel), true
}

func BenchmarkMatchOptimized(b *testing.B) {
	for _, n := range []int{8, 32, 128, 512} {
		c, id := benchCatalog(n), benchIdentity(n)
		want, err := c.Match(id)
		if err != nil {
			b.Fatal(err)
		}
		got, err := c.matchOpt(id)
		if err != nil {
			b.Fatal(err)
		}
		if got.Group != want.Group || got.Specificity != want.Specificity {
			b.Fatalf("matchOpt = %+v, want %+v", got, want)
		}
		b.Run(fmt.Sprintf("groups=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := c.matchOpt(id); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

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
