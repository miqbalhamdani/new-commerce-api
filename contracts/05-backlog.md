# Ecommerce Backoffice v2 · Backlog

**In order.** Items are listed in the order they are built. Dependencies flow downward; nothing
depends on something below it. Ids record when an item was added, not its position: take the
**topmost** `todo` item whose dependencies are `done`.

Acceptance cites the rules it proves (`BR-xxx`, `02-business-rules.md`); the rule text lives
there. Section references (`§7.3`) point into `04-api-spec.md` unless another file is named.

**Status** `todo` · `wip` · `review` · `done` · `blocked` · `dropped`
**Repo** `BE` backend · `FE` frontend · `CT` contracts · `OPS` infrastructure

Ids keep the `P1-` prefix from v1 so branches, commits and PR history stay readable. Phases follow
the v2 delivery plan (about 28 weeks).

---

## Rules

- **One item, one PR, one branch** named `p1-<id>-<slug>` (e.g. `p1-205-checkout`).
- **Claim by setting `wip` and your name in Owner**, in a commit to this file, before writing code.
  Two agents on one item is the failure this file exists to prevent.
- **An item is `done` only when its Acceptance is proven true**, not when the code merges. If
  acceptance needs a test, the test is part of the item.
- **A `BE` item and its `FE` counterpart are separate items.** Each ships on its own; the frontend
  works against the generated client and can be built before the backend once the contract exists.
- **`blocked` needs a reason** in a note under the table. A blocked item without one is `todo`.
- Don't reorder items for convenience. If the order is wrong, say why in the PR.

---

## Phase 0 · Foundation (weeks 1–4)

Nothing user-visible. Everything after depends on all of it.

**Exit:** the tenant isolation suite is green over every route.

**Host services, not containers.** A dev machine already running PostgreSQL and Redis gains nothing
from a second copy in Docker. So the dev versions are whatever the machine runs, which is why the
pins are PostgreSQL 18 and Redis 8 while there is no server and no data to migrate. The v2 spec
names PostgreSQL 16 and Redis 7; production follows the dev pins instead, so a feature that only
exists in 18 can never reach a 16 server (`P1-002`). Revisit only once the machine exists.

| ID | Item | Repo | Depends | Acceptance | Status | Owner |
|---|---|---|---|---|---|---|
| P1-000 | Local dev services: PostgreSQL 18 + Redis 8 on the host | OPS | — | `make dev` connects to PostgreSQL 18 on the host at `:5432` and Redis 8 at `:6379`; `GET /healthz` reports both | done | Iqbal Hamdani |
| P1-005 | `openapi.yaml` skeleton + generators wired in both repos | CT/BE/FE | 000 | `make generate` changes nothing on a clean tree in both repos | done | Iqbal Hamdani |
| P1-006 | Migration runner, non-owner `app_user` role, RLS helper | BE | 000 | `app_user` owns nothing; `FORCE RLS` on every tenant table (BR-001) | done | Iqbal Hamdani |
| P1-007 | `InTenantTx`, tenant context, fail closed with no tenant | BE | 006 | No tenant returns `ErrNoTenantContext`, never an empty result (BR-002) | done | Iqbal Hamdani |
| P1-008 | **Tenant isolation suite over every registered route** | BE | 007 | Two tenants seeded; token A returns zero of B's rows on every route (BR-001, BR-003) | done | Iqbal Hamdani |
| P1-009 | RLS policy guard | BE | 006 | `make lint-rls` exits non-zero for a `tenant_id` table without a policy (BR-001) | done | Iqbal Hamdani |
| P1-010 | Schema `tenants`, `users`, `refresh_tokens`, `api_keys` | BE | 006 | Matched `03-erd.md` §3.2 as of v1.6.1; v2 changes are `P1-017` and `P1-019` | done | Iqbal Hamdani |
| P1-011 | Auth: login, refresh rotation, logout, argon2id | BE | 010 | A reused refresh token revokes its whole chain (BR-020–022) | done | Iqbal Hamdani |
| P1-012 | RBAC: seeded roles, `resource:action` check at the handler boundary | BE | 011 | `403` names the required permission in `detail` (BR-023, BR-024). Shipped with five roles; `P1-017` makes it four | done | Iqbal Hamdani |
| P1-013 | Error envelope (RFC 9457), `trace_id`, OpenTelemetry wiring | BE | 007 | Every error carries a `trace_id` traceable to a span (BR-011) | done | Iqbal Hamdani |
| P1-014 | App shell, routing, auth screens, session handling | FE | 005, 011 | Access token in memory, refresh in an httpOnly cookie (BR-022) | done | Iqbal Hamdani |
| P1-015 | Admin rate limits, `RateLimit-*` headers | BE | 012 | Over 100 req/min/user → `429 rate_limited` with `Retry-After`; headers on every response; per-process fallback when Redis is down (BR-014). Storefront limits are `P1-209` | review | Iqbal Hamdani |
| P1-016 | Log field allow-list | BE | 013 | Fields not on the allow-list are redacted on every log line; `Authorization`, `X-Api-Key`, `X-Order-Token` and cookies never appear (BR-013) | review | Iqbal Hamdani |
| P1-017 | Four roles: drop `warehouse`, v2 permission matrix | BE/FE | 012 | Migration removes `warehouse` from the `users.role` CHECK and fails loudly if any user holds it; seeded permissions equal §3; `GET /v1/roles` returns four; generated clients pick up the enum from contracts `v2.0.0` (BR-023) | review | Iqbal Hamdani |
| P1-018 | `audit_log` table and recorder | BE | 007 | As `03-erd.md` §3.2. Every mutating admin route writes exactly one row in the same transaction, none if it rolls back; asserted over every registered mutating route (BR-018) | review | Iqbal Hamdani |
| P1-019 | `api_keys` v2 shape and `resolve_api_key` | BE | 010 | As `03-erd.md` §3.2: `allowed_origin` (one URL) added, `permissions` and `key_prefix` dropped, no `kind`; `resolve_api_key` is `SECURITY DEFINER` and returns only its four columns (BR-003, BR-028) | review | Iqbal Hamdani |
| P1-081 | Time in WIB everywhere | BE | 007, 013 | The pool sets `TimeZone = 'Asia/Jakarta'` on every connection; every JSON timestamp ends in `+07:00`; a timestamp sent without an offset → `422`; a test asserts no response contains a `Z` timestamp (BR-007) | review | Iqbal Hamdani |
| P1-082 | Drop `tenants.currency` | BE/FE | 010, 011 | Migration drops the column; the login query, `Session.tenant` and settings no longer carry `currency`; money columns still say `IDR` while the wire is a plain integer; the web app's generated types and session fixture updated (BR-006, BR-029) | review | Iqbal Hamdani |

---

## Phase 1 · Catalog (weeks 5–10)

M2 complete: products, variants, matrix editor, brands, categories, bulk edit, CSV import, R2 media
and the image domain. Team management and settings land here too, because the onboarding wizard
needs them.

**Exit:** a 10,000-variant CSV imports in under 5 minutes.

### Catalog core

| ID | Item | Repo | Depends | Acceptance | Status | Owner |
|---|---|---|---|---|---|---|
| P1-020 | `brands` schema + composite FK to tenant | BE | 010 | As `03-erd.md` §3.3; unique slug per tenant including archived (BR-004, BR-030) | review | Iqbal Hamdani |
| P1-021 | Brand CRUD API | BE | 020 | As §6.1; no `version`, no `If-Match` (BR-010, BR-012, BR-030) | review | Iqbal Hamdani |
| P1-022 | `categories` schema, ltree, slugify, path trigger | BE | 010 | As `03-erd.md` §3.7; a move rewrites every descendant path in one statement (BR-032) | review | Iqbal Hamdani |
| P1-023 | Category cycle guard + same-name sibling handling | BE | 022 | Moving a node beneath its own descendant errors; siblings named alike get `_1` labels (BR-034, BR-035) | review | Iqbal Hamdani |
| P1-024 | Category API, `kind` filter, depth-limited fetch | BE | 022 | As §6.2; sending `path` → `422`; deleting a category with children → `409 category_in_use` with the count (BR-008, BR-036) | review | Iqbal Hamdani |
| P1-025 | `products` schema incl. `slug`, `attributes`, `option_names` | BE | 020 | As `03-erd.md` §3.3; no quantity column anywhere (BR-017, BR-042) | review | Iqbal Hamdani |
| P1-026 | `variants` schema, partial unique SKU index, composite FK | BE | 025 | Many null SKUs allowed; non-null unique per tenant; one live variant per option combination; `variant_price()` returns the sale price only inside its schedule, and a sale price not below the regular price is refused (BR-039, BR-040, BR-046) | review | Iqbal Hamdani |
| P1-027 | `product_categories` join, multi-`kind` membership | BE | 022, 025 | One product in 3 trees of different kinds at once; a cross-tenant link is refused (BR-004, BR-031) | review | Iqbal Hamdani |
| P1-028 | Product CRUD, `If-Match`, slug, server-managed fields refused | BE | 025, 027 | As §7.1; changing the title never changes the slug (BR-008, BR-009, BR-010, BR-012, BR-042) | review | Iqbal Hamdani |
| P1-029 | Variant CRUD | BE | 026 | As §7.2; a duplicate SKU → `409 duplicate_sku` naming the holder; `regular_price`, `sale_price` and schedule writable, `price`/`on_sale` read-only (BR-039, BR-046) | review | Iqbal Hamdani |
| P1-030 | Product list: trigram search, filters, cursor pagination | BE | 028 | p95 < 600 ms with 10k products, categories included; each row lists its main-tree (`kind = category`) categories; `category_id` includes descendants; exact SKU matches | review | Iqbal Hamdani |
| P1-060 | Job runner (Redis Streams) + `jobs` table + `GET /v1/jobs/{id}` | BE | 007 | As §9 and `03-erd.md` §3.2; a job killed mid-run is redelivered and finishes once (BR-060, BR-063) | review | Iqbal Hamdani |
| P1-040 | `PUT /variant-matrix`: server-side diff, one transaction | BE | 029 | As §7.3: created, updated, restored and archived counted correctly (BR-040, BR-041) | review | Iqbal Hamdani |
| P1-041 | Partial-failure semantics in the matrix | BE | 040 | One duplicate SKU fails only its row; the rest save; 100 cells save in < 2 s (BR-041) | review | Iqbal Hamdani |
| P1-042 | `product_media` schema | BE | 025 | As `03-erd.md` §3.3 (BR-004, BR-050) | review | Iqbal Hamdani |
| P1-043 | Media API: presign, confirm with `HEAD` check, attach, reorder, delete | BE | 042 | As §8; a key never uploaded → `422`; keys carry the content hash (BR-051, BR-053) | review | Iqbal Hamdani |
| P1-044 | Worker: WebP derivatives 1600/800/200 via libvips | BE | 043, 060 | Derivatives ready in < 15 s p95; uploading never blocks the form (BR-052) | review | Iqbal Hamdani |
| P1-045 | R2 bucket, tenant prefixes, lifecycle rules, image domain Worker | OPS | 042 | Product images served public and edge-cached from the image domain (a dev domain until `P1-001`); `errors.csv` deleted after 30 days, exports after 7 (BR-053) | blocked | |
| P1-049 | Publish check on `draft → active` | BE | 040, 043 | `422 publish_check_failed` lists every failure, including zero weight, with its variant id (BR-038) | review | Iqbal Hamdani |
| P1-072 | `POST /v1/products/bulk` | BE | 029, 049 | As §7.5: up to 500 rows, per-row results by index, one bad row rolls back nothing (BR-043) | review | Iqbal Hamdani |
| P1-073 | CSV import job: server-side parse, column mapping, `errors.csv` | BE | 060, 072 | 10,000 variants in < 5 min; `,` and `;` delimiters and BOM handled; every error cites its original line number (BR-044) | review | Iqbal Hamdani |
| P1-031 | Brand manager screen | FE | 021 | `01-product-requirements.md` §4 | review | Iqbal Hamdani |
| P1-032 | Category manager: tree, drag-to-move, confirmation dialog | FE | 024 | The move dialog states descendant and product counts (BR-033) | review | Iqbal Hamdani |
| P1-033 | Product list screen: search, filters, saved state | FE | 030 | A Category column shows each product's main-tree categories; filters survive navigation and reload | review | Iqbal Hamdani |
| P1-034 | Product editor: fields, slug, brand picker, category multi-select | FE | 028 | Unsaved-changes prompt; editing a slug warns that old links break (BR-042) | review | Iqbal Hamdani |
| P1-046 | **Variant matrix editor**: grid, paste from Excel, fill-down | FE | 040 | A 2×5 grid renders 10 cells and saves in one request; a failed row is highlighted with its error (BR-041) | review | Iqbal Hamdani |
| P1-047 | Bulk price adjustment in the matrix (± amount / %) | FE | 046 | Applies to regular or sale price, chosen by the user; a preview shows before it applies (BR-046) | review | Iqbal Hamdani |
| P1-048 | Media library: drag-drop, direct R2 upload, reorder, attach to variant | FE | 043 | Image bytes never pass through the API (BR-051) | review | Iqbal Hamdani |
| P1-075 | Publish flow in the editor | FE | 049, 046 | Every publish-check failure links to its field or matrix cell (BR-038) | review | Iqbal Hamdani |
| P1-076 | Product list bulk actions (status, price) via bulk upsert | FE | 033, 072 | Per-row failures shown inline; successes stay applied (BR-043) | review | Iqbal Hamdani |
| P1-074 | Bulk import wizard: upload, 50-row preview, column mapping, progress, error download | FE | 073 | The browser parses only the preview (BR-044) | review | Iqbal Hamdani |

> **`P1-045` is blocked** on a Cloudflare account. The local half is done: `make storage-init`
> gives MinIO the same layout, public read on `*/products/*`, and the two expiry rules. Cutover,
> for the account owner:
> 1. Create the R2 bucket and an API token with object read/write on it.
> 2. Add lifecycle rules: prefix `jobs/` delete after 30 days, prefix `exports/` after 7 (BR-053).
> 3. Serve `*/products/*` on the image domain through a Worker or R2 custom domain, edge-cached.
> 4. Allow CORS `PUT` from the admin origin.
> 5. Set `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, `S3_BUCKET`, `S3_USE_SSL=true` and
>    `IMAGE_BASE_URL`. No code changes.

### Team and settings

| ID | Item | Repo | Depends | Acceptance | Status | Owner |
|---|---|---|---|---|---|---|
| P1-225 | Resend: account, sending domain, DKIM/SPF/DMARC | OPS | — | A test email from `no-reply@{domain}` reaches an outside inbox and passes DKIM. Until the domain exists (`P1-001`), Resend's test sender (`onboarding@resend.dev`, which only delivers to the account owner's address) is enough to build against (BR-128) | blocked | |
| P1-226 | Email sender in the worker + invitation template | BE | 225, 060 | Emails go through Resend from `"{shop name}" <no-reply@{domain}>` with Reply-To; sent after commit; a rolled-back change sends nothing (BR-128) | review | Iqbal Hamdani |
| P1-064 | Users: invite, accept, resend, set role, disable | BE | 017, 018, 226 | As §2 and §4; the invitation email arrives (BR-026, BR-027) | review | Iqbal Hamdani |
| P1-071 | Settings API: `GET`/`PATCH /v1/settings` | BE | 017 | As §4; only `owner` can `PATCH` (BR-023, BR-029) | review | Iqbal Hamdani |
| P1-077 | Audit log API: `GET /v1/audit-log` | BE | 018 | As §4; newest first, filterable by subject and actor (BR-018) | review | Iqbal Hamdani |
| P1-066 | Team & roles screen | FE | 064 | `ops` sees no user-management navigation at all (BR-025) | review | Iqbal Hamdani |
| P1-068 | Onboarding wizard: settings, first brand, first category tree | FE | 021, 024, 071 | `01-product-requirements.md` §4; defaults pre-filled (BR-029) | review | Iqbal Hamdani |
| P1-078 | Audit log screen | FE | 077 | Shows actor, action, before/after per row (BR-018) | review | Iqbal Hamdani |
| P1-079 | Accept invitation screen | FE | 064 | The invite link opens a form for a password (the name was set at invite); submitting `POST /v1/auth/accept-invite` signs the user in. An expired or used token says so and tells them to ask for a new invitation (BR-026) | review | Iqbal Hamdani |
| P1-083 | Settings screen | FE | 071 | Name, time zone and order prefix, per `01-product-requirements.md` §4; only `owner` sees save; a changed prefix says it applies to new orders only (BR-025, BR-029, BR-077) | review | Iqbal Hamdani |

> **`P1-225` is blocked** on a Resend account and the sending domain (`P1-001`). The local half is
> done: in development the worker sends to Mailpit when `SMTP_ADDR` is set (inbox at
> http://localhost:8025), else prints mail to stdout; elsewhere it refuses to start without
> `RESEND_API_KEY`. The Resend sender (`P1-226`) is built and tested against Resend's API shape.
> Cutover, for the account owner:
> 1. Create the Resend account and add the sending domain.
> 2. Add its DKIM, SPF and DMARC records to DNS; wait for Resend to verify.
> 3. Set `RESEND_API_KEY` and `EMAIL_DOMAIN` on the worker. No code changes.
> 4. Send an invitation to an outside inbox and check it passes DKIM.

---

## Phase 2 · Orders (weeks 11–13)

M1: state machine, order list and detail, manual entry, customer views, export. Orders come before
the storefront because checkout creates orders through this state machine, and ops must be able
to work an order the day the first one arrives.

**Exit:** every transition is audited; illegal transitions are rejected.

| ID | Item | Repo | Depends | Acceptance | Status | Owner |
|---|---|---|---|---|---|---|
| P1-100 | Schema: `customers`, `order_sequences`, `orders`, `order_lines` | BE | 026, 018 | As `03-erd.md` §3.5–3.6 minus `orders.cart_id`, which arrives with `P1-204`; the shipped-needs-tracking and refund CHECKs refuse direct SQL; archiving a variant leaves its order lines intact (BR-004, BR-045, BR-072, BR-076, BR-077, BR-079) | review | Iqbal Hamdani |
| P1-101 | Order `Transition`: allow-list, row lock, stamps, audit | BE | 100 | Unit-tested over every from × to pair; a move to the current status is a no-op; exactly one audit row per change (BR-070, BR-071, BR-073) | review | Iqbal Hamdani |
| P1-102 | Status routes: mark-paid, process, ship, complete, cancel, refund | BE | 101 | As §5.3; illegal → `409 illegal_transition`; ship without tracking → `422`; refund only on a paid, cancelled order, once (BR-072, BR-074, BR-075) | review | Iqbal Hamdani |
| P1-103 | Order list API with saved-view filters | BE | 100 | As §5.1; `refund_owed=true` returns exactly the cancelled-paid-unrefunded orders; p95 first byte < 800 ms at 10k orders (BR-075) | review | Iqbal Hamdani |
| P1-104 | Order detail and `PATCH` while pending | BE | 101 | As §5.2; `PATCH` after `pending` → `422`; totals recomputed; `allowed_transitions` from the allow-list (BR-010, BR-079) | review | Iqbal Hamdani |
| P1-105 | Manual order entry `POST /v1/orders` | BE | 101 | As §5.4; prices from the catalog; a `unit_price` in the body → `422 unknown_field`; snapshots written (BR-076, BR-078) | review | Iqbal Hamdani |
| P1-106 | Customer list and detail API | BE | 100 | As §5.5; no credential field ever appears (BR-092) | review | Iqbal Hamdani |
| P1-107 | Order export job | BE | 060, 103 | As §5.6; one row per line; opens cleanly in Excel with Indonesian locale; link regenerates (BR-063, BR-064, BR-065) | review | Iqbal Hamdani |
| P1-108 | Order list screen with saved views | FE | 103 | The four saved views of `01-product-requirements.md` §6.1; mark-paid from the list | review | Iqbal Hamdani |
| P1-109 | Order detail screen: actions, ship dialog, refund record, audit trail | FE | 102, 104, 077 | Only `allowed_transitions` are offered; the ship dialog requires courier and tracking (BR-025, BR-072) | review | Iqbal Hamdani |
| P1-110 | Manual order entry screen | FE | 105 | Submit is disabled while the request is in flight (BR-078) | review | Iqbal Hamdani |
| P1-111 | Customer list and detail screens | FE | 106 | Detail shows the customer's orders, newest first | review | Iqbal Hamdani |
| P1-112 | Order export screen | FE | 107 | Uses the order-list filters; download link expires in 15 minutes and can be regenerated (BR-063) | review | Iqbal Hamdani |

---

## Phase 3 · Storefront API (weeks 14–19)

M3: API keys and origin allowlist, storefront views, catalog routes, carts, checkout, customer
accounts, order history, guest lookup, public docs. The open questions on payment, shipping cost,
email provider and customer sign-in are decided (`01-product-requirements.md` §9): Midtrans or
bank transfer, Biteship rates, Resend, email + password and Google.

**Exit:** the customer isolation suite is green; 20 parallel checkouts of one cart create one
order; a minimal reference website built from the public docs alone goes product → cart → checkout
→ order history.

| ID | Item | Repo | Depends | Acceptance | Status | Owner |
|---|---|---|---|---|---|---|
| P1-200 | API key API: create, list, edit origins, revoke | BE | 019, 018 | As §4; plaintext returned once; missing or malformed `allowed_origin` → `422`; it must be exact `scheme://host[:port]` (BR-028, BR-083) | review | Iqbal Hamdani |
| P1-201 | API keys screen | FE | 200 | Copy-once UI with an explicit warning; link to the storefront docs (BR-028) | review | Iqbal Hamdani |
| P1-202 | Storefront route tree + key middleware | BE | 019, 015 | As §11.2: `invalid_api_key`; browser request from a foreign origin → `origin_not_allowed` without ACAO; same key with no `Origin` accepted; preflight for any origin; revoked key dead within 60 s; storefront and admin packages do not import each other (BR-003, BR-083, BR-085) | review | Iqbal Hamdani |
| P1-203 | Storefront views + catalog routes | BE | 202, 030, 044 | As §11.4; handlers query only the views; draft and archived products never appear; p95 < 300 ms ; `price` is the effective price and `on_sale` is set while a sale is active (BR-046, BR-080, BR-081) | review | Iqbal Hamdani |
| P1-204 | `carts`, `cart_items`, `orders.cart_id`; cart routes | BE | 203, 100 | As §11.5 and `03-erd.md` §3.5; cart id is v4; prices never stored; `price` field → `422 unknown_field` (BR-005, BR-087, BR-089) | review | Iqbal Hamdani |
| P1-205 | **Checkout** | BE | 204, 101 | As §11.7 (bank transfer, before shipping and Midtrans land); 20 parallel checkouts of one cart → one order, all 20 responses carry it; replay → `200` same order; `item_unavailable` leaves the cart untouched; guest `order_token`; a sale that ends while items sit in the cart is charged at the regular price (BR-088, BR-089, BR-090, BR-091) | review | Iqbal Hamdani |
| P1-206 | Customer accounts: register, login, refresh rotation, logout | BE | 202, 100 | As §11.8; tokens in the body; reused refresh token revokes the session; tenant-B key with tenant-A token → `401`; customer token on any admin route → `401` (BR-021, BR-086, BR-092, BR-093) | review | Iqbal Hamdani |
| P1-207 | Password reset | BE | 206, 226 | As §11.8; link valid 30 min, single use, revokes every session; always `202`; a Google-only customer can add a password (BR-094) | review | Iqbal Hamdani |
| P1-208 | `me`, order history, guest order lookup | BE | 205, 206 | As §11.9; cart follows the shopper after sign-in; one order token never opens another order (BR-082, BR-091, BR-095) | review | Iqbal Hamdani |
| P1-227 | Envelope encryption helper (AES-256-GCM, key in KMS) | BE | 007 | Encrypt/decrypt round-trips; ciphertext never equals plaintext; a log-scan test finds no secret bytes. Reused by `P1-216` and `P1-301` (BR-101, BR-129) | review | Iqbal Hamdani |
| P1-216 | `storefront_settings` + `GET`/`PATCH /v1/storefront-settings` | BE | 071, 227 | As §4 and `03-erd.md` §3.5; server key write-only; enabling Midtrans without keys → `422`; both methods off → `422` (BR-122, BR-129) | review | Iqbal Hamdani |
| P1-217 | Storefront settings screen | FE | 216 | Server key field is write-only ("set" / "replace"); shows the notification URL to copy (BR-129) | review | Iqbal Hamdani |
| P1-224 | `GET /v1/storefront/config` | BE | 216, 202 | As §11.4; contains nothing secret (BR-122, BR-127, BR-129) | review | Iqbal Hamdani |
| P1-218 | Biteship rates: client, 10-minute cache, storefront and admin rate routes | BE | 216, 204 | As §11.6 and §5.4; one platform key from config; Biteship down → `502 shipping_rates_unavailable` (BR-120) | review | Iqbal Hamdani |
| P1-219 | Checkout shipping: required choice, server re-quote | BE | 205, 218 | Within the cache window the charged price equals the quoted price; a service no longer offered → `409 shipping_unavailable`; no order on Biteship failure (BR-089, BR-121) | review | Iqbal Hamdani |
| P1-220 | Midtrans at checkout: `payments` table, Snap transaction | BE | 219, 216 | As §11.7; amount equals order total; attempt ids `ERG-000123`, `-2`, …; Midtrans down → order kept, `payment_unavailable` (BR-122, BR-123) | review | Iqbal Hamdani |
| P1-221 | Midtrans notification webhook | BE | 220 | As §11.10; bad signature → `401`, nothing changes; status confirmed by Get Status API; `settlement` marks paid once; a replayed notification is a no-op; amount mismatch never marks paid (BR-003, BR-124, BR-125) | review | Iqbal Hamdani |
| P1-222 | Pay-again route | BE | 221, 208 | As §11.10; a live attempt's link is reused; an expired one gets a new attempt; paid or cancelled → `422` (BR-126) | review | Iqbal Hamdani |
| P1-223 | Google sign-in + `customer_identities` table | BE | 206, 216 | As §11.8; wrong `aud`, unverified email or bad signature → `401`; existing verified-email account is linked through a `customer_identities` row, not duplicated; the same Google account cannot link to two customers in one shop (BR-127) | review | Iqbal Hamdani |
| P1-228 | Order emails: placed, payment received, shipped | BE | 226, 205, 221, 102 | Each email is sent once per event, from the shop's name, with courier and tracking on "shipped" (BR-128) | review | Iqbal Hamdani |
| P1-229 | Order detail shows payment attempts and chosen shipping | FE | 220, 109 | Attempts newest first with Midtrans status; `mark-paid` on a Midtrans order warns it is an override (BR-074) | review | Iqbal Hamdani |
| P1-209 | Storefront rate limits | BE | 202 | Browser requests per IP and per key, server requests per key and IP, login, checkout, as BR-014; headers on every response | review | Iqbal Hamdani |
| P1-210 | Storefront auth matrix suite | BE | 208 | Table-driven over origin (allowed, foreign, absent) × token presence against every storefront route (BR-082, BR-083, BR-085, BR-086) | review | Iqbal Hamdani |
| P1-211 | **Customer isolation suite** | BE | 208 | Two tenants × two customers; zero leakage both ways on every customer-scoped route; extends `P1-008` to the storefront tree (BR-001, BR-082) | review | Iqbal Hamdani |
| P1-213 | Public storefront OpenAPI at `/v1/storefront/openapi.json` + docs page | CT/BE | 222, 223, 224 | Every §11.3 route documented; the file lives in contracts and is served by the API | review | Iqbal Hamdani |
| P1-214 | Nightly retention job | BE | 204, 206 | Expired open carts and expired or revoked sessions deleted; checked-out carts kept (BR-096) | review | Iqbal Hamdani |
| P1-215 | Customer PII purge job | CT/BE/FE | 206, 060 | Purges one customer's PII from `customers`, `customer_identities`, `orders.customer`, `orders.shipping_address`; catalog untouched; audited (BR-097). The owner starts it with "Delete personal data" on the customer's page (`POST /v1/customers/{id}/purge`, `customers:purge`) | review | Iqbal Hamdani |
| P1-212 | Reference website from the public docs alone | FE | 213, 222, 223 | A throwaway site outside `new-commerce-web`, using only the docs and an API key: product → cart → shipping choice → guest checkout paid with Midtrans sandbox → order lookup, and Google sign-in → order history | blocked | |

> **Phase 3 is delivered in three slices** (decided 11 Oct 2026): 3a is P1-200…P1-208, 3b is
> P1-227…P1-229 (encryption, storefront settings, shipping, Midtrans, Google, order emails), and 3c
> is P1-209…P1-212. Each slice is reviewed and merged before the next starts.
>
> Checkout started bank-transfer-only and unshipped (P1-205); P1-219 added the re-quoted
> `shipping_option` and P1-220 Midtrans.
>
> **Keys held in env until a KMS exists** (decided 11 Oct 2026): the guest order-token key
> (`ORDER_TOKEN_SECRET`, BR-091), the reset-link key (`PASSWORD_RESET_SECRET`, BR-094) and, with
> P1-227, the envelope-encryption key are env secrets, required outside development. Moving them
> into a managed KMS changes no contract.
>
> **Midtrans, Biteship and Google are built against fakes** of their APIs (decided 11 Oct 2026),
> and **`P1-212` is blocked** on the owner's accounts for all three. The code needs nothing more;
> cutover, for the account owner:
> 1. **Biteship** (P1-218): create the account, then set `BITESHIP_API_KEY` on the API (and
>    `BITESHIP_URL` only to point elsewhere). Without a key, development quotes a fixed Rp 10.000
>    per courier and production refuses to start. In Settings › Storefront set the origin postal
>    code and couriers, then quote a cart and check the price against Biteship's dashboard.
> 2. **Midtrans** (P1-220…P1-222): per shop, in Settings › Storefront, choose the sandbox, paste
>    the client and server keys from the Midtrans dashboard and turn Midtrans on. Set `API_URL` to
>    the API's public base so the notification URL resolves. Check out with Midtrans, pay with a
>    sandbox method, and confirm the order turns paid and the webhook's attempt says `settlement`.
> 3. **Google** (P1-223): per shop, create an OAuth client (Web) in Google Cloud with the website's
>    origin as an authorised JavaScript origin, and paste its client ID in Settings › Storefront.
> 4. Then build P1-212 against the sandbox keys.
>
> **Order emails** (P1-228) go to every order whose customer has an email, manual orders included
> (decided 11 Oct 2026), and need `RESEND_API_KEY` like every email (P1-225).
>
> **3c decisions** (12 Oct 2026):
> - **PII purge** (P1-215) is started by the owner from the admin: `customers:purge` and a
>   "Delete personal data" action.
> - **Retention** (P1-214) is a worker loop at 02:00 WIB, shop by shop as `app_user`, with no
>   `BYPASSRLS` role; BR-003 says so.
> - **The storefront docs** (P1-213) are served by the API, with no key, from the spec it was
>   generated from.

---

## Phase 4 · Shopee import (weeks 20–23)

M4 for Shopee: OAuth, item import, matching, fill-only merge, images to R2, error report. Depends
on Shopee partner approval (`P1-080`).

> **`P1-080` is parked** by the owner (6 Oct 2026): partner access for Shopee and Tokopedia is left
> for later. Unpark it before Phase 4 starts; nothing earlier depends on it.

**Exit:** re-importing a shop creates nothing new and overwrites nothing the owner edited.

| ID | Item | Repo | Depends | Acceptance | Status | Owner |
|---|---|---|---|---|---|---|
| P1-080 | Apply for Shopee and Tokopedia partner API access | OPS | — | Both applications submitted; dates and status noted under this table. A human task: `/next` reports it and stops | blocked | |
| P1-300 | `channels`, `channel_listings` schema | BE | 026 | As `03-erd.md` §3.4; a single-variant item cannot be linked twice (`NULLS NOT DISTINCT`) (BR-004, BR-103) | todo | |
| P1-301 | Channel credentials encrypted with the `P1-227` helper | BE | 300, 227 | Decrypted only in the worker; a log-scan test finds no credential bytes (BR-101) | todo | |
| P1-302 | Adapter interface + Shopee OAuth: connect, callback, disconnect | BE | 301 | As §10; `state` is signed and the only tenant source on callback; disconnect erases credentials and keeps links (BR-003, BR-101) | todo | |
| P1-303 | Shopee `ListItemIDs` / `GetItems` into `ExternalItem` | BE | 302 | Replayed from recorded fixtures; per-shop token bucket; 5 retries with full jitter (BR-107) | todo | |
| P1-304 | Import job: lock, match, create, fill-only merge | BE | 303, 060, 049 | Importing twice creates zero new rows; edited description, price, weight and SKU survive; second click returns the running job (BR-100, BR-103, BR-104, BR-105) | todo | |
| P1-305 | Imported images copied to R2 with derivatives | BE | 304, 044 | No product image URL points at a marketplace domain (BR-106) | todo | |
| P1-306 | Per-item results and `errors.csv` | BE | 304 | One failing item lands in the report; every other item imports (BR-107) | todo | |
| P1-307 | Token refresh failure → `reauth_required` | BE | 304 | The job fails with the reason and is not retried; reconnect restores `connected` (BR-102) | todo | |
| P1-308 | Listings API and unlink | BE | 300 | As §10; after unlink the next import re-matches by SKU or creates (BR-108) | todo | |
| P1-309 | Channels screens: list, connect, import dialog, result, listings | FE | 302, 306, 308 | Reconnect shown for `reauth_required`; error report downloadable (BR-102, BR-107) | todo | |

---

## Phase 5 · Tokopedia import (weeks 24–25)

M4 for Tokopedia on the same adapter interface. **Conditional:** if Tokopedia access is not
available by week 20, ship without it and keep the adapter slot open (`01-product-requirements.md`
§9).

**Exit:** a SKU-matched product appears once, linked to both marketplaces.

| ID | Item | Repo | Depends | Acceptance | Status | Owner |
|---|---|---|---|---|---|---|
| P1-400 | Tokopedia adapter: OAuth, list, get, rate limit | BE | 304 | Same six adapter methods; recorded fixtures; no change to the import path (BR-101, BR-107) | todo | |
| P1-401 | Cross-channel SKU match | BE | 400 | A product on Shopee and Tokopedia with matching seller SKUs is one product with one listing per channel per variant (BR-103) | todo | |

---

## Phase 6 · Production readiness, hardening & pilot (weeks 26–28)

**Production readiness is here on purpose.** The v2 plan puts provisioning in Phase 0. It stays at
the end, as in v1.6.1: there is still no server and no domain, so `P1-001`'s acceptance has nothing
to point at, and nothing before the pilot needs a server because the backend runs on the host
services from `P1-000`. It cannot move any later: `P1-070` puts a real shop on the machine, and that
must not happen on storage that has never been restored from.

**Exit:** the pilot website takes real orders for 2 weeks.

| ID | Item | Repo | Depends | Acceptance | Status | Owner |
|---|---|---|---|---|---|---|
| P1-001 | VPS provisioning, Docker Compose, Caddy, TLS | OPS | — | `docker compose up` serves HTTPS on the domain; two `cmd/api` replicas drain one at a time on deploy | todo | |
| P1-002 | PostgreSQL 18 + Redis 8 with tuned config and resource limits | OPS | 001 | `shared_buffers` ≈ 25% RAM, `max_connections` 100; `cpus`/`mem_limit` per service; Redis AOF `everysec` | todo | |
| P1-003 | **pgBackRest to R2 + restore drill** | OPS | 002 | **A restore from R2 into a clean machine succeeds.** RPO 5 min. Gates `P1-070` | todo | |
| P1-004 | CI: lint, test, migrations on a snapshot, contract drift check | BE/FE | 001 | A PR breaking any of the four is red | todo | |
| P1-500 | k6 load test | OPS | 001, 203, 205 | Storefront browsing at 10× expected peak with an import running stays within `01-product-requirements.md` §8.1 | todo | |
| P1-501 | Alerts and traces | OPS/BE | 001 | Every alert in `01-product-requirements.md` §8.5 fires in a drill; checkout and import jobs each appear as one trace | todo | |
| P1-502 | Runbooks: restore, deploy, key rotation, channel reconnect | OPS | 003 | Each runbook was followed once by someone who did not write it | todo | |
| P1-070 | **Pilot tenant live** | OPS | 003, 212 | A real shop's catalog is loaded and its website takes real orders for 2 weeks | todo | |

> **Phase 6 is delivered in three slices** (decided 13 Oct 2026), while there is still no domain,
> VPS, Cloudflare zone or R2 bucket: **6a** builds and runs it (P1-004, P1-001, P1-002), **6b**
> operates it (P1-003, P1-500, P1-501, P1-502), both proven with Docker Compose on a developer
> machine, MinIO standing in for R2 and a local Grafana stack for Grafana Cloud; **6c** cuts over
> to the real box and runs the pilot (P1-070). An item whose acceptance needs the real box is
> `blocked` after its local half, with its cutover steps here, as `P1-045` is.
>
> Also decided: the admin passes `/v1/*` to the API on its own host (`04-api-spec.md` §1);
> monitoring is Grafana Cloud's free tier through an Alloy agent; expected peak is 10 storefront
> requests a second, so k6 runs at 100 (`01-product-requirements.md` §8.1).
>
> **`P1-004` in practice:** the snapshot is main's schema with `dev-seed` data until production
> exists, then a restored production backup. Contract drift is red when the generated code does
> not match the pinned contract, or the pinned contracts commit is not on contracts `main`, so an
> api or web PR goes green only once its contracts PR has merged (re-run it).
>
> **`P1-001` needs a decision at cutover:** the production domain. Every document still says
> `{domain}`. Caddy's default ACME challenge also cannot reach a machine behind the Cloudflare
> proxy, so the TLS mode is part of this item; a Cloudflare Origin certificate is the
> recommendation, since DNS-01 needs a custom Caddy build with the Cloudflare module.

---

## Dropped in v2

| ID | Item | Why |
|---|---|---|
| P1-061 | Catalog export templates for 5 marketplaces | Marketplace CSV export retired (BR-061); v2 imports instead |
| P1-062 | Export reports incomplete channel mappings | Went with P1-061 (BR-062) |
| P1-063 | Export screen | Went with P1-061. Order export is `P1-112` |
| P1-065 | API keys with permission sets | Replaced by one storefront key with one allowed origin: `P1-019`, `P1-200` |
| P1-067 | API keys screen (v1) | Replaced by `P1-201` |
| P1-069 | "No stock yet" empty state | Stock is out of scope, not coming later (BR-017) |

---

## Definition of Done

An item is `done` when **all** of these hold. Not five of seven.

1. Its Acceptance is proven true, with a test wherever a test is possible.
2. It matches the contract. If it can't, the contract changes first, in its own commit.
3. Tenant isolation holds: a new route is covered by `P1-008` (admin) or `P1-211` (storefront).
4. Errors use the RFC 9457 envelope with `trace_id`.
5. No server-managed or unknown field is accepted from a client.
6. `make generate` changes nothing on a clean tree in both repos.
7. Reviewed by someone who didn't write it.

---

## Out of scope for v2

Recorded here because these will be proposed again and again, and the answer should be one link.

| Request | Answer |
|---|---|
| Stock, quantities, warehouses, sold-out | No (BR-017) |
| Marketplace order sync, stock or price push | No; import is products only, pulled (BR-100) |
| "Update from marketplace" on an edited product | Not by default (BR-104); a per-product action if owners ask |
| Courier booking, rates, labels | No; record courier and tracking (BR-072) |
| Returns, RMA | No |
| Outbound webhooks | No |
| `Idempotency-Key` on mutations | No; cart idempotency (BR-088) |
| Payment gateways other than Midtrans | Not scheduled (BR-074) |
| Courier booking, labels via Biteship | No; rates only (BR-120) |
| WhatsApp or OTP sign-in | No; email + password and Google (BR-092) |
| Multi-currency | No; IDR only (BR-029) |
| Promotions engine, discount codes | No |
| Per-channel pricing | No |
