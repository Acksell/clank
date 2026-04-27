#!/bin/sh
# Entrypoint for the clank-host sandbox image. Reads the cloud-hub
# coordinates from env, optionally joins a Tailscale tailnet (dev
# mode), then invokes clank-host with the matching flags.
#
# Required env:
#   CLANK_HUB_URL    — cloud hub's externally-reachable base URL.
#   CLANK_HUB_TOKEN  — bearer token paired with CLANK_HUB_URL.
#   CLANK_HOST_PORT  — TCP port to bind. Defaults to 7878.
#
# Optional env (dev/Tailscale):
#   SANDBOX_ENVIRONMENT — when "development", attempt Tailscale setup.
#   TAILSCALE_AUTHKEY   — auth key for `tailscale up`. Required for
#                          tailscale to actually connect; absent = skip.
#   TAILSCALE_HOSTNAME  — node name on the tailnet. Defaults to
#                          "clank-sandbox" if unset.
#
# Failures are loud: missing required env crashes the container so
# Daytona surfaces the problem instead of leaving an idle sandbox.

set -eu

: "${CLANK_HUB_URL:?CLANK_HUB_URL is required}"
: "${CLANK_HUB_TOKEN:?CLANK_HUB_TOKEN is required}"
: "${CLANK_HOST_PORT:=7878}"

# --- Tailscale (dev only) -------------------------------------------
#
# When the user is iterating against a cloud hub running on their
# laptop, the sandbox needs network reach back to the laptop. The
# cleanest no-root option is Tailscale in userspace mode: it joins
# the user's tailnet, then publishes a SOCKS5 + outbound-HTTP proxy
# on localhost. Setting HTTP_PROXY/HTTPS_PROXY routes clank-host's
# git clone (and any backend's egress) through the tailnet, so the
# sandbox can reach `http://<laptop-tailnet-name>:7878` even though
# the laptop has no public IP.
#
# This whole block is a no-op in production: SANDBOX_ENVIRONMENT
# only ever equals "development" when the cloud hub launcher
# explicitly forwards it (see Daytona ExtraEnv config).
if [ "${SANDBOX_ENVIRONMENT:-}" = "development" ]; then
    echo "Development mode: setting up Tailscale..."
    # Userspace mode: no TUN device, no root capabilities required.
    # SOCKS5 on 1055, HTTP proxy on 1056 — same convention as the
    # user's prior Modal-based setup.
    tailscaled \
        --tun=userspace-networking \
        --socks5-server=localhost:1055 \
        --outbound-http-proxy-listen=localhost:1056 \
        > /var/log/tailscaled.log 2>&1 &

    # Poll for the tailscaled control socket (up to ~4s).
    for _ in $(seq 1 40); do
        tailscale status >/dev/null 2>&1 && break
        sleep 0.1
    done

    if [ -n "${TAILSCALE_AUTHKEY:-}" ]; then
        tailscale up \
            --authkey="${TAILSCALE_AUTHKEY}" \
            --hostname="${TAILSCALE_HOSTNAME:-clank-sandbox}"
        echo "Tailscale connected as ${TAILSCALE_HOSTNAME:-clank-sandbox}"

        # Route all subsequent egress through the tailnet proxy.
        # clank-host (Go net/http) and `git clone` both honor these.
        export HTTP_PROXY="http://localhost:1056"
        export HTTPS_PROXY="http://localhost:1056"
        export http_proxy="http://localhost:1056"
        export https_proxy="http://localhost:1056"
    else
        echo "TAILSCALE_AUTHKEY not set, skipping tailscale up (sandbox will only reach the public internet)"
    fi
fi

# Bind on the IPv6 wildcard. On Linux this is dual-stack by default
# (IPV6_V6ONLY=0), so the listener accepts both IPv4 and IPv6
# connections. Daytona's toolbox proxy dials `[::1]:<port>` (IPv6
# loopback) when forwarding preview-URL traffic into the sandbox; an
# IPv4-only `0.0.0.0` bind would be unreachable and the proxy would
# return 502s. Sticking to `[::]` here also keeps macOS happy (where
# `0.0.0.0` and `::` behave nearly identically).
exec /usr/local/bin/clank-host \
  --listen "tcp://[::]:${CLANK_HOST_PORT}" \
  --git-sync-source "${CLANK_HUB_URL}" \
  --git-sync-token "${CLANK_HUB_TOKEN}"
