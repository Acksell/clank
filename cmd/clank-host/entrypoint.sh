#!/bin/sh
# Entrypoint for the clank-host sandbox image. Reads the cloud-hub
# coordinates from env and invokes clank-host with the matching flags.
#
# Required env:
#   CLANK_HUB_URL    — cloud hub's externally-reachable base URL.
#   CLANK_HUB_TOKEN  — bearer token paired with CLANK_HUB_URL.
#   CLANK_HOST_PORT  — TCP port to bind. Defaults to 7878.
#
# Failures are loud: missing required env crashes the container so
# Daytona surfaces the problem instead of leaving an idle sandbox.

set -eu

: "${CLANK_HUB_URL:?CLANK_HUB_URL is required}"
: "${CLANK_HUB_TOKEN:?CLANK_HUB_TOKEN is required}"
: "${CLANK_HOST_PORT:=7878}"

exec /usr/local/bin/clank-host \
  --listen "tcp://0.0.0.0:${CLANK_HOST_PORT}" \
  --git-sync-source "${CLANK_HUB_URL}" \
  --git-sync-token "${CLANK_HUB_TOKEN}"
