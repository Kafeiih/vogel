# Boilerplate

Un boilerplate de servicio HTTP + worker, listo para copiar como punto de partida
de un servicio nuevo. A diferencia de `examples/api` (que muestra cómo cablear
`vogel` en el mínimo espacio posible), este directorio es una aplicación con
forma real: capas `domain`/`application`/`infrastructure`/`interfaces`,
Swagger, un `Dockerfile` multi-stage, `docker-compose` para desarrollo y
despliegue, un `Makefile`, y un job de ejemplo para el worker en background.

Viene de migrar [`go-bluprint`](https://github.com/kafeiih/go-bluprint) para que
consuma `vogel` en lugar de reimplementar localmente lo que `vogel` ya provee:
autenticación, autorización, auditoría, storage, notificaciones, Postgres,
migraciones y colas de trabajo. Lo que queda acá es exclusivamente lo que un
boilerplate necesita tener propio: los binarios de entrada, el router, la
configuración de la aplicación, y el andamiaje de ejemplo para agregar
negocio.

## Módulo anidado — por qué tiene su propio `go.mod`

Este directorio es un módulo Go independiente (`github.com/kafeiih/vogel/examples/boilerplate`),
no un paquete más del módulo `vogel`:

```
module github.com/kafeiih/vogel/examples/boilerplate

go 1.25.7

require github.com/kafeiih/vogel v0.1.0

replace github.com/kafeiih/vogel => ../..
```

El boilerplate trae dependencias que `vogel` deliberadamente no tiene:
`swaggo/swag` y `swaggo/http-swagger` (generación y servido de documentación
Swagger), `go-playground/validator/v10` (validación declarativa por tags, si
la necesita al agregar un módulo nuevo) y `joho/godotenv` (carga de `.env` en
desarrollo). Meter esas dependencias en el `go.mod` raíz de `vogel` le
impondría un toolchain de generación de código a **todo** consumidor de la
librería, aunque nunca sirva un endpoint de Swagger. El `replace` apunta al
código de `vogel` en este mismo commit, así que este módulo compila contra la
versión de la librería que vive en este repositorio — lo que lo convierte,
de paso, en un test de regresión de la API pública de `vogel` (ver el job
`boilerplate` en `.github/workflows/ci.yaml`).

**Al copiar este directorio para un servicio nuevo, hay que quitar el
`replace`** y fijar una versión real publicada de `vogel`:

```bash
cd tu-servicio-nuevo/
go mod edit -dropreplace github.com/kafeiih/vogel
go get github.com/kafeiih/vogel@v0.1.0
go mod tidy
```

Sin ese paso, el `go.mod` copiado sigue apuntando a `../..` — una ruta que ya
no existe fuera de este repositorio — y nada compila.

## Arranque rápido

```bash
cd examples/boilerplate
cp .env.example .env    # ver más abajo si el archivo no existe en su copia
docker compose -f deploy/docker-compose.developer.yaml up -d   # Postgres + Cerbos
go run ./cmd/api
```

> Este agente no pudo crear `.env.example` en este repositorio por una regla
> de permisos que bloquea la escritura de archivos con forma de `.env`. Cree
> el suyo con este contenido antes del primer arranque:
>
> ```bash
> APP_ENV=development
> APP_PORT=:8080
> APP_URL=localhost:8080
> APP_VERSION=0.1.0
> APP_TIMEOUT=30s
>
> DB_URL=postgres://postgres:postgres@localhost:5433/boilerplate_db?sslmode=disable
> DB_MAX_CONNS=25
> DB_MIN_CONNS=5
> DB_MAX_CONN_LIFETIME=1h
> DB_MAX_CONN_IDLE_TIME=30m
> DB_CONNECT_TIMEOUT=10s
>
> ZITADEL_ISSUER=https://auth.institucion.gob
> ZITADEL_CLIENT_ID=your-client-id-here
>
> CERBOS_HOST=localhost:3593
> CERBOS_TLS=false
>
> CORS_ALLOWED_ORIGINS=http://localhost:3000
> ```
>
> Variables adicionales (todas opcionales, con default razonable si se
> omiten): `WORKER_SCHEMA`, `WORKER_DEFAULT_MAX_WORKERS`, `WORKER_QUEUES`,
> `STORAGE_CONFIGS` (+ `STORAGE_<NOMBRE>_*`), `NOTIFICATION_PROVIDER`,
> `NOTIFICATION_DEFAULT_FROM`, `SMTP_*`, `SENDGRID_API_KEY`,
> `METRICS_LISTEN_ADDR`. Ver `internal/infrastructure/config/config.go` para
> el detalle completo de cada una.

`docker compose -f deploy/docker-compose.developer.yaml` levanta Postgres,
Cerbos y MinIO para desarrollo local. `docker compose -f
deploy/docker-compose.yaml` es el compose de despliegue (API + worker +
Postgres) que construye la imagen desde el `Dockerfile` de este directorio —
ese build sólo funciona **después** de quitar el `replace` (ver arriba):
mientras el `go.mod` apunte a `../..`, el contexto de build de Docker no
tiene forma de ver el código de `vogel` fuera de este directorio.

Las migraciones corren solas al arrancar. Verificación:

```bash
curl -s localhost:8080/v1/healthz
```

El worker se arranca aparte:

```bash
go run ./cmd/worker
```

El `Makefile` resume estos comandos (`make dev`, `make worker`, `make
dev-infra`, `make migrate-up`, `make lint`, `make docker-build`, ...).

## Qué trae de `vogel` y qué es propio

| Capa | Viene de `vogel` | Es propio de este boilerplate |
|---|---|---|
| Autenticación | `vogel/auth` + `vogel/auth/zitadel` | — |
| Autorización | `vogel/authz` + `vogel/authz/cerbos` | — |
| Auditoría | `vogel/audit` + `vogel/audit/postgres` + `vogel/audit/migrations` | `internal/interfaces/http/handler/audit_handler.go` — un handler propio y delgado que reutiliza `vogel/audit/httpx.FiltersFromRequest`/`ToResponse`/`ToResponseList`, para poder conservar sus propias anotaciones Swagger (que sólo se leen de los comentarios sobre la función handler concreta de la app, nunca de un tipo importado) |
| Storage S3-compatible | `vogel/storage` + `vogel/storage/s3` | — |
| Notificaciones (email) | `vogel/notification` + `vogel/notification/smtp` + `vogel/notification/sendgrid` | — |
| Postgres, métricas de pool | `vogel/postgres` | — |
| Migraciones de esquema | `vogel/migrate` | `internal/infrastructure/database/migrations/` (las tablas propias de este servicio) + `internal/infrastructure/database/migrate/create.go` (scaffolder de CLI para nuevas migraciones — ver nota abajo) |
| Cola de trabajos en background | `vogel/worker` (River) | `internal/application/jobs/` (los jobs de negocio concretos) |
| Middleware HTTP (recovery, rate limit, headers de seguridad, logging estructurado, contexto de request, métricas) | `vogel/httpx/middleware` | — |
| Respuestas HTTP | `vogel/httpx/response` | — |
| Parseo/validación de requests | `vogel/request` | — |
| Logging estructurado | `vogel/logger` | — |
| Binarios de entrada | — | `cmd/api/main.go`, `cmd/worker/main.go` |
| Router | — | `internal/interfaces/http/router.go`, `server.go` |
| Health checks | — | `internal/interfaces/http/handler/health_handler.go` (`vogel` no trae uno: cada app decide qué considera "sano") |
| Servidor de métricas dedicado | — | `internal/interfaces/http/metrics_server.go` (`/metrics` en un puerto aparte del tráfico público — `vogel` no lo define) |
| Configuración | — | `internal/infrastructure/config/config.go`: los *structs* de configuración (`Config`, `AppConfig`, ...) son propios — `vogel/config` deliberadamente no los define, sólo expone primitivas de lectura (`config.String`, `config.Int32`, `config.Duration`, `config.Require`, un acumulador `config.Errors`, ...). Donde el `Config` de un adaptador de `vogel` ya coincide campo a campo con lo que este boilerplate necesita (`postgres.Config`, `worker.Config`, `zitadel.Config`, `cerbos.Config`, `storage/s3.Config`, `notification/smtp.Config`, `notification/sendgrid.Config`), `Config` lo embebe directamente en vez de duplicarlo en un struct local que después hay que convertir a mano |

Sobre `internal/infrastructure/database/migrate/create.go`: es un scaffolder
de CLI (`go run ./cmd/api migrate create <nombre>`) que escribe un archivo
`.sql` vacío con el formato de goose. `vogel/migrate` no lo provee — es
deliberadamente una librería de ejecución (`Up`/`Down`/`Status`), no de
autoría de migraciones. Se conserva porque sigue siendo una comodidad de
desarrollo real y no depende de nada que se haya borrado.

### Tres conjuntos de migraciones, dos tablas de versión

`cmd/api/main.go` llama a `migrate.Up` dos veces, cada una contra su propia
tabla de versión de goose:

| Conjunto | Tabla de versión |
|---|---|
| Las de este servicio (`internal/infrastructure/database/migrations/`) | `goose_db_version` (default) |
| `vogel/audit/migrations` | `vogel_db_version` |

Un tercer conjunto, `vogel/workflow/migrations` (tabla `workflow_db_version`),
existe en `vogel` pero **no se monta acá** porque este boilerplate no usa
`vogel/workflow` — ver `docs/USAGE_GUIDE.md` si su servicio sí necesita un
motor de BPM/workflow. Los tres conjuntos numeran independientemente desde
`001`; con una tabla compartida, el segundo en correr se vería como "ya
aplicado" y se saltearía en silencio — por eso `migrate.Options.TableName` es
un parámetro obligatorio del llamador, no una constante escondida en la
librería.

`internal/infrastructure/database/migrations/` trae una única migración de
ejemplo (`001_enable_pgcrypto.sql`, habilita la extensión `pgcrypto` para
`gen_random_uuid()`) — bórrela o renumérela al agregar las tablas reales de
su servicio.

## Mensajes en español

Este boilerplate es para aplicaciones en español, así que fija explícitamente
los textos de error de `vogel/httpx/middleware` (cuyos defaults son en
inglés neutro) en vez de dejarlos caer al default:

```go
var authMessages = vmw.WithAuthMessages(vmw.AuthMessages{
    Unauthorized:       "Autenticación requerida",
    Forbidden:          "No tenés permisos para realizar esta acción",
    ServiceUnavailable: "Servicio no disponible temporalmente",
})
```

Ver `internal/interfaces/http/router.go`. Al agregar un módulo de negocio con
su propio handler de auditoría (si no reutiliza `audit_handler.go` tal cual),
aplique el mismo criterio con `httpx.WithMessages(...)` de
`vogel/audit/httpx`.

## Cómo copiar este boilerplate para un servicio nuevo

1. Copie `examples/boilerplate/` a un repositorio nuevo.
2. Quite el `replace` y fije una versión real de `vogel` (ver arriba).
3. Renombre el módulo (`go.mod`) al de su servicio.
4. Reemplace `internal/infrastructure/database/migrations/001_enable_pgcrypto.sql`
   por las migraciones reales de su dominio.
5. Sume dotfiles de desarrollo típicos que este repositorio no puede alojar
   dentro de `vogel` (`.gitignore`, `.dockerignore`, `.golangci.yml`,
   `.air.toml` para *live reload*) — no vinieron con esta copia porque viven
   fuera del árbol versionado por `go.mod`; tómelos como referencia del
   `go-bluprint` original si los necesita.
6. Cree su propio `.env` a partir de la sección "Arranque rápido" de este
   README.
7. Arranque un módulo de negocio nuevo siguiendo `docs/USAGE_GUIDE.md`.

## Agregar un módulo de negocio nuevo

`docs/USAGE_GUIDE.md` tiene el manual paso a paso, con un ejemplo completo
(`proveedor`): dominio, repositorio Postgres sobre `vogel/pgxtx`, comandos de
aplicación que auditan con `vogel/audit` (recordando pasar `audit.SourceHTTP`
o `audit.SourceWorker` según si el call site es un handler o un job), y un
handler HTTP con sus propias anotaciones Swagger. `internal/application/jobs/`
tiene dos jobs de referencia (`example.go`, `send_email.go`) para el mismo
patrón del lado del worker.

## Documentación adicional

- `docs/USAGE_GUIDE.md` — cómo agregar un módulo de negocio nuevo, paso a paso.
- `docs/CERBOS_POLICIES.md` — cómo escribir políticas Cerbos para este boilerplate.
- `deploy/RUNBOOK.md` — operación en producción.
- `docs/swagger.json` / `docs/swagger.yaml` — spec generada por `swag init`;
  regenerar con `make gen-docs` después de anotar un handler nuevo.
