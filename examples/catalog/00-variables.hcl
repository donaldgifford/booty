# Catalog inputs. `booty validate --catalog examples/catalog` loads every *.hcl
# file in this directory and merges them, so splitting variables, profiles and
# groups across files is purely organizational.
#
# Any variable can be overridden at load time (a future `--var` flag / platform
# source); absent an override, the default here is used.

variable "talos_version" {
  description = "Talos release to boot"
  default     = "v1.7.6"
}

variable "cluster" {
  description = "Cluster these machines join"
  default     = "home"
}

variable "cluster_endpoint" {
  description = "Kubernetes API endpoint (control-plane VIP)"
  default     = "https://192.168.1.100:6443"
}

locals {
  # Derived once, reused everywhere. This is the DRY win HCL buys over YAML.
  boot_base = "talos/${var.talos_version}"

  common_cmdline = [
    "console=ttyS0,115200n8",
    "console=tty0",
    "talos.platform=metal",
  ]
}
