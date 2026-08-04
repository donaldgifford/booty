# Chapter 5: The Catalog and the Matcher

← [Chapter 4](./04-ipxe-deep-dive.md) | [Chapter 6: Render Pipeline →](./06-render-pipeline.md)

---

Chapters 3 and 4 moved bytes: TFTP handed off the iPXE binary, iPXE asked for a
script. But *which* script, kernel, and config should a given machine get? That
question — mapping a booting machine to its desired state — is the heart of a
provisioning service, and it's what we build here: `catalog`.

This is the chapter where `booty` stops being a file server and starts being a
*fleet* server. It's also where the first third-party dependency enters, under
its own ADR ([ADR-0001](../adr/0001-hcl-for-catalog-configuration.md)), so we'll
be deliberate about why.

Source: [`catalog/catalog.go`](../../catalog/catalog.go) (the
domain model + matcher) and
[`catalog/source.go`](../../catalog/source.go) (HCL loading).

## The model: identity → group → profile

We borrow the shape matchbox got right and drop the parts it got wrong. Three
concepts:

- **Identity** — the normalized attributes `booty` knows about a booting machine:
  MAC, UUID, serial, hostname, IP, arch, product, manufacturer. In production
  these arrive as query parameters on the iPXE request (Chapter 4); here they're
  just a struct.
- **Profile** — a named boot recipe: what to boot (`kernel`/`initrd`/`cmdline`)
  and what per-machine config to render. Profiles are the *what*.
- **Group** — a rule that binds machines matching a *selector* to a profile.
  Groups are the *who*.

The separation matters: one profile (`talos-worker`) is shared by many machines,
and each machine reaches it through a group. You maintain one recipe, not one per
node.

```text
  iPXE request                Catalog
 ┌──────────────┐        ┌──────────────────────────┐
 │ mac=d0:50:.. │        │ group "cp-01"            │
 │ arch=x86_64  │──┐     │   selector { mac = ... }  │──► profile "talos-control"
 │ uuid=...     │  │     │ group "workers"          │       boot { kernel, ... }
 └──────────────┘  │     │   selector { arch=x86_64 }│──► profile "talos-worker"
        Identity ──┘     │ group "unknown"          │──► profile "rescue"
                         │   (no selector = catch-all)│
                         └──────────────────────────┘
```

## Matching: specificity, ties, and the catch-all

A group's **selector** is a set of `attribute = value` terms. A machine matches a
group when *every* term matches its identity (logical AND). Among all matching
groups, the **most specific** wins — specificity is simply the number of selector
terms. This gives the intuitive precedence for free:

- `selector { mac = "d0:50:99:a2:3b:40" }` (1 term, but a very targeted one)
- `selector { arch = "x86_64" }` (1 term, broad)
- `` (0 terms — the catch-all)

A per-MAC group and a per-arch group both have one term, so specificity alone
doesn't order them. When a machine matches both, the term counts tie, and we break
the tie **deterministically by group name** so the same machine always resolves
the same way. (A production system might warn on ambiguous ties; we keep it
predictable.) The whole algorithm is small enough to read at once:

```go
func (c *Catalog) Match(id Identity) (*Resolution, error) {
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
	// … resolve profile, merge vars …
}
```

An **empty selector matches everything at specificity 0** — that's the catch-all,
and it's the single most operationally important entry in the catalog. It's the
difference between an unknown node dropping to a rescue shell and an unknown node
hanging with no feedback at 2 a.m. `TestMatchSpecificity` pins all three tiers;
`TestMatchNoMatch` pins that a catalog *without* a catch-all returns `ErrNoMatch`
so the caller can decide what to serve.

**Variable precedence.** The resolution merges variables profile-first, then group
(`maps.Copy(vars, prof.Vars); maps.Copy(vars, best.Vars)`), so a group can
override a profile default — e.g. every worker shares `role = "worker"` from the
profile, but each per-machine group supplies its own `hostname`.

### MAC normalization is a matching concern

Selectors and identities rarely agree on MAC spelling — a config file might have
`D0-50-99-A2-3B-40`, the wire delivers `d0:50:99:a2:3b:40`. So both sides run
through `NormalizeMAC` before comparison. Everything collapses to lowercase colon
form; a 12-hex-digit string in any of the common separators (`:`, `-`, `.`, none)
canonicalizes, and anything that *isn't* a MAC is lowercased but left intact
rather than mangled. `TestNormalizeMAC` and `TestMatchMACNormalization` cover it.

## Why HCL — and where it is (and isn't)

The catalog is authored by humans, and the matchbox pain point is duplication:
fifty near-identical node definitions. JSON can't help (no comments, no
variables); YAML anchors barely help. We want a *typed config language* with
variables, locals, functions, and interpolation. That's HCL.

The decision, its admission-test justification, and its boundary are recorded in
[ADR-0001](../adr/0001-hcl-for-catalog-configuration.md). The two rules worth
repeating here:

1. **HCL is the input format only.** Outputs — Talos machineconfig, `answer.toml`,
   cloud-init, iPXE scripts — are dictated by their consumers and produced by
   `text/template` in Chapter 6, *not* by HCL. A happy consequence: `booty` needs
   no YAML or TOML emitter library.
2. **HCL types never leak.** `catalog.go` imports neither `hcl` nor `cty`;
   `DirSource.Load` returns plain `Catalog`/`Profile`/`Group` values. All the
   third-party machinery is quarantined in `source.go`. That's PLAN-0001's P3, and
   it's what will let a `git://` or `platform://` source slot in later without
   touching the matcher.

Here is a real catalog (from [`examples/catalog/`](../../examples/catalog)):

```hcl
variable "talos_version" { default = "v1.7.6" }
variable "cluster"       { default = "home" }

locals {
  boot_base      = "talos/${var.talos_version}"
  common_cmdline = ["console=ttyS0,115200n8", "console=tty0", "talos.platform=metal"]
}

profile "talos-worker" {
  boot {
    kernel  = "${local.boot_base}/vmlinuz"
    initrd  = "${local.boot_base}/initramfs.xz"
    # $${mac} escapes to a literal ${mac} so iPXE substitutes it at boot.
    cmdline = concat(local.common_cmdline, ["talos.config=http://boot.${var.cluster}.local:8080/machine-config?mac=$${mac}"])
  }
  render { kind = "talos-machineconfig"  template = "talos/worker.yaml.tmpl" }
  vars   { role = "worker"  cluster = var.cluster }
}

group "worker-01" {
  profile  = "talos-worker"
  selector = { mac = "d0:50:99:b3:4c:50" }
  vars     = { hostname = "talos-worker-01" }
}

group "unknown" { profile = "rescue" }   # catch-all
```

Two escaping details that bite everyone: `${var.talos_version}` is evaluated by
HCL at load time, while `$${mac}` is HCL's escape for a *literal* `${mac}` that
must survive untouched so iPXE substitutes the booting machine's MAC into the URL
at boot. Same-looking syntax, two different evaluation times — exactly the kind of
seam this guide exists to make explicit.

## Loading: two-phase evaluation

HCL isn't just parsed here — it's *evaluated* as a small language. That's what
`variable`, `locals`, and function calls require, and it forces a two-phase load
(the interesting part of `source.go`):

**Phase 1 — build the EvalContext.** Before we can decode `profile`/`group`
blocks (whose expressions reference `var.*`, `local.*`, and functions), those
values must exist:

- `evalVariables` reads each `variable` block's `default` (functions available,
  but no var/local references yet) and applies any override.
- `evalLocals` is the subtle one. A local may reference `var.*` *and other
  locals*, in any order. So we evaluate to a **fixed point**: loop over the
  unresolved locals, evaluate any whose dependencies are now known, repeat. If a
  full pass resolves nothing new, the survivors form a cycle or reference
  something undefined — and we say so, by name:

  ```go
  if !progressed {
      // … collect unresolved names …
      return nil, fmt.Errorf("cannot resolve locals %s (cycle or undefined reference): %s",
          strings.Join(stuck, ", "), lastErr)
  }
  ```

  `TestLoadErrors/"locals cycle"` feeds it `a = local.b; b = local.a` and asserts
  that message.

**Phase 2 — decode with context.** With `var`, `local`, and functions assembled
into one `hcl.EvalContext`, we `gohcl.DecodeBody` each `profile` and `group` block
into plain structs. Interpolations and function calls resolve during this decode.

### The function set is curated on purpose

The EvalContext exposes a deliberately small slice of the cty standard library
(`upper`, `join`, `concat`, `coalesce`, `merge`, `format`, `regex`, …) plus one
booty-specific function, `mac_bare`, which strips separators from a MAC for use in
filenames. It demonstrates how a project extends the language:

```go
var macBareFunc = function.New(&function.Spec{
	Params: []function.Parameter{{Name: "mac", Type: cty.String}},
	Type:   function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		s := strings.NewReplacer(":", "", "-", "", ".", "").Replace(strings.ToLower(args[0].AsString()))
		return cty.StringVal(s), nil
	},
})
```

This is a config language, not a runtime — the small surface is the point.

## DirSource, and the interface that isn't here

`DirSource` globs `*.hcl` in a directory, parses them in sorted order, and merges
them into one body (so splitting variables/profiles/groups across files is purely
organizational — `TestLoadMultipleFilesMerge` proves a variable in one file and a
group in another resolve together):

```go
type DirSource struct{ Root string }

func (s DirSource) Load(ctx context.Context) (*Catalog, error)
func (s DirSource) String() string
```

That `ctx` isn't used by the local dir loader beyond a cancellation check, but
it's in the signature because the *next* sources — a `git://` clone loop, a
`platform://` API poll — will need it (PLAN-0001's source-loop roadmap).

This package used to export a `Source` interface with `DirSource` as its only
implementation, and nothing in the module ever accepted one. That is worth
dwelling on, because "define the interface now so the implementations are
swappable later" is an instinct carried in from languages where it's necessary,
and in Go it isn't. Interfaces are satisfied **implicitly**: nothing declares
that it implements anything. So a consumer that wants to swap sources writes
the interface it needs, in its own package —

```go
// in the platform's package, not booty's
type catalogSource interface {
	Load(ctx context.Context) (*catalog.Catalog, error)
}
```

— and `catalog.DirSource` satisfies it with no change to booty at all. The
interface ends up describing what the **caller** requires, which is the only
place that information actually lives. An interface exported next to its single
implementation describes nothing; it just makes the package's surface bigger and
commits you to a method set before anyone has asked for one.

The general form: accept interfaces, return structs — and let the accepter
declare them.

## Try it yourself

The `validate` subcommand loads a catalog and exits non-zero on any problem — it's
the gate a config repo runs in CI ("does this parse and resolve?"):

```bash
go build -o bin/booty ./cmd/booty

# The example catalog resolves cleanly:
./bin/booty validate --catalog examples/catalog
# -> ok: dir://examples/catalog — 4 profiles, 5 groups   (exit 0)

# A dangling profile reference is caught, with an actionable message:
mkdir -p /tmp/bad && echo 'group "g" { profile = "ghost" }' > /tmp/bad/c.hcl
./bin/booty validate --catalog /tmp/bad
# -> invalid catalog … group "g": references unknown profile "ghost"   (exit 1)
```

`serve` accepts the same `--catalog` and loads it at boot, so a broken catalog
fails fast instead of at the first PXE request:

```bash
./bin/booty serve --catalog examples/catalog --log-format json
# … {"msg":"catalog loaded","source":"dir://examples/catalog","profiles":4,"groups":5}
```

The handlers that actually *use* the resolved profile — the iPXE script endpoint
(Chapter 4) and the machineconfig endpoint (Chapter 6) — turn this resolution into
bytes a machine consumes. What we have now is the brain: give it an identity, it
returns the profile and variables. The rest is rendering and serving.

## What we deliberately left out (for now)

- **List selectors** (`mac = ["a", "b"]` to match any of several). Today a selector
  value is a single string; matching several MACs means several groups. Easy to
  add to `matchSelector` later.
- **Server config in HCL.** Server flags (`--http-addr`, …) are still plain flags;
  moving them into an HCL config file reuses this same loader and is a later,
  small step.
- **`git://` / `platform://` sources.** The interface is ready; the
  implementations come after v1's dir-only path is solid (PLAN-0001).

---

← [Chapter 4](./04-ipxe-deep-dive.md) | [Chapter 6: Render Pipeline →](./06-render-pipeline.md)
