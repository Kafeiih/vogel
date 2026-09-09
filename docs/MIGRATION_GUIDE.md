# Guía de migración a vogel

Esta guía documenta cómo migrar `go-crucible` y `go-licencias` de su código
duplicado local a `github.com/kafeiih/vogel` (hoy en `v0.2.0`). Es un manual de
ejecución: cada sección indica qué archivo local se borra, qué import lo
reemplaza, y el diff exacto de las llamadas cuya firma cambió.

No cubre pasos aspiracionales. Cada afirmación sobre código local fue
verificada leyendo los archivos de `go-crucible` y `go-licencias` en el
momento de escribir esta guía (2026-09-07); si el código de la app cambió
después, revalidá las rutas antes de aplicar un paso.

## Antes de empezar

### 1. Configurar acceso al repo privado

`vogel` vive en un repositorio privado. Configurá `GOPRIVATE` antes de
cualquier `go get`:

```bash
go env -w GOPRIVATE=github.com/kafeiih/*
```

Esto evita que `go mod download` intente resolver el módulo contra el proxy
público de Go y el checksum database público (`sum.golang.org`), que no
tienen visibilidad de un repo privado.

### 2. Agregar la dependencia

Desde la raíz de `go-crucible` o `go-licencias`:

```bash
go get github.com/kafeiih/vogel@v0.2.0
```

Esto agrega la entrada a `go.mod` y `go.sum`. `vogel` no publica todos sus
paquetes en un único import — cada paquete de la tabla de correspondencia de
abajo se importa por separado (p. ej. `github.com/kafeiih/vogel/audit`,
`github.com/kafeiih/vogel/audit/httpx`), como corresponde a un módulo Go
multi-paquete.

### 3. Versión de Go

`vogel` declara `go 1.25.7` en su `go.mod`. Ambos consumidores son
compatibles sin cambios:

| App | `go` en `go.mod` | Compatible |
|---|---|---|
| `go-licencias` | `1.25.7` | Sí — versión idéntica |
| `go-crucible` | `1.26` | Sí — Go es retrocompatible con módulos importados que declaran una versión de lenguaje igual o menor a la del consumidor |

No hace falta tocar la directiva `go` de ninguna de las dos apps para
adoptar `vogel`.

## Tabla de correspondencia

Dos columnas: qué archivo local se borra (o dónde vive hoy la lógica
duplicada) a la izquierda, qué paquete de `vogel` lo reemplaza a la derecha.
Se cubren los 27 paquetes de `vogel`. Los marcados **"sin equivalente
local"** son capacidad nueva, no reemplazan nada existente.

| Paquete de `vogel` | Archivo local en `go-crucible` | Archivo local en `go-licencias` |
|---|---|---|
| `stringutil` | `pkg/stringutil/normalize.go` (nombre de paquete ya coincide) — `ParseFlexibleDate` **no tiene equivalente genérico**, solo `internal/infrastructure/mp/date_parser.go:ParseMPDate` (5 layouts distintos, específico de MP) | `pkg/stringutil/normalize.go` + `pkg/stringutil/dates.go:ParseFlexibleDate` (equivalente casi 1:1). También existe `pkg/stringutil/monto.go:ParseMonto` (parsing de montos CLP) sin equivalente en vogel — queda local |
| `reqctx` | `internal/interfaces/http/middleware/request_info.go` (vive en `middleware`, no en paquete propio) | `internal/interfaces/http/middleware/request_info.go` (idéntica ubicación) |
| `logger` | `pkg/logger/logger.go` | `pkg/logger/logger.go` |
| `storage` + `storage/s3` | `internal/domain/storage/storage.go` + `internal/infrastructure/storage/s3.go` (además `internal/infrastructure/storage/registry.go` — `StorageRegistry` local, vogel lo cubre con `storage/s3.StorageRegistry`) | `internal/domain/storage/storage.go` + `internal/infrastructure/storage/s3.go` (+ `registry.go`) |
| `notification` + `notification/smtp` + `notification/sendgrid` | `internal/domain/notification/notification.go` + `internal/infrastructure/notification/{smtp,sendgrid}.go` | `internal/domain/notification/notification.go` + `internal/infrastructure/notification/{smtp,sendgrid}.go` |
| `httpx/response` | `internal/interfaces/http/response/{success.go,error.go}` (tiene funciones extra: `JSONWithMessage`, `JSONFail`, `JSONListWithMeta` — quedan locales, vogel no las expone) | `internal/interfaces/http/response/{success.go,error.go}` (mismas funciones extra locales) |
| `httpx/middleware` | `internal/interfaces/http/middleware/{recovery,ratelimit,security,logger,metrics,auth,authz}.go` | `internal/interfaces/http/middleware/{recovery,ratelimit,security,logger,metrics,auth,authz}.go` |
| `auth` | **sin puerto local separado** — ver nota abajo | **sin puerto local separado** — ver nota abajo |
| `auth/zitadel` | Lógica de adaptador embebida dentro de `internal/interfaces/http/middleware/auth.go` (no hay archivo de infraestructura separado) | `internal/infrastructure/auth/zitadel.go` (`NewZitadelAuthorizer`) |
| `authz` | `internal/domain/authz/authz.go` | `internal/domain/authz/authz.go` |
| `authz/cerbos` | `internal/infrastructure/authz/cerbos.go` | `internal/infrastructure/authz/cerbos.go` |
| `pgxtx` | `internal/infrastructure/repository/tx.go` (además expone `AuditDB(ctx)`/`DB(ctx)`, wrappers específicos que quedan locales) | `internal/infrastructure/repository/tx.go` |
| `audit` | `internal/domain/audit/audit.go` + `internal/application/audit/recorder.go` | `internal/domain/audit/audit.go` + `internal/application/audit/recorder.go` |
| `audit/postgres` | `internal/infrastructure/repository/audit_postgres.go` | `internal/infrastructure/repository/audit_postgres.go` (origen del port — ver README de vogel) |
| `audit/migrations` | `internal/infrastructure/database/migrations/001_create_audit_log.sql` | `internal/infrastructure/database/migrations/001_create_audit_log.sql` |
| `audit/httpx` | `internal/application/audit/{dto.go,queries.go}` + `internal/interfaces/http/handler/audit_handler.go` | `internal/application/audit/{dto.go,queries.go}` + `internal/interfaces/http/handler/audit_handler.go` |
| `request` | `internal/interfaces/http/request/{json.go,validate.go}` (`Validator` local tiene funciones extra: `Int64Query`, `BoolQuery`, `DecimalQuery`, `IntQueryRange` — quedan locales) | `internal/interfaces/http/request/{json.go,validate.go}` |
| `config` | **sin equivalente 1:1** — `internal/infrastructure/config/config.go` usa helpers privados (`getEnv`, `requireEnv`, `parseInt32`, ...), no funciones exportadas reusables como `vogel/config` | **sin equivalente 1:1** — mismo patrón, helpers privados en `internal/infrastructure/config/config.go` |
| `postgres` | `internal/infrastructure/database/postgres.go` (`NewPostgresPool`) + `internal/infrastructure/database/{slow_query_tracer.go,metrics.go}` | `internal/infrastructure/database/postgres.go` (`NewPostgresPool`) + `internal/infrastructure/database/{slow_query_tracer.go,metrics.go}` |
| `migrate` | `internal/infrastructure/database/migrate/{runner.go,logger.go}` | `internal/infrastructure/database/migrate/{runner.go,logger.go}` |
| `worker` | `internal/domain/worker/worker.go` + `internal/infrastructure/worker/{river.go,schema.go}` | `internal/domain/worker/worker.go` + `internal/infrastructure/worker/{river.go,schema.go}` |
| `workflow` | **sin equivalente local** — motor nuevo | **sin equivalente local** — motor nuevo |
| `workflow/postgres` | **sin equivalente local** — motor nuevo | **sin equivalente local** — motor nuevo |
| `workflow/migrations` | **sin equivalente local** — motor nuevo | **sin equivalente local** — motor nuevo |

**Nota sobre `auth`:** ninguno de los dos consumidores tiene hoy un puerto
`Authenticator` de dominio separado del transporte HTTP. Ambos acoplan el SDK
de Zitadel directamente en `internal/interfaces/http/middleware/auth.go`
(`ZitadelAuth`/`zitadelAuthJSON` en go-crucible, lógica equivalente en
go-licencias). Adoptar `vogel/auth` no es borrar un archivo duplicado: es
introducir una capa de abstracción que hoy no existe. Ver la sección
«`auth` + `auth/zitadel`» más abajo para el patrón concreto.

## Orden de migración recomendado

De menor a mayor riesgo. El criterio: primero lo que no tiene estado ni
cambia de firma (un `go build` alcanza para validarlo), después lo que
cambia de firma pero sigue sin tocar una base de datos en producción
(afecta compilación y textos, no datos persistidos), y al final lo que toca
esquema/migraciones/tablas de versión (un error ahí es más caro de revertir).

1. **Sin estado, sin cambio de firma** — `stringutil`, `config`,
   `httpx/response`, `request`. Reemplazo mecánico de import; el compilador
   marca cualquier desajuste.
2. **Relocación de paquete, sin cambio de firma** — `reqctx`, `logger`,
   `pgxtx`. Cambia de dónde se importa la función, no cómo se llama.
3. **Adaptadores externos con constructor estable** — `storage` +
   `storage/s3`, `notification` + `notification/smtp` + `notification/sendgrid`.
   Firma de los puertos idéntica; solo cambia el import.
4. **Middleware sin cambio de firma** — `httpx/middleware.Recovery`,
   `RateLimitJSON`, `SecurityHeaders`, `StructuredLogger`. Reemplazo directo.
5. **Middleware con cambio de firma (constructor ahora falla)** —
   `httpx/middleware.Metrics`→`NewMetrics` (ahora retorna `error`),
   `postgres.NewPoolMetricsCollector` (ídem). Rompe compilación hasta manejar
   el error; no hay riesgo de datos.
6. **Auth/authz — requiere reestructurar, no solo reemplazar** — `auth` +
   `auth/zitadel`, `authz` + `authz/cerbos`, `httpx/middleware.Authenticate` /
   `RequirePermission` / `AuthMessages`. Cambia comportamiento observable
   (mapeo de errores a status HTTP, mensajes). Sin tocar la base de datos,
   pero sí el contrato HTTP hacia el cliente — probar contra un IdP/PDP de
   staging antes de producción.
7. **`audit` — el cambio de firma más grande, sin tocar todavía el esquema** —
   `audit` (`Recorder.Record` con `Source` obligatorio, `Queries`
   desaparece), `audit/httpx` (`Messages`/`WithMessages`). Rompe todos los
   call sites de `Record`; se corrige con el compilador, pero conviene
   revisar cada sitio a mano porque el argumento nuevo (`Source`) no tiene
   valor por defecto que el compilador pueda inferir.
8. **Tocan base de datos — máximo riesgo, requieren coordinación de
   despliegue** — `postgres` (`NewPool`), `migrate`, `audit/postgres`,
   `audit/migrations`, `worker`. Un error acá corrompe estado persistido o
   dispara colisiones de tabla de versión de goose. Migrar en un entorno de
   staging con una base de datos real antes de producción; ver
   «Verificación posterior».
9. **`workflow` (opcional, no bloquea nada anterior)** — se adopta cuando la
   app decida usar el motor, no como parte de esta migración.

## Cambios de firma que rompen

Esta es la sección más importante del documento. Cada uno de estos cambios
hace que el código actual **no compile** hasta corregirlo, o cambia
comportamiento observable en producción. Revisá esta lista completa antes de
tocar código.

### 1. `worker.NewRiverQueue` — opción funcional en vez de parámetro posicional

- **go-licencias no cambia ninguna llamada.** Su firma actual ya coincide:

  ```go
  // Antes y después, sin cambios:
  queue, err := infraWorker.NewRiverQueue(pool, workers, cfg.Worker, appLogger.Logger)
  ```

  Solo cambia el import: `infraWorker` (`internal/infrastructure/worker`) →
  `github.com/kafeiih/vogel/worker`. El tipo del tercer argumento pasa de
  `config.WorkerConfig` (local) a `worker.Config` (de vogel, mismos 3
  campos) — ver conversión en la sección «`worker`» más abajo.

- **go-crucible reemplaza el parámetro posicional `periodicJobs` por
  `worker.WithPeriodicJobs(...)`.**

  Antes (`cmd/worker/main.go:106-131`):

  ```go
  mpPeriodicJob := river.NewPeriodicJob(
      river.PeriodicInterval(1*time.Hour),
      func() (river.JobArgs, *river.InsertOpts) {
          return jobs.MPSyncJobArgs{}, nil
      },
      &river.PeriodicJobOpts{RunOnStart: false},
  )

  periodicJobs := []*river.PeriodicJob{mpPeriodicJob}
  queue, err := infraWorker.NewRiverQueue(pool, workers, periodicJobs, cfg.Worker, appLogger.Logger)
  ```

  Después:

  ```go
  mpPeriodicJob := river.NewPeriodicJob(
      river.PeriodicInterval(1*time.Hour),
      func() (river.JobArgs, *river.InsertOpts) {
          return jobs.MPSyncJobArgs{}, nil
      },
      &river.PeriodicJobOpts{RunOnStart: false},
  )

  queue, err := worker.NewRiverQueue(
      pool, workers,
      worker.Config{
          Schema:            cfg.Worker.Schema,
          DefaultMaxWorkers: cfg.Worker.DefaultMaxWorkers,
          Queues:            cfg.Worker.Queues,
      },
      appLogger.Logger,
      worker.WithPeriodicJobs([]*river.PeriodicJob{mpPeriodicJob}),
  )
  ```

### 2. `Queries` desaparece — el handler usa `audit.Repository` directamente

Ambos consumidores tienen hoy `internal/application/audit/queries.go` con
`type Queries struct{ repo domainAudit.Repository }`, `NewQueries(repo)`,
`GetByID`, `List`. `vogel/audit/httpx.Handler` no recibe un `Queries`: recibe
`audit.Repository` directamente, porque `Queries` no era más que
`repo.GetByID` seguido de `toResponse` — ver la sección «`audit/httpx`» más
abajo para el diff completo del wiring del handler. Borrar
`internal/application/audit/queries.go` (y `dto.go`, cuyo `ToResponse` pasa a
`audit/httpx.ToResponse`) en ambas apps.

### 3. Mensajes de `auth`/`authz` salen a `AuthMessages` / `WithAuthMessages`

`httpx/middleware.Authenticate` y `RequirePermission` ya no llevan texto en
español incrustado: reciben `opts ...AuthOption`, y cada app pasa
`middleware.WithAuthMessages(middleware.AuthMessages{...})` para fijar sus
tres textos (`Unauthorized`, `Forbidden`, `ServiceUnavailable`). Los textos
españoles actuales — **idénticos en ambas apps** — son:

| Campo `AuthMessages` | Texto actual en ambas apps | Dónde vive hoy |
|---|---|---|
| `Unauthorized` | `"Token inválido o expirado"` (falla de auth) / `"Autenticación requerida"` (sin principal en `RequirePermission`) | `middleware/auth.go` y `middleware/authz.go` respectivamente — **vogel unifica ambos casos en un solo texto** `Unauthorized`, hay que elegir uno |
| `Forbidden` | `"No tenés permisos para realizar esta acción"` (denegación genuina en `RequirePermission`) | `middleware/authz.go` |
| `ServiceUnavailable` | **no existe hoy** — ninguna de las dos apps devolvía 503 antes de esta migración (ver punto 13 del README de vogel: go-crucible devolvía 500, go-licencias 403 por DEC-08) | texto nuevo, a definir por cada app |

Ejemplo de wiring en cada app (idéntico en ambas, dado que el texto actual
coincide):

```go
authMessages := middleware.WithAuthMessages(middleware.AuthMessages{
    Unauthorized:       "Autenticación requerida",
    Forbidden:           "No tenés permisos para realizar esta acción",
    ServiceUnavailable: "Servicio no disponible temporalmente",
})

router.Use(middleware.Authenticate(authenticator, logger, authMessages))
router.With(middleware.RequirePermission(checker, "invoices:invoice", "read", logger, authMessages))
```

**Divergencia de comportamiento a tener en cuenta:** hoy `middleware/auth.go`
devuelve 403 con `"No tenés permisos para acceder a este recurso"` para
*cualquier* error de introspección que no sea "no autenticado" — en vogel,
`httpx/middleware.Authenticate` solo devuelve 403 cuando el `Authenticator`
señala explícitamente `auth.ErrForbidden` (algo que `auth/zitadel` casi nunca
hace, según su propio comentario de documentación); todo lo demás colapsa a
401. Un 403 de auth hoy puede convertirse en 401 después de migrar. Revisar
si algún cliente depende de esa distinción.

### 4. Mensajes de `audit/httpx` salen a `Messages` / `WithMessages`

Mismo patrón, para el handler de auditoría. Los tres textos actuales —
**también idénticos en ambas apps**:

| Campo `Messages` | Texto actual en ambas apps |
|---|---|
| `ListFailed` | `"No se pudo listar las entradas de auditoría"` |
| `NotFound` | `"Entrada de auditoría no encontrada"` |
| `GetFailed` | `"No se pudo obtener la entrada de auditoría"` |

```go
handler, err := httpx.NewHandler(repo, logger, httpx.WithMessages(httpx.Messages{
    ListFailed: "No se pudo listar las entradas de auditoría",
    NotFound:   "Entrada de auditoría no encontrada",
    GetFailed:  "No se pudo obtener la entrada de auditoría",
}))
```

### 5. `Authenticator` recibe un token, no un `*http.Request`

Ninguna de las dos apps tiene hoy un puerto `Authenticator` separado (ver la
nota en la tabla de correspondencia), así que no hay una firma previa a
comparar 1:1 — pero al introducir `vogel/auth`, la extracción del bearer
token pasa a ser responsabilidad exclusiva de `httpx/middleware.Authenticate`
(vía `extractBearerToken`, RFC 6750 §2.1), y el adaptador
`auth/zitadel.Authenticator.Authenticate(ctx, token string)` solo ve el token
desnudo — nunca el `*http.Request`. Cualquier lógica de extracción de header
propia de `middleware/auth.go` (parseo de `Authorization`, chequeo de
scheme `Bearer`) queda reemplazada por el propio middleware de vogel; no hay
que portarla.

### 6. `NewMetrics` / `NewPoolMetricsCollector` ahora devuelven `error`

- **go-crucible**: `middleware.Metrics` hoy es una función middleware directa
  (`func Metrics(next http.Handler) http.Handler`), sin constructor, con
  `prometheus.MustRegister` a nivel de paquete (`init()` o variables
  package-level). `NewMetrics` de vogel es un **constructor** que devuelve
  `(*Metrics, error)`:

  ```go
  // Antes:
  router.Use(middleware.Metrics)

  // Después:
  m, err := middleware.NewMetrics(prometheus.DefaultRegisterer)
  if err != nil {
      return fmt.Errorf("creating metrics middleware: %w", err)
  }
  router.Use(m.Middleware)
  ```

- **`postgres.NewPoolMetricsCollector`**: hoy es `NewPoolMetricsCollector(pool) *PoolMetricsCollector`
  (sin `reg`, sin error, `prometheus.MustRegister` interno). En vogel:

  ```go
  // Antes:
  collector := database.NewPoolMetricsCollector(pool)
  prometheus.MustRegister(collector)

  // Después:
  collector, err := postgres.NewPoolMetricsCollector(pool, prometheus.DefaultRegisterer)
  if err != nil {
      return fmt.Errorf("creating pool metrics collector: %w", err)
  }
  // ya queda registrado adentro de NewPoolMetricsCollector — no llamar MustRegister de nuevo
  ```

### 7. `Recorder.Record` exige `Source` como argumento posicional

Firma actual en ambas apps (`internal/application/audit/recorder.go`):

```go
func (r *Recorder) Record(ctx context.Context, db DBTX, action Action, resourceType, resourceID string, opts ...Option) error
```

Firma nueva (`vogel/audit`):

```go
func (r *Recorder) Record(ctx context.Context, db pgxtx.DBTX, source Source, action Action, resourceType, resourceID string, opts ...Option) error
```

`Source` no tiene valor por defecto — cada call site debe declarar
explícitamente `audit.SourceHTTP` o `audit.SourceWorker`:

```go
// Antes:
recorder.Record(ctx, db, audit.ActionCreate, "invoice", inv.ID.String(),
    audit.WithSubject(inv),
)

// Después — call site dentro de un handler HTTP:
recorder.Record(ctx, db, audit.SourceHTTP, audit.ActionCreate, "invoice", inv.ID.String(),
    audit.WithSubject(inv),
)

// Después — call site dentro de un job/worker:
recorder.Record(ctx, db, audit.SourceWorker, audit.ActionCreate, "invoice", inv.ID.String(),
    audit.WithSubject(inv),
)
```

Ninguna de las dos apps tiene hoy constantes tipadas `SourceHTTP`/
`SourceWorker` — `Entry.Source` es `string` plano con default `"http"` (o
`audit.WithSource("http")` como opción). El compilador marca cada call site
que falte actualizar porque el nuevo parámetro es posicional y obligatorio,
no una `Option`.

### 8. `audit.Filters.ResourceID` es `*uuid.UUID`

- **go-licencias no cambia nada**: su `Filters` local ya tiene
  `ResourceID *uuid.UUID`, idéntico al de vogel.
- **go-crucible gana el campo**: su `Filters` local hoy NO tiene
  `ResourceID`. Es una mejora gratuita — el filtro por recurso llega solo por
  adoptar `vogel/audit`. Si go-crucible documenta `/audit` con Swagger propio
  (ver sección «`audit/httpx`»), agregar la anotación:

  ```go
  //	@Param			resource_id			query		string	false	"Filtrar por ID de recurso (formato UUID)"	Format(uuid)
  ```

  (Texto tomado literal de la anotación ya existente en
  `go-licencias/internal/interfaces/http/handler/audit_handler.go:39`, para
  mantener consistencia entre ambas apps.)

### 9. `migrate.Up` y sus hermanas exigen un `fs.FS` explícito

Los tres repos de origen (`go-bluprint`, `go-crucible`, `go-licencias`)
comparten el mismo `runner.go`, en el que `Up`, `Down`, `UpTo`, `UpByOne` y
`Status` leían un `embed.FS` a nivel de paquete, con una variante `UpFS`
aparte para el caso en que hubiera que pasar otro. `vogel/migrate` no embebe
migración alguna — no posee ninguna — así que el `fs.FS` es un argumento
posicional obligatorio en todas, y `UpFS` desaparece:

```go
// Antes
err := migrate.Up(ctx, dbURL)
err := migrate.UpFS(ctx, dbURL, otroFS)
err := migrate.Status(ctx, dbURL, os.Stdout)

// Ahora
err := migrate.Up(ctx, dbURL, appMigrationsFS)
err := migrate.Up(ctx, dbURL, otroFS)
err := migrate.Status(ctx, dbURL, appMigrationsFS, os.Stdout)
```

Es un cambio que el compilador marca en cada llamada, así que no se puede
pasar por alto: la lista de errores es la lista de sitios a corregir.

**Caso borde**: si el directorio de migraciones propias de la aplicación
contenía ÚNICAMENTE la migración de `audit_log` (la que `audit/migrations`
reemplaza), borrarla lo deja vacío y el `//go:embed *.sql` deja de compilar
por no encontrar archivos. No afecta a `go-crucible` (25 migraciones propias)
ni a `go-licencias` (12), pero sí afectó a `go-bluprint`, que tenía una sola.
La salida es dejar una migración propia real, no un archivo vacío.

## Secciones por paquete

Cada sección: qué se borra, el import nuevo, y el diff de llamada cuando la
firma cambió. Los paquetes cuya migración es un simple cambio de import
(sin diff de llamada) se agrupan.

### `stringutil`

- **go-licencias**: borrar `pkg/stringutil/normalize.go` y
  `pkg/stringutil/dates.go`. `pkg/stringutil/monto.go` (`ParseMonto`) no
  tiene equivalente en vogel — queda local.
- **go-crucible**: borrar `pkg/stringutil/normalize.go`. **No hay
  `ParseFlexibleDate` equivalente que migrar**: `internal/infrastructure/mp/date_parser.go:ParseMPDate`
  prueba 5 layouts distintos (incluye `RFC3339`) que no coinciden con los 3
  de `vogel/stringutil.ParseFlexibleDate` (`DD/MM/YYYY`, `DD-MM-YYYY`,
  `YYYY-MM-DD`) — evaluar aparte si conviene reescribir `ParseMPDate` sobre
  la función de vogel o dejarlo como está.

```go
import "github.com/kafeiih/vogel/stringutil"

normalized := stringutil.Normalize(name)
t, err := stringutil.ParseFlexibleDate(dateStr) // solo go-licencias
```

Sin cambio de firma.

### `config`

No hay reemplazo de archivo 1:1: ambas apps arman `Load() (*Config, error)`
con helpers privados (`getEnv`, `requireEnv`, `parseInt32`, `parseDuration`,
`parseSlice`, `parseBoolDefault`). Migrar significa reescribir esos helpers
privados para usar las funciones exportadas de `vogel/config`:

```go
import "github.com/kafeiih/vogel/config"

// Antes (helper privado):
func getEnv(key, def string) string { ... }

// Después:
port := config.String("PORT", "8080")
debug := config.Bool("DEBUG", false)
maxConns, err := config.Int32("DB_MAX_CONNS", 10)
timeout, err := config.Duration("DB_TIMEOUT", 5*time.Second)
apiKey, err := config.Require("SENDGRID_API_KEY")
```

Los `Config` struct de la app (`AppConfig`, `DatabaseConfig`, etc.) **no se
portan** — vogel deliberadamente no define structs de configuración de
aplicación (ver README, sección `config`). Solo se reemplazan los helpers de
lectura/parseo dentro de `Load()`.

### `httpx/response`

- Ambas apps: borrar el uso de `internal/interfaces/http/response/{success.go,error.go}`
  para las funciones que vogel cubre. Las funciones extra locales
  (`JSONWithMessage`, `JSONFail`, `JSONListWithMeta`) no tienen equivalente
  en vogel — quedan locales o se reescriben sobre `response.JSON`/`response.JSONList`.

```go
import "github.com/kafeiih/vogel/httpx/response"

response.JSON(w, http.StatusOK, data)
response.Error(w, r, http.StatusNotFound, response.CodeNotFound, "not found")
response.ValidationError(w, r, fieldErrors)
response.JSONList(w, http.StatusOK, items, total, limit, offset)
```

Sin cambio de firma en las funciones que sí están portadas.

### `request`

- Ambas apps: borrar `internal/interfaces/http/request/{json.go,validate.go}`.
  Las funciones extra del `Validator` local (`Int64Query`, `BoolQuery`,
  `DecimalQuery`, `IntQueryRange` en go-crucible) no tienen equivalente en
  vogel — si se usan, quedan en un `Validator` propio de la app que envuelve
  o extiende al de vogel.

```go
import "github.com/kafeiih/vogel/request"

if err := request.JSON(w, r, &input); err != nil {
    return
}

v := request.NewValidator()
id := v.UUIDParam(r, "id")
limit := v.IntQuery(r, "limit", 20)
if v.HasErrors() {
    v.WriteErrors(w, r)
    return
}
```

Sin cambio de firma. Nota aparte: go-licencias ya tiene el límite de bytes
correcto (`1_048_576`, sin el typo `1_048_578` mencionado en el punto 5 del
README de vogel) — no requiere ningún ajuste por ese lado.

### `reqctx`

Reemplaza `internal/interfaces/http/middleware/request_info.go` en ambas
apps (mismo archivo y ubicación en ambas). Cambia el paquete de origen, no
la forma de uso — pero el request ID pasa a vivir en `reqctx`, no en una
clave que posee `logger` o `middleware`:

```go
import "github.com/kafeiih/vogel/reqctx"

// Antes: RequestInfoFromContext(ctx) definido localmente en middleware
info, ok := reqctx.RequestInfoFromContext(ctx)
id := reqctx.RequestIDFromContext(ctx)
```

El middleware que escribe estos valores pasa a ser
`httpx/middleware.RequestContext` (ver sección de middleware más abajo), que
reemplaza al `RequestInfoMiddleware` local.

### `logger`

Reemplaza `pkg/logger/logger.go` en ambas apps. Cambio de comportamiento
relevante: el `logger` local llama a `middleware.GetReqID(ctx)` de **chi**
directamente (`pkg/logger/logger.go:64`, función `getRequestID`); el
`logger` de vogel lee el ID de request desde `reqctx` en su lugar (no conoce
chi). Esto obliga a que `httpx/middleware.RequestContext` esté montado
*antes* de cualquier punto donde se llame `Logger.WithContext` — de lo
contrario el ID de request no aparece en las líneas de log.

```go
import "github.com/kafeiih/vogel/logger"

log := logger.New(env, os.Stdout)
log = log.WithContext(ctx) // ahora lee reqctx, no chi directamente
```

### `pgxtx`

Reemplaza `internal/infrastructure/repository/tx.go` en ambas apps. Los
wrappers específicos locales (`AuditDB(ctx)`, `DB(ctx)` en go-crucible) no
tienen equivalente directo — son atajos sobre `pgxtx.DBFromContext`, se
reescriben como funciones propias de la app que llaman a `pgxtx.DBFromContext`
en vez de a la función privada local.

```go
import "github.com/kafeiih/vogel/pgxtx"

txManager := pgxtx.NewPgxTxManager(pool)
err := txManager.WithTx(ctx, func(txCtx context.Context) error {
    db := pgxtx.DBFromContext(txCtx, pool)
    return repo.Create(txCtx, db, entity)
})
```

Sin cambio de firma. Mejora incluida (punto 2 del README de vogel):
`WithTx` ahora revierte la transacción ante un panic dentro del callback en
vez de dejarla filtrada.

### `storage` + `storage/s3`

Reemplaza `internal/domain/storage/storage.go` +
`internal/infrastructure/storage/{s3.go,registry.go}` en ambas apps.

```go
import (
    "github.com/kafeiih/vogel/storage"
    "github.com/kafeiih/vogel/storage/s3"
)

s3Storage, err := s3.NewS3Storage(s3.Config{
    Bucket: cfg.Storage.Bucket,
    Region: cfg.Storage.Region,
    // ...
}, logger)
```

Sin cambio de firma — `Config`, `Upload`, `Download`, `Delete`,
`PresignedGetURL`, `PresignedPutURL` coinciden exactamente con lo local en
ambas apps.

### `notification` + `notification/smtp` + `notification/sendgrid`

Reemplaza `internal/domain/notification/notification.go` +
`internal/infrastructure/notification/{smtp.go,sendgrid.go}` en ambas apps.

```go
import (
    "github.com/kafeiih/vogel/notification"
    "github.com/kafeiih/vogel/notification/smtp"
)

notifier, err := smtp.NewSMTPNotifier(smtp.Config{
    Host: cfg.SMTP.Host,
    Port: cfg.SMTP.Port,
    // ...
}, defaultFrom, logger)
```

Sin cambio de firma. `notification/smtp` documenta explícitamente el
dial-per-`Send` (una conexión nueva por envío) en vez de mantener un pool —
mismo comportamiento que el `go-mail` local ya tenía, ahora documentado en
lugar de contradicho por el ADR original (punto 3 del README de vogel).

### `httpx/middleware` — sin cambio de firma

`Recovery`, `RateLimitJSON`, `SecurityHeaders`, `StructuredLogger` se
reemplazan sin diff de llamada:

```go
import "github.com/kafeiih/vogel/httpx/middleware"

router.Use(middleware.Recovery(logger))
router.Use(middleware.RateLimitJSON())
router.Use(middleware.SecurityHeaders)
router.Use(middleware.StructuredLogger(logger.Logger))
router.Use(middleware.RequestContext) // reemplaza request_info.go local
```

`RequestContext` reemplaza directamente al `RequestInfoMiddleware` local —
ver sección `reqctx` arriba.

### `httpx/middleware.NewMetrics`

Ver «Cambios de firma que rompen», punto 6.

### `auth` + `auth/zitadel`

No hay un archivo local que borrar 1:1 (ver nota en la tabla de
correspondencia) — hay que **extraer** la lógica de adaptador que hoy vive
mezclada dentro del middleware:

- go-crucible: la lógica de introspección Zitadel vive dentro de
  `internal/interfaces/http/middleware/auth.go` (`zitadelAuthJSON`,
  `UserFromContext`, `extractOrgID`, `extractRoles`). Se reemplaza
  completamente por `auth/zitadel.Authenticator` + `httpx/middleware.Authenticate`.
- go-licencias: ya tiene un archivo de infraestructura separado,
  `internal/infrastructure/auth/zitadel.go` (`NewZitadelAuthorizer`), más
  cercano en forma al adaptador de vogel — pero tampoco implementa el puerto
  `auth.Authenticator`, solo expone el `*authorization.Authorizer` crudo del
  SDK.

```go
import (
    "github.com/kafeiih/vogel/auth/zitadel"
    "github.com/kafeiih/vogel/httpx/middleware"
)

authenticator, err := zitadel.New(ctx, zitadel.Config{
    Issuer:   cfg.Zitadel.Issuer,
    ClientID: cfg.Zitadel.ClientID,
})
if err != nil {
    return fmt.Errorf("creating zitadel authenticator: %w", err)
}

router.Use(middleware.Authenticate(authenticator, logger.Logger, authMessages))
```

`UserFromContext` local se reemplaza por `auth.FromContext(ctx)` (retorna
`*auth.Principal{UserID, Username, Roles}`, no el `UserContext` local con
`Email`, `Subject`, `OrgID` — esos tres campos no tienen equivalente en
`auth.Principal` y se pierden si algún handler los usa; revisar sitios de
uso de `UserContext.Email`/`.Subject`/`.OrgID` antes de migrar). Borrar
`extractOrgID` — la multi-tenencia por `org_id` fue eliminada en vogel por
no haber funcionado nunca (punto 11 del README de vogel).

### `authz` + `authz/cerbos`

Reemplaza `internal/domain/authz/authz.go` +
`internal/infrastructure/authz/cerbos.go` en ambas apps (mismas rutas en
ambas). `Checker.IsAllowed` conserva su firma exacta; el CONSTRUCTOR cambia
de nombre y pasa a recibir un struct de configuración:

```go
import (
    "github.com/kafeiih/vogel/authz"
    "github.com/kafeiih/vogel/authz/cerbos"
    "github.com/kafeiih/vogel/httpx/middleware"
)

// Antes: NewCerbosChecker(host string, useTLS bool) (*CerbosChecker, error)
checker, err := NewCerbosChecker(cerbosHost, useTLS)

// Ahora: New(cfg Config) (*Checker, error), con Config{Host, UseTLS}
checker, err := cerbos.New(cerbos.Config{Host: cerbosHost, UseTLS: useTLS})

router.With(middleware.RequirePermission(checker, "invoices:invoice", "read", logger.Logger, authMessages))
```

Sin cambio de firma en `Checker.IsAllowed`. Cambia el mapeo de errores del
checker a HTTP — ver el punto siguiente y «Cambios de firma que rompen»,
punto 3: go-crucible mapeaba un error del checker a 500
(`"Error en la verificación de permisos"`), go-licencias a 403 (DEC-08,
`"No tenés permisos para realizar esta acción"`); ambos pasan a 503.

### `audit`

Reemplaza `internal/domain/audit/audit.go` +
`internal/application/audit/recorder.go` en ambas apps. Ver «Cambios de
firma que rompen», puntos 2, 7 y 8, para el diff completo de `Record` y
`Filters`.

```go
import "github.com/kafeiih/vogel/audit"

recorder := audit.NewRecorder(auditRepo)
err := recorder.Record(ctx, db, audit.SourceHTTP, audit.ActionCreate, "invoice", inv.ID.String(),
    audit.WithSubject(inv),
)
```

### `audit/postgres`

Reemplaza `internal/infrastructure/repository/audit_postgres.go` en ambas
apps.

```go
import "github.com/kafeiih/vogel/audit/postgres"

repo := postgres.NewRepository(pool)
```

Cambio relevante para go-crucible: `buildWhereClause` local no filtra por
`resource_id` porque su `Filters` no tiene ese campo — al adoptar
`vogel/audit/postgres.Repository`, el filtro por `resource_id` funciona
automáticamente sin escribir SQL adicional (viene con el paquete).

### `audit/migrations`

Reemplaza `internal/infrastructure/database/migrations/001_create_audit_log.sql`
en ambas apps — el contenido es idéntico byte a byte entre go-bluprint,
go-crucible y go-licencias (según el README de vogel), así que no hay
riesgo de una migración de esquema divergente. Nota: la migración de
go-crucible habilita la extensión `pg_trgm` al tope del archivo — verificar
que `audit/migrations.FS()` de vogel también la incluye antes de retirar el
archivo local, para no perder esa dependencia si otra migración de
go-crucible la asume ya habilitada.

Ver «Ejecutar las migraciones de la librería junto a las de la aplicación»
en el README de vogel para el patrón completo con `migrate.Options.TableName`.

### `audit/httpx`

Reemplaza `internal/application/audit/{dto.go,queries.go}` +
`internal/interfaces/http/handler/audit_handler.go` en ambas apps. Borra
`Queries` por completo (ver «Cambios de firma que rompen», punto 2).

Si la app **no** necesita mantener sus propias anotaciones de Swagger sobre
`/audit`, monta el `Handler` de vogel directamente:

```go
import "github.com/kafeiih/vogel/audit/httpx"

handler, err := httpx.NewHandler(auditRepo, logger.Logger, httpx.WithMessages(httpx.Messages{
    ListFailed: "No se pudo listar las entradas de auditoría",
    NotFound:   "Entrada de auditoría no encontrada",
    GetFailed:  "No se pudo obtener la entrada de auditoría",
}))

router.Get("/audit", handler.List)
router.Get("/audit/{id}", handler.GetByID)
```

Si la app **sí** necesita sus anotaciones de Swagger propias (el caso de
ambos repos hoy — go-crucible tiene anotaciones completas en `List` y
`GetByID`, go-licencias también, incluyendo `@Param resource_id` en
go-licencias) — **no montés el `Handler` de vogel**: las anotaciones de
swaggo se leen de los comentarios sobre la función handler concreta de la
app, así que un `Handler` importado de otro módulo no puede llevarlas.
Escribí un handler propio delgado que reutiliza el parseo y el mapeo de
vogel:

```go
package handler

import (
    "net/http"

    "github.com/kafeiih/vogel/audit"
    "github.com/kafeiih/vogel/audit/httpx"
    "github.com/kafeiih/vogel/httpx/response"
    "github.com/kafeiih/vogel/request"
)

type AuditHandler struct {
    repo   audit.Repository
    logger *slog.Logger
}

func NewAuditHandler(repo audit.Repository, logger *slog.Logger) *AuditHandler {
    return &AuditHandler{repo: repo, logger: logger}
}

// List godoc
//
//	@Summary		Lista entradas de auditoría
//	@Tags			audit
//	@Produce		json
//	@Param			actor_id			query		string	false	"Filtrar por actor"
//	@Param			resource_type		query		string	false	"Filtrar por tipo de recurso"
//	@Param			resource_id			query		string	false	"Filtrar por ID de recurso (formato UUID)"	Format(uuid)
//	@Param			operation_category	query		string	false	"Filtrar por categoría de operación"	Enums(create,update,delete,execute)
//	@Param			status				query		string	false	"Filtrar por estado"	Enums(success,failed,partial,noop)
//	@Param			limit				query		int		false	"Límite de resultados"	default(20)
//	@Param			offset				query		int		false	"Desplazamiento"	default(0)
//	@Success		200	{object}	response.ListResponse
//	@Router			/audit [get]
func (h *AuditHandler) List(w http.ResponseWriter, r *http.Request) {
    v := request.NewValidator()
    filters := httpx.FiltersFromRequest(v, r)
    if v.HasErrors() {
        v.WriteErrors(w, r)
        return
    }

    entries, total, err := h.repo.List(r.Context(), filters)
    if err != nil {
        h.logger.Error("listing audit entries", "error", err)
        response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "No se pudo listar las entradas de auditoría")
        return
    }

    response.JSONList(w, http.StatusOK, httpx.ToResponseList(entries), total, filters.Limit, filters.Offset)
}
```

Este es exactamente el patrón que ambos repos necesitan: reusar
`httpx.FiltersFromRequest` y `httpx.ToResponse`/`ToResponseList` sin
duplicar el parseo ni el mapeo, mientras el handler concreto (con sus
anotaciones swaggo) sigue siendo propio de la app. Las rutas y su prefijo
(`/v1/audit`, etc.) también siguen siendo decisión de cada app — vogel no
las registra.

### `worker.Config` (reemplaza `config.WorkerConfig` local)

Ver «Cambios de firma que rompen», punto 1, para la conversión de
`config.WorkerConfig` local a `worker.Config` de vogel.

### `postgres`

Reemplaza `internal/infrastructure/database/postgres.go`
(`NewPostgresPool`) en ambas apps. Cambia el nombre del constructor:

```go
import "github.com/kafeiih/vogel/postgres"

// Antes: database.NewPostgresPool(ctx, cfg, logger)
pool, err := postgres.NewPool(ctx, postgres.Config{
    URL:                             cfg.Database.URL,
    MaxConns:                        cfg.Database.MaxConns,
    RequireTLS:                      cfg.Database.RequireTLS,
    StatementTimeout:                30 * time.Second,
    LockTimeout:                     5 * time.Second,
    IdleInTransactionSessionTimeout: 60 * time.Second,
    AfterConnect: func(ctx context.Context, conn *pgx.Conn) error {
        // registrar el codec pgx-shopspring-decimal, como hacía el constructor local
        return RegisterDecimalCodec(conn)
    },
}, logger.Logger)
```

Importante: el `AfterConnect` del `Config` local de go-crucible registraba
el codec `pgx-shopspring-decimal` inline dentro de `NewPostgresPool`; en
vogel, ese hook pasa a ser un campo explícito de `Config.AfterConnect` — hay
que portar esa llamada al closure, como en el ejemplo de arriba, o el codec
deja de registrarse silenciosamente.

Ver «Cambios de firma que rompen», punto 6, para `NewPoolMetricsCollector`.

### `migrate`

Reemplaza `internal/infrastructure/database/migrate/{runner.go,logger.go}`
en ambas apps. **La firma cambia y rompe la compilación** — ver el punto 9 de
«Cambios de firma que rompen»:

```go
import "github.com/kafeiih/vogel/migrate"

// Antes: el fs.FS era implícito (un embed a nivel de paquete) y existía una
// variante UpFS aparte para pasarlo.
err := migrate.Up(ctx, dbURL)

// Ahora: el fs.FS es un argumento obligatorio y UpFS no existe.
err := migrate.Up(ctx, dbURL, appMigrationsFS)
```

Diferencia de comportamiento: `migrate` de vogel toma un advisory lock de
PostgreSQL a nivel de sesión (`goose.WithSessionLocker`) que el runner local
de ninguna de las dos apps tomaba — ver punto 10 del README de vogel. No
requiere cambios de código, pero sí verificar que ninguna infraestructura de
despliegue (init container, pipeline de CI) dependía de que dos corridas de
migración concurrentes pudieran competir sin bloquearse.

### `worker`

Reemplaza `internal/domain/worker/worker.go` +
`internal/infrastructure/worker/{river.go,schema.go}` en ambas apps. Ver
«Cambios de firma que rompen», punto 1, para el diff completo de
`NewRiverQueue`.

```go
import "github.com/kafeiih/vogel/worker"

if err := worker.EnsureSchema(ctx, pool, cfg.Worker.Schema); err != nil {
    return err
}

queue, err := worker.NewRiverQueue(pool, workers, worker.Config{
    Schema:            cfg.Worker.Schema,
    DefaultMaxWorkers: cfg.Worker.DefaultMaxWorkers,
    Queues:            cfg.Worker.Queues,
}, logger.Logger)
```

Los jobs de negocio (`internal/application/jobs/*` — incluido
`MPSyncWorker`/`MPSyncJobArgs` en go-crucible, `SendEmailWorker` en ambas)
**no se migran**: quedan tal cual en cada app, registrados vía
`river.AddWorker(workers, &MyWorker{})` como siempre. Solo se reemplaza la
infraestructura de cola (`Queue`, `RiverQueue`, `Migrate`, `EnsureSchema`).

### `workflow` (capacidad nueva, no es paso de migración)

`workflow` + `workflow/postgres` + `workflow/migrations` no reemplazan
código existente en ninguno de los dos repos — no hay `internal/domain/workflow`
ni tabla `workflow_case` en ninguna de las dos apps hoy. Es un motor de
BPM/workflow genérico disponible para adoptar cuando la app lo necesite,
independiente de esta migración: `Definition` (nodos, transiciones,
guardas), `Case`, `Event`, y el puerto `Repository`
(`workflow/postgres.Repository`: `Create`, `GetByID`, `GetByExternalID`,
`Update`, `AppendEvent`, `ListEvents`, `ListByEligibility`). Su migración de
esquema propia (`001_create_workflow.sql`, tabla de versión
`workflow_db_version`) se aplica solo si la app decide usarlo — no es parte
de la migración del código duplicado existente.

## Verificación posterior

Después de migrar cada paquete (o cada tanda, según el orden recomendado):

1. **Build**: el compilador es la primera red de seguridad para todo cambio
   de firma.

   ```bash
   go build ./...
   ```

2. **Tests**: correr la suite completa, prestando atención a los tests que
   ejercitan mensajes de error en español (van a fallar hasta que se pase
   `WithAuthMessages`/`WithMessages` con el texto esperado) y a los que
   llaman `Recorder.Record` directamente (van a fallar en compilación hasta
   agregar `Source`).

   ```bash
   go test ./...
   ```

   Para los paquetes que tocan base de datos (`audit/postgres`, `postgres`,
   `migrate`, `worker`), correr también los tests de integración contra una
   base de datos real de staging antes de dar la migración por terminada —
   vogel los cubre con tests de integración propios, pero el wiring
   específico de cada app (nombres de schema, tablas de versión) no está
   cubierto por esos tests.

3. **`go list -deps` — confirmar que no quedó código duplicado importado en
   paralelo**. Después de borrar un archivo local, verificar que ningún otro
   punto de la app siga importando el paquete interno que se intentó
   reemplazar:

   ```bash
   # No debería haber ningún import de internal/domain/audit una vez
   # completada la migración de audit:
   go list -deps ./... | grep -i "go-crucible/internal/domain/audit" || echo "sin duplicados"

   # Confirmar que vogel/audit sí aparece:
   go list -deps ./... | grep "kafeiih/vogel/audit"
   ```

   Repetir por cada paquete migrado (`internal/domain/worker`,
   `internal/infrastructure/repository/tx.go` → `pgxtx`, etc.). Un resultado
   no vacío en el primer `grep` significa que quedó un import residual —
   típicamente un archivo que no se terminó de borrar, o un test que todavía
   referencia el tipo local viejo.

## Qué NO migrar

- **Jobs de negocio** (`internal/application/jobs/*`): el andamiaje de cola
  es compartible (`worker.Queue`, `RiverQueue`), el negocio que corre
  adentro de cada job no. `MPSyncWorker`, `SendEmailWorker`, y cualquier
  otro `river.Worker` concreto quedan en cada app.
- **`cmd/`**: binarios de entrada (`cmd/api`, `cmd/worker`), scaffolding de
  arranque, parseo de flags — específico de cada app.
- **Rutas y su prefijo** (`/v1`, registro en `router.go`): vogel
  deliberadamente no las define, ver «Todavía no está acá» en el README de
  vogel.
- **Anotaciones de Swagger**: se leen de los comentarios sobre la función
  handler concreta de cada app — no se pueden mover a una librería
  compartida. Ver el patrón de handler propio delgado en la sección
  `audit/httpx` arriba.
- **Migraciones SQL propias de la app** (todo lo que no sea
  `audit/migrations` o `workflow/migrations`): las tablas de negocio de cada
  app siguen siendo responsabilidad de cada app, corridas con su propio
  conjunto de migraciones y su propia tabla de versión de goose por defecto
  (`goose_db_version`).
- **Structs de configuración de aplicación** (`AppConfig`, `DatabaseConfig`,
  `WorkerConfig`, etc.): vogel expone primitivas de lectura (`config.String`,
  `config.Int`, ...), no los structs que cada app arma con ellas.
- **`Filters` locales con campos extra**: si alguna app tiene un campo de
  filtro de auditoría propio que no está en `vogel/audit.Filters`, no hay
  forma de portarlo sin extender el struct de vogel — evaluar aparte, fuera
  del alcance de esta migración.
