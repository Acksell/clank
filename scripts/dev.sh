#!/bin/bash
# dev.sh — one-shot local dev environment for clank.
#
# Spawns TWO Cloudflare quick tunnels:
#   1. minio (port 9000)  — for the laptop and any remote sprite to
#      reach object storage on a shared public hostname (SigV4 host
#      binding requires one URL across all consumers).
#   2. clankd (port 7878) — for a remote sprite to call back during
#      `clank pull --migrate`'s sprite-side checkpoint create.
#
# Captures both URLs, writes them to docker/.env, then brings the
# stack up. Foreground; ctrl-c tears tunnels + stack down together.
# Quick tunnels rotate per restart, so re-run if you stop and start.

set -eu

MINIO_PORT="${MINIO_API_PORT:-9000}"
CLANKD_PORT="${CLANKD_PORT:-7878}"
COMPOSE="docker compose -f docker/docker-compose.yml"
ENV_FILE="docker/.env"

# start_tunnel <port> — spawn cloudflared on the given port, wait up
# to 60s for it to advertise a URL, echo the URL + pid (newline-
# separated) on stdout. Exits the script on failure.
start_tunnel() {
        port="$1"
        log="$(mktemp)"
        cloudflared tunnel --url "http://localhost:$port" --no-autoupdate \
                > "$log" 2>&1 &
        pid=$!
        i=0
        while [ $i -lt 120 ]; do
                if ! kill -0 "$pid" 2>/dev/null; then
                        echo "ERROR: cloudflared for :$port exited early. Log:" >&2
                        cat "$log" >&2
                        rm -f "$log"
                        exit 1
                fi
                url="$(grep -oE 'https://[a-z0-9-]+\.trycloudflare\.com' "$log" | head -1 || true)"
                if [ -n "$url" ]; then
                        rm -f "$log"
                        echo "$url"
                        echo "$pid"
                        return
                fi
                sleep 0.5
                i=$((i + 1))
        done
        echo "ERROR: cloudflared for :$port did not advertise a URL within 60s. Log:" >&2
        cat "$log" >&2
        rm -f "$log"
        exit 1
}

echo "Spawning cloudflared tunnels (minio :$MINIO_PORT, clankd :$CLANKD_PORT)..."

# Run twice; capture URL + pid each.
minio_out="$(start_tunnel "$MINIO_PORT")"
MINIO_URL="$(echo "$minio_out" | sed -n 1p)"
MINIO_PID="$(echo "$minio_out" | sed -n 2p)"

clankd_out="$(start_tunnel "$CLANKD_PORT")"
CLANKD_URL="$(echo "$clankd_out" | sed -n 1p)"
CLANKD_PID="$(echo "$clankd_out" | sed -n 2p)"

cleanup() {
        trap '' INT TERM EXIT
        echo
        echo "Stopping cloudflared tunnels and docker stack..."
        kill "$MINIO_PID" "$CLANKD_PID" 2>/dev/null || true
        $COMPOSE down
}
trap cleanup INT TERM EXIT

echo "Tunnels ready:"
echo "  minio  $MINIO_URL"
echo "  clankd $CLANKD_URL"

# Seed docker/.env from .env.example on first run, then upsert the
# two URL lines. Rewrites via tmp to avoid os-specific sed -i tooling
# differences.
mkdir -p docker
if [ ! -f "$ENV_FILE" ] && [ -f docker/.env.example ]; then
        cp docker/.env.example "$ENV_FILE"
fi
touch "$ENV_FILE"
TMP_ENV="$(mktemp)"
grep -v '^CLANK_SYNC_S3_PUBLIC_ENDPOINT=' "$ENV_FILE" \
        | grep -v '^CLANK_SYNC_S3_ENDPOINT=' \
        | grep -v '^CLANK_PUBLIC_BASE_URL=' \
        > "$TMP_ENV" || true
echo "CLANK_SYNC_S3_PUBLIC_ENDPOINT=$MINIO_URL" >> "$TMP_ENV"
echo "CLANK_PUBLIC_BASE_URL=$CLANKD_URL" >> "$TMP_ENV"
mv "$TMP_ENV" "$ENV_FILE"
echo "Updated $ENV_FILE."

# Bring up the stack with the new env. --build so the dev image
# picks up any local edits without a separate `make docker-build`.
echo "Starting docker stack..."
$COMPOSE --env-file "$ENV_FILE" up -d --build

echo
echo "=========================================="
echo " Dev stack ready."
echo
echo "   minio tunnel  $MINIO_URL    (presigned URLs sign for this host)"
echo "   clankd tunnel $CLANKD_URL   (sprite calls back here during pull-migrate)"
echo "   Gateway       http://localhost:${CLANKD_PORT}"
echo "   Auth stub     http://localhost:${CLANK_AUTH_STUB_PORT:-7879}"
echo "   MinIO         http://localhost:$MINIO_PORT  (console :${MINIO_CONSOLE_PORT:-9001})"
echo
echo " On the laptop, register the remote once:"
echo
echo "   clank remote add dev \\"
echo "     --gateway-url=http://localhost:${CLANKD_PORT} \\"
echo "     --auth-url=http://localhost:${CLANK_AUTH_STUB_PORT:-7879} \\"
echo "     --token=${CLANK_AUTH_TOKEN:-clank-dev-token-change-me}"
echo "   clank login"
echo
echo " ctrl-c to tear down tunnels + stack."
echo "=========================================="

# Hold the foreground until one of the tunnels exits (ctrl-c kills both,
# cleanup tears the stack down).
wait -n "$MINIO_PID" "$CLANKD_PID" 2>/dev/null || wait
