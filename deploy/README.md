# deploy

The production stack: one VPS, Docker Compose, Caddy in front (P1-001, P1-002;
`contracts/01-product-requirements.md`, "Deployment").

| File | What |
|---|---|
| `compose.yaml` | Caddy, web, api ×2, worker, migrate (one-shot), PostgreSQL 18, Redis 8 |
| `Caddyfile` | `admin.{domain}` (the admin, and `/v1/*` to the API) and `api.{domain}` |
| `deploy.sh <tag>` | Migrate, then roll api-1, api-2, web and the worker without dropping a request |
| `check.sh` | What the running stack is tuned to: Postgres and Redis settings, each container's limits (P1-002) |
| `.env.example` | Every setting; copy to `.env` on the box |
| `compose.local.yaml`, `local.env`, `local/` | The same stack on a developer machine |

## On the box

```bash
cp .env.example .env && chmod 600 .env   # fill it in
./deploy.sh sha-<commit>                 # CI pushes an image per merge to main
```

## On a developer machine

Docker Desktop running, both repos checked out side by side. Caddy signs
`admin.localhost` and `api.localhost` with its own CA; MinIO stands in for R2
and a fixed-rate stub for Biteship.

```bash
cd deploy
export COMPOSE_FILE=compose.yaml:compose.local.yaml COMPOSE_ENV_FILES=local.env
docker compose build
docker compose up -d --wait
ENVIRONMENT=development \
  APP_DATABASE_URL='postgres://app_user:local-app-user@localhost:55432/new_commerce?sslmode=disable' \
  go run ../cmd/dev-seed                 # a shop to sign in to
open https://admin.localhost             # accept Caddy's certificate once
SKIP_PULL=1 ./deploy.sh local            # a rolling deploy of what is running
docker compose down                      # -v as well to start from empty
```

A browser upload does not work locally: presigned URLs name `minio:9000`,
which only the containers can resolve. R2 has no such problem.
