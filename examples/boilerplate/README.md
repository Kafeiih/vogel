# Boilerplate

<!-- vogel:only begin -->
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

`create-vogel-app` genera un repositorio nuevo a partir de este directorio y
pregunta qué componentes opcionales conservar (worker, notificaciones, PDP de
Cerbos empaquetado y storage S3):

```bash
go run github.com/kafeiih/vogel/cmd/create-vogel-app@<versión> <nombre>
```

## Módulo anidado — por qué tiene su propio `go.mod`

Este directorio es un módulo Go independiente (`github.com/kafeiih/vogel/examples/boilerplate`),
no un paquete más del módulo `vogel`:

```
module github.com/kafeiih/vogel/examples/boilerplate

go 1.25.7

require github.com/kafeiih/vogel v0.15.0

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
go get github.com/kafeiih/vogel@v0.15.0
go mod tidy
```

Sin ese paso, el `go.mod` copiado sigue apuntando a `../..` — una ruta que ya
no existe fuera de este repositorio — y nada compila.

<!-- vogel:only end -->
## Arranque rápido

```bash
# vogel:only begin
cd examples/boilerplate
# vogel:only end
cp env.example .env
docker compose -f deploy/docker-compose.developer.yaml up -d   # infraestructura local
go run ./cmd/api start-from-migrate   # o: make dev
```

<!-- vogel:component cerbos begin -->
El Cerbos local sirve las políticas de `cerbos/policies` y las recarga al
guardar. `make cerbos-test` las compila y corre sus tests; `make cerbos-smoke`
construye la imagen del PDP y prueba que las sirve (ver `cerbos/README.md`).

<!-- vogel:component cerbos end -->
`env.example` trae la configuración para desarrollo local; `.env` no se
versiona.

Variables adicionales (todas opcionales, con default razonable si se
omiten; ver `internal/infrastructure/config/config.go` para el detalle
completo de cada una): `METRICS_LISTEN_ADDR`.
<!-- vogel:component worker begin -->
Worker: `WORKER_SCHEMA`, `WORKER_DEFAULT_MAX_WORKERS`, `WORKER_QUEUES`,
`WORKER_POLL_ONLY`, `WORKER_METRICS_LISTEN_ADDR`.
<!-- vogel:component worker end -->
<!-- vogel:component storage begin -->
Storage: `STORAGE_CONFIGS` (+ `STORAGE_<NOMBRE>_*`). Los buckets son
privados (Block Public Access activado) y los clientes acceden con URLs
firmadas. `STORAGE_<NOMBRE>_PRESIGN_EXPIRY` (default `15m`) debe ser mayor
que cero y como máximo `1h`: un valor inválido impide arrancar.
<!-- vogel:component storage end -->
<!-- vogel:component bpms begin -->
Base BPMS: `GOTENBERG_URL` (obligatoria), `GOTENBERG_TIMEOUT` (default
`30s`, máximo `45s`), `BPMS_DOCUMENT_STORAGE` (default `documents`, debe estar en
`STORAGE_CONFIGS`) y `BPMS_PROFILE_HOLDERS` (quién tiene cada perfil, como
`posicion@unidad=user-id`). Ver `docs/BPMS.md`.
<!-- vogel:component bpms end -->
<!-- vogel:component notification begin -->
Notificaciones: `NOTIFICATION_PROVIDER`, `NOTIFICATION_DEFAULT_FROM`,
`SMTP_*`, `SENDGRID_API_KEY`.
<!-- vogel:component notification end -->
<!-- vogel:component tracing begin -->
Tracing (OpenTelemetry): `OTEL_EXPORTER_OTLP_ENDPOINT` (la URL base
OTLP/HTTP del colector, p. ej. `http://otel-collector:4318`; vacía, el
tracing queda apagado), `OTEL_SERVICE_NAME` (default `api` o `worker`) y
`OTEL_TRACES_SAMPLER_ARG` (la fracción de traces nuevos que se muestrean,
mayor que 0 y hasta 1; default `0.1`). Ninguna otra variable `OTEL_*` tiene
efecto: `vogel/otel` no lee el entorno.
<!-- vogel:component tracing end -->

`SWAGGER_ENABLED` (opcional, `false` por defecto) sirve la UI y la spec de
Swagger en `/swagger/`. Es un opt-in explícito, independiente de
`APP_ENV`: el compose de despliegue corre con `APP_ENV=development` y no
debe publicar la descripción de la API. Con `APP_ENV=production` y
`SWAGGER_ENABLED=true` el arranque falla, y un valor que no sea `true` o
`false` también (un error de tipeo no se interpreta en silencio).

`DB_REQUIRE_TLS` (opcional, `true` por defecto, en cualquier `APP_ENV`)
exige que `DB_URL` use `sslmode=verify-ca` o `verify-full`. Solo el `.env`
local y el compose de despliegue, cuyo Postgres no sirve TLS, la ponen en
`false`. Un valor que no sea `true` o `false` hace fallar el arranque.

`DB_ACQUIRE_TIMEOUT` (opcional, `3s` por defecto) es cuánto espera una
petición por una conexión libre del pool antes de fallar con un error que
envuelve `context.DeadlineExceeded`: con el pool lleno la aplicación falla
rápido en vez de encolar peticiones hasta `APP_TIMEOUT`. El límite también
cubre abrir una conexión nueva cuando el pool tiene lugar, así que debe
superar lo que tarda un connect sano; el `Ping` de arranque queda fuera y
espera hasta `DB_CONNECT_TIMEOUT`.
`DB_LEAK_THRESHOLD` (opcional, `10s` por defecto) registra un WARN, con la
función y el `archivo:línea` que pidió la conexión, cuando una conexión se
devuelve después de retenerla más que ese umbral. Ambas deben ser
duraciones mayores que cero; un valor inválido, cero o negativo hace fallar
el arranque. Las esperas y retenciones se exponen en los histogramas
`db_pool_acquire_wait_seconds` y `db_pool_conn_hold_seconds`.

Validación de tokens de Zitadel (todas opcionales; el arranque falla con un
valor inválido o una combinación incoherente, nombrando la variable):

| Variable | Default | Efecto |
|---|---|---|
| `ZITADEL_HTTP_TIMEOUT` | `10s` | Timeout de cada request al issuer (discovery, JWKS, introspección). Debe ser mayor que cero. |
| `ZITADEL_MAX_TOKEN_LIFETIME` | `12h` | Rechaza tokens cuyo `exp - iat` lo supera, y los que no traen `iat`. `0` desactiva el control. |
| `ZITADEL_ALLOWED_AUTHORIZED_PARTIES` | vacía | Client IDs (separados por comas) aceptados como `azp`, normalmente las apps frontend. Vacía no valida `azp`; la audiencia (`ZITADEL_CLIENT_ID`) se valida siempre. |
| `ZITADEL_INTROSPECTION` | `false` | `true` valida cada token contra el endpoint de introspección, así un token revocado deja de valer sin esperar su `exp`. Necesita exactamente una credencial: |
| `ZITADEL_INTROSPECTION_KEY_FILE` / `ZITADEL_INTROSPECTION_CLIENT_SECRET` | — | `key.json` de la app API, o su client secret. Definir una sin `ZITADEL_INTROSPECTION=true` es un error. |
| `ZITADEL_INTROSPECTION_CACHE_TTL` | `30s` | Cuánto se reutiliza un resultado activo; es también lo máximo que un token revocado sigue aceptado. |
| `ZITADEL_INTROSPECTION_CACHE_MAX_ENTRIES` | `10000` | Resultados en caché. |

`CERBOS_PLAINTEXT` (opcional, `false` por defecto) desactiva TLS hacia el
PDP de Cerbos. Sin ella la conexión gRPC usa TLS; el contenedor local de
Cerbos escucha sin TLS, por eso el `.env` local la pone en `true`. Nunca la active en staging ni en producción: las decisiones
de autorización viajarían en texto plano. Acepta solo valores booleanos
(`true`, `false`, `1`, `0`...): cualquier otro hace fallar el arranque en
lugar de leerse como `false`. Reemplaza a `CERBOS_TLS`: si esa
variable sigue definida (con cualquier valor), el arranque falla con un
error que indica usar `CERBOS_PLAINTEXT`, para que la migración no pase
inadvertida.

`TRUSTED_PROXIES` (opcional, vacía por defecto) lista, separados por comas,
los CIDR o IP de los proxies inversos o balanceadores cuyas cabeceras
`X-Forwarded-For` / `X-Real-IP` se aceptan para resolver la IP del cliente
(la que queda en `audit_log.ip` y la que usa el rate limit por IP), p. ej.
`TRUSTED_PROXIES=10.0.0.0/8,192.168.1.10`. Vacía, esas cabeceras se ignoran
y la IP es siempre la del par TCP: un cliente no puede falsificarla. Detrás
de un balanceador hay que declararlo aquí, o todas las requests aparecen con
su IP. Un valor inválido hace fallar el arranque.

`docker compose -f deploy/docker-compose.developer.yaml` levanta Postgres
para desarrollo local.
<!-- vogel:component cerbos begin -->
También levanta Cerbos, con las políticas de `cerbos/policies`.
<!-- vogel:component cerbos end -->
<!-- vogel:component storage begin -->
No trae MinIO (su imagen ya no se publica en Docker Hub): apunte `STORAGE_*`
a cualquier endpoint S3-compatible si lo necesita.
<!-- vogel:component storage end -->
<!-- vogel:component bpms begin -->
También levanta Gotenberg (HTML a PDF) en `127.0.0.1:3001`.
<!-- vogel:component bpms end -->

`docker compose -f deploy/docker-compose.yaml` es el compose de despliegue
(Postgres, `migrate`, `grants` y la API) que construye las imágenes desde el
`Dockerfile`, con la raíz del repositorio como contexto de build.
<!-- vogel:component worker begin -->
También corre el worker.
<!-- vogel:component worker end -->
<!-- vogel:component cerbos begin -->
También corre el PDP de Cerbos, construido desde `cerbos/`.
<!-- vogel:component cerbos end -->
<!-- vogel:component bpms begin -->
También corre Gotenberg, sin puertos publicados, para el worker.
<!-- vogel:component bpms end -->
<!-- vogel:only begin -->

Dentro de `vogel` esa raíz es la de `vogel`, no este directorio: como el
`go.mod` apunta a `../..` con el `replace`, el build necesita el código de la
librería (el compose ya lo configura así, y el `.dockerignore` de la raíz
filtra lo que entra):

```bash
# desde la raíz de vogel
docker build -f examples/boilerplate/Dockerfile --target api -t boilerplate:api .
docker build -f examples/boilerplate/Dockerfile --target worker -t boilerplate:worker .
```

En una copia sin `replace` (ver "Cómo copiar este boilerplate"), el módulo
es la raíz del contexto: construya desde ese directorio con `--build-arg
APP_DIR=.` (o cambie el valor por defecto en el `Dockerfile`).
<!-- vogel:only end -->

El binario separa el arranque en fases, como `zitadel setup` / `zitadel
start` / `zitadel start-from-setup`:

| Subcomando | Qué hace | Cuándo usarlo |
|---|---|---|
| `./api migrate up` | Aplica todos los conjuntos de migraciones contra `DB_URL` y termina | En despliegue, como paso previo, con un rol migrador |
| `./api server` (o `./api` sin argumentos) | Sirve la API; **no** toca el esquema | En despliegue, después de `migrate up`, con un rol que solo tenga `SELECT` e `INSERT` sobre `audit_log` |
| `./api start-from-migrate` | Aplica las migraciones y luego sirve, en un solo proceso | En desarrollo local (`make dev`) |
<!-- vogel:component worker begin -->

`./api migrate up` y `./api start-from-migrate` también crean el esquema de
River (`WORKER_SCHEMA`, default `river`) y aplican sus migraciones. El worker
tiene sus propios subcomandos:

| Subcomando | Qué hace | Cuándo usarlo |
|---|---|---|
| `./worker server` (o `./worker` sin argumentos) | Procesa jobs; **no** crea el esquema de River ni lo migra | En despliegue, después de `migrate up`, con un rol sin permiso `CREATE` |
| `./worker start-from-migrate` | Crea el esquema de River, aplica sus migraciones y luego procesa jobs | En desarrollo local (`make worker`) |

`WORKER_POLL_ONLY=true` hace que el worker busque jobs nuevos consultando
periódicamente en vez de usar `LISTEN`/`NOTIFY` de PostgreSQL, y así puede
conectarse a través de PgBouncer en modo transacción. El costo es latencia:
un job nuevo puede esperar hasta el `FetchPollInterval` de River (1 s por
defecto) antes de tomarse. Un valor que no sea `true` o `false` hace fallar
el arranque. Default `false`.

El worker sirve su propio `/metrics` en `WORKER_METRICS_LISTEN_ADDR`
(`:9091` por defecto, aparte del `:9090` del API para que ambos puedan correr
en el mismo host). Expone la duración de cada job como
`river_job_duration_seconds{kind,outcome}` (`outcome`: `success`, `error`,
`cancel`, `snooze`, `panic`), registrada con `worker.WithMetrics`, y las
métricas del pool (`db_pool_*`). Scrapéelo con `job="worker"`: las alertas de
`deploy/prometheus/alerts.yml` (`WorkerDown`, `WorkerJobErrorRatioHigh`,
`WorkerJobSlowP95`) asumen ese nombre. Una dirección inválida hace fallar el
arranque.
<!-- vogel:component worker end -->

`start-from-migrate` es una comodidad de desarrollo: el rol que migra queda
como dueño de `audit_log` y puede deshabilitar sus triggers append-only, así
que no sirve para producción. El compose de despliegue ya sigue el esquema de
dos fases: un servicio `migrate` de una sola ejecución corre `migrate up` y
los procesos que sirven arrancan con `server` solo cuando ese servicio
terminó bien.
<!-- vogel:component worker begin -->
`WORKER_SCHEMA` se define una sola vez en el compose (bloque
`x-worker-schema`) y llega con el mismo valor a `migrate`, `api` y `worker`:
se toma de `deploy/.env` o, si no está, vale `river`.
<!-- vogel:component worker end -->
`migrate` no carga
`.env` completo, así que el rol migrador no recibe los secretos de la
aplicación. El compose ya aplica la receta de la sección «Un rol para
migrar, otro para servir» del README de `vogel`: `deploy/initdb/10-roles.sh`
crea `app_migrator` (dueño del esquema, lo usan `migrate` y `grants`) y
`app_runtime` (el de los procesos que sirven), y el servicio `grants` aplica
`deploy/initdb/runtime-grants.sql` después de cada `migrate up`. Cada
ambiente es un host con su `deploy/.env` (plantilla completa en
`deploy/env.example`). Producción usa AWS RDS con `sslmode=verify-full`:
los roles se crean una vez con el usuario master (`db-roles`) y el bundle de
la CA de RDS se monta en los contenedores. Los demás ambientes pueden usar el
Postgres empaquetado con `COMPOSE_PROFILES=bundled-db`, que no publica
puertos en el host. Detalle en la sección 0 de `deploy/RUNBOOK.md`.
Verificación:

```bash
curl -s localhost:8080/healthz
```

<!-- vogel:component worker begin -->
El worker se arranca aparte:

```bash
go run ./cmd/worker start-from-migrate   # o: make worker
```

<!-- vogel:component worker end -->
El `Makefile` resume estos comandos (`make dev`, `make dev-infra`, `make
migrate-up`, `make lint`, `make docker-build`, ...).

## Qué trae de `vogel` y qué es propio

| Capa | Viene de `vogel` | Es propio de este boilerplate |
|---|---|---|
| Autenticación | `vogel/auth` + `vogel/auth/zitadel` | — |
| Autorización | `vogel/authz` + `vogel/authz/cerbos` | — |
| Auditoría | `vogel/audit` + `vogel/audit/postgres` + `vogel/audit/migrations` | `internal/interfaces/http/handler/audit_handler.go` — un handler propio y delgado que reutiliza `vogel/audit/httpx.FiltersFromRequest`/`ToResponse`/`ToResponseList`, para poder conservar sus propias anotaciones Swagger (que sólo se leen de los comentarios sobre la función handler concreta de la app, nunca de un tipo importado) |
| Postgres, métricas de pool | `vogel/postgres` | — |
| Migraciones de esquema | `vogel/migrate` | `internal/infrastructure/database/migrations/` (las tablas propias de este servicio) + `internal/infrastructure/database/migrate/create.go` (scaffolder de CLI para nuevas migraciones — ver nota abajo) |
| Middleware HTTP (recovery, rate limit, headers de seguridad, logging estructurado, contexto de request, métricas) | `vogel/httpx/middleware` | — |
| Respuestas HTTP | `vogel/httpx/response` | — |
| Parseo/validación de requests | `vogel/request` | — |
| Logging estructurado | `vogel/logger` | — |
| Binarios de entrada | — | `cmd/api/main.go` |
| Router | — | `internal/interfaces/http/router.go`, `server.go` |
| Health checks | — | `internal/interfaces/http/handler/health_handler.go` (`vogel` no trae uno: cada app decide qué considera "sano") |
| Servidor de métricas dedicado | — | `internal/interfaces/http/metrics_server.go` (`/metrics` en un puerto aparte del tráfico público — `vogel` no lo define) |
| Configuración | — | `internal/infrastructure/config/config.go`: los *structs* de configuración (`Config`, `AppConfig`, ...) son propios — `vogel/config` deliberadamente no los define, sólo expone primitivas de lectura (`config.String`, `config.Int32`, `config.Duration`, `config.Require`, un acumulador `config.Errors`, ...). Donde el `Config` de un adaptador de `vogel` ya coincide campo a campo con lo que este boilerplate necesita (`postgres.Config`, `zitadel.Config`, `cerbos.Config` y los de los componentes opcionales), `Config` lo embebe directamente en vez de duplicarlo en un struct local que después hay que convertir a mano |

<!-- vogel:component worker begin -->
- **Cola de trabajos en background**: `vogel/worker` (River) para la cola;
  propios son el binario `cmd/worker/main.go` y los jobs de negocio concretos
  de `internal/application/jobs/`.
<!-- vogel:component worker end -->
<!-- vogel:component notification begin -->
- **Notificaciones (email)**: `vogel/notification` +
  `vogel/notification/smtp` + `vogel/notification/sendgrid`, usados por el
  job `send_email` del worker.
<!-- vogel:component notification end -->
<!-- vogel:component storage begin -->
- **Storage S3-compatible**: `vogel/storage` + `vogel/storage/s3`.
<!-- vogel:component storage end -->
<!-- vogel:component tracing begin -->
- **Tracing con OpenTelemetry**: `vogel/otel` (+ `otelpgx`, `otelriver`),
  arrancado y cerrado por `internal/infrastructure/tracing`. Un span por
  request (nombrado por la ruta de chi), por consulta a Postgres, por job de
  River (en el mismo trace que el request que lo encoló), por llamada a
  Zitadel o S3 y por email. Los logs con contexto llevan `trace_id`, y los
  histogramas HTTP y del pool, exemplars. Apagado mientras
  `OTEL_EXPORTER_OTLP_ENDPOINT` esté vacía; `--without tracing` lo saca del
  todo.
<!-- vogel:component tracing end -->
<!-- vogel:component bpms begin -->
- **Base BPMS**: `vogel/workflow`, `vogel/action` y `vogel/document`
  (+ `gotenberg`, `docflow`), armados por `internal/infrastructure/bpms`, y un
  dominio de procesos en capas (`internal/domain/process`,
  `internal/application/process`, `internal/infrastructure/bpms`, rutas
  `/processes`, `/inbox` y `/cases`) con el proceso `consulta` ya publicado;
  ver `docs/BPMS.md`.
<!-- vogel:component bpms end -->

Sobre `internal/infrastructure/database/migrate/create.go`: es un scaffolder
de CLI (`go run ./cmd/api migrate create <nombre>`) que escribe un archivo
`.sql` vacío con el formato de goose. `vogel/migrate` no lo provee — es
deliberadamente una librería de ejecución (`Up`/`Down`/`Status`), no de
autoría de migraciones. Se conserva porque sigue siendo una comodidad de
desarrollo real y no depende de nada que se haya borrado.

### Conjuntos de migraciones y tablas de versión

`cmd/api/main.go` declara los conjuntos en `migrationSets()` y aplica cada uno
contra su propia tabla de versión de goose. `./api start-from-migrate` y
`./api migrate up|status` leen esa misma lista, así que migrar como paso
discreto del despliegue deja la base igual que `start-from-migrate`:

| Conjunto | Tabla de versión |
|---|---|
| Las de este servicio (`internal/infrastructure/database/migrations/`) | `goose_db_version` (default) |
| `vogel/audit/migrations` | `vogel_db_version` |

Cada conjunto numera independientemente desde `001`; con una tabla
compartida, el segundo en correr se vería como "ya aplicado" y se saltearía
en silencio — por eso `migrate.Options.TableName` es un parámetro obligatorio
del llamador, no una constante escondida en la librería.
<!-- vogel:component bpms begin -->

Con la base BPMS este servicio monta además tres conjuntos, después de los
dos de arriba:

| Conjunto | Tabla de versión |
|---|---|
| `vogel/workflow/migrations` | `workflow_db_version` |
| `vogel/document/migrations` | `document_db_version` |
| `internal/infrastructure/bpms/migrations` | `bpms_db_version` |

`migrate down` tampoco los revierte.
<!-- vogel:component bpms end -->

`./api migrate down` revierte sólo la última migración de este servicio, nunca
las de audit: `migrate.Down` deshace la última migración del conjunto que
recibe, y la única migración de audit es la que crea `audit_log`, así que
incluirla borraría el registro de auditoría en cada `migrate down`.

<!-- vogel:component worker begin -->
Las migraciones de River no son de goose: `migrate up` las aplica (son
idempotentes y registra cada versión aplicada), pero `migrate down` nunca las
revierte —sus migraciones de bajada borran las tablas de jobs y todos los
jobs encolados— y `migrate status` no las lista.

<!-- vogel:component worker end -->
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

## Publicar imágenes y CI

Al empujar un tag `vX.Y.Z`, `.github/workflows/release.yaml` construye y sube
a GHCR, con la versión `X.Y.Z` (sin `:latest`) y cada una con SBOM y
*provenance*:

- `ghcr.io/<owner>/<repo>` (api).
<!-- vogel:component worker begin -->
- `ghcr.io/<owner>/<repo>-worker`.
<!-- vogel:component worker end -->
<!-- vogel:component cerbos begin -->
- `ghcr.io/<owner>/<repo>-cerbos`, que antes pasa el *smoke test*.
<!-- vogel:component cerbos end -->

Es todo o nada: los tags se crean solo cuando todas las imágenes salieron
bien. La primera publicación crea paquetes privados: vincúlelos al
repositorio y elija su visibilidad en la configuración del paquete.
`deploy/docker-compose.yaml` descarga esas imágenes con `APP_VERSION`.

Cada imagen se firma con [cosign](https://docs.sigstore.dev/cosign/) y una
clave del repositorio antes de crear los tags; sin la clave, el release
falla. La firma no se sube al log público de transparencia de Sigstore, así
que un servicio privado no publica su nombre. Antes del primer release, cree
el par de claves y guárdelo como *secrets* del repositorio:

```bash
cosign generate-key-pair            # pide una contraseña; crea cosign.key y cosign.pub
gh secret set COSIGN_PRIVATE_KEY < cosign.key
gh secret set COSIGN_PASSWORD       # la misma contraseña
```

Guarde `cosign.key` fuera del repositorio (el `.gitignore` ya ignora
`*.key`) y publique `cosign.pub` donde lo lean quienes despliegan. Para
verificar una imagen antes de desplegarla:

```bash
cosign verify --key cosign.pub --insecure-ignore-tlog=true \
  -a version=X.Y.Z ghcr.io/<owner>/<repo>:X.Y.Z
```

La firma lleva la versión del release como anotación (`version`), y
`-a version=X.Y.Z` la exige: sin eso, alguien con escritura en el registry
podría mover el tag `X.Y.Z` a una imagen anterior, también firmada, y la
verificación pasaría.

`--insecure-ignore-tlog` es necesario porque la firma no está en el log
público; la confianza viene de la clave.

Commitee `cosign.pub` en `deploy/cosign.pub`: `deploy/verify-images.sh`
(o `make deploy`) baja las imágenes del compose, verifica la firma de cada
imagen que no está fijada por digest y falla si una no la tiene, así que el
host despliega solo lo que publicó el release (ver la sección "Verificar las
firmas antes de desplegar" del RUNBOOK). El host necesita `cosign`, por
ejemplo con `go install github.com/sigstore/cosign/v3/cmd/cosign@v3.1.4` o
el binario de su
[release](https://github.com/sigstore/cosign/releases/tag/v3.1.4).

Para rotar la clave (ante una sospecha de exposición o, como mínimo, una vez
al año):

1. Genere el par nuevo con `cosign generate-key-pair` en un directorio fuera
   del repositorio y reemplace los dos *secrets* (`COSIGN_PRIVATE_KEY` y
   `COSIGN_PASSWORD`) como arriba: desde ahí, cada release firma con la
   clave nueva.
2. En **un solo commit**, renombre `deploy/cosign.pub` a
   `deploy/cosign-<año>.pub` (las versiones ya publicadas siguen firmadas con
   ella) y agregue el `cosign.pub` nuevo como `deploy/cosign.pub`. Así el
   repositorio nunca queda sin la clave que usa `deploy/verify-images.sh`.
3. Publique un release y despliéguelo. Hasta entonces, la versión desplegada
   está firmada con la clave anterior (ver el párrafo siguiente).
4. Borre la clave privada anterior.

`deploy/verify-images.sh` verifica con una sola clave, así que para volver a
una versión firmada con la clave anterior use
`COSIGN_PUBLIC_KEY=deploy/cosign-<año>.pub deploy/verify-images.sh`. Si la
clave anterior se expuso, no confíe en esas firmas: vuelva a publicar la
versión con un tag nuevo, firmado con la clave nueva.

`.github/workflows/ci.yaml` corre lint, tests, `govulncheck`, `gitleaks` y
Trivy en cada push a `main`, en cada pull request y cada lunes.
<!-- vogel:only begin -->

Dentro de `vogel` estos workflows no corren (GitHub solo lee el `.github/` de
la raíz del repositorio); el CI de `vogel` cubre el boilerplate.

## Cómo copiar este boilerplate para un servicio nuevo

La forma recomendada es `create-vogel-app` (ver el comienzo de este README):
hace todos estos pasos. A mano:

1. Copie `examples/boilerplate/` a un repositorio nuevo.
2. Quite el `replace` y fije una versión real de `vogel` (ver arriba).
3. Renombre el módulo (`go.mod`) al de su servicio.
4. Reemplace `internal/infrastructure/database/migrations/001_enable_pgcrypto.sql`
   por las migraciones reales de su dominio.
5. Los dotfiles ya vienen: `.gitignore`, `.golangci.yml`, `.gitleaks.toml` y
   `.dockerignore` (este último, en la raíz de la copia, deja fuera del build
   `.env`, `secrets/`, las claves y lo que no es código).
6. Cree su propio `.env` a partir de la sección "Arranque rápido" de este
   README.
7. Arranque un módulo de negocio nuevo siguiendo `docs/USAGE_GUIDE.md`.
8. Cambie los `image:` de `deploy/docker-compose.yaml` (hoy
   `ghcr.io/kafeiih/vogel-boilerplate*`) a los nombres que publica
   `release.yaml` (ver "Publicar imágenes y CI"), y borre el job del
   componente que no use (y su entrada y su línea `tag` en el job `publish`).

<!-- vogel:only end -->

## Agregar un módulo de negocio nuevo

`docs/USAGE_GUIDE.md` tiene el manual paso a paso, con un ejemplo completo
(`proveedor`): dominio, repositorio Postgres sobre `vogel/pgxtx`, comandos de
aplicación que auditan con `vogel/audit` (recordando pasar `audit.SourceHTTP`
o `audit.SourceWorker` según si el call site es un handler o un job), y un
handler HTTP con sus propias anotaciones Swagger.
<!-- vogel:component worker begin -->
`internal/application/jobs/` tiene jobs de referencia (`example.go`) para el
mismo patrón del lado del worker.
<!-- vogel:component worker end -->

## Documentación adicional

- `docs/USAGE_GUIDE.md` — cómo agregar un módulo de negocio nuevo, paso a paso.
- `docs/CERBOS_POLICIES.md` — cómo escribir políticas Cerbos para este boilerplate.
<!-- vogel:component bpms begin -->
- `docs/BPMS.md` — la base BPMS: el dominio de procesos, sus rutas,
  definiciones, efectos, guards, handlers y la regla de la transacción.
<!-- vogel:component bpms end -->
<!-- vogel:component cerbos begin -->
- `cerbos/README.md` — cómo se construye, valida y despliega la imagen del PDP
  con las políticas de `cerbos/policies`.
<!-- vogel:component cerbos end -->
- `deploy/RUNBOOK.md` — operación en producción.
- `docs/swagger.json` / `docs/swagger.yaml` — spec generada por `swag init`;
  regenerar con `make gen-docs` después de anotar un handler nuevo. La UI y
  la spec se sirven en `/swagger/` solo con `SWAGGER_ENABLED=true` (nunca
  en producción); sin esa variable la ruta no existe (404).
