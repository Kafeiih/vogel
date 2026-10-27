# -include: un .env ausente no rompe los targets que no lo necesitan
# (lint, docker-build en una copia recién creada). Los que
# necesitan la base dependen de require-db-url, que corta con un mensaje claro.
-include .env
export DB_URL

.DEFAULT_GOAL := dev

require-db-url:
	@test -n "$(DB_URL)" || { echo "DB_URL no está definida: cree .env (ver README, Arranque rápido) o expórtela." >&2; exit 1; }

.PHONY: dev build test migrate lint docker generate lint-api mock-api migrate-up migrate-down migrate-status migrate-create require-db-url deploy
# vogel:component worker begin
.PHONY: worker build-worker
# vogel:component worker end
# vogel:component cerbos begin
.PHONY: cerbos-test cerbos-smoke
# vogel:component cerbos end

# Desarrollo
# start-from-migrate aplica las migraciones y luego sirve (comodidad local).
# En despliegue: "api migrate up" con un rol migrador y luego "api server".
# vogel:component worker begin
# "api migrate up" incluye el esquema de River, y "worker server" corre junto
# a "api server".
# vogel:component worker end
dev: require-db-url
	go run ./cmd/api start-from-migrate

# vogel:component worker begin
worker: require-db-url
	go run ./cmd/worker start-from-migrate

# vogel:component worker end
# Build
build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/api ./cmd/api

# vogel:component worker begin
build-worker:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/worker ./cmd/worker

# vogel:component worker end
# Tests (unit only — no Docker required)
test:
	go test ./... -race -cover

# Integration tests (requires Docker)
test-integration:
	go test ./... -race -cover -tags=integration

# Migraciones
# migrate-up/down/status delegate to the ./api binary subcommand which uses
# the embedded SQL files. DB_URL must be set (from .env or the environment).
migrate-up: require-db-url
	go run ./cmd/api migrate up

migrate-down: require-db-url
	go run ./cmd/api migrate down

migrate-status: require-db-url
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
	docker build -f Dockerfile --target api -t api:$(shell git rev-parse --short HEAD) ../..
# vogel:component worker begin
	docker build -f Dockerfile --target worker -t worker:$(shell git rev-parse --short HEAD) ../..
# vogel:component worker end

docker-run:
	docker compose -f deploy/docker-compose.yaml up -d

# Despliega solo si las imágenes propias llevan una firma válida (ver
# deploy/verify-images.sh).
deploy:
	./deploy/verify-images.sh
	docker compose -f deploy/docker-compose.yaml up -d

# vogel:component cerbos begin
# Cerbos: compila las políticas y corre sus tests (la misma etapa que valida
# la imagen), y prueba que la imagen construida sirva las políticas.
cerbos-test:
	./cerbos/test.sh

cerbos-smoke:
	./cerbos/smoke.sh

# vogel:component cerbos end
# Levanta infra de desarrollo (Postgres local).
# vogel:component cerbos begin
# También levanta Cerbos con las políticas de cerbos/.
# vogel:component cerbos end
dev-infra:
	docker compose -f deploy/docker-compose.developer.yaml up -d

dev-infra-down:
	docker compose -f deploy/docker-compose.developer.yaml down

# Regenerar el servidor desde el contrato (api/openapi.yaml)
generate:
	go generate ./...

# Validar el contrato
lint-api:
	npx --yes @redocly/cli@2.59.0 lint api/openapi.yaml

# Servidor mock del contrato en http://localhost:4010
mock-api:
	npx --yes @stoplight/prism-cli@5.16.0 mock api/openapi.yaml
