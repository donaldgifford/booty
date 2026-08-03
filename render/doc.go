// Package render turns a catalog resolution into machine-consumable text.
//
// One template pipeline produces every output booty serves:
//
//   - iPXE scripts — the chainload script and the per-machine boot script.
//   - Talos machineconfig — the primary provisioning path.
//   - cloud-init NoCloud — meta-data, user-data, and vendor-data documents.
//   - Proxmox answer.toml — the automated-installer answer file.
//
// Rendering is the second of booty's two evaluation phases: a catalog's HCL is
// evaluated at load time by [github.com/donaldgifford/booty/catalog], and
// text/template is evaluated here at request time. Keeping them separate is
// deliberate — the config language and the output language never touch. iPXE's
// own ${...} variables pass through untouched because Go templates use {{...}}
// delimiters.
//
// Templates are embedded in the binary. [WithTemplates] overlays a directory on
// top of them so operators can override any single template without a rebuild.
//
// # Usage
//
// Construct a renderer once and reuse it; it is safe for concurrent use:
//
//	renderer, err := render.New()
//	if err != nil {
//		return err
//	}
//
//	// Operator overrides layered over the embedded defaults:
//	renderer, err = render.New(render.WithTemplates(os.DirFS("/etc/booty/templates")))
//	if err != nil {
//		return err
//	}
//
//	// id and res come from a catalog match:
//	//   res, err := cat.Match(id)
//	script, err := renderer.IPXEScript(id, res, "http://192.168.1.10:8080")
//
// Ground-up walkthrough:
// https://github.com/donaldgifford/booty/blob/main/docs/go-ipxe/06-render-pipeline.md
package render
