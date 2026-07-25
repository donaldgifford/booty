# booty — docker recipes
#
# Docker image build/push pipeline via docker buildx bake. Reads
# docker-bake.hcl for target definitions. Import this from the main
# justfile with `import 'docker.just'`; the importing file owns the
# `set shell` declaration and defines the `version` / `commit_hash` /
# `build_date` variables the recipes below feed into the bake as build args.

# ─── Docker ─────────────────────────────────────────────────────────

# Local single-arch build via docker buildx bake
[group('docker')]
docker-build:
    @VERSION={{ version }} COMMIT={{ commit_hash }} DATE={{ build_date }} \
        docker buildx bake

# Multi-arch build and push via docker buildx bake
[group('docker')]
docker-buildx:
    @VERSION={{ version }} COMMIT={{ commit_hash }} DATE={{ build_date }} \
        docker buildx bake release

# Push the image (requires prior build)
[group('docker')]
docker-push:
    @VERSION={{ version }} COMMIT={{ commit_hash }} DATE={{ build_date }} \
        docker buildx bake --push

# CI build (linux/amd64 only, for fast PR validation)
[group('docker')]
docker-ci:
    @VERSION={{ version }} COMMIT={{ commit_hash }} DATE={{ build_date }} \
        docker buildx bake ci
