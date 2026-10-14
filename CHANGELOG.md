# Changelog

Todos los cambios relevantes de `vogel`. El formato sigue
[Keep a Changelog](https://keepachangelog.com/es-ES/1.1.0/) y el versionado,
[SemVer](https://semver.org/lang/es/).

## [Unreleased]

### Agregado

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

- **`httpx/middleware.StructuredLogger` registra el ID de correlación del
  cliente.** Si la request trae uno (`reqctx.ClientRequestIDFromContext`, que
  `RequestID` guarda saneado), el log de acceso lo incluye bajo
  `client_request_id`, separado del `request_id` generado por el servidor; sin
  él, la clave se omite.
- **`examples/boilerplate`: el arranque de la API falla si el PDP de Cerbos
  no responde.** Tras construir el checker se hace un `Ping` acotado a 5s; si
  falla, el arranque termina con un error que nombra el host, el modo (TLS o
  plaintext) y `CERBOS_PLAINTEXT`. Antes un despliegue que nunca definió
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
