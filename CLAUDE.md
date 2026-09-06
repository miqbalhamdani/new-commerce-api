# backend — Go API and worker

Phase 1 · Catalog & Foundation. Go 1.26 · PostgreSQL 18 · Redis 8 · Cloudflare R2.

**The contract lives in `contracts/`** (git submodule, pinned to a tag). Read
`contracts/API spec.md` and `contracts/erd.md` before changing anything they cover. If the code
and the contract disagree, the code is wrong — unless the contract is, in which case change it
there first, in its own PR.

---

## Commands

```bash
make dev          # run the API against host PostgreSQL and Redis
make db-create    # create the local development database
make migrate      # apply migrations
make generate     # sqlc + oapi-codegen. MUST be a no-op on a clean tree
make test         # unit + integration (testcontainers). Needs docker
make test-iso     # tenant isolation suite over every registered route
make lint         # golangci-lint + go-arch-lint import rules
make lint-rls     # fail if a tenant table has no RLS policy
make check        # generate + lint + test. Run before every PR
```

`make check` green is the bar for a PR. CI runs exactly these.

Where the repo stands today: `test` runs against the host services rather than testcontainers.
Everything else in this list is real.

The generator is pinned in `go.mod` as a `tool` directive and invoked as `go tool oapi-codegen`,
so `make generate` produces the same bytes on every machine without anyone installing anything.

---

## Layout

```
cmd/api/          HTTP API. Stateless, 2 replicas behind Caddy.
cmd/worker/       Queue consumer. Phase 1: image derivatives, CSV export.
cmd/migrate/      Runs migrations to completion, exits.
internal/
  platform/       config, logging, tracing, errors — imported by everything
  db/             pool, InTenantTx, sqlc output. THE ONLY package that imports pgx
  tenant/         request-scoped tenant context
  auth/           JWT, argon2id, API keys, RBAC
  catalog/        products, variants, categories, brands, media
  storage/        R2 presign, HEAD validation
  queue/          Redis Streams
  http/           chi router, middleware, handlers, DTOs
db/migrations/    golang-migrate, plain SQL, up + down
contracts/        submodule, pinned to a tag — READ ONLY from here
```

### Import rules (enforced by `go-arch-lint` in CI)

- Only `internal/db` imports `pgx`.
- Domain packages do not import each other. They compose in `internal/http`, or through an
  interface declared by the consumer.
- Nothing imports `internal/http`.

These exist so extracting a service later is mechanical. Breaking them is not a style question.

---

## Tenancy — the rules that matter most

Every tenant-owned table has `tenant_id`, `ENABLE ROW LEVEL SECURITY` **and** `FORCE ROW LEVEL
SECURITY`. `FORCE` matters: without it the table owner bypasses its own policy, and that is the
role migrations run as. The app connects as `app_user`, which owns nothing.

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
- **`app_user` is granted new tables automatically** by `ALTER DEFAULT PRIVILEGES` in the
  bootstrap migration. A schema migration does not need its own `GRANT`.
- **`sqlc`, not an ORM.** RLS, `FOR UPDATE` and partial indexes need SQL you control. It reads
  the schema from `db/migrations/` directly, so the generated types cannot drift from what is
  applied. Queries live in `db/queries/*.sql`; output is `internal/db/sqlcgen/` and is never
  edited.
- **`sqlc.yaml` maps `uuid` to `google/uuid.UUID` and `timestamptz` to `time.Time`**, rather than
  leaving the `pgtype` wrappers sqlc emits by default. That decision belongs in one file; undoing
  it means a conversion at every boundary between the database and everything else.
- **Keys** are UUID v7, generated in Go (`uuid.NewV7`). Never `gen_random_uuid()` in application
  inserts — time-ordered keys keep B-tree inserts append-only. (Migrations and backfills may use
  it; nothing reads those keys for ordering.)
- **Money** is `bigint` minor units + `char(3)`. Never `float`, never `numeric` for money.
- **Timestamps** are `timestamptz`, always UTC.
- **Soft delete** is `archived_at`, only where an audit trail needs it.
- **A new table with `tenant_id` and no RLS policy fails CI.** Do not disable that check. It is
  `make lint-rls`, part of `make check`, and it reads the live database rather than the migration
  files -- a migration written correctly and never applied passes a source-level check and still
  leaves the table open. It also catches the two failures the phrase above does not name: RLS
  enabled without `FORCE` (which looks entirely healthy and leaks only to the owner), and a policy
  missing entirely (which filters every row and reads as an empty table).
- **`app_user` reaches a new table through `ALTER DEFAULT PRIVILEGES`**, set in the bootstrap
  migration. It applies to tables created by the migrating role, so migrations must keep running
  as the same owner.

### Category paths

`categories.path` is an `ltree` maintained by triggers — a `BEFORE` trigger computes the row's
own path, an `AFTER` trigger rebases descendants, guarded by `pg_trigger_depth() > 1`. Do not
compute paths in Go, and do not accept `path` from a client. `contracts/erd.md` §3.4 has the
full DDL and the reasoning.

---

## HTTP

- The generated server interface comes from `contracts/openapi.yaml` via `oapi-codegen`. Do not
  hand-write route registration for a documented endpoint.
- **Errors are RFC 9457** `application/problem+json` with a `trace_id`. Use `platform/errors`;
  never `http.Error` in a handler.
- **Server-managed fields are ignored on create and `422` on update**: `id`, `tenant_id`,
  `version`, `created_at`, `updated_at`, `path`.
- **Omitted ≠ null.** An absent key takes the default; an explicit `null` is a validation error.
  Use pointer-or-sentinel decoding, not zero values.
- **`If-Match` on every `PATCH`**, checked as `UPDATE ... WHERE version = $n`. Zero rows affected
  is `409 version_conflict`, not a silent no-op.
- **Cursor pagination only.** No `offset` — deep offsets on a large table are a sequential scan.
- Tenant comes from the token. **Never** from a header, query param or body.

---

## Testing

| Layer | How |
|---|---|
| Unit | Pure logic: slugify, matrix diff, permission checks |
| Integration | Real PostgreSQL via testcontainers. **Do not mock the database** — RLS and triggers cannot be mocked meaningfully |
| Isolation | `make test-iso`: two seeded tenants, every registered route, zero leakage |
| Migration | Every migration applied to a restored snapshot in CI |

A new route without an isolation test is an incomplete route, and `make test-iso` says so rather
than leaving it to a reviewer: it walks the routes the generated code registers and fails on any
that has no entry in `isolationCases` (`internal/http/isolation_test.go`). Add the case in the
same PR as the route.

A case supplies two things -- how to seed a row as a given tenant, returning a marker string, and
how to build the request tenant A makes. The suite seeds both tenants, sends A's request, and
fails if B's marker appears anywhere in the response. Searching the raw bytes rather than a parsed
shape is deliberate: one check covers every route whatever its payload.

---

## Never do these

- `pool.Query` outside `internal/db`.
- A raw `UPDATE` on a table that has a `version` column, without checking it.
- Log a credential, a token, an API key, or a password hash. The logging redaction list is an
  **allow-list** — a new field is redacted by default.
- Accept `tenant_id` from a request.
- Add a quantity column to `variants`. There is none, in any phase. Stock is Phase 4 and lives in
  an append-only ledger keyed on (variant, location).
- Return `200` with an error body.
- Widen a `CHECK` constraint without a migration and a contract PR.

---

## Phase 1 scope guard

No stock. No orders. No marketplace channels. No `Idempotency-Key`. No bulk or CSV import.

If a task implies any of those, it belongs to a later phase — stop and say so rather than
building a partial version that a later phase then has to unpick. `contracts/BACKLOG.md` has the
out-of-scope table.

The one thing that *looks* like a channel feature and is in scope: `GET /v1/products/export`,
which renders marketplace-shaped CSV for the merchant to upload themselves. No `channels` table
is involved.
