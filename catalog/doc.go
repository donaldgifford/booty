// Package catalog is booty's desired-state model for network boot.
//
// A catalog is a set of boot profiles plus the group rules that match a
// booting machine to one of them. It is fed by a [Source] — a directory of
// HCL files today — and answers one question at request time: this machine
// has these identity attributes, so which profile and which variables apply
// to it?
//
// The matching model is deliberately matchbox-shaped — groups select machines
// by identity attributes and bind them to profiles — because that separation
// is the part of matchbox worth keeping.
//
// The domain types here carry no HCL or cty types. All HCL decoding and
// expression evaluation is quarantined in the [Source] implementation, so the
// config language never leaks into the matcher or the renderers.
//
// # Usage
//
// Load a catalog from a directory of HCL files, then match a machine:
//
//	cat, err := catalog.DirSource{Root: "examples/catalog"}.Load(ctx)
//	if err != nil {
//		return err
//	}
//
//	res, err := cat.Match(catalog.Identity{MAC: "de:ad:be:ef:00:01"})
//	if err != nil {
//		// Two distinct failures, with opposite remedies:
//		//   errors.Is(err, catalog.ErrNoMatch)       — no group selects this
//		//                                              machine; add one.
//		//   errors.Is(err, catalog.ErrUnknownProfile) — a group does select it
//		//                                              but names a profile the
//		//                                              catalog never defines;
//		//                                              fix the catalog.
//		return err
//	}
//	fmt.Println(res.Group, res.Vars)
//
// Ground-up walkthrough:
// https://github.com/donaldgifford/booty/blob/main/docs/go-ipxe/05-catalog-and-matcher.md
package catalog
