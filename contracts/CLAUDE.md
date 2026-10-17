# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working in this folder.

## What this folder is

The spec and contracts for Ecommerce Backoffice v2. No application code lives here.
`new-commerce-api/contracts/` is the source of truth; `new-commerce-web/contracts/` is a
byte-for-byte mirror of it (`npm run contracts` refreshes it; never edit it there). Both repos
generate code from the OpenAPI files. It used to be the `new-commerce-contracts` repo, consumed as
a submodule; that repo now only holds the one-off `acl-migration/`.

## Commands

api's CI `contract-lint` job (P1-004) lints both OpenAPI documents. Warnings are allowed; errors
fail the build. Run the same locally (Node 22), from the repo root:

```bash
npx -y @redocly/cli@2.62.1 lint contracts/openapi.yaml contracts/storefront.yaml
```

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

1. A change the code needs goes into `new-commerce-api/contracts/` first, as its own commit in
   the api PR that consumes it (Definition of Done item 2: if the code can't match the contract,
   the contract changes first). Backlog status-only edits may go straight to api `main`.
2. After it merges, web runs `npm run contracts` (mirror api `main`) and `npm run generate`,
   committing both. Web PRs never edit `contracts/` by hand.
3. Both repos' CI enforce drift: `generated-diff` fails if generated code is stale, and web's
   `drift` job fails unless its `contracts/` equals api's `contracts/` at some commit on api
   `main`, so a web PR goes green only once the api PR carrying its contract change has merged.
