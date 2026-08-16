# syntax=docker/dockerfile:1.26@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32

# Build stage
FROM --platform=$BUILDPLATFORM golang:1.26.5 AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

WORKDIR /src

# Cache dependencies.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
  go mod download

# Copy source and build. The cache mounts keep /go/pkg/mod and the Go build
# cache across builds — the first build is cold, rebuilds are incremental.
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
  go build -trimpath \
  -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
  -o /booty ./cmd/booty

# Runtime stage
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /booty /booty

# HTTP :8080, TFTP :69, proxyDHCP :67 + BINL :4011. The image runs as
# nonroot (UID 65532). On a bridge network every port binds fine (Docker
# zeroes ip_unprivileged_port_start inside the container netns), but real
# PXE needs --net=host, where the host's sysctl applies: binding 69/67/4011
# then needs --user 0:0, or ip_unprivileged_port_start=0 on the host, or a
# derived image that setcaps the binary. Plain --cap-add NET_BIND_SERVICE
# does NOT work — Docker grants no ambient capabilities to non-root users,
# so the capability never reaches the process (verified against v0.2.0).
# The rootfs is read-only — mount the catalog and boot assets as volumes.
EXPOSE 8080/tcp 69/udp 67/udp 4011/udp

USER nonroot:nonroot

ENTRYPOINT ["/booty"]
