DB_NAME ?= new_commerce_dev

# Load .env when one exists. The defaults compiled into cmd/api already target
# a host install, so a .env is only needed to deviate from them.
LOAD_ENV = set -a; [ -f .env ] && . ./.env; set +a;

.PHONY: dev db-create generate fmt-check vet lint test check

## dev: run the API against host PostgreSQL and Redis
dev:
	@$(LOAD_ENV) go run ./cmd/api

## db-create: create the local development database if it is not there yet
db-create:
	@psql -lqtA -F'|' | cut -d'|' -f1 | grep -qx '$(DB_NAME)' || createdb '$(DB_NAME)'
	@echo "$(DB_NAME) ready"

## generate: no-op until sqlc and oapi-codegen are wired in P1-005
generate:
	@echo "nothing to generate yet -- sqlc and oapi-codegen arrive in P1-005"

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

## check: the bar for a PR
check: generate fmt-check vet lint test
