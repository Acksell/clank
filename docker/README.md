# Self-hosted clank stack (docker compose)

Brings up a complete clank backend on your laptop (primarily for development & testing purposes, and as an example setup):

- **minio** — S3-compatible object storage for checkpoint bundles
- **clankd** — gateway with the embedded sync server (presigned URLs +
  sqlite metadata) and the local provisioner (spawns clank-host as a
  subprocess inside the container so migrations land somewhere)

Everything is self-contained — no fly.io, daytona, or AWS account
needed. Useful for smoke-testing the sync/migration flow end-to-end.

## One-time setup

```sh
make docker-setup    # adds `127.0.0.1 clank-minio` to /etc/hosts (sudo prompt)
```

### Why

clankd's embedded sync mints SigV4-signed presigned S3 URLs. SigV4
signs the **Host** header into the canonical request, so the URL bears
one hostname and that hostname must resolve to a reachable address
from *every* consumer:

- Containers (clankd fetching bundles, sprites pulling them) resolve
  `clank-minio` via Docker DNS → minio container.
- The laptop (uploading bundles) needs the same hostname pointed at
  minio's published port — hence the `/etc/hosts` entry.

Rewriting the URL host on the consumer side would invalidate the
signature; running minio under a different name on each side would
too. Until clankd grows a separate signing-vs-public-endpoint config,
the shared hostname is the only mechanism that works.

Without this entry, `clank push` from the laptop fails with
`dial tcp: lookup clank-minio: no such host` on the bundle PUT.

## Bringing the stack up

```sh
make docker-up      # implies docker-setup; builds + starts everything
make docker-logs    # tail logs from all services
make docker-down    # stop + remove containers
```

Health-checks:

```sh
curl -fsS http://localhost:7878/ping           # clankd (open without auth)
curl -fsS http://localhost:7878/v1/health      # clankd's embedded sync server
open http://localhost:9001                     # minio console (clankadmin / clankadmin)
```

Logs:

```sh
docker compose -f docker/docker-compose.yml logs -f clankd
```

## Smoke-testing the migration flow

The dev stack uses a single static bearer token for both sides. The
server reads it from the `CLANK_AUTH_TOKEN` env var (default
`clank-dev-token-change-me` if you haven't created `docker/.env`); the
laptop reads it from `cloud.access_token` in `~/.clank/preferences.json`.
**Both must match.**

From the laptop, with the stack running:

```sh
cd ~/some-real-repo

# Point the laptop's `clank` CLI at the docker clankd. Add to
# ~/.clank/preferences.json:
#   {
#     "cloud": {
#       "gateway_url":  "http://localhost:7878",
#       "access_token": "clank-dev-token-change-me"
#     }
#   }
# The access_token value MUST match the docker stack's CLANK_AUTH_TOKEN
# (see docker/.env or docker-compose.yml default).

# 1. Push a checkpoint AND hand off ownership to the remote.
clank push --migrate

# Output:
#   registered worktree 01J… as 'some-real-repo'
#   pushed checkpoint   01J… (HEAD a1b2c3d4)
#   migrated worktree   01J… → remote/<host_id>
#
# The bundles + manifest live in minio; the remote host has the
# materialized worktree at /root/work/<id>:
docker compose -f docker/docker-compose.yml exec -T minio \
  mc ls --recursive local/clank/checkpoints/
docker compose -f docker/docker-compose.yml exec clankd ls /root/work/

# 2. Open a session against the synced worktree. clank-host inside
# the clankd container resolves the WorktreeID to /root/work/<id>/
# and spawns opencode there — no clone, no GitHub auth needed.
clank code "summarize this codebase"

# 3. When you want to keep working on the laptop, reclaim ownership.
clank pull --migrate
```

## What's actually self-hosted

All HTTP services run in containers. Outbound traffic happens only
when the laptop pushes/pulls bundles via presigned minio URLs. No
secrets ever leave the docker network.

The "sandbox" in the default setup is a clank-host subprocess inside
the clankd container (the `local` provisioner) — useful for
end-to-end smoke testing but not what you'd run in production.

### Switching to fly.io provisioner

Edit `docker/preferences.json`:

```json
{
  "remote_hub":   { "auth_token": "<your-bearer>" },
  "default_launch_host_provider": "flyio",
  "flyio":        { "api_token": "<fly-api-token>", "organization_slug": "<slug>" }
}
```

When you start exercising sprite-side push (P6), set
`CLANK_PUBLIC_BASE_URL` in `docker/.env` to a publicly-reachable URL
of clankd — easiest is a cloudflared tunnel:

```sh
cloudflared tunnel --url http://localhost:7878
# then: CLANK_PUBLIC_BASE_URL=https://your-tunnel.trycloudflare.com
```

## Tearing down

```sh
docker compose -f docker/docker-compose.yml down            # stop + remove containers
docker compose -f docker/docker-compose.yml down -v         # also drop volumes (resets all state)
```
