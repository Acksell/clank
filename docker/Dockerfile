# syntax=docker/dockerfile:1.6
#
# Multi-stage build that produces /usr/local/bin/{clank-sync,clankd,clank-host}
# in a small runtime image. The same image is used for both the
# clank-sync and clankd services in docker-compose.yml; the CMD
# selects which binary runs.

ARG GO_VERSION=1.25.8

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

# Cache deps as a separate layer so source-only edits skip the
# 200MB+ go-mod download every rebuild.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    apk add --no-cache build-base sqlite-dev && \
    go build -trimpath -ldflags "-s -w" -o /out/clank-sync   ./cmd/clank-sync && \
    go build -trimpath -ldflags "-s -w" -o /out/clankd       ./cmd/clankd     && \
    go build -trimpath -ldflags "-s -w" -o /out/clank-host   ./cmd/clank-host

FROM alpine:3.20 AS runtime

# git: needed by clank-host for sync/apply (git fetch <bundle>) and by
# checkpoint construction inside clank-sync's tests if anyone wants to
# run them in-container.
# ca-certificates: outbound HTTPS (S3, fly.io, etc.).
# tini: PID 1 reaper so signals propagate cleanly to clank-host
# subprocesses spawned by the local provisioner.
RUN apk add --no-cache git ca-certificates tini sqlite-libs

# Persistent state. clank-sync writes its sqlite db here; clankd's local
# provisioner stores host.db here too. Mount as a named volume in
# docker-compose so it survives container rebuilds.
RUN mkdir -p /var/lib/clank /root/.clank
ENV HOME=/root

COPY --from=builder /out/clank-sync   /usr/local/bin/clank-sync
COPY --from=builder /out/clankd       /usr/local/bin/clankd
COPY --from=builder /out/clank-host   /usr/local/bin/clank-host

# clankd's local provisioner does exec.LookPath("clank-host"); having
# it on PATH satisfies that without an explicit BinPath option.

ENTRYPOINT ["/sbin/tini", "--"]
# Override per-service in docker-compose.yml.
CMD ["clankd", "start", "--foreground"]
