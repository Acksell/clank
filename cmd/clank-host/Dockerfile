# Pre-baked clank-host image for cloud sandboxes (Daytona).
#
# The cloud hub provisions sandboxes from this image. The entrypoint
# starts clank-host on $CLANK_HOST_PORT, configured to clone repos
# from the cloud hub at $CLANK_HUB_URL using the bearer token in
# $CLANK_HUB_TOKEN.
#
# Build:
#   docker build -f cmd/clank-host/Dockerfile -t ghcr.io/acksell/clank-host:dev .
#
# Run locally (mostly for image smoke-testing — production deploys go
# through Daytona):
#   docker run --rm -p 7878:7878 \
#     -e CLANK_HUB_URL=http://host.docker.internal:7878 \
#     -e CLANK_HUB_TOKEN=tkn \
#     -e CLANK_HOST_PORT=7878 \
#     ghcr.io/acksell/clank-host:dev

# ---- builder ---------------------------------------------------------
FROM golang:1.23-alpine AS builder

WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO disabled for a static binary that runs on the runtime stage.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/clank-host ./cmd/clank-host

# ---- runtime ---------------------------------------------------------
FROM alpine:3.20

# git: needed by host.workDirFor for clones from the cloud-hub mirror.
# ca-certificates: needed for HTTPS clones (origin remote URLs and the
#   cloud hub's preview URL).
RUN apk add --no-cache git ca-certificates

# Default agent backends (opencode, claude-code) are installed on top
# of this image by the layer the user actually runs. The base image
# only ships clank-host itself.

WORKDIR /workspace
COPY --from=builder /out/clank-host /usr/local/bin/clank-host
COPY cmd/clank-host/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

# Conventional default; the cloud hub's Daytona launcher passes
# CLANK_HOST_PORT=7878 explicitly. The EXPOSE matches the launcher's
# HostPort constant.
ENV CLANK_HOST_PORT=7878
EXPOSE 7878

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
