# Runbook Operativo — Go Bluprint

Guia de respuesta a incidentes para el equipo de operaciones.

---

## 0. Configuracion del compose de despliegue

`deploy/docker-compose.yaml` lee `deploy/.env` (nunca se commitea; el
`.gitignore` lo excluye). La plantilla es `deploy/env.example`: copiarla a
`deploy/.env` y completarla. Ese archivo cumple dos funciones: compose lo usa
para interpolar las variables `${...}` del propio compose y lo pasa como
`env_file` a `api` y `worker`. Variables obligatorias (si falta alguna,
`docker compose` falla antes de crear contenedores):

| Variable | Uso |
|---|---|
| `POSTGRES_PASSWORD` | Superusuario `postgres`; solo lo usa el contenedor de Postgres |
| `APP_MIGRATOR_PASSWORD` | Rol `app_migrator`, dueño del esquema y las tablas; solo lo usan `migrate` y `grants` |
| `APP_RUNTIME_PASSWORD` | Rol `app_runtime` de `api` y `worker`: sin ser dueño de nada, `SELECT` e `INSERT` sobre `audit_log` |
| `APP_ENV` | `development` con el Postgres del compose, que no sirve TLS (ver abajo) |
| `APP_VERSION` | Tag de las imagenes `vogel-boilerplate` y `vogel-boilerplate-worker` (nunca `latest`); la API tambien lo informa como su version |
| `ZITADEL_ISSUER`, `ZITADEL_CLIENT_ID`, `CERBOS_HOST`, ... | Configuracion de la aplicacion, igual que en el `.env` de desarrollo |

Las tres contraseñas viajan dentro de URLs `postgres://`: generarlas sin
caracteres especiales, p. ej. `openssl rand -hex 24`. `api` y `worker`
reciben `POSTGRES_PASSWORD` y `APP_MIGRATOR_PASSWORD` vacias aunque esten en
`.env`, para que el proceso que atiende requests no tenga credenciales capaces
de alterar el esquema.

`migrate`, `api` y `worker` corren con el sistema de archivos raiz de solo
lectura (`read_only: true`) y un `tmpfs` chico en `/tmp`: los binarios no
escriben a disco (los logs van a stdout). La imagen de Postgres y las bases
del `Dockerfile` estan fijadas por digest; para actualizarlas, `docker buildx
imagetools inspect <imagen:tag>` y reemplazar el `sha256`.

Los roles los crea `deploy/initdb/10-roles.sh` en el **primer** arranque del
volumen (`/docker-entrypoint-initdb.d`). El script es idempotente: correrlo
otra vez a mano (ver «Actualizar un volumen existente») crea
el rol que falte y fija las dos contraseñas a los valores actuales de
`deploy/.env`. Despues de cada `migrate up`, el servicio de una
sola ejecucion `grants` aplica `deploy/initdb/runtime-grants.sql` como
`app_migrator`: `SELECT`/`INSERT` sobre `audit_log`, `SELECT` sobre las
tablas de version de goose y DML sobre las tablas de River. Las tablas
propias de la aplicacion reciben DML de `app_runtime` por los privilegios por
defecto que deja `10-roles.sh`.

Postgres no publica puertos en el host: solo es accesible desde la red del
compose. Para inspeccionarlo:
`docker compose -f deploy/docker-compose.yaml exec postgres psql -U postgres boilerplate_db`.
(`docker-compose.developer.yaml` si publica el 5433, solo para desarrollo.)

**TLS:** dentro de la red del compose los DSN usan `sslmode=disable` porque
el Postgres empaquetado no sirve certificado, y por eso `APP_ENV` debe ser
`development` (con cualquier otro valor `RequireTLS` rechaza el DSN). Eso solo
es aceptable en una red privada de compose. **En produccion** la base debe ser
un PostgreSQL con TLS y los DSN deben usar `sslmode=verify-full` (con
`sslrootcert` si la CA es privada), con `APP_ENV=production`: `RequireTLS`
rechaza `disable`, `allow`, `prefer` y `require`.

**Swagger:** `APP_ENV=development` no lo activa. La UI y la spec solo se
sirven con `SWAGGER_ENABLED=true`, que `deploy/.env` no debe definir; con
`APP_ENV=production` esa combinacion hace fallar el arranque.

### Actualizar un volumen existente

Aplica a un volumen `pgdata` creado con el compose anterior, en el que
`migrate`, `api` y `worker` usaban el superusuario `postgres`. En ese volumen
los roles `app_migrator` y `app_runtime` no existen (`10-roles.sh` corre solo
sobre un volumen vacio) y todas las tablas son de `postgres`, asi que
`migrate` como `app_migrator` fallaria. **No hace falta borrar el volumen**:

1. Respaldar la base (ver seccion 9):
   ```bash
   docker compose -f deploy/docker-compose.yaml exec -T postgres \
     pg_dump -U postgres -Fc boilerplate_db > respaldo-pre-roles.dump
   ```
2. Detener lo que escribe: `docker compose -f deploy/docker-compose.yaml stop api worker`.
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
     -v worker_schema=river < deploy/initdb/upgrade-ownership.sql
   ```
   (`worker_schema` debe coincidir con `WORKER_SCHEMA` si se cambio.)
6. Desplegar normalmente: `docker compose -f deploy/docker-compose.yaml up -d`.
   `migrate` corre como `app_migrator`, `grants` aplica
   `runtime-grants.sql` y `api` y `worker` arrancan como `app_runtime`.
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
FROM unnest(ARRAY['audit_log', 'river.river_job']) AS t;
-- audit_log: insert t, update f.  river.river_job: insert t, update t.
```

**Por que no `REASSIGN OWNED BY postgres TO app_migrator`:** `postgres` es el
superusuario de arranque y es dueño del catalogo del sistema, asi que
PostgreSQL rechaza el comando ("required by the database system"). Aun con
otro dueño anterior, `REASSIGN OWNED` mueve todo lo que ese rol posee en la
base (la base misma, el esquema `public`, las extensiones), no solo las
tablas de la aplicacion. `upgrade-ownership.sql` nombra exactamente lo que
mueve: tablas, vistas, secuencias, funciones y tipos de `public` y del
esquema de River que no pertenecen a una extension, y el esquema de River.
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

| Variable | Default | Cuando subir |
|----------|---------|-------------|
| `DB_MAX_CONNS` | 25 | Pool constantemente lleno |
| `DB_MIN_CONNS` | 5 | Muchos `acquire_empty` (creando conexiones nuevas frecuentemente) |

Tener en cuenta: el total de conexiones entre todas las replicas no debe exceder `max_connections` de PostgreSQL (default: 100).

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

**Prevencion:** Configurar alerta cuando `http_request_duration_seconds{quantile="0.99"}` supere 2s.

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

Las migraciones no corren al arrancar la API ni el worker (`./api server` solo
sirve y `./worker server` solo procesa jobs): las aplica el servicio `migrate`
del compose (`./api migrate up`, que incluye el esquema y las migraciones de
River) antes de que arranquen `api` y `worker`. Si `migrate` falla, ninguno
de los dos arranca; revisar
`docker compose -f deploy/docker-compose.yaml logs migrate`. `./api migrate
down` nunca revierte River: sus migraciones de bajada borran los jobs
encolados.

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
   mismo advisory lock que `migrate up` y nunca toca `audit_log` ni River. No
   uses el CLI de `goose` directo: se salta ese lock y no sabe de las tablas
   de version separadas de cada conjunto. `./api migrate status` muestra el
   estado antes y despues.
   `cmd/api/migrate_integration_test.go` prueba que se revierte solo la
   ultima migracion de este servicio (y que su SQL de bajada corre) y que
   `audit_log` y River quedan intactos. El advisory lock no lo prueba ese
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

**Objetivo:** poder reconstruir la base (datos, `audit_log`, River) en otro
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
| `APP_RUNTIME_PASSWORD` | Cambiar el valor en `deploy/.env`, recrear Postgres para que reciba el valor (`docker compose -f deploy/docker-compose.yaml up -d postgres`) y correr `docker compose -f deploy/docker-compose.yaml exec postgres sh /docker-entrypoint-initdb.d/10-roles.sh`, que fija las dos contraseñas a los valores actuales; luego `docker compose -f deploy/docker-compose.yaml up -d api worker` | `/v1/healthz` responde 200 y el log no muestra `password authentication failed` |
| `APP_MIGRATOR_PASSWORD` | Igual que la anterior (el mismo script); el valor nuevo lo usan `migrate` y `grants` en el proximo despliegue | `docker compose -f deploy/docker-compose.yaml run --rm migrate migrate status` |
| `POSTGRES_PASSWORD` (superusuario) | `docker compose -f deploy/docker-compose.yaml exec postgres psql -U postgres -c "\password postgres"` y actualizar `deploy/.env` | `psql` con la contraseña nueva |
| Zitadel (clave o secreto de la aplicacion API, si se usa introspeccion) | Crear una clave nueva en la consola de Zitadel (aplicacion API → Keys), desplegarla, reiniciar `api` y `worker` y recien entonces borrar la vieja. `ZITADEL_CLIENT_ID` no es secreto | Las requests autenticadas responden 200; el log no muestra `initializing zitadel` |
| Cerbos (certificado TLS del PDP) | Renovar el certificado en el PDP; si es de una CA propia, desplegar primero la CA nueva en los clientes | Las rutas con `RequirePermission` responden 200 o 403, no 503 |
| Almacenamiento (`STORAGE_<NOMBRE>_ACCESS_KEY` y `_SECRET_KEY`) | Crear un segundo par de claves en el proveedor, desplegarlo, reiniciar, desactivar el par viejo y luego borrarlo | Una subida y una descarga de prueba; los enlaces firmados con el par viejo dejan de funcionar al desactivarlo |
| `SENDGRID_API_KEY` o `SMTP_PASSWORD` | Crear la clave nueva en el proveedor con el minimo permiso (solo envio), desplegarla, reiniciar `api` y `worker` y revocar la vieja | Un correo de prueba llega; el proveedor muestra la clave vieja sin uso |

Despues de rotar por una exposicion, revisar `audit_log` y los logs del
proveedor afectado desde la fecha probable de la fuga.

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

| Alerta | Condicion | Severidad |
|--------|-----------|-----------|
| DB caida | `/healthz` retorna 503 por > 30s | CRITICA |
| Pool agotado | `db_pool_connections_acquired == db_pool_connections_max` por > 1min | CRITICA |
| Latencia alta | `http_request_duration_seconds{quantile="0.99"}` > 2s por > 5min | WARNING |
| Error rate alto | `rate(http_requests_total{status=~"5.."}[5m])` > 5% del total | WARNING |
| Slow queries frecuentes | Mas de 10 logs "slow query" en 5 minutos | WARNING |
| Rate limiting excesivo | `rate(http_requests_total{status="429"}[5m])` > 50/min | WARNING |
| Goroutines creciendo | `go_goroutines` > 1000 por > 10min | WARNING |
| Pool acquire lento | `rate(db_pool_acquire_duration_seconds[5m])` > 1s promedio | WARNING |

---

## Contactos

| Rol | Responsabilidad |
|-----|----------------|
| Desarrollo | Bugs en la app, migraciones, queries |
| Infraestructura | Docker, nginx, PostgreSQL, red |
| Seguridad | Incidentes de auth, rate limiting, abuso |
