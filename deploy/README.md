# deploy

The production stack: one VPS, Docker Compose, Caddy in front (P1-001, P1-002;
`contracts/01-product-requirements.md`, "Deployment").

| File | What |
|---|---|
| `compose.yaml` | Caddy, web, api ×2, worker, migrate (one-shot), PostgreSQL 18, Redis 8 |
| `Caddyfile` | `admin.{domain}` (the admin, and `/v1/*` to the API) and `api.{domain}` |
| `deploy.sh <tag>` | Migrate, then roll api-1, api-2, web and the worker without dropping a request |
| `auto-deploy.sh` | What CI runs over SSH on every push to main of either repo: pull, then `deploy.sh main` |
| `check.sh` | What the running stack is tuned to: Postgres and Redis settings, each container's limits (P1-002) |
| `.env.example` | Every setting; copy to `.env` on the box |
| `compose.local.yaml`, `local.env`, `local/` | The same stack on a developer machine |
| `compose.managed-db.yaml` | PostgreSQL run by a provider instead of the `postgres` service |
| `compose.small.yaml` | Limits for a 2 vCPU / 2 GB box, with the database elsewhere |
| `db-preflight.sh [--apply]` | Can a managed database take this schema? `--apply` creates `app_user` |
| `backup.sh`, `restore-drill.sh`, `systemd/` | Nightly encrypted `pg_dump` to R2, and its drill (managed database) |

## On the box

```bash
cp .env.example .env && chmod 600 .env   # fill it in
./deploy.sh sha-<commit>                 # CI pushes an image per merge to main
```

`deploy.sh` takes one tag for both images, and the two repos' shas differ:
`./deploy.sh main` deploys the newest of each.

### Auto-deploy

A push to main of this repo or `new-commerce-web` deploys once its `image`
job has pushed: the `deploy` job in `ci.yml` SSHes to the box with a key that
can run only `auto-deploy.sh`, which pulls this checkout (for changes under
`deploy/`) and runs `./deploy.sh main`. Either repo's push brings both to
main, so the order the two arrive in does not matter; a lock queues one behind
the other. The output goes to the journal, not to Actions, whose logs are
public:

```bash
journalctl -t new-commerce-deploy -e
```

Set up once, on the box, one key per repo:

```bash
ssh-keygen -t ed25519 -N '' -C gh-deploy-api -f ~/deploy-keys/gh-deploy-api   # and -web
echo "restrict,command=\"$PWD/auto-deploy.sh\" $(cat ~/deploy-keys/gh-deploy-api.pub)" >> ~/.ssh/authorized_keys
ssh-keyscan -t ed25519 <box ip> > ~/deploy-keys/known_hosts
```

and in each repo's Actions secrets: `DEPLOY_HOST` (the box's IP),
`DEPLOY_KNOWN_HOSTS` (that `known_hosts`) and `DEPLOY_SSH_KEY` (that repo's
private key, which then leaves the box). The checkout must stay on main with
no local commits, or the pull refuses and the deploy fails.

To roll back, revert on main and push. A hand-run `deploy.sh` lasts until the
next push; to hold it, take the keys' lines out of `~/.ssh/authorized_keys`.

### Managed database

Set `COMPOSE_FILE`, `MIGRATE_DATABASE_URL` and `APP_DATABASE_URL` as the end
of `.env.example` shows. Before the first deploy:

```bash
./db-preflight.sh           # version, extensions, CREATEROLE, BYPASSRLS, no pooler
./db-preflight.sh --apply   # then create app_user with APP_DB_PASSWORD
```

The migrations create `auth_lookup NOLOGIN BYPASSRLS`, the owner of the four
cross-tenant lookups (`../CLAUDE.md`, "Tenancy"). A provider that will not let
its admin role create a `BYPASSRLS` role cannot run the default mode; preflight
says so before migrate half-applies.

Such a provider (a shared plan) can run `DB_ROLE_MODE=limited` (BR-001): no
`app_user`, the app connects as the admin role, `FORCE` still binds it, and a
`SELECT`-only policy behind `app.rls_bypass` serves the four lookups and the
backup. It is weaker -- the app owns its tables -- and fixed by the first
migrate: moving to a provider with the real roles means a fresh schema in the
default mode and a data-only restore, not a switch.

### A new shop

There is no signup; the platform team opens a shop. As the app's role
(`app_user`; the owner in limited mode), inside the new shop's own transaction:

```bash
docker compose run --rm --no-deps --entrypoint create-tenant api-1 \
  -name "Shop name" -slug shop-name -order-prefix SHP \
  -owner-email owner@example.com -owner-name "Owner Name"
```

It prints the owner's password once. Invitations take it from there.

### Backups (managed database)

The provider's backups first; `backup.sh` keeps an encrypted copy in a
separate R2 bucket, nightly at 02:00 WIB. RPO is a day unless the provider's
point-in-time restore is shorter. Install the timer once:

```bash
for f in systemd/new-commerce-backup.{service,timer}; do
  sed "s|DEPLOY_DIR|$PWD|g" "$f" | sudo tee "/etc/systemd/system/${f#systemd/}" >/dev/null
done
sudo systemctl daemon-reload && sudo systemctl enable --now new-commerce-backup.timer
sudo systemctl start new-commerce-backup.service   # one now; journalctl -u it
./restore-drill.sh   # newest backup -> a throwaway postgres here, row counts compared
```

R2 deletes backups after 30 days (a lifecycle rule on the bucket). In
limited mode `backup.sh` dumps through `limited_read_bypass`
(`--enable-row-security`, `app.rls_bypass` for its connection only); a plain
`pg_dump` there refuses every tenant table.

#### Restoring into a limited-mode database

Into a new, empty provider database, as its admin role. One `pg_restore` is
enough: `pg_dump` puts `ENABLE`/`FORCE ROW LEVEL SECURITY` and the policies in
its post-data section, so the rows load before RLS exists, and the grants
(`REVOKE … FROM PUBLIC` on the lookups) come back with them:

```bash
# decrypt the newest dump as restore-drill.sh does, into ./db.dump; then, with
# DBURL=<the new database's admin URL> in the environment:
pg_restore --no-owner --exit-on-error -d "$DBURL" db.dump
DB_ROLE_MODE=limited ./db-preflight.sh     # "existing schema is in limited mode"
DATABASE_URL=$DBURL go run ../cmd/lint-rls  # FORCE and both policies on every table
```

Point `MIGRATE_DATABASE_URL` and `APP_DATABASE_URL` at it and `./deploy.sh`.
Rehearsed on PostgreSQL 16 as a role with no CREATEROLE or BYPASSRLS.

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
