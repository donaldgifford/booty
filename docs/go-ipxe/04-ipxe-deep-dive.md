# Chapter 4: iPXE — What It Is and What It Actually Sends

← [Chapter 3](./03-tftp-from-scratch.md) | [Chapter 5: Catalog & Matcher →](./05-catalog-and-matcher.md)

---

Chapter 3 handed the firmware one file over TFTP: the iPXE binary. This chapter is
about what iPXE does once it's running — and, crucially, about a misconception
that quietly breaks most first attempts at a boot service. By the end we'll have
`booty` serving real boot scripts: the chainload script that collects a machine's
identity, and the per-machine script that boots it, rendered from the profile the
catalog matched (Chapter 5).

Source: [`render`](../../render) (the script templates and
renderer) and [`httpsrv`](../../httpsrv) (the `/ipxe`,
`/boot.ipxe`, and `/boot/` handlers).

## What iPXE is

iPXE is open-source network-boot firmware — a boot *environment*, not just a
loader. Where the NIC's built-in PXE ROM can do little more than TFTP, iPXE adds
HTTP/HTTPS, a scripting language with variables and conditionals, menus, and
signature verification. It's how a 1981 protocol (TFTP) bootstraps a 2020s
capability (HTTP boot of signed images). It reaches a machine one of three ways:

1. **Chainloaded via TFTP** — the NIC ROM TFTP-downloads `ipxe.efi` and runs it
   (Chapter 3). The common case.
2. **Built into the NIC** — some Mellanox/Intel NICs ship iPXE in firmware.
3. **Built into UEFI** — e.g. Dell "HTTP Boot".

In all three, the moment that matters next is: iPXE has a network and now needs a
*script* telling it what to boot.

## The misconception that breaks everything

Here is the trap. You stand up an endpoint, point iPXE at it, and expect a request
like:

```
GET /ipxe?mac=52:54:00:ab:cd:ef&uuid=...&arch=...
```

**iPXE does not send any of that on its own.** A stock `ipxe.efi` fetches whatever
URL it was handed and nothing more — no MAC, no UUID, no query string. Those
parameters exist only if *you* put them there, in a script that iPXE runs first.
Miss this and `/ipxe` is called with an empty query, matching nothing, and the
node boots to a rescue shell (or hangs) with no obvious cause.

So a booting machine actually runs **two** scripts:

1. **The chainload script** — static, tiny, run first. Its only job is to read
   iPXE's built-in settings (`${mac}`, `${uuid}`, `${buildarch}`, …) and pass them
   to booty as query parameters. This is what's embedded in `ipxe.efi`, or handed
   to already-running iPXE via DHCP option 175.
2. **The per-machine boot script** — dynamic, returned by `/ipxe`, rendered from
   the profile the catalog matched using the identity the chainload script
   supplied.

booty serves the first at `/boot.ipxe` (from
[`chain.ipxe`](../../render/templates/ipxe/chain.ipxe)):

```ipxe
#!ipxe
chain {{ .BaseURL }}/ipxe?mac=${mac}&ip=${ip}&uuid=${uuid}&serial=${serial}&arch=${buildarch}&product=${product}&manufacturer=${manufacturer} || goto failed
```

`${mac}`, `${ip}`, and friends are **iPXE settings**, expanded by iPXE on the
booting machine. `{{ .BaseURL }}` is a **Go template field**, expanded by booty
before it sends the script. That both use dollar-and-brace-ish syntax is a
coincidence; they use different delimiters (`${...}` vs `{{...}}`) and are
evaluated at different times by different engines, so they never collide. Getting
comfortable with that split is most of what this chapter teaches.

To deploy it, you either bake this script into `ipxe.efi` at build time (`iPXE`'s
`EMBED` option) or, for nodes already running iPXE, point DHCP option 175 at
`http://booty:8080/boot.ipxe`. (Building/pinning `ipxe.efi` itself is a separate
concern — PLAN-0001 evaluates `tinkerbell/ipxedust` for prebuilt, embedded
binaries; until then, `wget https://boot.ipxe.org/ipxe.efi` with a custom embed
script works.)

## The `/ipxe` contract

iPXE is picky about what it will execute. The handler must return:

1. HTTP **200** — even for "sorry, I can't help you" responses. On several
   firmware versions a non-200 status drops iPXE into an unhelpful hang rather
   than running a fallback.
2. `Content-Type: text/plain`.
3. A body starting with `#!ipxe`.

That shapes the error strategy: booty never returns a 404 or 500 from `/ipxe`.
When nothing matches, it returns a 200 script that drops to a shell and explains
why; when rendering fails, likewise. `writeIPXE` centralizes this:

```go
func writeIPXE(w http.ResponseWriter, script string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK) // iPXE expects 200 even for our "error" scripts
	_, _ = io.WriteString(w, script)
}
```

## Identity → match → render

The `/ipxe` handler is the seam where every earlier chapter meets. It reads the
identity the chain script supplied, asks the catalog (Chapter 5) which profile
applies, and renders that profile's boot script:

```go
func (s *Server) handleIPXE(w http.ResponseWriter, r *http.Request) {
	id := identityFromQuery(r.URL.Query())
	res, err := s.catalog.Match(id)
	if err != nil {
		writeIPXE(w, noMatchScript(id))   // 200 shell script, never a 404
		return
	}
	script, err := s.renderer.IPXEScript(id, res, s.effectiveBaseURL(r))
	if err != nil {
		writeIPXE(w, errorScript("render failed for profile "+res.Profile.Name))
		return
	}
	writeIPXE(w, script)
}
```

`identityFromQuery` maps the query parameters onto a `catalog.Identity`, and
`normalizeArch` folds iPXE's `${buildarch}` spellings (`x86_64`, `arm64`, `i386`,
and their aliases) to the values catalog selectors use. MAC normalization happens
inside the matcher, so `mac=D0-50-99-B3-4C-50` in the query still matches a
`d0:50:99:...` selector.

### The base URL, and why it's derived

The rendered script needs absolute URLs for the kernel and initrd
(`http://booty/boot/...`), which means booty must know its own address. Rather than
force configuration, `effectiveBaseURL` uses the `--url` flag if set and otherwise
derives it from the request's `Host` header:

```go
func (s *Server) effectiveBaseURL(r *http.Request) string {
	if s.baseURL != "" {
		return s.baseURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
```

So booty works out of the box on a laptop and can be pinned to a stable external
URL in production. `TestIPXEBaseURLFromHost` covers the derivation.

## The renderer

Rendering is booty's second evaluation phase (the first was HCL, Chapter 5). It's
`text/template` over an embedded template set, with a small data model:

```go
type Data struct {
	Identity catalog.Identity
	Profile  catalog.Profile
	Vars     map[string]string
	BaseURL  string
}
```

`IPXEScript` chooses which template to run based on the profile: a profile with a
boot kernel gets the generic [`boot.ipxe`](../../render/templates/ipxe/boot.ipxe);
a profile whose render kind is `ipxe` (the rescue catch-all) gets its own script;
a profile with neither is a hard error, not a silent empty response.

```go
switch {
case res.Profile.Boot != nil && res.Profile.Boot.Kernel != "":
	return r.execute("boot.ipxe", data)
case res.Profile.Render != nil && res.Profile.Render.Kind == "ipxe" && res.Profile.Render.Template != "":
	return r.execute(path.Base(res.Profile.Render.Template), data)
default:
	return "", fmt.Errorf("profile %q has neither a boot kernel nor an ipxe render template", res.Profile.Name)
}
```

The generic boot script is Talos-correct out of the box — note `initrd={{ base
.Profile.Boot.Initrd }}` puts the initrd's basename into the kernel command line
(Talos wants `initrd=initramfs.xz` there) while the separate `initrd` line loads
it by URL:

```ipxe
kernel {{ .BaseURL }}/boot/{{ .Profile.Boot.Kernel }} initrd={{ base .Profile.Boot.Initrd }}{{ range .Profile.Boot.Cmdline }} {{ . }}{{ end }}
initrd {{ .BaseURL }}/boot/{{ .Profile.Boot.Initrd }}
boot || goto failed
```

### One real bug worth keeping

The first version of `boot.ipxe` used `{{ coalesce .Vars.hostname .Identity.MAC }}`.
It passed every test — until a profile with *no* variables hit it in production-shaped
code, and text/template returned `rendering boot.ipxe: <.Vars.hostname>: invalid
value; expected string`. Indexing a **missing map key** with field syntax yields
template's untyped `<no value>`, which can't be passed to a `func(...string)`. The
fix is `index`, which returns the element zero value (`""`) for an absent key:

```ipxe
{{ coalesce (index .Vars "hostname") .Identity.MAC }}
```

The lesson generalizes: `.Map.key` is unsafe for optional keys in templates; reach
for `index`. `TestIPXENoMatchServesShell` and the httpsrv match test now exercise
the empty-vars path so this can't regress.

## Serving the kernel and initrd

The boot script's kernel/initrd URLs point at `/boot/...`, served by `handleBoot`
from the boot directory. It reuses the same path-traversal neutralization as the
TFTP server, then hands off to `http.ServeFile`, which gives Range requests
(resumable downloads), `ETag`/`Last-Modified`, `HEAD`, and sendfile-backed
streaming — everything a 100 MB initrd transfer wants — for free:

```go
http.ServeFile(w, r, fullAbs)
```

HTTP here is a massive win over TFTP's stop-and-wait: full TCP throughput, no
per-block round trips. That's exactly why the boot staged through TFTP only far
enough to get iPXE, then switched to HTTP for the big files.

## Golden-file tests

Boot scripts are the "why did node X get *that*?" artifact, so the renderer is
tested with golden files (`render/testdata/*.golden`, refreshed with `go
test ./render -update`). The golden pins the exact bytes; alongside it,
invariant assertions catch the things that must always hold regardless of golden
churn — the `#!ipxe` prefix, the kernel URL, the `initrd=` basename, and that a
literal `${mac}` survives untouched. This is the same golden-file discipline
PLAN-0001's `render` verb is built on.

## Try it yourself

The whole chapter, from a running binary:

```bash
go build -o bin/booty ./cmd/booty
mkdir -p /tmp/boot/talos/v1.7.6 && echo KERNEL > /tmp/boot/talos/v1.7.6/vmlinuz

./bin/booty serve --catalog examples/catalog --boot-dir /tmp/boot \
  --url http://localhost:8080 &

# 1. The chainload script iPXE would run first — note it carries the ${...} vars:
curl -s localhost:8080/boot.ipxe

# 2. A known worker MAC resolves to its profile, hostname, kernel and initrd:
curl -s "localhost:8080/ipxe?mac=d0:50:99:b3:4c:50&arch=x86_64"

# 3. An unknown MAC falls to the catch-all rescue profile (still HTTP 200):
curl -s "localhost:8080/ipxe?mac=00:00:00:00:00:99&product=SuperServer"

# 4. The kernel is served over HTTP:
curl -s localhost:8080/boot/talos/v1.7.6/vmlinuz
```

Request 2 returns a boot script whose kernel line points at
`localhost:8080/boot/talos/v1.7.6/vmlinuz`, carries `initrd=initramfs.xz`, and ends
with `talos.config=...?mac=${mac}` — that `${mac}` left for iPXE to fill in. You've
now watched an identity travel from a query string, through the matcher, into a
rendered boot script. That's the spine of the whole service.

## What's deferred

- **Building/pinning `ipxe.efi`.** Getting the binary (with the chain script
  embedded) is an asset-provenance problem, not a serving one — PLAN-0001 evaluates
  `ipxedust`. booty *serves* the chain script; embedding it is a build step.
- **The machineconfig / cloud-init endpoints.** The boot cmdline references
  `talos.config=.../machine-config?mac=${mac}`; that endpoint — and the Talos and
  cloud-init renderers behind it — is Chapter 6.
- **iPXE menus and multi-stage chaining.** booty resolves server-side via the
  catalog, so interactive menus are a niche fallback rather than the main path.

---

← [Chapter 3](./03-tftp-from-scratch.md) | [Chapter 5: Catalog & Matcher →](./05-catalog-and-matcher.md)
