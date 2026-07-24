# Groups match a booting machine's identity attributes to a profile. The most
# specific matching group (most selector terms) wins; ties break by group name.

# Per-machine binding: one control-plane node, pinned by MAC, with its hostname.
group "cp-01" {
  profile = "talos-control"
  selector = {
    mac = "d0:50:99:a2:3b:40"
  }
  vars = {
    hostname = "talos-cp-01"
  }
}

# Two workers, pinned by MAC.
group "worker-01" {
  profile = "talos-worker"
  selector = {
    mac = "d0:50:99:b3:4c:50"
  }
  vars = {
    hostname = "talos-worker-01"
  }
}

group "worker-02" {
  profile = "talos-worker"
  selector = {
    mac = "d0:50:99:c4:5d:61"
  }
  vars = {
    hostname = "talos-worker-02"
  }
}

# A Proxmox host, pinned by MAC. The automated installer POSTs every NIC's MAC;
# booty matches the most specific group across them, so only one NIC needs to be
# pinned here.
group "pve-01" {
  profile = "proxmox-host"
  selector = {
    mac = "d0:50:99:d5:6e:72"
  }
  vars = {
    hostname = "pve-01"
    fqdn     = "pve-01.home.local"
    # root_password_hashed = "$6$..."   (mkpasswd -m sha-512)
  }
}

# Catch-all: an empty selector matches any machine at the lowest specificity, so
# an unknown node boots to rescue rather than hanging.
group "unknown" {
  profile = "rescue"
}
