# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

# backend — Go API

Phases 0–3 (foundation, catalog, orders, storefront API) are built and mostly in `review`;
Phase 6a (CI, the Compose stack) is under way, Phase 4 (Shopee import) is next in the backlog.
Go 1.26 · PostgreSQL 18 · Redis 8 · Cloudflare R2 (MinIO locally).

**The contract lives in `contracts/`** (`contracts/VERSION`). This repo owns it; `new-commerce-web`
keeps a read-only mirror. `contracts/CLAUDE.md` says how the documents relate and are versioned:

| File | What it holds |
|---|---|
| `01-product-requirements.md` | Scope, screens, journeys, non-functional targets |
| `02-business-rules.md` | Every `BR-xxx` rule cited in code, tests and the backlog |
| `03-erd.md` | Schema DDL. Migrations transcribe it |
| `04-api-spec.md` | Routes, payloads, errors, the permission matrix (§3) |
| `05-backlog.md` | Items per phase, Acceptance, Rules, Definition of Done |
| `openapi.yaml` | The admin `/v1` contract the server is generated from |
| `storefront.yaml` | The `/v1/storefront` contract (`internal/storefront`) |

Read the relevant sections before changing anything they cover. If the code and the contract
disagree, the code is wrong — unless the contract is, in which case change `contracts/` first, as its
own commit in the same PR.

**Workflow and "done" are `05-backlog.md`**: take the topmost `todo` item whose dependencies are
done; an item is done only when its Acceptance is proven, with a test wherever a test is possible.
Claiming (`wip` + owner), branch naming (`p1-<id>-<slug>`) and the other statuses are in its "Rules".

---

## Commands

```bash
make dev          # run the API against host PostgreSQL and Redis
make worker       # run the background job worker (cmd/worker)
make storage-init # create the dev bucket in MinIO: public product images, expiry rules (R2 stand-in)
make dev-seed     # a demo shop to sign in as: owner@example.com / development-password (DEV_SEED_EMAIL, DEV_SEED_PASSWORD)
make db-create    # create the local development database
make migrate      # apply migrations
make migrate-down N=1  # roll back N (local only)
make generate     # sqlc + oapi-codegen. MUST be a no-op on a clean tree
make generated-diff  # fail if generated files differ from what is committed
make test         # unit + integration against host PostgreSQL, Redis and MinIO (P1-000)
make test-iso     # tenant isolation suite over every registered admin route
make fmt-check    # gofmt -l
make vet          # go vet ./...
make lint         # golangci-lint (v2 config: errcheck, govet, ineffassign, staticcheck, unused, gofmt)
make lint-rls     # fail if a tenant table has no RLS policy (reads the live database)
make check        # generate, generated-diff, fmt-check, vet, lint, lint-rls, test
```

There is no `make help` and no `build` target; `go build ./...` compiles everything (cgo:
libvips is needed for `internal/images`). Every target sources `.env` if present; the compiled-in
defaults (`internal/platform/config`) already match `.env.example`, so tests and `go run` work
without one.

```bash
make db-create migrate storage-init             # once, before the first test run
go test ./internal/http/ -run TestBrands -v     # one package / one test (no .env needed)
go test -short ./...                            # skips the slow 10,000-variant tests
GOFLAGS=-p=1 make test                          # what CI does: packages share the Redis
                                                # `jobs` stream, so parallel packages can steal
                                                # each other's jobs and flake
```

Integration tests fail rather than skip when a service is down (`cmd/api/health_test.go`
explains why); a red test with "connection refused" means start the service, not skip the test.

`make check` green is the bar for a PR. CI (`.github/workflows/ci.yml`, P1-004) runs it on every
PR against PostgreSQL 18, Redis 8 and a source-built MinIO, plus the PR's migrations over main's
seeded schema and the contract lint; run it locally first all the same.
Tests need PostgreSQL, Redis and MinIO running on the host
(`brew services start postgresql@18 redis minio`, then `make storage-init` once), and libvips
for the worker's image derivatives (`brew install vips pkg-config`). Local mail goes to Mailpit
(`brew services start mailpit`, inbox at http://localhost:8025) when `SMTP_ADDR` is set.

`oapi-codegen` is pinned in `go.mod` as a `tool` directive (`go tool oapi-codegen`), so it needs
no install. `sqlc` and `golangci-lint` are **not** pinned in the module: install the versions CI
uses (`SQLC_VERSION` / `GOLANGCI_LINT_VERSION` in `ci.yml`, currently sqlc 1.31.1 and
golangci-lint v2.13.2), or `make generate` / `make lint` may produce a different diff or result.

### Codegen pipeline

`make generate` does four things, and `make generated-diff` then requires the tree unchanged:
`contracts/openapi.yaml` → `internal/http/gen.go` (package `httpapi`, chi + strict server,
`oapi-codegen.yaml`); `contracts/storefront.yaml` → `internal/storefront/gen.go` (chi, non-strict,
`oapi-codegen.storefront.yaml`); a copy of `storefront.yaml` into `internal/storefront/` (embedded
and served); and `sqlc generate` (`db/migrations` + `db/queries` → `internal/db/sqlcgen`). An API
change therefore starts in `contracts/` (its own commit), then `make generate`, in one PR. CI's
`contract-lint` job lints both OpenAPI files; web's `drift` job then wants web's mirror refreshed.

---

## Layout

```
cmd/api/          HTTP API: /v1 admin, /v1/storefront from Phase 3. Stateless; 2 replicas
                  behind Caddy in production (P1-001).
cmd/migrate/      Runs migrations (embedded from db/migrations) to completion, exits. -down N
cmd/lint-rls/     RLS policy guard (make lint-rls).
cmd/dev-seed/     Demo shop + owner (make dev-seed). cmd/storage-init/: bucket + lifecycle rules
cmd/create-tenant/ A shop + owner in production, as app_user (deploy/README.md, "A new shop")
cmd/demo-seed/    A demo shop with every admin screen filled, through the real services; sends no
                  email (deploy/README.md, "A demo shop")
cmd/worker/       Redis Streams consumer (P1-060): `jobs` stream (product_import, order_export,
                  image_derivatives, customer_purge), `emails` stream (invitation,
                  password_reset, order mails), and the nightly retention sweep (P1-214).
                  Later: channel import.
internal/
  platform/       config, errors, logging, telemetry — imported by everything
  db/             pool, InTenantTx, audit recorder, SECURITY DEFINER lookups, sqlc output
  tenant/         request-scoped tenant and actor
  auth/           JWT, argon2id, RBAC (roles.go), invite / reset / order tokens, Google sign-in
  queue/          the one Redis client; stream helpers
  ratelimit/      BR-014 sliding windows, admin and storefront alike
  http/           chi router, middleware, handlers, DTOs (admin)
  httpkit/        problem+json, strict JSON decoding, cursors, tracing -- shared by http and storefront
  catalog/        products, variants, categories, brands, media, bulk upsert, CSV import
  orders/         state machine (Transition), checkout, payments, refunds, export, order mail
  carts/          storefront carts. customers/: customer accounts, sessions, password reset
  midtrans/ biteship/  payment and shipping-rate clients
  secrets/        envelope encryption for shop credentials (ENCRYPTION_KEY)
  retention/      nightly cross-shop sweep (see Tenancy)
  storage/        object store (R2 / MinIO): presign, HEAD, public URLs, lifecycle rules
  images/         WebP derivatives via libvips (cgo). Worker only -- cmd/api never imports it
  email/          senders (Resend; SMTP and stdout in development) and templates
  jobs/           jobs rows + Redis Streams runner (BR-060)
  team/           users, invitations, settings, audit-log reads
  storefront/     storefront routes from contracts/storefront.yaml, behind the API-key middleware;
                  never imports the admin http package, nor it this (a test enforces it)
deploy/           the production stack: Compose, Caddyfile, deploy.sh, and the same stack on a
                  developer machine (deploy/README.md). Dockerfile: one image, every binary
db/migrations/    golang-migrate, plain SQL, up + down
db/queries/       sqlc queries
contracts/        the contract (docs + OpenAPI) — owned here, mirrored by new-commerce-web
```

### Import rules

Not tool-enforced yet (except the admin/storefront boundary, enforced by
`TestAdminAndStorefrontDoNotImportEachOther` in `internal/storefront/keys_test.go`); checked in
review until a linter lands.

- Only `internal/db` holds the pool (`pgxpool`) and runs queries outside a transaction. Other
  packages see pgx only as the `pgx.Tx` that `InTenantTx` hands them (plus `pgx.ErrNoRows`).
- Domain packages (`catalog`, `orders`, `carts`, `customers`, `team`) do not import each other.
  They may use infrastructure (`db`, `jobs`, `storage`, `email`, `auth`, `secrets`, `midtrans`,
  `biteship`), and compose in `internal/http` / `internal/storefront` / `cmd/*`, or through an
  interface declared by the consumer.
- Nothing imports `internal/http`. Admin and storefront handlers do not import each other (P1-202).

These exist so extracting a service later is mechanical. Breaking them is not a style question.

### Request flow

`cmd/api/main.go` builds one `db.Store` (as `app_user`), the Redis client and the services, then
mounts two routers on one mux, plus `GET /healthz`:

- **Admin `/v1/*`** (`internal/http/router.go`): `tracing` → `Authenticate` (staff JWT → tenant,
  role and actor into the context; the short `unauthenticated` list mirrors `security: []` --
  login, refresh, accept-invite, the Midtrans webhook) → admin rate limiter → generated chi routes
  → handler wrapped in `requirePermission` → domain service → `store.InTenantTx` → sqlc.
- **Storefront `/v1/storefront/*`** (`internal/storefront/router.go`): tracing → docs (no key) →
  `Keys.Middleware` (`X-Api-Key` → shop via `resolve_api_key`, cached; Origin checks, BR-083) →
  storefront limits → optional customer token → generated routes.

Background work: a handler creates a `jobs` row inside its `InTenantTx` and enqueues it on the
Redis stream after commit; `cmd/worker` delivers it (at-least-once, reclaimed after `ClaimIdle`,
`MaxDeliveries` 5) and records the outcome the API's `GET /v1/jobs/{id}` reads.

---

## Tenancy — the rules that matter most

Every tenant-owned table has `tenant_id`, `ENABLE ROW LEVEL SECURITY` **and** `FORCE ROW LEVEL
SECURITY`. `FORCE` matters: without it the table owner bypasses its own policy, and that is the
role migrations run as. The app connects as `app_user`, which owns nothing.

**The tenant comes from a credential, never from the request** (BR-003): the staff token, the API
key, a verified Midtrans notification, or a signed OAuth `state`. Never a header, query parameter
or body field.

**Four reads cross a tenant boundary** (BR-003), each through a `SECURITY DEFINER` function whose
returned columns are fixed at definition time:

| Read | Function | Lands with |
|---|---|---|
| Staff login | `auth_lookup_user` | P1-011 |
| Staff token refresh | `auth_lookup_refresh_token` | P1-011 |
| API key resolution | `resolve_api_key` | P1-019 |
| Midtrans notification | `resolve_midtrans_webhook` | P1-216 (used by P1-221) |

They are hand-written in `internal/db/auth_lookup.go` rather than generated, so they do not blend
in with code that all looks alike. Adding a fifth is a schema change, a contract change, and a
conversation.

**`SECURITY DEFINER` alone does not defeat `FORCE RLS`** -- the definer is the table's owner, and
`FORCE` binds the owner. Those functions belong to `auth_lookup`, a `NOLOGIN BYPASSRLS` role that
owns nothing else. On a developer's machine the schema owner is a superuser and it would appear to
work without that role; in production it would not.

**Work that spans shops** (the nightly retention sweep, `internal/retention`) has no role of its
own: `Store.TenantIDs` lists shop ids (`tenants` has no RLS), then each shop's work runs in that
shop's own `InTenantTx` as `app_user`, audit-logged, never reachable from a request (BR-003). Do
not add a `BYPASSRLS` pool for it.

**Two roles, two DSNs.** `cmd/migrate` connects as the schema owner via `DATABASE_URL`.
`cmd/api` connects as `app_user` via `APP_DATABASE_URL` and never as the owner -- locally the
owner is a superuser, and a superuser bypasses RLS outright, `FORCE` included. Point the API at
`DATABASE_URL` and every policy in the schema becomes decorative.

**Limited-roles mode** (BR-001) is the one exception, for a managed database whose owner can
create neither role (production on Sumopod's shared plan). `DB_ROLE_MODE=limited` on the *first*
migrate freezes it into `db_role_mode()`; migrations branch on that function, and the strong
branch must stay statement-for-statement what it was. In limited mode the app connects as the
owner (not a superuser, so `FORCE` binds it), `enable_tenant_rls` adds a `SELECT`-only
`limited_read_bypass` policy, `USING (rls_bypass_on())`, and the four lookups are plpgsql that
set `app.rls_bypass` around their one query. `rls_bypass_on()` is deliberately `IMMUTABLE` so the
planner folds it to false for the app and `tenant_id = X OR …` still uses the tenant indexes; the
cost is that a statement keeps the value it was planned with. **So only those functions and
`deploy/backup.sh` may set `app.rls_bypass` -- never an application session**, or a statement
planned with it on sees every tenant afterwards. `TestNothingSetsRLSBypass` fails on Go code that
names it, and `lint-rls` checks the policy's shape and `rls_bypass_on()`'s exact body. CI's `limited-roles` job runs the suite on PostgreSQL 16 as such
an owner; its test owner DSN carries `app.rls_bypass` so test oracles can read across tenants.

**Every query goes through `InTenantTx`.** The tenant is set per *transaction* with
`set_config(..., true)`, never per connection — pgx pools connections and a leaked `SET` follows
into the next request's tenant.

```go
// Correct.
err := store.InTenantTx(ctx, func(tx pgx.Tx) error {
    return q.WithTx(tx).CreateProduct(ctx, params)
})
```

**Fail closed.** No tenant in context returns `ErrNoTenantContext`. Do not let it fall through:
with no tenant set, `current_setting('app.tenant_id', true)` is `NULL`, the policy is `NULL`, and
every row is filtered out — safe, but it presents as a baffling empty result instead of a bug.

**Another tenant's row is `404`**, identical to a row that does not exist (BR-011).

**Composite FKs on denormalised `tenant_id`.** `variants.tenant_id` is derived from `products`,
denormalised for RLS performance and for `UNIQUE (tenant_id, sku)`. The composite FK is what
makes divergence impossible. Every child table gets one.

**Composite indexes lead with `tenant_id`.** RLS adds that predicate to every query; an index
that cannot serve it is dead weight.

---

## Database

- **Migrations are forward-only in production.** Write a `down` for local use; never rely on it.
  `make migrate` applies them and exits, `make migrate-down N=1` rolls back locally.
- **A migration that reads or writes tenant rows across shops must not rely on the owner seeing
  them.** In limited-roles mode the owner is bound by `FORCE`: an unscoped read sees nothing and an
  unscoped write is refused, so a data guard like 000006's `IF EXISTS (SELECT 1 FROM api_keys)`
  would silently pass. Set a tenant per shop, or wrap the work in `ALTER TABLE … NO FORCE ROW LEVEL
  SECURITY` / `FORCE …` inside the migration's own transaction. Never `app.rls_bypass` (BR-001).
- **A new tenant table calls `SELECT enable_tenant_rls('<table>');` in its own migration.** That
  is the whole of the tenancy setup for a table -- the function does `ENABLE`, `FORCE` and the
  `tenant_isolation` policy, and refuses a table with no `tenant_id`. Do not hand-write the four
  statements; the failure mode of a missing `FORCE` is silent.
- **A new table with `tenant_id` and no RLS policy fails `make lint-rls`** (part of `make
  check`). Do not disable that check. It reads the live database rather than the migration files
  -- a migration written correctly and never applied passes a source-level check and still leaves
  the table open. It also catches RLS enabled without `FORCE` (which looks healthy and leaks only
  to the owner), and a policy missing entirely (which filters every row and reads as empty).
- **`app_user` reaches a new table through `ALTER DEFAULT PRIVILEGES`**, set in the bootstrap
  migration. It applies to tables created by the migrating role, so migrations must keep running
  as the same owner. Sequences are not covered: a `bigserial` table needs its own
  `GRANT USAGE ON SEQUENCE … TO app_user` (see `audit_log`).
- **`sqlc`, not an ORM.** RLS, `FOR UPDATE` and partial indexes need SQL you control. It reads
  the schema from `db/migrations/` directly, so the generated types cannot drift from what is
  applied. Queries live in `db/queries/*.sql`; output is `internal/db/sqlcgen/` and is never
  edited.
- **`sqlc.yaml` maps `uuid` to `google/uuid.UUID` and `timestamptz` to `time.Time`**, rather than
  leaving the `pgtype` wrappers sqlc emits by default. That decision belongs in one file; undoing
  it means a conversion at every boundary between the database and everything else.
- **Keys** are UUID v7, generated in Go (`uuid.NewV7`). Never `gen_random_uuid()` in application
  inserts — time-ordered keys keep B-tree inserts append-only. (Migrations and backfills may use
  it.) The one exception is `carts.id`, a random v4, because the id is the cart's bearer token
  (BR-005). Internal `bigserial` ids never appear in an API.
- **Money** is `bigint` minor units + `char(3)` (always `IDR`) in the database. On the wire it is a
  plain integer of minor units with no currency field: `"total": 39800000` is Rp 398.000 (BR-006).
  Midtrans and Biteship speak whole rupiah: divide by 100 going out, multiply coming back. Never
  `float`, never `numeric`. Prices are always computed server-side through `variant_price()`
  (BR-046, BR-089).
- **Timestamps** are `timestamptz`, and WIB everywhere (BR-007): the pool sets `TimeZone`, scans
  into WIB, and the wire is `+07:00`, never `Z`. Any input offset is accepted; one without an
  offset is `422` (decode bodies with `decodeJSON`). A date-only filter means midnight WIB.
- **`DELETE` archives** brands, categories, products and variants (`archived_at`, `204`). Users
  are disabled, API keys revoked, orders never deleted. Only media is hard-deleted (BR-012).

### Category paths

`categories.path` is an `ltree` maintained by triggers — a `BEFORE` trigger computes the row's
own path, an `AFTER` trigger rebases descendants, guarded by `pg_trigger_depth() > 1`. Do not
compute paths in Go, and do not accept `path` from a client. `contracts/03-erd.md` §3.3 (table)
and §3.7 (trigger) have the DDL; a move beneath a descendant is `422` on `parent_id` (BR-034).

---

## HTTP

- The generated server interfaces come from `contracts/openapi.yaml` (admin, `internal/http`) and
  `contracts/storefront.yaml` (`internal/storefront`) via `oapi-codegen`. Do not hand-write route
  registration for a documented endpoint. A path appears in either file when its backlog item is
  done. The storefront spec is copied into `internal/storefront` at generate time, embedded, and served, with no key, at
  `/v1/storefront/openapi.json` and as a Scalar page at `/v1/storefront/docs` (P1-213); bumping
  Scalar means a new `integrity` hash in `internal/storefront/docs.go`.
- **Errors are RFC 9457** `application/problem+json` with a `trace_id`. Use `platform/errors`;
  never `http.Error` in a handler, and never `200` with an error body.
- **A permission check goes at the handler boundary**, as `requirePermission("resource:action", …)`
  wrapping the generated method. Not in a service and not in a query: deeper means every new call
  path has to remember it, and the one that forgets is a silent authorisation hole rather than a
  compile error.
- **The `403` names the permission in `detail`** (BR-024) -- the difference between "you cannot do
  this" and "ask your owner for `users:write`".
- **`internal/auth/roles.go` is the only definition of the matrix**: four roles, owner, admin, ops,
  viewer (BR-023). The handler check, the login response and `GET /roles` all read from it, and a
  test asserts it equals the table in `04-api-spec.md` §3.
- **Unknown fields are `422 unknown_field`**, never silently ignored (04 §1, BR-089).
- **Server-managed fields are `422 validation_failed` on create and update alike** (BR-008): `id`,
  `tenant_id`, `version`, `created_at`, `updated_at`, `path`, any `*_at` stamp, and derived slugs
  (`brands.slug`). Product `slug` is the exception: editable (BR-042).
- **Omitted ≠ null** (BR-009). Create: omitted takes the default, `null` is `422`. `PATCH`: omitted
  is unchanged, `null` clears a column the ERD marks nullable and is `422` for any other. Use
  pointer-or-sentinel decoding, not zero values.
- **Responses carry every field**; an empty optional field is `null`, references expand to
  `{id, name}` (04 §1).
- **`If-Match: <version>` only on products, variants and orders** — every `PATCH` to them and
  `PUT …/variant-matrix` (with the product's version) (BR-010). Checked as
  `UPDATE … WHERE version = $n`; zero rows is `409 version_conflict`, a missing header is `422`.
  Brands, categories, settings, users, media and API keys have no `version`: last save wins.
- **Cursor pagination only**: `?limit=` 1–200 and `?cursor=`, answered `{data, next_cursor}`,
  `null` on the last page. No `offset`. Unpaginated lists omit `next_cursor`; `GET /v1/roles` is
  the one bare array.
- **Rate limits** (BR-014): admin is 100 req/min per user on a Redis sliding window, per-process
  when Redis errors. `RateLimit-*` headers on every response; over the limit is
  `429 rate_limited` with `Retry-After`. Storefront limits (browser per IP and key, server per
  key and IP, checkout per IP) and the login limits (per email and IP, staff and customer) run
  on the same `internal/ratelimit`.
- **Every admin write records exactly one `audit_log` row** with `db.Audit`, inside the same
  `InTenantTx`, so a rollback leaves none (BR-018). A guard test fails on a mutating route with
  neither an audit case nor a stated exemption (`internal/http/audit_test.go`).

---

## Testing

| Layer | How |
|---|---|
| Unit | Pure logic: slugify, matrix diff, permission checks |
| Integration | Real PostgreSQL and Redis on the host. **Do not mock the database** — RLS, triggers, row locks and `security_invoker` views cannot be mocked meaningfully |
| Isolation | `make test-iso` (P1-008): two seeded tenants, every registered admin route, zero leakage. Storefront routes are covered in `internal/storefront` (`isolation_test.go`, P1-211: two tenants × two customers; `authmatrix_test.go`, P1-210: origin × token matrix), run by `make test`, not `test-iso` |
| Concurrency | `go test -race`; checkout proven with 20 parallel requests on one cart (P1-205) |
| Migration | CI's `migrate-snapshot`: the PR's migrations over main's schema with `dev-seed` data, then `lint-rls` (P1-004). A restored production backup replaces the seed once production exists |

**A route added to `openapi.yaml` needs its isolation case in the same PR.** A new route without
an isolation test is an incomplete route, and `make test-iso` says so rather than leaving it to a
reviewer: it walks the routes the generated code registers and fails on any that has no entry in
`isolationCases` (`internal/http/isolation_test.go`). A mutating route also needs its
`auditCases` entry. A storefront route needs its P1-211 case.

A case supplies two things -- how to seed a row as a given tenant, returning a marker string, and
how to build the request tenant A makes. The suite seeds both tenants, sends A's request, and
fails if B's marker appears anywhere in the response, or if any timestamp in it ends in `Z`.
Searching the raw bytes rather than a parsed shape is deliberate: one check covers every route
whatever its payload.

---

## Never do these

- `pool.Query` outside `internal/db`.
- A raw `UPDATE` on a table that has a `version` column, without checking it. A bare
  `UPDATE orders SET status` outside `Transition` (BR-071).
- Log anything off the allow-list in `internal/platform/logging` — a new field is redacted by
  default (BR-013). Never, allow-list or not: `Authorization`, `X-Api-Key`, `X-Order-Token`,
  cookies, passwords, channel credentials, Midtrans server keys, Google ID tokens.
- Accept `tenant_id` from a request.
- Add stock in any form: no quantity on `variants`, no locations, reservations or sold-out flags.
  Visible means orderable (BR-017). If stock ever returns it is a (variant, location) ledger, never
  a column.
- Assume a job runs once. Delivery is at-least-once; every handler must be safe to run twice
  (BR-060).
- Widen a `CHECK` constraint without a migration and a contract change.

---

## Scope (v2)

| Phase | What |
|---|---|
| 0 Foundation | Tenancy, auth, RBAC, errors, rate limits, logs, audit, WIB |
| 1 Catalog | Brands, categories, products, variants, matrix, media, bulk upsert, CSV import, team, settings |
| 2 Orders | State machine, list/detail, manual entry, customers, order export |
| 3 Storefront API | API keys, catalog routes, carts, checkout, Midtrans, Biteship, customer accounts |
| 4 · 5 | Shopee, then Tokopedia: product import only, pulled (BR-100) |
| 6 | Production, hardening, pilot |

**Never, in any phase:** stock (BR-017), `Idempotency-Key` (cart idempotency is BR-088),
marketplace order sync or stock/price push (BR-100), outbound webhooks, courier booking or labels,
multi-currency, catalog CSV export to marketplaces (BR-061, retired).

If a task implies something from a later phase, stop and say so rather than building a partial
version that phase then has to unpick. `contracts/05-backlog.md` has the phase of every item and
the "Out of scope for v2" table.
