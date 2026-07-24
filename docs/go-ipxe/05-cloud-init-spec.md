# Chapter 5: The cloud-init Specification

> **Legacy chapter.** In the restructured guide, catalog & matcher is Chapter 5
> (see [`05-catalog-and-matcher.md`](./05-catalog-and-matcher.md)) and cloud-init
> becomes a *secondary* render path folded into the forthcoming Render chapter
> (`booty` is Talos-first). This file is kept for its cloud-init reference content
> until that chapter absorbs it. The concepts below are still accurate.

← [Chapter 4](./04-ipxe-deep-dive.md) | [Chapter 6: HTTP Server with stdlib →](./06-http-server-stdlib.md)

---

## What cloud-init Actually Is

cloud-init is the industry-standard mechanism for first-boot configuration of Linux instances. It was written at Canonical for Ubuntu, adopted by Red Hat for RHEL/CentOS/Fedora, and now runs on virtually every Linux distribution in cloud environments. When you launch an EC2 instance and specify user data, cloud-init is what processes it.

It's a Python program (or set of programs) that runs during the boot sequence, typically as a systemd service. It has one job: apply instance-specific configuration to a generic OS image so the instance becomes the specific thing you wanted.

---

## The Data Source Model

cloud-init is designed to run on many different platforms — EC2, GCP, Azure, OpenStack, bare metal, VMs. Each platform has a different way of providing configuration data. Cloud-init abstracts this with **data sources**.

A data source is a cloud-init plugin that knows how to retrieve `meta-data`, `user-data`, and `vendor-data` from a specific platform. EC2's data source fetches from `169.254.169.254`. OpenStack's data source uses config drives. The **NoCloud** data source fetches from a URL you specify.

On boot, cloud-init tries each configured data source in order until one succeeds. The order is configured in `/etc/cloud/cloud.cfg` under `datasource_list`. For our case, we want `NoCloud` first:

```yaml
# /etc/cloud/cloud.cfg
datasource_list:
  - NoCloud
  - None
```

---

## NoCloud Data Source: How It Finds Its URL

NoCloud is the flexible, URL-based data source. It looks for its configuration in three places (tried in order):

**1. Kernel command line**
```
ds=nocloud-net;s=http://forge.example.com:8080/v1/
```
The `ds=nocloud-net` part identifies the data source. The `s=` is the "seedfrom" URL — the base URL where cloud-init will append `meta-data`, `user-data`, and `vendor-data`.

**2. `/etc/cloud/cloud.cfg.d/` config file**
```yaml
datasource:
  NoCloud:
    seedfrom: http://forge.example.com:8080/v1/
```

**3. Filesystem seed directory**
cloud-init looks for files on a special filesystem labeled `cidata` (usually an ISO or FAT volume). This is common for local VM provisioning (`cloud-localds`).

For network boot, option 1 (kernel cmdline) is what we use. Your iPXE script sets the `ds=` argument.

---

## The Three Data Endpoints

cloud-init makes exactly three requests to Forge (relative to the seedfrom URL):

### `GET {seedfrom}meta-data`

Returns YAML (or JSON) with instance identity information. cloud-init uses this to populate variables it uses internally, and some modules read specific fields.

**Required fields:**
```yaml
instance-id: unique-and-stable-id
```

The `instance-id` is critically important. cloud-init uses it to detect "this is a new instance" vs "I've already run on this instance." If the `instance-id` matches what's in `/var/lib/cloud/data/instance-id`, cloud-init skips most of its work (it's already been provisioned). If it doesn't match, cloud-init runs the full provisioning sequence.

This is why a static `instance-id` like `"iid-local01"` causes cloud-init to only run once (subsequent boots see the same ID and skip). A changing `instance-id` would cause re-provisioning on every boot — usually not what you want for bare metal.

**Commonly used fields:**
```yaml
instance-id: forge-talos-worker-03
local-hostname: talos-worker-03

# Network config in cloud-init v2 (Netplan) format
network:
  version: 2
  ethernets:
    id0:
      match:
        macaddress: "52:54:00:ab:cd:ef"
      addresses:
        - 192.168.1.103/24
      gateway4: 192.168.1.1
      nameservers:
        addresses: [192.168.1.1, 8.8.8.8]
      set-name: eth0
```

### `GET {seedfrom}user-data`

The user-data is the main provisioning payload. It must start with a "magic header" that tells cloud-init what type of data it is:

| Header | Type | What it does |
|--------|------|-------------|
| `#cloud-config` | YAML config | The standard format — modules and directives |
| `#!/bin/bash` | Shell script | Run directly as root |
| `#!` + any path | Script | Run with specified interpreter |
| `#include` | URL list | Fetch and process additional user-data files |
| `#cloud-boothook` | Boothook | Run very early, before cloud-init stages |
| `Content-Type: multipart/...` | Multi-part | Combine multiple user-data types |

The `#cloud-config` format is what you almost always want. It's a YAML document with a defined schema:

```yaml
#cloud-config

# Create/modify users
users:
  - name: ubuntu
    groups: [sudo, docker]
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - ssh-ed25519 AAAA... ops@example.com

# Install packages
packages:
  - curl
  - jq
  - htop

# Write files
write_files:
  - path: /etc/sysctl.d/99-kubernetes.conf
    content: |
      net.bridge.bridge-nf-call-iptables = 1
      net.ipv4.ip_forward = 1
    owner: root:root
    permissions: '0644'

# Run commands
runcmd:
  - systemctl enable --now kubelet
  - kubeadm join 192.168.1.100:6443 --token abc.def --discovery-token-ca-cert-hash sha256:...

# Final message (written to /var/log/cloud-init-output.log)
final_message: |
  cloud-init complete for ${HOSTNAME}
  Provisioned at: $TIMESTAMP
```

### `GET {seedfrom}vendor-data`

vendor-data is an optional second layer of config, typically used by infrastructure providers to add their own standard config on top of user-data. The user's user-data can override or extend it.

For Forge, returning `200 OK` with an empty body (or 404) is fine. Some cloud-init versions are picky about this endpoint existing, so return 200 with empty body to be safe.

---

## The cloud-init Execution Stages

Understanding the stages is essential for debugging "why didn't my config apply?" cloud-init runs in five stages, each as a separate systemd unit:

**1. `cloud-init-local.service`**
Runs with no network available. Only accesses local data sources (filesystem seeds). Performs initial network configuration from meta-data.

**2. `cloud-init.service` (network stage)**
Runs after the network is up. Fetches data from network data sources (this is when it hits Forge). Applies `bootcmd` directives (run before anything else, on every boot).

**3. `cloud-config.service`**
Applies the bulk of the cloud-config: users, groups, packages, write_files, etc.

**4. `cloud-final.service`**
Applies `runcmd` (arbitrary commands), `chef`, `puppet`, etc. This is the last stage.

**5. Lifecycle tracking**
cloud-init writes `/var/lib/cloud/instance/boot-finished` when complete. This file's existence is how other services know provisioning is done.

```
Timeline:
  :00  Kernel boot
  :10  cloud-init-local starts — configures lo, reads local data
  :15  Networking up (DHCP or static from meta-data)
  :20  cloud-init starts — fetches meta-data/user-data from Forge
  :25  cloud-config starts — applies users, files, packages
  :90  (packages install) cloud-final starts — runs runcmd
  :95  cloud-init complete — boot-finished written
```

---

## What Forge Must Implement

The NoCloud HTTP data source has a specific contract:

**URL format**: seedfrom URL must end with `/`. Forge appends `meta-data`, `user-data`, `vendor-data`.

**Response format**: YAML or JSON for meta-data, any cloud-config format for user-data.

**Node identification**: cloud-init doesn't identify itself in the request headers. The only identification is the **source IP address** of the request. Forge maps `request IP → node → config`.

**Response codes**: 
- `200 OK` with content: normal case
- `404 Not Found`: cloud-init treats this as "data not present" (okay for vendor-data)
- `4xx/5xx`: cloud-init retries or fails, depending on severity

**The `instance-id` contract**: Must be stable for a given node (same value across reboots) but unique across nodes. Using the hostname is fine for bare metal. Using a UUID is better practice.

---

## Implementing the Data Source in Go

```go
// pkg/http/cloudinit.go
package http

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"text/template"
	"bytes"
)

func (s *Server) handleMetaData(w http.ResponseWriter, r *http.Request) {
	node := s.nodeFromRequest(r)
	if node == nil {
		http.NotFound(w, r)
		return
	}

	s.logger.Info("cloud-init meta-data request",
		"ip", clientIP(r),
		"hostname", node.Hostname,
	)

	vars := s.inventory.ResolveVars(node)
	out, err := s.templates.Render("cloud_init/meta-data.yaml.tmpl", vars)
	if err != nil {
		s.logger.Error("meta-data render failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, out)
}

func (s *Server) handleUserData(w http.ResponseWriter, r *http.Request) {
	node := s.nodeFromRequest(r)
	if node == nil {
		http.NotFound(w, r)
		return
	}

	s.logger.Info("cloud-init user-data request",
		"ip", clientIP(r),
		"hostname", node.Hostname,
		"profile", node.Profile,
	)

	profile := s.inventory.GetProfile(node.Profile)
	vars := s.inventory.ResolveVars(node)

	out, err := s.templates.Render(profile.UserdataTemplate, vars)
	if err != nil {
		s.logger.Error("user-data render failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, out)
}

func (s *Server) handleVendorData(w http.ResponseWriter, r *http.Request) {
	// Vendor-data is optional. Return 200 with empty body.
	// cloud-init handles this correctly; returning 404 also works
	// but some versions log noisy warnings.
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
}

// nodeFromRequest extracts the client IP and finds the matching node.
// This is the core of cloud-init node identification.
func (s *Server) nodeFromRequest(r *http.Request) *Node {
	ip := clientIP(r)
	node := s.inventory.FindByIP(ip)
	if node == nil {
		s.logger.Warn("cloud-init request from unknown IP", "ip", ip)
	}
	return node
}

// clientIP extracts the real client IP from the request.
// Handles X-Forwarded-For if running behind a proxy (not typical for Forge,
// but included for environments that load-balance the HTTP server).
func clientIP(r *http.Request) string {
	// In direct deployment, use RemoteAddr
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
```

### The Meta-Data Template

```
{{/* templates/cloud_init/meta-data.yaml.tmpl */}}
instance-id: forge-{{.Hostname}}
local-hostname: {{.Hostname}}

network:
  version: 2
  ethernets:
    id0:
      match:
        macaddress: "{{.MAC}}"
      addresses:
        - {{.IP}}/{{.PrefixLen}}
      gateway4: {{.Gateway}}
      nameservers:
        addresses: [{{range $i, $dns := .DNSServers}}{{if $i}}, {{end}}{{$dns}}{{end}}]
      set-name: eth0
```

### The User-Data Template

```
{{/* templates/cloud_init/ubuntu-worker.yaml.tmpl */}}
#cloud-config
# Generated by Forge for {{.Hostname}} (profile: {{.Profile}})

hostname: {{.Hostname}}
fqdn: {{.Hostname}}.{{.Domain}}

users:
  - name: ubuntu
    groups: [sudo, docker, adm]
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
{{- range .SSHKeys}}
      - {{.}}
{{- end}}

packages:
  - apt-transport-https
  - ca-certificates
  - curl
  - jq

write_files:
  - path: /etc/sysctl.d/99-kubernetes.conf
    content: |
      net.bridge.bridge-nf-call-iptables = 1
      net.bridge.bridge-nf-call-ip6tables = 1
      net.ipv4.ip_forward = 1

runcmd:
  - sysctl --system
  - echo "Provisioned by Forge at $(date)" > /etc/forge-provisioned
  - echo "Node: {{.Hostname}}" >> /etc/forge-provisioned
  - echo "Profile: {{.Profile}}" >> /etc/forge-provisioned

final_message: |
  Forge provisioning complete for {{.Hostname}}
  Profile: {{.Profile}}
  Timestamp: $TIMESTAMP
```

---

## Debugging cloud-init

When cloud-init doesn't behave as expected, these are the authoritative sources:

```bash
# Full log — everything cloud-init did
sudo cat /var/log/cloud-init.log

# Just the output log (what your runcmd printed)
sudo cat /var/log/cloud-init-output.log

# What cloud-init thinks its data source is
sudo cloud-init status --long

# What data cloud-init received (cached locally after first fetch)
sudo cat /var/lib/cloud/instance/user-data.txt
sudo cat /var/lib/cloud/instance/meta-data.txt

# Validate your user-data YAML before deploying
cloud-init schema --config-file /path/to/your/user-data.yaml

# Force cloud-init to re-run from scratch (wipes the cached instance-id)
sudo cloud-init clean --logs
sudo cloud-init init

# Run only a specific stage
sudo cloud-init single --name users-groups
sudo cloud-init single --name runcmd
```

The most common failure: `cloud-init status` shows `error` but the log shows "DataSource not found." This means cloud-init couldn't reach Forge, or the URL in the kernel cmdline is wrong. Always check:
1. Is the `ds=` kernel arg set correctly in your iPXE script?
2. Can the node reach Forge at that URL? (Try `curl` from the node)
3. Is Forge returning 200 for `/v1/meta-data`?

---

## cloud-init and Talos: A Different Model

It's worth noting that Talos doesn't use cloud-init at all. Instead, it has its own config fetching mechanism (`talos.config=` kernel arg) that retrieves a machine config in Talos's own YAML format. The provisioning model is similar (fetch from URL, apply on boot) but the format and tooling are completely different.

Forge handles both: the `/v1/` endpoints for cloud-init, and `/machine-config/:hostname` for Talos. Same pattern, different consumers.

---

← [Chapter 4](./04-ipxe-deep-dive.md) | [Chapter 6: HTTP Server with stdlib →](./06-http-server-stdlib.md)
