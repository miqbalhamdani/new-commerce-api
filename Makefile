DB_NAME ?= new_commerce_dev

# Load .env when one exists. The defaults compiled into cmd/api already target
# a host install, so a .env is only needed to deviate from them.
LOAD_ENV = set -a; [ -f .env ] && . ./.env; set +a;

.PHONY: dev db-create generate generated-diff fmt-check vet lint test check

## dev: run the API against host PostgreSQL and Redis
dev:
	@$(LOAD_ENV) go run ./cmd/api

## db-create: create the local development database if it is not there yet
db-create:
	@psql -lqtA -F'|' | cut -d'|' -f1 | grep -qx '$(DB_NAME)' || createdb '$(DB_NAME)'
	@echo "$(DB_NAME) ready"

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

## check: the bar for a PR
check: generate generated-diff fmt-check vet lint test
