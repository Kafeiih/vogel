# Runbook Operativo — Go Bluprint

Guia de respuesta a incidentes para el equipo de operaciones.

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
3. Verificar SSL: en staging/produccion `sslmode=require` es obligatorio
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
# Cambiar la imagen al tag anterior en docker-compose.yaml o .env
docker compose -f deploy/docker-compose.yaml up -d
```

**Si el problema es una migracion:**

1. **No** desplegar la version nueva hasta resolver la migracion
2. Revertir la ultima migracion:
   ```bash
   goose -dir internal/infrastructure/database/migrations postgres "$DB_URL" down
   ```
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
