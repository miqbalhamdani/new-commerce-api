# Ecommerce Backoffice v2 · Business Rules

**Every rule in the system, numbered.** Each rule is written once, here. `01-product-requirements.md`,
`03-erd.md`, `04-api-spec.md` and `05-backlog.md` cite rules by id (`BR-038`) and do not restate
them. Tests should cite the same ids.

A rule says what must be true, followed by a short note on why. The mechanism that enforces it
lives in the ERD or the API spec.

Ids are grouped by area and never reused: `001–019` platform · `020–029` auth & team ·
`030–049` catalog · `050–059` media · `060–069` jobs & export · `070–079` orders ·
`080–099` storefront · `100–119` marketplace import · `120–129` shipping, payments & email. A rule that no longer applies is marked
**retired** and keeps its number.

---

## 1. Platform

These hold everywhere. Changing one is a breaking change for both code repos.

### BR-001 The database enforces tenant isolation
Every tenant-owned table has `tenant_id`, `ENABLE` **and** `FORCE ROW LEVEL SECURITY`, and a
`tenant_isolation` policy. The app connects as `app_user`, which owns nothing, so RLS always
applies to it. CI fails if any table with a `tenant_id` column lacks an enabled policy.

*Why:* one forgotten `WHERE tenant_id = $1` is a cross-tenant leak, and code review cannot be
relied on to catch that class of bug. `FORCE` matters because without it the table owner, which
is the role migrations run as, bypasses its own policy.

**Limited-roles mode.** Some managed databases give the schema owner neither `CREATEROLE` nor
`BYPASSRLS`, so `app_user` (and BR-003's lookup role) cannot exist. A deployment may then opt in,
once, when the schema is first created, to a limited mode; it is never the default, and never
mixed with the roles above:

- The app connects as the schema owner. It is not a superuser, so `FORCE` still binds it and
  every table keeps its `tenant_isolation` policy: a forgotten tenant filter still reads nothing.
- Each tenant table also gets one `SELECT`-only policy that admits every row while the
  transaction-local setting `app.rls_bypass` is `on`. Only two things may set it: BR-003's four
  lookup functions, for their one query, and the backup's `pg_dump`. Application code never sets
  it. Writes never pass it.
- What is weaker: the app owns the tables, so it could switch RLS off or set the bypass itself.
  The database no longer contains a SQL injection; it still contains an ordinary query bug.

*Why limited mode exists:* it lets a shop run on a shared managed database rather than not at
all. Moving to a database with the real roles means re-creating the schema in the default mode,
not switching modes in place.

### BR-002 Tenant context is set per transaction and fails closed
The tenant is set with `set_config('app.tenant_id', …, true)` inside every transaction, never per
connection. A query with no tenant context returns `ErrNoTenantContext`, never an empty result.

*Why:* pgx pools connections, so a connection-level `SET` would follow the connection to the next
request's tenant. Without the error, a missing context looks like "no rows", which is safe but
very confusing to debug.

### BR-003 The tenant comes from a credential, never from the request
No header, query parameter or body field can select a tenant. Admin requests take it from the
staff token; storefront requests take it from the API key (BR-086). Exactly four reads cross
tenants on a request path, each through its own `SECURITY DEFINER` function that returns only
what it must:

- staff login: `id, tenant_id, password_hash, status, role`;
- staff token refresh: `id, tenant_id, user_id, expires_at, revoked_at` (the cookie carries no
  tenant, and taking one from the request is what this rule forbids);
- API key resolution: `id, tenant_id, allowed_origin, revoked_at`;
- Midtrans notification: `webhook_id` → `tenant_id`, encrypted server key, environment. The tenant
  is trusted only after the notification's signature verifies with that key (BR-124).

Each function belongs to a role that bypasses RLS and owns nothing else. In BR-001's limited-roles
mode there is no such role: the function belongs to the schema owner and turns `app.rls_bypass`
on for its one query, then back off. Its columns are fixed the same way.

Work that spans shops, such as the nightly retention sweep (BR-096), crosses no boundary in a
query and has no role of its own: it lists shop ids from `tenants`, which has no RLS, then does
each shop's work inside that shop's own tenant transaction as the application role, so RLS holds
as it does for a request. It is audit-logged per shop and never reachable from a request. A PII
purge (BR-097) is not cross-tenant at all: the owner asks for it, and the tenant comes from their
token.

*Why:* a tenant read from the request is a trivial cross-tenant escalation.

### BR-004 A copied `tenant_id` on a child table is held by a composite foreign key
When a child table carries a `tenant_id` that could be derived from its parent (`variants` →
`products`, `order_lines` → `orders`, `cart_items` → `carts`), a foreign key on
`(parent_id, tenant_id)` makes the two impossible to disagree.

*Why:* without the copy, the RLS policy has to be a correlated subquery on every row, including
the catalog joins behind every storefront page, and per-tenant uniqueness
(`UNIQUE (tenant_id, sku)`) cannot be expressed. Foreign-key checks bypass RLS, so a plain FK
would accept another tenant's parent.

### BR-005 Keys are UUID v7, generated by the app
No sequential integer id appears in any API. **One exception:** `carts.id` is a random UUID v4,
because it doubles as the cart's bearer token (BR-087) and the leading bits of a v7 are a
timestamp.

*Why:* time-ordered UUIDs keep B-tree inserts at the end of the index; sequential ids leak
business volume.

### BR-006 Money is an integer in minor units
- **In the database:** `bigint` minor units plus a `char(3)` currency column, which is always
  `IDR` (BR-029).
- **On the wire:** a plain integer in minor units, no currency field: `"total": 39800000` is
  Rp 398.000. Every amount in every request and response has this one shape.
- IDR has no cents in practice, but the scale of 2 is kept: clients divide by 100 to display.
- Midtrans and Biteship speak whole rupiah: the API divides by 100 when sending to them and
  multiplies by 100 when reading from them (BR-120, BR-123).

*Why:* floats lose cents. One integer shape everywhere means no client ever has to handle two
money formats. The currency column and the scale stay in the database so that adding a currency
later is a feature, not a migration.

### BR-007 Time is WIB (UTC+7) on the wire and in the database
- **Column type:** always `timestamptz`, never `timestamp`. It stores an exact instant, so no value
  is ever ambiguous.
- **Database:** every connection sets `TimeZone = 'Asia/Jakarta'`, so `now()`, query output,
  `date_trunc` and date boundaries are WIB.
- **Wire:** RFC 3339 with the `+07:00` offset: `2026-10-06T16:15:00+07:00`, never `Z`.
- **Input:** any timestamp a client sends must carry an offset. Any offset is accepted and stored
  as the same instant; a timestamp without one is `422`.
- **Dates:** a date-only filter (`2026-10-01`) means midnight WIB.

A shop whose own `timezone` is not WIB (Makassar, Jayapura) still gets WIB from the API; the admin
converts to the shop's zone only for display.

*Why WIB:* the owners, shoppers and staff are in Indonesia. Reading `+07:00` in a response, a log
or a database session matches their clock with no mental arithmetic. `timestamptz` keeps the
instant exact, so the choice of zone is presentation only.

### BR-008 Server-managed fields are never accepted
`id`, `tenant_id`, `version`, `created_at`, `updated_at`, `path`, any `*_at` audit timestamp, and
derived values are set by the server. A client that sends one gets `422 validation_failed`
naming the field, **on create and on update alike**. Slugs are the exception: brand and product
slugs and a category's `label` default to a derived value and are editable (BR-030, BR-032,
BR-042).

*Why:* silently ignoring a field teaches the client that sending it worked. One rule for both
verbs means there is nothing to remember.

### BR-009 Omitting a field is not the same as sending `null`
- **Create:** an omitted field takes its default; `null` is `422`.
- **Update (`PATCH`):** an omitted field is unchanged. `null` clears a field marked nullable in
  the ERD, and is `422` for any other field.

*Why:* without this, "use the default" and "I want it empty" mean the same thing and the server
has to guess.

### BR-010 Concurrent edits are caught, not overwritten
Products, variants and orders carry `version`. Every `PATCH` to them, and `PUT` on the variant
matrix, requires `If-Match: <version>`; a stale version returns `409 version_conflict`. `version`
travels only in the header, never in the body.

Settings, users, media, API keys, brands and categories have no `version`, and their `PATCH` takes
no `If-Match`: the last save wins. Edits there are rare, small (a name, a move in a tree the
database keeps consistent on its own, BR-032) and made by an owner or admin, so a lost edit is
cheap to notice and redo.

### BR-011 Errors have one shape and are traceable
Every error is RFC 9457 `application/problem+json` and carries `trace_id`, the OpenTelemetry trace
id. A row belonging to another tenant, or another customer, returns `404`, exactly like a row that
does not exist.

*Why:* a support ticket quoting `trace_id` goes straight to the span. A different answer for
someone else's row would reveal which ids exist.

### BR-012 Deleting catalog data means archiving
`DELETE` on a brand, category, product or variant sets `archived_at`; the row stays. Deleting a
brand also clears `brand_id` on every product that carries it, bumping each product's `version`;
the admin shows a deleted brand nowhere (BR-030). Deleting a category removes it from every
product it is assigned to (the `product_categories` rows go), bumping each product's `version`
(BR-036). Users are
disabled (BR-027), API keys are revoked (BR-028). Media is the only thing truly deleted. Orders
are never deleted (BR-079).

*Why:* order lines and channel listings reference catalog rows; a hard delete would leave them
dangling.

### BR-013 Logs redact by default
Structured logging uses an **allow-list** of fields: a new field is redacted until someone allows
it. `Authorization`, `X-Api-Key`, `X-Order-Token`, cookies, passwords and channel credentials are
never logged, allow-list or not, and neither are Midtrans server keys or Google ID tokens.

*Why:* v2 holds shopper PII (names, emails, phones, addresses). A new field should be redacted by
default rather than leaked by default.

### BR-014 Rate limits
| Caller | Limit | Where |
|---|---|---|
| Admin session (staff JWT) | 100 req/min per user | Redis sliding window |
| API key, browser request (has `Origin`) | 120 req/min per IP, 3,000 req/min per key | Cloudflare edge + Redis |
| API key, server request (no `Origin`) | 600 req/min per key per IP, burst 100 | Redis sliding window |
| Login and password reset (staff and customer) | 10 per 15 min per email, 30 per 15 min per IP | Redis |
| Checkout | 10 per min per IP | Redis |
| Background jobs | 10 concurrent per tenant, 1 import per channel (BR-100) | Queue depth check + lock |

Every response carries `RateLimit-Limit`, `RateLimit-Remaining` and `RateLimit-Reset`. Going over
returns `429 rate_limited` with `Retry-After`. If Redis is down, limits fall back to per-process
limits.

*Why:* per-IP limits are deliberately loose. Indonesian mobile carriers put many shoppers behind
one address (carrier-grade NAT), so a tight per-IP limit would throttle real customers.

### BR-015 *Retired in v2*
Was "no stock, no orders, no channels in Phase 1". v2 has orders (§6) and marketplace import
(§8). No stock is now BR-017.

### BR-016 Language: English
Everything the web app shows is English: UI copy, email (invitations, password reset, order
confirmations) and API error `title`/`detail`. Code, identifiers, `openapi.yaml` and these
contract documents are English too.

*Why:* one language across product, code and docs means no text, error or generated type ever
needs translating between layers.

### BR-017 No stock: visible means orderable
Stock is not tracked in any form: no quantities, locations, reservations or sold-out states.
A variant can be ordered whenever it is visible on the storefront (BR-080). When an owner no
longer has an item, they archive the variant, or contact the customer after the order arrives.

*Why:* at 2–5 orders a day per tenant, a stock ledger costs far more to build and keep correct
than the occasional "sorry, that one's gone" message it prevents. This is a deliberate scope
decision, not an omission. If stock returns, it returns as a ledger of
*(variant, location)*, never as a `qty` column on `variants`.

### BR-018 Every admin mutation is audited
Every admin write records one `audit_log` row with actor, action, subject, before/after and IP,
in the same transaction as the change.

*Why:* "who changed this price?" and "who cancelled this order?" are questions ops will ask, and
they can only be answered if the record is written at the time.

---

## 2. Auth & team

### BR-020 Staff email is unique system-wide and determines the tenant
A user row belongs to exactly one tenant, and its email is unique across all tenants. Staff login
takes only email and password; the tenant is read from the user row.

*Why:* with the same email in two tenants, nothing in the login request could tell them apart.
The cost is accepted: an agency running two shops needs two email addresses. Shopper accounts are
different: they are per tenant (BR-092).

### BR-021 Every failed login looks the same
Wrong password, unknown email and disabled account all return the same `401`. This applies to
staff and customer login alike.

*Why:* any difference tells an attacker which emails exist.

### BR-022 Staff sessions are short and refresh rotates
- The access token is a 15-minute JWT with `aud = admin`, held in memory only, never in
  `localStorage`.
- The refresh token lives in an `httpOnly`, `Secure`, `SameSite=Lax` cookie and is never in a
  response body.
- Every refresh issues a new refresh token and revokes the old one. **A reused refresh token means
  theft:** its whole rotation chain is revoked and the user is signed out everywhere.
- Passwords are hashed with `argon2id` (64 MB memory, 3 iterations).

*Why:* a short access token keeps the tenant context fresh, and a cookie no script can read keeps
the long-lived token out of reach.

### BR-023 Four fixed roles; permissions come only from the role
The roles are `owner`, `admin`, `ops` and `viewer`, seeded and identical for every tenant. There
are no custom roles or per-user overrides yet; permissions are a flat string set, so adding a
custom role later is a data change, not a deploy. A permission is `resource:action` with two
actions, `read` and `write` (deleting is archiving, BR-012, so it counts as writing). Only an
owner can grant `owner`. The full matrix is `04-api-spec.md` §3.

| Role | Intent |
|---|---|
| `owner` | Everything, including tenant settings and billing. |
| `admin` | Everything except tenant settings and billing. |
| `ops` | Orders and customers. Read-only on the catalog. No users, API keys or channels. |
| `viewer` | Read-only. For accountants and external bookkeepers. |

*Why four, not five:* v1 seeded `warehouse` for a stock module that v2 removed (BR-017).

### BR-024 `403` names the missing permission
`403 permission_denied` puts the required permission in `detail` (`requires users:write`).

*Why:* the caller can tell "you can't do this" from "ask your owner for X".

### BR-025 What you can't do isn't shown
A screen or action the user isn't allowed is **absent**, not disabled. An `ops` user does not see
Team, API keys, Channels or Settings in the navigation; a `viewer` sees no save button anywhere.

*Why:* a disabled control advertises a capability and generates support tickets.

### BR-026 Invitations
An owner or admin invites with email, name and role. The user is `invited`, with no password,
until they accept, set a password and become `active`. An invitation link expires after
**7 days** and can be resent while the user is still `invited`. Once the user is `active`, every
link ever sent to them stops working.

### BR-027 Disabling a user
Users are disabled, never deleted: `created_by` and `audit_log.actor_id` elsewhere must stay
explainable. Disabling revokes the user's refresh tokens, so they are out within 15 minutes at
worst, when the access token expires, and immediately if the revocation list is hit. The last
active owner of a tenant cannot be disabled or demoted.

*Why:* without the owner guard, a tenant could lock itself out of its own settings.

### BR-028 API keys
- **One kind of key** (`sf_live_…`). The same key works from the shop's website in a browser and
  from the website's own server (BR-083). It reaches only storefront routes and never
  authenticates admin routes.
- A key has exactly one allowed origin: the website's URL, `https://tokoabc.com`.
- Keys are stored as SHA-256 hashes, and the plaintext is shown **exactly once**, at creation.
  Besides the hash, only the key's last four characters are kept, so a list shows
  `sf_live_••••42ab` next to its name and allowed origin; four characters of a 256-bit random
  part give nothing away.
- A key's name and allowed origin can be edited. Keys are revoked, never deleted.
- A website served from two origins (`tokoabc.com` and `www.tokoabc.com`), or a staging site,
  gets one key per origin. Each is revoked on its own.

*Why one kind:* the key can only reach public data, carts and checkout; personal data always
needs a customer or order token (BR-082). Two kinds (one for browsers, one for servers) would
make every owner work out how their website is built before creating a key, for almost no gain in
protection. A server-only key comes back if a server-only capability ever does.

*Why read-once:* a key that could be read back is a key that leaks from a screenshot of the
admin.

### BR-029 Tenant defaults
A new tenant defaults to time zone `Asia/Jakarta` without anyone choosing. Only the owner can
change tenant settings.

**IDR only.** Every amount is IDR and `currency` cannot be changed; there is no multi-currency and
no cross-border selling. Tenants have no currency setting. The `currency` on money columns and
the scale of 2 stay in the database (BR-006) so that adding a
currency later is a feature, not a migration.

---

## 3. Catalog

### BR-030 Brands
A brand's `slug` defaults to `slugify(name)` and can be set by the client on create or update.
A rename that does not send `slug` re-derives it from the new name. Slugs are lower-case a-z0-9
joined by single hyphens and unique per tenant, deleted (archived) brands included; a clash is
`422` on `slug` when the client sent one, else on `name`. A brand on a product is optional, and
deleting a brand clears it from its products (BR-012). Marketplace import matches brands by name
and creates a missing one (BR-103).

### BR-031 Each category kind is an independent tree
`kind` is one of `category`, `series`, `collection`, `activity` or `custom`, and each kind is its
own tree. A product can sit in categories from several trees at once: a jacket can be in
`apparel.outerwear.jackets`, `hiking` and `ss26`.

*Why:* adding this later means re-tagging the whole catalog by hand.

### BR-032 The database derives category paths
`path` (an `ltree`) is computed by a trigger from `parent_id` and the category's own segment:
its `label` when set, else `slugify_label(name)`. Clients never send `path` (BR-008), but may set
`label` on create or update: lower-case a-z0-9 joined by single underscores. `null` on update
goes back to the derived segment. A label clashing with a sibling's, archived ones included, is
`422` on `label`; derived segments are disambiguated instead (BR-035). Renaming, relabelling or
moving a category rewrites every descendant's `path` in the same statement.

*Why:* the hard case is the move. If application code had to remember to rewrite descendants, one
write path that forgot would leave a silently broken tree.

### BR-033 Moving or renaming a category never touches products
Product assignments reference the category id, never its path. The move confirmation says so,
with counts: "Move Jackets and its 4 subcategories? 128 products keep their assignments."

*Why:* users assume a move re-tags products and avoid the feature. Saying it explicitly gets it
used.

### BR-034 No category cycles
A category cannot be moved beneath its own descendant. The client blocks it and the database
refuses it if forced.

### BR-035 Category names may repeat
Two "Jackets" under different parents are fine. Siblings with the same name are allowed too; the
database disambiguates their path labels (`jackets`, `jackets_1`).

### BR-036 A category with subcategories cannot be deleted
Deleting a category that has live children is rejected with `409 category_in_use`, and the
response says how many children are in the way. Assigned products do not block it: deleting
removes the category from them (BR-012).

### BR-037 Product lifecycle
`draft` → `active` → `archived`. New products start as `draft`. Only `draft → active` is gated
(BR-038). Only `active`, unarchived products are visible on the storefront (BR-080).

### BR-038 Publish check
Moving a product from `draft` to `active` requires:
0. at least one unarchived variant (a product with none cannot be sold);
1. every unarchived variant has a SKU;
2. every unarchived variant has a regular price greater than zero (BR-046);
3. every unarchived variant has a weight greater than zero (shipping rates need it, BR-120);
4. at least one image;
5. at least one category of kind `category`.

On failure the product stays `draft` and the response lists every failure, so the client can link
to the offending cell (`422 publish_check_failed`). This is a check on the publish path, **not** a
table constraint.

*Why:* drafts must be quick to create. But a variant without a SKU cannot be bulk-upserted,
CSV-imported or matched by a marketplace import (BR-043, BR-044, BR-103), and a shopper must never
see a zero price, so the publish path is where those are caught.

### BR-039 SKU is optional while drafting and unique once set
Any number of variants may lack a SKU. A SKU that is set is unique within the tenant, across all
products. A clash returns `409 duplicate_sku` naming the product that holds it.

### BR-040 Option axes are positional
A product's `option_names` is an ordered list (`["Colour","Size"]`), and each variant's
`option_values` follows the same positions (`["Black","S"]`). **If a product has a Colour axis, it
is at position 0.** Products with no Colour axis are fine. No two live variants of one product
have the same `option_values`.

*Why:* positions make the matrix editor cheap: one query returns every variant with its values,
and the client pivots it into a grid. A join table would make every grid render a three-way join.

### BR-041 The variant matrix saves in one request; one bad row fails alone
The matrix editor sends the whole intended grid in one request, and the server works out what to
create, update and archive. A failing row (a duplicate SKU, say) fails **alone**: the other rows
still save, and the response reports a result for every row. A row that matches an archived
variant restores it, so a colourway removed and added back keeps its SKU.

*Why:* without this, the client fires dozens of separate requests with no transaction, and a
partial failure leaves a grid neither the user nor the system understands.

### BR-042 Product slugs
A product's `slug` is the URL handle on the owner's website (`/products/erigo-basic-tee`). It is
derived from the title on create, editable afterwards, and unique per tenant, archived products
included. It does **not** change when the title changes.

*Why:* a slug that followed the title would break every link and search result for the product
the first time someone fixed a typo.

### BR-043 Bulk upsert is keyed on SKU and partially succeeds
`POST /v1/products/bulk` takes up to 500 structured rows. A row with a SKU updates the variant
holding it (`on_conflict: "update"`) or is reported as a conflict (`"error"`); a row with no SKU
is a create. One bad row does not roll back the others; results are indexed by request position.

### BR-044 CSV files are parsed on the server
A product CSV goes browser → R2 (presigned PUT) → worker. The browser parses only the first ~50
rows to preview and map columns; that preview is display-only. The worker streams the file,
validates and upserts in batches of 500, and writes `errors.csv` citing the **original line
number** and reason for every failed row.

*Why:* client-side parsing means two parsers that must agree on delimiter, encoding, BOM and
Excel's locale quirks (`;` separators and `,` decimals in an Indonesian export), 10–20 MB of JSON
over a mobile connection, and lost line numbers.

### BR-046 Sale prices (WooCommerce-style)
Each variant has a **regular price** and an optional **sale price**, with an optional schedule
(`sale_starts_at`, `sale_ends_at`).

- **What the shopper pays** is the sale price while a sale is active, otherwise the regular price.
  A sale is active when the sale price is set and the current time is inside its schedule; an
  empty start or end means "open" on that side.
- That decision lives in **one database function, `variant_price()`**. Carts, checkout, manual
  orders, shipping item values and Midtrans amounts all go through it; no code reads the two
  columns to work out a price on its own.
- A sale price is always **lower** than the regular price, else `422`. A schedule's end is after
  its start.
- A scheduled sale starts and ends by itself: the price is worked out at the moment it is read,
  with no job to run. The price is frozen per line at checkout (BR-076), so a sale ending after
  checkout changes nothing on that order.
- When a sale is active, the storefront returns both prices and `on_sale: true`, so the website can
  show the regular price crossed out.
- Removing a sale is setting the sale price to `null`.

*Why one function:* the risk of a two-price model is one forgotten place that charges the regular
price to a shopper who saw the sale price. With one function, there is no second place to forget.

### BR-045 Archiving never touches order history
Archiving a product or variant removes it from the storefront and from new carts and checkouts,
and changes nothing on existing orders. Order lines keep their own snapshot (BR-076).

---

## 4. Media

### BR-050 Store object keys, never URLs
The database stores R2 object keys. URLs are built when someone reads the media.

*Why:* URLs expire or change domain; keys do not.

### BR-051 Uploads go straight to R2, then are confirmed
The browser uploads directly to R2 with a presigned `PUT`; image bytes never transit the API.
Accepted types are JPEG, PNG and WebP, up to 20 MB each. An upload exists only once the client
confirms it, and confirmation checks content type and size against R2's `HEAD`. A key cannot be
registered for an object that was never uploaded.

### BR-052 Image derivatives are made asynchronously
The worker makes 1600, 800 and 200 px WebP derivatives (libvips). Derivatives are ready within
15 s at p95. Uploading never blocks the product form. A derivative is never wider than its
original, which is not enlarged; the job also records the original's width and height.

### BR-053 Bucket layout, access and link lifetimes
One bucket, every object keyed by its tenant. Product images start with the tenant; temporary
files start with their kind (`jobs/`, `exports/`) and then the tenant, because R2 lifecycle rules
match a key's start only, and one rule must cover every tenant.

| Prefix | Contents | Access |
|---|---|---|
| `{tenant}/products/{product}/{hash}.{ext}`, `…/{hash}_{1600,800,200}.webp` | Product images: the original plus three `.webp` derivatives | **Public** through the image domain, edge-cached |
| `jobs/{tenant}/{job}/upload.csv` | Uploaded product CSV | Presigned `PUT`, 10 min; read by the worker only; deleted after 30 days |
| `jobs/{tenant}/{job}/errors.csv` | Error reports for CSV and marketplace imports | Presigned `GET`, 15 min; deleted after 30 days |
| `exports/{tenant}/{job}.csv` | Order exports | Presigned `GET`, 15 min; deleted after 7 days |

Product images are the one public class: the storefront API returns full image-domain URLs so the
owner's website renders them with a plain `<img>`. Keys contain a content hash, so a replaced
image gets a new URL and an edge cache never serves a stale one. Lifetimes are enforced by two R2
lifecycle rules: `jobs/` expires after 30 days, `exports/` after 7.

---

## 5. Jobs & export

### BR-060 Long work runs as a job
CSV import, order export, marketplace import and image derivatives run asynchronously: the request
returns `202 {job_id}` and the client polls `GET /v1/jobs/{id}`. Jobs are at-least-once, so every
job handler is safe to run twice.

### BR-061 *Retired in v2*
Was "catalog export templates follow each marketplace's bulk-upload sheet". v2 imports from
marketplaces instead of exporting to them (§8).

### BR-062 *Retired in v2*
Was "missing marketplace mappings never block an export". It went with BR-061.

### BR-063 Download links expire in 15 minutes and can be regenerated
Every `GET` on a finished job signs a fresh 15-minute `download_url`.

### BR-064 CSVs open cleanly in Excel with Indonesian locale settings
Amounts are written so a comma-decimal locale does not corrupt them, and the file has a UTF-8 BOM.

### BR-065 Order export
Order export takes the same filters as the order list (`04-api-spec.md` §5.1) and produces one row
per order line, with order fields repeated. It is the accounting integration for v1.

---

## 6. Orders

### BR-070 Order statuses and the allow-list
`pending → paid → processing → shipped → completed`, plus `cancelled` from `pending`, `paid` or
`processing`. No other move exists:

| From | Allowed to |
|---|---|
| `pending` | `paid`, `cancelled` |
| `paid` | `processing`, `cancelled` |
| `processing` | `shipped`, `cancelled` |
| `shipped` | `completed` |
| `completed`, `cancelled` | nothing |

A move not in the table returns `409 illegal_transition` and changes nothing.

### BR-071 One Transition function changes status
Status changes only through one function that locks the order row (`FOR UPDATE`), checks the
allow-list, stamps the matching timestamp (`paid_at`, `shipped_at`, `completed_at`,
`cancelled_at`), bumps `version` and writes the audit row. There is no bare
`UPDATE orders SET status` anywhere. Moving to the status the order already has is a **no-op that
succeeds**.

*Why:* the no-op makes a repeated click, or a repeated payment-gateway callback later, harmless.
The row lock makes two operators clicking Cancel and Ship at once serialise instead of interleave.

### BR-072 Shipping needs a courier and a tracking number
`processing → shipped` requires `courier` and `tracking_number`, else `422`. A database `CHECK`
refuses a `shipped` or `completed` order without them even if the handler check is bypassed.
Shipping is recorded, not booked: there is no courier integration (see out of scope).

### BR-073 Every status change is audited once
Every transition writes exactly one `audit_log` row with actor, from and to (BR-018).

### BR-074 Two ways to pay
- **Bank transfer:** an operator checks the transfer and marks the order paid.
- **Midtrans:** a verified Midtrans notification marks it paid (BR-124, BR-125).

Both call the same Transition (BR-071), so a payment confirmed twice, by an operator and by
Midtrans or by a repeated notification, is harmless. Which methods a shop offers is its own
setting (BR-122).

### BR-075 Refunds happen outside the system and are recorded
Cancelling a paid order moves no money. The operator refunds the customer (by bank transfer, or in
the Midtrans dashboard for a Midtrans payment) and records it, which sets `refunded_at`. Cancelled orders with `paid_at` set and `refunded_at` empty form the
"Cancelled, refund owed" saved view, so none is forgotten. A refund can only be recorded on a
cancelled order that was paid, and only once.

### BR-076 Orders are snapshots
At creation an order copies what it needs: the customer's name, email and phone, the shipping
address, and per line the SKU, title (`"Erigo Basic Tee — Black / M"`) and unit price. Later edits
to the product, the variant or the customer's profile change nothing on the order.

### BR-077 Order numbers
Each order gets a human reference unique per tenant, `{tenant prefix}-{6-digit sequence}`
(`TKA-000123`), from a per-tenant sequence. Gaps are allowed; reuse is not.

### BR-078 Manual orders are priced from the catalog
Manual (WhatsApp) orders are priced by `variant_price()` exactly as checkout is (BR-046); an operator may add
a per-line discount, and may set `shipping_amount`. A manual order has no cart; the admin UI
disables submit while the request is in flight.

*Why:* at 2–5 orders a day, a rare duplicate manual order is visible in the list and cancellable;
an idempotency mechanism would cost more than it saves.

### BR-079 Orders are never deleted
Orders are cancelled, never deleted. An order may be edited (address, note, shipping amount) only
while `pending`.

---

## 7. Storefront

### BR-080 What the storefront can see
A product is visible when `status = 'active'` and `archived_at IS NULL`. A variant is visible when
its product is visible and the variant is not archived. Visible means orderable (BR-017).

### BR-081 Storefront handlers read only the storefront views
Storefront handlers query `storefront_products` and `storefront_variants`, never the base tables.
The views are `security_invoker = true` and select only shopper-facing columns.

*Why:* a new storefront endpoint then cannot forget the visibility filter, and cost fields,
internal notes or staff identities cannot leak through a careless `SELECT *`. Without
`security_invoker`, a view runs as its owner, which bypasses RLS and shows every tenant's products.

### BR-082 Who can call what
| Routes | Credential |
|---|---|
| Catalog: products, categories, brands | API key |
| Cart and checkout | API key + cart id |
| Customer account and order history | API key + customer token |
| Guest order lookup | API key + `X-Order-Token` |

No route under `/v1/storefront/me` or `/v1/storefront/orders` answers with an API key alone
(`401 customer_auth_required`).

### BR-083 The key is checked by where the request comes from
- **From a browser** (the request has an `Origin` header): `Origin` must exactly match the key's
  `allowed_origin` (scheme + host + port), else `403 origin_not_allowed` with no
  `Access-Control-Allow-Origin` header. CORS responses echo the matched origin, never `*`, and add
  `Vary: Origin`.
- **From a server** (no `Origin`): accepted. Rate limits are per key and per IP (BR-014).
- Preflight (`OPTIONS`) carries no key and is answered for any origin; the real request is where
  the check happens.

*Why:* browsers always send `Origin` on these requests and page scripts cannot change it, so a
stranger's website cannot use the owner's key in its visitors' browsers. A script or a server can
send anything it likes, and that is acceptable: the key reaches only what any visitor could
already see. The per-IP part of the server limit keeps someone scraping with a copied key from
using up the limit of the shop's own server.

### BR-084 *Retired in v2.3*
Was "a secret key in a browser is refused loudly". There is one kind of key now (BR-028), so
there is no secret key to leak into a browser.

### BR-085 Revoked keys stop working within 60 seconds
Key resolution is cached for at most 60 s.

### BR-086 Tokens are bound to their tenant and audience
The storefront tenant is the API key's tenant. A customer token's `tid` must equal it, else `401`.
Staff tokens (`aud = admin`) are rejected on storefront routes, and customer tokens
(`aud = storefront`) on every admin route.

### BR-087 Carts
- A cart's id is a random UUID v4 (BR-005); holding it is the permission to read and edit the cart.
- A cart stores no prices. It always shows the price right now (BR-046); the price is frozen only
  on order lines at checkout.
- Adding a variant already in the cart sets its quantity. Quantity is 1–999; `0` removes the item.
- Only visible variants can be added (BR-080).
- An open cart expires 30 days after its last change.

### BR-088 One cart, one order
Checkout locks the cart row first, so two checkouts of the same cart serialise. The second sees
`checked_out` and gets **the existing order back with `200`**, not an error. `orders.cart_id` is
`UNIQUE`, so even a bypassed lock cannot create a second order.

*Why:* a double-tapped "Place order" on a slow phone must produce one order, and from the
shopper's side both taps "worked".

### BR-089 Prices are computed on the server, always
The checkout request has no price field. Item totals come from `variant_price()` (BR-046) at the
moment of checkout, and shipping comes from the server's own Biteship quote (BR-121). Any unknown field, a price field in particular, is rejected with `422 unknown_field`,
never ignored, on every storefront route.

*Why:* a bug on the website shows up in development instead of in a customer's total.

### BR-090 Unavailable items fail checkout without touching the cart
Every cart line is re-checked against `storefront_variants`. A variant archived or unpublished
since it went into the cart fails checkout with `409 item_unavailable`, naming the items. The cart
is left as it was so the shopper can remove them and try again. An empty cart is
`422 empty_cart`.

### BR-091 Guest order tokens
A guest checkout returns `order_token = base64url(HMAC-SHA256(k, tenant_id ‖ order_id))`, with `k`
held in the KMS. Nothing is stored: a replayed checkout returns the same token, and lookup
verifies by recomputing and comparing in constant time. A token opens exactly one order. Rotating
`k` invalidates every guest link at once.

### BR-092 Customer accounts are per tenant
Registration and sign-in are per tenant: the same email at two shops is two unrelated customers.
Email is stored lower-case and unique per tenant. A customer signs in with **email and password,
or with Google** (BR-127); there is no WhatsApp or OTP sign-in. Passwords use the staff
parameters (BR-022). A Google-only customer has no password until they set one by reset.
Staff can read customers but never create accounts, set passwords or see password hashes; a
customer resets their own (BR-094).

### BR-093 Customer sessions
- Tokens travel in the response body, not cookies: a cookie set by `api.{domain}` is third-party on
  the owner's domain and Safari blocks it. A website with a server keeps them in its own `httpOnly`
  cookie; a static site keeps the access token in memory.
- Access token: JWT, 15 minutes, `sub` = customer id, `tid` = tenant id, `aud = storefront`.
- Refresh token: 256 random bits, stored hashed, valid 30 days from the last refresh, rotated on
  every use. **Presenting an already-rotated refresh token revokes the whole session.**

### BR-094 Password reset
A reset sends a one-time link valid for 30 minutes. Using it revokes every session of that
customer. The request always answers `202`, whether or not the email exists (BR-021). A
Google-only customer may use it to add a password.

### BR-095 Carts follow the shopper
When a request carries both a cart id and a customer token, the cart's `customer_id` is set, so a
cart started as a guest belongs to the customer after sign-in. A checkout with a customer token
puts the order in that customer's history.

### BR-096 Retention
A nightly job deletes expired open carts with their items, and expired or revoked customer
sessions. Checked-out carts are kept: `orders.cart_id` references them, and that reference is the
checkout guarantee (BR-088). It runs at 02:00 WIB on one worker replica, shop by shop (BR-003),
and a shop it deleted something from gets one `retention.sweep` audit row with the counts and no
actor. A night missed is not caught up; the next night deletes what it would have.

### BR-097 Customer PII can be purged
Shopper PII lives only in `customers`, `customer_identities`, `orders.customer` and
`orders.shipping_address`. A
customer's deletion request, or a tenant deletion, can purge it without touching catalog data.

The owner starts a purge from the customer's page (`customers:purge`, a `customer_purge` job). In
one transaction it:

- anonymises the `customers` row: name `Deleted customer`, email `purged+{id}@invalid` (freeing
  the address to sign up again), phone, password and verification cleared, `purged_at` set;
- deletes their `customer_identities` and `customer_sessions`, which signs them out everywhere;
- redacts every order that is theirs: their own, and guest or manual orders placed with the
  same email. `customer` becomes `{"name": "Deleted customer", "email": null, "phone": null}`,
  the address keeps `city`, `province` and `postal_code` for sales by region and loses its
  street lines, the `note` is cleared, and the stored Midtrans notification is dropped. Totals,
  lines, statuses and payments stay, since the shop's books need them;
- redacts the shipping addresses and notes that `order.update` audit rows recorded for those
  orders;
- writes one `customer.purge` audit row, which names no personal data.

It is safe to run twice. An order export already downloaded is outside the system; one still in
storage expires within 7 days (BR-053).

*Why:* Indonesia's PDP law gives data subjects that right; it must be a job, not a migration.

---

## 8. Marketplace import

### BR-100 Pull only, one import per channel
An operator starts an import; nothing arrives from Shopee or Tokopedia unasked, and there is no
inbound webhook. A Redis lock per channel means two clicks run one import (the second returns the
running job).

### BR-101 Channel credentials
OAuth credentials are envelope-encrypted (AES-256-GCM, key in a managed KMS), decrypted only in
the worker, and never logged; a credential in a log line is a P1 incident. Disconnecting erases
the credentials and keeps the channel row, its imported products and its listing links.

### BR-102 A failed token refresh needs a human
If refreshing the OAuth token fails, the channel moves to `reauth_required`, the job fails with
that reason, and the admin shows Reconnect. The job never retries silently.

### BR-103 Matching an incoming item
Each marketplace model is matched to a variant; the first rule that hits wins:

| Order | Rule | Result |
|---|---|---|
| 1 | `channel_listings` already has (channel, item, model) | That variant. This is a re-import. |
| 2 | A variant in the tenant has the model's non-empty seller SKU | Link it. A product listed on Shopee and Tokopedia stays one product. |
| 3 | Neither | Create the product, its variants and the listing links. Variation tiers become `option_names`, tier values become `option_values`. The brand is matched by name and created if missing. |

### BR-104 After the first import, the backoffice owns the data
A re-import creates what is new and fills fields that are empty. It never changes a field that
already has a value: description, attributes (existing keys win), SKU, regular price and
weight. Sale prices are never imported; a sale is the owner's decision (BR-046).

*Why:* the owner's edits are never silently lost. The trade, stated plainly: a price changed on
Shopee after the first import does not reach the backoffice. If owners ask for it, add an explicit
per-product "update from marketplace" action rather than changing this default.

### BR-105 New products land as drafts
Imported products land as `draft` unless the import asks for `active`. A product that would fail
the publish check (BR-038) lands as `draft` regardless, and its result says why. Models with no
seller SKU always land as drafts.

### BR-106 Images are copied, never hot-linked
Imported images are copied into R2 and get derivatives (BR-052). No product image URL ever points
at a marketplace domain.

*Why:* marketplace CDN URLs change and break.

### BR-107 One bad item never stops an import
Each item gets one result. A failed item goes into the job's `errors.csv` and the import carries
on. Marketplace calls go through a per-shop token bucket sized to that marketplace's documented
quota; retries are exponential with full jitter, 5 attempts, after which the item fails into the
report.

### BR-108 Unlinking
Deleting a listing link leaves the variant and product alone. The next import matches that item
again by SKU, or creates it (BR-103).

---

## 9. Shipping, payments & email

### BR-120 Shipping rates come from Biteship
Rates are quoted by Biteship's rates API with **one platform API key** held in server config,
never in tenant data. A quote uses the tenant's `origin_postal_code`, the shopper's destination
postal code, the tenant's enabled couriers, and the cart's items with their weights. Courier
booking, labels and pickup are not done here (BR-072).

*Why one key:* a rate check costs about Rp5, which the platform absorbs. A key per owner would add
setup to every onboarding for no gain.

### BR-121 Shipping is re-quoted at checkout
The shopper picks a `courier_code` and `courier_service_code` from the quote. Checkout quotes again
on the server and charges that price. Quotes are cached for 10 minutes per (tenant, destination,
total weight, couriers), so the price the shopper saw is the price charged within that window. A
service no longer offered is `409 shipping_unavailable`; Biteship failing is
`502 shipping_rates_unavailable`, and checkout cannot complete until it is back.

### BR-122 Payment methods are per shop
A shop enables `bank_transfer`, `midtrans`, or both. Checkout must name an enabled method, else
`422`. `midtrans` can only be enabled once its server key, client key and environment are set.

### BR-123 Midtrans payments are attempts
Each Midtrans Snap transaction is one `payments` row. Midtrans refuses a reused `order_id`, so the
first attempt uses the order number (`ERG-000123`) and later attempts add a suffix
(`ERG-000123-2`). The amount is always the order's `total_amount`; nothing from the client sets
it. One order has at most one live attempt.

### BR-124 A Midtrans notification is trusted only after two checks
1. `signature_key` equals `SHA512(order_id + status_code + gross_amount + server_key)` with the
   tenant's server key, else `401` and nothing changes.
2. Midtrans' Get Status API confirms the status before anything is written.

Then `gross_amount` (whole rupiah, so ×100 before comparing, BR-006) must equal the attempt's amount, else the attempt is flagged for an operator
and the order is not marked paid. A verified notification is answered `200` even when it changes
nothing, so Midtrans stops retrying. The notification URL is set on each transaction
(`X-Override-Notification`), so owners do not have to configure it.

*Why the status call:* the signature proves who sent the message; the status call proves the
message is current, and costs one request at a few orders a day.

### BR-125 Paid means settled
An attempt is paid when `transaction_status` is `settlement`, or `capture` with
`fraud_status = accept`. That calls Transition to `paid` (BR-074). `pending` changes nothing.
`expire`, `cancel` and `deny` fail the attempt and leave the order `pending`, so the shopper can
pay again (BR-126) or ops can cancel it.

### BR-126 Paying again
The shopper (customer token or order token) can ask for a payment link for a `pending` Midtrans
order. A live attempt's link is returned; if the last attempt failed or expired, a new attempt is
created. A paid or cancelled order has no payment link.

### BR-127 Social sign-in (Google)
A customer can sign in with an external provider as well as, or instead of, a password. **Google
is the only provider enabled.** Apple and Microsoft follow the same standard (OpenID Connect) and
are added by enabling the provider value, a client-ID setting and its token check; the tables do
not change.

- **Each shop uses its own client ID** (`google_client_id`). The website gets an ID token from the
  provider and sends it to the API. The API verifies its signature against the provider's public
  keys and checks that `iss` is the provider, `aud` is the shop's client ID, the token has not
  expired, and the email is verified.
- **Logins live in `customer_identities`**, one row per (provider, subject). A customer may have a
  password and several identities; all lead to the same account.
- **Finding the customer:** by (provider, subject); else by email, linking the identity to the
  existing account, but **only when the provider says the email is verified**; else a new customer
  with no password.
- A customer is never created with neither a password nor an identity. Sessions are the same as
  any customer's (BR-093).

*Why per shop:* the provider's consent screen shows the shop's name, and the client ID's allowed
origins are the shop's own domain.

*Why a separate table:* one column per provider (`google_sub`, `apple_sub`, …) would need a
migration for every new provider. Apple can also hide a shopper's real email behind a relay
address, so linking by email cannot be assumed for every provider.

### BR-128 Transactional email
Sent through Resend from `"{shop name}" <no-reply@{domain}>` with Reply-To
set to the shop's contact email. Emails: staff invitation, customer password reset, order placed,
payment received, order shipped (with courier and tracking). The worker sends them after the
change commits; a rare lost email is accepted, and every email's content is also visible in the
admin or on the website.

### BR-129 Integration secrets are write-only
The Midtrans server key is envelope-encrypted like channel credentials (BR-101), can be set or
replaced, and is never returned: responses say only whether it is set. Client keys and Google
client IDs are public by design and are returned.

Unlike channel credentials, it is decrypted in the API as well as the worker: checkout, pay-again
and the notification webhook call Midtrans while the shopper or Midtrans waits for the answer.
It is decrypted for that one call and never logged.

---

## Traceability

| BR | Shows up in | Proven by |
|---|---|---|
| 001 | every tenant table | P1-006, P1-008, P1-009 |
| 002 | every request | P1-007 |
| 003 | staff login, API key resolution, Midtrans webhook | P1-008, P1-011, P1-202, P1-221 |
| 004 | `variants`, `order_lines`, `cart_items`, … | P1-020, P1-026, P1-100, P1-204 |
| 005–007 | every payload | P1-028, P1-081 |
| 008–009 | every create and `PATCH` | P1-024, P1-028 |
| 010 | `PATCH` on versioned rows | P1-028, P1-029, P1-104 |
| 011 | every error | P1-013 |
| 012 | every catalog `DELETE` | P1-021, P1-028, P1-029 |
| 013 | logging | P1-016 |
| 014 | every route | P1-015, P1-209 |
| 016 | every screen, email and error | review |
| 017 | no quantity anywhere | P1-025, P1-205 |
| 018 | every admin mutation | P1-018 |
| 020–022 | sign in, invitations | P1-010, P1-011, P1-014 |
| 023–025 | Team & roles | P1-012, P1-017, P1-066 |
| 026–027 | Team & roles | P1-064 |
| 028 | API keys screen | P1-019, P1-200, P1-201 |
| 029 | settings, onboarding | P1-071, P1-068 |
| 030–036 | brand and category managers | P1-020–024, P1-031, P1-032 |
| 037–042 | product editor, variant matrix | P1-025–029, P1-040, P1-041, P1-046, P1-049 |
| 043–044 | bulk edit, CSV import | P1-072, P1-073, P1-074 |
| 045 | archive a product with orders | P1-100 |
| 046 | variant editor, matrix, cart, checkout | P1-026, P1-029, P1-040, P1-205 |
| 050–053 | media library, image domain | P1-042–045, P1-048 |
| 060, 063–065 | jobs, order export | P1-060, P1-107, P1-112 |
| 070–073 | order detail | P1-101, P1-102 |
| 074–075 | order list saved views, Midtrans payments | P1-102, P1-103, P1-108, P1-221 |
| 076–079 | orders, manual entry | P1-100, P1-104, P1-105 |
| 080–081 | every storefront catalog route | P1-203 |
| 082–086 | every storefront route | P1-202, P1-210, P1-211 |
| 087 | cart routes | P1-204 |
| 088–091 | checkout | P1-205, P1-208 |
| 092–095 | customer accounts | P1-206, P1-207, P1-208 |
| 096 | nightly retention | P1-214 |
| 097 | data-subject request | P1-215 |
| 100–108 | marketplace import | P1-300–309, P1-400, P1-401 |
| 120–121 | checkout shipping choice | P1-218, P1-219 |
| 122–126 | checkout payment, pay again, order detail | P1-216, P1-220, P1-221, P1-222, P1-229 |
| 127 | sign in with Google | P1-223, P1-224 |
| 128 | every email | P1-225, P1-226, P1-228 |
| 129 | storefront settings | P1-216, P1-217, P1-227 |
