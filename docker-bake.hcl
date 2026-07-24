// docker-bake.hcl — multi-arch build pipeline for booty.
//
// Targets:
//   - default: local host-arch build (used by `just docker-build`)
//   - ci:      linux/amd64 build + push of `:dev-ci` for PR validation
//   - release: multi-arch build + push to GHCR (CI only, on release)
//
// The docker-build job in .github/workflows/ci.yml consumes this via
// docker/bake-action with the `targets` input. The docker job in
// .github/workflows/release.yml merges in the version-derived image refs
// from docker/metadata-action's bake-file outputs.

variable "REGISTRY" {
  default = "ghcr.io/donaldgifford/booty"
}

variable "TAG" {
  default = "dev"
}

variable "VERSION" {
  default = "0.0.0-dev"
}

variable "COMMIT" {
  default = "none"
}

variable "DATE" {
  default = "unknown"
}

group "default" {
  targets = ["booty"]
}

group "ci" {
  targets = ["booty-ci"]
}

group "release" {
  targets = ["booty-release"]
}

target "_common" {
  context    = "."
  dockerfile = "Dockerfile"
  args = {
    VERSION = "${VERSION}"
    COMMIT  = "${COMMIT}"
    DATE    = "${DATE}"
  }
  labels = {
    "org.opencontainers.image.source"      = "https://github.com/donaldgifford/booty"
    "org.opencontainers.image.licenses"    = "Apache-2.0"
    "org.opencontainers.image.description" = "booty — network-boot service: proxyDHCP, TFTP, iPXE, Talos/cloud-init/Proxmox config"
    "org.opencontainers.image.title"       = "booty"
  }
  // Annotations land on the OCI manifest (and the index, for multi-arch
  // builds). GHCR reads these — not labels — to populate the version
  // page's source-repo link, description, and license badge.
  annotations = [
    "index,manifest:org.opencontainers.image.source=https://github.com/donaldgifford/booty",
    "index,manifest:org.opencontainers.image.licenses=Apache-2.0",
    "index,manifest:org.opencontainers.image.description=booty — network-boot service: proxyDHCP, TFTP, iPXE, Talos/cloud-init/Proxmox config",
    "index,manifest:org.opencontainers.image.title=booty",
  ]
}

// Stub providing default `tags` for local `docker buildx bake`. The
// release workflow overrides this target via docker/metadata-action's
// bake-file outputs so the bake pushes the same semver-derived image
// refs the metadata-action emits. The release target inherits from
// this and does NOT declare tags itself, so the override actually
// takes effect (with HCL inheritance, a child's tags list replaces
// the parent's, not extends it).
target "docker-metadata-action" {
  tags = [
    "${REGISTRY}:${TAG}",
    "${REGISTRY}:latest",
  ]
}

// No `platforms` here on purpose: buildx defaults to the host platform,
// so a local `just docker-build` produces a native image (arm64 on Apple
// Silicon) instead of an emulated amd64 one.
target "booty" {
  inherits = ["_common"]
  tags     = ["${REGISTRY}:${TAG}"]
}

// CI builds are linux/amd64 only — emulated arm64 builds via QEMU on
// GitHub's ubuntu-latest runners take ~25 min and dominate PR feedback
// time. Multi-arch coverage is restored in the release target, which
// runs only on tag pushes.
target "booty-ci" {
  inherits  = ["_common"]
  tags      = ["${REGISTRY}:${TAG}-ci"]
  platforms = ["linux/amd64"]
  attest = [
    "type=sbom",
    "type=provenance,mode=min",
  ]
}

target "booty-release" {
  inherits = ["_common", "docker-metadata-action"]
  // tags intentionally omitted — they come from docker-metadata-action
  // (defaults for local bake; CI overrides via metadata-action).
  platforms = [
    "linux/amd64",
    "linux/arm64",
  ]
  output = ["type=registry"]
  attest = [
    "type=sbom",
    "type=provenance,mode=max",
  ]
}
