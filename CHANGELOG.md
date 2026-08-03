# Changelog

All notable changes to this project are documented here. The format is
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
this project adheres to [Semantic Versioning](https://semver.org/).
## [unreleased]

### Bug Fixes

- *(deps)* Bump x/text and Go to clear two reachable vulnerabilities
- *(justfile)* Pin GOTOOLCHAIN=local for the license recipes
- *(tftp,proxydhcp)* Honour ctx cancellation on shutdown
- *(httpsrv,catalog)* Close two injection paths found in the pre-release audit
- *(catalog,tftp)* Distinguish a broken catalog, bound the OACK timeout
- *(catalog)* Distinguish the ways a catalog root can be unusable

### Refactor

- *(catalog)* Report match specificity instead of making callers recompute it

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

### Testing

- *(proxydhcp)* Exercise handleDHCP instead of only the parser
- *(httpsrv)* Cover the error branches added by the audit fixes

### Miscellaneous Tasks

- *(renovate)* Add patch label so Renovate PRs pass the label check
- Pin trufflehog to a resolvable version and stop changelog drift

