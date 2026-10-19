# Cómo se compone vogel

El README documenta cada paquete por separado. Este documento no repite ese
mapa: es sobre lo que deja afuera, el cableado entre paquetes — quién
escribe en qué, en qué orden hay que montar las piezas, y qué se rompe
(casi siempre en silencio) si el orden es el equivocado. Verificado contra
el código de `reqctx`, `logger`, `httpx/middleware`, `auth`, `authz`,
`access`, `audit`, `pgxtx`, `postgres`, `migrate`, `worker` y `workflow`, y contra el
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
No genera un request ID propio: lo lee del que dejó `vmw.RequestID` en
`reqctx`, así que ese middleware tiene que estar montado más arriba en la
cadena. Si no lo encuentra, cae a `chimw.GetReqID`, de modo que un
consumidor que todavía monta `chimw.RequestID` sigue funcionando; pero el de
chi adopta tal cual un `X-Request-Id` enviado por el cliente, así que con él
es el cliente quien elige el `request_id` de logs, filas de `audit_log` y
respuestas de error. `vmw.RequestID` siempre genera uno propio y guarda el
del cliente aparte (`reqctx.WithClientRequestID`), sólo para correlación.

```go
// httpx/middleware/requestcontext.go
func RequestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := reqctx.RequestIDFromContext(r.Context())
		if id == "" {
			id = chimw.GetReqID(r.Context())
		}
		ctx := reqctx.WithRequestID(r.Context(), id)
		ctx = reqctx.WithUserSlot(ctx) // ver «El usuario autenticado en el log»

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

Los métodos `*Context` (`InfoContext`, `ErrorContext`, …) de un logger creado
con `logger.New` hacen lo mismo sin `WithContext`: el handler lee `reqctx` en
cada línea y agrega `"request_id"` y `"user_id"` cuando el contexto los trae.
Una clave que la línea o el logger ya llevan (por `WithContext`, un `With` o
un argumento explícito, como hacen los logs de `Authenticate` y
`RequireAccess`) no se repite, y gana el valor explícito. Fuera de una request
no se agrega nada. Para que también lo hagan las llamadas a `slog.InfoContext`
y las librerías que loguean por `slog.Default()`, el binario instala el logger
con `slog.SetDefault(appLogger.Logger)`.

El request ID también cruza la cola de jobs. `worker.NewRiverQueue` instala
un middleware de River que, al encolar, copia el request ID del contexto a la
metadata del job (clave `vogel_request_id`, sin tocar los args) y, al
procesarlo, lo devuelve al contexto con `reqctx.WithRequestID`. Así los logs
del worker y las filas de `audit_log` que graba (con `audit.SourceWorker`)
llevan el `request_id` de la request que encoló el job. Un job encolado fuera
de una request (uno periódico, una CLI) no lleva ninguno, y un
`vogel_request_id` puesto a mano en `InsertOpts.Metadata` gana sobre el del
contexto. La misma cola pasa el logger de la app a River, así que las líneas
de River salen con el mismo formato que las de la app, pero sólo desde
`WARN`: es el piso del logger propio de River, que por debajo es muy verboso.

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

r.Use(vmw.RequestID)      // genera el request ID; nunca adopta el del cliente
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

### El usuario autenticado en el log: el slot de `reqctx`

Las líneas de log de una request autenticada llevan además `user_id`, para
poder responder «qué hizo este usuario» sin cruzar con `audit_log`. El dato
lo conoce `Authenticate`, pero quien escribe la línea de acceso,
`StructuredLogger`, está montado **por fuera** de `Authenticate`: el
`Principal` que este guarda viaja en una request derivada
(`r.WithContext`) que el logger nunca ve, porque al terminar la request sigue
teniendo el contexto anterior. Un valor de contexto común no alcanza.

Por eso `reqctx` tiene, además de las claves inmutables, un **slot**
mutable:

1. `RequestContext` instala el slot vacío (`reqctx.WithUserSlot`): es un
   puntero, así que todo contexto derivado de ahí —el que ve el logger y el
   que ve el handler— comparte la misma celda. Si ya hay un slot, no crea
   otro (uno interno ocultaría al externo).
2. `Authenticate`, al validar el token, escribe en el slot
   `reqctx.SetUserID(ctx, principal.UserID)`: **solo el ID opaco** del
   usuario, nunca el username, los roles ni el token, porque el valor
   termina en cada línea de acceso. Una request rechazada no escribe nada.
3. Lo leen `reqctx.UserIDFromContext`:
   - `StructuredLogger`, en la línea de acceso que emite al terminar la
     request (después de que `next` volvió, así que la escritura ya ocurrió);
   - `logger.Logger.WithContext(ctx)` y los métodos `*Context` del logger,
     en las líneas que escriben los handlers y servicios.

   Ambos agregan `"user_id"` solo si hay un valor: una request anónima, o un
   log fuera de una request, conserva la forma de siempre (sin la clave).

La celda es un `atomic.Pointer` porque un handler envuelto en
`http.TimeoutHandler` corre en otra goroutine que puede seguir —y escribir el
slot— después de que el logger de afuera ya está leyendo; con el atómico la
línea lleva el ID o nada, sin carrera de datos.

**Requiere `RequestContext` más arriba que `StructuredLogger` y
`Authenticate`**, que es el orden documentado. Sin él no hay slot:
`SetUserID` no hace nada, la autenticación sigue funcionando y simplemente
no se registra `user_id` — la misma degradación silenciosa que el
`request_id` vacío. El slot no reemplaza a `auth.FromContext`: para decidir
algo con el usuario, los handlers siguen leyendo el `Principal`.

Las marcas de tiempo de `logger.New` / `logger.NewDefault` se emiten siempre
en UTC, sea cual sea la zona horaria del host, para poder ordenar y
correlacionar líneas de servicios en zonas distintas (y con `audit_log`, cuyo
`created_at` es `timestamptz`) sin aritmética de offsets. Solo se convierte
el `time` del registro; un `time.Time` que el código loguea como atributo
propio conserva su zona.

`logger.New` / `logger.NewDefault` también redactan: el valor de todo
atributo cuya clave coincide con la lista de `logger.DefaultRedactedKeys`
(`password`, `token`, `secret`, `authorization`, `api_key`, `cookie`, …, la
misma de `audit`, con las mismas reglas: sin distinguir mayúsculas, ignorando
`_`, `-`, `.` y espacios, por subcadena) se escribe como `"[REDACTED]"`, a
cualquier profundidad de grupo y también en lo que se fijó con `With`.
`logger.WithRedactedKeys(...)` agrega claves. Alcanza a la línea de acceso y
a todo log que pase por el `*slog.Logger` del `Logger` (o por `slog.Default()`
tras `slog.SetDefault`). No inspecciona un struct o mapa logueado como un solo
atributo, ni el SQL que registra el tracer de queries lentas de `postgres`
(que nunca registra los argumentos).

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

### Revocación de tokens: introspección opcional en `auth/zitadel`

Por defecto `auth/zitadel` verifica el access token localmente (firma con el
JWKS de Zitadel, solo con `RS256`, `ES256` o `PS256`; `iss`, `exp`, `aud`,
más los chequeos propios de `nbf`, `iat` no futuro, `MaxTokenLifetime` y
`AllowedAuthorizedParties`). Es rápido y no depende de
Zitadel en cada request, pero un token revocado o una sesión cerrada siguen
valiendo hasta su `exp`.

Si la aplicación necesita revocación (cerrar sesión en todos lados, dar de
baja a un usuario o un cliente comprometido y que el corte sea inmediato),
se activa la introspección (RFC 7662):

```go
authn, err := zitadel.New(ctx, zitadel.Config{
	Issuer:   issuer,
	ClientID: apiClientID,
	Introspection: &zitadel.IntrospectionConfig{
		KeyFile: "/run/secrets/zitadel-api-key.json", // o ClientSecret: ...
	},
})
```

- **Credencial:** el endpoint de introspección autentica a la API. Se usa
  exactamente una: `KeyFile` (el `key.json` de la aplicación API, perfil
  JWT) o `ClientSecret` (basic auth con `Config.ClientID`).
- **Costo:** cada token no cacheado es un round trip a Zitadel, acotado por
  `Config.HTTPTimeout`. Si Zitadel no responde, o rechaza las credenciales
  de la API, la request recibe 503 (`auth.ErrServiceUnavailable`), no 401:
  la caída del proveedor nunca se confunde con un token inválido. Los
  misses concurrentes del mismo token se fusionan en una sola llamada
  (`singleflight` por el SHA-256 del token): una ráfaga de requests con un
  token nuevo cuesta un round trip, y un llamador que cancela su `ctx`
  vuelve enseguida sin abortar la llamada que comparten los demás.
- **Avalanchas de tokens distintos:** no hay caché negativa (los tokens
  inválidos aleatorios desalojarían a los buenos), así que cada token
  desconocido distinto sigue costando una llamada a Zitadel. Una avalancha
  sin autenticar de tokens aleatorios se convierte en una introspección por
  request: hay que poner rate limiting delante de las rutas autenticadas,
  por ejemplo `httprate.LimitBy` con la IP real del cliente (resuelta por
  `httpx/middleware.RealIP` con proxies de confianza), como hace
  `examples/boilerplate` en su router.
- **Caché:** los resultados activos se guardan en memoria durante
  `CacheTTL` (30s por defecto), nunca más allá del `exp` del token, con a lo
  sumo `CacheMaxEntries` entradas (10000 por defecto, desaloja la menos
  usada). La clave es el SHA-256 del token, nunca el token. Los resultados
  inactivos no se cachean. El compromiso: un token revocado puede seguir
  aceptándose hasta `CacheTTL` después de la revocación; bajar el TTL acorta
  esa ventana a cambio de más llamadas a Zitadel. La caché es por proceso:
  cada réplica hace sus propias llamadas.
- **Chequeos:** sobre el resultado de la introspección siguen corriendo
  `nbf`, `iat` no futuro, `MaxTokenLifetime` y `AllowedAuthorizedParties`, y además se exige
  que `aud` incluya `Config.ClientID`, igual que en la verificación local.
  Un token inactivo es el mismo 401 genérico que cualquier token inválido.

### El mapeo de códigos

Ambos middlewares comparten la misma regla de tres vías, y la corrección 13
del README es la que la fija así de manera explícita — reemplazando el
DEC-08 de go-licencias (que colapsaba una caída de Cerbos en 403 "para
evitar un oráculo") y el 500 que devolvía go-crucible en el mismo caso:

| Situación | `Authenticate` | `RequirePermission` | `access.Guard.Check` + `WriteAccessError` |
|---|---|---|---|
| credencial ausente/inválida | `auth.ErrUnauthenticated` (o cualquier error no reconocido) → **401** | sin `Principal` en contexto → **401** | sin `Principal` en contexto → `access.ErrUnauthenticated` → **401** |
| denegación genuina | `auth.ErrForbidden` → **403** | `checker.IsAllowed` devuelve `(false, nil)` → **403** | `checker.IsAllowed` devuelve `(false, nil)` → `access.ErrForbidden` → **403** |
| el proveedor falla | `auth.ErrServiceUnavailable` → **503** | `checker.IsAllowed` devuelve error no nulo → **503** | `checker.IsAllowed`, o el resolutor `PrincipalAttributes`, devuelve error → `access.ErrUnavailable` → **503** |

La última columna es la de un chequeo por instancia, hecho a mano en el
handler con `access.Guard.Check` + `httpx/middleware.WriteAccessError` — ver
más abajo, "El chequeo por instancia: `access.Guard`" — no la de
`RequireAccess`, que es la contraparte de `RequirePermission` montada en el
router y comparte exactamente el mismo mapeo por estar construida sobre el
mismo `Guard`.

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

### El chequeo por instancia: `access.Guard`

`RequirePermission` (y su primo `RequireAccess`, ver abajo) sólo pueden hacer
un chequeo **grueso**: corren antes de que el handler cargue nada, así que lo
único que tienen para armar el `authz.Resource` es el `resourceKind` fijo del
middleware y, a lo sumo, el `id` de la URL. Una decisión que dependa de un
dato de la entidad misma — su dueño, su estado, si ya fue enviada — no es
posible ahí. `access.Guard` es la pieza que hace ese segundo chequeo, **fino**,
desde el handler, después de su propia carga:

```go
// access/access.go
type Guard struct{ /* ... */ }

func New(checker authz.Checker, opts ...Option) *Guard
func WithPrincipalAttributes(fn PrincipalAttributes) Option

func (g *Guard) Check(ctx context.Context, resource authz.Resource, action string) error
```

`PrincipalAttributes` es la pieza que resuelve atributos del **principal** —no
del recurso— desde otro sistema (p. ej. qué documentos tiene asignados
actualmente el usuario, según un servicio de asignaciones), y que
`Guard.Check` (vía `Guard.Principal`) vuelca en `authz.Principal.Attr` antes
de invocar `checker.IsAllowed`. Sin esa opción, `Guard` se comporta
exactamente igual que antes de que existiera: `Attr` queda `nil`.

Uso típico en un handler, después de cargar la entidad (adaptado de
`examples/api/handler.go`, `DocumentHandler.Delete`):

```go
doc, err := h.store.GetByID(r.Context(), db, id)
// ... manejo de error / 404 ...

resource := authz.Resource{
	Kind: "documents:document",
	ID:   doc.ID.String(),
	Attr: map[string]any{"owner": doc.OwnerID, "kind": doc.Kind},
}
err = h.guard.Check(r.Context(), resource, "delete")
if err != nil {
	if vmw.WriteAccessErrorFor(w, r, err, h.logger, resource, "delete") {
		return
	}
	// error no reconocido por WriteAccessErrorFor: es del propio handler
}
```

`httpx/middleware.WriteAccessErrorFor` hace lo mismo que `WriteAccessError`
pero además registra en la línea de log `resource_kind`, `resource_id` y
`action` del chequeo por instancia (nunca `Attr`); `WriteAccessError` los deja
vacíos y se conserva por compatibilidad. Ambas son la contraparte, dentro del handler,
del mapeo de estados que `RequirePermission`/`RequireAccess` ya hacen en el
router: mapea `access.ErrUnauthenticated`/`ErrForbidden`/`ErrUnavailable` a
401/403/503 (logueando el 403 en `Warn` con `user_id`, `method`, `path` y
`request_id`, y el 503 en `Error`), y devuelve `false` sin escribir
nada para cualquier otro error, de modo que el handler puede seguir con su
propio mapeo de errores de dominio.

`httpx/middleware.RequireAccess` es el reemplazo de `RequirePermission`
cuando el mismo `Guard` también respalda un chequeo por instancia más
adelante en el mismo handler:

```go
// httpx/middleware/access.go
func RequireAccess(guard *access.Guard, resourceKind, action string, logger *slog.Logger, opts ...AuthOption) func(http.Handler) http.Handler
```

`RequirePermission` en sí mismo pasó a ser un envoltorio delgado sobre
`access.New(checker)` + `RequireAccess` — el mapeo de estados vive una sola
vez, no dos.

**El memo por request.** Cuando `guard` tiene un `PrincipalAttributes`
configurado, `RequireAccess` instala `access.WithRequestScope` en el
contexto de la request ANTES de correr su propio chequeo. Eso significa que
el chequeo grueso del middleware y el chequeo fino que el handler hace más
tarde con el mismo `Guard` comparten un único resultado resuelto: el
resolutor corre como máximo una vez por request, sin importar cuántas veces
se llame a `Check` — no dos consultas al sistema de asignaciones por la
misma request, una por cada capa de chequeo. Cuando `guard` no tiene
resolutor configurado, `RequireAccess` no toca el contexto de la request en
absoluto — el comportamiento es, literalmente, el mismo que tenía
`RequirePermission` antes de que este mecanismo existiera. Fuera de una
request HTTP —un worker, una CLI— `Guard.Check` sigue funcionando sin ese
memo: simplemente resuelve los atributos de nuevo en cada llamada, que es lo
correcto cuando no hay ningún "request" sobre el cual amortizar la consulta.

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

Esa caída al pool es silenciosa: si alguien olvida `WithTx`, o pasa el
contexto de afuera en vez del `txCtx` del callback, la sentencia corre en una
**segunda conexión**, fuera de la transacción, mientras la primera sigue
tomada. Con el pool agotado, dos requests así se bloquean entre sí. El código
que sólo tiene sentido dentro de una transacción (un paso de una unidad de
trabajo, un método que depende de un lock tomado antes en la misma
transacción) usa `pgxtx.RequireTx(ctx)`, que devuelve `pgxtx.ErrNoTx` en vez
de caer al pool.

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
  periódicos por error (ver `WithPeriodicJobs`). `WithMetrics` y
  `Config.PollOnly` también se ignoran ahí: no hay jobs que medir ni `LISTEN`
  que evitar.
- **Binario worker:** construye la cola con un `*river.Workers` real
  (registrado vía `river.AddWorker`), y llama a `Start`, que bloquea
  procesando jobs hasta `Stop`. Con `WithMetrics(reg)` registra en `reg` el
  histograma `river_job_duration_seconds{kind,outcome}` (`outcome`:
  `success`, `error`, `cancel`, `snooze`, `panic`), medido por un middleware
  que corre después del de request ID.

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
  bug de origen del punto 16 del README. Arreglo: `vmw.RequestID` →
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

## 7. Varias réplicas y PgBouncer

Cada réplica del API y del worker abre su propio pool. El total no puede
pasar de `max_connections` de PostgreSQL (100 por defecto), menos las que
reservan las herramientas de administración y las migraciones:

```
réplicas_api × DB_MAX_CONNS(api) + réplicas_worker × DB_MAX_CONNS(worker) + reserva < max_connections
```

Lo que se calcula por el hardware del **servidor de base de datos**, no por
la cantidad de usuarios ni de goroutines, es el **presupuesto total** de
conexiones activas del servidor: el punto de partida habitual es
`(núcleos × 2) + discos`. Más conexiones activas que eso no aumentan el
throughput; sólo agregan contexto que conmutar y memoria por sesión. Ese
presupuesto se **reparte** entre todas las réplicas: la suma de sus
`DB_MAX_CONNS` debe quedar cerca de él, no cada pool por separado. Cuando el
reparto deja a cada réplica con un pool demasiado chico para su
concurrencia, lo que corresponde es poner un pooler (PgBouncer) delante, no
subir los pools ni `max_connections`.

**PgBouncer en modo `transaction`** reparte una conexión del servidor por
transacción, y eso rompe tres supuestos de este módulo:

1. **Los `SET` por conexión de `postgres.NewPool`.** `statement_timeout`,
   `lock_timeout` e `idle_in_transaction_session_timeout` se aplican con `SET`
   en `AfterConnect`, sobre la conexión con PgBouncer, no con el servidor: la
   transacción siguiente puede caer en otra conexión del servidor que no los
   tiene. Fijarlos en el rol, que PgBouncer no puede perder:

   ```sql
   ALTER ROLE app_runtime SET statement_timeout = '30s';
   ALTER ROLE app_runtime SET lock_timeout = '5s';
   ALTER ROLE app_runtime SET idle_in_transaction_session_timeout = '60s';
   ```

   `NewPool` sigue enviando sus `SET` igual, y detrás de PgBouncer quedan
   pegados en la conexión del servidor que después atiende a otro cliente. Por
   eso los tres campos de `postgres.Config` deben tener **los mismos valores**
   que el `ALTER ROLE` (así el `SET` es redundante e inofensivo), y **nunca un
   valor negativo**: negativo envía `SET ... = 0`, que desactiva el timeout
   para cualquier cliente que reciba después esa conexión.

2. **La caché de sentencias preparadas de pgx.** pgx prepara cada consulta en
   la conexión (`cache_statement`). En modo transacción la sentencia preparada
   queda en otra conexión del servidor y falla con `prepared statement ...
   does not exist`. Opciones: `default_query_exec_mode=exec` en el DSN (pgx lo
   lee de ahí), o PgBouncer ≥ 1.21 con `max_prepared_statements` > 0, que
   sigue las sentencias preparadas por cliente.

3. **`LISTEN`/`NOTIFY` de River.** El worker escucha notificaciones para
   enterarse de jobs nuevos, y `LISTEN` necesita una sesión estable. Con
   `worker.Config.PollOnly = true` (en el boilerplate, `WORKER_POLL_ONLY=true`)
   River no emite `LISTEN` y busca jobs nuevos consultando, así que el worker
   puede ir por el pool de PgBouncer en modo transacción. El costo es
   latencia: un job nuevo puede esperar hasta `FetchPollInterval` de River
   (1 s por defecto) antes de tomarse, y si el líder renuncia, otro tarda
   hasta cinco segundos en elegirse. Sin `PollOnly`, el worker se conecta
   directo a PostgreSQL o a un pool de PgBouncer en modo `session`. El API,
   que sólo encola, puede ir por el pool en modo transacción en cualquier
   caso (`PollOnly` no aplica a un cliente insert-only).

Con PgBouncer, `DB_MAX_CONNS` de cada réplica pasa a ser conexiones con
PgBouncer (baratas), y lo que se calcula por hardware es `default_pool_size`
de PgBouncer.
