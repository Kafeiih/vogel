include .env
export DB_URL

.PHONY: dev build test migrate lint docker gen-docs worker build-worker migrate-up migrate-down migrate-status migrate-create

# Desarrollo
dev:
	go run ./cmd/api

worker:
	go run ./cmd/worker

# Build
build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/api ./cmd/api

build-worker:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/worker ./cmd/worker

# Tests (unit only — no Docker required)
test:
	go test ./... -race -cover

# Integration tests (requires Docker)
test-integration:
	go test ./... -race -cover -tags=integration

# Migraciones
# migrate-up/down/status delegate to the ./api binary subcommand which uses
# the embedded SQL files. DB_URL must be set (loaded from .env via include above).
migrate-up:
	go run ./cmd/api migrate up

migrate-down:
	go run ./cmd/api migrate down

migrate-status:
	go run ./cmd/api migrate status

# migrate-create scaffolds a new goose-format .sql file via the binary subcommand.
# Usage: make migrate-create NAME=add_users_table
migrate-create:
	go run ./cmd/api migrate create $(NAME)

# Lint
lint:
	golangci-lint run ./...

# Docker
docker-build:
	docker build -t api:$(shell git rev-parse --short HEAD) .

docker-run:
	docker compose -f deploy/docker-compose.yaml up -d

# Levanta infra de desarrollo (Postgres + Cerbos)
dev-infra:
	docker compose -f deploy/docker-compose.developer.yaml up -d

dev-infra-down:
	docker compose -f deploy/docker-compose.developer.yaml down

# Generar documentación
gen-docs:
	@swag init -g main.go -d cmd/api,internal --output docs --parseDependency --parseInternal && swag fmt
