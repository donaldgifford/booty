package catalog

import (
	"fmt"
	"testing"
)

// Match runs once per booting machine and scans every group, so per-group work
// is multiplied by the size of the catalog. That made it easy to hide an
// expensive mistake: NormalizeMAC used to build its strings.NewReplacer inline,
// which allocates a ~6.7 kB trie at construction, and Match called it twice per
// group. A single request against 128 groups allocated 1.69 MiB and 1641 times,
// and nothing failed — the cost was invisible until someone profiled it.
//
// These bounds exist to make that class of regression loud. They are budgets,
// not measurements: the numbers below sit well above what the code does today
// and well below what it did when the replacer was rebuilt per call, so
// ordinary changes will not trip them and a reintroduced per-comparison
// allocation will.
func TestMatchAllocationBudget(t *testing.T) {
	const (
		groups   = 128
		maxAlloc = 600 // ~258 today; was 1641 with the per-call replacer
	)

	c := &Catalog{
		Profiles: map[string]Profile{"worker": {Name: "worker"}},
		Groups:   make([]Group, 0, groups+1),
	}
	c.Groups = append(c.Groups, Group{Name: "default", Profile: "worker"})
	for i := range groups {
		c.Groups = append(c.Groups, Group{
			Name:     fmt.Sprintf("node-%03d", i),
			Profile:  "worker",
			Selector: map[string]string{"mac": fmt.Sprintf("d0:50:99:b3:4c:%02x", i&0xff)},
		})
	}

	id := Identity{MAC: "D0-50-99-B3-4C-7F", Arch: "x86_64"}
	if _, err := c.Match(id); err != nil {
		t.Fatalf("Match: %v", err)
	}

	got := testing.AllocsPerRun(100, func() {
		if _, err := c.Match(id); err != nil {
			t.Fatalf("Match: %v", err)
		}
	})
	if got > maxAlloc {
		t.Errorf("Match over %d groups allocated %.0f times, budget is %d — "+
			"something is allocating per group again", groups, got, maxAlloc)
	}
}

// TestNormalizeMACAllocationBudget pins the replacer specifically. Rebuilding
// strings.NewReplacer per call cost 7 allocations and 6792 B; sharing one costs
// 3 and 56 B.
func TestNormalizeMACAllocationBudget(t *testing.T) {
	const maxAlloc = 4 // 3 today; was 7 when the replacer was built per call

	for _, spelling := range []string{
		"d0:50:99:b3:4c:50", "D0-50-99-B3-4C-50", "d05099b34c50", "not-a-mac",
	} {
		got := testing.AllocsPerRun(100, func() { _ = NormalizeMAC(spelling) })
		if got > maxAlloc {
			t.Errorf("NormalizeMAC(%q) allocated %.0f times, budget is %d — "+
				"is macSeparators being rebuilt per call?", spelling, got, maxAlloc)
		}
	}
}
