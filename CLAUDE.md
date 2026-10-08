# backend — Go API

Phase 0 · Foundation (Phase 1 · Catalog next). Go 1.26 · PostgreSQL 18 · Redis 8 · Cloudflare R2.

**The contract lives in `contracts/`** (git submodule, pinned to a tag):

| File | What it holds |
|---|---|
| `01-product-requirements.md` | Scope, screens, journeys, non-functional targets |
| `02-business-rules.md` | Every `BR-xxx` rule cited in code, tests and the backlog |
| `03-erd.md` | Schema DDL. Migrations transcribe it |
| `04-api-spec.md` | Routes, payloads, errors, the permission matrix (§3) |
| `05-backlog.md` | Items per phase, Acceptance, Rules, Definition of Done |
| `openapi.yaml` | The admin `/v1` contract the server is generated from |

Read the relevant sections before changing anything they cover. If the code and the contract
disagree, the code is wrong — unless the contract is, in which case change it there first, in its
own commit.

**Workflow and "done" are `05-backlog.md`**: take the topmost `todo` item whose dependencies are
done; an item is done only when its Acceptance is proven, with a test wherever a test is possible.

---

## Commands

```bash
make dev          # run the API against host PostgreSQL and Redis
make worker       # run the background job worker (cmd/worker)
make storage-init # create the dev bucket in MinIO (stands in for R2 until P1-045)
make db-create    # create the local development database
make migrate      # apply migrations
make generate     # sqlc + oapi-codegen. MUST be a no-op on a clean tree
make test         # unit + integration against host PostgreSQL and Redis (P1-000)
make test-iso     # tenant isolation suite over every registered route
make lint         # golangci-lint
make lint-rls     # fail if a tenant table has no RLS policy
make check        # generate, generated-diff, fmt-check, vet, lint, lint-rls, test
```

`make check` green is the bar for a PR. There is no CI yet (P1-004, Phase 6); run it locally.
Tests need PostgreSQL, Redis and MinIO running on the host
(`brew services start postgresql@18 redis minio`, then `make storage-init` once).

The generator is pinned in `go.mod` as a `tool` directive and invoked as `go tool oapi-codegen`,
so `make generate` produces the same bytes on every machine without anyone installing anything.

---

## Layout

```
cmd/api/          HTTP API: /v1 admin, /v1/storefront from Phase 3. Stateless; 2 replicas
                  behind Caddy in production (P1-001).
cmd/migrate/      Runs migrations to completion, exits.
cmd/lint-rls/     RLS policy guard (make lint-rls).
cmd/worker/       Redis Streams consumer (P1-060). Phase 1: image derivatives, product CSV
                  import, invitation email. Later: order export, channel import, retention.
internal/
  platform/       config, errors, logging, telemetry — imported by everything
  db/             pool, InTenantTx, audit recorder, sqlc output. THE ONLY package that imports pgx
  tenant/         request-scoped tenant and actor
  auth/           JWT, argon2id, RBAC
  queue/          Redis: rate-limit windows now, Redis Streams from P1-060
  http/           chi router, middleware, handlers, DTOs (admin)
  catalog/        (Phase 1) products, variants, categories, brands, media
  storage/        object store (R2 / MinIO): presign, HEAD, public URLs
  storefront/     (P1-202) storefront routes; never imports the admin http package, nor it this
db/migrations/    golang-migrate, plain SQL, up + down
db/queries/       sqlc queries
contracts/        submodule, pinned to a tag — READ ONLY from here
```

### Import rules

Not tool-enforced yet; checked in review until a linter lands (P1-004).

- Only `internal/db` imports `pgx`.
- Domain packages do not import each other. They compose in `internal/http`, or through an
  interface declared by the consumer.
- Nothing imports `internal/http`. Admin and storefront handlers do not import each other (P1-202).

These exist so extracting a service later is mechanical. Breaking them is not a style question.

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
| Midtrans notification | — | P1-221 |

They are hand-written in `internal/db/auth_lookup.go` rather than generated, so they do not blend
in with code that all looks alike. Adding a fifth is a schema change, a contract change, and a
conversation.

**`SECURITY DEFINER` alone does not defeat `FORCE RLS`** -- the definer is the table's owner, and
`FORCE` binds the owner. Those functions belong to `auth_lookup`, a `NOLOGIN BYPASSRLS` role that
owns nothing else. On a developer's machine the schema owner is a superuser and it would appear to
work without that role; in production it would not.

**Cross-tenant admin work** (retention, purge) uses a separate `BYPASSRLS` role and pool, is
audit-logged, and is never reachable from a request (BR-003).

**Two roles, two DSNs.** `cmd/migrate` connects as the schema owner via `DATABASE_URL`.
`cmd/api` connects as `app_user` via `APP_DATABASE_URL` and never as the owner -- locally the
owner is a superuser, and a superuser bypasses RLS outright, `FORCE` included. Point the API at
`DATABASE_URL` and every policy in the schema becomes decorative.

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

- The generated server interface comes from `contracts/openapi.yaml` via `oapi-codegen`. Do not
  hand-write route registration for a documented endpoint. `openapi.yaml` covers `/v1` admin only,
  and a path appears there when its backlog item is done; the storefront has its own public
  document (P1-213).
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
  `429 rate_limited` with `Retry-After`. Storefront limits are P1-209.
- **Every admin write records exactly one `audit_log` row** with `db.Audit`, inside the same
  `InTenantTx`, so a rollback leaves none (BR-018). A guard test fails on a mutating route with
  neither an audit case nor a stated exemption (`internal/http/audit_test.go`).

---

## Testing

| Layer | How |
|---|---|
| Unit | Pure logic: slugify, matrix diff, permission checks |
| Integration | Real PostgreSQL and Redis on the host. **Do not mock the database** — RLS, triggers, row locks and `security_invoker` views cannot be mocked meaningfully |
| Isolation | `make test-iso` (P1-008): two seeded tenants, every registered admin route, zero leakage. Storefront routes also need P1-211 (two tenants × two customers) and P1-210 (origin × token matrix) |
| Concurrency | `go test -race`; checkout proven with 20 parallel requests on one cart (P1-205) |
| Migration | (P1-004) every migration applied to a restored snapshot in CI |

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
