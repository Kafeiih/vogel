# vogel

`vogel` es la base compartida en Go extraída de `go-bluprint`, el ancestro común
de `go-crucible` y `go-licencias`. Contiene los paquetes que una auditoría de los
tres repositorios estableció como comprobadamente compartibles: puertos/adaptadores
puros y utilidades pequeñas sin lógica de negocio, pensados para ser consumidos por
ambos sistemas en lugar de duplicados.

Los puertos (interfaces) viven en paquetes sin dependencias; los adaptadores viven
en subpaquetes, de modo que importar un puerto nunca arrastra transitivamente el
AWS SDK, SendGrid o go-mail.

## Mapa de paquetes

| Paquete | Qué es |
|---|---|
| `stringutil` | Parseo flexible de fechas (`ParseFlexibleDate`) y normalización de cadenas de acentos/espacios en blanco (`Normalize`). |
| `reqctx` | Metadatos con alcance de request transportados a través de `context.Context`, independientes de cualquier router HTTP o framework de logging: un ID de request/correlación (`WithRequestID`/`RequestIDFromContext`) y `RequestInfo` — IP del cliente y User-Agent (`WithRequestInfo`/`RequestInfoFromContext`). Sin más dependencia que el paquete `context` de la biblioteca estándar. Es el terreno neutral entre `httpx/middleware` (quien escribe) y `logger`/`audit` (quienes leen), de modo que ninguno de los dos tiene que importar al otro. |
| `logger` | Un wrapper delgado de `*slog.Logger` (`Logger`). Lee el ID de request/correlación desde `reqctx` (vía `Logger.WithContext`) — no depende de ningún router HTTP, y no posee su propia clave de contexto para el ID de request (esa propiedad se trasladó a `reqctx` — ver el punto 16 más abajo). |
| `storage` | El puerto `Storage`: `Upload`, `Download`, `Delete`, `PresignedGetURL`, `PresignedPutURL`. Sin más dependencia que la biblioteca estándar. |
| `storage/s3` | Adaptador compatible con S3 (`S3Storage`) que implementa `storage.Storage` mediante el AWS SDK v2. Funciona con AWS S3, MinIO, DigitalOcean Spaces y otros servicios compatibles con S3. Incluye `StorageRegistry` para mantener múltiples storages con nombre. |
| `notification` | El puerto `Notifier`: `Send(ctx, *Message) error`, más `Message.Validate()`. Sin más dependencia que la biblioteca estándar. |
| `notification/smtp` | Adaptador SMTP (`SMTPNotifier`) vía `go-mail`. Abre una conexión nueva por cada `Send` — ver el comentario de documentación de `SMTPNotifier` para el porqué. |
| `notification/sendgrid` | Adaptador de SendGrid (`SendGridNotifier`) vía la API HTTP de SendGrid. |
| `httpx/response` | Envoltorios estándar de respuesta JSON de éxito/error/lista (`response.JSON`, `response.Error`, `response.ValidationError`, `response.JSONList`, ...). |
| `httpx/middleware` | Middleware HTTP: `Recovery`, `RateLimitJSON`, `RequestContext` (el único escritor que completa el ID de request, la IP del cliente y el User-Agent en `reqctx` para cada request entrante — reemplaza al viejo par `RequestInfoMiddleware` + `LoggerRequestID`), `SecurityHeaders`, `StructuredLogger`, `Metrics` (Prometheus), `Authenticate` (ejecuta un `auth.Authenticator` y guarda el principal resultante en el contexto de la request), y `RequirePermission` (ejecuta un `authz.Checker` contra el principal autenticado). |
| `auth` | El puerto `Authenticator`: `Authenticate(ctx, token string) (*Principal, error)`. Helpers de contexto (`WithPrincipal`/`FromContext`) y errores centinela tipados (`ErrUnauthenticated`, `ErrForbidden`, `ErrServiceUnavailable`) que distinguen 401/403/503. Recibe un token desnudo en lugar de una request: dónde vive la credencial en el cable de transmisión es una decisión de transporte, así que le corresponde a `httpx/middleware`, y un llamador que no tiene una request a mano — un worker que corre bajo una identidad de servicio, una CLI — igual puede resolver un principal. Solo importa `context` y `errors`. |
| `auth/zitadel` | Adaptador de Zitadel (`Authenticator`) que envuelve el `authorization.Authorizer[*oauth.IntrospectionContext]` de `zitadel-go/v3` — ese tipo genérico nunca aparece fuera de este paquete. Prefiere el claim estándar de OIDC `preferred_username` por sobre el campo legado `Username`. |
| `authz` | El puerto `Checker`: `IsAllowed(ctx, Principal, Resource, action) (bool, error)`. Un error no nulo significa que la decisión no pudo tomarse (mapearlo a 503); `false` significa una denegación genuina (mapearlo a 403). Sin más dependencia que la biblioteca estándar. |
| `authz/cerbos` | Adaptador de Cerbos (`Checker`) vía el cliente gRPC de `cerbos-sdk-go`. `Close()` es un no-op documentado — `cerbos.GRPCClient` en la v0.3.17 no expone ningún método `Close`, así que no hay nada que liberar. |
| `pgxtx` | Infraestructura de transacciones de pgx: `PgxTxManager.WithTx`, `DBFromContext`, `TxFromContext`, la interfaz `DBTX`. Se llama `pgxtx` (y no `repository`) porque contiene infraestructura de transacciones, no repositorios. Vive en la raíz del módulo, como hermano de `postgres`, porque `WithTx` funciona contra cualquier `*pgxpool.Pool` que el consumidor haya construido por su cuenta. |
| `audit` | El puerto de rastro de auditoría: `Entry`, la interfaz `Auditable` (`AuditRepr`/`AuditSnapshot`), el `Recorder` (`Record`, más `Option`s funcionales: `WithSubject`, `WithChange`, `WithAggregate`, `WithAffectedResources`, `WithError`, ...), y el puerto `Repository` (`Create`/`GetByID`/`List`). Lee al actor desde `auth.FromContext` y los metadatos de la request desde `reqctx` — nunca importa `httpx` ni un router (`go list -deps ./audit` no arrastra `chi` ni `httpx`; ver el punto 17). `Recorder.Record` recibe un `Source` (`SourceHTTP`/`SourceWorker`) como argumento obligatorio, no como una opción con valor por defecto — ver el punto 20. |
| `audit/postgres` | `Repository` respaldado por PostgreSQL (`NewRepository(pool)`), portado desde go-licencias: una única consulta `List` que usa `count(*) OVER()` para la paginación (un solo round trip, no dos) y un `Filters.ResourceID` tipado como `*uuid.UUID` (no como `string`). |
| `audit/migrations` | La migración embebida `001_create_audit_log.sql` (idéntica byte a byte entre go-bluprint, go-crucible y go-licencias) expuesta como un `fs.FS` vía `migrations.FS()`, lista para pasarse a `migrate.Up`. Ver «Ejecutar las migraciones de la librería junto a las de la aplicación» más abajo. |
| `request` | Helpers de request HTTP: `JSON` / `JSONWithLimit` (decodificación JSON con límite de tamaño y rechazo de campos desconocidos, con respuestas mapeadas a 400/413) y `Validator` (acumula errores de validación por campo para query params y parámetros de URL de chi: `UUIDParam`, `IntQuery`, `TimeQuery`, `DateQuery`, `Enum`, `PublicIDParam`, ...). |
| `config` | Primitivas para leer y validar variables de entorno: lectores tipados con valores por defecto (`String`, `Bool`, `Int`, `Int32`, `Duration`, `StringSlice`), un lector de variable obligatoria (`Require`), un acumulador `Errors` para que una falla de arranque reporte todos los problemas de una sola vez, y validadores semánticos (`ValidURL`, `IntRange`, `OneOf`, `MinMax`). Deliberadamente **no** define structs de configuración de la aplicación — esos permanecen en la app consumidora. |
| `postgres` | `NewPool(ctx, Config, *slog.Logger) (*pgxpool.Pool, error)`: un constructor de pgxpool con tracing de queries lentas (`SlowQueryTracer`), métricas de pool para Prometheus (`PoolMetricsCollector`), `statement_timeout` / `lock_timeout` / `idle_in_transaction_session_timeout` del lado del servidor (configurables, con valores por defecto razonables), y un error duro de arranque cuando `RequireTLS` está activado pero el DSN deshabilita TLS. |
| `migrate` | Acceso programático a migraciones de goose v3 propiedad de la *aplicación consumidora* (este paquete no embebe ninguna propia): `Up`, `Down`, `UpTo`, `UpByOne`, `Status`, todas recibiendo un `fs.FS` explícito. Cada corrida adquiere un advisory lock de PostgreSQL a nivel de sesión vía `goose.WithSessionLocker`, de modo que las invocaciones concurrentes de `migrate up` se serializan en lugar de competir por la carrera (race). `Options.TableName` selecciona una tabla de versión de goose distinta de la predeterminada, de modo que un conjunto de migraciones numerado de forma independiente (por ejemplo, `audit/migrations`) puede correr contra la misma base de datos que las propias migraciones de una aplicación sin colisionar — ver «Ejecutar las migraciones de la librería junto a las de la aplicación» más abajo. |
| `worker` | Procesamiento de trabajos en segundo plano sobre River (`github.com/riverqueue/river`) respaldado por PostgreSQL: la interfaz `Queue` (`Enqueue`, `EnqueueTx`, `Start`, `Stop`), la implementación `RiverQueue`, `Migrate` y `EnsureSchema`. A diferencia del resto del módulo, la interfaz y su implementación viven en el MISMO paquete y River se importa abiertamente: un consumidor que define jobs ya importa River de todos modos (`river.WorkerDefaults[T]`, `river.Job[T]`), así que esconderlo detrás de un puerto aparte sería ceremonia sin beneficio. `Config` es propio de `vogel` (`Schema`, `DefaultMaxWorkers`, `Queues`) porque el paquete no lee variables de entorno. Los trabajos periódicos se registran con la opción funcional `WithPeriodicJobs` — ver el punto 21 más abajo. |

## Ejecutar las migraciones de la librería junto a las de la aplicación

`audit/migrations` incluye su propia `001_create_audit_log.sql`, numerada de forma
independiente de como estén numeradas las propias migraciones de la aplicación. Si
ambos conjuntos registraran las versiones aplicadas en la tabla predeterminada de
goose, `goose_db_version`, el `001` de la librería y el `001` de la aplicación
colisionarían — el que corriera en segundo lugar se vería como "ya aplicado" y
sería omitido silenciosamente, o fallaría directamente.

Ejecute los dos conjuntos contra la misma base de datos con dos llamadas separadas
a `migrate.Up`, dándole al conjunto de la librería su propia tabla de versión
mediante `migrate.Options.TableName`:

```go
import (
	"github.com/kafeiih/vogel/audit/migrations"
	"github.com/kafeiih/vogel/migrate"
)

// Application migrations track in goose's default "goose_db_version" table.
if err := migrate.Up(ctx, dbURL, appMigrationsFS); err != nil {
	return err
}

// Library migrations track in their own table, independently versioned from
// the application's own 001, 002, ... — migrations.DefaultTableName is
// "vogel_db_version".
if err := migrate.Up(ctx, dbURL, migrations.FS(), migrate.Options{
	TableName: migrations.DefaultTableName,
}); err != nil {
	return err
}
```

Ambas llamadas adquieren el advisory lock de PostgreSQL a nivel de sesión de
`migrate` de forma independiente (ver el punto 10 más abajo), de modo que
ejecutarlas una tras otra en el arranque de la aplicación es seguro incluso con
múltiples réplicas compitiendo por migrar al iniciar. `migrate.Status` y las demás
funciones de `migrate` aceptan las mismas `Options` para consultar el estado del
conjunto de la librería por separado del de la aplicación.

## Correcciones aplicadas durante la extracción

Los paquetes de origen tenían veinte problemas conocidos; todos se corrigieron
como parte de este port, en lugar de arrastrarse. El punto 21 no es un problema
de origen sino una divergencia entre los dos consumidores que hubo que resolver
al unificarlos:

1. **`logger` ya no importa chi.** El `pkg/logger` original leía el ID de request
   directamente desde `github.com/go-chi/chi/v5/middleware`, acoplando un paquete
   de logging a un router HTTP. `logger` ahora posee su propia clave de contexto
   (`WithRequestID` / `RequestIDFromContext`); `httpx/middleware.LoggerRequestID`
   es el puente que lee el ID de request de chi y lo alimenta a esa clave.
   `Logger.WithContext` fue reescrito para usar esta clave en lugar de ser código
   muerto.
2. **`pgxtx.PgxTxManager.WithTx` es seguro ante panics.** Un panic dentro del
   callback ahora revierte la transacción (mediante un `defer`/`recover`) antes
   de volver a lanzar el panic, en lugar de dejar filtrada una transacción
   abierta hasta que el pool la desaloje. Cubierto por
   `TestPgxTxManager_WithTx_PanicInCallback_RollsBackAndPropagates`.
3. **`notification/smtp` documenta su comportamiento real en lugar de
   contradecirlo.** El ADR original afirmaba un "connection pool"; el código
   abría una conexión SMTP nueva en cada `Send`. Este port mantiene el
   dial-per-send (más simple, siempre correcto, sin preocupaciones de
   concurrencia por cliente compartido) y lo declara explícitamente en el
   comentario de documentación de `SMTPNotifier`, incluyendo una referencia a
   lo que necesitaría una implementación con pool si el volumen de envíos
   llegara a justificarlo.
4. **`httpx/middleware.Metrics` ya no entra en panic ante un doble registro.**
   El original llamaba a `prometheus.MustRegister` en `init()`, lo que entra en
   panic si se construye más de una vez. Ahora es
   `NewMetrics(reg prometheus.Registerer) (*Metrics, error)` (un `reg` nulo usa
   por defecto `prometheus.DefaultRegisterer`), que trata `AlreadyRegisteredError`
   como éxito reutilizando el colector existente. Cubierto por
   `TestNewMetrics_DoubleConstruction_DoesNotPanic`.
5. **El límite de bytes de `request.JSON` estaba mal por 2.** Los tres
   repositorios de origen escribían `maxBytes := 1_048_578 // 1mb`; el valor
   real de 1 MiB es `1_048_576`, y el error tipográfico ya se había copiado a un
   segundo sitio de llamada en uno de los repositorios. Corregido a
   `1_048_576` (`request.JSONWithLimit` también permite que los llamadores
   sobrescriban el límite por endpoint). Cubierto por
   `TestJSON_DefaultLimitIsExactly1MiB`.
6. **`config` ganó validación semántica, no solo chequeos de presencia.** El
   `config.Load()` de origen solo verificaba "¿esta variable de entorno no está
   vacía?" — un error tipográfico en `NOTIFICATION_PROVIDER` (por ejemplo,
   `sendgrdi`) caía silenciosamente al valor por defecto de SMTP y deshabilitaba
   el envío de correo, sin nada más que un log `Warn` para advertirlo. `config`
   agrega `ValidURL`, `IntRange`, `OneOf` y `MinMax`, más un acumulador `Errors`
   para que una falla de arranque reporte todas las variables faltantes/inválidas
   de una sola vez en lugar de una por reinicio.
7. **`postgres.NewPool` falla rápido cuando no se puede honrar `RequireTLS`.**
   El `applyTLSConfig` de origen solo elevaba la versión mínima de TLS *si el
   DSN ya tenía TLS habilitado*; un DSN con `sslmode=disable` producía
   silenciosamente una conexión en texto plano mientras el llamador creía que
   `RequireTLS` había surtido efecto — el código de origen incluso documentaba
   el hueco sin cerrarlo. Ahora, en cambio, devuelve un error de arranque que
   nombra la configuración culpable. Cubierto por
   `TestApplyTLSConfig_RequireTLSTrueWithDisabledDSN_FailsFast` y una prueba de
   integración que demuestra que el camino positivo sigue preservando
   `ServerName` (ver `postgres_test.go`).
8. **`postgres` agrega los tres timeouts del lado del servidor que ninguno de
   los tres repositorios configuraba**: `statement_timeout`, `lock_timeout` e
   `idle_in_transaction_session_timeout`, aplicados mediante el hook
   `AfterConnect` de `pgxpool` con valores por defecto configurables y
   razonables. `idle_in_transaction_session_timeout` es el más importante: es
   el único backstop del lado del servidor contra un handler que se cuelga
   mientras mantiene abierta una transacción y sus locks — `statement_timeout`
   no ayuda ahí porque no hay ningún statement en ejecución. Verificado contra
   un contenedor real en `TestNewPool_AppliesServerSideTimeouts`.
9. **`postgres.PoolMetricsCollector` ya no entra en panic ante un doble
   registro** — la misma clase de bug y la misma forma de corrección que
   `httpx/middleware.Metrics` (punto 4): `NewPoolMetricsCollector(pool, reg)`
   ahora devuelve `(*PoolMetricsCollector, error)` y reutiliza un colector ya
   registrado en lugar de llamar a `MustRegister`. Cubierto por
   `TestNewPoolMetricsCollector_DoubleConstruction_DoesNotPanic`.
10. **`migrate` ahora usa locks.** Ninguno de los runners de migración de los
    tres repositorios tomaba ningún lock, así que dos invocaciones concurrentes
    de `migrate up` (dos jobs de CI, dos init containers, una corrida manual
    solapada con un pipeline) podían intercalar DDL contra la misma base de
    datos. `migrate` ahora construye su `goose.Provider` con
    `goose.WithSessionLocker` (un advisory lock de PostgreSQL a nivel de
    sesión), serializando las corridas concurrentes en lugar de dejarlas
    competir. Verificado contra un contenedor real con 5 llamadas concurrentes
    a `migrate.Up` en `TestUp_ConcurrentInvocations_SerializeInsteadOfRacing`.
    Una migración que necesite `CREATE INDEX CONCURRENTLY` igual debe usar la
    anotación de goose `-- +goose NO TRANSACTION`, dado que PostgreSQL rechaza
    ese statement dentro de una transacción — ver el comentario de
    documentación en `migrate/runner.go`.
11. **`auth`/`authz` eliminan por completo la multi-tenencia basada en
    org_id — nunca funcionó.** Ambos sistemas de origen reflejaban un
    `UserContext.OrgID` en cada recurso de Cerbos para que los roles derivados
    pudieran comparar `principal.attr.org_id == resource.attr.org_id`, pero
    `ResourceForUser` copiaba ese org_id directamente del mismo principal, así
    que la comparación siempre era `X == X` — siempre verdadera. Incluso con
    ese bug corregido, la instancia de Zitadel del propietario tiene
    exactamente una organización ("intranet"), con la separación por sistema
    hecha en cambio a nivel de proyecto/aplicación, de modo que
    `urn:zitadel:iam:user:resourceowner:id` devuelve el mismo valor para cada
    usuario en cada sistema: nunca hubo un segundo valor contra el cual
    comparar. `org_id` tampoco aparece en ninguna consulta de repositorio ni en
    ninguna migración de ninguno de los dos sistemas. `Principal.OrgID`,
    `extractOrgID`, y el reflejo de org_id en `ResourceForUser`/`principalAttr`
    desaparecieron, junto con la rama de "tratar como no autenticado si falta
    org_id" que fallaba ruidosamente y que existía solo para proteger ese
    mecanismo muerto. La separación real entre sistemas es la validación de
    audience de OIDC, que el SDK de Zitadel ya realiza.
12. **`auth/zitadel` prefiere `preferred_username` por sobre el campo legado
    `Username`.** go-crucible leía `authCtx.Username` directamente, que queda
    vacío para tokens provenientes de un flujo conforme al estándar;
    go-licencias ya llevaba esta corrección. Portada aquí para que el único
    adaptador de este módulo lo haga bien.
13. **Una caída del PDP/IdP ahora se mapea a HTTP 503, nunca a 401/403/500.**
    go-crucible devolvía 500 cuando el chequeo de Cerbos fallaba; go-licencias
    devolvía deliberadamente 403 (su DEC-08, "para evitar un oráculo"); la base
    colapsaba también el `ServiceUnavailableErr` de Zitadel en 403. Los tres
    dejan al monitoreo ciego a la diferencia entre "el cliente hizo algo mal" y
    "nuestra infraestructura está caída", y ninguno de 401/403/500 es
    reintentable de forma significativa como sí lo es 503. Tanto
    `httpx/middleware.Authenticate` (proveedor de identidad) como
    `httpx/middleware.RequirePermission` (punto de decisión de política) ahora
    mapean un error del lado del proveedor a 503, una denegación genuina a
    403, y credenciales faltantes/inválidas a 401 — ver los comentarios de
    documentación de ambos para el razonamiento completo. Esto reemplaza
    deliberadamente el DEC-08 de go-licencias.
14. **Los mensajes de auth/authz orientados al usuario ya no están fijados en
    español dentro del código.** Los orígenes devolvían literales como
    `"No tenés permisos para realizar esta acción"` incrustados en una
    librería — algo que un paquete compartido no debería poseer en un único
    idioma. `httpx/middleware.AuthMessages` (con `DefaultAuthMessages` para
    valores por defecto neutrales en inglés y `WithAuthMessages` para
    sobrescribirlos) hace configurable el texto de 401/403/503; cada sistema
    consumidor establece su propio texto localizado.
15. **`ZitadelAuthWithRole`, `IsGrantedRole` y `PrincipalFromUser` no fueron
    portados.** Los tres estaban exportados en cada repositorio de origen sin
    ningún sitio de llamada fuera de los tests. El comportamiento de
    `wildcardResourceID = "*"` de `RequirePermission` *sí* se mantuvo — es
    estructural, no cosmético: Cerbos rechaza cualquier recurso con un ID
    vacío antes de evaluar una política, lo cual de otro modo se manifiesta
    como un 500 en cada ruta a nivel de colección (list/create).

16. **`audit_log.request_id` finalmente se asigna.** En los tres repositorios
    de origen, `request_id` estaba declarado en el DTO, mapeado desde la
    entidad y persistido por el repositorio — y nunca se asignaba en ningún
    lado. Cada fila de auditoría en producción tiene `request_id = NULL`, lo
    cual hace imposible vincular una entrada de auditoría con las líneas de
    log de su request, determinar que varias filas escritas por una operación
    fueron una sola acción, o seguir un cambio hasta el background job que
    generó. `audit.Recorder.Record` ahora lee el ID de request desde `reqctx`
    en el mismo paso incondicional en el que lee al principal actuante — no
    detrás de una opción que se pueda omitir por separado — de modo que un
    futuro sitio de llamada no pueda completar el actor mientras "olvida" el
    ID de request, como siempre hizo hasta ahora cada sitio de llamada.
    Cubierto por `TestRecord_RequestIDInContext_IsPersistedOnEntry` y
    `TestRecord_NoRequestIDInContext_LeavesRequestIDNilWithoutError` en
    `audit/recorder_test.go`, y verificado de punta a punta contra una base de
    datos real en `audit/postgres/repository_integration_test.go`.
17. **`audit` ya no importa la capa de transporte HTTP.** El
    `application/audit/recorder.go` de origen llamaba a
    `middleware.UserFromContext`/`middleware.RequestInfoFromContext` — un
    puerto de la capa de aplicación importando `interfaces/http/middleware`,
    la dirección equivocada, y la razón por la que este paquete no pudo
    publicarse en la tanda 3. `audit.Recorder` ahora lee al actor vía
    `vogel/auth.FromContext` y los metadatos de la request vía `vogel/reqctx`,
    ambos paquetes con pocas dependencias orientados a la aplicación.
    `go list -deps ./audit` no arrastra `chi`, ni `httpx`, ni `net/http`.

    El último de esos puntos requirió un segundo paso. `Authenticator`
    originalmente recibía un `*http.Request`, y como Go resuelve dependencias
    por paquete y no por archivo, todo consumidor que importaba `auth` solo
    para leer un `Principal` heredaba `net/http` — `audit` entre ellos.
    Angostar el puerto a un token desnudo la eliminó desde la fuente.
18. **Los metadatos con alcance de request tienen un único dueño: `reqctx`.**
    `logger` solía poseer su propia clave de contexto para el ID de request;
    `httpx/middleware.RequestInfoMiddleware` poseía por separado una clave no
    exportada para IP/User-Agent, con una vía de escape exportada
    `RequestInfoContextKey()` solo para que los tests pudieran inyectar
    valores. Ambos son ahora `reqctx.WithRequestID`/`RequestIDFromContext` y
    `reqctx.WithRequestInfo`/`RequestInfoFromContext`.
    `logger.Logger.WithContext` lee el ID de request desde `reqctx` en lugar
    de una clave que posee él mismo, de modo que el ID que llega a una línea
    de log y el que llega a una fila de `audit_log` son demostrablemente el
    mismo valor. `httpx/middleware.RequestContext` reemplaza al viejo par
    `LoggerRequestID` + `RequestInfoMiddleware` con un único middleware que
    escribe los tres valores en `reqctx`; el propio `middleware.RequestID` de
    chi sigue siendo la fuente del ID upstream. El `RequestInfoContextKey()`
    exportado desapareció — los tests usan `reqctx.WithRequestInfo`
    directamente.
19. **Las migraciones propiedad de la librería obtienen su propia tabla de
    versión de goose.** `audit/migrations` comienza su propia numeración en
    `001`, exactamente igual que las propias migraciones de cada aplicación
    consumidora, de modo que una tabla `goose_db_version` compartida vería a
    los dos `001` como la misma versión. `migrate.Options` ganó `TableName`
    (una opción provista por el llamador, no una constante fijada dentro de
    `migrate`, dado que ese paquete no posee migraciones propias y no tiene
    opinión sobre el esquema de nombres de ningún consumidor en particular),
    de modo que las migraciones de la librería se registran en su propia
    tabla (`vogel_db_version` por convención — ver
    `audit/migrations.DefaultTableName`). Verificado contra una base de datos
    real, con ambos conjuntos reutilizando la versión `1` y aplicándose ambos
    por completo, en `TestUp_IndependentTableNames_DoNotCollide`
    (`migrate/runner_integration_test.go`).
20. **Las entradas de auditoría de un background job declaran su origen
    explícitamente.** El `Recorder.Record` de cada repositorio de origen
    fijaba `Source` en `"http"` por defecto dentro del struct literal, así que
    un worker o job programado que lo llamara — ninguno lo hizo jamás,
    precisamente porque no había ningún valor correcto al cual recurrir por
    defecto — habría producido una fila con forma de HTTP para un trabajo que
    nunca tocó una request. `Recorder.Record` ahora recibe `Source`
    (`SourceHTTP` o `SourceWorker`) como argumento posicional obligatorio, no
    como una `Option`, de modo que cada sitio de llamada declara su origen en
    lugar de heredar uno. Una entrada `SourceWorker` legítimamente no tiene ID
    de request a menos que su contexto haya sido a su vez derivado de la
    request que generó el job — ver `TestRecord_SourceHTTP_And_SourceWorker` y
    `TestRecord_WorkerOrigin_RequestIDStillPropagatedWhenPresent`.
21. **Los trabajos periódicos del worker pasaron de parámetro posicional a
    opción funcional.** Los dos consumidores tenían la misma cola sobre River,
    con una única diferencia de firma: go-crucible había extendido
    `NewRiverQueue(pool, workers, periodicJobs []*river.PeriodicJob, cfg,
    logger)` para su sweep nocturno, mientras go-licencias seguía con
    `NewRiverQueue(pool, workers, cfg, logger)`. Portar cualquiera de las dos
    tal cual rompía al otro consumidor. La firma unificada deja los parámetros
    obligatorios como estaban y mueve los trabajos periódicos a
    `WithPeriodicJobs`, de modo que go-licencias no cambia ninguna llamada y
    go-crucible sólo agrega la opción. `TestBuildRiverConfig_WorkerMode_NoPeriodicJobsOption_MatchesLicenciasShape`
    fija esa equivalencia: si algún día omitir la opción dejara de significar
    «sin trabajos periódicos», el test falla. Los trabajos periódicos siguen
    registrándose únicamente en modo binario worker — un cliente de sólo
    inserción no procesa nada, así que pasarlos junto a un `*river.Workers`
    nulo no los registra.

`WithAffectedResources` es la forma prevista de auditar una operación
masiva/por lotes (bulk/batch): una única entrada sobre el recurso primario que
nombra cada fila que tocó, en lugar de iterar y llamar a `Record` una vez por
cada fila afectada dentro de la misma transacción (como hace hoy
`derecho_cobrar/commands.go` de go-licencias) — esto último multiplica los
INSERTs y extiende el tiempo de retención de los locks en proporción al
tamaño del lote sin ningún beneficio, dado que `AffectedResources` es en sí
misma una columna JSON consultable. Ver el comentario de documentación de
`WithAffectedResources` y `TestWithAffectedResources_SetsListOnSingleEntry`.

## Todavía no está acá

Esta es una cuarta tanda. Excluidos deliberadamente, decisiones pendientes:

- **El prefijo de URL `/v1` / `router.go`** — bloqueado por decisiones de
  convergencia entre los dos sistemas sobre convenciones de ruteo.
- **`cmd/`, migraciones (los archivos SQL en sí, más allá de las propias de
  `audit`), Dockerfile, docker-compose, swagger** — fuera del alcance de una
  tanda de librería compartida; son asuntos propios de cada aplicación, no
  puertos/adaptadores compartibles. `migrate/create.go` (un scaffolder de
  archivos de migración) se dejó afuera de igual manera, por ser un asunto de
  CLI/plantilla y no de API de librería. Los jobs concretos de cada dominio
  (`application/jobs/*`) tampoco se portan: el andamiaje es compartible, el
  negocio que corre adentro no.
- **La capa de Queries/DTO/HTTP handler de `audit`** — los
  `application/audit/{dto,queries}.go` e
  `interfaces/http/handler/audit_handler.go` de los repositorios de origen se
  leyeron como contexto pero no se portaron: son asuntos de la capa de
  presentación (formateo de respuestas, parseo de query-string, anotaciones
  de Swagger) específicos de la propia capa HTTP de cada aplicación
  consumidora, la misma razón por la que `vogel` tampoco portó nunca un
  handler para `auth` ni para `storage`. `audit.Repository.List`/`GetByID` y
  `audit.Filters` son el puerto sobre el cual se construye la propia capa de
  consultas de una aplicación consumidora.
- **`null_helpers.go`** (`nullInt`/`nullInt64`) — dejado atrás
  deliberadamente. Devuelve `nil` cuando `n == 0`, confundiendo "sin
  establecer" con un cero legítimo. Además no se usaba en ninguno de los tres
  repositorios de origen.
