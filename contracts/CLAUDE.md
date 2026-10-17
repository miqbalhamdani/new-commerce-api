# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

The spec and contracts for Ecommerce Backoffice v2. No application code lives here. It is
consumed as a git submodule at `contracts/` by `new-commerce-api` (Go) and `new-commerce-web`
(Next.js admin), both of which treat it as read-only and generate code from the OpenAPI files.

## Commands

CI (`.github/workflows/ci.yml`, P1-004) only lints both OpenAPI documents. Warnings are allowed;
errors fail the build. Run the same locally (Node 22):

```bash
npx -y @redocly/cli@2.62.1 lint openapi.yaml storefront.yaml
```

ACL Cosmetics catalog migration (`acl-migration/`, one-off; stdlib Python 3.9+, run with `-I`).
`acl-migration/RUNBOOK.md` is the full procedure (rehearsal on a local stack, backups, rollback):

```bash
cd acl-migration
python3 -I import_acl.py --backup ~/Downloads/acl-cosm-backup --dry-run
ACL_EMAIL=... ACL_PASSWORD=... python3 -I import_acl.py --backup ~/Downloads/acl-cosm-backup \
  --api https://api.<domain>/v1 [--state state.local.json]
```

It resumes from `state.json` (git-ignored, tied to one API URL; use a different `--state` per API).

## How the documents relate

- `02-business-rules.md` is the single home of every rule, as `BR-xxx`. All other docs, and tests
  in the code repos, cite rules by id and do not restate them. Ids are grouped by area (see the
  file header), never reused; a dropped rule is marked **retired** and keeps its number. Platform
  rules (BR-001..019) are breaking changes for both code repos.
- `01-product-requirements.md`: scope, screens, module stories and acceptance, NFRs, decisions.
- `03-erd.md`: DDL and views; constraints are tagged with the BR they enforce. The API repo's
  migrations must match it.
- `04-api-spec.md`: the human-readable HTTP contract for both route trees (admin `/v1`,
  storefront `/v1/storefront` in §11), each endpoint listing its permission and BR ids.
- `05-backlog.md`: build order, status and Definition of Done. Items are `P1-xxx` with a repo tag
  (`BE`, `FE`, `CT`, `OPS`); acceptance cites BR ids, and `§x.y` refers to `04-api-spec.md`.
  Rules: one item per PR/branch (`p1-<id>-<slug>`), claim by setting `wip` + Owner in a commit
  first, `done` only when acceptance is proven.
- `GO-LIVE-CHECKLIST.md`: Phase 6 go-live status (P1-070 pilot); the backlog is the source of truth.

## The two OpenAPI files

- `openapi.yaml`: admin `/v1`. API generates `internal/http` from it (`oapi-codegen`); web
  generates `src/lib/api/schema.d.ts` from it (`openapi-typescript`, `npm run generate`).
- `storefront.yaml`: storefront `/v1/storefront`. Only the API consumes it: generated into
  `internal/storefront`, copied there, embedded, and served at `/v1/storefront/openapi.json` and
  `/v1/storefront/docs`. It shares nothing by `$ref` with `openapi.yaml` (P1-202) and must stand
  alone for a storefront developer.
- Both are **incomplete by design**: a path is added only when the backlog item that builds it
  is done, because the API generates a strict server interface and a path without a handler
  breaks its build. `/healthz` is deliberately absent.
- Wherever `04-api-spec.md` and an OpenAPI file both describe something, they must agree: change
  both in the same commit.

## Versioning

Each release commit is titled `Contracts vX.Y.Z: <summary>` and bumps `VERSION`. Minor for
contract additions or changes; patch for fixes with no route, payload or behaviour change (e.g.
v2.12.1, tags only). `openapi.yaml`'s `info.version` is bumped to the release version when that
file changes; `storefront.yaml` has its own `info.version` (0.x) bumped when it changes.

## Cross-repo workflow

1. A change the code needs goes here first, in its own commit/PR (Definition of Done item 2: if
   the code can't match the contract, the contract changes first).
2. After it merges to `main`, each code repo bumps its `contracts` submodule pin and runs its
   generator (`make generate` in api, `npm run generate` in web), committing the generated output.
3. Both code repos' CI enforce drift: `generated-diff` fails if generated code is stale, and a
   `drift` job fails unless the pinned contracts commit is on this repo's `main`, so a code PR
   goes green only once its contracts PR has merged.
