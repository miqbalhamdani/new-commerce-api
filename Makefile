DB_NAME ?= new_commerce_dev

# Load .env when one exists. The defaults compiled into cmd/api already target
# a host install, so a .env is only needed to deviate from them.
LOAD_ENV = set -a; [ -f .env ] && . ./.env; set +a;

.PHONY: dev db-create migrate migrate-down generate generated-diff fmt-check vet lint test test-iso lint-rls check

## dev: run the API against host PostgreSQL and Redis
dev:
	@$(LOAD_ENV) go run ./cmd/api

## db-create: create the local development database if it is not there yet
db-create:
	@psql -lqtA -F'|' | cut -d'|' -f1 | grep -qx '$(DB_NAME)' || createdb '$(DB_NAME)'
	@echo "$(DB_NAME) ready"

## migrate: apply every migration and exit
migrate:
	@$(LOAD_ENV) go run ./cmd/migrate

## migrate-down: roll back N migrations. Local only -- production is forward-only.
migrate-down:
	@$(LOAD_ENV) go run ./cmd/migrate -down $(or $(N),1)

## generate: contracts/openapi.yaml -> internal/http/gen.go
generate:
	@test -f contracts/openapi.yaml \
		|| { echo "contracts/ is empty. Run: git submodule update --init"; exit 1; }
	@go tool oapi-codegen -config oapi-codegen.yaml contracts/openapi.yaml

## generated-diff: fail if the tree does not match what the generator produces
generated-diff:
	@git diff --exit-code -- internal/http/gen.go \
		|| { echo "internal/http/gen.go is stale -- commit the result of 'make generate'"; exit 1; }

fmt-check:
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	@go vet ./...

lint:
	@golangci-lint run

## test: needs PostgreSQL and Redis up -- see the note in cmd/api/health_test.go
test:
	@$(LOAD_ENV) go test ./...

## test-iso: tenant isolation over every registered route, plus the harness's own checks
test-iso:
	@$(LOAD_ENV) go test ./internal/http/ -run 'TestTenantIsolation|TestIsolationHarness' -v

## lint-rls: fail if a tenant table is not protected by row level security
lint-rls:
	@$(LOAD_ENV) go run ./cmd/lint-rls

## check: the bar for a PR. test covers test-iso -- this is the focused run.
check: generate generated-diff fmt-check vet lint lint-rls test
