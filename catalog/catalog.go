package catalog

import (
	"errors"
	"fmt"
	"maps"
	"strings"
)

// Catalog is a loaded, validated set of profiles and groups. Construct it via a
// [DirSource]; do not build one field-by-field outside tests.
type Catalog struct {
	Profiles map[string]Profile
	Groups   []Group
}

// Profile is a named boot recipe: what to boot (kernel/initrd/cmdline) and what
// per-machine config to render for machines assigned to it. Vars are arbitrary
// key/values exposed to templates at render time.
type Profile struct {
	Name   string
	Boot   *Boot
	Render *Render
	Vars   map[string]string
}

// Boot is the kernel/initrd/cmdline a profile boots.
type Boot struct {
	Kernel  string   `hcl:"kernel,optional"`
	Initrd  string   `hcl:"initrd,optional"`
	Cmdline []string `hcl:"cmdline,optional"`
}

// Render names which renderer produces the machine config and the template it
// uses. Kept as opaque strings here so this package stays independent of the
// renderers.
type Render struct {
	Kind     string `hcl:"kind,optional"`
	Template string `hcl:"template,optional"`
}

// Group binds machines matching a selector to a profile. An empty selector
// matches every machine with the lowest possible specificity, which makes it a
// catch-all/default group.
type Group struct {
	Name     string
	Profile  string
	Selector map[string]string
	Vars     map[string]string
}

// Identity is the normalized set of attributes booty knows about a booting
// machine. In production these come from the iPXE request; in tests they are
// set directly. Selectors match against these keys.
type Identity struct {
	MAC          string
	UUID         string
	Serial       string
	Hostname     string
	IP           string
	Arch         string
	Product      string
	Manufacturer string
}

// attr returns the identity's value for a selector key, normalized the same way
// the selector value will be, so comparisons are apples-to-apples. The one
// exception is "mac": [Catalog.Match] normalizes it once before the group scan,
// because normalizing it again for every group was the single most expensive
// thing a boot request did.
func (id Identity) attr(key string) string {
	switch strings.ToLower(key) {
	case "mac":
		return id.MAC
	case "uuid":
		return strings.ToLower(strings.TrimSpace(id.UUID))
	case "serial":
		return id.Serial
	case "hostname":
		return id.Hostname
	case "ip":
		return id.IP
	case "arch":
		return id.Arch
	case "product":
		return id.Product
	case "manufacturer":
		return id.Manufacturer
	default:
		return ""
	}
}

// Resolution is the answer to Match: the winning group, the profile it binds to,
// and the merged variable set (profile vars overlaid by group vars).
type Resolution struct {
	Group   string
	Profile Profile
	Vars    map[string]string
	// Specificity is how many selector terms the winning group had to match —
	// 0 for a catch-all. A caller resolving one machine several ways (a
	// multi-NIC host tried under each of its MACs, say) needs this to pick the
	// most specific result, and only Match is in a position to report it.
	Specificity int
}

// ErrNoMatch is returned by Match when no group selects the identity and there is
// no catch-all group.
var ErrNoMatch = errors.New("no matching group")

// ErrUnknownProfile is returned by Match when a group did select the identity
// but names a profile the catalog does not define. It is deliberately distinct
// from [ErrNoMatch]: the machine is configured, the catalog is broken, and the
// two call for opposite remedies. Callers should branch with errors.Is rather
// than treating every Match failure as "unknown machine".
var ErrUnknownProfile = errors.New("group references unknown profile")

// Match finds the most specific group that selects id and resolves it to a
// profile. Specificity is the number of selector terms that had to match; ties
// break deterministically by group name so the same input always yields the same
// output. An empty selector matches everything at specificity 0 (a default).
func (c *Catalog) Match(id Identity) (*Resolution, error) {
	// Normalize the identity's MAC once rather than once per group: it is the
	// same string however many selectors it is tested against. See attr.
	id.MAC = NormalizeMAC(id.MAC)
	bestScore := -1
	var best *Group
	for i := range c.Groups {
		g := &c.Groups[i]
		score, ok := matchSelector(g.Selector, id)
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
		return nil, fmt.Errorf("group %q names profile %q: %w", best.Name, best.Profile, ErrUnknownProfile)
	}

	vars := make(map[string]string, len(prof.Vars)+len(best.Vars))
	maps.Copy(vars, prof.Vars)
	maps.Copy(vars, best.Vars) // group overrides profile
	return &Resolution{Group: best.Name, Profile: prof, Vars: vars, Specificity: bestScore}, nil
}

// matchSelector reports whether every selector term matches id, and if so the
// specificity (term count). An empty selector matches with specificity 0.
func matchSelector(sel map[string]string, id Identity) (score int, ok bool) {
	for k, want := range sel {
		if strings.EqualFold(k, "mac") {
			want = NormalizeMAC(want)
		}
		if got := id.attr(k); got == "" || got != want {
			return 0, false
		}
	}
	return len(sel), true
}

// validate checks referential integrity: every group names an existing profile,
// and group/profile names are unique. Returns all problems joined, so a bad
// catalog reports everything wrong at once rather than one error per run.
func (c *Catalog) validate() error {
	var errs []error
	seen := make(map[string]bool, len(c.Groups))
	for _, g := range c.Groups {
		switch {
		case g.Profile == "":
			errs = append(errs, fmt.Errorf("group %q: missing profile", g.Name))
		case c.Profiles[g.Profile].Name == "":
			errs = append(errs, fmt.Errorf("group %q: references unknown profile %q", g.Name, g.Profile))
		}
		if seen[g.Name] {
			errs = append(errs, fmt.Errorf("duplicate group %q", g.Name))
		}
		seen[g.Name] = true
	}
	return errors.Join(errs...)
}

// macSeparators strips the three separators a MAC may be spelled with.
//
// It is built once at package scope rather than per call. strings.NewReplacer
// compiles a trie when constructed, which cost 6.7 kB and dominated
// NormalizeMAC: building it per call measured 1134 ns/op and 6792 B/op against
// 91 ns/op and 56 B/op sharing one. Match runs NormalizeMAC once per group per
// boot request, so that difference is the bulk of the request's cost. A
// strings.Replacer is safe for concurrent use.
var macSeparators = strings.NewReplacer(":", "", "-", "", ".", "")

// NormalizeMAC converts any common MAC spelling to lowercase colon form:
// "52-54-00-AB-CD-EF", "5254.00ab.cdef" and "525400ABCDEF" all become
// "52:54:00:ab:cd:ef". Inputs that aren't 12 hex digits are lowercased and
// returned as-is rather than corrupted.
func NormalizeMAC(mac string) string {
	clean := strings.ToLower(macSeparators.Replace(strings.TrimSpace(mac)))
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

func isHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
