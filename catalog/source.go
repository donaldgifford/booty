package catalog

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
)

// Source loads a Catalog. Implementations vary by where desired state lives:
// DirSource reads HCL from a directory now; git:// and platform:// sources come
// later (PLAN-0001). The interface is the seam that keeps those swappable.
type Source interface {
	Load(ctx context.Context) (*Catalog, error)
	String() string
}

// DirSource loads every *.hcl file in Root, merging them into one catalog.
// Overrides supply values for `variable` blocks by name (e.g. from a flag),
// falling back to each variable's default.
type DirSource struct {
	Root      string
	Overrides map[string]string
}

// String identifies the source for logs, in URL form.
func (s DirSource) String() string { return "dir://" + s.Root }

// Load reads, parses, evaluates and validates the catalog. Files are processed in
// sorted order so a fixed input always produces a fixed result.
func (s DirSource) Load(ctx context.Context) (*Catalog, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(filepath.Join(s.Root, "*.hcl"))
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", s.Root, err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no .hcl files found in %s", s.Root)
	}
	sort.Strings(paths)

	parser := hclparse.NewParser()
	files := make([]*hcl.File, 0, len(paths))
	for _, p := range paths {
		f, diags := parser.ParseHCLFile(p)
		if diags.HasErrors() {
			return nil, fmt.Errorf("parsing %s: %w", p, diags)
		}
		files = append(files, f)
	}
	return decodeCatalog(files, s.Overrides)
}

// The block schema booty understands at the top level of a catalog file.
var catalogSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "variable", LabelNames: []string{"name"}},
		{Type: "locals"},
		{Type: "profile", LabelNames: []string{"name"}},
		{Type: "group", LabelNames: []string{"name"}},
	},
}

// decodeCatalog runs the two-phase evaluation HCL requires when later blocks
// reference values defined by earlier ones:
//  1. evaluate variable defaults and locals to build an EvalContext;
//  2. decode profile/group blocks with that context so their expressions
//     (interpolations, function calls, var/local references) resolve.
func decodeCatalog(files []*hcl.File, overrides map[string]string) (*Catalog, error) {
	body := hcl.MergeFiles(files)
	content, diags := body.Content(catalogSchema)
	if diags.HasErrors() {
		return nil, fmt.Errorf("catalog structure: %w", diags)
	}

	funcs := evalFuncs()

	vars, err := evalVariables(content.Blocks.OfType("variable"), funcs, overrides)
	if err != nil {
		return nil, err
	}
	locals, err := evalLocals(content.Blocks.OfType("locals"), vars, funcs)
	if err != nil {
		return nil, err
	}

	ctx := &hcl.EvalContext{
		Variables: map[string]cty.Value{
			"var":   cty.ObjectVal(vars),
			"local": cty.ObjectVal(locals),
		},
		Functions: funcs,
	}

	cat := &Catalog{Profiles: make(map[string]Profile)}

	for _, b := range content.Blocks.OfType("profile") {
		name := b.Labels[0]
		if _, dup := cat.Profiles[name]; dup {
			return nil, fmt.Errorf("duplicate profile %q", name)
		}
		var pb struct {
			Boot   *Boot             `hcl:"boot,block"`
			Render *Render           `hcl:"render,block"`
			Vars   map[string]string `hcl:"vars,optional"`
		}
		if d := gohcl.DecodeBody(b.Body, ctx, &pb); d.HasErrors() {
			return nil, fmt.Errorf("profile %q: %w", name, d)
		}
		cat.Profiles[name] = Profile{Name: name, Boot: pb.Boot, Render: pb.Render, Vars: pb.Vars}
	}

	for _, b := range content.Blocks.OfType("group") {
		name := b.Labels[0]
		var gb struct {
			Profile  string            `hcl:"profile"`
			Selector map[string]string `hcl:"selector,optional"`
			Vars     map[string]string `hcl:"vars,optional"`
		}
		if d := gohcl.DecodeBody(b.Body, ctx, &gb); d.HasErrors() {
			return nil, fmt.Errorf("group %q: %w", name, d)
		}
		cat.Groups = append(cat.Groups, Group{
			Name: name, Profile: gb.Profile, Selector: gb.Selector, Vars: gb.Vars,
		})
	}

	if err := cat.validate(); err != nil {
		return nil, err
	}
	return cat, nil
}

// evalVariables evaluates each variable block's default (functions available but
// no var/local references), then applies any string override by name.
func evalVariables(blocks hcl.Blocks, funcs map[string]function.Function, overrides map[string]string) (map[string]cty.Value, error) {
	base := &hcl.EvalContext{Functions: funcs}
	out := make(map[string]cty.Value, len(blocks))
	for _, b := range blocks {
		name := b.Labels[0]
		var decl struct {
			Default hcl.Expression `hcl:"default,optional"`
			Remain  hcl.Body       `hcl:",remain"` // tolerate type/description/etc.
		}
		if d := gohcl.DecodeBody(b.Body, base, &decl); d.HasErrors() {
			return nil, fmt.Errorf("variable %q: %w", name, d)
		}
		val := cty.NullVal(cty.DynamicPseudoType)
		if decl.Default != nil {
			v, d := decl.Default.Value(base)
			if d.HasErrors() {
				return nil, fmt.Errorf("variable %q default: %w", name, d)
			}
			val = v
		}
		if ov, ok := overrides[name]; ok {
			val = cty.StringVal(ov)
		}
		out[name] = val
	}
	return out, nil
}

// evalLocals evaluates all locals to a fixed point so a local may reference
// var.* and other locals regardless of declaration order. If a pass makes no
// progress, the remaining locals form a cycle or reference something undefined.
func evalLocals(blocks hcl.Blocks, vars map[string]cty.Value, funcs map[string]function.Function) (map[string]cty.Value, error) {
	exprs := map[string]hcl.Expression{}
	for _, b := range blocks {
		attrs, diags := b.Body.JustAttributes()
		if diags.HasErrors() {
			return nil, fmt.Errorf("locals: %w", diags)
		}
		for name, a := range attrs {
			if _, dup := exprs[name]; dup {
				return nil, fmt.Errorf("duplicate local %q", name)
			}
			exprs[name] = a.Expr
		}
	}

	resolved := make(map[string]cty.Value, len(exprs))
	var lastErr string
	for len(resolved) < len(exprs) {
		progressed := false
		for name, expr := range exprs {
			if _, ok := resolved[name]; ok {
				continue
			}
			ctx := &hcl.EvalContext{
				Variables: map[string]cty.Value{
					"var":   cty.ObjectVal(vars),
					"local": cty.ObjectVal(resolved),
				},
				Functions: funcs,
			}
			v, diags := expr.Value(ctx)
			if diags.HasErrors() {
				lastErr = diags.Error() // may just depend on an as-yet-unresolved local
				continue
			}
			resolved[name] = v
			progressed = true
		}
		if !progressed {
			var stuck []string
			for n := range exprs {
				if _, ok := resolved[n]; !ok {
					stuck = append(stuck, n)
				}
			}
			sort.Strings(stuck)
			return nil, fmt.Errorf("cannot resolve locals %s (cycle or undefined reference): %s",
				strings.Join(stuck, ", "), lastErr)
		}
	}
	return resolved, nil
}

// macBareFunc is a custom function: mac_bare("52:54:00:ab:cd:ef") -> "525400abcdef",
// handy for deriving per-MAC asset filenames. It demonstrates how a project
// registers its own functions into the EvalContext alongside the cty stdlib.
var macBareFunc = function.New(&function.Spec{
	Params: []function.Parameter{{Name: "mac", Type: cty.String}},
	Type:   function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		s := strings.NewReplacer(":", "", "-", "", ".", "").Replace(strings.ToLower(args[0].AsString()))
		return cty.StringVal(s), nil
	},
})

// evalFuncs is the curated function set exposed to catalog expressions: a subset
// of the cty standard library plus booty's own. Kept deliberately small — this
// is a config language, not a general-purpose runtime.
func evalFuncs() map[string]function.Function {
	return map[string]function.Function{
		// strings
		"upper":      stdlib.UpperFunc,
		"lower":      stdlib.LowerFunc,
		"trimspace":  stdlib.TrimSpaceFunc,
		"trimprefix": stdlib.TrimPrefixFunc,
		"trimsuffix": stdlib.TrimSuffixFunc,
		"replace":    stdlib.ReplaceFunc,
		"join":       stdlib.JoinFunc,
		"split":      stdlib.SplitFunc,
		"format":     stdlib.FormatFunc,
		"formatlist": stdlib.FormatListFunc,
		"substr":     stdlib.SubstrFunc,
		"chomp":      stdlib.ChompFunc,
		// collections
		"concat":   stdlib.ConcatFunc,
		"coalesce": stdlib.CoalesceFunc,
		"contains": stdlib.ContainsFunc,
		"length":   stdlib.LengthFunc,
		"element":  stdlib.ElementFunc,
		"lookup":   stdlib.LookupFunc,
		"keys":     stdlib.KeysFunc,
		"values":   stdlib.ValuesFunc,
		"merge":    stdlib.MergeFunc,
		// encoding / regex
		"jsonencode": stdlib.JSONEncodeFunc,
		"regex":      stdlib.RegexFunc,
		// booty custom
		"mac_bare": macBareFunc,
	}
}
