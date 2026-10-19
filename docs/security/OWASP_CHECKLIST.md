# Auditoría OWASP de `vogel`

Registro de la auditoría de `vogel` (librería y `examples/`) contra la lista
de verificación de seguridad basada en la
[OWASP Cheat Sheet Series](https://cheatsheetseries.owasp.org/). Dice, ítem
por ítem, qué cumple la librería, qué cumplen los ejemplos y qué queda a
cargo del servicio consumidor.

| Campo | Valor |
|---|---|
| Sistema | `github.com/kafeiih/vogel` (librería Go) + `examples/api` + `examples/boilerplate` |
| Fecha de la auditoría | 2026-10-14 |
| Commit auditado | `82da551` (rama `security/owasp-checklist-cycle2`) |
| Lista usada | Web Application Security Audit Checklist (secciones 1 a 20) |
| Próxima revisión | En cada versión menor, y en el PR que agregue un punto de entrada nuevo |

## Resumen

| Estado | Ítems |
|---|---|
| PASS | 80 |
| PARTIAL | 8 |
| FAIL | 0 |
| CONSUMER | 26 |
| N/A | 79 |
| **Total (secciones 1 a 18)** | **193** |

Las secciones 19 (IA y LLM) y 20 (frameworks y runtimes específicos) no
aplican y no se cuentan: ver [§19 y §20](#19-y-20-no-aplican).

Los PARTIAL y los pendientes están en
[Brechas conocidas y seguimiento](#brechas-conocidas-y-seguimiento).

**Leyenda de estados:**

| Estado | Significado |
|---|---|
| PASS | Se cumple en la librería o, si el ítem es de despliegue, en los ejemplos; la evidencia lo muestra |
| PARTIAL | Se cumple en parte; la columna de evidencia dice qué falta |
| FAIL | No se cumple |
| CONSUMER | Es tarea del servicio consumidor: la librería no puede imponerla. Ver [`docs/THREAT_MODEL.md` §6](../THREAT_MODEL.md#6-responsabilidades-del-consumidor) |
| N/A | El componente no existe en `vogel` (p. ej. contraseñas, cookies de sesión, SAML) |

**ID de ítem:** la lista original no numera sus ítems; el ID es
`sección.posición` en el orden en que aparecen (los subtítulos de las
secciones 16 y 17 siguen la numeración corrida). La severidad (C, H, M, L)
es la de la lista.

**Evidencia:** rutas relativas a la raíz del repositorio, con línea cuando
apunta a un punto exacto, y el test que lo prueba cuando existe.

---

## 1. Autenticación

`vogel` no maneja contraseñas, formularios de login ni recuperación de cuenta:
delega la identidad en Zitadel y solo valida el Bearer token
(`auth/zitadel`). Lo que sigue es del proveedor de identidad.

| ID | Sev. | Ítem | Estado | Evidencia |
|---|---|---|---|---|
| 1.1 | C | Hash de contraseñas (Argon2id, scrypt, bcrypt) | N/A | Sin contraseñas de usuario; las gestiona Zitadel |
| 1.2 | C | Rate limit en login, reset y MFA | N/A | Sin endpoints de login; el rate limit delante de `Authenticate` es tarea del consumidor (§8.2) |
| 1.3 | H | Errores de login genéricos | N/A | Sin login. `Authenticate` responde 401 genérico: `httpx/middleware/authmessages.go:27`, `httpx/middleware/auth_test.go` (`TestAuthenticate_Unauthenticated_Returns401AndDoesNotCallNext`) |
| 1.4 | H | Política de contraseñas | N/A | Zitadel |
| 1.5 | H | Contraseñas contra listas filtradas | N/A | Zitadel |
| 1.6 | H | Token de reset de un solo uso | N/A | Zitadel |
| 1.7 | H | Respuesta de reset idéntica | N/A | Zitadel |
| 1.8 | M | MFA disponible y obligatorio para admins | N/A | Zitadel |
| 1.9 | M | Bloqueo o demora progresiva | N/A | Zitadel |
| 1.10 | M | Credenciales solo por TLS, nunca en logs ni URLs | PASS | El token solo se lee de la cabecera `Authorization` (`httpx/middleware/auth.go:37`); nunca se registra (`httpx/middleware/auth_test.go`, `TestAuthenticate_FailureLog_CarriesRequestFieldsWithoutToken`); el log de acceso no guarda la query string (`httpx/middleware/logger.go:45`). TLS en el borde: CONSUMER (§7.1) |
| 1.11 | L | Atributos `autocomplete` en formularios | N/A | Sin frontend |

## 2. Gestión de sesiones

`vogel` es sin estado: no emite cookies ni IDs de sesión. Cada request trae
un Bearer token que se valida en el servidor.

| ID | Sev. | Ítem | Estado | Evidencia |
|---|---|---|---|---|
| 2.1 | C | IDs de sesión con CSPRNG ≥ 128 bits | N/A | Sin sesiones |
| 2.2 | C | ID nuevo al iniciar sesión | N/A | Sin sesiones |
| 2.3 | H | Cookies `HttpOnly`, `Secure`, `SameSite` | N/A | Sin cookies |
| 2.4 | H | Nombre de cookie no genérico | N/A | Sin cookies |
| 2.5 | H | Timeouts de inactividad y absoluto en servidor | N/A | Sin sesiones; la vida del token la acota `MaxTokenLifetime` (§2.9) |
| 2.6 | H | Logout invalida en el servidor | N/A | Sin sesiones; la revocación de tokens está en §2.9 |
| 2.7 | H | Invalidar sesiones al cambiar contraseña | N/A | Zitadel |
| 2.8 | M | IDs de sesión fuera de URLs y logs | N/A | Sin sesiones (para el token, ver §1.10) |
| 2.9 | M | JWT: algoritmo fijado, expiración corta, firma verificada, revocación | PASS | Firma, emisor, audiencia y `exp` contra el JWKS del emisor (`auth/zitadel/zitadel.go`, `auth/zitadel/jwt_test.go`); algoritmos fijados a `RS256`/`ES256`/`PS256`, `none` y `HS256` rechazados (`TestAuthenticate_VerifiedJWT_RejectsUnpinnedAlgorithms`); `nbf`, `iat` futuro, `azp` y vida máxima (`auth/zitadel/claims_test.go`, `TestAuthenticate_RejectsClaimsTheSDKDoesNotCheck`); revocación con introspección RFC 7662 (`auth/zitadel/introspection.go`, `auth/zitadel/introspection_test.go`). El modo JWT local no revoca antes de `exp`: riesgo aceptado en `docs/THREAT_MODEL.md` §7 |
| 2.10 | M | JWT fuera de `localStorage` | CONSUMER | Lo decide el cliente que obtiene el token, no la API |
| 2.11 | M | Detección de robo de cookies | N/A | Sin cookies |
| 2.12 | L | Device Bound Session Credentials | N/A | Sin cookies |

## 3. Autorización y control de acceso

| ID | Sev. | Ítem | Estado | Evidencia |
|---|---|---|---|---|
| 3.1 | C | Autorización en el servidor en cada endpoint | PASS | `RequirePermission` / `RequireAccess` (`httpx/middleware/authz.go:62`, `httpx/middleware/access.go:54`); test que recorre el router y falla si una ruta `/api/` no está protegida: `examples/api/router_test.go` (`TestNewRouter_EveryAPIRouteIsGuarded`). Proteger cada ruta propia: CONSUMER (§6) |
| 3.2 | C | Chequeo por objeto (IDOR) | PASS | `access.Guard.Check` por instancia (`access/access.go:154`); `examples/api` lo usa en `DELETE /documents/{id}` (`examples/api/router.go:89`). Usarlo en cada ruta con ID: CONSUMER |
| 3.3 | C | Denegar por defecto | PASS | Sin principal 401, denegado 403, error del PDP 503 (falla cerrado): `httpx/middleware/authz_test.go` (`TestRequirePermission_CheckerError_Returns503NotForbidden`) |
| 3.4 | H | Autorización centralizada | PASS | Middleware + `authz.Checker` (Cerbos) en `authz/cerbos/cerbos.go:88` |
| 3.5 | H | DTOs explícitos; sin asignación masiva | PASS | `request.JSON` rechaza campos desconocidos (`request/json_test.go`, `TestJSONWithLimit_UnknownField`); el dueño sale del principal, no del cuerpo (`examples/api/handler_integration_test.go`, `TestDocumentHandlerCreate_PersistsThePrincipalAsOwner`) |
| 3.6 | H | Endpoints internos fuera de internet | PASS | `/metrics` en un listener aparte, loopback por defecto (`examples/api/config.go:80`, `examples/api/router_test.go`, `TestNewMetricsServer_ServesOnlyMetrics`); Swagger rechazado en producción (`examples/boilerplate/internal/infrastructure/config/config_swagger_test.go`, `TestLoad_SwaggerRefusedInProduction`) |
| 3.7 | M | Fallos de autorización registrados con usuario, recurso y acción | PASS | `WriteAccessErrorFor` (`httpx/middleware/access.go:115`); `httpx/middleware/access_test.go` (`TestRequireAccess_Forbidden_LogsDenialWithActorResourceAndRequest`, `TestWriteAccessErrorFor_LogsResourceAndAction`) |
| 3.8 | M | IDs impredecibles | PASS | UUID validados en params (`request/validate.go:71`); claves de objeto por UUID (`examples/api/document.go:120`) |
| 3.9 | L | Archivos detrás de autorización | PASS | Descarga por URL firmada tras `requireRead` (`examples/api/router.go:104`), que `examples/api` firma por 5 min (`downloadURLExpiry`, `examples/api/handler.go:559`); el adaptador limita cualquier expiración a `MaxPresignExpiry` (1 h por defecto) y usa 15 min si no se indica una (`storage/s3/s3.go:63`) |

## 4. Validación de input e inyección

| ID | Sev. | Ítem | Estado | Evidencia |
|---|---|---|---|---|
| 4.1 | C | Consultas parametrizadas | PASS | pgx con placeholders (`audit/postgres/repository.go:112`, `workflow/postgres/repository.go:120`); ninguna consulta armada con `fmt.Sprintf` en el árbol (búsqueda sin resultados) |
| 4.2 | C | Nada de input en una shell | PASS | Sin `os/exec` fuera de tests (solo `access/deps_test.go`, que corre `go list`) |
| 4.3 | C | Sin `eval` ni deserialización insegura | PASS | Solo `encoding/json`; sin `gob`, XML ni YAML en el código de producción |
| 4.4 | H | Validación en servidor con esquema | PASS | `request.Validator` (`request/validate.go:31`) y `validation`; filtros de auditoría acotados (`audit/httpx/filters.go:16`). Validar todo input propio: CONSUMER |
| 4.5 | H | Inyección de operadores NoSQL | N/A | Sin NoSQL |
| 4.6 | H | SSRF en URLs del usuario | N/A | Ninguna URL saliente viene del cliente: emisor, PDP y S3 salen de la configuración |
| 4.7 | M | LDAP, XPath, XML, cabeceras | PASS | Sin LDAP, XPath ni XML. Cabeceras: el `X-Request-Id` del cliente se sanea a ASCII visible y largo acotado (`httpx/middleware/requestid.go:67`, `httpx/middleware/requestid_test.go`, `TestSanitizeClientRequestID`) |
| 4.8 | M | Path traversal | PASS | La librería no abre rutas derivadas de input; claves de objeto generadas en el servidor (`examples/api/document.go:120`) |
| 4.9 | L | Normalización Unicode y límites de largo antes de validar | PARTIAL | `MaxLen` cuenta runas (`request/validate.go:244`), pero no hay normalización NFC antes de validar; `stringutil.Normalize` es para búsqueda, no para validación |

## 5. XSS y cliente

`vogel` sirve JSON, no HTML. La única página HTML es Swagger UI en
`examples/boilerplate`, apagada por defecto y rechazada en producción.

| ID | Sev. | Ítem | Estado | Evidencia |
|---|---|---|---|---|
| 5.1 | C | Codificación de salida por contexto | N/A | Sin plantillas HTML; las respuestas son JSON |
| 5.2 | C | Sanitizar HTML del usuario | N/A | No se acepta ni se muestra HTML |
| 5.3 | H | CSP desplegada | PASS | `default-src 'none'` (`httpx/middleware/security.go:28`); Swagger con su propia CSP (`examples/boilerplate/internal/interfaces/http/router.go:65`, `TestNewRouter_SwaggerServedWithItsOwnCSPWhenEnabled`) |
| 5.4 | H | URLs del usuario en `href`/`src` | N/A | Sin frontend |
| 5.5 | H | `X-Frame-Options` o `frame-ancestors` | PASS | `httpx/middleware/security.go:25`; `httpx/middleware/security_test.go` (`TestSecurityHeaders_SetsAllHeaders`) |
| 5.6 | M | JSON con `application/json` y `nosniff` | PASS | `httpx/response/success.go:17`, `httpx/response/error.go:104`; `nosniff` en `httpx/middleware/security.go:24` |
| 5.7 | M | Sin datos sensibles en HTML o JS | N/A | Sin HTML ni JS servidos |
| 5.8 | M | Prototype pollution | N/A | Go, sin merge de objetos JavaScript |
| 5.9 | L | SRI en scripts de terceros | N/A | Sin scripts de terceros en la API |

## 6. CSRF

| ID | Sev. | Ítem | Estado | Evidencia |
|---|---|---|---|---|
| 6.1 | C | Token anti-CSRF o autenticación sin cookies | PASS | Autenticación solo por cabecera `Authorization` (`httpx/middleware/auth.go:37`) |
| 6.2 | H | Cookies `SameSite` | N/A | Sin cookies |
| 6.3 | H | GET nunca cambia estado | PASS | Las rutas GET de los ejemplos solo leen (`examples/api/router.go:100`, `examples/boilerplate/internal/interfaces/http/router.go`); la matriz de roles las fija (`examples/api/rolematrix_test.go`) |
| 6.4 | M | Chequeo de `Origin`/`Referer` | N/A | Sin cookies, no hay credencial ambiental que un sitio ajeno pueda usar |
| 6.5 | M | Reautenticación en operaciones sensibles | N/A | Sin cambio de contraseña, correo ni pagos; es del IdP |
| 6.6 | L | CORS sin reflejar orígenes con credenciales | PASS | Lista explícita y `AllowCredentials: false` (`examples/boilerplate/internal/interfaces/http/router.go:208`, `TestNewRouter_CORSDoesNotAllowCredentials`) |

## 7. Cabeceras HTTP y transporte

| ID | Sev. | Ítem | Estado | Evidencia |
|---|---|---|---|---|
| 7.1 | C | Todo por HTTPS; tráfico interno cifrado | CONSUMER | TLS en el borde (§6). Hacia dentro, la librería cifra por defecto: Cerbos con TLS salvo `Plaintext` (`authz/cerbos/cerbos.go:27`), Postgres con `RequireTLS` (`postgres/postgres.go:107`, `postgres/tls_verify_test.go`), SMTP sobre TLS (`notification/smtp/smtp.go:25`) |
| 7.2 | H | HSTS | PASS | `max-age=63072000; includeSubDomains` (`httpx/middleware/security.go:29`) |
| 7.3 | H | TLS 1.2 mínimo, certificados monitoreados | CONSUMER | Lo fija quien termina TLS |
| 7.4 | H | `nosniff`, framing, `Referrer-Policy`, `Permissions-Policy` | PASS | `httpx/middleware/security.go:24`; primer middleware del boilerplate para que los rechazos tempranos también las lleven (`examples/boilerplate/internal/interfaces/http/router_test.go`, `TestNewRouter_EarlyRejectionsCarrySecurityHeaders`) |
| 7.5 | M | Sin `Server` ni `X-Powered-By` | PASS | `net/http` no agrega esas cabeceras y ningún código del repo las escribe (búsqueda sin resultados) |
| 7.6 | M | `Cache-Control: no-store` en datos personales | PASS | `private, no-store` por defecto (`httpx/middleware/security.go:33`, `TestSecurityHeaders_DownstreamHandlerCanOverrideCacheControl`) |
| 7.7 | L | Notas de securityheaders.com y SSL Labs | CONSUMER | Requiere un despliegue público |

## 8. APIs

| ID | Sev. | Ítem | Estado | Evidencia |
|---|---|---|---|---|
| 8.1 | H | Autenticación en todo endpoint no público | PASS | `Authenticate` (`httpx/middleware/auth.go:88`) en `/api/v1` (`examples/api/router.go:98`); solo `/healthz` es público |
| 8.2 | H | Rate limit, límite de payload, paginación acotada | PARTIAL | Cuerpo de 1 MiB por defecto (`request/json_test.go`, `TestJSON_DefaultLimitIsExactly1MiB`); `limit` y `offset` acotados (`audit/audit.go:185`, `workflow/workflow.go:738`). El limitador de `examples/api` (`examples/api/ratelimit.go:64`) es por proceso y, sin `RealIP`, detrás de un proxy cuenta la IP del proxy (documentado). Limitador compartido: CONSUMER (§6) |
| 8.3 | H | Validar `Content-Type` | PASS | `request.JSON` responde 415 a un tipo distinto de JSON (`request/json_test.go`, `TestJSON_ContentType`) |
| 8.4 | H | Errores sin stack traces, SQL ni hosts | PASS | `Recovery` registra el stack y responde 500 genérico (`httpx/middleware/recovery.go:12`, `httpx/middleware/recovery_test.go`); errores de base solo en el log |
| 8.5 | M | GraphQL | N/A | Sin GraphQL |
| 8.6 | M | WebSockets | N/A | Sin WebSockets |
| 8.7 | M | Redirecciones validadas | N/A | Sin parámetros de redirección |
| 8.8 | L | Versionado de la API | PASS | `/api/v1` (`examples/api/router.go:91`) y `/v1` en el boilerplate |

## 9. Subida de archivos

| ID | Sev. | Ítem | Estado | Evidencia |
|---|---|---|---|---|
| 9.1 | C | Archivos nunca ejecutados; en almacenamiento de objetos | PASS | Puerto `storage` sobre S3 (`storage/s3/s3.go`); descarga por URL firmada, nunca desde el proceso |
| 9.2 | H | Tipo por contenido con lista permitida | PASS | `storage.SniffContentType` (`storage/sniff.go:40`, `storage/sniff_test.go`); `examples/api` lo aplica en el job `validate_upload`, después de la subida directa al bucket (`examples/api/jobs.go:218`, `TestValidateUpload`): un objeto no permitido se borra y el documento queda `rejected`, o vuelve a su archivo validado anterior si lo tenía; uno aceptado se copia a una key final que nadie puede firmar para `PUT`, así que reusar la URL de subida no reemplaza el archivo validado (`TestPresignedUploadFlow`) |
| 9.3 | H | Nombre regenerado en el servidor | PASS | `newUploadKey(id)` genera `documents/{id}/uploads/{uuid}` en el servidor (`examples/api/document.go:120`); el archivo validado vive en `documents/{id}/file/{uuid}`, que sólo escribe el job (`examples/api/document.go:107`) |
| 9.4 | H | Límite de tamaño antes de leer todo | PASS | La API ya no recibe el cuerpo: `upload-url` rechaza un tamaño fuera de 1..25 MiB antes de firmar (`examples/api/handler.go:621`) y firma el tamaño exacto con `storage.WithContentLength` (`examples/api/handler.go:647`), así que el bucket rechaza otro tamaño |
| 9.5 | M | Reencodear imágenes | CONSUMER | Depende del dominio; `examples/api` acepta PNG y JPEG sin reencodear, mitigado por la descarga como adjunto con el tipo verificado por contenido (§9.7) |
| 9.6 | M | Escaneo de malware | CONSUMER | Según el riesgo del servicio |
| 9.7 | M | Descarga con `Content-Disposition: attachment` y tipo seguro | PASS | Valores por defecto `attachment` y `application/octet-stream` (`storage/presign.go:13`, `storage/presign.go:23`, `storage/presign_test.go`) |

## 10. Secretos, configuración y dependencias

| ID | Sev. | Ítem | Estado | Evidencia |
|---|---|---|---|---|
| 10.1 | C | Sin secretos en el repo; escáner sobre toda la historia | PASS | gitleaks sobre la historia completa (`.github/workflows/secrets.yaml:34`, `.github/workflows/secrets.yaml:48`; reglas en `.gitleaks.toml`). Check requerido en `master` |
| 10.2 | C | Secretos inyectados en ejecución; `.env` ignorado y fuera de imágenes | PASS | `.gitignore:19`, `.dockerignore:10`; compose exige las variables con `:?` (`examples/boilerplate/deploy/docker-compose.yaml:176`). El gestor de secretos: CONSUMER |
| 10.3 | H | Secretos rotables y rotación probada | CONSUMER | `config` solo lee el entorno; procedimiento en `examples/boilerplate/deploy/RUNBOOK.md` §10. Probar la rotación es del operador |
| 10.4 | H | Escaneo de dependencias en CI que bloquea | PASS | `govulncheck` (`.github/workflows/ci.yaml:142`), Trivy semanal y por PR (`.github/workflows/trivy.yaml:58`), Dependabot (`.github/dependabot.yml`). `govulncheck`, Trivy y gitleaks son checks requeridos en `master` |
| 10.5 | H | Lockfiles versionados | PASS | `go.sum`, `examples/boilerplate/go.sum`; cambios revisados por `.github/CODEOWNERS` |
| 10.6 | H | Debug y credenciales por defecto apagados en producción | PASS | Swagger rechazado en producción (`TestLoad_SwaggerRefusedInProduction`); logs JSON en `production` (`logger/logger.go:36`); compose sin contraseñas por defecto (`examples/boilerplate/deploy/docker-compose.yaml:176`) |
| 10.7 | M | Cifrado en reposo con AES-GCM o KMS | CONSUMER | `vogel` no cifra datos en reposo ni implementa criptografía propia (solo SHA-256 para la clave de caché, `auth/zitadel/introspection.go:221`) |
| 10.8 | M | Revisión del mantenimiento de dependencias | PARTIAL | Dependabot semanal y CODEOWNERS en `go.mod`; no hay un criterio escrito para evaluar si una dependencia está abandonada |
| 10.9 | L | SBOM | PASS | CycloneDX por release (`.github/workflows/release.yaml:48`) |

## 11. Errores y logs

| ID | Sev. | Ítem | Estado | Evidencia |
|---|---|---|---|---|
| 11.1 | H | Manejador global: mensaje genérico al usuario, detalle al log | PASS | `httpx/middleware/recovery.go:12`; `httpx/response/error.go:88` |
| 11.2 | H | Eventos de seguridad registrados | PARTIAL | Fallos de autenticación (`TestAuthenticate_FailureLog_CarriesRequestFieldsWithoutToken`) y de autorización (§3.7) se registran; cambios de datos van a `audit_log` (`audit/recorder.go:211`). Login, logout y MFA son de Zitadel. Los fallos de validación de input responden 400 pero no se registran como evento |
| 11.3 | H | Sin secretos en logs | PASS | Token nunca en el log (§1.10); redacción de claves sensibles en auditoría, fallando cerrado (`audit/redact.go:82`, `audit/redact_test.go`, `TestRecord_UnredactablePayload_FailsClosed`) |
| 11.4 | M | UTC, usuario, IP, request ID y resultado, estructurado | PASS | `httpx/middleware/logger.go` (estado, `remote_addr`, `request_id`, `user_id`; `WithRemoteAddr` y `WithoutUserAgent` truncan u omiten los datos del cliente); UTC en `logger/logger_test.go` (`TestNew_TimestampsAreUTC`); `user_id` en `httpx/middleware/logger_test.go` (`TestStructuredLogger_LogsAuthenticatedUserID`) |
| 11.5 | M | Logs fuera del host, a prueba de alteración, con retención | CONSUMER | `logger` escribe a stdout; ver `docs/THREAT_MODEL.md` §6 |
| 11.6 | M | Alertas de fuerza bruta, picos de 401/403, actividad admin | CONSUMER | Reglas sugeridas en `examples/boilerplate/deploy/RUNBOOK.md` («Alertas recomendadas») |
| 11.7 | L | Prevenir inyección en logs | PASS | `slog` escapa valores (JSON y texto citado); el ID de request del cliente se sanea (`httpx/middleware/requestid.go:67`) |

## 12. Infraestructura y despliegue

Evidencia del despliegue de referencia (`examples/boilerplate/deploy`); el
despliegue real es de cada consumidor.

| ID | Sev. | Ítem | Estado | Evidencia |
|---|---|---|---|---|
| 12.1 | C | Base no expuesta; usuario de mínimo privilegio | PASS | Postgres sin `ports` en el compose de despliegue (solo la API publica `8080`, `examples/boilerplate/deploy/docker-compose.yaml:170`); roles migrador y de ejecución separados (`examples/boilerplate/deploy/initdb/10-roles.sh:45`, `examples/boilerplate/deploy/initdb/runtime-grants.sql:19`) |
| 12.2 | C | Contenedores no root, solo lectura, imágenes fijadas y escaneadas | PASS | `USER appuser` (`examples/boilerplate/Dockerfile:60`, `examples/boilerplate/Dockerfile:80`); bases fijadas por digest (`examples/boilerplate/Dockerfile:50`); `read_only`, `cap_drop: [ALL]`, `no-new-privileges` (`examples/boilerplate/deploy/docker-compose.yaml:58`); Trivy (`.github/workflows/trivy.yaml`) |
| 12.3 | H | Producción, staging y desarrollo aislados | CONSUMER | Depende de la infraestructura del consumidor |
| 12.4 | H | Secretos de CI acotados; acciones fijadas por SHA; build no modificable por PR sin revisar | PASS | `permissions: contents: read` (`.github/workflows/ci.yaml:19`); todas las acciones fijadas por SHA de 40 caracteres (búsqueda de `uses:` sin SHA sin resultados); CODEOWNERS (`.github/CODEOWNERS`) y protección de `master` (configuración de GitHub, fuera del árbol) |
| 12.5 | H | Respaldos cifrados con restauración probada | CONSUMER | Procedimiento en `examples/boilerplate/deploy/RUNBOOK.md` §9 |
| 12.6 | M | IAM de nube con mínimo privilegio | CONSUMER | Sin recursos de nube en el repo |
| 12.7 | M | Puertos y paneles sin uso cerrados | PASS | Métricas en loopback por defecto (`examples/api/config.go:80`); solo `8080` publicado en el compose de despliegue |
| 12.8 | L | Cadencia de parches | PASS | `examples/boilerplate/deploy/RUNBOOK.md` §0 «Parches»; Dependabot y Trivy semanales (`.github/workflows/trivy.yaml:16`) |

## 13. Privacidad y datos

| ID | Sev. | Ítem | Estado | Evidencia |
|---|---|---|---|---|
| 13.1 | H | Inventario de datos personales | PASS | `docs/THREAT_MODEL.md` §2 y §3 (lo que trata la librería); el inventario del servicio es del consumidor |
| 13.2 | H | Minimización | PASS | La ranura de usuario guarda solo `Principal.UserID`, nunca el nombre, los roles ni el token (`httpx/middleware/auth_test.go`, `TestAuthenticate_Success_FillsUserSlot`); redacción en auditoría (§11.3) |
| 13.3 | M | Exportar y borrar datos del usuario | CONSUMER | `audit_log` es append-only; la purga es un procedimiento manual (`audit/migrations/002_audit_log_append_only.sql`, `TestAuditLog_AppendOnly_DocumentedRetentionProcedureWorks`) |
| 13.4 | M | Terceros documentados y bajo contrato | PASS | `docs/THREAT_MODEL.md` §3 los lista; el contrato es del consumidor |
| 13.5 | L | Analítica y consentimiento | N/A | Sin analítica ni tracking |

## 14. Proceso

| ID | Sev. | Ítem | Estado | Evidencia |
|---|---|---|---|---|
| 14.1 | M | Modelo de amenazas actualizado | PASS | `docs/THREAT_MODEL.md` (se actualiza en el PR que agregue un punto de entrada) |
| 14.2 | M | Revisión de seguridad en los PR | PASS | `.github/pull_request_template.md:10` («Security checklist»); CODEOWNERS en los paquetes de seguridad |
| 14.3 | M | Vía de reporte y plazos | PASS | `SECURITY.md` (reporte privado de GitHub, plazos de respuesta) |
| 14.4 | M | Runbook de incidentes | PASS | `examples/boilerplate/deploy/RUNBOOK.md` §10 a §12 (secretos, revocación de tokens, comunicación); divulgación coordinada en `SECURITY.md` |
| 14.5 | L | SAST, DAST y escaneo de dependencias en CI | PASS | `gosec` en `.golangci.yml:18`, `govulncheck`, Trivy, gitleaks. Sin DAST: la librería no tiene un despliegue propio que escanear |
| 14.6 | M | Revisión de código con lista y hallazgos seguidos | PASS | Plantilla de PR; este documento y el `CHANGELOG.md` registran los hallazgos y su corrección |
| 14.7 | M | Análisis de superficie de ataque | PASS | `docs/THREAT_MODEL.md` §4 |
| 14.8 | M | Matriz rol × endpoint en CI | PASS | `examples/api/rolematrix_test.go` (`TestNewRouter_RoleMatrix`), `examples/boilerplate/internal/interfaces/http/rolematrix_test.go`. La política falsa de `examples/api` deja leer `/audit` a cualquier autenticado (ver seguimiento) |
| 14.9 | L | Inventario de componentes legados | N/A | Librería anterior a 1.0 sin componentes legados; la adopción desde los servicios previos está en `docs/MIGRATION_GUIDE.md` |
| 14.10 | L | Parcheo virtual (WAF) | CONSUMER | El borde es del consumidor |

## 15. Ataques web avanzados

| ID | Sev. | Ítem | Estado | Evidencia |
|---|---|---|---|---|
| 15.1 | C | Sin deserialización nativa de datos no confiables | PASS | Solo JSON con decodificación estricta (`request/json.go:90`) |
| 15.2 | C | Input fuera de las plantillas | N/A | Sin motores de plantillas (`text/template` y `html/template` no se usan) |
| 15.3 | H | XML sin DTD ni entidades externas | N/A | Sin XML |
| 15.4 | H | Envenenamiento de caché | PASS | Respuestas no cacheables por defecto (`httpx/middleware/security.go:33`) |
| 15.5 | H | Engaño de caché | PASS | Igual que §15.4 |
| 15.6 | H | Toma de subdominios | CONSUMER | DNS del consumidor |
| 15.7 | H | DoS: límites de cuerpo, cabeceras y JSON; regex lineal; timeouts salientes | PASS | Cuerpo (§8.2); `MaxHeaderBytes` y timeouts del servidor (`examples/api/main.go:346`, `TestNewAPIServer_SetsTimeoutsAndHeaderLimit`); regex de Go (RE2) es lineal; timeouts salientes: Zitadel (`auth/zitadel/timeout_test.go`), Cerbos (`authz/cerbos/timeout_test.go`), SendGrid (`notification/sendgrid/sendgrid.go:33`), S3 (`storage/s3/timeout_test.go`), Postgres (`postgres/postgres_integration_test.go`) |
| 15.8 | M | Gestión de bots | CONSUMER | Sin registro ni login propios; rate limit en el borde (§6) |
| 15.9 | M | DOM clobbering | N/A | Sin frontend |
| 15.10 | M | XS-Leaks | PASS | COOP y CORP `same-origin` (`httpx/middleware/security.go:31`); sin cookies |
| 15.11 | M | APIs de HTML5 | N/A | Sin frontend |
| 15.12 | L | Build de frontend | N/A | Sin frontend |

## 16. Federación de identidad e integraciones

`vogel` es un servidor de recursos: valida tokens, no ejecuta flujos OAuth ni
SAML, y no tiene webhooks, pagos ni scripts de terceros.

| ID | Sev. | Ítem | Estado | Evidencia |
|---|---|---|---|---|
| 16.1–16.3 | C, C, H | Code + PKCE, `redirect_uri` exacto, `state` y `nonce` | N/A | El flujo de login lo hace el cliente con Zitadel |
| 16.4 | H | Firma, `iss`, `aud`, `exp` y `azp` validados | PASS | `auth/zitadel/zitadel.go:206`; `azp` con `AllowedAuthorizedParties` (`auth/zitadel/jwt_test.go`, `TestAuthenticate_VerifiedJWT_AzpPolicy`). Configurar la lista: CONSUMER (§6) |
| 16.5 | H | Refresh tokens rotados | N/A | La API no recibe refresh tokens |
| 16.6 | M | Scopes mínimos y vida corta | CONSUMER | `MaxTokenLifetime` acota la vida aceptada (`auth/zitadel/zitadel.go:39`); el boilerplate usa 12 h; los scopes los pide el cliente |
| 16.7 | M | Vinculación de cuentas | N/A | Zitadel |
| 16.8–16.10 | C, H, H | SAML | N/A | Sin SAML |
| 16.11–16.13 | H, H, M | Passkeys / WebAuthn | N/A | Zitadel |
| 16.14–16.16 | H, H, M | Preguntas de seguridad, verificación y cambio de correo | N/A | `notification` envía correo, pero `vogel` no registra ni verifica cuentas |
| 16.17–16.20 | C, H, H, M | Webhooks entrantes y salientes | N/A | Sin webhooks |
| 16.21–16.25 | C, C, H, H, M | Pagos | N/A | Sin pagos (`decimalx` valida montos, ver §17.14) |
| 16.26–16.28 | H, H, M | JavaScript de terceros | N/A | Sin frontend |

## 17. Arquitectura: multi-tenant, microservicios, nube

| ID | Sev. | Ítem | Estado | Evidencia |
|---|---|---|---|---|
| 17.1–17.5 | C, C, H, H, M | Multi-tenancy | N/A | `vogel` no tiene concepto de tenant |
| 17.6 | C | Servicios internos se autentican entre sí | CONSUMER | La librería usa TLS por defecto hacia Cerbos y verifica el certificado de Postgres con `RequireTLS` (§7.1); mTLS o malla son del consumidor |
| 17.7 | H | Identidad propagada como token verificado | N/A | `vogel` no reenvía identidad a otros servicios |
| 17.8 | H | Cada servicio valida tokens y autoriza por su cuenta | PASS | `Authenticate` + `RequirePermission` dentro del proceso (§3.1) |
| 17.9 | H | Cuentas de servicio con identidad de carga de trabajo | CONSUMER | Infraestructura del consumidor |
| 17.10 | M | Documento de arquitectura de seguridad | PASS | `docs/THREAT_MODEL.md` §1 y §2 |
| 17.11 | M | gRPC con TLS, tamaños acotados y deadlines | PASS | Cliente de Cerbos con TLS (`authz/cerbos/cerbos.go:27`) y `CallTimeout` (`authz/cerbos/timeout_test.go`, `TestChecker_SilentPDP_CallsAreBoundedByCallTimeout`); sin servidor gRPC propio |
| 17.12 | M | Mensajería asíncrona | CONSUMER | Jobs de River con argumentos tipados; sin secretos en argumentos y autorizar quién encola: `docs/THREAT_MODEL.md` §4 |
| 17.13 | H | Flujos de varios pasos con estado en servidor; claves de idempotencia | PARTIAL | `workflow` guarda estado y transiciones en Postgres con revisión optimista (`workflow/postgres/repository.go:139`, `ErrConflict`); no ofrece claves de idempotencia genéricas para mutaciones |
| 17.14 | H | Rangos numéricos | PASS | `decimalx.ValidateAmount` y límites (`decimalx/bounds.go:316`); `Validator.MaxInt` (`request/validate.go:105`) |
| 17.15 | M | Casos de abuso documentados | PASS | `docs/THREAT_MODEL.md` §5 |
| 17.16 | M | Condiciones de carrera | PASS | Actualización condicionada por revisión (`workflow/postgres/repository.go:139`); `audit_log` append-only (`audit/postgres/append_only_integration_test.go`) |
| 17.17–17.18 | C, H | IAM por función; triggers como input no confiable | N/A | Sin serverless |
| 17.19 | H | Buckets privados por defecto | CONSUMER | El bucket es del consumidor; `vogel` solo entrega URLs firmadas (§3.9) |
| 17.20 | H | Segmentación de red | CONSUMER | Infraestructura del consumidor |
| 17.21 | M | Zero trust | CONSUMER | Cada request se autentica y autoriza en el proceso (§17.8); el resto es de la plataforma |
| 17.22 | M | Escaneo continuo de configuración de nube | CONSUMER | Trivy cubre el Dockerfile (`.github/workflows/trivy.yaml:58`); la nube es del consumidor |
| 17.23 | M | Secretos desde el gestor en ejecución | CONSUMER | `config` lee el entorno; de dónde vienen es de la plataforma |
| 17.24 | L | Cold start | N/A | Sin serverless |

## 18. Cadena de suministro

| ID | Sev. | Ítem | Estado | Evidencia |
|---|---|---|---|---|
| 18.1 | C | CI sin código no confiable con secretos | PASS | Sin `pull_request_target` (búsqueda sin resultados); token de solo lectura (`.github/workflows/ci.yaml:19`) |
| 18.2 | C | Acciones fijadas por SHA completo | PASS | Todas las `uses:` de `.github/workflows/` con SHA de 40 caracteres; Dependabot las actualiza (`.github/dependabot.yml`) |
| 18.3 | H | Instalación reproducible desde lockfile | PASS | `go.sum` verificado por la toolchain; cambios bajo CODEOWNERS (`.github/CODEOWNERS`) |
| 18.4 | H | Scripts de instalación desactivados | N/A | Los módulos de Go no ejecutan scripts al instalarse |
| 18.5 | H | Revisión de dependencias nuevas | PARTIAL | `go.mod` y `go.sum` requieren revisión del dueño (`.github/CODEOWNERS`), pero no hay criterios escritos (mantenimiento, typosquatting) |
| 18.6 | H | Credenciales de publicación acotadas; procedencia | PARTIAL | Sin tokens de registro (el módulo se publica por tag y `proxy.golang.org`); tags `v*` protegidos por ruleset (configuración de GitHub). Sin atestaciones de procedencia |
| 18.7 | H | `GITHUB_TOKEN` de solo lectura; sin interpolar input en `run:` | PASS | `contents: read` por defecto, `contents: write` solo en el job de publicación (`.github/workflows/release.yaml:66`); ningún `run:` interpola título, cuerpo ni rama de un PR |
| 18.8 | M | SBOM por release | PASS | `.github/workflows/release.yaml:48` (CycloneDX + `SHA256SUMS`) |
| 18.9 | M | Artefactos e imágenes firmados | PARTIAL | El módulo se verifica contra `sum.golang.org`; el SBOM lleva sumas SHA-256 pero sin firma, y las imágenes del boilerplate no se firman |
| 18.10 | M | Imágenes Docker (prácticas del ítem Node.js) | PASS | El ítem es de Node.js; el equivalente en Go se cumple: digest fijado, multi-stage, no root, `.dockerignore` sin `.env` ni `.git`, `HEALTHCHECK` (`examples/boilerplate/Dockerfile:64`, `examples/boilerplate/Dockerfile:85`) |
| 18.11 | M | Registro privado o proxy | N/A | Las rutas de módulo de Go son URLs, sin confusión de dependencias por nombre; `proxy.golang.org` conserva versiones publicadas |
| 18.12 | L | Pinning de certificados | N/A | No se usa |

## 19 y 20. No aplican

- **19. IA y LLM:** `vogel` no llama modelos, no tiene RAG, agentes ni
  servidores MCP. El ítem de «IA en el proceso de desarrollo» (código
  generado con la misma revisión, tests y escaneos) se cumple de hecho con
  §14.2 y §14.5, pero la sección entera se marca N/A porque el componente no
  existe en el producto.
- **20. Frameworks y runtimes:** todas las hojas listadas son de stacks que
  `vogel` no usa (Node.js, Next.js, NoSQL, Python, PHP, Ruby, JVM/.NET,
  micro frontends, extensiones, móviles). No hay una hoja de Go en la lista;
  las prácticas de Go están cubiertas por las secciones 4, 10, 15 y 18.

## Brechas conocidas y seguimiento

| # | Brecha | Ítems | Estado |
|---|---|---|---|
| 1 | `CERBOS_PLAINTEXT` en `examples/boilerplate` usa `config.Bool`, que acepta valores no parseables como `false` en silencio, en lugar del parseo estricto de `SWAGGER_ENABLED` y `DB_REQUIRE_TLS` | 7.1 | Cerrada (2026-10-16): usa `parseBool`, que rechaza el valor (`examples/boilerplate/internal/infrastructure/config/config_cerbos_test.go`) |
| 2 | La política falsa de `examples/api` deja leer `/audit` a cualquier usuario autenticado (`examples/api/fakes.go:53`); la matriz de roles lo fija. Un servicio copiado debe usar Cerbos con su política | 14.8 | Abierta (ejemplo) |
| 3 | El limitador de `examples/api` cuenta por IP del par directo: detrás de un proxy todos los clientes comparten la IP del proxy (documentado en `examples/api/ratelimit.go:28`) | 8.2 | Abierta (documentada) |
| 4 | 6 hallazgos previos de `golangci-lint` en `auth/zitadel/introspection*.go` (idénticos en la base) | 14.5 | Abierta |
| 5 | gitleaks y Trivy todavía no son checks requeridos de la protección de `master` | 10.1, 10.4 | Cerrada (2026-10-16): la protección de `master` los exige |
| 6 | Sin normalización Unicode antes de validar | 4.9 | Abierta |
| 7 | Fallos de validación de input sin evento de log | 11.2 | Abierta |
| 8 | Sin claves de idempotencia genéricas | 17.13 | Abierta |
| 9 | Sin criterios escritos para adoptar o mantener dependencias | 10.8, 18.5 | Abierta |
| 10 | Sin atestaciones de procedencia ni firma de SBOM e imágenes | 18.6, 18.9 | Abierta |
