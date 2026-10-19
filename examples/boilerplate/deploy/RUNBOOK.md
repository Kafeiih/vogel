# Runbook Operativo — Go Bluprint

Guia de respuesta a incidentes para el equipo de operaciones.

---

## 0. Configuracion del compose de despliegue

`deploy/docker-compose.yaml` lee `deploy/.env` (nunca se commitea; el
`.gitignore` lo excluye). La plantilla es `deploy/env.example`: copiarla a
`deploy/.env` y completarla. Ese archivo cumple dos funciones: compose lo usa
para interpolar las variables `${...}` del propio compose y lo pasa como
`env_file` a los procesos que sirven (`api`, y `worker` si existe). Variables obligatorias (si falta alguna,
`docker compose` falla antes de crear contenedores):

| Variable | Uso |
|---|---|
| `POSTGRES_PASSWORD` | Superusuario `postgres`; solo lo usa el contenedor de Postgres |
| `APP_MIGRATOR_PASSWORD` | Rol `app_migrator`, dueño del esquema y las tablas; solo lo usan `migrate` y `grants` |
| `APP_RUNTIME_PASSWORD` | Rol `app_runtime` de `api` (y `worker`, si existe): sin ser dueño de nada, `SELECT` e `INSERT` sobre `audit_log` |
| `APP_ENV` | `development` con este compose, `production` en produccion. Ya no decide el TLS hacia la base (eso es `DB_REQUIRE_TLS`, abajo); en `production` rechaza `SWAGGER_ENABLED=true` |
| `APP_VERSION` | Tag de las imagenes `ghcr.io/kafeiih/vogel-boilerplate*` del servicio (nunca `latest`); la API tambien lo informa como su version |
| `ZITADEL_ISSUER`, `ZITADEL_CLIENT_ID`, `CERBOS_HOST`, ... | Configuracion de la aplicacion, igual que en el `.env` de desarrollo |

Ajustes que no son obligatorios pero conviene conocer (los valores por
defecto entre parentesis; un valor que no se puede interpretar hace fallar el
arranque):

| Variable | Uso |
|---|---|
| `DB_REQUIRE_TLS` | (`true`, con cualquier `APP_ENV`) exige `sslmode=verify-full` o `verify-ca` en `DB_URL`. Rige también `api migrate`. El compose lo fija en `false` solo para `migrate` y los procesos que sirven, porque el Postgres empaquetado no sirve TLS (ver abajo); no definirlo en `deploy/.env` |
| `ZITADEL_HTTP_TIMEOUT` | (`10s`, mayor que cero) tiempo maximo de cada request hacia el issuer (discovery, JWKS, introspeccion) |
| `ZITADEL_MAX_TOKEN_LIFETIME` | (`12h`; `0` desactiva el control) vida maxima aceptada de un token (`exp - iat`) |
| `ZITADEL_ALLOWED_AUTHORIZED_PARTIES` | (vacio: no se controla `azp`) client IDs aceptados como `azp`, separados por coma |
| `ZITADEL_INTROSPECTION` | (`false`) valida cada token contra el endpoint de introspeccion, para que una revocacion tenga efecto antes de que expire; exige exactamente uno de `ZITADEL_INTROSPECTION_KEY_FILE` o `ZITADEL_INTROSPECTION_CLIENT_SECRET`, y acepta `ZITADEL_INTROSPECTION_CACHE_TTL` (`30s`) y `ZITADEL_INTROSPECTION_CACHE_MAX_ENTRIES` (`10000`) |

Las tres contraseñas viajan dentro de URLs `postgres://`: generarlas sin
caracteres especiales, p. ej. `openssl rand -hex 24`. Los procesos que sirven
reciben `POSTGRES_PASSWORD` y `APP_MIGRATOR_PASSWORD` vacias aunque esten en
`.env`, para que el proceso que atiende requests no tenga credenciales capaces
de alterar el esquema.

`migrate`, `grants` y los procesos que sirven corren con el sistema de
archivos raiz de solo lectura (`read_only: true`) y un `tmpfs` chico en
`/tmp`, sin ninguna capability de Linux (`cap_drop: [ALL]`) y con
`no-new-privileges`: los binarios no escriben a disco (los logs van a stdout;
el worker, si existe, solo escribe su archivo de heartbeat en `/tmp`) ni
necesitan privilegios. Postgres conserva solo las capabilities que usa su entrypoint
para ajustar permisos del directorio de datos y bajar al usuario `postgres`
(`CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `SETGID`, `SETUID`), tambien con
`no-new-privileges`. Las imagenes de los compose y las bases del `Dockerfile`
estan fijadas por version exacta y digest; ver «Parches» abajo.

Healthchecks: la imagen de la API consulta `/v1/healthz`.
<!-- vogel:component worker begin -->
La del worker,
que no expone puertos, corre `./worker healthcheck`, que falla si el worker
no reescribio su archivo de heartbeat (en `/tmp`, cada 10s mientras procesa
trabajos; ver `cmd/worker/heartbeat.go`) en los ultimos 30s. Un worker `unhealthy` esta
colgado o deteniendose; no indica por si solo que la base este caida.
<!-- vogel:component worker end -->

Los roles los crea `deploy/initdb/10-roles.sh` en el **primer** arranque del
volumen (`/docker-entrypoint-initdb.d`). El script es idempotente: correrlo
otra vez a mano (ver «Actualizar un volumen existente») crea
el rol que falte y fija las dos contraseñas a los valores actuales de
`deploy/.env`. Despues de cada `migrate up`, el servicio de una
sola ejecucion `grants` aplica `deploy/initdb/runtime-grants.sql` como
`app_migrator`: `SELECT`/`INSERT` sobre `audit_log` y `SELECT` sobre las
tablas de version de goose.
<!-- vogel:component worker begin -->
Tambien DML sobre las tablas de River.
<!-- vogel:component worker end -->
Las tablas
propias de la aplicacion reciben DML de `app_runtime` por los privilegios por
defecto que deja `10-roles.sh`.

Postgres no publica puertos en el host: solo es accesible desde la red del
compose. Para inspeccionarlo:
`docker compose -f deploy/docker-compose.yaml exec postgres psql -U postgres boilerplate_db`.
(`docker-compose.developer.yaml` si publica el 5433, solo para desarrollo.)

**TLS:** dentro de la red del compose los DSN usan `sslmode=disable` porque
el Postgres empaquetado no sirve certificado, y por eso el compose fija
`DB_REQUIRE_TLS=false` en `migrate` y los procesos que sirven (con su valor por defecto, `true`,
el arranque rechaza ese DSN sea cual sea `APP_ENV`). Eso solo es aceptable en
una red privada de compose. **En produccion** la base debe ser un PostgreSQL
con TLS, los DSN deben usar `sslmode=verify-full` (con `sslrootcert` si la CA
es privada) y hay que quitar ese `DB_REQUIRE_TLS=false` del compose: con
`DB_REQUIRE_TLS=true` se rechazan `disable`, `allow`, `prefer` y `require`.

**Swagger:** `APP_ENV=development` no lo activa. La UI y la spec solo se
sirven con `SWAGGER_ENABLED=true`, que `deploy/.env` no debe definir; con
`APP_ENV=production` esa combinacion hace fallar el arranque.

**Cerbos:** `CERBOS_HOST` y `CERBOS_PLAINTEXT` son obligatorias en
`deploy/.env` y van siempre de a pares: sin cualquiera de las dos,
`docker compose` no arranca. Un PDP en otro host se usa con su host y
`CERBOS_PLAINTEXT=false` (TLS), nunca con `true`. Un modo TLS equivocado hace
fallar el arranque del `api` con un error que nombra el modo. Un PDP sin
politicas reporta sano y niega todo: despues de cada despliegue de politicas,
verifique que el PDP las cargo.
<!-- vogel:component cerbos begin -->

El servicio `cerbos` corre la imagen del PDP con las politicas de
`cerbos/policies` horneadas (ver `cerbos/README.md`). Sirve gRPC sin TLS, por
eso vive solo en la red `authz`, que es `internal` y no publica puertos: solo
el `api` llega a el. `env.example` trae el par del PDP empaquetado
(`cerbos:3593`, `true`). Para usar un PDP externo, quite del compose el
servicio `cerbos`, la entrada `cerbos` del `depends_on` del `api` y su red
`authz`; si no, el `api` sigue esperando al PDP empaquetado. Las politicas
viven en `/bundle/policies` de la imagen, no en un volumen, asi que cada
version nueva sirve sus propias politicas. Para verificar que las cargo:

```bash
docker compose -f deploy/docker-compose.yaml logs cerbos | grep "executable policies"
# "Found 0 executable policies" = toda verificacion de permisos responde 403
```
<!-- vogel:component cerbos end -->

### Parches

Nada se actualiza solo: las imagenes y los modulos estan fijados para que
cada build sea reproducible, asi que los parches llegan como PRs que alguien
revisa y despliega.

| Que | Cadencia | Mecanismo |
|---|---|---|
| Imagenes base del `Dockerfile` (`golang`, `alpine`) | Semanal | Dependabot (`docker`, `.github/dependabot.yml`) propone tag y digest nuevos en un PR agrupado |
| Imagenes de los compose (`postgres`, `cerbos`) | Semanal | Dependabot (`docker-compose`) hace lo mismo en `deploy/` y `examples/api/` |
| Modulos Go (vogel y este boilerplate) | Semanal | Dependabot (`gomod`), un PR agrupado por modulo; `govulncheck` en CI |
| Vulnerabilidades conocidas en las imagenes construidas | Cada push o PR y cada lunes (06:17 UTC) | Workflow `Trivy` (`.github/workflows/trivy.yaml`): construye las imagenes del servicio desde este `Dockerfile` y falla con hallazgos HIGH o CRITICAL que tengan version corregida |
| Paquetes del sistema de la imagen (`apk`) | En cada rebuild | El runtime parte de `alpine` fijado por digest: un rebuild sin bump no trae parches, por eso importa mergear el PR semanal de Dependabot |

Objetivo sugerido (ajustarlo a la politica del equipo): mergear y desplegar
el PR semanal de Dependabot dentro de la misma semana (reconstruir con
`docker compose -f deploy/docker-compose.yaml build` y desplegar con un
`APP_VERSION` nuevo, ver seccion 5), y corregir un hallazgo CRITICAL de Trivy
en produccion en 48 horas.

**Si el workflow `Trivy` falla:**

1. Abrir el log del paso que fallo: `Scan config` (configuracion del
   `Dockerfile`) o `Scan images` (paquete del sistema o modulo Go con CVE).
   La tabla indica paquete, version instalada y version corregida.
2. Si es un modulo Go: subirlo a la version corregida en el `go.mod` que
   corresponda (`go get <modulo>@<version> && go mod tidy`), en su propio PR.
   Si el modulo llega por `vogel`, actualizar tambien el `go.mod` de la raiz.
3. Si es un paquete de la imagen base: esperar o adelantar el bump de
   Dependabot (`docker buildx imagetools inspect <imagen:tag>` da el digest
   nuevo) y reconstruir.
4. Si no hay version corregida, el workflow ya lo ignora
   (`--ignore-unfixed`). Si se decide aceptar un hallazgo con correccion
   disponible, registrarlo en `.trivyignore` en la raiz con el CVE, el
   motivo y una fecha de revision; nunca desactivar el paso.
5. Una falla en el cron del lunes sin cambios en el repositorio es una
   advisory nueva contra lo que ya esta desplegado: tratarla como incidente
   segun su severidad (seccion 12), no solo como CI en rojo.

### Actualizar un volumen existente

Aplica a un volumen `pgdata` creado con el compose anterior, en el que
`migrate` y los procesos que sirven usaban el superusuario `postgres`. En ese volumen
los roles `app_migrator` y `app_runtime` no existen (`10-roles.sh` corre solo
sobre un volumen vacio) y todas las tablas son de `postgres`, asi que
`migrate` como `app_migrator` fallaria. **No hace falta borrar el volumen**:

1. Respaldar la base (ver seccion 9):
   ```bash
   docker compose -f deploy/docker-compose.yaml exec -T postgres \
     pg_dump -U postgres -Fc boilerplate_db > respaldo-pre-roles.dump
   ```
2. Detener lo que escribe (el paso 3 vuelve a levantar solo Postgres):
   `docker compose -f deploy/docker-compose.yaml stop`.
3. Actualizar el compose y completar `deploy/.env` con las variables de
   arriba. Levantar solo Postgres, que ahora recibe las contraseñas de los
   roles: `docker compose -f deploy/docker-compose.yaml up -d postgres`.
4. Crear los roles corriendo el mismo script de inicializacion como
   superusuario:
   ```bash
   docker compose -f deploy/docker-compose.yaml exec postgres \
     sh /docker-entrypoint-initdb.d/10-roles.sh
   ```
5. Transferir a `app_migrator` los objetos que crearon las migraciones
   anteriores y dar a `app_runtime` DML sobre las tablas que ya existian:
   ```bash
   docker compose -f deploy/docker-compose.yaml exec -T postgres \
     psql -v ON_ERROR_STOP=1 -U postgres -d boilerplate_db \
     < deploy/initdb/upgrade-ownership.sql
   ```
<!-- vogel:component worker begin -->
   Si se cambio `WORKER_SCHEMA`, agregue `-v worker_schema=<esquema>` al
   `psql` (por defecto el script usa `river`).
<!-- vogel:component worker end -->
6. Desplegar normalmente: `docker compose -f deploy/docker-compose.yaml up -d`.
   `migrate` corre como `app_migrator`, `grants` aplica
   `runtime-grants.sql` y los procesos que sirven arrancan como `app_runtime`.
7. Cambiar la contraseña del superusuario, que en el compose anterior era
   `postgres` y que `POSTGRES_PASSWORD` no cambia en un volumen existente:
   ```bash
   docker compose -f deploy/docker-compose.yaml exec postgres \
     psql -U postgres -c "\password postgres"
   ```

Verificar que `app_runtime` quedo con los privilegios esperados:

```sql
SELECT t,
       has_table_privilege('app_runtime', t, 'INSERT') AS insert,
       has_table_privilege('app_runtime', t, 'UPDATE') AS update
FROM unnest(ARRAY['audit_log'
-- vogel:component worker begin
  , 'river.river_job'
-- vogel:component worker end
]) AS t;
-- audit_log: insert t, update f.
-- vogel:component worker begin
-- river.river_job: insert t, update t.
-- vogel:component worker end
```

**Por que no `REASSIGN OWNED BY postgres TO app_migrator`:** `postgres` es el
superusuario de arranque y es dueño del catalogo del sistema, asi que
PostgreSQL rechaza el comando ("required by the database system"). Aun con
otro dueño anterior, `REASSIGN OWNED` mueve todo lo que ese rol posee en la
base (la base misma, el esquema `public`, las extensiones), no solo las
tablas de la aplicacion. `upgrade-ownership.sql` nombra exactamente lo que
mueve: tablas, vistas, secuencias, funciones y tipos de `public` (y, con el
worker, del esquema de River y el esquema mismo) que no pertenecen a una
extension.
Las funciones de `pgcrypto` y la extension siguen siendo de `postgres`; el
esquema `public` conserva su dueño. Correr el script dos veces no cambia
nada.

Si algo falla a mitad de camino: `upgrade-ownership.sql` corre en una sola
transaccion (o se aplica entero o no se aplica), y los dos scripts se pueden
repetir. Como ultimo recurso, restaurar el respaldo del paso 1 con
`pg_restore --clean`.

---

## 1. Base de datos no responde

**Sintoma:** `/healthz` devuelve 503 con `"status": "degraded"`, `/readyz` devuelve 503.

**Verificar:**

```bash
# Desde el servidor de la app
curl -s http://localhost:8080/v1/healthz | jq .

# Conectividad directa a PostgreSQL
pg_isready -h <DB_HOST> -p 5432 -U <DB_USER>

# Estado del container de PostgreSQL
docker ps | grep postgres
docker logs postgres --tail 50
```

**Si PostgreSQL esta caido:**

1. Reiniciar el container: `docker compose -f deploy/docker-compose.yaml restart postgres`
2. Esperar al healthcheck (5s interval, 3 retries)
3. Verificar que `/healthz` vuelve a 200
4. Revisar logs de PostgreSQL para la causa: `docker logs postgres --since 30m`

**Si PostgreSQL responde pero la app no conecta:**

1. Verificar que `DB_URL` es correcto en las variables de entorno
2. Verificar que el firewall/security group permite el puerto 5432
3. Verificar SSL: en staging/produccion `sslmode=verify-full` (o
   `verify-ca`) es obligatorio. `RequireTLS` rechaza `sslmode=require` porque
   cifra sin verificar el certificado del servidor; si el arranque falla con
   ese error, ajustar el DSN y, si hace falta, `sslrootcert`
4. Revisar logs de la app: `docker logs api --tail 100 | grep "database"`

**Prevencion:** Configurar alerta en Prometheus cuando `db_pool_connections_acquired == db_pool_connections_max` por mas de 1 minuto.

---

## 2. Pool de conexiones agotado

**Sintoma:** Requests lentos o con timeout. Metrica `db_pool_acquire_canceled_total` subiendo.

**Verificar:**

```bash
# Metricas del pool
curl -s http://localhost:8080/metrics | grep db_pool

# Buscar valores criticos:
# db_pool_connections_acquired == db_pool_connections_max  → pool lleno
# db_pool_acquire_empty_total subiendo rapido             → no hay conexiones disponibles
# db_pool_acquire_duration_seconds subiendo               → espera larga para obtener conexion
```

**Si hay queries lentas bloqueando conexiones:**

1. Identificar queries lentas en PostgreSQL:
   ```sql
   SELECT pid, now() - pg_stat_activity.query_start AS duration, query
   FROM pg_stat_activity
   WHERE state != 'idle'
   ORDER BY duration DESC
   LIMIT 10;
   ```
2. Matar queries que lleven mas de 30s:
   ```sql
   SELECT pg_terminate_backend(pid)
   FROM pg_stat_activity
   WHERE duration > interval '30 seconds'
   AND state != 'idle';
   ```
3. Revisar slow query logs de la app: `docker logs api | grep "slow query"`

**Si hay pico de trafico:**

1. Verificar `http_requests_total` por rate inusual
2. Verificar si el rate limiting esta actuando: buscar respuestas con status 429
3. Si es trafico legitimo, escalar replicas y ajustar `DB_MAX_CONNS`

**Ajustar pool:**

El presupuesto de conexiones activas se calcula por el hardware del servidor de base de datos, no por usuarios ni requests concurrentes: punto de partida `(nucleos x 2) + discos` del servidor PostgreSQL, **en total**. Ese presupuesto se reparte entre todas las replicas: la suma de sus `DB_MAX_CONNS` debe quedar cerca de el. Mas conexiones no dan mas throughput; un pool siempre lleno con queries lentas se arregla en las queries (ver seccion 3), no subiendo el pool.

| Variable | Default | Cuando cambiar |
|----------|---------|-------------|
| `DB_MAX_CONNS` | 25 | Repartir el presupuesto entre replicas (ver abajo); subirlo solo si el servidor tiene CPU libre y el pool esta lleno con queries rapidas |
| `DB_MIN_CONNS` | 5 | Muchos `acquire_empty` (creando conexiones nuevas frecuentemente) |

El total entre todas las replicas no debe exceder `max_connections` de PostgreSQL (default: 100), con margen para administracion y migraciones:

```
replicas_api x DB_MAX_CONNS + replicas_worker x DB_MAX_CONNS + reserva < max_connections
```

Ejemplo: servidor de 4 nucleos y 1 disco -> presupuesto de ~9 conexiones activas en total. Con 3 replicas del API y 1 worker: `DB_MAX_CONNS` en 2 para cada API y en 3 para el worker suman 9, con `DB_MIN_CONNS` en 1 (nunca mayor que `DB_MAX_CONNS`: el arranque lo valida y falla). Si un pool de 2 por replica no alcanza para su concurrencia, poner PgBouncer delante del API en vez de subir los pools o `max_connections`. El worker no pasa por PgBouncer en modo transaccion (River necesita `LISTEN`), asi que el presupuesto se reparte entre los dos: por ejemplo, `default_pool_size` de PgBouncer en 6 para todo el API mas los 3 del worker suman 9 conexiones con el servidor. Ver `docs/WIRING.md` seccion 7 (que cambia en modo transaccion: timeouts con `ALTER ROLE` e iguales en `postgres.Config`, `default_query_exec_mode=exec` y el worker con conexion de sesion).

---

## 3. Slow queries en produccion

**Sintoma:** Logs con `"msg":"slow query detected"` y duracion > 200ms.

**Verificar:**

```bash
# Buscar slow queries en logs recientes
docker logs api --since 1h | grep "slow query"

# Ver las queries mas lentas en PostgreSQL
```

```sql
-- Top 10 queries por tiempo promedio (requiere pg_stat_statements)
SELECT query, calls, mean_exec_time, total_exec_time
FROM pg_stat_statements
ORDER BY mean_exec_time DESC
LIMIT 10;

-- Verificar indices faltantes
SELECT relname, seq_scan, idx_scan
FROM pg_stat_user_tables
WHERE seq_scan > idx_scan
ORDER BY seq_scan DESC;
```

**Acciones:**

1. Si es una query sin indice → crear indice y monitorear
2. Si es una tabla grande con seq scan → analizar el query plan con `EXPLAIN ANALYZE`
3. Si es un bloqueo → verificar locks:
   ```sql
   SELECT * FROM pg_locks WHERE NOT granted;
   ```

**Prevencion:** Configurar alerta cuando el P99 de latencia supere 2s (ver "Latencia alta" en [Alertas recomendadas](#alertas-recomendadas-prometheus)).

---

## 4. Rate limiting activado (429)

**Sintoma:** Usuarios reportan errores. Metrica `http_requests_total{status="429"}` subiendo.

**Verificar:**

```bash
# Ver cuantos 429 hay por endpoint
curl -s http://localhost:8080/metrics | grep 'http_requests_total.*429'
```

**Si es un ataque/abuso:**

1. Identificar la IP en los logs: `docker logs api | grep "429"`
2. Si es una sola IP, bloquear en nginx:
   ```nginx
   deny 1.2.3.4;
   ```
3. Considerar bajar el limite temporal: cambiar de 100 req/min a 50 req/min

**Si es trafico legitimo:**

1. Verificar que el cliente envia requests eficientemente (no polling excesivo)
2. Si el limite es bajo para el caso de uso, ajustar en `router.go`:
   - IP: `httprate.LimitByIP(100, 1*time.Minute)` → subir el 100
   - Usuario autenticado: `httprate.Limit(1000, ...)` → subir el 1000
3. Redesplegar con el nuevo limite

**Limites actuales:**

| Scope | Limite | Donde |
|-------|--------|-------|
| Por IP (global) | 100 req/min | Todas las rutas |
| Por usuario autenticado | 1000 req/min | Rutas autenticadas |
| Auditoria por usuario | 30 req/min | `/v1/audit/*` |

---

## 5. Deployment fallido

**Sintoma:** La nueva version no arranca o arranca con errores.

**Rollback inmediato:**

```bash
# Volver a la version anterior
docker compose -f deploy/docker-compose.yaml pull   # si hay imagen nueva
docker compose -f deploy/docker-compose.yaml down
# Cambiar APP_VERSION en deploy/.env al tag anterior
docker compose -f deploy/docker-compose.yaml up -d
```

**Si el problema es una migracion:**

Las migraciones no corren al arrancar los procesos que sirven (`./api server`
solo sirve): las aplica el servicio `migrate` del compose (`./api migrate up`)
antes de que arranquen. Si `migrate` falla, no arranca ninguno; revisar
`docker compose -f deploy/docker-compose.yaml logs migrate`.
<!-- vogel:component worker begin -->
`./worker server` solo procesa jobs. `./api migrate up` incluye el esquema y
las migraciones de River, y `./api migrate down` nunca revierte River: sus
migraciones de bajada borran los jobs encolados.
<!-- vogel:component worker end -->

1. **No** desplegar la version nueva hasta resolver la migracion
2. Revertir la ultima migracion con el subcomando del propio binario, usando
   la URL del rol migrador (el dueño de las tablas):
   ```bash
   # Con el compose de despliegue:
   docker compose -f deploy/docker-compose.yaml run --rm migrate migrate down
   # O con el binario directamente:
   DB_URL="<url del rol migrador>" ./api migrate down
   ```
   `migrate down` revierte solo la ultima migracion de este servicio, toma el
   mismo advisory lock que `migrate up` y nunca toca `audit_log` (ni River). No
   uses el CLI de `goose` directo: se salta ese lock y no sabe de las tablas
   de version separadas de cada conjunto. `./api migrate status` muestra el
   estado antes y despues.
   `cmd/api/migrate_integration_test.go` prueba que se revierte solo la
   ultima migracion de este servicio (y que su SQL de bajada corre) y que
   `audit_log` (y River) quedan intactos. El advisory lock no lo prueba ese
   test: lo cubren los tests de integracion de `vogel/migrate`.
3. Verificar que la version anterior funciona con el schema revertido
4. Investigar la causa del fallo en la migracion

**Si el problema es la app (no migracion):**

1. Revisar logs de inicio: `docker logs api --tail 50`
2. Errores comunes:
   - `environment variable X is required` → falta variable de entorno
   - `connecting to database` → DB_URL incorrecto o DB inaccesible
   - `initializing zitadel` → ZITADEL_ISSUER o CLIENT_ID incorrectos, o Zitadel no responde
3. Corregir y redesplegar, o hacer rollback a la version anterior

---

## 6. App no arranca

**Sintoma:** El container se reinicia repetidamente o se queda en estado "unhealthy".

**Verificar:**

```bash
# Estado del container
docker ps -a | grep api

# Logs de inicio
docker logs api --tail 100

# Si el container muere inmediatamente
docker logs api 2>&1 | head -20
```

**Errores comunes al inicio:**

| Error en log | Causa | Solucion |
|-------------|-------|----------|
| `config errors: environment variable DB_URL is required` | Falta variable | Verificar `.env` o variables de entorno |
| `connecting to database: pinging database` | DB no accesible | Verificar DB_URL, firewall, SSL |
| `initializing zitadel: context deadline exceeded` | Zitadel no responde en 15s | Verificar ZITADEL_ISSUER accesible |
| `address already in use` | Puerto ocupado | Otro proceso usa el puerto 8080 |

---

## 7. Certificado TLS / Zitadel JWKS

**Sintoma:** Todas las requests autenticadas fallan con 401.

**Verificar:**

```bash
# Probar endpoint publico (no requiere auth)
curl -s http://localhost:8080/v1/healthz

# Si healthz funciona pero auth falla, verificar Zitadel
curl -s https://<ZITADEL_ISSUER>/.well-known/openid-configuration
```

**Si Zitadel no responde:**

1. Verificar que `ZITADEL_ISSUER` es accesible desde el servidor de la app
2. Verificar DNS y firewall
3. Si Zitadel esta temporalmente caido, la app sigue rechazando requests hasta que pueda refrescar las JWKS keys
4. Reiniciar la app cuando Zitadel este disponible: `docker restart api`

**Si el certificado TLS de Zitadel expiro:**

1. Renovar el certificado en el servidor de Zitadel
2. Reiniciar la app para refrescar la cache de JWKS

---

## 8. Disco lleno (logs)

**Sintoma:** La app deja de escribir logs o PostgreSQL se detiene por falta de espacio.

**Verificar:**

```bash
df -h
docker system df
```

**Accion inmediata:**

```bash
# Limpiar logs de Docker antiguos
docker system prune -f

# Si los logs de un container son muy grandes
truncate -s 0 $(docker inspect --format='{{.LogPath}}' api)
```

**Prevencion:** Configurar log rotation en Docker daemon:

```json
// /etc/docker/daemon.json
{
  "log-driver": "json-file",
  "log-opts": {
    "max-size": "50m",
    "max-file": "3"
  }
}
```

---

## 9. Respaldos y prueba de restauracion

**Objetivo:** poder reconstruir la base (datos, `audit_log`, jobs) en otro
servidor y saber cuanto tarda. Un respaldo que nunca se restauro no cuenta.

| Elemento | Minimo |
|---|---|
| Que | `pg_dump -Fc` diario de `boilerplate_db` (o snapshots del servicio gestionado con PITR) |
| Cifrado | En reposo, con una clave guardada fuera del servidor de la base (p. ej. `age` o `gpg` con clave publica, o el KMS del proveedor), y en transito |
| Donde | Fuera del host y de la cuenta de la base (otro bucket o region), con escritura solo para el proceso de respaldo |
| Retencion | 7 diarios, 4 semanales, 12 mensuales (ajustar a la politica de datos personales) |
| Quien | Infraestructura |

**Respaldo (ejemplo con `age`):**

```bash
docker compose -f deploy/docker-compose.yaml exec -T postgres \
  pg_dump -U postgres -Fc boilerplate_db \
  | age -r "$BACKUP_AGE_PUBLIC_KEY" > "boilerplate_db-$(date +%F).dump.age"
# subir el archivo al almacenamiento de respaldos y verificar su tamaño
```

**Simulacro de restauracion (al menos trimestral):**

1. Bajar el respaldo mas reciente y descifrarlo en una maquina de pruebas:
   `age -d -i clave.txt respaldo.dump.age > respaldo.dump`.
2. Levantar un Postgres vacio de la misma version mayor, con la base y los
   roles creados por `deploy/initdb/10-roles.sh`.
3. Restaurar como el rol dueño:
   `pg_restore -U postgres -d boilerplate_db --no-owner --role=app_migrator respaldo.dump`.
4. Aplicar `deploy/initdb/runtime-grants.sql` como `app_migrator`.
5. Verificar: `SELECT count(*), max(created_at) FROM audit_log;` coincide con
   lo esperado; la API arranca contra esa base y `/v1/healthz` responde 200.
6. Registrar el resultado abajo y borrar la copia descifrada.

| Campo | Valor (lo completa el operador) |
|---|---|
| Ultima prueba de restauracion | `AAAA-MM-DD` |
| Respaldo usado (fecha) | |
| Tiempo total de restauracion | |
| Resultado y observaciones | |
| Responsable | |

---

## 10. Rotacion de secretos

Todos los secretos salen del entorno (`deploy/.env` o el gestor de secretos
de la plataforma): rotarlos no requiere cambios de codigo, solo cambiar el
valor y reiniciar el proceso que lo usa. Rotar ante cualquier sospecha de
exposicion y, como minimo, una vez al año.

| Secreto | Como rotarlo | Como verificar |
|---|---|---|
| `APP_RUNTIME_PASSWORD` | `ALTER ROLE` en el Postgres en marcha, sin recrear el contenedor ni el volumen: ver «Rotar las contraseñas de los roles» abajo | `/v1/healthz` responde 200, el worker (si existe) queda `healthy` y el log no muestra `password authentication failed` |
| `APP_MIGRATOR_PASSWORD` | Igual que la anterior; el valor nuevo lo usan `migrate` y `grants` en el proximo despliegue | `docker compose -f deploy/docker-compose.yaml run --rm migrate migrate status` |
| `POSTGRES_PASSWORD` (superusuario) | `docker compose -f deploy/docker-compose.yaml exec postgres psql -U postgres -c "\password postgres"` y actualizar `deploy/.env` | `psql` con la contraseña nueva |
| Zitadel (clave o secreto de la aplicacion API, si se usa introspeccion) | Crear una clave nueva en la consola de Zitadel (aplicacion API → Keys), desplegarla, reiniciar `api` (y `worker`, si existe) y recien entonces borrar la vieja. `ZITADEL_CLIENT_ID` no es secreto | Las requests autenticadas responden 200; el log no muestra `initializing zitadel` |
| Cerbos (certificado TLS del PDP) | Renovar el certificado en el PDP; si es de una CA propia, desplegar primero la CA nueva en los clientes | Las rutas con `RequirePermission` responden 200 o 403, no 503 |

<!-- vogel:component storage begin -->

| Secreto | Como rotarlo | Como verificar |
|---|---|---|
| Almacenamiento (`STORAGE_<NOMBRE>_ACCESS_KEY` y `_SECRET_KEY`) | Crear un segundo par de claves en el proveedor, desplegarlo, reiniciar, desactivar el par viejo y luego borrarlo | Una subida y una descarga de prueba; los enlaces firmados con el par viejo dejan de funcionar al desactivarlo |

<!-- vogel:component storage end -->
<!-- vogel:component notification begin -->

| Secreto | Como rotarlo | Como verificar |
|---|---|---|
| `SENDGRID_API_KEY` o `SMTP_PASSWORD` | Crear la clave nueva en el proveedor con el minimo permiso (solo envio), desplegarla, reiniciar `worker` y revocar la vieja | Un correo de prueba llega; el proveedor muestra la clave vieja sin uso |

<!-- vogel:component notification end -->

Despues de rotar por una exposicion, revisar `audit_log` y los logs del
proveedor afectado desde la fecha probable de la fuga.

### Rotar las contraseñas de los roles sin recrear Postgres

`deploy/initdb/10-roles.sh` (y el `POSTGRES_PASSWORD` de la imagen) solo
corren cuando el volumen `pgdata` esta **vacio**: en un volumen existente,
cambiar `APP_RUNTIME_PASSWORD` o `APP_MIGRATOR_PASSWORD` en `deploy/.env` no
cambia nada en la base. La contraseña vive en el catalogo de PostgreSQL y se
cambia con `ALTER ROLE` sobre la instancia en marcha; no hace falta recrear
el contenedor ni, mucho menos, el volumen.

1. Generar el valor nuevo (`openssl rand -hex 24`: viaja dentro de una URL
   `postgres://`, sin caracteres especiales).
2. Cambiarlo en la base. `\password` lo pide por teclado y envia solo el
   hash SCRAM, asi que el valor no queda en el historial del shell, en la
   lista de procesos ni en el log de Postgres:
   ```bash
   docker compose -f deploy/docker-compose.yaml exec postgres \
     psql -U postgres -d boilerplate_db -c "\password app_runtime"
   ```
   (`app_migrator` para `APP_MIGRATOR_PASSWORD`.) Las conexiones ya abiertas
   siguen funcionando; las nuevas exigen el valor nuevo.
3. Actualizar `APP_RUNTIME_PASSWORD` en `deploy/.env` (o en el gestor de
   secretos).
4. Recrear solo los procesos que usan ese rol, **sin** tocar Postgres:
   ```bash
   docker compose -f deploy/docker-compose.yaml up -d --no-deps api
   # vogel:component worker begin
   docker compose -f deploy/docker-compose.yaml up -d --no-deps worker
   # vogel:component worker end
   ```
   `--no-deps` importa: el servicio `postgres` tambien recibe esas variables
   (para `10-roles.sh`), asi que un `up -d` sin nombrar servicios lo
   recrearia por cambio de configuracion. Para `app_migrator` no hay que
   reiniciar nada: `migrate` y `grants` toman el valor en el proximo
   despliegue.
5. Verificar (tabla de arriba). Entre los pasos 2 y 4, una conexion nueva del
   pool viejo falla con `password authentication failed`; hacer los pasos
   seguidos, o en una ventana de bajo trafico.

El contenedor de Postgres sigue con el valor viejo en su entorno hasta que se
recree, lo que es inofensivo porque nadie lo lee en un volumen existente.
Excepcion: no correr `10-roles.sh` a mano con ese contenedor sin recrearlo
antes (`up -d postgres`), porque fijaria otra vez las contraseñas viejas.

---

## 11. Revocacion de tokens y sesiones

El boilerplate valida los tokens **localmente** (JWT firmado, el modo por
defecto de `vogel/auth/zitadel`): no consulta a Zitadel en cada request, asi
que **un token ya emitido sigue siendo aceptado hasta su `exp`** aunque la
sesion se cierre en Zitadel.

| Modo | Que pasa al revocar | Ventana |
|---|---|---|
| JWT local (el del boilerplate) | La API no se entera | Hasta el `exp` del token (la vida del access token configurada en Zitadel) |
| Introspeccion (`zitadel.Config.Introspection`, opt-in) | La API consulta a Zitadel y rechaza el token revocado | Hasta `CacheTTL` (30 s por defecto), nunca mas alla del `exp` |

**Revocar a un usuario (cuenta comprometida o baja):**

1. En la consola de Zitadel: usuario → **Desactivar** (o **Bloquear**) y
   cerrar sus sesiones; si es una cuenta de servicio, borrar sus claves.
2. Si sus permisos en Cerbos deben desaparecer ya, quitar sus roles o
   atributos en el origen del principal.
3. Esperar la ventana de la tabla de arriba. En un incidente grave con JWT
   local, rotar las claves de firma de la instancia de Zitadel invalida
   **todos** los tokens: todos los usuarios vuelven a iniciar sesion.
4. Revisar en `audit_log` lo que hizo ese actor:
   `SELECT * FROM audit_log WHERE actor_id = '<id>' ORDER BY created_at DESC;`.

**Acotar la ventana:** bajar la vida del access token en Zitadel (p. ej. 5 a
15 minutos, con refresh token), o activar la introspeccion agregando
`Introspection` (con `KeyFile` o `ClientSecret` de la aplicacion API) al
`zitadel.Config` que arma `internal/infrastructure/config`. Con
introspeccion hace falta rate limiting delante: cada token desconocido
cuesta una llamada a Zitadel.

---

## 12. Comunicacion de incidentes

**Quien:** quien detecta abre el incidente y avisa al responsable de guardia.
Seguridad decide si hay datos personales afectados; esa decision dispara las
notificaciones a la autoridad y a los afectados que exija la normativa
aplicable, dentro de sus plazos.

**Cuando:** aviso interno inmediato; actualizaciones cada hora mientras el
incidente este activo (cada 30 minutos si hay impacto a usuarios); informe de
cierre dentro de 5 dias habiles.

**Plantilla:**

```text
[INCIDENTE <id>] <titulo corto> - <ABIERTO | MITIGADO | CERRADO>
Severidad:     <critica | alta | media | baja>
Inicio:        <fecha y hora, zona horaria>   Deteccion: <fecha y hora>
Impacto:       <que no funciona / que datos y cuantos usuarios>
Datos personales afectados: <si | no | en evaluacion>
Que sabemos:   <hechos verificados>
Que hacemos:   <acciones en curso y responsable>
Proxima actualizacion: <hora>
Contacto:      <canal y responsable>
```

**Cierre:** linea de tiempo, causa raiz, acciones correctivas con
responsable y fecha y, si corresponde, la entrada en `CHANGELOG.md` o el
advisory de seguridad (ver `SECURITY.md` en la raiz de `vogel`).

---

## Alertas recomendadas (Prometheus)

Estas alertas, mas un SLO de disponibilidad (99.5% sin 5xx en 30 dias) con alertas de burn-rate en varias ventanas, estan listas para cargar en `deploy/prometheus/alerts.yml` (con `rule_files:` en `prometheus.yml`). Asumen `job="api"` en el scrape: ajusta los selectores si tu configuracion usa otro nombre. CI las valida con `promtool check rules` y corre sus tests (`alerts_test.yml`) con `promtool test rules`.

| Alerta | Condicion | Severidad |
|--------|-----------|-----------|
| DB caida | `/healthz` retorna 503 por > 30s | CRITICA |
| Pool agotado | `db_pool_connections_acquired == db_pool_connections_max` por > 1min | CRITICA |
| Latencia alta | `histogram_quantile(0.99, sum by (le, path) (rate(http_request_duration_seconds_bucket[5m]))) > 2` por > 5min | WARNING |
| Error rate alto | `sum(rate(http_requests_total{status=~"5.."}[5m])) / sum(rate(http_requests_total[5m])) > 0.05` | WARNING |
| Slow queries frecuentes | Mas de 10 logs "slow query" en 5 minutos | WARNING |
| Rate limiting excesivo | `rate(http_requests_total{status="429"}[5m])` > 50/min | WARNING |
| Goroutines creciendo | `go_goroutines` > 1000 por > 10min | WARNING |
| Pool acquire lento | `histogram_quantile(0.99, sum by (le) (rate(db_pool_acquire_wait_seconds_bucket[5m]))) > 1` (p99 de la espera; con `DB_ACQUIRE_TIMEOUT=3s`, cerca de 1s ya es saturacion) | WARNING |
| Conexion retenida | `sum(rate(db_pool_conn_hold_seconds_count[5m])) - sum(rate(db_pool_conn_hold_seconds_bucket{le=~"10|10.0"}[5m])) > 0` (conexiones devueltas despues de mas de `DB_LEAK_THRESHOLD`; el log WARN nombra el codigo que la pidio. Si cambias `DB_LEAK_THRESHOLD`, cambia tambien `le`: debe ser un limite de bucket del histograma, 0.001 a 30 segundos. El regex acepta `10` y `10.0` porque Prometheus 3.x normaliza `le` a decimal) | WARNING |

`http_request_duration_seconds` es un histograma: no tiene etiqueta `quantile`, los percentiles se calculan con `histogram_quantile` sobre la serie `_bucket`. `db_pool_acquire_duration_seconds` es un acumulado de segundos (sólo da un promedio); para percentiles de la espera por conexión está el histograma `db_pool_acquire_wait_seconds`.

---

## Contactos

| Rol | Responsabilidad |
|-----|----------------|
| Desarrollo | Bugs en la app, migraciones, queries |
| Infraestructura | Docker, nginx, PostgreSQL, red |
| Seguridad | Incidentes de auth, rate limiting, abuso |
