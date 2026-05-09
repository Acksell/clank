# Self-hosted clank stack (docker compose)

Brings up a complete clank backend on your laptop:

- **minio** — S3-compatible object storage for checkpoint bundles
- **clank-sync** — checkpoint substrate (presigned URLs + sqlite metadata)
- **clankd** — gateway with the local provisioner (spawns clank-host
  as a subprocess inside the container so migrations land somewhere)

Everything is self-contained — no fly.io, daytona, or AWS account
needed. Useful for smoke-testing the sync/migration flow end-to-end.

## One-time setup

The presigned URLs that clank-sync mints for the laptop reference
the minio container by its hostname (`clank-minio`). For your laptop
to dial that hostname when uploading bundles, add a single
`/etc/hosts` line:

```sh
echo "127.0.0.1 clank-minio" | sudo tee -a /etc/hosts
```

Without this, `clank sync push` from the laptop will hang on PUT.

## Bringing the stack up

```sh
cp docker/.env.example docker/.env             # tweak ports / passwords if needed
docker compose -f docker/docker-compose.yml --env-file docker/.env up -d --build
```

Health-checks:

```sh
curl -fsS http://localhost:8081/v1/health      # clank-sync
curl -fsS http://localhost:7878/ping           # clankd (open without auth)
open http://localhost:9001                     # minio console (clankadmin / clankadmin)
```

Logs:

```sh
docker compose -f docker/docker-compose.yml logs -f clank-sync clankd
```

## Smoke-testing the migration flow

From the laptop, with the stack running:

```sh
# 1. Push a checkpoint of any local repo.
export CLANK_SYNC_URL=http://localhost:8081
go run ./cmd/clank sync push ~/some-real-repo

# Output:
#   registered worktree 01J… as 'some-real-repo'
#   pushed checkpoint   01J… (HEAD a1b2c3d4)
#
# The bundles + manifest now live in minio:
docker compose -f docker/docker-compose.yml exec -T minio \
  mc ls --recursive local/clank/checkpoints/

# 2. Trigger a migration. clankd's TCP listener requires the bearer
# token from docker/preferences.json (default: clank-dev-token-change-me).
WORKTREE_ID=$(cat ~/some-real-repo/.clank/worktree-id)
DEVICE_ID=$(cat ~/.config/clank/device-id)
curl -X POST http://localhost:7878/v1/migrate/worktrees/$WORKTREE_ID \
  -H "Authorization: Bearer clank-dev-token-change-me" \
  -H "X-Clank-Device-Id: $DEVICE_ID" \
  -d '{"direction":"to_sprite","confirm":true}'

# Output:
#   {"worktree_id":"…","new_owner_kind":"sprite","new_owner_id":"…","checkpoint_id":"…"}

# 3. Inspect what the local provisioner did. clank-host runs as a
# subprocess inside the clankd container; the working tree should
# now contain your repo's files at /root/work/<repo>:
docker compose -f docker/docker-compose.yml exec clankd \
  ls -la /root/work/
```

## What's actually self-hosted

All HTTP services run in containers. Outbound traffic happens only
when the laptop pushes/pulls bundles via presigned minio URLs. No
secrets ever leave the docker network.

The "sandbox" in this setup is a clank-host subprocess inside the
clankd container — useful for end-to-end smoke testing but not what
you'd run in production. Real cloud sandboxes come from swapping the
provisioner: edit `docker/preferences.json` to use `flyio` or
`daytona` and provide the corresponding API token (see
`internal/cli/daemoncli/provisioner.go` for the required preference
fields).

## Tearing down

```sh
docker compose -f docker/docker-compose.yml down            # stop + remove containers
docker compose -f docker/docker-compose.yml down -v         # also drop volumes (resets all state)
```
