# Chapter 6: The Render Pipeline — Talos-First

← [Chapter 5](./05-catalog-and-matcher.md) | [Chapter 7: HTTP Serving Core →](./06-http-server-stdlib.md)

---

Chapter 4 rendered the boot script, and its kernel command line ended with a
promise:

```text
talos.config=http://boot.home.local:8080/machine-config?mac=${mac}
```

That URL is a loose end. The kernel boots, Talos starts, and it immediately tries
to `GET` its machine configuration from there — and so far booty has nothing to
answer with. This chapter ties it off: we build the `/machine-config` endpoint and
the Talos machineconfig renderer behind it, then add cloud-init as a secondary
path for the non-Talos machines in the fleet, and finish with the Proxmox
automated-install answer — a fourth render kind with the richest identity model
of them all.

This is the third and final evaluation phase in booty's pipeline. HCL is evaluated
at load time (Chapter 5); the iPXE script is rendered at boot time (Chapter 4);
the *config document* is rendered here, when the booted OS phones home for it.
Same `text/template` engine as Chapter 4, same `render` package — we're
extending the renderer we already built, not starting a new one.

Source: [`render`](../../render) (the `Config` renderer and the
Talos/cloud-init/Proxmox templates) and
[`httpsrv`](../../httpsrv) (the `/machine-config`,
`/cloud-init/*` and `/proxmox/answer` handlers).

## Three ways an OS asks "who am I?"

The boot script got the machine *running*. Now the running OS needs its
configuration — the hostname, the cluster to join, the users to create. booty
serves three delivery models, and they differ in one decisive respect: **how the
OS identifies itself when it asks.**

- **Talos** pulls its config from the URL we put in the kernel cmdline, and iPXE
  already substituted `${mac}` into that URL at boot time. So identity arrives the
  same way it did at `/ipxe`: as query parameters. `GET /machine-config?mac=d0:...`
  is a request booty can *match* against the catalog exactly like the iPXE request.
- **cloud-init** (the NoCloud data source) is the opposite. It sends **no
  identifiers at all** — no MAC, no query string, nothing in the body. It just
  `GET`s `/meta-data`, `/user-data`, `/vendor-data` from a fixed base URL. The only
  thing booty knows about the requester is its **source IP**. That single fact is
  what the catalog has to match on.
- **Proxmox** (the automated installer, PVE 8.2+) goes the other direction
  entirely: it **POSTs a JSON document** describing the machine — full DMI system
  identity (UUID, serial, product, manufacturer) plus *every* NIC's MAC — and
  expects its `answer.toml` back. The richest identity of the three, delivered in
  the request body.

That spectrum drives everything below. Talos config is identity-rich and matches
like `/ipxe`; cloud-init is identity-poor and forces us to match on the client
address — which means cloud-init profiles need `ip` selectors in the catalog,
where Talos profiles get to key off MAC. Proxmox is identity-*flooded*: so many
candidate identifiers that the problem inverts from "what do I match on?" to
"which of these matches should win?" — the section at the end of this chapter.

## Talos-first is not machinery-first

We lead with Talos because it's the fleet's target. But "Talos-first" is a
decision about *ordering and defaults*, not about pulling in Talos's Go libraries.
There are two different things it could mean:

1. **Serve hand-templated machineconfig YAML** — booty owns a `text/template`
   that emits `v1alpha1` machine config, exactly as if you'd written the YAML by
   hand and filled in a few variables.
2. **Generate config with `siderolabs/machinery`** — Talos's own Go module that
   builds a typed, schema-validated config *and* the secrets bundle (PKI, tokens,
   cluster id/secret).

This chapter does (1). Option (2) is the correct production answer — it's the only
way to get the secrets right — but it's a dependency with real surface area, so it
gets its own ADR and lands *after* the walkthrough (it's a PLAN-0001 item). The
templates below are deliberately, visibly incomplete about secrets:

```yaml
# NOTE: secrets (machine.token, machine.ca, cluster.id/secret, PKI) are intentionally
# omitted. In production the Talos `machinery` secrets bundle supplies them behind an
# owned interface; hand-templating them would be insecure and schema-incorrect.
# machinery is the deferred upgrade — see docs/adr and PLAN-0001.
```

Leaving that comment *in the rendered output* is intentional: it makes the seam
where machinery plugs in impossible to miss, and it stops anyone from mistaking a
walkthrough artifact for a production config.

## One renderer method for both

The iPXE path had `IPXEScript`. The config path gets `Config`, and it's simpler,
because unlike a boot script — which can come from a `boot{}` block *or* a rescue
`render{}` block — a config document is *always* named by the profile's `render`
template:

```go
func (r *Renderer) Config(id catalog.Identity, res *catalog.Resolution, baseURL string) (string, error) {
	if res.Profile.Render == nil || res.Profile.Render.Template == "" {
		return "", fmt.Errorf("profile %q declares no render template", res.Profile.Name)
	}
	return r.execute(path.Base(res.Profile.Render.Template),
		Data{Identity: id, Profile: res.Profile, Vars: res.Vars, BaseURL: baseURL})
}
```

The same `Data` model from Chapter 4 flows through — `Identity`, `Profile`,
`Vars`, `BaseURL`. A Talos worker profile and an Ubuntu cloud-init profile use the
*same* method; only the template named in their `render{}` block differs. The
render `kind` (`talos-machineconfig` vs `cloud-init`) isn't consulted here — it's
the *handler's* job to reject a mismatch, which we'll see below. The renderer just
executes the template it's told to.

## Shared partials, and the embed gotcha that bit us

Worker and control-plane machineconfigs share most of a `machine:` section. Rather
than copy it, the templates factor it into a partial with `{{ define }}`, in
[`talos/_common.yaml.tmpl`](../../render/templates/talos/_common.yaml.tmpl):

```yaml
{{- define "talos-machine" -}}
machine:
  type: {{ index .Vars "role" }}
  install:
    disk: {{ or (index .Vars "install_disk") "/dev/sda" }}
    image: ghcr.io/siderolabs/installer:{{ index .Vars "talos_version" }}
    wipe: false
  network:
    hostname: {{ or (index .Vars "hostname") .Identity.MAC }}
  kubelet:
    defaultRuntimeSeccompProfileEnabled: true
{{- end -}}
```

Both role templates then compose it in with a one-liner —
[`worker.yaml.tmpl`](../../render/templates/talos/worker.yaml.tmpl):

```yaml
version: v1alpha1
persist: true
{{ template "talos-machine" . }}
cluster:
  clusterName: {{ index .Vars "cluster" }}
  controlPlane:
    endpoint: {{ index .Vars "cluster_endpoint" }}
```

The control-plane template is the same shape plus the `apiServer`/`etcd`/scheduler
stanzas a control plane needs. `{{ template "talos-machine" . }}` — note the
trailing `.`, which passes the current data down; forget it and the partial
renders against `nil` and every `.Vars` lookup comes back empty.

Here's the bug that cost an afternoon. The partial file is named
`_common.yaml.tmpl`, with a leading underscore, because it's not a standalone
document. But `go:embed` has a rule most people learn the hard way:

> By default, patterns don't match files whose names start with `.` or `_`.

So `//go:embed templates` silently *skipped* `_common.yaml.tmpl`. Everything
compiled. Every worker/control-plane render then failed at runtime with `template
"talos-machine" not defined` — because the file defining it was never embedded.
The fix is the `all:` prefix, which overrides that exclusion:

```go
//go:embed all:templates
var builtinTemplates embed.FS
```

The lesson: if you name partials with a leading `_` (a good convention — it flags
"not a top-level template" at a glance), you *must* embed with `all:`. And gofmt
will insist on a blank comment line between the doc comment and the `//go:embed`
directive, since it treats the directive as a distinct comment group.

## The `/machine-config` endpoint

Now the endpoint that keeps the cmdline's promise. Identity arrives as query
parameters — Talos fetched a URL that iPXE had already expanded `${mac}` into — so
we reuse `identityFromQuery` and `Match` verbatim from `/ipxe`:

```go
func (s *Server) handleMachineConfig(w http.ResponseWriter, r *http.Request) {
	id := identityFromQuery(r.URL.Query())
	res, err := s.catalog.Match(id)
	if err != nil {
		http.Error(w, "no machine config for this machine", http.StatusNotFound)
		return
	}
	if res.Profile.Render == nil || res.Profile.Render.Kind != "talos-machineconfig" {
		http.Error(w, "profile "+res.Profile.Name+" is not a talos machineconfig", http.StatusConflict)
		return
	}
	out, err := s.renderer.Config(id, res, s.effectiveBaseURL(r))
	if err != nil {
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_, _ = io.WriteString(w, out)
}
```

There's a contract difference from `/ipxe` worth dwelling on. `/ipxe` could
**never** return a non-200, because iPXE firmware hangs on error statuses (Chapter
4). `/machine-config` is the opposite: **ordinary HTTP status codes are exactly
right here**, because Talos *retries*. A machine that boots before its catalog
group exists should get a clean `404` and keep asking, not a fabricated 200 with
an empty config it would then try to apply. So the endpoint uses real codes:

| Situation | Status | Why |
|-----------|--------|-----|
| Matched a `talos-machineconfig` profile | `200` | Serve the rendered YAML |
| No catalog match | `404` | Unknown machine; Talos retries until a group appears |
| Matched, but the profile isn't a machineconfig | `409` | e.g. it's a cloud-init or rescue profile — a config mistake, not a transient one |
| Template execution failed | `500` | A bug in the template or missing var |

That `409` is the interesting one. It fires when a machine matches a profile that
*renders* something, but not a Talos machineconfig — say it matched the `rescue`
catch-all. Returning the rescue script's bytes as if they were a machineconfig
would be worse than useless, so the kind mismatch is a hard, distinct error the
operator can see in the logs. `TestMachineConfigWrongKind` pins this: it points a
booting profile (one with a `boot{}` block but no machineconfig `render{}`) at
`/machine-config` and asserts a `409`.

The content type is `application/yaml`; Talos doesn't strictly require it, but it
makes `curl` and browser debugging sane.

## cloud-init: identity by source IP

cloud-init's NoCloud data source is the awkward one. The kernel cmdline points it
at a seed URL (`ds=nocloud-net;s=http://booty:8080/cloud-init/`) and it fetches
three fixed paths under there:

- `meta-data` — instance identity (an `instance-id` and hostname). Required.
- `user-data` — the `#cloud-config` document (users, packages, runcmd). The payload.
- `vendor-data` — an optional lower-precedence layer.

None of those requests carry a MAC or any identifier. cloud-init assumes the seed
URL is already machine-specific (in the classic setup you'd bake a per-machine
URL into each image). booty doesn't get that luxury — every node hits the same
`/cloud-init/` base — so the **only** discriminator is the TCP source address:

```go
func (s *Server) cloudInitResolve(w http.ResponseWriter, r *http.Request) (catalog.Identity, *catalog.Resolution, bool) {
	id := catalog.Identity{IP: clientIP(r)}
	res, err := s.catalog.Match(id)
	if err != nil {
		http.Error(w, "no data for this instance", http.StatusNotFound)
		return id, nil, false
	}
	return id, res, true
}
```

`clientIP` just splits the host off `r.RemoteAddr`. The consequence for the
catalog is concrete: **a cloud-init profile must be reachable by an `ip` selector**,
because the source IP is all the matcher will have. A group like:

```hcl
group "ubuntu-01" {
  profile  = "ubuntu"
  selector = { ip = "192.168.1.50" }
}
```

That IP is typically the DHCP-assigned lease, so in a real deployment the DHCP
reservation and the catalog `ip` selector are two views of the same fact and must
agree. This is the single biggest operational sharp edge in cloud-init boot, and
it's *inherent to NoCloud* — not something booty imposes.

The three handlers layer on top of `cloudInitResolve`:

- **meta-data** renders a fixed-shape document (`CloudInitMetaData`) — its shape is
  dictated by cloud-init, not the profile, so it uses a built-in template rather
  than the profile's:

  ```yaml
  instance-id: booty-{{ or (index .Vars "hostname") .Identity.MAC }}
  local-hostname: {{ or (index .Vars "hostname") .Identity.MAC }}
  ```

  The `instance-id` must be *stable* across reboots — cloud-init treats a changed
  `instance-id` as "new machine, re-run everything," so we derive it from the
  hostname (or MAC), never from anything time- or request-varying.

- **user-data** renders the profile's own template via the same `Config` method
  Talos uses, but first enforces `render.kind == "cloud-init"` (the mirror of the
  machineconfig `409` check). The body must begin with a header cloud-init
  recognizes — `#cloud-config` — or it's silently ignored by the client.

- **vendor-data** returns `200` with an empty body. It's optional, and an empty
  200 is the least surprising "nothing here" — a 404 would show up as a scary line
  in cloud-init's logs on every boot.

## Proxmox: identity in the POST body

The fourth render kind serves **Proxmox VE's automated installation** (PVE 8.2+),
and it completes the identity spectrum. Where Talos sends query parameters and
cloud-init sends nothing, the Proxmox installer *pushes* a full system
description at booty and asks for its install answer in return.

The flow starts before any machine boots. You prepare the installer once with
Proxmox's assistant, pointing it at booty:

```bash
proxmox-auto-install-assistant prepare-iso proxmox.iso \
  --fetch-from http --url http://boot.home.local:8080/proxmox/answer \
  --pxe --pxe-loader ipxe \
  --answer-auth-token booty:s3cret        # optional; see auth below
```

`--pxe` emits a kernel + initrd you stage under booty's boot dir (the
`proxmox-host` profile in `examples/catalog` boots them, with
`proxmox-start-auto-installer` on the cmdline to enable automated mode). At
install time, the installer brings up networking and **POSTs a JSON system
report** to the baked-in URL — and expects a TOML answer file back:

```text
Installer → POST /proxmox/answer   {"dmi": {"system": {...}}, "network_interfaces": [...]}
booty     → 200 application/toml   [global] fqdn = "pve-01.home.local" …
```

This is why the answer *can't* come from a static file server: the request is a
`POST`, which a file server answers with `405`. The endpoint must be something
that can read a body, decide *which* machine is asking, and respond per-machine.
That's exactly the shape booty already is — match, then render — which makes
this less a new feature than a new door into the same pipeline.

booty decodes only the subset of the report it matches on:

```go
type proxmoxSysInfo struct {
	DMI struct {
		System struct {
			UUID         string `json:"uuid"`
			Serial       string `json:"serial"`
			Name         string `json:"name"`
			Manufacturer string `json:"manufacturer"`
		} `json:"system"`
	} `json:"dmi"`
	NICs []struct {
		Name string `json:"name"`
		MAC  string `json:"mac"`
	} `json:"network_interfaces"`
}
```

### Too much identity: the multi-NIC problem

Here the identity problem *inverts*. A Proxmox host has several NICs, and the
installer reports all of them — but a catalog group typically pins one MAC. The
naive loop ("try each NIC, take the first match") has a trap: the example
catalog ends with a catch-all `unknown` group whose empty selector matches
*anything*. If the pinned MAC is the machine's second NIC, the first NIC hits
the catch-all and the host boots to rescue instead of installing.

So the handler tries every NIC and keeps the **most specific** resolution — the
one whose matched group has the most selector terms:

```go
// A host has several NICs and the catalog selector typically names one MAC,
// so try each NIC as the identity's MAC and keep the most specific match —
// otherwise a catch-all group would win on whichever NIC happens to be first.
id, res := s.mostSpecificMatch(base, macs)
```

`base` carries the DMI fields (UUID, serial, product, manufacturer), so a group
could select on those instead of a MAC; each attempt layers one NIC's MAC on
top. A pinned one-term group (`mac = "d0:50:99:d5:6e:72"`) beats the zero-term
catch-all no matter which position the NIC arrives in.
`TestProxmoxAnswer` pins this deliberately: its fixture puts the cataloged MAC
in the *second* NIC slot, with the catch-all present.

### Auth: the answer file is worth protecting

The rendered answer contains the root password hash and the node's install
layout — worth more than a boot script. Proxmox's assistant has a built-in
scheme: `--answer-auth-token name:secret` at prepare time makes the installer
send `Authorization: Bearer name:secret` with its POST. booty's side is the
`--proxmox-token` flag (→ `Options.ProxmoxAuthToken`):

```go
if s.proxmoxToken != "" {
	want := "Bearer " + s.proxmoxToken
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
}
```

`subtle.ConstantTimeCompare` instead of `==` so the comparison doesn't leak
where the mismatch is via timing. Leave the flag unset and the check is off —
reasonable on a trusted provisioning VLAN, where the bigger win is that the two
sides agree.

### The answer template

The profile's `render` block names the fourth kind:

```hcl
render {
  kind     = "proxmox-answer"
  template = "proxmox/answer.toml.tmpl"
}
```

and the template emits the three sections a minimal install needs — `[global]`,
`[network]`, `[disk-setup]`:

```toml
[global]
keyboard = "{{ coalesce (index .Vars "keyboard") "en-us" }}"
fqdn = "{{ coalesce (index .Vars "fqdn") (index .Vars "hostname") .Identity.MAC }}"
# Hashed, never plaintext: generate with `mkpasswd -m sha-512` and set the
# root_password_hashed var on the profile or group. An empty value here fails
# the install loudly rather than installing with a blank root password.
root_password_hashed = "{{ index .Vars "root_password_hashed" }}"
reboot_on_error = false

[network]
source = "from-dhcp"

[disk-setup]
filesystem = "{{ coalesce (index .Vars "filesystem") "ext4" }}"
disk_list = ["{{ coalesce (index .Vars "install_disk") "sda" }}"]
```

Three deliberate choices. The root password is **only** accepted pre-hashed
(`root_password_hashed`, per-host in the group's vars) — the plaintext
`root_password` key exists in Proxmox's answer format and booty's template
refuses to know about it. `reboot_on_error = false` keeps a failed install on
its error screen instead of silently boot-looping through PXE forever. And
`coalesce` — the string-typed sibling of the `or (index …)` idiom below —
lets the group override what the profile defaults.

One gotcha worth knowing: the installer can also discover the answer URL via
DHCP option 250 — but that option must come from your *real* DHCP server. The
installer's DHCP client never talks to booty's proxyDHCP (Chapter 2's offer is
PXE-boot steering, not a lease), so with booty the reliable path is baking
`--url` into the ISO at prepare time, as above.

Status codes follow the `/machine-config` philosophy — a failed fetch aborts
the install *visibly* on the machine's console, so honest codes beat fake 200s:

| Situation | Status |
|-----------|--------|
| Matched a `proxmox-answer` profile | `200` (`application/toml`) |
| Body isn't valid system-info JSON | `400` |
| Token set and header missing/wrong | `401` |
| No catalog match on any NIC or DMI field | `404` |
| `GET` instead of `POST` | `405` (from the mux — Chapter 7) |
| Matched, but the profile isn't a proxmox answer (e.g. the catch-all) | `409` |
| Template execution failed | `500` |

## The `or (index ...)` idiom, carried forward

Every template above reaches for `{{ or (index .Vars "hostname") .Identity.MAC }}`
rather than `{{ .Vars.hostname }}`. That's the same lesson Chapter 4 paid for in
the iPXE renderer, and it's load-bearing here too: `.Vars.hostname` on a profile
that didn't set `hostname` yields template's untyped `<no value>`, which blows up
the moment it's passed to a function. `index` returns the zero value (`""`) for an
absent key, and `or` then falls through to the next candidate. In config templates
— where half the fields are optional overrides — this is the default idiom, not
the exception. Reach for `.Map.key` only when the key is guaranteed present.

## Tested three ways

The render pipeline is verified at three levels, each catching what the others
can't:

1. **Golden files** (`render/testdata/*.golden`) pin the exact rendered
   bytes for a worker machineconfig, a control-plane machineconfig, a
   cloud-init user-data document, and a Proxmox answer file. Refresh with
   `go test ./render -update`; the diff on a template change is the review.
   Alongside each golden, invariant assertions (`type: worker`,
   `installer:v1.7.6`, `endpoint: https://...`) catch the load-bearing
   substrings regardless of golden churn.

2. **Document validity.** A golden file can be pinned *and wrong* — pinned to
   malformed YAML or TOML. So the goldens are separately checked to parse and
   carry the right values:

   ```bash
   yq -e '.machine.type'  render/testdata/talos-worker.yaml.golden        # -> worker
   yq -e '.cluster.etcd'  render/testdata/talos-controlplane.yaml.golden  # present
   yq -e '.hostname'      render/testdata/cloud-init-user-data.golden      # -> ubuntu-01
   python3 -c 'import tomllib,sys; print(tomllib.load(sys.stdin.buffer)["global"]["fqdn"])' \
     < render/testdata/proxmox-answer.toml.golden                          # -> pve-01.home.local
   ```

3. **Handler tests** (`httpsrv`) exercise the status-code contract: a MAC
   match returns `200 application/yaml`; an unknown MAC returns `404`; a booting
   (non-machineconfig) profile returns `409`. The cloud-init tests set
   `req.RemoteAddr` to control the source IP and assert that an `ip`-selected group
   matches, that `user-data` starts with `#cloud-config`, that `vendor-data` is an
   empty `200`, and that an unknown IP is a `404`. The Proxmox tests POST real
   system-info JSON and cover the whole table above — including the multi-NIC
   most-specific match, both `401` shapes (missing and wrong token), and the
   mux-level `405` on `GET`.

## Try it yourself

```bash
go build -o bin/booty ./cmd/booty

./bin/booty serve --catalog examples/catalog --boot-dir /tmp \
  --url http://boot.home.local:8080 &

# A control-plane MAC pulls its Talos machineconfig — the same document Talos
# fetches from the talos.config= URL after it boots:
curl -s "localhost:8080/machine-config?mac=d0:50:99:a2:3b:40"
#   version: v1alpha1
#   machine:
#     type: controlplane
#     install:
#       image: ghcr.io/siderolabs/installer:v1.7.6
#   ...
#   cluster:
#     controlPlane:
#       endpoint: https://192.168.1.100:6443
#     etcd: {}

# A worker MAC gets the worker variant (machine.type: worker, no etcd):
curl -s "localhost:8080/machine-config?mac=d0:50:99:b3:4c:50"

# An unknown MAC is a clean 404 — Talos will retry until you add a group:
curl -si "localhost:8080/machine-config?mac=00:00:00:00:00:99" | head -1
#   HTTP/1.1 404 Not Found

# Proxmox: the installer POSTs its system report and gets the node's answer.
# Note the cataloged MAC is deliberately the *second* NIC — the most-specific
# match still finds pve-01 past the catch-all:
curl -s -X POST localhost:8080/proxmox/answer -d '{
  "dmi": {"system": {"uuid": "11111111-2222-3333-4444-555555555555"}},
  "network_interfaces": [
    {"name": "eno1", "mac": "aa:bb:cc:00:11:22"},
    {"name": "eno2", "mac": "d0:50:99:d5:6e:72"}
  ]}'
#   [global]
#   keyboard = "en-us"
#   ...
#   fqdn = "pve-01.home.local"
```

Run it against the example catalog and you've watched a machine's identity travel
all the way from a DHCP lease, through TFTP and iPXE, into the matcher, and out as
the exact YAML Talos will apply to become a cluster node. That's the whole spine of
the service, end to end.

## What's deferred

- **`siderolabs/machinery`.** The real secrets bundle — PKI, `machine.token`,
  `cluster.id`/`cluster.secret` — must come from machinery, behind an owned
  interface (PLAN-0001's P3). The hand-templated YAML here is correct in *shape*
  and deliberately incomplete in *secrets*. This is the single most important
  deferred upgrade, and it gets its own ADR.
- **A config bundle pipeline.** Production Talos config is often generated once
  (`talosctl gen config`) and then patched per-machine. booty renders per-request
  today; a generate-once-patch-many pipeline is a later optimization.
- **Config validation before serving.** We check the YAML parses in tests, but the
  running server doesn't validate a machineconfig against Talos's schema before
  handing it over. machinery gives that for free once it lands.
- **HTTPS / signed config.** Serving config over plain HTTP is fine on a trusted
  provisioning VLAN; anything less trusted wants TLS and ideally signed configs.

---

← [Chapter 5](./05-catalog-and-matcher.md) | [Chapter 7: HTTP Serving Core →](./06-http-server-stdlib.md)
