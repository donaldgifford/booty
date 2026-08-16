# Changelog

All notable changes to this project are documented here. The format is
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
this project adheres to [Semantic Versioning](https://semver.org/).
## [unreleased]

### Documentation

- *(guide)* Talos overlay-template field walkthrough (chapter 11) ([#22](https://github.com/donaldgifford/booty/issues/22))

## [0.2.1] - 2026-08-16

### Bug Fixes

- *(proxydhcp)* Boot-server type 0 cancels UEFI netboot (PXE-E21) ([#21](https://github.com/donaldgifford/booty/issues/21))

### Documentation

- Stop the install command 404ing on every release ([#19](https://github.com/donaldgifford/booty/issues/19))

### Miscellaneous Tasks

- Mandatory Talos kernel args + INV-0001 on the boot-chain gaps ([#20](https://github.com/donaldgifford/booty/issues/20))

## [0.2.0] - 2026-08-11

### Bug Fixes

- *(renovate)* Stop stacking a second semver label on every PR ([#10](https://github.com/donaldgifford/booty/issues/10))

### Documentation

- Add an Install section, and fix the release-note grouping
- Close out IMPL-0001 and DESIGN-0001
- *(impl)* Record the main ruleset as it actually is
- Record Codecov and Renovate as enabled ([#9](https://github.com/donaldgifford/booty/issues/9))
- Record the Renovate label verification ([#16](https://github.com/donaldgifford/booty/issues/16))

### Miscellaneous Tasks

- Delete the changelog drift check ([#17](https://github.com/donaldgifford/booty/issues/17))

## [0.1.1] - 2026-08-04

### Bug Fixes

- *(changelog)* Stop attributing the release to the v0.0.0 baseline
- *(cmd)* Report a real version for go-installed and local builds
- *(ci)* Pass version build args to the release image bake

### Documentation

- *(impl)* Record Phase 4 — v0.1.0 published and verified

## [0.1.0] - 2026-08-04

### Features

- *(httpsrv)* [**breaking**] New validates BaseURL and returns an error

### Bug Fixes

- *(deps)* Bump x/text and Go to clear two reachable vulnerabilities
- *(justfile)* Pin GOTOOLCHAIN=local for the license recipes
- *(tftp,proxydhcp)* Honour ctx cancellation on shutdown
- *(httpsrv,catalog)* Close two injection paths found in the pre-release audit
- *(catalog,tftp)* Distinguish a broken catalog, bound the OACK timeout
- *(catalog)* Distinguish the ways a catalog root can be unusable
- *(ci)* Gate the Starlight build on the site scaffold existing
- *(cmd)* Reject a --url that machines cannot chain to
- *(tftp)* Bound in-flight transfers and stop amplifying to silent peers
- *(release)* Ship LICENSE and README in the archives

### Refactor

- *(catalog)* Report match specificity instead of making callers recompute it
- *(httpsrv)* [**breaking**] Rename Options to Config
- *(tftp)* [**breaking**] New takes a Config struct
- *(proxydhcp)* [**breaking**] Split Serve's bool, unexport the port constants
- *(catalog)* [**breaking**] Drop the Source interface

### Documentation

- Add DESIGN-0001, DESIGN-0002, and IMPL-0001 release docs
- *(go)* Move package comments to doc.go and rewrite for pkg.go.dev
- Add README badges and file the runnable-examples follow-up
- *(impl)* Record Phase 3 CI findings and the DESIGN-0002 blocker
- *(impl)* Check off the plumbing-test PR task
- Make docs/ markdownlint-clean and add the lint-md recipe
- *(claude)* Record the GOTOOLCHAIN and markdown-lint gotchas
- *(impl)* Record the pre-release audit and its open decisions
- *(impl)* Record what Phase 4 pre-flight has already verified
- *(impl)* Record the first measured performance data
- *(impl)* Record OQ-7a/OQ-8a and add Phase 3b
- Reconcile the ADRs with the Phase 3b API and document proxydhcp usage
- *(impl)* Record Phase 3b as complete with the measured results

### Performance

- *(catalog)* Stop rebuilding the MAC replacer on every comparison

### Testing

- *(proxydhcp)* Exercise handleDHCP instead of only the parser
- *(httpsrv)* Cover the error branches added by the audit fixes
- *(catalog)* Budget allocations so the per-group cost cannot creep back

### Miscellaneous Tasks

- *(renovate)* Add patch label so Renovate PRs pass the label check
- Pin trufflehog to a resolvable version and stop changelog drift
- *(release)* Publish the release-signing public key

