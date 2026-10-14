# Modelo de amenazas de `vogel`

`vogel` no es un servicio: es una librería que corre **dentro** de cada
servicio consumidor. Este documento dice, para cada punto de entrada de la
librería, qué amenaza cubre `vogel`, qué queda a cargo del consumidor y qué
riesgo residual se acepta. Sirve para revisar un PR que toca seguridad y para
que un servicio nuevo sepa qué le falta configurar.

**Lectura rápida:**

1. [Contexto y límites de confianza](#1-contexto-y-límites-de-confianza): quién
   habla con quién.
2. [Superficie de ataque](#4-superficie-de-ataque): cada punto de entrada,
   con el control de `vogel` y la tarea del consumidor.
3. [Riesgos residuales](#7-riesgos-residuales): lo que se acepta a sabiendas.

La auditoría ítem por ítem contra la lista de OWASP, con su evidencia, está
en [`docs/security/OWASP_CHECKLIST.md`](security/OWASP_CHECKLIST.md).

Revisión: actualizar este documento en el mismo PR que agregue un punto de
entrada, una dependencia externa o un dato personal nuevo.

---

## 1. Contexto y límites de confianza

```text
                 LC1                          LC2
 Cliente  ──HTTPS──▶  Servicio consumidor  ──TLS──▶  Zitadel   (identidad)
 (navegador,          (API con vogel:      ──gRPC─▶  Cerbos    (autorización)
  app, otro            httpx, request,     ──TLS──▶  PostgreSQL (datos, audit_log,
  servicio)            auth, authz,                              River, workflow)
                       audit, storage,     ──HTTPS─▶ S3 / MinIO (objetos)
                       notification,       ──TLS──▶  SMTP / SendGrid (correo)
                       worker, migrate)
                             ▲
                             └── LC3: operador (despliegue, migraciones, secretos)
```

| Límite | Del lado no confiable | Del lado confiable | Qué cruza |
|---|---|---|---|
| LC1 | Cliente HTTP (anónimo o con token) | Servicio consumidor | Requests, Bearer token, cuerpos JSON, archivos |
| LC2 | Servicios externos (red) | Servicio consumidor | Claves JWKS, resultados de introspección, decisiones de Cerbos, filas de Postgres, objetos S3 |
| LC3 | Operador y pipeline de despliegue | Proceso en ejecución | Variables de entorno, secretos, roles de base, migraciones |

Supuestos:

- La terminación TLS de LC1 (balanceador o proxy) es del consumidor. `vogel`
  solo cree en `X-Forwarded-For` de los proxies declarados en `RealIP`.
- Zitadel y Cerbos son confiables en sus respuestas; lo que no se asume es
  que estén disponibles (un fallo es 503, nunca un acceso concedido).
- Postgres puede verlo el rol de la aplicación; `audit_log` se protege del
  propio rol de ejecución (ver «Un rol para migrar, otro para servir» en el
  README).

## 2. Flujos de datos y clasificación

| Clase | Ejemplos | Regla |
|---|---|---|
| **Secreto** | Bearer token, secreto o clave de Zitadel, contraseñas de base, claves S3, API key de SendGrid, contraseña SMTP | Nunca en logs, respuestas ni `audit_log`; solo desde el entorno |
| **Personal** | ID y nombre de usuario, IP, User-Agent, direcciones de correo, contenido de documentos y snapshots auditados | Solo lo necesario; registrado en `audit_log` y en logs con propósito; retención definida por el consumidor |
| **Interno** | IDs de request, rutas, métricas, nombres de hosts y roles | No se expone a clientes anónimos (p. ej. `/metrics` en listener interno, errores de base solo en logs) |
| **Público** | Respuestas de `/healthz`, mensajes de error genéricos | Sin detalle de infraestructura |

| Flujo | Datos | Clase | Dónde queda |
|---|---|---|---|
| Cliente → `Authenticate` → Zitadel | Token | Secreto | Memoria; con introspección, caché en memoria por SHA-256 del token |
| `Authenticate` → `reqctx` / logs | ID de usuario, IP, User-Agent, request ID | Personal / interno | Logs del consumidor |
| Handler → `audit.Recorder` → Postgres | Actor, IP, User-Agent, payload y snapshots (redactados) | Personal | `audit_log` (append-only) |
| Handler → `RequirePermission` → Cerbos | Principal (ID, roles, atributos), recurso | Personal | Logs de Cerbos (operado por el consumidor) |
| Handler → `storage` → S3 | Archivos | Personal según el contenido | Bucket del consumidor |
| Handler → `notification` → SMTP / SendGrid | Destinatario, asunto, cuerpo | Personal | Proveedor de correo |
| Handler → `worker` → River | Argumentos del job | Según el job | Tabla `river_job` hasta que el limpiador la borra |

## 3. Datos personales y terceros que los procesan

| Dato | Lo trata | Tercero que lo recibe |
|---|---|---|
| Identidad del usuario (ID, nombre de usuario, roles) | `auth`, `audit`, `authz`, `workflow` | Zitadel (origen), Cerbos (en cada decisión) |
| IP y User-Agent | `reqctx`, `audit`, logs | Ninguno fuera del consumidor |
| Correo electrónico y contenido del mensaje | `notification` | SendGrid o el proveedor SMTP |
| Archivos subidos | `storage` | Proveedor S3 (AWS u otro compatible) |
| Payloads y snapshots auditados | `audit` | Ninguno fuera del consumidor |

Cada consumidor debe registrar estos terceros como encargados de tratamiento
en su propio inventario, con contrato y región de los datos. `vogel` no envía
datos a ningún tercero por su cuenta: solo a los que el consumidor configura.

## 4. Superficie de ataque

| Punto de entrada | Control que da `vogel` | Tarea del consumidor |
|---|---|---|
| `httpx/middleware.RequestID` | Genera siempre el ID en el servidor; el ID del cliente se sanea y queda aparte | Montarlo antes de `RequestContext` |
| `RealIP` | Solo cree en `X-Forwarded-For` / `X-Real-IP` de los CIDR declarados; vacío no cree en ninguno | Declarar sus proxies (`TRUSTED_PROXIES`) |
| `RequestContext` | Único escritor de request ID, IP y User-Agent en `reqctx` | Orden de montaje (`docs/WIRING.md`) |
| `StructuredLogger` | Registra ruta (sin query string), estado y duración; nunca el token | No loguear cuerpos ni secretos en sus handlers |
| `Recovery` | El pánico se registra con stack en el servidor; el cliente recibe un 500 genérico | — |
| `SecurityHeaders` | `nosniff`, `X-Frame-Options: DENY`, CSP `default-src 'none'`, HSTS 2 años, `Referrer-Policy`, `Permissions-Policy`, COOP y CORP `same-origin`, `Cache-Control: private, no-store` | Relajar solo en las rutas que lo necesiten (p. ej. Swagger) |
| `Authenticate` + `auth/zitadel` | Valida firma, emisor, audiencia y expiración (JWT local) o introspección RFC 7662 opcional; fallo del IdP es 503, nunca acceso | Elegir modo; configurar `ClientID` y audiencia; limitar tasa delante |
| `RequirePermission` + `authz/cerbos` | Sin principal es 401; denegado es 403; error de Cerbos es 503 (falla cerrado); TLS por defecto | Proteger **cada** ruta; escribir las políticas |
| `RequireAccess` + `access.Guard` | Chequeo por instancia (dueño, atributos) además del rol | Llamar `guard.Check` de nuevo cuando la decisión depende de la fila cargada |
| `RateLimitJSON` | Respuesta 429 uniforme | Montar el limitador (p. ej. `httprate` por IP real y por usuario) |
| `request.JSON` / `Decoder` | Límite de cuerpo (1 MiB por defecto), `Content-Type` exigido, campos desconocidos y datos extra rechazados | Elegir el límite por ruta |
| `request.Validator` | Validación de params y query (UUID, enteros, enums, fechas, `MaxLen` en runas) | Validar todo input antes de usarlo |
| `audit/httpx.Handler` | Filtros validados, texto libre ≤ 256 caracteres, límite y `offset` acotados, errores sin detalle de la base | Montarlo **detrás** de `Authenticate` y `RequirePermission` (no se protege solo) |
| `audit.Recorder` | Redacta claves sensibles (`password`, `token`, `secret`, `authorization`, `api_key`, `cookie`, ...) y falla cerrado si no puede; `audit_log` rechaza `UPDATE`/`DELETE`/`TRUNCATE` por trigger | Agregar sus claves sensibles; usar rol de ejecución sin dueño |
| `storage` (presign) | GET firmado con `Content-Disposition: attachment` y `application/octet-stream` por defecto; expiración por defecto 15 min; `SniffContentType` con lista permitida | Autorizar antes de firmar; claves de objeto no derivadas de input sin validar; verificar el tipo al recibir |
| `notification` | Puerto sin SDK; SMTP con STARTTLS obligatorio (política por defecto de go-mail); timeouts HTTP de SendGrid | No armar destinatarios ni cabeceras con input sin validar; limitar envíos |
| `worker` / River | Esquema propio, migraciones separadas del arranque del servidor | No poner secretos en los argumentos de los jobs; autorizar quién encola |
| `workflow` | Estado y transiciones en Postgres, reclamo con actor del contexto | Autorizar cada transición |
| `migrate` | Advisory lock de sesión; tablas de versión separadas por conjunto | Correrlo con el rol migrador, nunca con el de ejecución |
| `postgres` | `RequireTLS` rechaza `disable`, `allow`, `prefer` y `require` (exige verificación del certificado) | Activarlo fuera de desarrollo; usar `sslmode=verify-full` |
| `config` | Acumula errores y no repite valores de URL en ellos | No loguear la configuración completa |

## 5. Casos de abuso

| Caso | Control o riesgo aceptado |
|---|---|
| Token robado reutilizado tras cerrar la sesión | Modo introspección: rechazado tras a lo sumo `CacheTTL` (30 s por defecto). Modo JWT local: **aceptado hasta su `exp`** (ver §7) |
| Token con otra audiencia o emisor | Rechazado por la validación de `auth/zitadel` |
| Inundación de tokens aleatorios contra la introspección | Las fallas no se cachean: cada token desconocido es una llamada a Zitadel. Mitigación: rate limit delante (tarea del consumidor) |
| Ruta nueva sin autorización | `examples/api` tiene un test que recorre el router y falla si una ruta `/api/` no pasa por `RequirePermission` o `RequireAccess`; la plantilla de PR lo pregunta |
| Acceso a un recurso ajeno con un ID válido (IDOR) | `access.Guard` por instancia; el consumidor debe usarlo en rutas con ID |
| Falsificar la IP para evadir el rate limit o ensuciar `audit_log` | `RealIP` ignora cabeceras de pares no declarados |
| Borrar o reescribir la auditoría | Triggers append-only y rol de ejecución que no es dueño; un superusuario o el dueño aún puede (riesgo aceptado, ver §7) |
| Secretos en la auditoría | Redacción por defecto y configurable; falla cerrado |
| Cuerpo enorme o JSON malicioso | Límite de bytes y decodificación estricta |
| Filtro de auditoría gigante para cargar la base | `MaxFilterLength`, límite de página y `offset` máximo |
| Archivo HTML o SVG subido que ejecuta script | Descarga como adjunto y `octet-stream` por defecto; `SniffContentType` |
| Enlace firmado filtrado | Expira (15 min por defecto); quien lo tiene descarga hasta entonces (riesgo aceptado) |
| Cerbos o Zitadel caídos | 503: nunca se concede por error |
| Inyección SQL | Repositorios de `vogel` con consultas parametrizadas (pgx); el consumidor debe hacer lo mismo |
| Mensajes de error que filtran infraestructura | Errores de base solo en el log; respuestas genéricas |
| Dependencia comprometida o vulnerable | Acciones fijadas por SHA, Dependabot, `govulncheck`, SBOM por release |

## 6. Responsabilidades del consumidor

`vogel` corre dentro del proceso: valida lo que recibe y falla cerrado, pero
no controla la red, la infraestructura ni la operación del servicio que lo
usa. Lo siguiente queda a cargo del consumidor porque la librería no puede
imponerlo; la última columna dice dónde está el control de `vogel`
relacionado o la guía.

| Responsabilidad | Por qué `vogel` no puede imponerla | Dónde |
|---|---|---|
| Terminar TLS en el borde (o en el propio `http.Server`) y declarar los proxies de confianza | La librería recibe un `http.Handler`, no el listener; `RealIP` solo puede creer en los CIDR que le pasen, y si no se declaran, todas las requests salen con la IP del balanceador | `httpx/middleware.RealIP`, `ParseTrustedProxies`; §4 |
| Rate limiting en el borde, o con un almacenamiento compartido entre réplicas | `RateLimitJSON` solo da la respuesta 429; un limitador en memoria (como el de `examples/api`) cuenta por proceso, así que con N réplicas el límite efectivo es N veces el configurado | `httpx/middleware.RateLimitJSON`; §5 |
| Autenticación y autorización en **cada** ruta no pública | El router es del consumidor; montar `Authenticate` sin `RequirePermission`/`RequireAccess` (o al revés) compila y arranca | `docs/WIRING.md` §2 y §6; test de rutas de `examples/api` |
| Configurar la validación de tokens: `ClientID` (audiencia), y según el riesgo `AllowedAuthorizedParties`, `MaxTokenLifetime` e introspección | Los valores dependen de la aplicación registrada en Zitadel; con `AllowedAuthorizedParties` vacío no se controla `azp` | `auth/zitadel.Config`; `docs/WIRING.md` §2 |
| Cerbos con TLS y políticas propias | `authz/cerbos` usa TLS salvo `Plaintext: true`, pero no emite el certificado del PDP (que debe validar contra las raíces del sistema) ni escribe las políticas | `authz/cerbos.Config`; `examples/boilerplate/docs/CERBOS_POLICIES.md` |
| Separar el rol migrador del rol de ejecución | `vogel` recibe un pool ya conectado; los triggers append-only de `audit_log` protegen del rol de ejecución solo si ese rol no es dueño de la tabla | `migrate`, `audit`; `examples/boilerplate/deploy/initdb/` |
| TLS hacia PostgreSQL | `postgres.Config.RequireTLS` rechaza los `sslmode` que no verifican el certificado, pero el consumidor decide activarlo y provee la base con TLS y su CA | `postgres.Config`; `examples/boilerplate/deploy/RUNBOOK.md` §0 |
| Gestión de secretos: almacenarlos fuera del código y de las imágenes, y rotarlos | `config` solo lee variables de entorno; no sabe de dónde vienen ni cuándo caducan | `config`; RUNBOOK §10 |
| Retención y exportación de `audit_log` | La tabla es append-only y `vogel` no purga nada solo: cuánto guardarla, y si exportarla a un almacenamiento inmutable, es una decisión legal y operativa; la purga es un procedimiento manual del dueño de la tabla | `audit`; procedimiento en `audit/migrations/002_audit_log_append_only.sql`; §3 y §7 |
| Claves sensibles propias en la redacción de auditoría | La lista por defecto cubre nombres genéricos (`password`, `token`, ...), no los campos de cada dominio | `audit.WithRedactedKeys` |
| Envío y retención de logs | `logger.NewDefault` escribe a stdout (JSON en `production`, con `request_id` y `user_id` y marcas de tiempo en UTC); recolectarlos, protegerlos y borrarlos a tiempo es de la plataforma | `logger`; `docs/WIRING.md` §1 |
| Respaldos cifrados con restauración probada | La librería no ve la base desde afuera | RUNBOOK §9 |
| Parchear dependencias e imágenes | `vogel` fija y actualiza sus propios módulos, pero el binario final lo construye el consumidor con sus versiones y sus imágenes base | `.github/dependabot.yml`, `.github/workflows/trivy.yaml`; RUNBOOK §0 «Parches» |
| Registrar a los terceros como encargados de tratamiento | Contractual | §3 |

Lista de verificación para un servicio nuevo:

- [ ] TLS en el borde y `TRUSTED_PROXIES` correcto.
- [ ] `Authenticate` y una autorización (`RequirePermission` o `RequireAccess`) en cada ruta no pública.
- [ ] Rate limiting por IP real y por usuario, compartido entre réplicas, sobre todo con introspección.
- [ ] `ClientID` y, si aplica, `AllowedAuthorizedParties`, `MaxTokenLifetime` e introspección configurados.
- [ ] Cerbos con TLS (nunca `Plaintext` fuera de una red local de confianza).
- [ ] Rol migrador y rol de ejecución separados; `RequireTLS` activado con `sslmode=verify-full`.
- [ ] Secretos en un gestor, nunca en la imagen; rotación documentada.
- [ ] Claves sensibles propias agregadas a la redacción de auditoría.
- [ ] Retención de `audit_log`, logs y jobs definida y documentada.
- [ ] Respaldos cifrados con restauración probada (ver `examples/boilerplate/deploy/RUNBOOK.md`).
- [ ] Dependencias e imágenes base con actualización periódica y escaneo de vulnerabilidades.
- [ ] Terceros (§3) registrados como encargados de tratamiento.

## 7. Riesgos residuales

| Riesgo | Por qué se acepta | Cómo acotarlo |
|---|---|---|
| **Modo JWT local no puede revocar**: un token sigue válido hasta su `exp` | Es el modo por defecto, sin una llamada a Zitadel por request | Tokens de vida corta en Zitadel, o activar introspección |
| **Ventana de caché de introspección**: un token revocado se acepta hasta `CacheTTL` | Evita una llamada a Zitadel por request | Bajar `CacheTTL`; nunca supera el `exp` del token |
| Sin caché negativa en introspección | Cachear rechazos podría bloquear un token recién emitido | Rate limit delante |
| **COOP y CORP `same-origin` por defecto** pueden romper integraciones legítimas entre orígenes (popups OAuth, recursos embebidos) | Lo seguro es el valor por defecto | El consumidor los relaja en la ruta puntual |
| `audit_log` es alterable por su dueño o un superusuario | Los triggers protegen del rol de ejecución, no de un administrador | Rol de ejecución sin dueño; respaldos y, si hace falta, exportación a un almacenamiento inmutable |
| Enlaces firmados utilizables hasta su expiración | Es la naturaleza de un enlace firmado | Expiraciones cortas; autorizar antes de firmar |
| Disponibilidad de Zitadel y Cerbos | Fallar cerrado es preferible a conceder | Alta disponibilidad y alertas en esos servicios |
| HSTS con `includeSubDomains` afecta a todo el dominio | Lo seguro por defecto para APIs | Revisarlo antes de servir subdominios sin HTTPS |
