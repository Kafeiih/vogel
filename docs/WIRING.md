# Cómo se compone vogel

El README documenta cada paquete por separado. Este documento no repite ese
mapa: es sobre lo que deja afuera, el cableado entre paquetes — quién
escribe en qué, en qué orden hay que montar las piezas, y qué se rompe
(casi siempre en silencio) si el orden es el equivocado. Verificado contra
el código de `reqctx`, `logger`, `httpx/middleware`, `auth`, `authz`,
`audit`, `pgxtx`, `postgres`, `migrate`, `worker` y `workflow`, y contra el
ejemplo funcional en `examples/api/`, que monta el módulo completo en un
solo proceso y es la referencia más confiable que tiene este repositorio
sobre cómo se supone que encajan las piezas.

## 1. La cadena de metadatos de request

Tres piezas necesitan el mismo dato — el request ID, y en menor medida la IP
y el User-Agent del cliente — pero no pueden importarse entre sí sin violar
la dirección de dependencias del módulo:

- `httpx/middleware` conoce el `*http.Request` y a `chi`. Puede escribir.
- `logger` y `audit` son capas de aplicación. Necesitan leer, pero no pueden
  importar `httpx` ni `chi` — `audit` en particular no puede, porque debe
  poder correr desde un worker que jamás tuvo una request.

`reqctx` es el terreno neutral que resuelve esa tensión: un paquete que no
importa nada más que `context`, dueño de dos claves de contexto no
exportadas (`requestIDKey`, `requestInfoKey`, con `WithRequestID` /
`RequestIDFromContext` y `WithRequestInfo` / `RequestInfoFromContext` como
único acceso). Ni `logger` ni `audit` importan `httpx`; ambos importan
`reqctx`, que no sabe que `httpx` existe.

`httpx/middleware.RequestContext` es el ÚNICO escritor de estas tres claves.
No genera un request ID propio: lo lee del middleware `RequestID` de chi
(`chimw.GetReqID`), así que ese middleware de chi tiene que estar montado
más arriba en la cadena.

```go
// httpx/middleware/requestcontext.go
func RequestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := reqctx.WithRequestID(r.Context(), chimw.GetReqID(r.Context()))

		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || ip == "" {
			ip = r.RemoteAddr
		}
		ctx = reqctx.WithRequestInfo(ctx, reqctx.RequestInfo{
			IP:        ip,
			UserAgent: r.UserAgent(),
		})

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
```

Del lado de la lectura, `logger.Logger.WithContext(ctx)` agrega el atributo
`"request_id"` a cada línea leyendo `reqctx.RequestIDFromContext(ctx)` —
si no hay ninguno, el atributo queda en `""`, sin error.

`audit.Recorder.Record` lee el mismo request ID de `reqctx`, en el mismo
paso incondicional en el que lee al actor desde `auth.FromContext` (ver la
sección 3 y el punto 16 del README):

```go
// audit/recorder.go
func applyActorAndRequestContext(ctx context.Context, e *Entry) {
	if p := auth.FromContext(ctx); p != nil {
		e.ActorID = p.UserID
		e.Username = p.Username
	}
	if id := reqctx.RequestIDFromContext(ctx); id != "" {
		e.RequestID = &id
	}
	if info, ok := reqctx.RequestInfoFromContext(ctx); ok {
		e.IP = info.IP
		e.UserAgent = info.UserAgent
	}
}
```

Como ambos lectores apuntan a la misma clave no exportada del mismo paquete,
el request ID que llega a una línea de log y el que llega a una fila de
`audit_log` son demostrablemente el mismo valor — no dos valores que
coinciden por casualidad de implementación.

**Consecuencia práctica de omitir `RequestContext`:** `RequestIDFromContext`
y `RequestInfoFromContext` no fallan ante la ausencia del valor — devuelven
`""` y `(RequestInfo{}, false)`. Sin panic, sin error, sin log de
advertencia. El síntoma es puramente de datos: toda línea de log lleva
`request_id=""`, y toda fila de `audit_log` de ese request queda con
`request_id NULL` — exactamente el bug de origen que el punto 16 del README
describe haber corregido, reproducido en silencio si este middleware falta.

Orden correcto (ver también `examples/api/router.go`):

```go
r := chi.NewRouter()

r.Use(chimw.RequestID)   // genera/propaga el request ID upstream
r.Use(vmw.RequestContext) // el único escritor de reqctx: request ID, IP, User-Agent
// todo middleware y handler montado después de esta línea puede leer reqctx
```

`httpx/middleware.StructuredLogger` cierra la cadena: su línea de acceso
también toma el `request_id` de `reqctx`, no de chi. Es una consecuencia
directa de lo anterior — leer `chimw.GetReqID` ahí coincidiría con lo que
reportan `logger` y `audit` sólo por casualidad, porque hoy ambos valores
derivan del mismo ID upstream. Un request ID que llegue al contexto por
cualquier otra vía (un worker que lo deriva de la request que lo generó, un
test que lo inyecta con `reqctx.WithRequestID`) terminaría en la fila de
`audit_log` mientras el log de acceso imprime vacío. Con las tres lecturas
sobre `reqctx`, el enlace entre una línea de log y una fila de auditoría es
una propiedad del diseño y no una coincidencia.

La contrapartida: `StructuredLogger` ahora depende de que `RequestContext`
esté montado más arriba. Sin él, la línea de acceso imprime `request_id`
vacío — que es exactamente lo que corresponde, porque en ese escenario la
fila de `audit_log` también lo tiene vacío.

## 2. Autenticación y autorización

La cadena es `chi` → `RequestContext` → `Authenticate` → `RequirePermission`,
y cada eslabón deja algo en el contexto para que el siguiente lo lea.

`Authenticate` extrae el bearer token del header `Authorization` (única
pieza del módulo que sabe dónde vive la credencial en el cable), corre un
`auth.Authenticator`, y si tiene éxito guarda el `*auth.Principal` resultante
en el contexto vía `auth.WithPrincipal`:

```go
// httpx/middleware/auth.go
func Authenticate(a auth.Authenticator, logger *slog.Logger, opts ...AuthOption) func(http.Handler) http.Handler
```

`RequirePermission` lee ese principal con `auth.FromContext`, lo traduce a
un `authz.Principal`, y corre un `authz.Checker` contra un `authz.Resource`
construido con el `resourceKind` fijo del middleware y el `id` de la URL
(o el comodín `"*"` si la ruta no trae uno — necesario porque Cerbos
rechaza cualquier resource con ID vacío antes de evaluar la política):

```go
// httpx/middleware/authz.go
func RequirePermission(checker authz.Checker, resourceKind, action string, logger *slog.Logger, opts ...AuthOption) func(http.Handler) http.Handler
```

### El mapeo de códigos

Ambos middlewares comparten la misma regla de tres vías, y la corrección 13
del README es la que la fija así de manera explícita — reemplazando el
DEC-08 de go-licencias (que colapsaba una caída de Cerbos en 403 "para
evitar un oráculo") y el 500 que devolvía go-crucible en el mismo caso:

| Situación | `Authenticate` | `RequirePermission` |
|---|---|---|
| credencial ausente/inválida | `auth.ErrUnauthenticated` (o cualquier error no reconocido) → **401** | sin `Principal` en contexto → **401** |
| denegación genuina | `auth.ErrForbidden` → **403** | `checker.IsAllowed` devuelve `(false, nil)` → **403** |
| el proveedor falla | `auth.ErrServiceUnavailable` → **503** | `checker.IsAllowed` devuelve error no nulo → **503** |

El porqué del 503 separado: un IdP o un PDP caídos no son "el cliente hizo
algo mal" (401/403) ni "bug genérico del servidor" (500) — son un incidente
de infraestructura, y a diferencia de 401/403/500, 503 es razonablemente
reintentable. Colapsar los tres casos en un mismo código deja al monitoreo
ciego a la diferencia entre un usuario sin permiso y un Zitadel o un Cerbos
caídos.

Los mensajes visibles al usuario para cada código se configuran con
`AuthMessages{Unauthorized, Forbidden, ServiceUnavailable}` (punto 14 del
README — nunca texto fijo en un idioma dentro de una librería compartida),
pasando `vmw.WithAuthMessages(...)` como opción a `Authenticate` o a
`RequirePermission`. `WithAuthMessages` sólo sobrescribe los campos no
vacíos; el resto conserva `DefaultAuthMessages()`. Los dos middlewares
reciben la opción por separado — no comparten una instancia de
`AuthMessages`, así que un mismo texto para ambos hay que pasarlo dos veces.

Montaje típico (de `examples/api/router.go`):

```go
r.Route("/api/v1", func(r chi.Router) {
	r.Use(vmw.Authenticate(authenticator, logger))

	requireRead := vmw.RequirePermission(checker, "documents:document", "read", logger)
	r.With(requireRead).Get("/documents", docs.List)
})
```

`RequirePermission` asume que `Authenticate` ya corrió sobre la misma
request: si se monta sin `Authenticate` por delante, `auth.FromContext`
siempre devuelve `nil` y toda ruta protegida responde 401 incondicionalmente
— fail-closed, nunca deja pasar una request sin principal.

## 3. Transacciones y auditoría juntas

Este es el patrón más valioso del módulo, y el que más falla al copiarse a
mano: una mutación de negocio, su entrada de auditoría, y el job en cola que
dispara como consecuencia, todo dentro de la MISMA transacción de
PostgreSQL.

`pgxtx.PgxTxManager.WithTx` abre la transacción, la guarda en el contexto,
y hace commit o rollback según el callback devuelva `nil` o un error (y
rollback + re-panic si el callback entra en panic):

```go
// pgxtx/tx.go
func (m *PgxTxManager) WithTx(ctx context.Context, fn func(ctx context.Context) error) (err error)
```

`pgxtx.DBFromContext(ctx, pool)` recupera esa transacción activa cuando hay
una, o cae al `pool` cuando no la hay — es lo que permite que un repositorio
nunca necesite una variante "transaccional" y otra "standalone" de cada
método: siempre recibe un `pgxtx.DBTX` (la interfaz común entre `pgx.Tx` y
`*pgxpool.Pool`) y no le importa cuál de los dos es.

El ejemplo completo, adaptado de `examples/api/handler.go` (`DocumentHandler.Create`):

```go
err := h.tx.WithTx(r.Context(), func(txCtx context.Context) error {
	db := pgxtx.DBFromContext(txCtx, h.pool)

	// 1. La mutación de negocio.
	if err := h.store.Create(txCtx, db, doc); err != nil {
		return err
	}

	wfCase, err := h.engine.Open(txCtx, db, workflow.OpenInput{
		Definition: ApprovalDefinitionName,
		Domain:     DocumentDomain,
		ExternalID: doc.ID.String(),
		ActorID:    actorID(txCtx),
	})
	if err != nil {
		return err
	}

	// 2. La entrada de auditoría, en la misma transacción y el mismo db.
	// Record lee actor y request ID de txCtx por su cuenta (ver sección 1).
	if err := h.recorder.Record(txCtx, db, audit.SourceHTTP, audit.ActionCreate,
		"document", doc.ID.String(),
		audit.WithSubject(doc),
		audit.WithAggregate("workflow_case", wfCase.ID.String()),
	); err != nil {
		return err
	}

	// 3. El job en cola, dentro de la misma transacción vía EnqueueTx.
	if _, err := h.queue.EnqueueTx(txCtx, pgxtx.TxFromContext(txCtx), NotifyReviewersArgs{
		DocumentID: doc.ID.String(),
		Title:      doc.Title,
	}, nil); err != nil {
		return err
	}

	return nil
})
```

Por qué `EnqueueTx` y no `Enqueue`: River inserta la fila del job dentro de
la misma transacción de PostgreSQL que las otras tres escrituras — el
patrón Transactional Outbox. El job sólo se vuelve visible para un worker
si la transacción entera hace commit. `Enqueue` después del commit deja una
ventana donde un crash entre ambos pasos pierde la notificación sin dejar
rastro; `Enqueue` antes del commit (fuera de la transacción) deja la
ventana inversa — un worker podría procesar el job para un documento que un
error posterior en el mismo handler termina revirtiendo. `EnqueueTx` cierra
las dos: el job vive o muere con la misma transacción que el resto.

`worker.Queue.EnqueueTx` toma un `pgx.Tx` real, no un `pgxtx.DBTX` — se lo
recupera del contexto con `pgxtx.TxFromContext(txCtx)`, no con
`DBFromContext`, porque River necesita el tipo concreto de pgx para anclar
el `INSERT` a esa transacción:

```go
// worker/worker.go
type Queue interface {
	Enqueue(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
	EnqueueTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}
```

## 4. Arranque de la aplicación, en orden

El orden que sigue no es estilo: cada paso depende de que el anterior haya
terminado, y varios fallan de forma opaca si se invierten.

```
1. config   -- leer y validar TODO el entorno, acumulando errores
2. postgres.NewPool -- el pool del que depende todo lo demás
3. migrate.Up (x3)  -- app, audit/migrations, workflow/migrations
4. construir adaptadores (recorder, engine, repos, queue...)
5. montar middlewares (RequestContext primero, StructuredLogger último)
6. montar rutas
```

**1. `config`.** `config.Errors` acumula CADA variable faltante o inválida
antes de fallar (`e.Require("DATABASE_URL")`, `e.Int("PORT", 8080)`, luego
un único `e.Err()` al final), en vez de que el proceso reinicie una vez por
cada problema descubierto de a uno. Tiene que pasar antes que cualquier
otra cosa: no tiene sentido abrir un pool con una URL vacía sólo para
descubrir después que `PORT` también estaba mal.

**2. `postgres.NewPool`.** Necesita la config ya validada. Falla rápido —
antes de devolver un pool — si el DSN es inválido, si `RequireTLS` está en
`true` pero el DSN no habilita TLS, o si el ping inicial falla. Todo lo que
sigue asume un pool ya vivo.

**3. `migrate.Up` (tres veces).** Cada conjunto de migraciones corre contra
su propia tabla de versión de goose, porque los tres numeran desde `001` de
forma independiente:

```go
migrate.Up(ctx, dbURL, appFS, migrate.Options{Logger: log.Logger})
// tabla: "goose_db_version" (default de goose)

migrate.Up(ctx, dbURL, auditmigrations.FS(), migrate.Options{
	TableName: auditmigrations.DefaultTableName, // "vogel_db_version"
})

migrate.Up(ctx, dbURL, wfmigrations.FS(), migrate.Options{
	TableName: wfmigrations.DefaultTableName, // "workflow_db_version"
})
```

Tiene que correr después del pool y antes de construir cualquier adaptador
que consulte esas tablas (`audit/postgres`, `workflow/postgres`) — un
`Repository` construido contra tablas inexistentes no falla al construirse,
falla en el primer query real, con un error genérico de PostgreSQL
("relation does not exist") que no dice nada sobre el orden de arranque.

**4. Construir adaptadores.** `audit.NewRecorder(repo)`, `workflow.New(...)`,
`pgxtx.NewPgxTxManager(pool)`, `worker.NewRiverQueue(...)`, los handlers.
Necesitan el pool y las migraciones ya aplicadas; no necesitan el router
todavía.

**5-6. Montar middlewares, luego rutas.** Ver secciones 1 y 2 para el orden
interno de middlewares — `RequestContext` cerca del principio porque todo
lo montado después puede querer leer `reqctx`. Las rutas van al final
porque cierran sobre middlewares y adaptadores ya construidos (p. ej.
`RequirePermission` se construye una vez por combinación recurso/acción y
se reutiliza en varias rutas — ver `examples/api/router.go`).

## 5. Modo sólo-inserción vs. binario worker

`worker.NewRiverQueue` sirve para dos roles distintos según qué le pases en
`workers *river.Workers`:

```go
// worker/river.go
func NewRiverQueue(pool *pgxpool.Pool, workers *river.Workers, cfg Config, logger *slog.Logger, opts ...Option) (*RiverQueue, error)
```

- **Proceso API:** construye la cola con `workers == nil`. Sólo llama a
  `Enqueue`/`EnqueueTx`. Nunca llama a `Start`. `buildRiverConfig` deja
  `Workers`, `Queues` y `PeriodicJobs` sin fijar en ese caso, así que un
  cliente insert-only no procesa nada aunque alguien le pase trabajos
  periódicos por error (ver `WithPeriodicJobs`).
- **Binario worker:** construye la cola con un `*river.Workers` real
  (registrado vía `river.AddWorker`), y llama a `Start`, que bloquea
  procesando jobs hasta `Stop`.

`examples/api/` corre ambos roles en un solo proceso por simplicidad — pasa
un `*river.Workers` no nulo y llama a `Start` — pero su propio comentario
deja explícito que en producción se esperan dos binarios separados.

**`EnsureSchema` va antes de `Migrate`, siempre:**

```go
// worker/schema.go
func EnsureSchema(ctx context.Context, pool *pgxpool.Pool, schema string) error
```

River crea sus propias tablas DENTRO del esquema configurado (`Config.Schema`,
sin default de librería — cada consumidor tiene que elegir uno), pero nunca
crea el esquema en sí. En una base de datos recién provista, `Migrate` sin
`EnsureSchema` antes falla con un error de PostgreSQL de esquema
inexistente. `EnsureSchema` es idempotente (`CREATE SCHEMA IF NOT EXISTS`),
así que correrlo en cada arranque del worker, incluso contra un esquema que
ya existe, es seguro: `EnsureSchema` → `NewRiverQueue` → `queue.Migrate` →
(sólo el binario worker) `queue.Start`.

## 6. Los errores de montaje que el compilador no detecta

- **Middlewares en el orden equivocado (p. ej. `Authenticate` antes de
  `RequestContext`), u olvidar `RequestContext` directamente.** No hay
  error de compilación ni de arranque en ningún caso: `Authenticate` sigue
  autenticando bien porque no depende de `reqctx`. El síntoma es puramente
  de datos — todo log y toda fila de `audit_log` de esa request (o del
  proceso entero, si falta por completo) queda con `request_id` vacío o
  `NULL`, indefinidamente, sin ningún error que lo señale — exactamente el
  bug de origen del punto 16 del README. Arreglo: `chimw.RequestID` →
  `vmw.RequestContext` antes que cualquier middleware o handler que loguee
  o audite.

- **Llamar a `Record` con `audit.SourceHTTP` desde un worker.** Compila
  perfecto: `Source` es sólo un `string` con nombre de tipo, sin
  verificación de que corresponda al contexto de ejecución real. Síntoma:
  filas de `audit_log` con forma de HTTP (`source = "http"`) para trabajo
  que nunca tocó una request, probablemente sin actor (`auth.FromContext`
  es `nil` en un worker) — una fila que miente sobre su propio origen.
  Arreglo: todo `river.Worker.Work` usa `audit.SourceWorker`, como
  `NotifyReviewersWorker.Work` en `examples/api/jobs.go`.

- **Compartir la tabla de versión de goose entre dos conjuntos de
  migraciones.** No falla en la primera corrida de `migrate.Up`. Síntoma en
  la segunda, contra un conjunto distinto: goose ve su propio `001` como
  "ya aplicado" (porque el otro conjunto ya escribió versión `1` en la
  misma tabla) y lo salta en silencio, o falla creando un objeto que en
  realidad nunca se creó. Arreglo: `migrate.Options{TableName: ...}` — una
  tabla propia por conjunto (`goose_db_version`, `vogel_db_version`,
  `workflow_db_version`), nunca compartida.

- **Construir `Metrics` (o `PoolMetricsCollector`) dos veces sin manejar el
  error.** Ambos devuelven `(*T, error)` precisamente porque un segundo
  registro contra el mismo `prometheus.Registerer` produce
  `AlreadyRegisteredError`, manejado reutilizando el colector existente.
  Ignorar el error con `_` en vez de chequearlo esconde cualquier OTRO
  fallo de registro (p. ej. un tipo de colector incompatible bajo el mismo
  nombre), que debería tumbar el arranque y en cambio pasa desapercibido.

- **`RequireTLS` con un DSN que trae `sslmode=disable`.** Corregido para
  fallar rápido y nombrar la configuración culpable (punto 7 del README);
  el riesgo que el compilador sigue sin detectar es el inverso — dejar
  `RequireTLS: false` cuando debería ser `true` compila y arranca sin
  ninguna queja, sólo que sin la protección que uno cree tener. Es una
  decisión explícita de cada consumidor, no algo que el paquete infiera
  del DSN.
