# Profiles are named boot recipes. A group binds machines to one of these.

profile "talos-control" {
  boot {
    kernel  = "${local.boot_base}/vmlinuz"
    initrd  = "${local.boot_base}/initramfs.xz"
    # $${mac} is HCL's escape for a literal ${mac}: it must survive HCL untouched
    # so iPXE substitutes the booting machine's MAC into the URL at boot time.
    cmdline = concat(local.common_cmdline, ["talos.config=http://boot.${var.cluster}.local:8080/machine-config?mac=$${mac}"])
  }
  render {
    kind     = "talos-machineconfig"
    template = "talos/controlplane.yaml.tmpl"
  }
  vars = {
    role             = "controlplane"
    cluster          = var.cluster
    talos_version    = var.talos_version
    cluster_endpoint = var.cluster_endpoint
  }
}

profile "talos-worker" {
  boot {
    kernel  = "${local.boot_base}/vmlinuz"
    initrd  = "${local.boot_base}/initramfs.xz"
    # $${mac} is HCL's escape for a literal ${mac}: it must survive HCL untouched
    # so iPXE substitutes the booting machine's MAC into the URL at boot time.
    cmdline = concat(local.common_cmdline, ["talos.config=http://boot.${var.cluster}.local:8080/machine-config?mac=$${mac}"])
  }
  render {
    kind     = "talos-machineconfig"
    template = "talos/worker.yaml.tmpl"
  }
  vars = {
    role             = "worker"
    cluster          = var.cluster
    talos_version    = var.talos_version
    cluster_endpoint = var.cluster_endpoint
  }
}

# Proxmox VE hosts install via the automated installer. Prepare the boot assets
# once with:
#   proxmox-auto-install-assistant prepare-iso proxmox.iso \
#     --fetch-from http --url http://boot.<cluster>.local:8080/proxmox/answer \
#     --pxe --pxe-loader ipxe
# and stage the resulting vmlinuz/initrd under the boot dir. At install time the
# installer POSTs its system info (DMI + NIC MACs) to /proxmox/answer and
# receives this profile's rendered answer.toml.
profile "proxmox-host" {
  boot {
    kernel = "proxmox/8.4/vmlinuz"
    initrd = "proxmox/8.4/initrd.img"
    # The authoritative cmdline comes from the assistant's --pxe-loader ipxe
    # snippet; proxmox-start-auto-installer is what enables automated mode.
    cmdline = ["ramdisk_size=16777216", "rw", "quiet", "splash=silent", "proxmox-start-auto-installer"]
  }
  render {
    kind     = "proxmox-answer"
    template = "proxmox/answer.toml.tmpl"
  }
  vars = {
    country  = "us"
    timezone = "America/New_York"
    mailto   = "root@${var.cluster}.local"
    # root_password_hashed is per-host: set it in the group (mkpasswd -m sha-512).
  }
}

# A fallback profile for machines that match nothing specific — boots to a
# rescue/inventory shell instead of failing silently.
profile "rescue" {
  render {
    kind     = "ipxe"
    template = "ipxe/rescue.ipxe"
  }
}
