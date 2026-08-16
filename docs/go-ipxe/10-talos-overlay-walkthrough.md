# Chapter 11: Talos in the Field — Overlay Templates End to End

Chapters 2–9 built the packages; chapter 10 taught you to debug them. This
chapter is different: it is the record of driving the *shipped* booty — the
container, the released binary, the real `proxydhcp` — from an empty Proxmox
VM to a bootstrapped single-node Talos cluster, on a network whose DHCP
server booty does not control. Everything here was executed for real, and
the failure catalog at the end is the actual sequence of failures hit on the
way, in order, with what each one turned out to mean.

Two genuine booty bugs came out of this walkthrough — that was rather the
point. Both are recorded in
[INV-0001](../investigation/0001-talos-boot-chain-gaps-machineconfig-secrets-ipxe-chainload-loop.md)
(Observations 4 and 5); one is fixed, one has a documented host-level
workaround you will apply below.

The worked example uses these placeholder values — substitute your own:

| Value | Placeholder |
|-------|-------------|
| booty host (docker, host networking) | `192.168.10.5` |
| Server VLAN / subnet | `192.168.10.0/24`, gateway `192.168.10.1` (also the DHCP server) |
| Control-plane node MAC | `52:54:00:12:34:56` |
| Control-plane node reserved IP | `192.168.10.44` |
| Talos version | `v1.13.8` |

## What you need

- **booty with the PXE-E21 fix** — any release *after* v0.2.0. A v0.2.0
  `proxydhcp` tells UEFI firmware to cancel netboot (see the failure
  catalog); this walkthrough is why that release exists.
- **`talosctl`** matching your target Talos minor.
- **A machine to boot.** Here: a Proxmox VM with OVMF firmware. Bare metal
  works identically from booty's point of view.
- **The network between them.** The client and booty must share a broadcast
  domain — proxyDHCP is eavesdropping on broadcasts, and broadcasts do not
  cross VLANs. Everything else (TFTP, HTTP, the Talos API) is unicast and
  merely needs a route.

## Step 1: boot assets from Image Factory

On [factory.talos.dev](https://factory.talos.dev), choose **Bare-metal
Machine**, your version, and amd64. System extensions are baked into the
initramfs — for a Proxmox VM, add `qemu-guest-agent`. The "extra kernel
arguments" field is irrelevant here: booty's catalog owns the kernel command
line. The schematic ID names the download URLs:

```text
https://factory.talos.dev/image/<schematic-id>/v1.13.8/kernel-amd64
https://factory.talos.dev/image/<schematic-id>/v1.13.8/initramfs-amd64.xz
```

Place them in booty's `--boot-dir` under the paths your profile references
(here `talos/v1.13.8/vmlinuz` and `talos/v1.13.8/initramfs.xz`).

If your schematic has extensions, note the matching installer image
`factory.talos.dev/installer/<schematic-id>:v1.13.8` — you will feed it to
the templates in step 3 so the *installed* system keeps the extensions.

## Step 2: generate the cluster configs

Pick the cluster endpoint IP **first**, and make sure you actually own it:
reserve it, or choose outside the DHCP pool. An endpoint that some other
device already holds means a control plane that bootstraps toward an IP
conflict. The cheapest robust pattern for a small cluster: create a
DHCP fixed-IP reservation for the control-plane node's MAC and point the
endpoint at that address.

```bash
talosctl gen secrets -o secrets.yaml
talosctl gen config talos-test https://192.168.10.44:6443 \
  --with-secrets secrets.yaml \
  --kubernetes-version 1.36.2 \
  --install-image ghcr.io/siderolabs/installer:v1.13.8
```

This drops `controlplane.yaml`, `worker.yaml`, and `talosconfig` in the
working directory.

**These files are cluster secrets.** `controlplane.yaml` carries five
private keys (machine CA, cluster CA, aggregator, service account, etcd)
plus both join tokens. The templates you derive from them embed all of it.
Before anything else, make sure none of these names can ever reach a
commit — this repository's `.gitignore` guards `secrets.yaml`,
`talosconfig`, `controlplane.yaml`, `worker.yaml`, `kubeconfig`, and the
local templates directory, precisely because this workflow starts inside a
repo checkout.

## Step 3: configs → overlay templates

booty's embedded Talos templates are deliberately secret-free
(INV-0001 Observation 1) — they cannot form a cluster. The overlay-template
path closes the gap today: your *generated* configs become the templates,
verbatim, with two surgical template edits. Create
`<templates-dir>/talos/controlplane.yaml.tmpl` and
`worker.yaml.tmpl` as byte-for-byte copies of the generated files, then:

**Edit 1 — per-machine hostname.** Replace the trailing `HostnameConfig`
document with a var-driven one:

```yaml
---
apiVersion: v1alpha1
kind: HostnameConfig
{{- $h := index .Vars "hostname" }}
{{- if $h }}
hostname: {{ $h }}
{{- else }}
auto: stable
{{- end }}
```

**Edit 2 — installer image as a var** (skip if you used
`--install-image` with the exact image you want baked in):

```yaml
        image: {{ or (index .Vars "install_image") "ghcr.io/siderolabs/installer:v1.13.8" }}
```

That is the entire delta. The point of the overlay model is that the
machine-independent 99% of the file stays exactly what `talosctl` produced
and `talosctl validate` blessed; only identity varies per MAC.

Verify locally before the server ever sees it — run booty against the
templates on a loopback port and validate the render:

```bash
booty serve --catalog-dir examples/catalog --templates-dir ./templates-local \
  --http-addr 127.0.0.1:8081 --tftp-addr 127.0.0.1:6969 --boot-dir /tmp &
curl -s 'http://127.0.0.1:8081/machine-config?mac=52:54:00:12:34:56' \
  | talosctl validate --mode metal -c /dev/stdin
```

## Step 4: the catalog

The group entry for the node carries its identity vars; the profile carries
the kernel line. Three things matter:

1. **The mandatory metal args.** `talos.platform=metal init_on_alloc=1
   slab_nomerge pti=on` — the PXE guide's required set, already in
   `examples/catalog/00-variables.hcl`.
2. **Absolute URLs everywhere.** The kernel `talos.config=` URL, the
   asset URLs, and the chain URL all embed booty's IP. If booty moves (or
   grows a second interface — see step 6), they all move with it.
3. **The catalog and templates load once, at startup.** Editing HCL or
   templates on disk does nothing until the container restarts. If a curl
   shows stale values, you are looking at the process's copy, not the
   file's.

## Step 5: the embedded iPXE binary

A stock `ipxe.efi` re-DHCPs after loading, booty's proxyDHCP answers it
with `ipxe.efi` again, and the machine chainloads forever
(INV-0001 Observation 2). Until the user-class loop-break lands in
`proxydhcp`, the escape is an embedded script — three lines that skip the
second proxyDHCP round entirely:

```text
#!ipxe
dhcp
chain http://192.168.10.5:8080/boot.ipxe || shell
```

The `dhcp` line is load-bearing: a freshly-loaded iPXE has no IP yet. The
`|| shell` means a failed chain drops you at an iPXE prompt — the single
best debugging position in the whole stack — instead of silently looping.

The repo's justfile wraps the upstream build in a container (booty is not
a build service — this is an operator convenience, the pipeline is
upstream iPXE's):

```bash
just ipxe-embed http://192.168.10.5:8080/boot.ipxe
```

Copy `build/ipxe/ipxe.efi` into `--boot-dir`, then prove the loop closed —
fetch it back over TFTP and compare hashes:

```bash
curl -s tftp://192.168.10.5/ipxe.efi -o /tmp/got.efi
shasum -a 256 /tmp/got.efi build/ipxe/ipxe.efi   # must match
```

Unlike the catalog, TFTP reads per request — no restart needed for asset
copies.

## Step 6: run booty, then prove every stage with curl

```bash
docker run -d --name booty --net=host \
  -v /srv/booty/boot:/boot \
  -v /srv/booty/catalog:/catalog \
  -v /srv/booty/templates:/templates \
  ghcr.io/donaldgifford/booty:<tag> serve \
  --boot-dir /boot --catalog-dir /catalog --templates-dir /templates \
  --server-ip 192.168.10.5 --proxydhcp
```

Two deployment facts bite here:

- **Host networking is not optional for real PXE.** Bridge networking eats
  the DHCP broadcasts and breaks TFTP's ephemeral-port handshake; the
  README's container matrix has the full story, including why
  `--cap-add NET_BIND_SERVICE` does *not* solve the privileged-port bind
  for a non-root image.
- **A multi-homed host silently breaks the proxy offer**
  (INV-0001 Observation 4). booty's phase-1 offer goes to
  `255.255.255.255:68`, and Linux routes limited broadcast via the
  *default route* — if the PXE VLAN is on the host's second NIC, every
  offer exits the wrong interface while booty's log happily records the
  send. Until booty replies via the receiving interface, pin the route on
  the host:

  ```bash
  ip route add 255.255.255.255/32 dev <pxe-facing-iface>
  ```

  This is not persistent; add it to the host's network config or your PXE
  dies at the next reboot with the most misleading symptom in the catalog
  below.

Then run the pre-flight battery — one curl per boot stage, in boot order,
from any routed machine:

```bash
curl -s http://192.168.10.5:8080/healthz                          # server up
curl -s http://192.168.10.5:8080/boot.ipxe                        # stage-1 chain script
curl -s 'http://192.168.10.5:8080/ipxe?mac=52:54:00:12:34:56'     # per-MAC script: kernel line, args, URLs
curl -s -o /dev/null -w '%{http_code}\n' -r 0-1023 \
  http://192.168.10.5:8080/boot/talos/v1.13.8/vmlinuz             # 206 = assets + range support
curl -s 'http://192.168.10.5:8080/machine-config?mac=52:54:00:12:34:56' \
  | talosctl validate --mode metal -c /dev/stdin                  # stage-3 payload, validated
curl -s tftp://192.168.10.5/ipxe.efi -o /dev/null && echo TFTP ok # stage-0 file
```

When all six pass, every failure from here on is between the firmware and
the wire — which is exactly what the catalog below is for.

## Step 7: the client machine (Proxmox specifics)

Each of these was learned the hard way; the catalog maps their symptoms.

| Setting | Value | Why |
|---------|-------|-----|
| BIOS | OVMF (UEFI) | the modern PXE path this guide targets |
| EFI disk | add one; **uncheck "Pre-Enroll keys"** | pre-enrolled keys = Secure Boot on = unsigned iPXE/Talos refused. The EFI disk itself just persists boot order |
| VirtIO RNG | **required** | post-PixieFail EDK2 wants real entropy for its network stack; without the device the PXE boot option can silently vanish |
| CPU type | `host` (or ≥ x86-64-v2) | Talos requires x86-64-v2; `kvm64` panics early |
| NIC | VirtIO, MAC set to the catalog's MAC, **Firewall unchecked** | the MAC *is* the machine's identity to booty; the Proxmox firewall can eat DHCP replies |
| NIC VLAN tag | tag **or** native — never tagged-as-native | tag it if the bridge trunks the VLAN; leave untagged if the port's native VLAN is the right one. A frame tagged with the port's own native VLAN gets dropped by many switches |
| Boot order | disk first, then net | empty disk falls through to PXE on first boot; installed disk boots without re-PXE forever after |
| RAM | ≥ 2 GB | the initramfs unpacks in RAM |

And the one that is not a VM setting at all: **the switch port profile**.
If the VM bridge rides a trunk port, the trunk must actually carry the
server VLAN — a VM tagged onto a trunk whose allowed-VLAN list omits it
produces perfect silence.

## Step 8: boot, bootstrap, done

The console sequence when everything is right:

```text
disk boot fails (empty) → Start PXE over IPv4 → Station IP address is 192.168.10.44
→ TFTP ipxe.efi → iPXE banner → dhcp → chain http://…/boot.ipxe
→ booty: booting <host> (profile talos-control) → kernel + initramfs download
→ Talos dashboard: STAGE Booting, "etcd is waiting to join the cluster,
   … please run `talosctl bootstrap`"
```

booty's log mirrors it: `proxyDHCP offer` → `proxyDHCP boot-ack` → TFTP →
`http request` lines for script, assets, `/machine-config`. Talos installs
to disk, reboots (from disk this time — boot order pays off), and waits.
Then:

```bash
talosctl --talosconfig talosconfig -e 192.168.10.44 -n 192.168.10.44 bootstrap
talosctl --talosconfig talosconfig -e 192.168.10.44 -n 192.168.10.44 \
  kubeconfig ./kubeconfig
kubectl --kubeconfig ./kubeconfig get nodes   # → Ready
```

One node is a quorate etcd — majority of 1 is 1. Grow 1 → 3 control-plane
nodes, never linger at 2.

## The failure catalog

Every row below actually happened during this walkthrough, in roughly this
order. The console error alone is never enough — cross-reference with
booty's log, which splits each symptom into two distinct causes.

| Console says | booty log says | It means | Fix |
|--------------|----------------|----------|-----|
| `PXE-E16: No valid offer received` | nothing | client's broadcasts never reach booty | wrong VLAN / bridge / trunk; walk the L2 path |
| `PXE-E16: No valid offer received` | `proxyDHCP offer` ×4 (backoff 0/4/8/16 s) | booty heard the DISCOVER but its reply never arrived — or the *address* offer is missing (proxy offers carry no IP by design) | multi-homed broadcast egress → the `255.255.255.255/32` route (step 6); or the real DHCP server isn't offering — check pool/reservation |
| `PXE-E18: Server response timeout` | offers, no `boot-ack` | client got offers, but REQUEST/ACK or the port-4011 exchange died | hypervisor/VM firewall eating unicast replies; check the path to `--server-ip`:4011 |
| `PXE-E21: Remote boot cancelled` right after `Station IP address is …` | offers, no `boot-ack` | DHCP succeeded; booty's boot item then told the firmware "boot local" — the boot-server type 0 bug | run a booty release with the fix (INV-0001 Observation 5) |
| no PXE attempt at all, `No bootable option or device was found` | nothing | firmware has no netboot option | NIC dropped from boot order (re-created NICs do this), or missing VirtIO RNG |
| `Start HTTP Boot over IPv4 … Could not retrieve NBP` | n/a | fallthrough after PXE failed; a red herring | fix the PXE failure above it, ignore the HTTP boot noise |
| iPXE loads, then loads iPXE again, forever | `offer`/`boot-ack` repeating for the same MAC | stock binary, chainload loop | embedded-script binary (step 5) |
| iPXE drops to shell | `http request` 4xx/5xx or absent | chain/kernel URL wrong or unreachable | you are *in* the debugger — `dhcp`, then `chain` the URL by hand and read the error |
| Talos boots, config rejected | `machine-config served` | template/render problem | `curl \| talosctl validate --mode metal` (step 6) — never debug this via a VM reboot loop |

The log-side rule that makes the table work: an `offer` line proves booty
*heard* the client and *attempted* a reply — it does not prove delivery. A
`boot-ack` line proves the client heard booty back, because only a client
that received and accepted the offer ever unicasts to port 4011. The gap
between those two lines is where every network-layer failure in this
chapter lived.

## What the field test changed

- `proxydhcp`'s boot-server type is non-zero (the PXE-E21 fix) — shipped.
- The multi-homed offer-egress bug is recorded with its workaround;
  the proper fix (reply via receiving interface, or `--interface`) is
  queued behind INV-0001's user-class loop-break, which touches the same
  packet path.
- This chapter exists.

Previous: [Chapter 10: Debugging Field Guide](./08-debugging-field-guide.md)
