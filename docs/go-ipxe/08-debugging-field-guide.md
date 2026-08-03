# Chapter 10: The Debugging Field Guide

← [Chapter 9](./09-qemu-e2e.md) | [Index →](./00-index.md)

---

Everything until now built booty and proved it works in the lab. This chapter is
for the day it *doesn't* — a real machine on a real VLAN that powers on and sits
there, or boots to a rescue shell, or hangs partway into Talos. The goal is to
turn "it doesn't boot" into a specific, located failure in under a few minutes.

The organizing idea, from Chapter 1: a network boot is a state machine, and every
failure is a *failed transition* between states. booty's states are its endpoints:

```text
POWER ON → DHCP → TFTP:ipxe.efi → GET /boot.ipxe → GET /ipxe → GET /boot/{kernel,initrd}
                                    (chain script)  (resolve)   (download)
                                                                     │
                                          Talos ─ GET /machine-config┤
                                     cloud-init ─ GET /cloud-init/*  ─┘
```

Find the transition that didn't happen and you've found the layer to debug. booty
gives you two things the old model didn't: **the whole boot is replayable with
`curl`**, and **every step is a structured log line**. Most bugs never need a
packet capture.

## Rule 1: it's probably the catalog — so validate it first

The majority of "it won't boot" is really "the catalog doesn't say what you think."
booty has an admission test for exactly this (Chapter 8), and it needs no machine:

```bash
booty validate --catalog ./catalog
# ok: dir://catalog — 4 profiles, 5 groups          ← parses and resolves
# invalid catalog dir://catalog:
#   group "worker-07": references unknown profile "talos-workr"   ← typo, exit 1
```

Run it in CI on the config repo so a broken catalog never merges. If `validate`
passes but a specific machine still misbehaves, the catalog is *valid* but the
*match* is wrong — which is Rule 2.

## Rule 2: replay the boot with curl

Because booty is HTTP-first, you can be the booting machine. This sequence
reproduces the entire boot from your laptop, and the failing `curl` is the failing
transition:

```bash
BASE=http://booty:8080
MAC=d0:50:99:b3:4c:50

curl -s $BASE/healthz                                  # 1. is booty even up?  -> ok
curl -s $BASE/boot.ipxe                                # 2. chain script; must carry ${mac}
curl -s "$BASE/ipxe?mac=$MAC&arch=x86_64"              # 3. the boot script this MAC resolves to
curl -sI "$BASE/boot/talos/v1.7.6/vmlinuz"             # 4. kernel present? (200 + Content-Length)
curl -s "$BASE/machine-config?mac=$MAC"                # 5. Talos config for this MAC
```

Step 3 is where most matching bugs surface, and it has a trap covered below: `/ipxe`
returns **200 even when nothing matched**. So read the *body*, not just the status —
a rescue/no-match script is a 200 that means "I couldn't place this machine."

## Reading booty's logs

Run with `--log-format json` and pipe through `jq`; every request and decision is a
line. These are real lines from a running booty:

```json
{"level":"INFO","msg":"catalog loaded","source":"dir://examples/catalog","profiles":4,"groups":5}
{"level":"INFO","msg":"TFTP listening","addr":{"IP":"0.0.0.0","Port":69},"boot_dir":"/boot"}
{"level":"INFO","msg":"ipxe request","mac":"d0:50:99:b3:4c:50","ip":"","arch":"x86_64","remote":"192.168.1.111:56762"}
{"level":"INFO","msg":"ipxe resolved","mac":"d0:50:99:b3:4c:50","group":"worker-01","profile":"talos-worker"}
{"level":"INFO","msg":"machine-config served","mac":"d0:50:99:b3:4c:50","group":"worker-01","profile":"talos-worker"}
{"level":"INFO","msg":"TFTP transfer complete","file":"ipxe.efi","bytes":1138688,"throughput_mbps":"94.2"}
```

The pairing that matters most is **`ipxe request` → `ipxe resolved`**. The request
line shows the identity that *arrived* (`mac`, `ip`, `arch`); the resolved line
shows the `group` and `profile` it produced. Reading those two together answers
"why did this machine get that boot script?" without guessing.

## The status-and-log diagnostic map

booty's endpoints deliberately use *different* error strategies (Chapters 4, 6, 7),
so the meaning of a code is endpoint-specific. This table is the core of the field
guide:

| Endpoint | Code | What it means | Where to look |
|----------|------|---------------|---------------|
| `/ipxe` | **404** | route not registered — booty started with **no `--catalog`** | your `serve` flags, not the machine |
| `/ipxe` | **200** + boot script | matched a profile — normal | `ipxe resolved` log: right group/profile? |
| `/ipxe` | **200** + rescue/no-match body | matched the catch-all, or nothing | Rule 2 trap below |
| `/ipxe` | **500**-ish (still 200, body says "internal error") | template render failed | the `ipxe render failed` ERROR log |
| `/machine-config` | **200** | served Talos config | `machine-config served` log |
| `/machine-config` | **404** | no group matched **and no catch-all** | add a group, or check the MAC |
| `/machine-config` | **409** | matched a profile that isn't `talos-machineconfig` | usually the catch-all `rescue` — see below |
| `/machine-config` | **500** | template error (missing var, partial not embedded) | the `render failed` ERROR log |
| `/cloud-init/*` | **404** | source IP didn't match an `ip` selector | the DHCP lease vs the catalog |
| `/proxmox/answer` | **200** | served the answer file | `proxmox-answer served` log |
| `/proxmox/answer` | **400** | POST body wasn't system-info JSON | what actually POSTed — a probe, not the installer? |
| `/proxmox/answer` | **401** | `--proxmox-token` doesn't match the ISO's `--answer-auth-token` | both sides of the token; re-prepare the ISO |
| `/proxmox/answer` | **404** | no NIC MAC or DMI field matched, and no catch-all | `proxmox-answer: no match` WARN — it lists every MAC tried |
| `/proxmox/answer` | **405** | request was a `GET` — the installer POSTs | whatever fetched it (curl without `-X POST`?) |
| `/proxmox/answer` | **409** | matched a non-`proxmox-answer` profile (usually the catch-all) | same shape as the `/machine-config` 409 below |
| `/boot/{path}` | **403** | path traversal blocked | the requested path |
| `/boot/{path}` | **404** | file missing from `--boot-dir` | `ls` the boot dir |

Two rows deserve their own sections because they're the ones that waste the most
time.

### The `/ipxe` 200 trap: "it boots to rescue"

The single most confusing booty symptom: the machine boots, but to the **rescue
shell** instead of Talos. There's no HTTP error anywhere, because — by design —
`/ipxe` never returns one (iPXE firmware hangs on non-200; Chapter 4). The failure
is invisible in status codes and *visible in the logs*.

The cause is almost always that **identity didn't arrive**. If the chain script
isn't delivering `${mac}` (it wasn't embedded in `ipxe.efi`, or DHCP option 175
isn't set — Chapter 4's core misconception), `/ipxe` is called with an empty query,
and with a catch-all group in the catalog it resolves to `rescue`:

```json
{"level":"INFO","msg":"ipxe request","mac":"","ip":"","arch":"x86_64","remote":"192.168.1.111:56763"}
{"level":"INFO","msg":"ipxe resolved","mac":"","group":"unknown","profile":"rescue"}
```

`"mac":""` is the smoking gun — the machine reached booty but told it nothing.
Fix the chain delivery, not the catalog. Contrast that with a machine whose MAC
*did* arrive but matches no group, in a catalog with **no** catch-all:

```json
{"level":"WARN","msg":"no catalog match","mac":"d0:50:99:aa:bb:cc","err":"no matching group"}
```

That's a real WARN, and the fix is a catalog group. So: `profile":"rescue"` with an
empty `mac` means *delivery* is broken; a `no catalog match` WARN with a real `mac`
means the *catalog* is missing a group. Different bug, different fix, and the logs
tell them apart at a glance.

### The catch-all makes `/machine-config` return 409, not 404

A subtle one that surprises everyone. The example catalog ends with a catch-all:

```hcl
group "unknown" { profile = "rescue" }   # empty selector → matches anything
```

That means *every* machine matches *something*, so `/machine-config` for an unknown
MAC does **not** 404 — it matches `rescue`, which is an iPXE profile, not a Talos
machineconfig, and the handler returns **409 Conflict**:

```bash
curl -si "$BASE/machine-config?mac=00:00:00:00:00:09" | head -1
# HTTP/1.1 409 Conflict          ← matched the catch-all rescue, which isn't a machineconfig
```

So with a catch-all, `404` from `/machine-config` is impossible — a 409 is what
"this machine has no Talos config" actually looks like. Drop the catch-all and the
same request becomes a 404. Knowing which shape your catalog produces saves you
chasing a 404 that will never come.

## The template-error 500s (the ones we actually hit)

Two render failures bit during development and are worth recognizing on sight,
because both compile fine and only fail at request time:

- **`template "talos-machine" not defined`** on `/machine-config` — the shared
  partial `talos/_common.yaml.tmpl` wasn't embedded. `//go:embed templates`
  silently skips files whose names start with `_`; the fix is `//go:embed
  all:templates` (Chapter 6). If you add a `_`-prefixed partial and every render
  starts 500ing, this is it.
- **`... <.Vars.hostname>: invalid value; expected string`** — a template used
  `.Vars.hostname` on a profile that set no `hostname`. A missing map key with
  field syntax yields template's untyped `<no value>`; the fix is `index .Vars
  "hostname"` (Chapter 4). Any optional var accessed with dot syntax is a latent
  version of this.

Both surface as an `ipxe render failed` or `machine-config render failed` ERROR
line with the exact template error attached — grep for `render failed`.

## When you do need the wire: tcpdump

If curl and logs don't explain it, the failure is below booty — DHCP, TFTP, or
reachability. Capture the netboot-relevant traffic on the provisioning interface:

```bash
sudo tcpdump -i eth0 -n -v "port 67 or port 68 or port 69 or port 8080"
```

A healthy boot looks like this (abbreviated), and any missing phase is your answer:

```text
# DHCP — node has no IP yet (source 0.0.0.0), Option 60 marks it PXE
0.0.0.0.68  > 255.255.255.255.67  DHCP Discover  Option 60 "PXEClient:Arch:00007..."
192.168.1.1.67 > ...              DHCP Offer     Your-IP 192.168.1.111  next-server 192.168.1.10  file "ipxe.efi"
...                               DHCP Request / ACK

# TFTP — note the server replies from a NEW port (the TID), and negotiates blksize
192.168.1.111.49152 > 192.168.1.10.69     RRQ "ipxe.efi" octet blksize 1468 tsize 0
192.168.1.10.51000  > 192.168.1.111.49152 OACK blksize=1468 tsize=1138688     ← from :51000, not :69
192.168.1.111.49152 > 192.168.1.10.51000  ACK block 0
192.168.1.10.51000  > 192.168.1.111.49152 DATA block 1 ... (and so on)

# iPXE over HTTP — TWO scripts: the chain first, then the resolved boot script
192.168.1.111 > 192.168.1.10.8080  GET /boot.ipxe                          200
192.168.1.111 > 192.168.1.10.8080  GET /ipxe?mac=d0:50:99:b3:4c:50&arch=…  200
192.168.1.111 > 192.168.1.10.8080  GET /boot/talos/v1.7.6/vmlinuz          200 (Content-Length: …)
192.168.1.111 > 192.168.1.10.8080  GET /boot/talos/v1.7.6/initramfs.xz     200

# Talos, seconds later, polling for its config
192.168.1.111 > 192.168.1.10.8080  GET /machine-config?mac=d0:50:99:b3:4c:50  200
```

The TFTP port change (`:69` → `:51000`) is the TID model from Chapter 3, and it's
why **stateful firewalls need a TFTP ALG** — if the reply from the new port is
dropped, TFTP stalls after the RRQ with no error. That's a common "DHCP works, TFTP
doesn't" cause.

## Failure catalog, by transition

**DHCP Discover, no Offer.** Node broadcasts forever. Even with `--proxydhcp`,
booty never assigns IPs — the lease always comes from your real DHCP server — so
this is your DHCP/VLAN: wrong subnet, firewall on UDP 67, or the DISCOVER not
reaching the server. `tcpdump port 67` on the DHCP host tells you which.

**Lease arrives, but no boot instructions.** The node gets an IP and then sits
there (or prints **PXE-E55**). With `--proxydhcp` this is booty's territory:
PXE-E55 specifically means the client's port-4011 Boot Server Request got no
ACK — check booty logged `proxyDHCP boot-ack` (not just `proxyDHCP offer`), and
that UDP 4011 isn't firewalled. No `proxyDHCP offer` at all usually means the
broadcast isn't reaching booty (different subnet — proxyDHCP is L2-local;
Chapter 2).

**DHCP Offer, no TFTP.** Node gets an IP but never requests `ipxe.efi`. Check the
Offer actually carried `next-server` (booty's IP) and `file "ipxe.efi"`. Then
confirm booty's TFTP is listening — the `TFTP listening` log line, or `ss -lun |
grep 69`. **Distroless caveat:** the release image runs as nonroot (UID 65532),
and binding port 69 needs privilege — grant `CAP_NET_BIND_SERVICE` or map the port,
or the TFTP listener silently never comes up.

**TFTP ERROR "file not found".** RRQ arrives, booty replies with an ERROR packet;
the log shows `TFTP file not found file=ipxe.efi`. `ipxe.efi` isn't in `--boot-dir`
(or a case mismatch). Stage it: `wget -O $BOOTDIR/ipxe.efi https://boot.ipxe.org/ipxe.efi`
(building one with the chain script embedded is the asset-provenance step Chapter 4
and PLAN-0001 flag).

**iPXE loaded, no `GET /boot.ipxe`.** TFTP completes but booty sees no HTTP. The
`ipxe.efi` has no boot script — the Chapter 4 misconception. Either embed a chain
script (`EMBED=chain.ipxe`) or set DHCP option 175 to `http://booty:8080/boot.ipxe`.
Verify a binary has a URL: `strings ipxe.efi | grep http://`.

**`GET /ipxe` but boots to rescue.** The `/ipxe` 200 trap above. Empty `mac` in the
`ipxe request` log → fix chain delivery; a `no catalog match` WARN with a real MAC →
add a catalog group.

**Kernel/initrd won't download.** `/ipxe` resolved correctly but the boot stalls
fetching the kernel. Either the files are missing from `--boot-dir` (curl the
`/boot/...` URL — 404?) or `--url` is wrong so the absolute URLs in the script point
somewhere unreachable. Prefer an IP in `--url` if DNS is unreliable that early.

**Talos hangs after boot.** Talos fetched a kernel but the install never proceeds.
It's polling `/machine-config` and getting a non-200 — and because Talos *retries*,
the symptom is a hang, not an error. Grep the log for `machine-config`: a repeating
`machine-config: no match` (404) means no group for this MAC yet; a 409 means it
matched a non-Talos profile (the catch-all rescue — see above); a 500 means a
template error.

**cloud-init finds no data.** cloud-init sends **no identity** — only its source IP
(NoCloud, Chapter 6). A 404 on `/cloud-init/*` means that source IP matched no `ip`
selector. The DHCP-assigned lease and the catalog's `ip = "..."` must be the same
address; a machine on a different lease than the catalog expects gets a 404 and
falls back to "no datasource."

**Proxmox installer aborts fetching the answer.** The installer POSTs its system
report to `/proxmox/answer` and shows the failure on its console — so read booty's
side of the same exchange. A `401` means the token baked into the ISO
(`--answer-auth-token`) and booty's `--proxmox-token` disagree — re-prepare the
ISO or fix the flag. A `404` comes with a `proxmox-answer: no match` WARN that
lists **every MAC the installer reported** — compare that list against the
catalog's `mac` selector (typo, or you pinned a NIC the machine doesn't have). A
`409` is the catch-all shape again: the machine matched `rescue`, not the
`proxmox-answer` profile, so the pinned group's selector didn't hit. And if
booty logged *nothing*, the ISO's `--url` points somewhere else — `strings` the
prepared ISO or just re-run `prepare-iso` and watch the log for the POST.

## booty-flavored one-liners

```bash
# Boots per MAC in the last hour (json logs)
jq -r 'select(.msg=="ipxe request") | .mac' booty.log | sort | uniq -c | sort -rn

# Machines that arrived with NO identity (chain-delivery bugs)
jq -r 'select(.msg=="ipxe request" and .mac=="") | .remote' booty.log

# Every machine that resolved to rescue (unplaced nodes)
jq -r 'select(.msg=="ipxe resolved" and .profile=="rescue") | .mac // .remote' booty.log

# Talos machines still polling (no matching machine-config)
jq -r 'select(.msg=="machine-config: no match") | .mac' booty.log | sort | uniq -c
```

## Before blaming the network: the protocol smoke test

If you suspect the *service* rather than the wire, the e2e protocol tier (Chapter 9)
runs booty's real TFTP and HTTP servers in-process and drives the whole chain over
sockets — no VM, no machine:

```bash
just test-e2e          # protocol tier PASSes if the assembled service serves the chain
```

A green protocol tier means booty itself is fine and the problem is DHCP, the
`ipxe.efi`, or reachability. A red one means the bug is in booty or your catalog, and
the failing assertion names the broken step.

## The serial console is still your best friend

For the on-machine half — everything after iPXE hands off — nothing beats a serial
console. booty's example cmdline already carries `console=ttyS0,115200n8`
(`common_cmdline` in the catalog), so kernel and Talos messages go to serial:

```bash
ipmitool -I lanplus -H 192.168.1.200 -U admin -P '***' sol activate
# iPXE 1.21.1 -- Open Source Network Boot Firmware
# booty: booting talos-worker-01 …
# [    0.000000] Linux version 6.6…-talos …
```

Without `console=ttyS0`, those messages go to the VGA console and you're blind over
the network — always keep it in the cmdline.

---

That's the loop: `validate` the catalog, replay with `curl`, read the
`request`→`resolved` log pair, consult the status-and-log map, and only reach for
`tcpdump` when the failure is below booty. Nearly every boot failure is one of the
transitions above, and each one has a specific place to look.

← [Chapter 9](./09-qemu-e2e.md) | [Index →](./00-index.md)
