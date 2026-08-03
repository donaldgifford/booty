package catalog

import (
	"errors"
	"testing"
)

func TestNormalizeMAC(t *testing.T) {
	tests := map[string]string{
		"52:54:00:ab:cd:ef":     "52:54:00:ab:cd:ef",
		"52-54-00-AB-CD-EF":     "52:54:00:ab:cd:ef",
		"525400ABCDEF":          "52:54:00:ab:cd:ef",
		"5254.00ab.cdef":        "52:54:00:ab:cd:ef",
		"  52:54:00:AB:CD:EF  ": "52:54:00:ab:cd:ef",
		"not-a-mac":             "not-a-mac", // left alone, not corrupted
	}
	for in, want := range tests {
		if got := NormalizeMAC(in); got != want {
			t.Errorf("NormalizeMAC(%q) = %q, want %q", in, got, want)
		}
	}
}

// testCatalog builds a small catalog by hand: a catch-all, a per-arch group, and
// a per-MAC group, all valid.
func testCatalog() *Catalog {
	return &Catalog{
		Profiles: map[string]Profile{
			"talos-worker":  {Name: "talos-worker", Vars: map[string]string{"role": "worker", "cluster": "default"}},
			"talos-control": {Name: "talos-control", Vars: map[string]string{"role": "control"}},
			"rescue":        {Name: "rescue"},
		},
		Groups: []Group{
			{Name: "default", Profile: "rescue"}, // empty selector = catch-all
			{Name: "workers", Profile: "talos-worker", Selector: map[string]string{"arch": "x86_64"}},
			{
				Name:     "cp-01",
				Profile:  "talos-control",
				Selector: map[string]string{"mac": "D0-50-99-A2-3B-40"}, // non-canonical on purpose
				Vars:     map[string]string{"hostname": "talos-cp-01", "cluster": "home"},
			},
		},
	}
}

func TestMatchSpecificity(t *testing.T) {
	c := testCatalog()

	tests := []struct {
		name      string
		id        Identity
		wantGroup string
		wantProf  string
	}{
		{
			name:      "exact mac beats arch group and catch-all",
			id:        Identity{MAC: "d0:50:99:a2:3b:40", Arch: "x86_64"},
			wantGroup: "cp-01",
			wantProf:  "talos-control",
		},
		{
			name:      "arch group beats catch-all",
			id:        Identity{MAC: "aa:bb:cc:dd:ee:ff", Arch: "x86_64"},
			wantGroup: "workers",
			wantProf:  "talos-worker",
		},
		{
			name:      "unknown machine falls to catch-all",
			id:        Identity{MAC: "aa:bb:cc:dd:ee:ff", Arch: "arm64"},
			wantGroup: "default",
			wantProf:  "rescue",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := c.Match(tt.id)
			if err != nil {
				t.Fatalf("Match: %v", err)
			}
			if r.Group != tt.wantGroup {
				t.Errorf("group = %q, want %q", r.Group, tt.wantGroup)
			}
			if r.Profile.Name != tt.wantProf {
				t.Errorf("profile = %q, want %q", r.Profile.Name, tt.wantProf)
			}
		})
	}
}

func TestMatchMACNormalization(t *testing.T) {
	// Selector holds a dash-separated upper MAC; identity holds colon lower. They
	// must match through NormalizeMAC on both sides.
	c := testCatalog()
	r, err := c.Match(Identity{MAC: "d0:50:99:a2:3b:40"})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if r.Group != "cp-01" {
		t.Fatalf("group = %q, want cp-01", r.Group)
	}
}

func TestMatchVarsMerge(t *testing.T) {
	// Group vars override profile vars; profile-only vars survive.
	c := testCatalog()
	r, err := c.Match(Identity{MAC: "d0:50:99:a2:3b:40"})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if r.Vars["hostname"] != "talos-cp-01" { // from group
		t.Errorf("hostname = %q, want talos-cp-01", r.Vars["hostname"])
	}
	if r.Vars["role"] != "control" { // from profile
		t.Errorf("role = %q, want control", r.Vars["role"])
	}
}

func TestMatchNoMatch(t *testing.T) {
	// Catalog with no catch-all: an unmatched identity gets ErrNoMatch.
	c := &Catalog{
		Profiles: map[string]Profile{"p": {Name: "p"}},
		Groups:   []Group{{Name: "g", Profile: "p", Selector: map[string]string{"arch": "x86_64"}}},
	}
	_, err := c.Match(Identity{Arch: "arm64"})
	if !errors.Is(err, ErrNoMatch) {
		t.Fatalf("err = %v, want ErrNoMatch", err)
	}
}

func TestMatchTieBreak(t *testing.T) {
	// Two single-term selectors both match; the lexicographically smaller group
	// name wins, deterministically.
	c := &Catalog{
		Profiles: map[string]Profile{"p": {Name: "p"}},
		Groups: []Group{
			{Name: "zebra", Profile: "p", Selector: map[string]string{"arch": "x86_64"}},
			{Name: "alpha", Profile: "p", Selector: map[string]string{"product": "R640"}},
		},
	}
	r, err := c.Match(Identity{Arch: "x86_64", Product: "R640"})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if r.Group != "alpha" {
		t.Fatalf("group = %q, want alpha (tie broken by name)", r.Group)
	}
}

func TestValidate(t *testing.T) {
	t.Run("dangling profile reference", func(t *testing.T) {
		c := &Catalog{
			Profiles: map[string]Profile{"real": {Name: "real"}},
			Groups:   []Group{{Name: "g", Profile: "ghost"}},
		}
		if err := c.validate(); err == nil {
			t.Fatal("want error for unknown profile reference")
		}
	})
	t.Run("duplicate group", func(t *testing.T) {
		c := &Catalog{
			Profiles: map[string]Profile{"p": {Name: "p"}},
			Groups:   []Group{{Name: "dup", Profile: "p"}, {Name: "dup", Profile: "p"}},
		}
		if err := c.validate(); err == nil {
			t.Fatal("want error for duplicate group name")
		}
	})
	t.Run("valid", func(t *testing.T) {
		if err := testCatalog().validate(); err != nil {
			t.Fatalf("valid catalog rejected: %v", err)
		}
	})
}

// TestMatchUnknownProfileSentinel pins the distinction between "this machine is
// not in the catalog" and "the catalog is broken". They call for opposite
// remedies, and before ErrUnknownProfile existed a caller could only tell them
// apart by matching on the error string — so httpsrv told the operator to add a
// group for a machine whose group already existed.
func TestMatchUnknownProfileSentinel(t *testing.T) {
	c := &Catalog{
		Profiles: map[string]Profile{},
		Groups: []Group{
			{Name: "worker-01", Profile: "does-not-exist", Selector: map[string]string{"mac": "d0:50:99:b3:4c:50"}},
		},
	}

	_, err := c.Match(Identity{MAC: "d0:50:99:b3:4c:50"})
	if err == nil {
		t.Fatal("want an error for a group naming a missing profile")
	}
	if !errors.Is(err, ErrUnknownProfile) {
		t.Errorf("errors.Is(err, ErrUnknownProfile) = false; err = %v", err)
	}
	if errors.Is(err, ErrNoMatch) {
		t.Error("a dangling profile reference must not report as ErrNoMatch")
	}

	// A machine no group selects still reports ErrNoMatch, not ErrUnknownProfile.
	_, err = c.Match(Identity{MAC: "aa:bb:cc:dd:ee:ff"})
	if !errors.Is(err, ErrNoMatch) {
		t.Errorf("unselected machine: errors.Is(err, ErrNoMatch) = false; err = %v", err)
	}
	if errors.Is(err, ErrUnknownProfile) {
		t.Error("unselected machine must not report as ErrUnknownProfile")
	}
}

// TestMatchReportsSpecificity pins the term count Match already computes onto
// the result. Without it a caller resolving one machine several ways — a
// multi-NIC host tried under each of its MACs — had to rescan Catalog.Groups by
// name to recover the number, which is what httpsrv did.
func TestMatchReportsSpecificity(t *testing.T) {
	c := &Catalog{
		Profiles: map[string]Profile{"p": {Name: "p"}},
		Groups: []Group{
			{Name: "catch-all", Profile: "p"},
			{Name: "by-mac", Profile: "p", Selector: map[string]string{"mac": "d0:50:99:b3:4c:50"}},
			{Name: "by-mac-and-arch", Profile: "p", Selector: map[string]string{
				"mac": "d0:50:99:b3:4c:51", "arch": "x86_64",
			}},
		},
	}

	tests := []struct {
		name      string
		id        Identity
		wantGroup string
		wantSpec  int
	}{
		{"catch-all matches with no terms", Identity{MAC: "aa:bb:cc:dd:ee:ff"}, "catch-all", 0},
		{"one selector term", Identity{MAC: "d0:50:99:b3:4c:50"}, "by-mac", 1},
		{"two selector terms", Identity{MAC: "d0:50:99:b3:4c:51", Arch: "x86_64"}, "by-mac-and-arch", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := c.Match(tt.id)
			if err != nil {
				t.Fatalf("Match: %v", err)
			}
			if res.Group != tt.wantGroup {
				t.Fatalf("group = %q, want %q", res.Group, tt.wantGroup)
			}
			if res.Specificity != tt.wantSpec {
				t.Errorf("Specificity = %d, want %d", res.Specificity, tt.wantSpec)
			}
		})
	}
}
