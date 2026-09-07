# API de ejemplo

Una API HTTP ejecutable que cablea **todos** los paquetes de `vogel` en una sola
vertical de negocio. No es un catálogo de snippets: es un `package main` que
arranca, atiende requests y persiste en PostgreSQL. El README de la raíz explica
*qué es* cada paquete; este directorio muestra *cómo se conectan entre sí*, que
es la parte que ninguna tabla de paquetes puede enseñar.

El dominio es deliberadamente chico —un documento que pasa por un flujo de
aprobación— para que lo interesante sea el cableado y no las reglas de negocio.

## Arranque rápido

```bash
cd examples/api
docker compose up -d   # PostgreSQL 17, publicado en el host en el 55432
cp env.example .env    # revise DATABASE_URL si cambió algo
go run .
```

El ejemplo lee el `.env` por su cuenta (`dotenv.go`), así que no hace falta
exportar nada en la shell. Dos aclaraciones que importan:

- **Corra `go run .` parado en `examples/api`**, no desde la raíz del repo: el
  archivo se busca como `.env` relativo al directorio actual.
- Una variable ya presente en el entorno **le gana** al archivo, así que
  `PORT=9000 go run .` hace lo que dice aunque el `.env` diga otra cosa.

`vogel/config` lee `os.Getenv` y nada más — no conoce archivos, ni rutas de
búsqueda, ni reglas de precedencia. Eso es a propósito: de dónde sale el entorno
(un `.env`, un secrets manager, Kubernetes, systemd) es una decisión del
consumidor, y `dotenv.go` es esa decisión tomada acá, en treinta líneas y sin
dependencias.

El contenedor publica el `5432` interno en el **55432** del host, para no
chocar con un PostgreSQL que ya esté escuchando en el puerto default. El
`DATABASE_URL` de `env.example` ya apunta ahí.

Las migraciones corren solas al arrancar. Verificación:

```bash
curl -s localhost:8080/healthz
```

### Autenticarse contra el ejemplo

El ejemplo trae un `auth.Authenticator` falso que acepta un token con la forma
`<userID>:<username>:<rol1,rol2>`. No hay nada que registrar:

```bash
TOKEN='u-1:oscar:admin'

# Crear un documento
curl -s -X POST localhost:8080/api/v1/documents \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"title":"Contrato marco","kind":"contract","issued_at":"03/02/2026","owner_id":"u-1"}'

# Ver el rastro de auditoría que esa única llamada dejó
curl -s "localhost:8080/api/v1/audit?resource_type=document" -H "Authorization: Bearer $TOKEN"
```

El `authz.Checker` falso permite todo al rol `admin` y sólo acciones de lectura
al resto. Pruebe con `u-2:ana:viewer` para ver un 403 real.

## Por dónde empezar a leer

El orden importa: cada archivo asume el anterior.

| Orden | Archivo | Lo que enseña |
|---|---|---|
| 1 | `main.go` | El **composition root**. Todo se construye acá y en ningún otro lado: config, pool, migraciones, métricas, adaptadores, cola, router, apagado ordenado. Si entiende este archivo, entiende el módulo. |
| 2 | `handler.go` → `Create` | La pieza central. Una transacción, cuatro escrituras. Explicado abajo. |
| 3 | `fakes.go` | Los cuatro puertos implementados en memoria, cada uno con el adaptador real que reemplaza. |
| 4 | `router.go` | El orden del stack de middleware, con el porqué de cada posición. |
| 5 | `store.go` | Un repositorio que recibe `pgxtx.DBTX` en vez de un pool, y por qué eso elimina la duplicación de métodos "con tx" y "sin tx". |
| 6 | `workflow.go` | Una `Definition` válida y una guarda, con la restricción que define al motor. |
| 7 | `jobs.go` | Un job de River que audita con `SourceWorker`, no con `SourceHTTP`. |
| 8 | `config.go` | El acumulador de errores de configuración: un arranque reporta *todos* los problemas juntos. |
| 9 | `dotenv.go` | Por qué `vogel/config` no lee archivos, y cómo el consumidor cubre ese hueco sin sumar dependencias. |

## La pieza central: `POST /documents`

Un solo `tx.WithTx` hace cuatro escrituras:

1. La fila del documento (`store.Create`)
2. El caso de workflow (`engine.Open`)
3. La entrada de auditoría (`recorder.Record`)
4. El job de notificación (`queue.EnqueueTx`)

Las cuatro commitean o no commitea ninguna. **Ese es el motivo de que
`worker.EnqueueTx` exista además de `Enqueue`**, y es el patrón *transactional
outbox*:

| Si en vez de `EnqueueTx` usara… | Qué se rompe |
|---|---|
| `Enqueue` **después** del commit | Una caída entre el commit y el encolado deja un documento cuya notificación no se envía nunca, y sin registro de que se debía |
| `Enqueue` **antes** del commit | Un worker puede procesar el job de un documento que un error posterior en el mismo handler termina revirtiendo: se notifica sobre algo que nunca existió |

`EnqueueTx` inserta la fila del job dentro de la **misma** transacción. El job se
vuelve visible para un worker si y sólo si todo commiteó.

## Cambiar un fake por un adaptador real

Ese es el punto de la arquitectura de puertos, y acá se ve en una línea:

| Puerto | Fake del ejemplo | Adaptador real | Dónde se cambia |
|---|---|---|---|
| `auth.Authenticator` | `fakeAuthenticator` | `auth/zitadel` | `main.go` |
| `authz.Checker` | `fakeChecker` | `authz/cerbos` | `main.go` |
| `storage.Storage` | `fakeStorage` (un `map` con mutex) | `storage/s3` | `main.go` |
| `notification.Notifier` | `fakeNotifier` (loguea) | `notification/smtp` o `notification/sendgrid` | `main.go` |

Ningún handler, ningún job y ningún repositorio cambia. Ese es el resultado que
la separación puerto/adaptador compra, y por eso el ejemplo usa fakes: si
requiriera Zitadel, Cerbos y MinIO levantados a mano, nadie lo abriría — y de
paso no demostraría nada, porque el cambio quedaría invisible.

## Rutas

Todo bajo `/api/v1` exige `Authorization: Bearer <token>`.

| Método | Ruta | Permiso | Qué demuestra |
|---|---|---|---|
| `POST` | `/documents` | write | La transacción de cuatro escrituras |
| `GET` | `/documents` | read | Paginación con `response.JSONList` |
| `GET` | `/documents/{id}` | read | `pgxtx.DBFromContext` fuera de transacción |
| `PUT` | `/documents/{id}/file` | write | `storage.Upload` + auditoría con `WithChange` (snapshot antes/después) |
| `GET` | `/documents/{id}/download-url` | read | `storage.PresignedGetURL` |
| `POST` | `/documents/{id}/transitions` | write | `engine.Move`, con guarda y errores mapeados a 409 |
| `POST` | `/documents/{id}/claim` | write | `engine.Claim` con el actor sacado de `auth.FromContext` |
| `GET` | `/documents/{id}/history` | read | `engine.History` |
| `GET` | `/inbox` | read | `engine.Inbox` por elegibilidad |
| `GET` | `/audit`, `/audit/{id}` | read | El handler de `audit/httpx` montado tal cual |

Sin autenticación: `GET /healthz` y `GET /metrics` (Prometheus).

## Tres conjuntos de migraciones, tres tablas de versión

`main.go` llama a `migrate.Up` **tres veces**:

| Conjunto | Tabla de versión |
|---|---|
| Las de esta app (`migrations/`) | `goose_db_version` (default) |
| `audit/migrations` | `vogel_db_version` |
| `workflow/migrations` | `workflow_db_version` |

Los tres empiezan a numerar en `001`. Con una sola tabla compartida, el segundo
en correr se vería como "ya aplicado" y se saltearía en silencio. Por eso
`migrate.Options.TableName` es un parámetro del llamador y no una constante
escondida en la librería.

## Lo que este ejemplo deliberadamente NO hace

Saber qué se dejó afuera vale tanto como lo que está adentro.

- **No limita el rate.** `httpx/middleware.RateLimitJSON()` se apoya en
  `github.com/go-chi/httprate`, que no es dependencia de `vogel`. Agregarla sólo
  para el ejemplo le impondría una dependencia a todos los consumidores. En su
  app, si ya tiene `httprate`:
  ```go
  r.Use(httprate.Limit(100, time.Minute,
      httprate.WithLimitHandler(vmw.RateLimitJSON())))
  ```
- **No separa API y worker.** Acá un mismo proceso atiende HTTP *y* ejecuta
  jobs, porque un ejemplo de dos binarios se lee peor. En producción se separan:
  el proceso de API construye la cola con `workers == nil` (modo sólo-inserción)
  y un binario aparte le pasa los workers reales. Esa es exactamente la razón de
  que `worker.NewRiverQueue` acepte `nil` ahí.
- **No tiene tests.** Es una guía de lectura, no una referencia de testing. Los
  tests de cada paquete viven junto a su paquete.
- **No valida la configuración de producción.** `RequireTLS` está en `false` para
  que el compose local funcione. En producción va en `true`, y `postgres.NewPool`
  falla en el arranque si el DSN deshabilita TLS.

## Siguiente paso

Copie `examples/api/` a su repo, borre `fakes.go`, cambie las cuatro líneas de
`main.go` por los adaptadores reales, y reemplace `Document` por su primera
entidad. El resto del cableado ya está resuelto.
