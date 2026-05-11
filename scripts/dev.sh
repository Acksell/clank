#!/bin/sh
# dev.sh — one-shot local dev environment for clank.
#
# Spawns a Cloudflare quick tunnel pointing at the local minio port,
# captures the trycloudflare URL, writes it into docker/.env as
# CLANK_SYNC_S3_ENDPOINT, then brings the docker stack up. Quick
# tunnels are anonymous and rotate per restart, so keep this script
# in the foreground; ctrl-c tears down both the tunnel and the stack.
#
# Why it exists: presigned S3 URLs bake the bucket hostname into the
# SigV4 signature, so the laptop and a remote sandbox (fly.io) must
# both dial the same hostname. cloudflared gives us that hostname for
# free without exposing minio to the public network from /etc/hosts
# hacks.

set -eu

MINIO_PORT="${MINIO_API_PORT:-9000}"
COMPOSE="docker compose -f docker/docker-compose.yml"
ENV_FILE="docker/.env"

# Spawn cloudflared in the background, capture its log.
TUNNEL_LOG="$(mktemp)"
cloudflared tunnel --url "http://localhost:$MINIO_PORT" --no-autoupdate \
        > "$TUNNEL_LOG" 2>&1 &
CLOUDFLARED_PID=$!

cleanup() {
        trap '' INT TERM EXIT
        echo
        echo "Stopping cloudflared (pid=$CLOUDFLARED_PID) and docker stack..."
        kill "$CLOUDFLARED_PID" 2>/dev/null || true
        $COMPOSE down
        rm -f "$TUNNEL_LOG"
}
trap cleanup INT TERM EXIT

# Wait for cloudflared to print a URL. 60s is generous — usually <2s.
echo "Waiting for cloudflared to advertise a URL..."
TUNNEL_URL=""
i=0
while [ $i -lt 120 ]; do
        if ! kill -0 "$CLOUDFLARED_PID" 2>/dev/null; then
                echo "ERROR: cloudflared exited before advertising a URL. Log:" >&2
                cat "$TUNNEL_LOG" >&2
                exit 1
        fi
        TUNNEL_URL="$(grep -oE 'https://[a-z0-9-]+\.trycloudflare\.com' "$TUNNEL_LOG" | head -1 || true)"
        [ -n "$TUNNEL_URL" ] && break
        sleep 0.5
        i=$((i + 1))
done
if [ -z "$TUNNEL_URL" ]; then
        echo "ERROR: cloudflared did not advertise a URL within 60s. Log:" >&2
        cat "$TUNNEL_LOG" >&2
        exit 1
fi
echo "Tunnel ready: $TUNNEL_URL"

# Seed docker/.env from .env.example on first run, then upsert the
# tunnel URL line. The gateway uses the internal docker hostname
# (http://clank-minio:9000) for its OWN calls — the tunnel URL only
# rides in the presigned URLs handed to the laptop and remote sprites,
# so we set CLANK_SYNC_S3_PUBLIC_ENDPOINT (not CLANK_SYNC_S3_ENDPOINT).
# Avoids os-specific sed -i tooling differences by rewriting via tmp.
mkdir -p docker
if [ ! -f "$ENV_FILE" ] && [ -f docker/.env.example ]; then
        cp docker/.env.example "$ENV_FILE"
fi
touch "$ENV_FILE"
TMP_ENV="$(mktemp)"
grep -v '^CLANK_SYNC_S3_PUBLIC_ENDPOINT=' "$ENV_FILE" \
        | grep -v '^CLANK_SYNC_S3_ENDPOINT=' \
        > "$TMP_ENV" || true
echo "CLANK_SYNC_S3_PUBLIC_ENDPOINT=$TUNNEL_URL" >> "$TMP_ENV"
mv "$TMP_ENV" "$ENV_FILE"
echo "Updated $ENV_FILE with CLANK_SYNC_S3_PUBLIC_ENDPOINT=$TUNNEL_URL."

# Bring up the stack with the new env. --build so the dev image
# picks up any local edits without a separate `make docker-build`.
echo "Starting docker stack..."
$COMPOSE --env-file "$ENV_FILE" up -d --build

echo
echo "=========================================="
echo " Dev stack ready."
echo
echo "   Tunnel    $TUNNEL_URL"
echo "   Gateway   http://localhost:${CLANKD_PORT:-7878}"
echo "   Auth stub http://localhost:${CLANK_AUTH_STUB_PORT:-7879}"
echo "   MinIO     http://localhost:$MINIO_PORT  (console :${MINIO_CONSOLE_PORT:-9001})"
echo
echo " On the laptop, register the remote once:"
echo
echo "   clank remote add dev \\"
echo "     --gateway-url=http://localhost:${CLANKD_PORT:-7878} \\"
echo "     --auth-url=http://localhost:${CLANK_AUTH_STUB_PORT:-7879} \\"
echo "     --token=${CLANK_AUTH_TOKEN:-clank-dev-token-change-me}"
echo "   clank login"
echo
echo " ctrl-c to tear down tunnel + stack."
echo "=========================================="

# Hold the foreground until cloudflared exits (ctrl-c kills it, cleanup
# tears the stack down).
wait "$CLOUDFLARED_PID"
