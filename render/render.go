// Package render turns a catalog resolution into the text a booting machine
// consumes. At this stage it produces iPXE scripts; Chapter 6 adds the Talos
// machineconfig and cloud-init renderers to the same package and template set.
//
// Rendering is the second of the two evaluation phases in booty: the catalog's
// HCL is evaluated at load time (Chapter 5), and text/template is evaluated here
// at request time. Keeping them separate is deliberate — the config language and
// the output language never touch. iPXE's own ${...} variables pass through
// untouched because Go templates use {{...}} delimiters.
package render

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"text/template"

	"github.com/donaldgifford/booty/catalog"
)

// all: is required so files beginning with "_" (our shared partials, e.g.
// talos/_common.yaml.tmpl) are embedded — plain //go:embed excludes them.
//
//go:embed all:templates
var builtinTemplates embed.FS

// Data is the model passed to every template.
type Data struct {
	Identity catalog.Identity
	Profile  catalog.Profile
	Vars     map[string]string
	// BaseURL is booty's own base URL (e.g. http://192.168.1.10:8080), used to
	// build absolute kernel/initrd/config URLs the client can fetch.
	BaseURL string
}

// Renderer executes booty's templates. Construct with New; it is safe for
// concurrent use (text/template execution is read-only).
type Renderer struct {
	tmpl *template.Template
}

// Option configures New.
type Option func(*config)

type config struct {
	overlay fs.FS
}

// WithTemplates layers a caller-supplied template set over the embedded one.
// The FS must mirror the embedded layout — templates in family subdirectories
// (ipxe/, talos/, cloud_init/, proxmox/, or new families), matched with the
// pattern "*/*". Templates are looked up by base filename, so an overlay file
// whose base name matches an embedded template replaces it, and new names are
// added. This is the seam for both the binary's --templates-dir override
// (os.DirFS) and a platform consumer's dynamic template set.
func WithTemplates(fsys fs.FS) Option {
	return func(c *config) { c.overlay = fsys }
}

// New parses the embedded templates, then applies any options.
func New(opts ...Option) (*Renderer, error) {
	var cfg config
	for _, o := range opts {
		o(&cfg)
	}
	t, err := template.New("booty").Funcs(funcs()).ParseFS(builtinTemplates, "templates/*/*")
	if err != nil {
		return nil, fmt.Errorf("parsing templates: %w", err)
	}
	if cfg.overlay != nil {
		// An overlay that matches nothing is a misconfiguration (wrong dir, flat
		// layout) — ParseFS surfaces it as an error rather than silently no-oping.
		t, err = t.ParseFS(cfg.overlay, "*/*")
		if err != nil {
			return nil, fmt.Errorf("parsing template overlay: %w", err)
		}
	}
	return &Renderer{tmpl: t}, nil
}

// IPXEScript renders the per-machine boot script for a resolution. A profile that
// declares a boot kernel gets the generic boot script; a profile whose render
// kind is "ipxe" (e.g. a rescue shell) gets its own named script instead.
func (r *Renderer) IPXEScript(id catalog.Identity, res *catalog.Resolution, baseURL string) (string, error) {
	data := Data{Identity: id, Profile: res.Profile, Vars: res.Vars, BaseURL: baseURL}

	switch {
	case res.Profile.Boot != nil && res.Profile.Boot.Kernel != "":
		return r.execute("boot.ipxe", data)
	case res.Profile.Render != nil && res.Profile.Render.Kind == "ipxe" && res.Profile.Render.Template != "":
		return r.execute(path.Base(res.Profile.Render.Template), data)
	default:
		return "", fmt.Errorf("profile %q has neither a boot kernel nor an ipxe render template", res.Profile.Name)
	}
}

// ChainScript renders the chainloading script iPXE runs first — the one that
// actually collects the machine's identity and passes it to booty. This is the
// script embedded in ipxe.efi (or served to nodes via DHCP option 175). Without
// it, iPXE sends no identity at all: that is the crucial correction to the naive
// mental model where /ipxe "just knows" the MAC.
func (r *Renderer) ChainScript(baseURL string) (string, error) {
	return r.execute("chain.ipxe", Data{BaseURL: baseURL})
}

// Config renders a profile's declared config document — a Talos machineconfig or
// a cloud-init user-data payload — from the template named by the profile's
// render block. The caller chooses the content type from the render kind. Unlike
// the iPXE script, a config profile MUST declare a render template.
func (r *Renderer) Config(id catalog.Identity, res *catalog.Resolution, baseURL string) (string, error) {
	if res.Profile.Render == nil || res.Profile.Render.Template == "" {
		return "", fmt.Errorf("profile %q declares no render template", res.Profile.Name)
	}
	return r.execute(path.Base(res.Profile.Render.Template),
		Data{Identity: id, Profile: res.Profile, Vars: res.Vars, BaseURL: baseURL})
}

// CloudInitMetaData renders the NoCloud meta-data document (instance identity).
// It uses a built-in template rather than the profile's, because meta-data shape
// is fixed by cloud-init, not by the profile.
func (r *Renderer) CloudInitMetaData(id catalog.Identity, res *catalog.Resolution, baseURL string) (string, error) {
	return r.execute("meta-data.yaml.tmpl",
		Data{Identity: id, Profile: res.Profile, Vars: res.Vars, BaseURL: baseURL})
}

func (r *Renderer) execute(name string, data Data) (string, error) {
	var b strings.Builder
	if err := r.tmpl.ExecuteTemplate(&b, name, data); err != nil {
		return "", fmt.Errorf("rendering %s: %w", name, err)
	}
	return b.String(), nil
}

func funcs() template.FuncMap {
	return template.FuncMap{
		"base": path.Base,    // initrd basename for the kernel cmdline
		"join": strings.Join, // join cmdline args
		"coalesce": func(vals ...string) string { // first non-empty
			for _, v := range vals {
				if v != "" {
					return v
				}
			}
			return ""
		},
	}
}
