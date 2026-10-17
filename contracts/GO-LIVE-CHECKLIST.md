# Go-live checklist: new-commerce-api + new-commerce-web

Status as of 16 Oct 2026. Source of truth: `new-commerce-contracts/05-backlog.md`, Phase 6.
Go-live means **P1-070: pilot tenant live**, which depends on P1-003 (backups) and P1-212
(reference website). Neither is done yet.

## What pgBackRest backs up

**The whole PostgreSQL cluster (the `pgdata` volume).** That covers every shop's catalog,
orders, payments, customers, audit log, API keys, carts, customer sessions, jobs rows and
encrypted credentials. All business data lives here.

Plan (`01-product-requirements.md`): four weekly full backups with continuous WAL archiving
between them, encrypted, stored in R2. RPO 5 min, RTO 1 hour.

- Not set up yet: `deploy/compose.yaml` has `wal_level=replica` but no `archive_mode` or
  `archive_command`.
- RPO 5 min needs `archive_timeout=300` or lower. At pilot traffic a WAL segment fills slowly,
  and without it the archive could lag hours behind.
- Use a **separate R2 bucket and token for backups**, so the app's own S3 key can't delete them.

**Not covered by pgBackRest:**

| Data | Where to keep it | If it's lost |
|---|---|---|
| **`.env` secrets**, above all `ENCRYPTION_KEY`, plus the pgBackRest cipher passphrase | Password manager, not only on the box | A restore works, but encrypted credentials can't be read. Without the passphrase every backup is useless |
| Redis (job queue, rate-limit counters) | AOF on its volume. No backup needed | Queued jobs stay `queued` in Postgres. There's no sweeper yet, so re-run them |
| R2 objects (product images, imports, exports) | R2 itself | Imports expire after 30 days and exports after 7 anyway. Accidental deletes aren't covered, which is fine for the pilot |

## Accounts to create

| # | Account | What it's for | Blocks |
|---|---|---|---|
| 1 | Domain registrar | The production domain | P1-001 |
| 2 | **Cloudflare** | DNS, WAF, Origin certificate, R2 (images and backups), image-domain Worker | P1-001, P1-003, P1-045 |
| 3 | VPS provider (4 vCPU / 8 GB / NVMe) | The box | P1-001 |
| 4 | **Resend** | Email, with DKIM/SPF/DMARC on the domain | P1-225 (the worker won't start without it) |
| 5 | **Biteship** | Shipping rates, one platform key | P1-218 (the API won't start without it) |
| 6 | **Midtrans** (the pilot shop's, sandbox then production) | Payments | P1-220, P1-212 |
| 7 | **Google Cloud**, OAuth client | Customer Google sign-in | P1-223, P1-212 |
| 8 | Grafana Cloud (free tier) | Alerts, traces, logs through Alloy | P1-501 |
| 9 | Password manager or vault | `.env` secrets and the backup passphrase | P1-003 |
| — | GitHub (already have it) | A GHCR read token so the box can pull images | P1-001 |

## Checklist

### Phase 6b: build it locally (Compose, MinIO standing in for R2)

- [ ] **P1-003**: pgBackRest sidecar, WAL archiving, `archive_timeout`, weekly full backups,
      encrypted repo, **restore drill on a clean machine**
- [ ] **P1-501**: Alloy agent; every alert in PRD §8.5 fires in a drill (disk at 70% included);
      checkout and import each appear as one trace
- [ ] **P1-500**: k6 at 100 req/s with an import running, within PRD §8.1
- [ ] **P1-502**: runbooks for restore, deploy and key rotation, each followed once by someone
      who didn't write it
- [ ] **Gap: create a tenant in production.** `dev-seed` refuses to run outside development and
      nothing else inserts into `tenants`. Needs a small `cmd/create-tenant`
- [ ] Update backlog statuses: P1-001, P1-002 and P1-004 are merged but still say `todo`
- [ ] Decide on review: about 90 items are still in `review` (Definition of Done #7)
- [x] dev-seed credentials from env instead of edited source (api PR #25)

### Phase 6c: accounts and cutover

- [ ] Accounts 1–9 above
- [ ] Domain → set `ADMIN_HOST`, `API_HOST`, `EMAIL_DOMAIN`, `IMAGE_BASE_URL`; Origin
      certificate into `/certs`
- [ ] R2 images bucket: lifecycle (`jobs/` 30 days, `exports/` 7 days), CORS `PUT` from the
      admin origin, image domain
- [ ] R2 backup bucket with its own token
- [ ] Resend sending domain verified
- [ ] `.env` filled and `chmod 600`; secrets copied to the vault
- [ ] `./deploy.sh sha-<commit>`; HTTPS works; `check.sh` shows the tuned settings
- [ ] **Restore drill against real R2**
- [ ] **P1-212**: reference website built against the Midtrans, Biteship and Google sandboxes

### Pilot (P1-070)

- [ ] Create the pilot tenant and owner; load the catalog
- [ ] Switch Midtrans to production
- [ ] First real order end to end: payment → email → shipping
- [ ] Two weeks of real orders → exit

**Not needed for go-live:** Shopee and Tokopedia import (P1-300 to P1-401). P1-070 doesn't
depend on them.
