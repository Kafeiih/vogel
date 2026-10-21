# Changelog

Todos los cambios relevantes de `vogel`. El formato sigue
[Keep a Changelog](https://keepachangelog.com/es-ES/1.1.0/) y el versionado,
[SemVer](https://semver.org/lang/es/).

## [Unreleased]

### Cambios que rompen

- **`examples/boilerplate` exige la configuración de BPMS.** El ejemplo trae
  todos los componentes, `bpms` incluido, así que la API y el worker ya no
  arrancan sin `GOTENBERG_URL` ni con un `STORAGE_CONFIGS` que no incluya el
  storage de `BPMS_DOCUMENT_STORAGE` (`documents` por defecto). Un despliegue
  del ejemplo que actualice con su `.env` anterior debe sumar esas variables
  (ver `deploy/env.example`) y un servicio Gotenberg. Los servicios generados
  con `create-vogel-app` no cambian: `bpms` está apagado por defecto.
- **`examples/boilerplate` y `examples/api` sirven sus rutas en `/`.** Se
  quitó el prefijo de versión del router (`/v1` en el boilerplate, `/api/v1`
  en `examples/api`): `/healthz`, `/readyz`, `/livez`, `/swagger/` y
  `/audit` cuelgan de la raíz, y el `basePath` de Swagger es `/`. Versionar
  queda a cargo del proxy inverso (nginx), que publica cada versión de la app
  bajo su propio prefijo. **Migración:** un cliente o un proxy que llamaba a
  `/v1/...` debe apuntar a `/...` (o el proxy debe reescribir su prefijo), y
  un servicio ya generado que quiera seguir el ejemplo cambia su
  `r.Route("/v1", ...)` por `r.Group(...)`, el healthcheck del `Dockerfile`
  y las alertas de Prometheus sobre `path="/v1/healthz"`.

### Agregado

- **Boilerplate: dominio de procesos en el componente `bpms`.**
  `create-vogel-app --with bpms` genera un dominio en capas
  (`internal/domain/process`, `internal/application/process`,
  `internal/infrastructure/bpms` y las rutas `/processes`, `/inbox` y
  `/cases`) con el proceso `consulta` ya publicado: definiciones JSON
  versionadas (publicar valida todo y congela la versión; cada caso corre con
  la suya), tipos de campo como plugins con reglas `visibleWhen` en CEL y
  responsables por perfil resueltos en vivo por un puerto `ProfileResolver`,
  con la persona fijada al tomar la tarea. Usa la misma infraestructura del
  boilerplate: el actor es el usuario autenticado, cada ruta pasa por las
  políticas `bpms:process` y `bpms:case` de Cerbos, sus tablas son un conjunto
  de migraciones propio (`bpms_db_version`) y `BPMS_PROFILE_HOLDERS` dice quién
  tiene cada perfil hasta conectar el directorio real.
- **`workflow`: efectos declarativos.** `Node.Effects` (al entrar al nodo) y
  `Transition.Effects` (al tomar la transición o la ruta) nombran efectos que
  el motor **nunca ejecuta**: `Engine.Open` y `Engine.Move` llaman una sola
  vez a `EffectSink.Dispatch`, después de persistir todo, con el caso final y
  exactamente los eventos que esa llamada agregó (en orden de `Seq`: el salto
  humano, los saltos de decisión y el `EventClosed` automático), sobre el
  mismo `db` del llamador; si `Dispatch` falla, el método devuelve el error y
  el llamador debe hacer rollback. `Claim` y `Release` no despachan.
  `WithEffectSink` fija el sink (en cualquier orden respecto de
  `WithDefinitions`, que ahora registra después de aplicar todas las
  opciones); `Engine.Register` rechaza con `ErrUnknownEffect` una definición
  que nombra un efecto que el sink no conoce, o cualquier efecto sin sink.
  `Definition.EffectNames` lista los nombres ordenados y sin repetir, y
  `Definition.Validate` rechaza nombres vacíos o repetidos dentro de un mismo
  nodo o transición, y efectos en un nodo de decisión (van en sus rutas).

- **`document`: documentos PDF generados desde plantillas.** `Template`
  (nombre, versión y cuerpo HTML) se ejecuta con `html/template`, así que todo
  dato interpolado se escapa automáticamente (a diferencia de la sustitución
  sin escapar de SIMPLE). `Service.Generate` renderiza la plantilla, la
  convierte a PDF con un `Renderer` intercambiable, la sube con
  `storage.Storage` (`application/pdf`, clave
  `documents/<dominio>/<caso o external id>/<id>.pdf`) e inserta sus
  metadatos: caso, plantilla y versión, sha256, tamaño y una clave de
  verificación aleatoria y URL-safe para un endpoint público
  (`FindByVerificationKey`); `Get` y `ListByCase` completan las consultas. Si
  el insert falla después de subir, se intenta borrar el objeto y se devuelve
  el error original. `document/gotenberg` es el primer `Renderer` (ruta
  Chromium HTML de Gotenberg por HTTP, con timeout y tamaño de respuesta
  acotados); `document/postgres` y `document/migrations` (tabla de versión
  `document_db_version`) persisten los metadatos en la tabla
  `document_record`. `GenerateInput.SourceKey` identifica a quien pidió el
  documento: con una clave ya conocida, `Generate` devuelve el documento
  guardado sin volver a generarlo, y `ExistsForCase` responde si un caso ya
  tiene un documento de una plantilla. `document/docflow` lo une con el motor
  y con `action`: `Exists` es el guard que impide avanzar sin el documento y
  `Generate` es el handler de un efecto que genera documentos, idempotente por
  `<caso>/<seq>/<efecto>`; una entrada inválida, una plantilla que no compila
  o no se puede ejecutar (`ErrTemplate`), un PDF vacío (`ErrEmptyPDF`) o un
  documento que el `Renderer` rechaza (`ErrRenderRejected`: en
  `document/gotenberg`, un 400, 409 o 422, o un PDF más grande que el límite)
  cancela el trabajo en vez de reintentarlo; un Gotenberg caído, lento,
  saturado o mal configurado (por ejemplo un 401, 403, 404 o 413 de un proxy,
  un WAF o una URL equivocada) se reintenta. El borrado de limpieza después de un insert
  fallido tiene su propio tope (`Config.CleanupTimeout`, `10s` por defecto) y
  su falla se registra con el contexto del llamador. Un dominio o external id igual a `.` o `..` se rechaza, porque
  forma parte de la clave de almacenamiento. La firma electrónica queda
  fuera de alcance: sólo se define la interfaz `Signer`.

- **`action`: los efectos del workflow se ejecutan en segundo plano.**
  `action.Registry` implementa `workflow.EffectSink`: `Dispatch` traduce cada
  evento que agregó `Open` o `Move` a nombres de efecto (los del nodo de
  destino y luego los de la transición o ruta tomada; `EventClosed` no agrega
  nada) y encola un trabajo de River por cada (caso, `seq`, efecto) con
  `worker.Queue.EnqueueTx` en la transacción del llamador, que debe ser el
  mismo `db` con que el motor escribió el caso: un rollback no encola nada y,
  si `db` no es una `pgx.Tx` (aunque haya una transacción en el `ctx`), falla
  con un error que envuelve `pgxtx.ErrNoTx`. Esa comprobación ocurre antes
  de escribir: `EffectSink.CheckDB` se llama al inicio de `Open` y `Move`
  cuando la definición declara efectos, así que un pool como `db` no avanza
  el caso ni pierde sus efectos. Los trabajos son únicos por esa tripleta, así que
  redespachar el mismo evento no los duplica. `action.Worker` (registrado con
  `action.AddWorker`) busca el `Handler` por nombre: sus errores se
  reintentan, `river.JobCancel(err)` falla en forma permanente y un efecto
  desconocido se reintenta con backoff: en un despliegue escalonado, un worker
  de la versión anterior puede tomar el trabajo de un efecto que sólo registra
  la nueva. `NotifyHandler` envía por un
  `notification.Notifier` los mensajes que arma el llamador, validando cada
  uno antes de enviar; con más de un mensaje, el trabajo del efecto encola un
  trabajo por mensaje (`Args.Part`/`Run.Part`), así que un envío fallido
  reintenta sólo ese mensaje y no reenvía los que ya salieron. El efecto `webhook` queda planificado.

- **Boilerplate: componente opcional `bpms`.** `create-vogel-app --with bpms`
  (apagado por defecto; requiere `worker` y `storage`) arma la base BPMS en el
  servicio generado, sin ningún proceso de negocio propio:
  `internal/infrastructure/bpms` construye el `action.Registry` sobre la cola
  de River, el `workflow.Engine` (`workflow/postgres`,
  `WithEffectSink(registry)`) y el `document.Service` (`document/postgres`, el
  storage `BPMS_DOCUMENT_STORAGE` y `document/gotenberg`); la API y el worker
  los construyen desde el mismo `Process`, así que registran los mismos
  efectos, y solo el worker los ejecuta (`action.AddWorker`). `migrate up`
  aplica también `workflow/migrations` (`workflow_db_version`) y
  `document/migrations` (`document_db_version`). Configuración nueva:
  `GOTENBERG_URL` (obligatoria), `GOTENBERG_TIMEOUT` (`30s`, como máximo
  `45s` para terminar dentro del minuto que River le da a un trabajo) y
  `BPMS_DOCUMENT_STORAGE` (`documents`, debe estar en `STORAGE_CONFIGS`).
  Los compose de desarrollo y despliegue suman un servicio Gotenberg
  (`gotenberg/gotenberg:8.38.1-chromium`, fijado por digest), y
  `docs/BPMS.md` explica cómo declarar una `Definition` con efectos,
  registrar guards (`docflow.Exists`) y handlers (`docflow.Generate`,
  `action.NotifyHandler`) y por qué `Open`/`Move` reciben la `pgx.Tx`. Sin
  `bpms` el proyecto generado no cambia.

### Cambiado

- **`examples/boilerplate`: el despliegue usa RDS en producción y una
  plantilla completa por ambiente.** `deploy/env.example` lista toda la
  configuración de un host (base, HTTP, Zitadel, Cerbos y los componentes
  opcionales, con sus valores por defecto). El compose arma los DSN de los
  dos roles a partir de `DB_HOST`, `DB_NAME` y sus contraseñas (cada dato una
  sola vez), con `sslmode=verify-full` y el bundle de la CA de RDS montado en
  los contenedores. El Postgres empaquetado queda
  detrás del profile `bundled-db` para los ambientes que no son producción, y
  el nuevo servicio `db-roles` crea los roles en RDS con el usuario master,
  que ya no necesita ser superusuario. Cada servicio rota sus logs, `api` se
  reinicia solo como el worker (`restart: unless-stopped`) y `API_PORT` elige
  el puerto publicado. **Migración:** en un `deploy/.env`
  existente, agregar `DB_HOST` y `DB_NAME`; con el Postgres empaquetado,
  `DB_HOST=postgres`, `DB_SSLMODE=disable`, `DB_REQUIRE_TLS=false` y
  `COMPOSE_PROFILES=bundled-db`. Todo host baja el bundle de RDS (ver la
  sección 0 del RUNBOOK).
- **`examples/boilerplate`: `deploy/env.example` deja `DB_HOST` vacío**, así
  que compose falla si no se completa en vez de aceptar un host de ejemplo, y
  declara `STORAGE_CONFIGS` en una sola línea: con `bpms`, descomentar otro
  storage ya no queda pisado en silencio por una segunda línea.
- **`examples/boilerplate`: los YAML y los `env.example` no tienen
  comentarios**, salvo los marcadores del generador, las versiones al lado de
  las acciones fijadas por SHA y notas de una línea donde un cambio rompe o
  expone algo.

## [0.13.2] — 2026-10-20

### Cambiado

- **`examples/boilerplate`: trae `env.example` y el código Go no tiene
  comentarios.** La configuración de desarrollo que el README pegaba dentro
  de una nota ahora es un archivo `env.example` (sin punto inicial, porque
  `create-vogel-app` nunca copia archivos `.env*`), y el arranque rápido corre
  `cp env.example .env`. Los proyectos generados reciben ese archivo con el
  nombre de su base. Del código Go se quitaron los comentarios, salvo los que
  leen las herramientas (anotaciones de swag, marcadores `vogel:component`,
  directivas) y cinco notas de una línea donde un error rompe algo. El
  Swagger generado no cambia.

## [0.13.1] — 2026-10-20

### Corregido

- **`auth/zitadel`: `Config.Issuer` acepta la URL que documenta.** `New` le
  pasaba el valor tal cual a `zitadel.New`, que espera un host y siempre
  antepone `https://`, así que un issuer como
  `https://my-instance.zitadel.cloud` terminaba en
  `https://https://my-instance.zitadel.cloud` y el discovery de OIDC fallaba.
  Ahora una URL `https://` (con puerto opcional) o `http://` (sin TLS, solo
  con un host loopback: `localhost`, `127.0.0.1`, `::1`) se separa en host y
  puerto; un `http://` hacia otro host, una URL con path, query,
  fragment o user info, o con otro esquema, es un error de construcción. Un
  valor sin esquema (`my-instance.zitadel.cloud`) se sigue tratando como antes.

## [0.13.0] — 2026-10-19

### Cambios que rompen

- **`httpx/response`: se quita el campo `trace_id`.** `ErrorResponse` y
  `ValidationErrorResponse` pierden `TraceID`, y las respuestas de error ya no
  incluyen la clave `trace_id`, que llevaba el request ID y no un trace ID de
  OpenTelemetry. Quedó obsoleto en v0.12.0: los clientes deben leer
  `request_id`, que tiene el mismo valor. El código Go que lea o asigne
  `TraceID` deja de compilar.

## [0.12.0] — 2026-10-19

### Agregado

- **`otel`: adaptador de OpenTelemetry.** `Setup(ctx, Config)` exporta trazas
  por OTLP/HTTP (a `<Endpoint>/v1/traces`) con muestreo
  `ParentBased(TraceIDRatioBased)`, 10 % por defecto (`DefaultSampleRatio`),
  e instala el `TracerProvider` y el propagador W3C globales; sin `Endpoint`
  no instala nada. `Provider.Shutdown` vacía lo pendiente. `Middleware` crea un
  span por request (`otelhttp`) nombrado según el patrón de ruta de chi; por
  defecto **no adopta el `traceparent` del cliente** (trace nuevo con un link
  al del cliente, sin su `baggage`), `WithTrustedPropagation()` sí.
  `TraceIDs` encaja en `logger.WithTraceIDs`. No lee variables de entorno:
  pasa explícitas todas las opciones que el SDK tomaría de `OTEL_*` (headers,
  compresión, lotes, límites), y `Config.Headers` exige un endpoint
  `https://`. Es el único paquete que depende del SDK de OpenTelemetry (lo
  verifica el CI). Ver `docs/WIRING.md` §8.
- **Boilerplate: componente `tracing`.** `create-vogel-app` lo incluye por
  defecto (`--without tracing` lo saca). Lee `OTEL_EXPORTER_OTLP_ENDPOINT`,
  `OTEL_SERVICE_NAME` y `OTEL_TRACES_SAMPLER_ARG`, validadas al arrancar, y
  queda apagado sin endpoint. Conecta `otel.Middleware`, `otelpgx`,
  `otelriver`, `otel.Transport` (Zitadel, S3), `otel.Notifier`, los
  `trace_id` en los logs y los exemplars, y vacía las trazas al final del
  apagado, después del servidor HTTP y de la cola.
- **`otel`: `SampledTraceIDs`, `Transport` y `Notifier`.** `SampledTraceIDs`
  es `TraceIDs` solo para spans muestreados, para exemplars. `Transport()`
  encaja en el `WrapTransport` de Zitadel, SendGrid y S3: un span de cliente
  por llamada que manda el `traceparent` pero no el baggage. `Notifier(n)`
  envuelve un notificador (SMTP o SendGrid) con un span `notification.send`
  sin destinatario, remitente, asunto ni texto del error.
- **Exemplars en las métricas.** `httpx/middleware.WithExemplars(fn)` y
  `postgres.Config.TraceIDs` guardan el `trace_id` y el `span_id` del request
  como exemplar en `http_request_duration_seconds`,
  `db_pool_acquire_wait_seconds` y `db_pool_conn_hold_seconds`; el aviso de
  conexión retenida de más también lleva el `trace_id` de quien la pidió.
  Los ejemplos sirven `/metrics` con OpenMetrics, el único formato que
  transporta exemplars.
- **`otel/otelpgx`: tracer de pgx.** `NewTracer()` se enchufa en
  `postgres.Config.Tracer` y crea un span de cliente por consulta, batch (uno
  solo, con un evento por consulta), `COPY FROM` y conexión, con las
  convenciones `db.*` de OpenTelemetry. Guarda el texto SQL, nunca los
  argumentos. Nunca guarda el mensaje de un error, que puede citar el valor
  rechazado: de un error de Postgres guarda el SQLSTATE, de uno de contexto
  su texto fijo y de cualquier otro su tipo (la misma regla que
  `HashSlowQuerySQL`).
- **`otel/otelriver`: tracing de jobs de River.** `NewMiddleware()` se
  enchufa en `worker.WithMiddleware`: crea un span *producer* al encolar y
  deja su `traceparent`/`tracestate` en la metadata del job
  (`vogel_traceparent`, `vogel_tracestate`), y un span *consumer* al trabajar
  que continúa ese trace. Un valor explícito en `InsertOpts.Metadata` gana; el
  baggage no se guarda. Un `river.JobSnooze` queda como evento, no como
  error.
- **`logger`: `WithTraceIDs`.** Recibe una función que lee del contexto el
  `trace_id` y el `span_id` del span activo, y los agrega a cada línea logueada
  con contexto y a lo que fija `WithContext`, con las mismas reglas que
  `request_id`: solo si hay valor y si la línea no lo trae ya. `logger` no
  depende de ninguna librería de tracing; sin la opción, las líneas no
  cambian.
- **`postgres`: `Config.Tracer`, un tracer de pgx extra.** `NewPool` lo
  llama junto al suyo en cada hook que implemente (`pgx.QueryTracer` y,
  opcionalmente, los de batch, `CopyFrom`, prepare, connect, acquire y
  release). Sus `Start` corren antes que los de vogel y sus `End` después, así
  que su span envuelve la línea de consulta lenta, que ahora se loguea con
  `WarnContext` (lleva `request_id` y, con tracing, los IDs del span).
- **`worker`: `WithMiddleware(...)`.** Agrega middlewares de River (de
  inserción, de trabajo o ambos) después del de request ID y antes del de
  métricas, en el orden dado. A diferencia de `WithMetrics`, también se
  instalan en un cliente solo de inserción: un middleware de inserción es como
  el proceso que encola deja su contexto (un trace, por ejemplo) en el job.
- **Clientes salientes instrumentables.** `auth/zitadel.Config`,
  `notification/sendgrid.Config` y `storage/s3.Config` aceptan
  `WrapTransport func(http.RoundTripper) http.RoundTripper`, que envuelve el
  transporte de cada llamada (Zitadel y SendGrid reciben
  `http.DefaultTransport`; S3, el propio cliente del SDK, así que se mantienen
  los límites de vogel y la política de redirects de AWS), y
  `authz/cerbos.Config` acepta `StatsHandler` de gRPC. Sirven
  para trazar esas llamadas (`otelhttp.NewTransport`,
  `otelgrpc.NewClientHandler`); sin ellos nada cambia.
- **`httpx/response`: `request_id` en las respuestas de error.**
  `ErrorResponse` y `ValidationErrorResponse` llevan el request ID también
  bajo `request_id`.

### Obsoleto

- **`httpx/response`: el campo `trace_id`.** Lleva el request ID, no un trace
  ID de OpenTelemetry. Sigue presente con el mismo valor que `request_id`
  durante v0.12.0 y se quita en v0.13.0: los clientes deben leer
  `request_id`.

## [0.11.0] — 2026-10-19

### Cambios que rompen

- **`storage`: `PresignedPutURL` acepta opciones.** La firma pasa a
  `PresignedPutURL(ctx, key, contentType, expiry, opts ...PresignPutOption)`.
  Quien llama no cambia nada, pero un tipo propio que implemente
  `storage.Storage` (un fake de tests, por ejemplo) deja de compilar hasta
  agregar el parámetro variádico.
- **`storage/s3`: la expiración de las URL firmadas tiene un máximo.**
  `Config.MaxPresignExpiry` (por defecto `DefaultMaxPresignExpiry`, 1 h) acota
  `PresignedGetURL` y `PresignedPutURL`: una expiración mayor devuelve
  `storage.ErrPresignExpiryTooLong` en vez de firmarse. `NewS3Storage` falla si
  `PresignExpiry` (o su default implícito de 15 min) supera el máximo, o si el
  máximo supera los 7 días que permite SigV4. Una URL firmada es una credencial que nada
  revoca antes de que expire. Quien hoy firme por más de 1 h debe subir
  `MaxPresignExpiry` o acortar la expiración.

### Agregado

- **`examples/boilerplate`: el CI de los proyectos generados valida las
  reglas de Prometheus.** La plantilla `.github/workflows/ci.yaml` suma el job
  `Prometheus rules`, que corre `promtool check rules` y `promtool test rules`
  sobre `deploy/prometheus/` con la imagen fijada por digest, como el CI de
  vogel.
- **`examples/boilerplate`: el worker sirve `/metrics` y tiene alertas.**
  `cmd/worker` registra `worker.WithMetrics` y el collector del pool, y sirve
  `/metrics` en `WORKER_METRICS_LISTEN_ADDR` (`:9091` por defecto, aparte del
  `:9090` del API); una dirección inválida hace fallar el arranque.
  `deploy/prometheus/alerts.yml` suma `WorkerDown`, `WorkerJobErrorRatioHigh`
  (más del 10% de intentos en `error`/`panic` por `kind`) y `WorkerJobSlowP95`
  (p95 por `kind` sobre 60 s), con tests en `alerts_test.yml`, y el RUNBOOK una
  sección 13 para el worker. Todo queda fuera de un proyecto generado sin
  worker.
- **`httpx/middleware`: buckets configurables para la latencia HTTP.**
  `NewMetrics(reg, WithDurationBuckets(...))` fija los límites de
  `http_request_duration_seconds` (deben ser no vacíos y estrictamente
  crecientes, o `NewMetrics` falla); `DefaultDurationBuckets()` devuelve los de
  siempre, que siguen siendo el valor por defecto. Las llamadas existentes
  siguen compilando.
- **`httpx/middleware`: opciones de `StructuredLogger` para datos del
  cliente.** `remote_addr` y `user_agent` son datos personales bajo reglas
  tipo GDPR. `WithRemoteAddr(RemoteAddrTruncated)` loguea solo la red (/24 en
  IPv4, /48 en IPv6, sin puerto; una dirección que no se puede leer se omite),
  `WithRemoteAddr(RemoteAddrOmitted)` la omite y `WithoutUserAgent()` omite el
  user agent. Sin opciones la línea no cambia y las llamadas existentes
  siguen compilando.
- **`examples/boilerplate`: reglas de Prometheus en `deploy/prometheus/alerts.yml`.**
  Las alertas del RUNBOOK como reglas listas para cargar, más un SLO de
  disponibilidad (99.5% de requests sin 5xx en 30 días) con alertas de
  burn-rate en varias ventanas: rápida (14.4x en 1 h y 6x en 6 h, críticas) y
  lenta (1x en 3 días, ticket). `alerts_test.yml` las prueba con `promtool test
  rules`, y CI valida ambos archivos con `promtool` de una imagen de Prometheus
  fijada por digest.
- **`worker`: `WithMetrics(reg)`.** Registra el histograma
  `river_job_duration_seconds{kind,outcome}` (`outcome`: `success`, `error`,
  `cancel`, `snooze`, `panic`) con un middleware de River que corre después del
  de request ID. Es opcional: sin la opción, o en un cliente sólo de inserción,
  no se instala ni se registra nada; registrar dos veces en el mismo
  `Registerer` reutiliza el colector. El worker del boilerplate la usa y sirve
  `/metrics` en su propia dirección (ver la entrada de `examples/boilerplate`).
- **`worker`: `Config.PollOnly`.** El worker busca jobs consultando cada
  `FetchPollInterval` (1 s por defecto) en vez de usar `LISTEN`/`NOTIFY`, así que
  puede ir detrás de PgBouncer en modo transacción, a costa de esa latencia al
  tomar un job. El boilerplate lo lee de `WORKER_POLL_ONLY`; un valor inválido
  impide arrancar.

- **`pgxtx`: `RequireTx` y `ErrNoTx`.** Para el código que debe correr dentro
  de una transacción: devuelve la transacción del contexto o `ErrNoTx`, en vez
  de caer al pool como `DBFromContext`. Así, olvidar `WithTx` o pasar el
  contexto equivocado es un error y no una segunda conexión tomada en silencio
  fuera de la transacción. `DBFromContext` no cambia.
- **`postgres`: `Config.AcquireTimeout`.** Acota la espera por una conexión del
  pool (vía `AcquireTracer` de pgxpool): con el pool lleno, la operación falla
  dentro del límite con un error que envuelve `context.DeadlineExceeded`, en
  vez de esperar todo el plazo del request. Cero lo desactiva (el
  comportamiento anterior); negativo es un error de `NewPool`.
- **`postgres`: `Config.LeakThreshold`.** Al devolver una conexión retenida más
  que el umbral se registra un WARN con la duración y la función y
  `archivo:línea` que la pidió (el primer frame fuera de pgx, `postgres` y
  `pgxtx`); nunca incluye SQL ni argumentos. Cero lo desactiva.
- **`postgres`: histogramas `db_pool_acquire_wait_seconds` y
  `db_pool_conn_hold_seconds`** (sin labels, de 1 ms a 30 s), exportados por el
  mismo `NewPoolMetricsCollector` para los pools creados con `NewPool`. Dan
  percentiles reales de la espera por conexión, donde antes sólo había un
  acumulado.
- **`examples/boilerplate`: `DB_ACQUIRE_TIMEOUT` (default `3s`) y
  `DB_LEAK_THRESHOLD` (default `10s`).** Un valor inválido, cero o negativo
  impide arrancar y nombra la variable.
- **`storage`: `ErrNotFound`.** `Download` envuelve `storage.ErrNotFound`
  cuando la clave no tiene objeto (`storage/s3` traduce el `NoSuchKey` del SDK),
  así que quien llama distingue un objeto inexistente de una falla transitoria
  sin importar el SDK de AWS. Los demás errores no cambian.
- **`storage`: `WithContentLength(n)` para `PresignedPutURL`.** Firma el tamaño
  exacto del cuerpo: el store rechaza una subida de otro tamaño (verificado con
  un servidor S3 real: 403), así que quien tiene la URL no puede guardar un
  objeto de cualquier tamaño. Un tamaño que no es positivo devuelve
  `storage.ErrInvalidContentLength`: con cero, el SDK de AWS no firmaría ni el
  largo ni el `Content-Type`.

### Cambiado

- **`httpx/middleware`: `Metrics` cuenta los requests que terminan en panic.**
  Con `Recovery` por fuera de `Metrics` (el orden de los ejemplos), un handler
  que hacía panic devolvía 500 pero no aparecía en `http_requests_total` ni en
  los histogramas. Ahora se registra como 500 con su ruta antes de que el
  panic siga hacia `Recovery`. Fuera de un router chi el path es `"unknown"`.
- **`httpx/middleware`: el log de `Recovery` lleva el `request_id`.** Usa
  `ErrorContext`, así el handler de `logger.New` le agrega `request_id` y
  `user_id` como al resto de las líneas del request.
- **`migrate`: la salida de goose es estructurada.** Cada línea usa el
  formato de goose como mensaje y sus valores como `args` (errores y
  duraciones como texto), en vez de un único texto armado con `Sprintf`.
- **`httpx/middleware`: `Recovery` acota el valor del panic.** El log
  `panic recovered` lleva siempre `panic_type` con el tipo Go del valor. Si el
  valor es un error (su `Error()`), un string, un bool o un número, `error` lo
  lleva como texto cortado en 1 KiB (con `error_truncated: true` si se cortó);
  para cualquier otro tipo, como un struct, `error` se omite, porque `fmt`
  imprimiría también sus campos no exportados. Un tipo propio de tipo string,
  bool o numérico sin métodos (un `type sentinel string`, por ejemplo)
  conserva su valor; si tiene métodos (un `String` que tapa un secreto, por
  ejemplo), se loguea solo su tipo. Antes `error` era el
  valor tal cual; ahora es un string o no está.
- **`audit/httpx`: el log de error de `List` ya no incluye la query.** En
  vez de `query` (la query cruda, que puede traer un `actor_id`), la línea
  lleva `filters`: los nombres de los filtros reconocidos que usó el request,
  sin sus valores ni claves desconocidas.
- **`postgres`: el SQL de las consultas lentas se acota.** El WARN
  `slow query detected` corta el texto SQL en 2 KiB (sin partir un carácter
  UTF-8) y en ese caso agrega `sql_truncated: true` y `sql_bytes` con el largo
  real. `Config.HashSlowQuerySQL` (o `WithSlowQuerySQLHash()` en
  `NewSlowQueryTracer`, que ahora acepta opciones variádicas) reemplaza el
  texto por `sql_hash`, un prefijo del SHA-256, para no loguear literales
  escritos dentro del SQL. El hash no tiene sal: quien lea los logs y conozca
  la plantilla puede confirmar un literal de baja entropía probando valores,
  así que la protección real es pasar esos valores como parámetros. Con el
  hash tampoco se loguea el texto del error, que puede citar un valor: un
  error de PostgreSQL va como `error_code` (SQLSTATE) y `error_constraint`, un
  contexto cancelado o vencido conserva su texto fijo y cualquier otro error
  va solo como `error_type`. Sin el hash, el texto del error se corta en
  1 KiB (con `error_truncated: true` si se cortó). Los valores de los
  parámetros nunca se loguean.
- **`auth/zitadel`: se rechaza un `iat` futuro y los algoritmos JWT quedan
  fijados.** Un token cuyo `iat` sea posterior al instante actual más un minuto
  de tolerancia se rechaza con el mismo 401 genérico, aunque `exp - iat` quepa
  en `MaxTokenLifetime` o no haya máximo configurado (aplica también al
  resultado de la introspección). La verificación local de JWT acepta solo
  `RS256`, `ES256` y `PS256`: es el mismo conjunto que zitadel/oidc usa por
  defecto, ahora explícito para que una actualización del SDK no lo amplíe;
  `none` y `HS256` se rechazan (probado).
- **`logger`: los valores de claves sensibles se redactan.** `logger.New` y
  `logger.NewDefault` reemplazan por `"[REDACTED]"` el valor de todo atributo
  cuya clave es sensible (`password`, `token`, `secret`, `authorization`,
  `api_key`, `cookie`, …; la misma lista y las mismas reglas que `audit`, y un
  test falla si se separan), a cualquier profundidad de grupo y también en
  atributos fijados con `With`. Un grupo cuyo nombre es sensible se redacta
  entero. `logger.DefaultRedactedKeys()` expone la lista y
  `logger.WithRedactedKeys(...)` agrega claves; las firmas existentes siguen
  compilando. Un struct o un mapa logueado como un solo atributo no se
  inspecciona. Las líneas sin claves sensibles no cambian (probado byte a
  byte).

- **`examples/api`: los archivos se suben directo al storage con una URL
  firmada.** `PUT /documents/{id}/file`, que recibía los bytes y los reenviaba
  al storage, ya no existe. El flujo nuevo:
  - `POST /documents/{id}/upload-url` con `content_type` y `size` (`415` para
    un tipo no permitido, `400` para un tamaño fuera de 1..25 MiB) devuelve
    una URL de `PUT` de 5 minutos que firma el tipo y el largo exacto, con una
    key nueva por subida (`documents/{id}/uploads/{uuid}`); `expires_at`
    aproxima el vencimiento firmado.
  - `POST /documents/{id}/file` con `{"key": ...}` confirma la subida: lee el
    documento con `SELECT ... FOR UPDATE`, lo deja `pending` y, en la misma
    transacción, encola el job `validate_upload` con 5 intentos como máximo
    (unos 12 minutos con el backoff de River, no semanas). Se valida una
    subida por vez: mientras otra sigue `pending` responde `409` sin encolar
    nada, salvo que lleve más de una hora así (`stalePendingAfter`), en cuyo
    caso la da por abandonada y la reemplaza.
  - `validate_upload` detecta el tipo por el contenido. Lo aceptado se copia a
    una key final, `documents/{id}/file/{uuid}`, que nadie puede firmar para
    `PUT`: reusar la URL de subida ya no reemplaza un archivo validado. Lo no
    permitido se borra, y lo que nunca se subió se rechaza; un objeto
    inexistente se reconoce con `storage.ErrNotFound`, así que el job
    funciona igual con `storage/s3`.
  - Si la confirmación reemplazaba un archivo `ready`, ese archivo queda en la
    fila como anterior y `download-url` lo sigue sirviendo con su tipo
    mientras el nuevo se valida. Un reemplazo aceptado lo retira (un job
    `delete_object` encolado en la misma transacción lo borra, con
    reintentos); uno rechazado lo restaura, auditado como
    `document_restore_file`, así que repetir una confirmación no destruye el
    archivo validado.
  - El veredicto sólo se escribe sobre un archivo `pending`: un reintento del
    job no cambia nada ni audita dos veces, y termina la limpieza que un
    borrado fallido dejó pendiente. Si el job falla en su último intento,
    resuelve el documento como un rechazo y lo registra en el log.
  - `GET /documents/{id}/download-url` firma por 5 minutos (antes 15) y sirve
    el tipo detectado; sin archivo `ready` ni anterior responde `409`.

  La migración `002_add_file_status` agrega `file_status`,
  `file_content_type`, `file_pending_since`, `previous_storage_key` y
  `previous_content_type`; las filas que ya tenían archivo pasan a `ready`.
  El storage falso hace ahora de bucket: sus URL apuntan a
  `FAKE_STORAGE_BASE_URL` (por defecto `http://localhost:<PORT>`) bajo
  `/fake-storage/`, firmadas con HMAC, y rechaza con `403` una firma inválida,
  una URL vencida o un tipo o largo distinto del firmado; limita el total de
  sus objetos en memoria a 256 MiB (`507` al pasarse). Las subidas nunca
  confirmadas no se barren: en producción, una regla de ciclo de vida del
  bucket sobre `documents/*/uploads/`. Ninguna URL firmada se loguea ni se
  audita.
- **`logger`: los métodos `*Context` agregan `request_id` y `user_id` desde
  `reqctx`.** Antes, `InfoContext(ctx, …)` ignoraba el contexto y una línea
  dentro de una request salía sin `request_id` salvo que se usara
  `Logger.WithContext`. Una clave que la línea o el logger ya llevan no se
  repite y gana el valor explícito; fuera de una request no se agrega nada.
  Las líneas que hoy se loguean con contexto pueden ganar esas dos claves.
- **`worker`: el request ID viaja con el job.** `NewRiverQueue` instala un
  middleware de River que copia el request ID del contexto que encola a la
  metadata del job (`vogel_request_id`) y lo devuelve al contexto del worker.
  Los logs del worker y las filas de `audit_log` grabadas con
  `audit.SourceWorker` llevan ahora el `request_id` de la request de origen,
  donde antes quedaba `NULL`. Un `vogel_request_id` puesto a mano en
  `InsertOpts.Metadata` se respeta. River recibe además el logger de la app
  en vez de usar su propio handler de texto, desde `WARN` hacia arriba como
  hasta ahora: sus líneas de info y debug siguen sin salir.
- **`examples/boilerplate`: el logger de la app es también `slog.Default()`.**
  Los binarios `api` y `worker` lo instalan con `slog.SetDefault`, así que los
  jobs que loguean con `slog.InfoContext`, las migraciones de River y las
  librerías que usan `slog.Default()` salen en el mismo formato y con
  `request_id`. Cada línea lleva además `component` (`api` o `worker`) para
  filtrar por binario. `slog.SetDefault` también redirige el paquete `log` de
  la biblioteca estándar: `log.Printf` sale como registro JSON de nivel
  `INFO` en vez de texto plano. Por eso el error con el que termina cada
  binario ya no usa `log.Fatal` sino `slog.Error`, y conserva el nivel
  `ERROR`.
- **`examples/boilerplate`: `STORAGE_<NOMBRE>_PRESIGN_EXPIRY` se valida al
  arrancar.** Antes, un valor que no era una duración se reemplazaba por `15m`
  sin aviso y no había tope. Ahora un valor inválido, cero o negativo, o mayor
  que `1h`, impide arrancar y nombra la variable (`STORAGE_DOCS_PRESIGN_EXPIRY:
  must be at most 1h0m0s, got 2h0m0s`). Un despliegue con un valor así que hoy
  arranca dejará de hacerlo. Los ejemplos de nombres de storage pasan de
  `public`/`private` a `assets`/`documents`: todo bucket es privado y se accede
  con URLs firmadas.

## [0.10.1] — 2026-10-16

### Agregado

- **`examples/boilerplate`: `TestMatrixPolicyMatchesCerbosPolicies`.** Lee
  `cerbos/policies` y falla cuando `matrixPolicy`, el sustituto del PDP con el
  que corre `TestNewRouter_RoleMatrix`, no concede exactamente lo mismo que
  las políticas. También falla ante reglas que `matrixPolicy` no puede
  representar (`condition`, roles derivados, comodines, efectos distintos de
  `EFFECT_ALLOW`). Es parte del componente `cerbos`: `--without cerbos` lo
  quita junto con las políticas.

### Cambiado

- **`examples/boilerplate`: las excepciones de Trivy para Cerbos vencen el
  2026-12-15** (antes, el 2026-11-14). Ninguna versión de Cerbos posterior a
  0.56.0 incluye todavía los arreglos de Go 1.27.2 y `golang.org/x/net`. Un
  proyecto generado con `v0.10.0` tiene su propia copia: debe renovar a mano
  las fechas de `cerbos/.trivyignore`, o su CI de Trivy falla desde el
  2026-11-14.

### Corregido

- **`cmd/create-vogel-app`: lee como máximo 100 000 entradas del tarball.**
  Las entradas que se omiten (fuera de `examples/boilerplate`, enlaces,
  dispositivos) no contaban para ningún límite, así que un tarball con
  millones de entradas vacías dejaba la CLI en un bucle. Ahora cuentan todas;
  el tarball de `v0.10.0` tiene 391.

## [0.10.0] — 2026-10-15

### Agregado

- **`cmd/create-vogel-app`: genera un servicio nuevo desde el boilerplate.**
  `go run github.com/kafeiih/vogel/cmd/create-vogel-app@vX.Y.Z <nombre>`
  descarga el tarball del tag de su propia versión (o usa un checkout local con
  `--from`), pregunta qué componentes opcionales conservar (worker,
  notificaciones, PDP de Cerbos empaquetado, storage S3) y escribe un
  repositorio listo: módulo e imágenes renombrados, sin el `replace` de vogel,
  `go mod tidy`, `git init` y primer commit, y el `gh repo create` para
  subirlo. La extracción rechaza entradas con *path traversal*, rutas absolutas
  y symlinks que escapan del destino, y limita el tamaño. Solo usa la librería
  estándar. El boilerplate marca sus bloques opcionales
  (`vogel:component <nombre> begin|end`, `vogel:only`) y declara componentes y
  reemplazos en `.vogel/template.json`; el CI de vogel genera cada combinación y
  la compila, la prueba y renderiza sus compose. El boilerplate trae además
  plantillas de `.gitignore`, `.golangci.yml`, `.gitleaks.toml`, `dependabot.yml`
  y un `ci.yaml` (lint, tests, govulncheck, gitleaks, Trivy, Cerbos) para el
  repositorio generado.

- **`examples/boilerplate`: plantilla de publicación de imágenes.**
  `.github/workflows/release.yaml` (inerte dentro de `vogel`) publica, en el
  repositorio de un servicio generado y al empujar un tag `vX.Y.Z`,
  `ghcr.io/<owner>/<repo>` (api), `-worker` y `-cerbos` con la versión exacta,
  sin `:latest`, con SBOM y *provenance*, y la de Cerbos solo si pasa el
  *smoke test*; todo o nada: cada job sube su imagen por digest y un job final
  las etiqueta solo si todas salieron bien. Un job por componente para poder
  quitar los que no se usen.
  Un `.dockerignore` propio deja fuera del build `.env`, `secrets/` y las
  claves cuando la copia es la raíz del contexto. El compose de despliegue
  suma el PDP de Cerbos con las políticas horneadas, solo en una red
  `internal` y sin puertos publicados, y el `api` le habla en texto plano
  (`CERBOS_PLAINTEXT=true`) únicamente dentro de esa red.

- **`examples/boilerplate/cerbos/`: políticas de Cerbos versionadas con el
  código.** La política `audit:entry` refleja la matriz de
  `rolematrix_test.go` (`list` solo `admin`; `view` `admin` y `auditor`), con
  su suite de tests. El `Dockerfile` construye el PDP con las políticas
  horneadas y no produce imagen si no compilan o falla un test; corre como
  UID 65532. `make cerbos-test` valida y `make cerbos-smoke` prueba que la
  imagen las sirve (una tienda vacía arranca sana y deniega todo). El Cerbos
  de `make dev-infra` sirve ahora esas políticas con recarga en caliente; antes
  arrancaba vacío y negaba todo chequeo de permisos. El `Makefile` usa
  `-include .env`, así que los targets que no lo necesitan funcionan en una
  copia sin `.env`.

### Cambiado

- **`github.com/prometheus/client_golang` v1.25.0** en el módulo raíz. Sus
  cambios incompatibles están en `api/prometheus/v1`, que `vogel` no usa.
- **`examples/boilerplate`: el compose de despliegue exige `CERBOS_HOST` y
  `CERBOS_PLAINTEXT` en `deploy/.env`** (`docker compose` no arranca sin las
  dos) y trae su propio servicio `cerbos`, del que depende el `api`. Un
  despliegue existente del boilerplate debe sumar el par a su `deploy/.env`:
  `env.example` trae el del PDP empaquetado (`cerbos:3593`, `true`); para un
  PDP externo, su host con `CERBOS_PLAINTEXT=false` y quitar el servicio
  `cerbos` (ver `deploy/RUNBOOK.md`).

## [0.9.0] — 2026-10-15

### Agregado

- **`docs/security/OWASP_CHECKLIST.md`.** Registro de la auditoría de
  `vogel` y sus ejemplos contra la lista de verificación de OWASP: estado de
  cada ítem (PASS, PARTIAL, CONSUMER o N/A) con su evidencia en el código y
  los tests, y las brechas pendientes. Enlazado desde `docs/THREAT_MODEL.md`,
  `SECURITY.md` y el README.

- **`postgres.CheckTLS(dsn)`.** Aplica a un DSN la misma regla de
  `RequireTLS` que `NewPool`, sin conectar, para código que entrega el DSN a
  otra herramienta (p. ej. migraciones). `examples/boilerplate` lo usa en
  `api migrate`, que ahora también respeta `DB_REQUIRE_TLS` (por defecto
  `true`); el compose de despliegue lo fija en `false` también para
  `migrate`, porque el Postgres empaquetado no sirve TLS.

- **`examples/boilerplate`: despliegue endurecido.** El worker tiene
  `HEALTHCHECK`: `worker server` reescribe un archivo de heartbeat en `/tmp`
  cada 10s y el subcomando `worker healthcheck` falla si falta o tiene más de
  30s. En `deploy/docker-compose.yaml`, `migrate`, `grants`, `api` y `worker`
  suman `cap_drop: [ALL]` y `no-new-privileges` al sistema de archivos de solo
  lectura; Postgres conserva solo las capabilities de su entrypoint. Todas las
  imágenes de los compose quedan fijadas por versión exacta y digest
  (`postgres:17.11-alpine3.24`, `cerbos:0.56.0` en lugar de `:latest`).
  `deploy/RUNBOOK.md` documenta la cadencia de parches (Dependabot y Trivy
  semanales) y qué hacer si Trivy falla, y rota las contraseñas de
  `app_migrator`/`app_runtime` con `ALTER ROLE` sin recrear Postgres.
  `docs/THREAT_MODEL.md` §6 lista lo que el servicio consumidor debe
  resolver porque `vogel` no puede imponerlo; `docs/WIRING.md` documenta la
  ranura de usuario de `reqctx`.
- **`examples/boilerplate`: endurecimiento del ejemplo.** Nuevas variables
  de validación de tokens que llenan `zitadel.Config`:
  `ZITADEL_HTTP_TIMEOUT` (10s), `ZITADEL_MAX_TOKEN_LIFETIME` (12h; `0`
  desactiva), `ZITADEL_ALLOWED_AUTHORIZED_PARTIES` y `ZITADEL_INTROSPECTION`
  con `ZITADEL_INTROSPECTION_KEY_FILE` o `_CLIENT_SECRET` y el tamaño y TTL
  de su caché; las combinaciones inválidas hacen fallar el arranque nombrando
  la variable. `vmw.SecurityHeaders` pasa a ser el primer middleware, así los
  401/403/404/429/500/504 también llevan las cabeceras. `GET /v1/audit`
  responde 400 (no 500) a un `offset` mayor que `audit.MaxListOffset`. Los
  probes de salud registran un fallo de la base una vez por causa distinta.
  El ping corre con su propio timeout, desacoplado de la cancelación y del
  plazo del llamador: un cliente que corta la conexión no abre una caída en
  el log, y una base colgada se registra aunque el kubelet cierre la
  conexión al vencer el `timeoutSeconds` del probe.
  `rolematrix_test.go` fija la matriz ruta × rol.
- **`examples/api`: endurecimiento del ejemplo.** `GET /documents` rechaza con
  400 un `offset` mayor que `workflow.MaxListOffset`; `POST /documents` valida
  `title` con `MaxLen(200)` (en caracteres). El `http.Server` fija
  `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout` e `IdleTimeout`
  (`HTTP_*_TIMEOUT`) y `MaxHeaderBytes` de 64 KiB. Un limitador por IP de
  ventana fija (`RATE_LIMIT_REQUESTS`/`RATE_LIMIT_WINDOW`, 300 por minuto)
  protege `/api/v1` y responde 429 con `Retry-After`; es por proceso y de
  ejemplo. `rolematrix_test.go` fija la matriz ruta × rol.
- **`user_id` del usuario autenticado en los logs.** `reqctx` agrega una
  ranura mutable por request (`WithUserSlot`, `SetUserID`,
  `UserIDFromContext`). `httpx/middleware.RequestContext` la instala,
  `Authenticate` la llena con `Principal.UserID` al autenticar (nunca el
  username, los roles ni el token) y `StructuredLogger` la lee al escribir la
  línea de acceso, que ahora incluye `user_id`. Hacía falta una ranura porque
  `StructuredLogger` envuelve a `Authenticate` y nunca ve la request derivada
  que lleva el `Principal`. `logger.Logger.WithContext` también agrega
  `user_id` cuando lo hay. En ambos casos la clave se omite si la request no
  está autenticada; sin `RequestContext` no hay ranura, `SetUserID` no hace
  nada y `Authenticate` funciona igual que antes.

- **`httpx/middleware.WriteAccessErrorFor`.** Variante de `WriteAccessError`
  que recibe el `authz.Resource` y la acción del chequeo por instancia y los
  registra (`resource_kind`, `resource_id`, `action`; nunca `Attr`) en la
  línea de log del 401/403/503, que con `WriteAccessError` quedaban vacíos.
  Mismo mapeo de estados y mismo `false` sin escribir nada para un error que
  no es un centinela de `access`. `WriteAccessError` no cambia;
  `examples/api` (`DocumentHandler.Delete`) usa la nueva variante.
- **`auth/zitadel`: introspección opcional de tokens (`Config.Introspection`
  e `IntrospectionConfig`).** Si se configura, cada token se valida contra el
  endpoint de introspección de Zitadel (RFC 7662, vía el `rs.ResourceServer`
  del SDK y un verificador propio equivalente a su
  `IntrospectionVerificationWithCache`) en lugar de verificar el
  JWT localmente, de modo que un token revocado o una sesión cerrada dejan de
  aceptarse antes de su `exp`. Sin configurar (el valor por defecto) nada
  cambia: sigue la verificación local del JWT. La API se autentica con
  exactamente una credencial: `KeyFile` (el `key.json` de la aplicación,
  perfil JWT) o `ClientSecret` (basic auth con `Config.ClientID`); ninguna o
  ambas hacen fallar el constructor. Discovery y cada introspección usan el
  cliente acotado por `Config.HTTPTimeout`. Los resultados activos se
  cachean en memoria durante `CacheTTL` (constante
  `DefaultIntrospectionCacheTTL`, 30s, sin pasar del `exp` del token) con a
  lo sumo `CacheMaxEntries` entradas (`DefaultIntrospectionCacheMaxEntries`,
  10000, desalojo LRU); la clave es el SHA-256 del token y los resultados
  inactivos no se cachean. Un valor negativo en cualquiera de los dos hace
  fallar el constructor. Compromiso: un token revocado puede seguir
  aceptándose hasta `CacheTTL`. Sobre el resultado corren los mismos
  chequeos (`nbf`, `MaxTokenLifetime`, `AllowedAuthorizedParties`) más que
  `aud` incluya `Config.ClientID`; un token inactivo es un 401 genérico, y
  una introspección fallida (caída, timeout o credenciales de la API
  rechazadas) es `auth.ErrServiceUnavailable` (503). Los misses
  concurrentes del mismo token comparten una sola llamada (`singleflight`
  de `golang.org/x/sync`, ahora dependencia directa, con el SHA-256 del
  token como clave); cada llamador recibe su propia copia del resultado, y
  uno cuyo `ctx` se cancela vuelve de inmediato con ese error (503) sin
  abortar la llamada compartida. No hay caché negativa: cada token
  desconocido distinto sigue costando una llamada, así que una avalancha de
  tokens aleatorios sin autenticar necesita rate limiting delante (p. ej.
  `httprate` por IP real, como el boilerplate).
- **`request`: `Validator.MaxLen(param, value, max)` y el mensaje
  `Messages.TooLong(field, max)`.** Registra un error de validación si el
  valor tiene más de `max` caracteres, contados en runas
  (`utf8.RuneCountInString`) y no en bytes; un valor vacío siempre pasa y el
  valor se devuelve sin cambios. El texto por defecto es
  `<campo> must be at most <max> characters long` y se localiza con
  `WithMessages` como los demás.
- **`audit/httpx`: constante `MaxFilterLength`** (256), el largo máximo de
  los filtros de texto libre de `FiltersFromRequest`.
- **`httpx/middleware`: `RealIP(trusted ...netip.Prefix)` y
  `ParseTrustedProxies`.** Reemplazo del `middleware.RealIP` de chi, que cree
  `X-Forwarded-For`, `X-Real-IP` y `True-Client-IP` de cualquier cliente (un
  cliente directo podía falsificar su IP en `reqctx`, en `audit_log.ip` y en la
  clave del rate limit). Las cabeceras solo se consultan si el par directo
  (`r.RemoteAddr`) está dentro de un prefijo confiable; sin prefijos nunca se
  consultan. `X-Forwarded-For` se recorre de derecha a izquierda saltando los
  proxies confiables y se toma la primera IP no confiable (si todas son
  confiables, la de más a la izquierda); una entrada vacía o inválida detiene
  el recorrido y deja `r.RemoteAddr` sin cambios. `X-Real-IP` se usa solo si no
  hay `X-Forwarded-For`; `True-Client-IP` se ignora siempre. Acepta puertos,
  zonas IPv6 e IPv4 mapeadas en IPv6. `ParseTrustedProxies` convierte una
  lista separada por comas de CIDR o IP sueltas (`/32` o `/128`) en prefijos y
  devuelve error ante cualquier entrada inválida.
- **`examples/boilerplate`: nueva variable `TRUSTED_PROXIES`** (CIDR o IP
  separadas por comas, vacía por defecto). El router monta `vmw.RealIP` con
  esos prefijos en lugar del `middleware.RealIP` de chi; un valor inválido
  hace fallar el arranque. Cambio de comportamiento: detrás de un balanceador,
  hay que declarar sus direcciones en `TRUSTED_PROXIES` o todas las requests
  aparecen con la IP del balanceador.
- **`storage`: `SniffContentType(r, allowed...)` y `ErrContentTypeNotAllowed`.**
  Detecta el tipo de un archivo subido a partir de su contenido (los primeros
  512 bytes, con `http.DetectContentType`) en lugar de confiar en el
  `Content-Type` que declara el cliente, y lo compara con una lista permitida.
  La comparación usa solo el tipo de medio (ignora parámetros como `charset`
  y mayúsculas, vía `mime.ParseMediaType`). Devuelve el tipo detectado y un
  lector con el contenido completo (los bytes leídos incluidos) para pasarlo a
  `Upload`; si el tipo no está permitido, el error envuelve
  `ErrContentTypeNotAllowed`. `examples/api` lo usa en la subida de archivos:
  acepta `application/pdf`, `image/png`, `image/jpeg` y `text/plain`, guarda
  el tipo detectado y responde 415 a cualquier otro contenido, con un mensaje
  que lista los tipos aceptados (armado desde la misma lista que se valida).
- `httpx/response`: constante `CodeUnsupportedMediaType` (`UNSUPPORTED_MEDIA_TYPE`).
- CI: nuevo job `govulncheck` que escanea la raíz y `examples/boilerplate`.
- CI: nuevo workflow `secrets.yaml` que escanea toda la historia de git con
  gitleaks 8.30.1 (binario verificado por SHA-256) en cada push y pull
  request, sin filtros de rutas para que un secreto en documentación también
  se detecte. `.gitleaks.toml` solo exceptúa la clave falsa de SendGrid de
  los tests.
- CI: Dependabot también actualiza los módulos Go de la raíz y de
  `examples/boilerplate` (semanal, un PR agrupado por directorio, prefijo
  `build(deps)`). `govulncheck` solo avisa de vulnerabilidades conocidas y
  alcanzables; Dependabot mantiene las dependencias al día.
- CI: nuevo workflow `release.yaml`. Con cada tag `v*` genera el SBOM
  CycloneDX de la raíz y de `examples/boilerplate` (`cyclonedx-gomod`
  v1.12.0) y lo adjunta, con un `SHA256SUMS`, al release de ese tag (lo crea
  si no existe). Solo el job que publica tiene `contents: write`, y no hace
  checkout ni corre el generador. El README explica cómo descargarlo y
  verificarlo.
- CI: nuevo workflow `trivy.yaml` (push, pull request y semanal) con trivy
  0.75.0 (binario verificado por SHA-256): `trivy config` sobre el repo y
  `trivy image` sobre las imágenes `api` y `worker` construidas desde el
  `Dockerfile` de `examples/boilerplate`; falla con HIGH/CRITICAL
  (`--ignore-unfixed` en las imágenes).
- CI: golangci-lint también corre sobre `examples/boilerplate`, con el
  `.golangci.yml` de la raíz.
- CI: Dependabot también actualiza las imágenes del `Dockerfile` del
  boilerplate y de los compose de `examples/` (ecosistemas `docker` y
  `docker-compose`). `.github/CODEOWNERS` cubre los `go.mod`/`go.sum` de la
  raíz y del boilerplate.
- Documentación de seguridad: `SECURITY.md` (reporte privado de
  vulnerabilidades, versiones con soporte, plazos y alcance),
  `docs/THREAT_MODEL.md` (límites de confianza, datos personales y terceros,
  superficie de ataque, casos de abuso y riesgos residuales),
  `.github/CODEOWNERS` para los paquetes sensibles y una plantilla de PR con
  checklist de seguridad. El runbook del boilerplate suma respaldos con
  simulacro de restauración, rotación de secretos, revocación de tokens y
  comunicación de incidentes.
- **`notification/sendgrid` y `auth/zitadel`: campo opcional
  `Config.HTTPTimeout` y constante `DefaultHTTPTimeout`** en cada paquete
  (30s y 10s respectivamente). Cero usa el valor por defecto; un valor
  negativo hace fallar el constructor.
- **`storage/s3`: campo opcional `Config.ResponseHeaderTimeout` y constante
  `DefaultResponseHeaderTimeout`** (60s). Cero usa el valor por defecto; un
  valor negativo hace fallar el constructor.
- **`auth/zitadel`: campo opcional `Config.MaxTokenLifetime`.** Si es
  positivo, se rechaza (401) todo token cuya vida (`exp - iat`) lo supere, y
  también un token sin `iat` (o sin `exp`), porque su vida no se puede probar.
  Cero (el valor por defecto) desactiva la comprobación; un valor negativo hace
  fallar el constructor. Acota cuánto tiempo sirve un token filtrado aunque el
  emisor esté configurado para emitir tokens de larga duración.
- **`auth/zitadel`: campo opcional `Config.AllowedAuthorizedParties`.** Si
  no está vacío, solo se aceptan tokens cuyo `azp` sea uno de esos client IDs
  (un `azp` ausente o vacío se rechaza). Vacío (el valor por defecto) no
  comprueba `azp`; una entrada vacía hace fallar el constructor.
- **`httpx/middleware`: `RequestID` y constante `RequestIDHeader`
  (`X-Request-Id`).** Reemplazo del `middleware.RequestID` de chi, que adopta
  tal cual el `X-Request-Id` que envíe el cliente (un cliente podía elegir el
  `request_id` de los logs, de `audit_log` y del `trace_id` de los errores,
  colisionar con el de otra request o inyectar contenido en los logs).
  `RequestID` genera siempre el ID en el servidor (16 bytes de `crypto/rand`
  en hexadecimal, 32 caracteres), lo guarda en `reqctx` y en la clave de
  contexto de chi (así `GetReqID` devuelve el mismo valor) y lo escribe en la
  cabecera de respuesta `X-Request-Id`. El valor que envíe el cliente solo se
  conserva como ID de correlación aparte, sin espacios ni caracteres fuera del
  ASCII visible y truncado a 128 bytes.
- **`reqctx`: `WithClientRequestID` y `ClientRequestIDFromContext`**, para el
  ID de correlación enviado por el cliente, separado del ID de request del
  servidor.
- **`authz/cerbos`: `(*Checker).Ping(ctx)`.** Pide `ServerInfo` al PDP (una
  RPC barata que no evalúa políticas) y devuelve un error que nombra el host si
  falla. Como la conexión gRPC es perezosa, `New` no detecta un host erróneo ni
  un modo de transporte que no coincide; `Ping` permite detectarlo al arrancar.

### Cambiado

- **Dependencias del módulo raíz al día**, entre ellas
  `shopspring/decimal` v1.5.0, que rechaza en `NewFromString` los
  exponentes por encima de `decimal.MaxDecodeExponent` (10000) con un error
  sin tipo. `decimalx.Parse` (y por lo tanto `ParseAmount` y `Query`) revisa
  antes el sufijo de exponente, así que un número bien formado como
  `1e100000000` sigue devolviendo `ErrOutOfRange` y no `ErrNotParseable`; la
  entrada malformada sigue en `ErrNotParseable`. Un `decimal.Decimal` con un
  exponente así ya no se obtiene desde JSON ni desde un string: el
  decodificador lo rechaza antes de llegar a `ValidateBounds`. La directiva
  `go` pasa a `1.26.7` (la pide el bump), también en `examples/boilerplate`,
  cuyo `go mod tidy` contra la raíz sube además, entre otras, `river` a
  v0.49.0 y `testcontainers-go` a v0.44.0, y cambia `docker/docker` por los
  módulos `moby/moby`.
- **`examples/boilerplate`: el `Dockerfile` se construye desde la raíz del
  repositorio** (`docker build -f examples/boilerplate/Dockerfile .`), porque
  el `replace` del `go.mod` apunta a `../..`; el compose de despliegue y el
  workflow de Trivy lo construyen así, sin el parche que quitaba
  `go mod download`. El filtro del contexto pasa a un `.dockerignore` en la
  raíz. Una copia del boilerplate sin `replace` construye con
  `--build-arg APP_DIR=.`. `docker-compose.developer.yaml` ya no incluye
  MinIO: `minio/minio` dejó de publicarse en Docker Hub.
- **`examples/boilerplate`: `DB_REQUIRE_TLS` decide el TLS hacia la base, no
  `APP_ENV`.** Vale `true` por defecto en cualquier entorno (antes solo fuera
  de `APP_ENV=development`); el compose de despliegue y el `.env` local la
  ponen en `false` para su Postgres sin TLS. Cambio de comportamiento: un
  despliegue propio con `APP_ENV=development` y `sslmode=disable` debe definir
  `DB_REQUIRE_TLS=false` o deja de arrancar. `SWAGGER_ENABLED` y las nuevas
  variables booleanas rechazan un valor que no sea `true`/`false` en vez de
  usar el default, y el rechazo de Swagger en producción usa
  `Config.IsProduction`.
- Dependencias: `google.golang.org/grpc` v1.83.1 → v1.83.2 (CVE-2026-84445,
  HIGH, detectada por el escaneo de Trivy sobre los binarios del boilerplate),
  en el módulo raíz y en `examples/boilerplate`.
- **`logger.New` y `logger.NewDefault` emiten el timestamp en UTC.** Un
  `ReplaceAttr` convierte el atributo `time` de cada registro a UTC, sin
  importar la zona horaria del host, para que las líneas de servicios en zonas
  distintas se ordenen y correlacionen sin aritmética de offsets. Los
  `time.Time` que el llamador registra como atributos con otra clave
  conservan su zona. Cambio de comportamiento: antes el timestamp salía en la
  zona local del host.

- **`authz/cerbos`: cada llamada al PDP tiene un plazo propio
  (`Config.CallTimeout`, constante `DefaultCallTimeout`, 2s).** `IsAllowed`
  envuelve el `ctx` del llamador en `context.WithTimeout`, así que un PDP que
  acepta la conexión y nunca responde ya no cuelga la request: la llamada
  falla con un error, que `access.Guard` y `RequirePermission` convierten en
  503 como cualquier otra caída del PDP. Un plazo más corto en el `ctx` sigue
  ganando. `Ping` aplica `CallTimeout` solo cuando el `ctx` no trae plazo; si
  lo trae, respeta ese plazo aunque sea más largo, para que una sonda de
  arranque pueda esperar a que el PDP levante. El valor cero usa el valor por
  defecto y uno negativo hace fallar `New`. Cambio de comportamiento: antes
  la llamada esperaba indefinidamente al `ctx` del llamador.
- **`audit/httpx.FiltersFromRequest` rechaza filtros de texto libre
  demasiado largos.** `actor_id`, `resource_type`, `operation_name`,
  `aggregate_type` y `aggregate_id` de más de `MaxFilterLength` (256)
  caracteres registran un error de validación, y `Handler.List` responde 400
  sin consultar el repositorio. Antes se pasaban tal cual a la consulta.
- **`httpx/middleware.SecurityHeaders` agrega aislamiento entre orígenes y
  `Cache-Control: private, no-store`.** Ahora envía
  `Cross-Origin-Opener-Policy: same-origin` y
  `Cross-Origin-Resource-Policy: same-origin`, y `Cache-Control` pasa de
  `no-store` a `private, no-store` para que ninguna caché compartida conserve
  la respuesta. Cambio de comportamiento: CORP `same-origin` hace que el
  navegador bloquee el uso embebido de las respuestas desde otro origen
  (`<img>`, `<script>`, `fetch` en modo `no-cors`); quien sirva recursos
  públicos a otros orígenes puede cambiar `Cross-Origin-Resource-Policy` (por
  ejemplo a `cross-origin`) en ese handler, igual que ya podía cambiar
  `Cache-Control`, porque todas las cabeceras se fijan antes de llamar al
  handler.
- **`httpx/middleware.StructuredLogger` registra el ID de correlación del
  cliente.** Si la request trae uno (`reqctx.ClientRequestIDFromContext`, que
  `RequestID` guarda saneado), el log de acceso lo incluye bajo
  `client_request_id`, separado del `request_id` generado por el servidor; sin
  él, la clave se omite.
- **`examples/boilerplate`: el arranque de la API falla si el PDP de Cerbos
  no responde.** Tras construir el checker se hace un `Ping` (cada intento
  acotado a 5s) que se reintenta con backoff exponencial (desde 500ms, máximo
  4s entre intentos) durante hasta 30s, para tolerar un PDP que tarda en
  levantar (orden de arranque de contenedores, un rollout de Cerbos). Si
  sigue sin responder, el arranque termina con un error que nombra el host,
  el modo (TLS o plaintext) y `CERBOS_PLAINTEXT`. Antes un despliegue que nunca definió
  `CERBOS_TLS` (y por eso usaba plaintext) arrancaba bien con el nuevo
  default TLS y respondía 503 en cada autorización.
- **`httpx/middleware.RequestContext` prefiere el ID de `RequestID`.** Toma
  el ID de request ya guardado en `reqctx` y solo si no hay ninguno recurre
  al `GetReqID` de chi, de modo que quien siga montando el
  `middleware.RequestID` de chi no ve cambios.
- **`httpx/middleware`: los logs de fallo de autenticación llevan contexto de
  la request.** `Authenticate` registra ahora `method`, `path`, `ip` y
  `request_id` (los dos últimos desde `reqctx`) junto al error, nunca el
  token. `RequireAccess`/`WriteAccessError` registran en nivel Warn la rama
  `access.ErrUnauthenticated` (antes respondía 401 sin dejar rastro) con los
  mismos campos que la denegación 403.
- **`examples/api` y `examples/boilerplate`: montan `vmw.RequestID`** en lugar
  del `middleware.RequestID` de chi.
- **`auth/zitadel`: el autenticador comprueba `azp` y `nbf` además de lo que
  verifica el SDK.** El verificador local de zitadel-go (`oauth.WithJWT`)
  comprueba firma, `iss`, `aud` y `exp`, pero no `azp` ni `nbf`. Ahora, tras
  aceptar el SDK el token, se rechaza (401, con el mismo mensaje genérico de
  siempre) un token cuyo `azp` no sea un string, un token cuyo `azp` falte, esté
  vacío o no figure en `Config.AllowedAuthorizedParties` cuando esa lista está
  configurada, y un token cuyo `nbf` sea posterior al instante actual más un
  margen de un minuto para el desfase de relojes. Sin
  `AllowedAuthorizedParties` el valor de `azp` no se compara con nada: en un
  access token `azp` es el cliente que lo pidió (en el montaje habitual, la app
  frontend), no esta API, así que exigir `azp == Config.ClientID` rechazaría
  todas esas peticiones.
- **`audit/postgres` y `workflow/postgres`: los listados acotan `Limit` y
  rechazan un `Offset` excesivo.** Antes `Repository.List` (audit) y
  `Repository.ListByEligibility` (workflow) pasaban cualquier `Limit` y
  `Offset` a Postgres (solo `audit/httpx` topaba `limit` en 100), y como
  Postgres lee y descarta cada fila saltada, un `offset` enorme forzaba un
  recorrido de toda la tabla. Ahora ambos paquetes de dominio exportan
  `DefaultListLimit` (20), `MaxListLimit` (1000), `MaxListOffset` (100000),
  `ErrOffsetOutOfRange` y el método `Page()` en `audit.Filters` y
  `workflow.InboxFilter`, que los repositorios usan: un `Limit` `<= 0` usa
  20, uno mayor que 1000 se recorta a 1000, y un `Offset` negativo o mayor
  que 100000 devuelve un error que envuelve `ErrOffsetOutOfRange` sin
  consultar la base. **Cambio de comportamiento** para quien pedía más de
  1000 filas por página (recibe 1000) o paginaba más allá de 100000 filas
  (recibe el error; conviene acotar por filtros, p. ej. rango de fechas).
  `audit/httpx`: `Handler.List` responde 400 `VALIDATION_ERROR` con
  `field: "offset"` ante ese error, con el nuevo mensaje configurable
  `Messages.InvalidOffset`. `examples/api` rechaza con 400 un `offset` mayor
  que `workflow.MaxListOffset` en el inbox antes de consultar.
- **`request`: `JSON`, `JSONWithLimit` y `JSONOptional` exigen un `Content-Type`
  JSON.** Antes decodificaban cualquier cuerpo sin mirar la cabecera. Ahora
  aceptan `application/json` y cualquier `application/*+json` (con parámetros
  como `charset`); cualquier otro tipo, o la cabecera ausente, responde HTTP
  415 con el nuevo código `response.CodeUnsupportedMediaType`
  (`UNSUPPORTED_MEDIA_TYPE`) y el nuevo mensaje `Messages.UnsupportedMediaType`
  (por defecto `Content-Type must be application/json or application/*+json`,
  que le dice al cliente qué se acepta).
  `JSONOptional` conserva su semántica: un cuerpo vacío sin cabecera sigue
  devolviendo `nil`; un cuerpo no vacío sin cabecera, o una cabecera no JSON,
  es 415, salvo que el cuerpo sin cabecera supere el límite de bytes
  (por ejemplo, solo espacios en blanco más allá del límite), que es 413 como
  con cabecera. Cambio de comportamiento para consumidores cuyos clientes no envían
  la cabecera: la opción `request.AllowAnyContentType()` en `request.New`
  desactiva el chequeo para ese `Decoder`. Es la única forma de desactivarlo:
  las funciones a nivel de paquete siguen siendo estrictas a propósito y no
  tienen interruptor.
- **`request`: el cuerpo debe contener exactamente un valor JSON.** Un segundo
  valor o cualquier byte distinto de espacio en blanco tras el primero
  (`{"a":1} {"b":2}`, `{"a":1}garbage`) responde HTTP 400 `INVALID_JSON` con el
  nuevo mensaje `Messages.TrailingData`. Antes el resto del cuerpo se ignoraba
  en silencio. Este chequeo no tiene opción de desactivación. Si la lectura
  del cuerpo falla después del primer valor (por ejemplo, se corta la
  conexión), no se informa como datos sobrantes: responde como cualquier otro
  error de lectura del cuerpo (HTTP 400 `INVALID_JSON` con
  `Messages.InvalidBody`, o 413 si se superó el límite) y se devuelve el error
  de lectura.
- **`config`: el error de `ValidURL` ya no incluye el valor.** Antes citaba la
  URL completa (y el error de `url.Parse`, que también la cita), así que una
  URL con credenciales, como el DSN de una base de datos, dejaba la contraseña
  en el log de arranque. Ahora el mensaje nombra la variable y un motivo fijo
  que no contiene ninguna parte del valor, para poder diagnosticar sin verlo:
  `must be a well-formed URL` (`url.Parse` la rechazó), `missing scheme`,
  `missing "//" before the host` (un `host:puerto` sin esquema, que
  `url.Parse` lee como `esquema:opaco`) o `missing host`. Cambio de
  comportamiento: el error ya no envuelve el `*url.Error` de `url.Parse`, así
  que `errors.As` sobre él deja de encontrarlo.
- **`notification`: `Message.Validate` exige que `To` y `From` sean una
  dirección válida.** Antes solo comprobaba que no estuvieran vacíos. Ahora
  cada uno debe ser una única dirección RFC 5322 según `net/mail.ParseAddress`,
  sola (`a@b.cl`) o con nombre (`Nombre <a@b.cl>`); se rechazan listas de
  varias direcciones y cualquier CR/LF, que permitiría inyectar cabeceras. El
  error nombra el campo pero no la dirección. `notification/sendgrid` separa
  el nombre de la dirección antes de llamar a la API (antes enviaba
  `Nombre <a@b.cl>` entero como dirección). Cambio de comportamiento: un
  mensaje con una dirección mal formada que antes llegaba al proveedor ahora
  falla en `Validate`.
- **`notification`: `Message.From` es opcional y el `defaultFrom` de los
  adaptadores por fin se aplica.** `smtp.NewSMTPNotifier` y
  `sendgrid.NewSendGridNotifier` reciben un `defaultFrom` para usar cuando el
  mensaje no trae `From`, pero `Send` llamaba antes a `Validate`, que
  rechazaba un `From` vacío, así que ese valor por defecto nunca se usaba.
  Ahora `Validate` acepta `From` vacío (si viene, sigue teniendo que ser una
  dirección válida) y `Send` usa `msg.From` o, si está vacío, `defaultFrom`;
  si ambos están vacíos, `Send` falla sin contactar al proveedor. Los
  constructores validan `defaultFrom` con `net/mail.ParseAddress` y devuelven
  error si es inválido (vacío sigue permitido), para que una configuración
  mala falle al arrancar. Los errores de los adaptadores ya no incluyen la
  dirección: antes `notification/smtp` citaba `From` y `To` en el mensaje.
- **`examples/boilerplate`: `api server` ya no aplica migraciones.** Antes
  `server` (y el binario sin argumentos) corría `migrate.Up` de todos los
  conjuntos al arrancar, lo que obligaba al rol que sirve a ser dueño de las
  tablas y le permitía deshabilitar los triggers append-only de `audit_log`.
  Ahora el arranque sigue las fases de Zitadel: `api migrate up` aplica las
  migraciones (con `DB_URL`, idealmente un rol migrador), `api server` solo
  sirve, y el nuevo `api start-from-migrate` migra y luego sirve, igual que
  el `server` anterior. `make dev` usa `start-from-migrate` y el compose de
  despliegue agrega un servicio `migrate` de una sola ejecución del que
  depende `api` (`service_completed_successfully`). Cambio de
  comportamiento: quien arranque con `server` debe correr `migrate up` antes
  de desplegar, o usar `start-from-migrate` en desarrollo.
- **`examples/boilerplate`: el worker ya no migra River al arrancar.** Antes
  `cmd/worker` corría `worker.EnsureSchema` y `queue.Migrate` en cada
  arranque, lo que exigía permiso `CREATE` al rol del worker y hacía migrar a
  cada réplica. Ahora `worker server` (y el binario sin argumentos) solo
  procesa jobs; `api migrate up` (y `api start-from-migrate`) crean el
  esquema `WORKER_SCHEMA` y aplican las migraciones de River, y el nuevo
  `worker start-from-migrate` migra River y luego procesa jobs, como el
  worker anterior. `api migrate down` nunca revierte River y `api migrate
  status` no lo lista. `make worker` usa `start-from-migrate` y en el compose
  de despliegue `worker` también espera al servicio `migrate`. Cambio de
  comportamiento: un worker desplegado sin correr antes `api migrate up`
  falla al arrancar si las tablas de River no existen.
- **`examples/api`: `POST /documents` ya no acepta `owner_id`.** Antes el
  dueño del documento venía del cuerpo de la request, así que cualquier
  usuario con permiso de escritura podía crear un documento a nombre de otro
  y darle el permiso de borrado reservado al dueño. Ahora el dueño es siempre
  el principal autenticado (`auth.FromContext`); sin principal la respuesta
  es 401. Cambio de comportamiento: como `request.JSON` rechaza campos
  desconocidos, un cuerpo que todavía envía `owner_id` recibe 400 en vez de
  ignorarse.
- **`httpx/middleware`: `SecurityHeaders` añade `Cache-Control: no-store` y
  `Pragma: no-cache`.** Las respuestas de una API autenticada son por usuario
  y no deben quedar en la caché del navegador ni de un proxy intermedio. Las
  cabeceras se fijan antes de llamar al siguiente handler, así que un handler
  que sirve contenido realmente cacheable (un recurso público, un esquema
  versionado) puede sobreescribir `Cache-Control` y borrar `Pragma` antes de
  escribir su respuesta.
- **`httpx/middleware`: `RequireAccess` y `WriteAccessError` registran cada
  denegación (403) en nivel `Warn`.** Antes el 403 era silencioso; solo el 503
  se logueaba. La línea `authorization denied` lleva `user_id`,
  `resource_kind`, `resource_id` (el parámetro `id` de la ruta o `*` cuando no
  hay), `action`, `method`, `path` y `request_id` (el que `RequestContext`
  pone en `reqctx`; vacío si ese middleware no está montado). No incluye el
  token ni ninguna cabecera. Desde `WriteAccessError` (dentro de un handler)
  `resource_kind`, `resource_id` y `action` van vacíos. El 401 sigue sin
  loguearse.
- **Versión mínima de Go: 1.26.** Corregir las vulnerabilidades alcanzables
  GO-2026-6603/6610/6611/6612/6617 exige `golang.org/x/net` v0.60.0, que
  declara `go 1.26.0` (igual que `x/text` v0.42.0 y `x/crypto` v0.57.0). No
  existe una versión corregida que conserve 1.25. Los consumidores necesitan
  Go 1.26 o superior, o `GOTOOLCHAIN` con descarga automática.
- Dependencias: `golang.org/x/net` v0.57.0 → v0.60.0, `google.golang.org/grpc`
  v1.83.0 → v1.83.1 (GO-2026-6348, alcanzable desde `authz/cerbos` y
  `notification/sendgrid`) y `github.com/go-chi/chi/v5` v5.2.3 → v5.3.2
  (GO-2026-5774/5775/5777 en `middleware.RealIP`). Nota: chi v5.3.2 sigue
  confiando en `X-Forwarded-For`, `X-Real-IP` y `True-Client-IP` de cualquier
  cliente; solo valida que el valor sea una IP. Montar `RealIP` únicamente
  detrás de un proxy confiable.
- **`storage`: `Storage.PresignedGetURL` acepta opciones variádicas
  (`...PresignGetOption`).** La firma del puerto cambia: toda implementación
  propia (fakes de test incluidos) debe añadir el parámetro
  `opts ...storage.PresignGetOption`; los llamadores existentes compilan sin
  cambios. Nuevas en `storage`: `PresignGetOptions`, `ApplyPresignGetOptions`
  (aplica los valores por defecto; los adaptadores deben usarla),
  `WithContentDisposition`, `AsAttachment` (construye
  `attachment; filename="<nombre>"` eliminando caracteres de control, incluidos
  CR, LF y tabulador, caracteres de formato Unicode como los que invierten la
  dirección del texto, separadores de línea, comillas y barras invertidas; con
  nombre vacío queda `attachment`. Un nombre con caracteres no ASCII se envía
  además como `filename*=UTF-8''<nombre codificado>` según RFC 5987, con un
  `filename` de respaldo donde cada carácter no ASCII pasa a `_`; el valor de
  la cabecera siempre es ASCII imprimible) y `WithResponseContentType`.
  `examples/api`: el presigner falso respeta el contrato de `expiry` (un valor
  `<= 0` usa un valor por defecto de 15m, como `storage/s3`).
- **`storage/s3`: las URL de descarga prefirmadas sirven el objeto como
  `attachment` por defecto.** `PresignedGetURL` fija
  `response-content-disposition=attachment` (firmado en la URL, no se puede
  quitar) y, con `WithResponseContentType`, `response-content-type`. Antes el
  navegador renderizaba un HTML o SVG subido en el origen del bucket. Cambio de
  comportamiento: quien necesite renderizado en línea pasa
  `storage.WithContentDisposition("inline")` explícitamente.
- **`storage`: las URL de descarga prefirmadas sirven
  `application/octet-stream` por defecto.** `ApplyPresignGetOptions` fija
  `ContentType` en la nueva constante `storage.DefaultResponseContentType`
  salvo que se pase `WithResponseContentType` con un valor no vacío, y
  `storage/s3` siempre firma `response-content-type`. Antes, sin esa opción,
  se servía el tipo guardado, que normalmente venía del cliente que subió el
  archivo. Junto con `attachment` por defecto, el navegador nunca renderiza
  en línea un objeto subido salvo que el consumidor lo pida. **Cambio de
  comportamiento**: quien dependía del tipo guardado pasa
  `WithResponseContentType` con un tipo verificado (p. ej. el que detectó
  `SniffContentType` al subir). `PresignGetOptions.ContentType` ya no queda
  vacío tras `ApplyPresignGetOptions`.
- **`storage/s3`: `Config.PresignExpiry` se respeta.** Se declaraba pero nada lo
  leía. Ahora `PresignedGetURL` y `PresignedPutURL` lo usan cuando el `expiry`
  de la llamada es `<= 0` (nueva constante `DefaultPresignExpiry` = 15m cuando
  tampoco está configurado). Un `expiry` positivo sigue mandando.
- **`audit`: `Recorder.Record` redacta valores sensibles antes de persistir.**
  Antes `audit_log` guardaba contraseñas, tokens y secretos tal como llegaban
  en `InputPayload`, los snapshots y `ChangedFields`. Ahora el valor de toda
  clave que coincide con la lista de denegación (`password`, `passwd`,
  `token`, `secret`, `authorization`, `api_key`, `private_key`, `credential`,
  `cookie`) se reemplaza por
  `audit.RedactedValue` (`[REDACTED]`) en los cuatro campos y a cualquier
  profundidad (mapas, slices y structs, estos en su forma JSON); la clave se
  conserva y un `nil` sigue siendo `nil`. En `ChangedFields` el nombre del
  campo se mantiene y se redactan `old` y `new` por separado. La coincidencia
  ignora mayúsculas, `_`, `-`, `.` y espacios, y es por subcadena
  (`access_token`, `refreshToken`, `client_secret`, `X-Api-Key` y
  `Set-Cookie` quedan cubiertas, y `credential` cubre `credentials`; también
  `token_count`, a propósito). `session` queda fuera a propósito porque
  redactaría `session_id` y contadores; se agrega con
  `WithRedactedKeys("session")`. La redacción falla cerrada: si un payload
  no se puede redactar (un mapa o slice que se contiene a sí mismo, un
  anidamiento de más de 1000 niveles, o un valor que `encoding/json` no
  codifica, como un canal, una función o un `NaN`),
  `Record` devuelve un error que envuelve `audit.ErrUnredactable` y no
  persiste nada; el valor de una clave sensible se reemplaza sin inspeccionarlo,
  así que nunca provoca ese error. Los
  mapas del llamador no se modifican. `NewRecorder` acepta opciones variádicas
  (`...RecorderOption`), así que los llamadores existentes compilan sin
  cambios: `WithRedactedKeys` amplía la lista y `WithRedactor` instala un
  hook propio (`audit.Redactor`). El valor que devuelve un hook no se guarda
  tal cual: se normaliza a su forma JSON y se recorre igual que el original,
  así que una clave de la lista de denegación dentro del reemplazo también se
  redacta y uno que no se puede redactar hace fallar `Record` con
  `audit.ErrUnredactable`. Dos slices que comparten el arreglo subyacente con
  distinta longitud (`s` y `s[:1]`) ya no se confunden con un ciclo. Cambio de
  comportamiento: quien leía esos valores desde el log de auditoría deja de
  verlos.
- **`workflow`: `Move`, `Claim` y `Release` usan control de concurrencia
  optimista.** Antes leían el caso y lo reescribían con
  `UPDATE ... WHERE id = $1`, así que dos llamadas concurrentes sobre el mismo
  caso podían leer el mismo estado y la última escritura pisaba a la primera
  en silencio (dos personas "tomaban" el mismo caso, o un `Move` deshacía un
  `Claim`). Ahora `workflow_case` tiene una revisión de fila: el nuevo campo
  `Case.Revision` (`int64`, distinto de `Case.Version`, que sigue siendo la
  versión de la definición) se lee en cada carga, `Create` lo deja en 0 y
  `Repository.Update` es un compare-and-swap
  (`... SET ..., revision = revision + 1 WHERE id = $1 AND revision = $n`) que,
  si tiene éxito, avanza también el `Case.Revision` en memoria. Si la revisión
  ya no coincide, `Update` devuelve el nuevo centinela `workflow.ErrConflict`
  (envuelto con contexto) y no escribe nada; si el caso no existe, sigue
  devolviendo `workflow.ErrCaseNotFound`. El motor escribe el caso ANTES de
  agregar eventos, así que un `Move`/`Claim`/`Release` en conflicto no agrega
  ningún evento. Aun así, el llamador debe hacer rollback de su transacción
  ante cualquier error (una guarda pudo haber escrito en ella). **Para
  reintentar:** ante `errors.Is(err, workflow.ErrConflict)`, hacer rollback y
  repetir la operación completa desde una lectura nueva (nueva transacción);
  un reintento acotado con backoff corto basta, y conviene volver a evaluar
  si la operación sigue teniendo sentido (p. ej. el caso ya fue tomado por
  otra persona). Bajo aislamiento `REPEATABLE READ`/`SERIALIZABLE`,
  PostgreSQL aborta al segundo escritor con un error de serialización
  (SQLSTATE `40001`) en vez de `ErrConflict`; también es seguro reintentarlo.
  Quien implemente `workflow.Repository` por su cuenta debe respetar el mismo
  contrato. Nueva migración `workflow/migrations/003_workflow_case_revision.sql`:
  agrega `workflow_case.revision BIGINT NOT NULL DEFAULT 0`. **Todo
  consumidor debe correr esta migración ANTES de desplegar esta versión de
  `vogel`** — igual que la 002, no es opcional:
  `workflow/postgres/repository.go` lee y escribe `revision` de forma
  incondicional, así que contra una base de datos sin la 003 fallan
  `Engine.Open`, `Move`, `Claim`, `Release`, `Available`, `EligibilityFor` e
  `Inbox`. Los casos existentes arrancan en revisión 0. Correrla vía
  `migrate.Up` con `workflow/migrations.FS()`, como la 001 y la 002.
  **Orden de despliegue:** la detección de conflictos solo funciona cuando
  TODOS los procesos que escriben `workflow_case` avanzan `revision`. Un
  binario compilado con una versión anterior de `vogel` actualiza el caso sin
  tocar `revision`, así que durante un despliegue gradual (binarios viejos y
  nuevos a la vez) un escritor nuevo puede leer la revisión n, un escritor
  viejo sobrescribir la fila (que sigue en n) y el nuevo confirmar su propia
  escritura en n: el cambio del viejo se pierde sin ningún `ErrConflict`, y
  los escritores viejos nunca detectan conflictos. Aplicar la 003, luego
  reemplazar TODOS los binarios viejos (o detener sus escrituras) antes de
  confiar en `ErrConflict`; mientras tanto se pueden seguir perdiendo
  actualizaciones concurrentes en silencio, igual que antes de este cambio.
- **Cambio incompatible (breaking): `authz/cerbos` usa TLS por defecto.**
  `cerbos.Config.UseTLS` desaparece y lo reemplaza `Plaintext bool`, una
  exclusión explícita: el valor cero conecta por TLS. Antes `UseTLS` valía
  `false` por defecto y eso significaba `cerbos.WithPlaintext()`, así que los
  principales, sus roles y las decisiones de autorización viajaban en texto
  plano salvo que el consumidor se acordara de activarlo. Migración:
  `UseTLS: true` → quitar el campo; `UseTLS: false` (o no declararlo) →
  `Plaintext: true`, solo si el PDP realmente escucha sin TLS (p. ej. el
  contenedor local de desarrollo). Un consumidor que no declaraba `UseTLS` y
  apunta a un PDP sin TLS deja de conectar hasta agregar `Plaintext: true`.
- **`examples/boilerplate`: `CERBOS_TLS` se reemplaza por `CERBOS_PLAINTEXT`**
  (`false` por defecto). Sin ella la conexión a Cerbos usa TLS; el `.env` de
  desarrollo del README la pone en `true` para el contenedor de
  `docker-compose.developer.yaml`, así que la experiencia local no cambia.
  `CERBOS_TLS` ya no se lee, y si sigue definida (con cualquier valor, incluso
  vacía) el arranque falla con un error que nombra `CERBOS_PLAINTEXT` como
  reemplazo, en lugar de pasar a TLS en silencio y responder 503 en cada
  autorización contra un PDP sin TLS.
- **Las llamadas HTTP salientes de `notification/sendgrid`, `storage/s3` y
  `auth/zitadel` tienen un timeout acotado por defecto.** Antes ninguna lo
  tenía: un proveedor que dejaba de responder colgaba la goroutine para
  siempre si el contexto del llamador no traía deadline.
  - `notification/sendgrid`: cada envío usa un `http.Client` propio con
    `Timeout` de 30s (`Config.HTTPTimeout`). El SDK solo arma la plantilla de
    la request; ya no se envía por `rest.DefaultClient` (un `http.Client` sin
    timeout y global), y como cada `Send` construye su propia request, los
    envíos concurrentes tampoco comparten el cuerpo que el SDK mutaba en el
    cliente. La respuesta se lee hasta 64 KiB. Si la lectura del cuerpo falla
    tras un 2xx, el envío se informa como aceptado y el error se registra en
    nivel Warn (sin datos personales): devolver error haría que un llamador
    que reintenta enviara el correo dos veces.
  - `storage/s3`: `s3.Options.HTTPClient` es un `awshttp.BuildableClient`
    que conserva los valores de transporte del SDK y agrega
    `ResponseHeaderTimeout` de 60s (`Config.ResponseHeaderTimeout`): la espera
    de las cabeceras de respuesta una vez escrita la request (incluido el
    cuerpo de un `Upload`). Si el valor es menor que los del SDK (30s de dial,
    10s de handshake TLS), también acota la conexión. No acota la
    transferencia del cuerpo de la respuesta ni fija `http.Client.Timeout`:
    un `Download` largo y sano sigue transmitiendo mientras el llamador lea,
    y la transferencia completa se acota con el contexto. El presign no hace
    I/O y no cambia.
  - `auth/zitadel`: el discovery OIDC y la descarga de JWKS (al construir y
    al refrescar ante un `kid` desconocido) usaban `http.DefaultClient`
    porque `oauth.DefaultJWTAuthorization` le pasa `nil` a `oauth.WithJWT`;
    ahora `New` llama a `oauth.WithJWT` con un cliente de timeout 10s
    (`Config.HTTPTimeout`).
- **`notification/sendgrid`: el log de envío exitoso ya no incluye
  destinatario ni asunto.** `email sent via SendGrid` registraba `to` (la
  dirección completa, con nombre) y `subject`; ambos son datos personales (un
  asunto suele nombrar a la persona o su caso). Ahora registra solo `status`
  y `message_id` (la cabecera `X-Message-Id` de SendGrid, que permite cruzar
  el envío con su activity feed sin exponer datos). Quien filtraba o contaba
  envíos por `to` o `subject` en sus logs debe usar `message_id`.
- **`notification/smtp`: el log de envío exitoso ya no incluye destinatario
  ni asunto**, por la misma razón que en `notification/sendgrid`. `email
  sent via SMTP` queda sin atributos.
- **`postgres`: `RequireTLS` exige que pgx verifique el certificado del
  servidor.** Antes bastaba con que el DSN habilitara TLS, así que
  `sslmode=require` (y `prefer`, el valor por defecto sin `sslmode`) pasaba el
  chequeo aunque pgx aceptara cualquier certificado: el tráfico iba cifrado
  pero un intermediario podía leerlo. Con `RequireTLS: true`, `NewPool` ahora
  falla al arrancar con `sslmode` `disable`, `allow`, `prefer` (o ausente) y
  `require` sin `sslrootcert`, y acepta `verify-full` (recomendado),
  `verify-ca` y `require` con `sslrootcert` (que libpq y pgx tratan como
  `verify-ca`). El chequeo lee la configuración ya parseada, por lo que cubre
  también DSN clave/valor, `PGSSLMODE` y DSN con varios hosts; el mínimo de
  TLS 1.2 se aplica ahora también a los hosts de respaldo (`Fallbacks`), que
  antes quedaban sin él. **Cambio de comportamiento para quien usa
  `RequireTLS` con `sslmode=require`:** cambiar el DSN a
  `sslmode=verify-full` (con `sslrootcert` si la CA del servidor no está en
  el almacén del sistema, como la de RDS). Con `RequireTLS: false` nada
  cambia. El ejemplo del comentario de `Config.URL` usa `verify-full`.
- **`examples/boilerplate`: el compose de despliegue separa los roles de la
  base y deja de publicarla.** `migrate`, `api` y `worker` se conectaban con
  el superusuario `postgres` y una contraseña fija en el archivo, y Postgres
  se publicaba en el puerto 5433 del host. Ahora `deploy/initdb/10-roles.sh`
  crea `app_migrator` (dueño del esquema; lo usan `migrate` y el nuevo paso
  `grants`) y `app_runtime` (`api` y `worker`: `SELECT` e `INSERT` sobre
  `audit_log`, `SELECT` sobre las tablas de versión, DML sobre las tablas de
  la aplicación y de River, y `MAINTAIN` en River desde PostgreSQL 17 para su
  reindexador), y `grants` aplica `deploy/initdb/runtime-grants.sql` después
  de cada `migrate up`. Las contraseñas salen de `deploy/.env`
  (`POSTGRES_PASSWORD`, `APP_MIGRATOR_PASSWORD` y `APP_RUNTIME_PASSWORD`,
  obligatorias: si falta una, `docker compose` falla antes de arrancar) y
  `api` y `worker` las reciben vacías salvo la propia. Postgres ya no publica
  puertos; el compose de desarrollo sigue publicando el 5433. El runbook
  documenta que en producción los DSN deben usar `sslmode=verify-full` contra
  un PostgreSQL con TLS. Quien ya usaba este compose debe agregar las tres
  variables a `deploy/.env` y actualizar el volumen existente sin borrarlo:
  `10-roles.sh` es idempotente y se puede correr a mano como superusuario,
  y el nuevo `deploy/initdb/upgrade-ownership.sql` transfiere a
  `app_migrator` las tablas, secuencias, funciones, tipos y el esquema de
  River que las migraciones anteriores crearon como `postgres` (sin
  `REASSIGN OWNED`, que PostgreSQL rechaza para el superusuario de arranque
  y que movería también objetos de toda la base). El procedimiento está en la
  sección 0 del runbook, «Actualizar un volumen existente».
- **`examples/boilerplate`: build de contenedores endurecido.** Nuevo
  `.dockerignore` (el `Dockerfile` hace `COPY . .`) que excluye `.env*`,
  claves y certificados, `.git`, `deploy/`, `bin/`, coverage y carpetas de
  herramientas locales, para que ningún secreto quede en una capa de la
  imagen. Las imágenes base (`golang:1.26-alpine3.23`, `alpine:3.23`) y la de
  Postgres del compose de despliegue quedan fijadas por digest, con el tag a
  la vista. Las imágenes de la aplicación ya no usan `:latest`: su tag sale
  de `APP_VERSION` en `deploy/.env` (obligatoria), y `migrate`, `api` y
  `worker` corren con el sistema de archivos raíz de solo lectura y un
  `tmpfs` en `/tmp`. Quien ya usaba este compose debe definir `APP_VERSION`.
- **`examples/boilerplate`: `/v1/healthz` ya no expone el error de la base,
  Swagger solo con opt-in explícito y CORS sin credenciales.** El chequeo
  profundo devolvía a cualquier llamador sin autenticar el texto del error
  del ping (`checks.database.error`), que puede nombrar hosts, puertos, roles
  y detalles de TLS; ahora responde solo `status: down` y el error queda en
  el log del servidor (`NewHealthHandler` recibe un `*slog.Logger`; `/readyz`
  también registra la causa), solo cuando el estado cambia: `Error` al pasar
  de sana a caída y `Info` al recuperarse, para que un llamador anónimo no
  llene el log con un error por cada sondeo durante una caída.
  `/v1/swagger/*` se montaba siempre; ahora solo si
  `RouterConfig.EnableSwagger`, que `main.go` toma de la nueva variable
  `SWAGGER_ENABLED` (por defecto `false`; con `APP_ENV=production` y `true`
  el arranque falla), bajo `SecurityHeaders` y con una CSP propia que
  permite el script y los estilos inline de swagger-ui. No depende de
  `APP_ENV` porque el compose de despliegue corre con `development` (su
  Postgres no sirve TLS) y no debe publicar la descripción de la API; en
  desarrollo local hay que agregar `SWAGGER_ENABLED=true` al `.env`. CORS pasa a
  `AllowCredentials: false`: la autenticación es un Bearer token, no una
  cookie.
- **`examples/api`: `/metrics` sale del router público.** Se sirve en un
  listener interno aparte (`METRICS_ADDR`, por defecto `127.0.0.1:2112`; no
  `9090`, el puerto por defecto del propio Prometheus), así que el puerto de
  la API ya no expone las métricas de Prometheus a cualquier llamador;
  `/healthz` sigue público. Si `METRICS_ADDR` está ocupada o es inválida, el
  arranque falla con un error que la nombra (probado). Un test recorre el router
  con `chi.Walk` y falla si alguna ruta bajo `/api/` se registra sin
  `RequirePermission` o `RequireAccess`.

## [0.8.0] — 2026-09-30

### Agregado

- `httpx/response`: el sobre de error admite un identificador estable por error
  de dominio (`error`, SCREAMING_SNAKE_CASE, p. ej. `CORREO_EN_USO`) y el campo
  ofensor (`field`), ambos `omitempty`. Nuevos `ErrorWith(w, r, status, code,
  message, opts...)`, `ErrorOption`, `WithErrorID` y `WithField`; `Error` conserva
  su firma y su JSON (aditivo, sin cambios incompatibles). `code` sigue siendo la
  categoría genérica; los clientes deben decidir por `error`, nunca por `message`.
- `httpx/response`: constante `CodeUnprocessableEntity` (`UNPROCESSABLE_ENTITY`).
- `httpx/response`: `ValidationErrorResponse` incluye `trace_id` (`omitempty`),
  igual que `ErrorResponse`.

## [0.7.0] — 2026-09-30

### Cambiado

- `audit/migrations/002_audit_log_append_only.sql`: `audit_log` pasa a ser
  append-only. Un trigger `BEFORE UPDATE OR DELETE ... FOR EACH ROW` y otro
  `BEFORE TRUNCATE ... FOR EACH STATEMENT` rechazan la operación con SQLSTATE
  `23001` (`restrict_violation`) y un mensaje que empieza con
  `audit_log is append-only`. Cambio de comportamiento, no de API: `Recorder` y
  el repositorio sólo hacen `INSERT` y `SELECT`. Quien mutaba `audit_log` (por
  ejemplo, un helper de test con `TRUNCATE audit_log`) fallará tras correr esta
  migración. Límites: el dueño de la tabla o un superusuario puede deshabilitar
  los triggers; roles, hash chaining y envío externo quedan fuera de alcance.
  Toda purga debe deshabilitar y rehabilitar los triggers en una misma
  transacción (receta en el encabezado de la migración). Corre con
  `migrate.Up` y `audit/migrations.FS()`, sobre tablas con filas existentes.

## [0.6.0] — 2026-09-22

### Agregado

- `workflow.CommentPolicy` (`""`/`"none"` por defecto, `"optional"`,
  `"required"`, con su propio `Valid()`) declara si un `Transition` acepta,
  exige o prohíbe un comentario en el `MoveInput` que lo toma: es historial
  de proceso, nunca dato de dominio. `Transition.Comment` fija la política
  por transición y `Definition.Validate` rechaza un valor no reconocido.
  `MoveInput.Comment` se recorta con `strings.TrimSpace` antes de validarse
  contra esa política y de guardarse (ya recortado) en el `Event.Comment`
  del `EventMoved` correspondiente; el `EventClosed` automático que `Move`
  agrega al entrar a un nodo terminal nunca repite ese comentario (queda
  vacío a propósito). La verificación corre ANTES de mutar el caso o de
  invocar la guarda de la transición, así que un `Move` rechazado por
  comentario no deja al motor a medio camino. Dos centinelas nuevos:
  `workflow.ErrCommentRequired` (política `required` con comentario vacío
  tras recortarlo) y `workflow.ErrCommentNotAllowed` (política `none`/vacía
  con un comentario no vacío, para que la política declarada no mienta).
  Nueva migración `workflow/migrations/002_workflow_event_comment.sql`:
  agrega `workflow_event.comment TEXT NOT NULL DEFAULT ''`. **Todo
  consumidor debe correr esta migración ANTES de desplegar esta versión de
  `vogel`, use o no `Event.Comment`** — no es opcional para quien no le
  interesa el comentario. `workflow/postgres/repository.go` arma
  `eventColumns` incluyendo `comment` de forma incondicional, así que contra
  una base de datos que sólo corrió `001_create_workflow.sql` CUALQUIER
  `AppendEvent`/`ListEvents` falla, y con ellos `Engine.Move`, `Claim`,
  `Release` e `History` — todos, no sólo los que tocan comentarios. El
  sentido seguro es el inverso: código viejo (que nunca lee ni escribe
  `Event.Comment`) sigue funcionando sin cambios contra el esquema ya
  migrado, porque la columna nueva es `NOT NULL DEFAULT ''`. Correrla vía
  `migrate.Up` con `workflow/migrations.FS()`, igual que ya corren
  `001_create_workflow.sql`.

- `workflow.NodeKind` (`""`/`"task"` por defecto, `"decision"`, con su propio
  `Valid()`) y el nuevo campo `Node.Kind` agregan al motor un nodo de
  decisión al estilo compuerta exclusiva BPMN: un caso ya no tiene que fundir
  la acción humana con su destino cuando ese destino depende de datos de
  dominio (p. ej. "¿algún ítem tiene subsidio SEP?" → coordinador SEP;
  si no, Presupuesto). Un `Transition` cuyo `From` es un nodo de decisión se
  llama ruta; `Engine.Move` evalúa las rutas salientes en el orden en que
  aparecen en `Definition.Transitions`, y toma la primera cuya `Guard`
  evalúa verdadero. `Definition.Validate` exige que un nodo de decisión: no
  sea `Start` ni `Terminal`; no declare `Eligible` ni `Deadline`; tenga al
  menos dos rutas salientes; tenga exactamente una ruta sin `Guard` (la
  ruta por defecto), declarada al final; y que ninguna de sus rutas declare
  `CommentPolicy` (una ruta no es una acción humana, no tiene nada que
  comentar). También rechaza cualquier ciclo formado sólo por nodos de
  decisión, porque un caso así nunca llegaría a descansar en ningún lado.
  Cuando `Engine.Move` aterriza en un nodo de decisión sigue enrutando,
  dentro de la misma llamada y la misma transacción `db`, hasta que el caso
  descansa en un nodo de tarea o terminal — un caso nunca se persiste
  descansando en un nodo de decisión. Cada salto agrega su propio
  `EventMoved` (con `Action` igual a la etiqueta de esa transición o ruta y
  `ActorID` igual al actor original de `MoveInput`), pero sólo el primer
  salto (el humano) lleva el comentario; los saltos automáticos siempre
  quedan con `Event.Comment` vacío. Si el caso termina descansando en un
  nodo terminal, el caso se cierra con un único `EventClosed` — pero su
  `Action` toma la etiqueta del ÚLTIMO salto de la cadena (la ruta automática
  ganadora), nunca el `MoveInput.Action` humano original cuando hubo saltos
  de por medio: describe cómo el caso llegó realmente al nodo terminal, no
  lo que pidió el humano. Centinela nuevo `workflow.ErrNoRoute`: si ninguna ruta
  calza (una `Definition` que de algún modo llegó al motor sin pasar por
  `Validate` y le falta la ruta por defecto), `Move` falla con este error y
  no muta el caso ni su historial — toda la cadena de saltos se planea en
  memoria antes de escribir nada. Un error real de una guarda de ruta se
  propaga tal cual, como hoy. Centinela adicional
  `workflow.ErrTooManyDecisionHops`: cinturón y tirantes contra una
  `Definition` patológica que de algún modo evadió el rechazo de ciclos de
  `Validate` — `Move` corta después de un número fijo de saltos
  (`maxDecisionHops`) en vez de girar para siempre.

- `workflow.Transition.Return` (D4): una transición de retorno, para que una
  etapa burocrática (un visto bueno) pueda devolver un caso a CUALQUIER nodo
  de tarea anterior que el caso haya ocupado de verdad — nunca a uno que
  saltó. `Engine.Available` la ofrece, y `Engine.Move` la acepta, sólo si el
  caso ocupó su `To` alguna vez, determinado a partir del historial de
  eventos propio del caso (el nodo de apertura más el `FromState`/`ToState`
  de cada `EventMoved`), nunca por alcanzabilidad en el grafo de la
  `Definition` — un caso que saltó al coordinador SEP nunca ve ofrecido
  "volver al coordinador SEP". Centinela nuevo `workflow.ErrReturnNotVisited`
  cuando `Move` recibe un retorno cuyo destino el caso nunca ocupó; falla sin
  mutar el caso ni su historial, igual que una guarda rechazada. El chequeo
  corre en un orden fijo, antes de mutar nada: primero la política de
  comentario (heredada de D2), después si el caso ocupó el destino, y recién
  al final la `Guard` de la transición, si tiene una — ambas condiciones
  (visitado Y guarda) deben pasar. Un retorno que pasa las tres corre
  exactamente como cualquier otro `Move`: se recalculan `Deadline` y
  elegibilidad para el nodo de destino y se limpia la asignación, sin
  semántica especial de "deshacer". `Definition.Validate` rechaza una ruta
  saliente de un nodo de decisión marcada como retorno (una ruta enruta
  automáticamente sobre datos de dominio; un retorno es una acción humana),
  rechaza un retorno que apunte a un nodo terminal, y rechaza un retorno que
  apunte a un nodo de decisión (un caso nunca descansa ahí; permitirlo
  reactivaría el enrutamiento automático, y un "retorno" podría terminar
  empujando el caso hacia adelante en vez de hacia atrás). `Engine.Available` y
  `Engine.Move` leen el historial del caso a lo sumo una vez por llamada, y
  sólo cuando alguna transición candidata es efectivamente un retorno — el
  costo extra es cero para cualquier `Definition` que no use D4.

### Cambiado

- **Cambio incompatible (breaking):** `workflow.GuardFunc` gana un segundo
  parámetro, `db pgxtx.DBTX`: `func(ctx context.Context, db pgxtx.DBTX, c
  Case) (bool, error)`. El motor pasa siempre el mismo `db` que recibió en
  `Engine.Available`/`Engine.Move`, nunca uno distinto ni `nil` salvo que el
  propio caller haya pasado `nil`, para que una guarda que lea datos de
  dominio (p. ej. "¿algún ítem tiene subsidio SEP?") vea las escrituras que
  el caller ya hizo antes en su misma transacción. Migración: agregar el
  parámetro `db pgxtx.DBTX` a la firma de cada `GuardFunc` registrada.

## [0.5.0] — 2026-09-22

Cierra las tres últimas brechas que le impedían a go-crucible borrar
`pkg/decimalutil`, su wrapper local de `Int64Query`/`BoolQuery`/`DecimalQuery`
y su validador go-playground montado a mano, cuyos errores hoy se envían
crudos al cliente (`{"validation": "<err.Error() de Go>"}`). Aditivo: ninguna
API de `v0.4.0` cambia.

### Agregado

- `request.Validator`: `Int64Query(r, param) *int64` y
  `BoolQuery(r, param) *bool`, con la misma forma que los demás `*Query`
  existentes (ausente/vacío → `nil` sin error; valor presente pero inválido →
  `nil` más un error de campo). `request.Messages.InvalidBoolean` (nuevo) e
  `InvalidDecimal` (nuevo, usado por `decimalx.Query` — ver abajo) se suman a
  `DefaultMessages`/`WithMessages`, y `Validator.Messages()` expone los
  mensajes efectivos de un `Validator` para que un paquete externo (como
  `decimalx`) registre un error con la misma redacción configurada, sin que
  `request` tenga que conocerlo ni importar su dependencia.
- Paquete nuevo `decimalx`: parser acotado de decimales anti-DoS, portado de
  `pkg/decimalutil` de go-crucible con identificadores y documentación en
  inglés. `Parse(s)` acota la longitud del string ANTES de construir nada,
  luego `decimal.NewFromString`, luego `ValidateBounds`; `ValidateBounds(d)`
  es la única definición de las cotas (exponente en `[MinExp, MaxExp]`
  primero, O(1); longitud del coeficiente vía `NumDigits` después) para un
  decimal que llegó ya construido (p. ej. por `encoding/json`, sin pasar por
  `Parse`). `Bounds`/`NewBounds` permiten cotas propias, pero `NewBounds`
  rechaza toda configuración que desactive la guarda (longitud no positiva o
  por encima de `MaxLengthLimit`, un techo duro de 1000 caracteres; rango
  invertido; o un exponente que cruce `MaxExponentLimit`, un techo duro
  de ±1000). La regla de dinero, con la escala como PARÁMETRO (no fija en 2
  como en crucible): `ValidateAmount(d, scale)` corre `ValidateBounds`
  primero y rechaza — sin redondear nunca en silencio — lo que no sobrevive a
  `d.Round(scale)`; `ParseAmount(s, scale)` es `Parse` + `ValidateAmount`. La
  propia `scale` está acotada a `[-MaxExponentLimit, MaxExponentLimit]`
  (`ErrInvalidScale`) antes de llegar a `Round`, por la misma razón de
  materialización que las cotas de `Bounds`. `decimalx.Query(v, r, param)`
  vive en este paquete (no en `request`, que no debe importar
  `shopspring/decimal`) y NO rechaza negativos, porque un monto como el total
  de una nota de crédito es legítimamente negativo. Semántica deliberadamente
  conservada de crucible: rellenar un literal con ceros más allá del
  exponente configurado se rechaza en la cota, aunque el valor resultante sea
  exacto a la escala pedida (`"100.500"` se acepta, `"100.000000000"` se
  rechaza a pesar de valer lo mismo a escala 2); y la cota de `Parse` mide
  caracteres del STRING (el signo cuenta) mientras que la de `ValidateBounds`
  mide dígitos del COEFICIENTE (el signo no cuenta), así que la puerta de
  string es más estricta, nunca más laxa.
- Subpaquete nuevo `decimalx/decimalxtest`:
  `AssertNoDirectNewFromString(t, root, dirs...)`, un guardián estructural
  (basado en un regex sobre archivos `.go` no-test) que falla si algún
  archivo fuera de `decimalx` llama a `decimal.NewFromString`,
  `RequireFromString` o `NewFromFormattedString` directamente en lugar de
  pasar por `decimalx.Parse` — portado de
  `pkg/decimalutil/guard_test.go` de go-crucible.
- Paquete nuevo `validation`: traduce los errores de
  `go-playground/validator/v10` a la misma forma que ya usa
  `request.Validator`, `request.FieldErrors`. `validation.New(opts...)`
  construye un `*validator.Validate` con
  `validator.WithRequiredStructEnabled()` y un `RegisterTagNameFunc` que
  nombra cada campo según su tag `json` (el nombre antes de la primera coma;
  `json:"-"` o sin tag cae al nombre del campo de Go, igual que
  `encoding/json`). `(*Validator).Struct(v)` retorna `(nil, nil)` si `v` es
  válido; ante `validator.ValidationErrors` retorna una entrada por campo,
  con clave igual a la ruta JSON (`fe.Namespace()` sin el segmento raíz:
  `"address.street"`, `"items[0].monto"`) y valor un mensaje de
  `validation.Messages` (`Required` — usado también para las variantes
  condicionales `required_if`, `required_unless`, `required_with`,
  `required_with_all`, `required_without` y `required_without_all`, en vez de
  caer en `Default` —, `Min`/`Max` — conscientes del `reflect.Kind` del
  campo, porque "al menos 3" significa algo distinto para un string, una
  colección o un número —, `OneOf`, y `Default` como respaldo para cualquier
  otro tag). Un campo embebido (anónimo) se aplana igual que `encoding/json`:
  sin tag `json` propio, no aporta segmento — `Base.id` nunca se filtra, la
  clave es `id` — pero con un tag `json` explícito (`Base json:"base"`) se
  conserva como segmento ordinario (`base.id`); aplica también embebido por
  puntero, dentro de un struct anidado, y dentro de elementos de un `dive`.
  Cualquier otro error —
  `*validator.InvalidValidationError` si `v` no es un struct o es `nil` — se
  retorna tal cual: nunca se envía `err.Error()` al cliente.
  `(*Validator).Write(w, r, v)` valida y escribe la respuesta:
  `response.ValidationError` con las claves por campo si es inválido,
  `response.Error` (500, `CodeInternalError`) sin exponer el texto del error
  de Go si es un error de programación. Hay instancias y funciones de
  paquete (`validation.Struct`, `validation.Write`) con `DefaultMessages`,
  igual que `request`. Vive separado de `request` para que un consumidor que
  no valida structs no arrastre `go-playground/validator/v10`.
- Nuevas dependencias directas: `github.com/shopspring/decimal` v1.4.0 (para
  `decimalx`) y `github.com/go-playground/validator/v10` v10.30.1 (para
  `validation`). Ninguna de las dos la importa `request`
  (`go list -deps ./request` lo verifica).

## [0.4.0] — 2026-09-22

Cierra las brechas que impedían a go-crucible borrar sus forks locales de
`request` y `httpx/response`. Compatible hacia atrás: con opciones por defecto,
las respuestas son byte a byte iguales a `v0.3.0`.

### Agregado

- `httpx/response`: `CodeExportTooLarge = "EXPORT_TOO_LARGE"`, para HTTP 413
  cuando un export pedido supera el tope de filas del servidor (distinto de
  `CodeBodyTooLarge`, que es sobre el cuerpo de la solicitud).
- `request.JSONOptional`: un cuerpo vacío devuelve `nil` sin escribir nada y
  deja el destino sin tocar; cualquier otro error se comporta igual que `JSON`.
- `request.Messages`, `request.DefaultMessages`, `request.WithMessages` y
  `request.Decoder` (vía `request.New`): todos los textos de decodificación y
  del `Validator` son localizables, configurados una vez por aplicación. Los
  campos vacíos conservan el texto en inglés por defecto. Las funciones de
  paquete (`JSON`, `JSONWithLimit`, `JSONOptional`, `NewValidator`) usan una
  instancia por defecto. Un `Decoder` que no pasó por `New` (valor cero o
  puntero nil) usa los mensajes por defecto en vez de entrar en pánico.
- `Validator.AddError`: la forma soportada de registrar errores desde parsers
  propios de la app que envuelven al `Validator`.

### Notas

- `Validator.MaxInt` no tiene mensaje: solo acota el valor y nunca registra un
  error, igual que en el fork de go-crucible.
