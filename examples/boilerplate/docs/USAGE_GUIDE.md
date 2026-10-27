# Guia de Uso del Boilerplate

Manual paso a paso para agregar un nuevo modulo de negocio usando este boilerplate.

---

## Prerequisitos

```bash
# Herramientas requeridas
go install github.com/pressly/goose/v3/cmd/goose@latest

# Infra de desarrollo
make dev-infra    # Levanta Postgres + Cerbos
```

---

## Agregar un nuevo modulo: ejemplo "Proveedor"

### Paso 1 — Dominio

Crear la entidad y la interfaz de repositorio en el mismo archivo. El dominio NO importa nada externo.

```go
// internal/domain/proveedor/proveedor.go
package proveedor

import (
    "context"
    "time"

    "github.com/google/uuid"
    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgconn"
)

type Proveedor struct {
    ID        uuid.UUID
    Rut       string
    Nombre    string
    Activo    bool
    CreatedAt time.Time
    UpdatedAt time.Time
}

// DBTX permite que el repositorio funcione con pool o transaccion.
type DBTX interface {
    Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
    Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
    QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Filters struct {
    Nombre string
    Activo *bool
    Limit  int
    Offset int
}

type Repository interface {
    Create(ctx context.Context, db DBTX, p *Proveedor) error
    GetByID(ctx context.Context, id uuid.UUID) (*Proveedor, error)
    Update(ctx context.Context, db DBTX, p *Proveedor) error
    Delete(ctx context.Context, db DBTX, id uuid.UUID) error
    List(ctx context.Context, f Filters) ([]Proveedor, int, error)
}
```

**Reglas del dominio:**
- Cero imports de infraestructura (ni chi, ni pgxpool, ni slog)
- `DBTX` se define en el dominio como contrato — la infra lo satisface
- `Create` y `Update` reciben `DBTX` para participar en transacciones

---

### Paso 2 — Migracion SQL

```bash
make migrate-create name=create_proveedores
```

Editar el archivo generado:

```sql
-- +goose Up
CREATE TABLE proveedores (
    id         UUID PRIMARY KEY,
    rut        TEXT NOT NULL UNIQUE,
    nombre     TEXT NOT NULL,
    activo     BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_proveedores_rut ON proveedores (rut);

-- +goose Down
DROP TABLE IF EXISTS proveedores;
```

```bash
make migrate-up
```

---

### Paso 3 — Repositorio (infraestructura)

Implementar la interfaz del dominio usando pgx directo.

```go
// internal/infrastructure/repository/proveedor_postgres.go
package repository

import (
    "context"
    "fmt"

    "github.com/google/uuid"
    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgxpool"

    domain "github.com/kafeiih/vogel/examples/boilerplate/internal/domain/proveedor"
)

type ProveedorPostgresRepo struct {
    pool *pgxpool.Pool
}

func NewProveedorPostgresRepo(pool *pgxpool.Pool) *ProveedorPostgresRepo {
    return &ProveedorPostgresRepo{pool: pool}
}

func (r *ProveedorPostgresRepo) Create(ctx context.Context, db domain.DBTX, p *domain.Proveedor) error {
    _, err := db.Exec(ctx,
        `INSERT INTO proveedores (id, rut, nombre, activo, created_at, updated_at)
         VALUES ($1, $2, $3, $4, $5, $6)`,
        p.ID, p.Rut, p.Nombre, p.Activo, p.CreatedAt, p.UpdatedAt,
    )
    return err
}

func (r *ProveedorPostgresRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Proveedor, error) {
    row := r.pool.QueryRow(ctx,
        `SELECT id, rut, nombre, activo, created_at, updated_at
         FROM proveedores WHERE id = $1`, id,
    )
    var p domain.Proveedor
    err := row.Scan(&p.ID, &p.Rut, &p.Nombre, &p.Activo, &p.CreatedAt, &p.UpdatedAt)
    if err != nil {
        return nil, err
    }
    return &p, nil
}

func (r *ProveedorPostgresRepo) List(ctx context.Context, f domain.Filters) ([]domain.Proveedor, int, error) {
    rows, err := r.pool.Query(ctx,
        `SELECT id, rut, nombre, activo, created_at, updated_at,
                count(*) OVER()::INT AS total
         FROM proveedores
         WHERE ($1::TEXT IS NULL OR nombre ILIKE '%' || $1 || '%')
           AND ($2::BOOLEAN IS NULL OR activo = $2)
         ORDER BY created_at DESC
         LIMIT $3 OFFSET $4`,
        nullString(f.Nombre), f.Activo, f.Limit, f.Offset,
    )
    if err != nil {
        return nil, 0, fmt.Errorf("list proveedores: %w", err)
    }
    defer rows.Close()

    var result []domain.Proveedor
    var total int
    for rows.Next() {
        var p domain.Proveedor
        if err := rows.Scan(&p.ID, &p.Rut, &p.Nombre, &p.Activo,
            &p.CreatedAt, &p.UpdatedAt, &total); err != nil {
            return nil, 0, err
        }
        result = append(result, p)
    }
    return result, total, rows.Err()
}

// Update y Delete siguen el mismo patron...
```

**Notas:**
- `nullString()` viene de `null_helpers.go` — convierte `""` a `nil` para filtros opcionales
- `count(*) OVER()::INT` da el total en la misma query (ver ADR-008)
- `GetByID` y `List` usan `r.pool` directo (solo lectura, no transaccion)
- `Create` y `Update` reciben `DBTX` para soportar transacciones

---

### Paso 4 — Capa de aplicacion (CQRS)

#### DTOs

```go
// internal/application/proveedor/dto.go
package proveedor

import domain "github.com/kafeiih/vogel/examples/boilerplate/internal/domain/proveedor"

type CreateRequest struct {
    Rut    string `json:"rut" validate:"required"`
    Nombre string `json:"nombre" validate:"required"`
}

type UpdateRequest struct {
    Nombre string `json:"nombre" validate:"required"`
}

type Response struct {
    ID        string `json:"id"`
    Rut       string `json:"rut"`
    Nombre    string `json:"nombre"`
    Activo    bool   `json:"activo"`
    CreatedAt string `json:"created_at"`
    UpdatedAt string `json:"updated_at"`
}

func toResponse(p *domain.Proveedor) Response {
    return Response{
        ID:        p.ID.String(),
        Rut:       p.Rut,
        Nombre:    p.Nombre,
        Activo:    p.Activo,
        CreatedAt: p.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
        UpdatedAt: p.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
    }
}

func toResponseList(ps []domain.Proveedor) []Response {
    result := make([]Response, len(ps))
    for i := range ps {
        result[i] = toResponse(&ps[i])
    }
    return result
}
```

#### Queries (lectura)

```go
// internal/application/proveedor/queries.go
package proveedor

import (
    "context"

    "github.com/google/uuid"

    domain "github.com/kafeiih/vogel/examples/boilerplate/internal/domain/proveedor"
)

type Queries struct {
    repo domain.Repository
}

func NewQueries(repo domain.Repository) *Queries {
    return &Queries{repo: repo}
}

func (q *Queries) GetByID(ctx context.Context, id uuid.UUID) (*Response, error) {
    p, err := q.repo.GetByID(ctx, id)
    if err != nil {
        return nil, err
    }
    r := toResponse(p)
    return &r, nil
}

func (q *Queries) List(ctx context.Context, f domain.Filters) ([]Response, int, error) {
    ps, total, err := q.repo.List(ctx, f)
    if err != nil {
        return nil, 0, err
    }
    return toResponseList(ps), total, nil
}
```

#### Commands (escritura con audit)

```go
// internal/application/proveedor/commands.go
package proveedor

import (
    "context"
    "fmt"
    "time"

    "github.com/google/uuid"
    "github.com/jackc/pgx/v5/pgxpool"

    audit "github.com/kafeiih/vogel/audit"
    domain "github.com/kafeiih/vogel/examples/boilerplate/internal/domain/proveedor"
    "github.com/kafeiih/vogel/examples/boilerplate/internal/infrastructure/repository"
)

type Commands struct {
    repo     domain.Repository
    recorder *audit.Recorder
    pool     *pgxpool.Pool
}

func NewCommands(repo domain.Repository, recorder *audit.Recorder, pool *pgxpool.Pool) *Commands {
    return &Commands{repo: repo, recorder: recorder, pool: pool}
}

func (c *Commands) Create(ctx context.Context, req CreateRequest) (*Response, error) {
    p := &domain.Proveedor{
        ID:        uuid.New(),
        Rut:       req.Rut,
        Nombre:    req.Nombre,
        Activo:    true,
        CreatedAt: time.Now().UTC(),
        UpdatedAt: time.Now().UTC(),
    }

    txManager := repository.NewPgxTxManager(c.pool)
    err := txManager.WithTx(ctx, func(txCtx context.Context) error {
        db := repository.DBFromContext(txCtx, c.pool)

        if err := c.repo.Create(txCtx, db, p); err != nil {
            return fmt.Errorf("create proveedor: %w", err)
        }

        return c.recorder.Record(txCtx, db, audit.SourceHTTP, audit.ActionCreate, "proveedor", p.ID.String(),
            audit.WithSubject(p),
            audit.WithObjectRepr(fmt.Sprintf("Proveedor %s - %s", p.Rut, p.Nombre)),
        )
    })
    if err != nil {
        return nil, err
    }

    r := toResponse(p)
    return &r, nil
}
```

**Patron clave:** La mutacion y el audit ocurren en la MISMA transaccion via `WithTx`. Si el audit falla, la mutacion se revierte.

---

### Paso 5 — Contrato y handler HTTP

`api/openapi.yaml` es la fuente de verdad: primero se describe la operación
ahí y después se implementa. Agregar los paths con un `operationId` y un tag,
y reutilizar los parámetros y respuestas de `components` (los envelopes de
error de vogel ya están descritos):

```yaml
paths:
  /proveedor:
    get:
      operationId: listProveedores
      tags: [proveedor]
      summary: List proveedores
      parameters:
        - name: nombre
          in: query
          schema: {type: string, x-go-type: string}
        - $ref: "#/components/parameters/Limit"
        - $ref: "#/components/parameters/Offset"
      responses:
        "200":
          description: A page of proveedores.
          content:
            application/json:
              schema: {$ref: "#/components/schemas/ProveedorListResponse"}
        "400":
          $ref: "#/components/responses/ValidationError"
    post:
      operationId: createProveedor
      tags: [proveedor]
      summary: Create proveedor
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: "#/components/schemas/CreateProveedorRequest"}
      responses:
        "201":
          description: The created proveedor.
          content:
            application/json:
              schema: {$ref: "#/components/schemas/ProveedorResponse"}
        "400":
          $ref: "#/components/responses/ValidationError"
```

Los parámetros llevan `x-go-type: string`: el código generado no los parsea,
el handler los valida con `request.Validator` y así los mensajes de error
siguen siendo los de vogel. Después:

```bash
make generate   # regenera internal/interfaces/http/apigen
make lint-api   # valida el contrato
```

El handler implementa los métodos que `apigen.StrictServerInterface` exige
(agregarlo a `handler.API`; si falta una operación, no compila). Los éxitos
devuelven el tipo generado; los errores, un error de `vogel/httpx/openapi` que
lleva su respuesta de vogel:

```go
// internal/interfaces/http/handler/proveedor_handler.go
package handler

import (
    "context"
    "log/slog"
    "net/http"

    "github.com/kafeiih/vogel/httpx/openapi"
    "github.com/kafeiih/vogel/httpx/response"
    "github.com/kafeiih/vogel/request"

    app "github.com/kafeiih/vogel/examples/boilerplate/internal/application/proveedor"
    domain "github.com/kafeiih/vogel/examples/boilerplate/internal/domain/proveedor"
    "github.com/kafeiih/vogel/examples/boilerplate/internal/interfaces/http/apigen"
)

type ProveedorHandler struct {
    queries  *app.Queries
    commands *app.Commands
    logger   *slog.Logger
}

func NewProveedorHandler(q *app.Queries, c *app.Commands, l *slog.Logger) *ProveedorHandler {
    return &ProveedorHandler{queries: q, commands: c, logger: l}
}

func (h *ProveedorHandler) ListProveedores(ctx context.Context, _ apigen.ListProveedoresRequestObject) (apigen.ListProveedoresResponseObject, error) {
    r := openapi.Request(ctx)
    v := request.NewValidator()
    limit := v.MaxInt("limit", v.IntQuery(r, "limit", 20), 100)
    offset := v.IntQuery(r, "offset", 0)
    if v.HasErrors() {
        return nil, openapi.Invalid(v)
    }

    filters := domain.Filters{Nombre: r.URL.Query().Get("nombre"), Limit: limit, Offset: offset}
    entries, total, err := h.queries.List(ctx, filters)
    if err != nil {
        h.logger.Error("listing proveedores", "error", err)
        return nil, openapi.Respond(http.StatusInternalServerError, response.CodeInternalError, "Failed to list proveedores")
    }

    return apigen.ListProveedores200JSONResponse{
        Success: true, Message: "OK", Data: entries,
        Meta: apigen.ListMeta{Total: total, Limit: limit, Offset: offset},
    }, nil
}

func (h *ProveedorHandler) CreateProveedor(ctx context.Context, req apigen.CreateProveedorRequestObject) (apigen.CreateProveedorResponseObject, error) {
    // req.Body ya pasó por request.JSON (openapi.DecodeJSON en la ruta).
    result, err := h.commands.Create(ctx, app.CreateRequest{Nombre: req.Body.Nombre})
    if err != nil {
        h.logger.Error("creating proveedor", "error", err)
        return nil, openapi.Respond(http.StatusInternalServerError, response.CodeInternalError, "Failed to create proveedor")
    }
    return apigen.CreateProveedor201JSONResponse{Success: true, Message: "OK", Data: result}, nil
}
```

**Notas:**
- `openapi.Request(ctx)` devuelve el request: los validadores de vogel leen la
  query y los parámetros de chi desde ahí.
- `openapi.Invalid(v)`, `openapi.Respond(...)` y `openapi.Fail(err, write)`
  responden con los envelopes de vogel; cualquier otro error termina en 500.
- Errores de repo se loguean con `slog` pero al cliente solo va un mensaje generico

---

### Paso 6 — Wiring en main.go

```go
// En cmd/api/main.go, agregar:

// Repositorio
proveedorRepo := repository.NewProveedorPostgresRepo(pool)

// Application layer
proveedorQueries := proveedor.NewQueries(proveedorRepo)
proveedorCommands := proveedor.NewCommands(proveedorRepo, auditRecorder, pool)

// Handler
proveedorHandler := handler.NewProveedorHandler(proveedorQueries, proveedorCommands, appLogger.Logger)

// Agregar a Dependencies struct y pasar al router
```

---

### Paso 7 — Rutas en router.go

Las rutas se registran a mano, cada una con sus guards, y llaman al servidor
generado (`ops`). `openapi.RequestInContext` va último; las operaciones con
body JSON llevan `openapi.DecodeJSON` para que los 400/413/415 sean los de
vogel:

```go
// En internal/interfaces/http/router.go, dentro del grupo autenticado:

r.Route("/proveedor", func(r chi.Router) {
    r.With(can("proveedor", "list"), openapi.RequestInContext).
        Get("/", func(w http.ResponseWriter, r *http.Request) {
            ops.ListProveedores(w, r, apigen.ListProveedoresParams{})
        })
    r.With(can("proveedor", "create"), openapi.DecodeJSON[apigen.CreateProveedorRequest](nil), openapi.RequestInContext).
        Post("/", ops.CreateProveedor)
})
```

`TestSpecAndRouterMatch` falla si una ruta queda fuera del contrato o una
operación del contrato no está en el router.

---

### Paso 8 — Tests

Crear tests para cada capa:

| Capa | Tipo | Tecnica |
|------|------|---------|
| Handler | Unit | `httptest` + stub repo (como `audit_handler_test.go`) |
| Application | Unit | Mock repo, verificar campos del DTO |
| Repository | Integration | testcontainers + `//go:build integration` |

---

## Checklist para nuevo modulo

- [ ] Entidad + interfaz de repositorio en `domain/`
- [ ] Migracion SQL aplicada
- [ ] Repositorio pgx en `infrastructure/repository/`
- [ ] DTOs con mappers en `application/entity/dto.go`
- [ ] Queries (lectura) en `application/entity/queries.go`
- [ ] Commands (escritura + audit) en `application/entity/commands.go`
- [ ] Operaciones en `api/openapi.yaml` y `make generate` ejecutado
- [ ] Handler que implementa las operaciones en `interfaces/http/handler/`, agregado a `handler.API`
- [ ] Wiring en `main.go`
- [ ] Rutas con middleware de authz en `router.go`
- [ ] Tests de handler (unit)
- [ ] Tests de repository (integration, `//go:build integration`)
- [ ] `make lint-api` sin errores

---

## Patrones recurrentes

### Auditable interface

Si tu entidad implementa `Auditable`, el recorder genera snapshots automaticamente:

```go
func (p *Proveedor) AuditRepr() string {
    return fmt.Sprintf("Proveedor %s - %s", p.Rut, p.Nombre)
}

func (p *Proveedor) AuditSnapshot() map[string]any {
    return map[string]any{"id": p.ID.String(), "rut": p.Rut, "nombre": p.Nombre}
}
```

### Update con diff

```go
recorder.Record(ctx, db, audit.SourceHTTP, audit.ActionUpdate, "proveedor", p.ID.String(),
    audit.WithChange(before, after),
)
// WithChange calcula automaticamente ChangedFields, BeforeSnapshot, AfterSnapshot
```

### Batch operations

```go
recorder.Record(ctx, db, audit.SourceHTTP, audit.ActionExecute, "prorateo", docID,
    audit.WithAggregate("documento", docID),
    audit.WithAffectedResources(affected),
    audit.WithInputPayload(map[string]any{"items": items}),
)
```

### Error tracking en audit

```go
if err != nil {
    recorder.Record(ctx, db, audit.SourceHTTP, audit.ActionCreate, "proveedor", "",
        audit.WithError("DUPLICATE_RUT", err.Error()),
    )
}
```

---

## Convenciones

### 1. Idioma

Todos los mensajes al cliente van en **español**. Esto incluye:
- Mensajes de error HTTP (`"Documento no encontrado"`)
- Mensajes de validacion (`"rut es requerido"`)
- Errores de negocio (`"no se puede eliminar: tiene prorateos con devengo asociado"`)

Descripciones del contrato OpenAPI, comentarios de codigo, y nombres de variables van en **ingles**.

### 2. Import aliases

| Situacion | Alias | Ejemplo |
|-----------|-------|---------|
| Dominio local (mismo modulo) | `domain` | `domain "...domain/proveedor"` en `application/proveedor/` |
| Dominio ajeno | `domain` + nombre | `audit "github.com/kafeiih/vogel/audit"` en `application/proveedor/` |
| Application local (handler del mismo modulo) | `app` | `app "...application/proveedor"` en `handler/proveedor_handler.go` |
| Application ajena | `app` + nombre | `appDevengo "...application/devengo"` en otro handler |
| Infra authz (evitar colision con domain authz) | `infraAuthz` | `infraAuthz "...infrastructure/authz"` en `main.go` |
| Middleware (evitar colision con chi middleware) | `customMiddleware` | `customMiddleware "...middleware"` en `router.go` |

Regla: si el package name ya es unico en contexto, no uses alias.

### 3. Tiempo

Siempre **UTC** y **truncado a microsegundos** (compatibilidad con PostgreSQL `TIMESTAMPTZ`):

```go
CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
```

PostgreSQL almacena con precision de microsegundos (6 decimales). Go genera nanosegundos (9 decimales). Si no truncas, al leer de la DB perdés los 3 digitos extra y las comparaciones fallan.

Esta regla aplica en la capa de aplicacion (commands) donde se construyen las entidades.

### 4. Generacion de UUID

Los UUIDs se generan en el **command** (application layer), nunca en el handler ni en el dominio:

```go
// application/proveedor/commands.go
func (c *Commands) Create(ctx context.Context, req CreateRequest) (*Response, error) {
    p := &domain.Proveedor{
        ID: uuid.New(),  // <-- aca
        // ...
    }
}
```

El dominio define la estructura. La aplicacion orquesta la creacion. El handler solo recibe y despacha.

Excepcion: el `audit.Recorder` genera su UUID internamente porque es infraestructura cross-cutting.

### 5. Codigos de error HTTP (catalogo cerrado)

Los codigos de error estan definidos como constantes en `response/error.go`:

```go
const (
    CodeNotFound        = "NOT_FOUND"
    CodeInternalError   = "INTERNAL_ERROR"
    CodeInvalidJSON     = "INVALID_JSON"
    CodeUnknownField    = "UNKNOWN_FIELD"
    CodeBodyTooLarge    = "BODY_TOO_LARGE"
    CodeValidationError = "VALIDATION_ERROR"
    CodeConflict        = "CONFLICT"
    CodeUnauthorized    = "UNAUTHORIZED"
    CodeForbidden       = "FORBIDDEN"
    CodeRateLimited     = "RATE_LIMITED"
    CodeAuthzError      = "AUTHZ_ERROR"
)
```

**Los modulos de negocio NO inventan codigos nuevos.** Los errores de negocio se distinguen por el campo `message`, no por el `code`. Usar siempre `response.CodeX` — un typo como `"INTERNEL_ERROR"` compila pero la constante no.

### 6. Errores de dominio (validators.go)

Cada modulo define sus errores de negocio como `var` en `validators.go`:

```go
// application/proveedor/validators.go
package proveedor

import "errors"

var (
    ErrNotFound     = errors.New("proveedor no encontrado")
    ErrDuplicateRut = errors.New("ya existe un proveedor con este RUT")
)
```

Reglas:
- Prefijo `Err` + descripcion en PascalCase
- Mensajes en español (son lo que ve el cliente)
- Un archivo `validators.go` por modulo. Aca van los errores Y las funciones de validacion de negocio

### 7. Validacion (2 capas)

**Capa 1 — Handler (request validation):** "¿El request esta bien formado?"

```go
// Parsing + errores de JSON (automatico)
if err := request.JSON(w, r, &req); err != nil {
    return
}

// Struct tags (required, min, max, email, etc.)
if err := request.Validate.Struct(req); err != nil {
    response.Error(w, r, http.StatusBadRequest, response.CodeValidationError, err.Error())
    return
}
```

**Capa 2 — Application/validators.go (business validation):** "¿Los datos tienen sentido para el negocio?"

Reglas que necesitan contexto: consultar la DB, comparar montos, verificar unicidad. Retornan errores especificos definidos en `validators.go`.

**El handler distingue los errores con `errors.Is()`:**

```go
result, err := h.commands.Create(r.Context(), req)
if err != nil {
    if errors.Is(err, proveedor.ErrNotFound) {
        response.Error(w, r, http.StatusNotFound, response.CodeNotFound, err.Error())
        return
    }
    if errors.Is(err, proveedor.ErrDuplicateRut) {
        response.Error(w, r, http.StatusConflict, response.CodeConflict, err.Error())
        return
    }
    // Infraestructura — loguear + generico
    h.logger.Error("creating proveedor", "error", err)
    response.Error(w, r, http.StatusInternalServerError, response.CodeInternalError, "No se pudo crear el proveedor")
    return
}
```

Para modulos con muchos errores de negocio, usa un helper `isValidationErr`:

```go
func isProveedorValidationErr(err error) bool {
    return errors.Is(err, proveedor.ErrDuplicateRut) ||
        errors.Is(err, proveedor.ErrInactivo)
}
```

### 8. Logging

**Todos los errores se loguean** para saber donde se cayo:

| Situacion | Nivel | Ejemplo |
|-----------|-------|---------|
| Error de infra (DB, red, timeout) | `Error` | `h.logger.Error("creating proveedor", "error", err)` |
| Error de negocio (400, 404, 409) | `Warn` | `h.logger.Warn("proveedor duplicado", "rut", req.Rut)` |
| Request rechazado por validacion | `Info` | no loguear (el rate es alto y es ruido) |
| Auth/authz fallido | `Warn` | ya logueado por el middleware |

Siempre incluir campos estructurados relevantes: `"id"`, `"error"`, `"user_id"`.

### 9. Delete: soft vs hard

No hay convencion global. Depende de las reglas de negocio de cada entidad:

- **Soft delete** (`UPDATE SET activo = false`): para entidades que necesitan trazabilidad historica o que otros registros referencian. Ejemplo: proveedores, documentos.
- **Hard delete** (`DELETE FROM`): para sub-recursos sin vida propia. Ejemplo: prorateos, items de un devengo.

La interfaz de `Repository` define `Delete(ctx, db DBTX, id uuid.UUID) error` — la implementacion decide si es soft o hard.

### 10. Sub-recursos

Si un sub-recurso **no tiene sentido sin su padre**, va en el **mismo handler** del padre:

```go
// handler/documento_handler.go
func (h *DocumentoHandler) AgregarProrateos(w http.ResponseWriter, r *http.Request) { ... }
func (h *DocumentoHandler) RemoverProrateo(w http.ResponseWriter, r *http.Request) { ... }
func (h *DocumentoHandler) PatchProrateo(w http.ResponseWriter, r *http.Request) { ... }
```

Rutas anidadas en `router.go`:

```go
r.Route("/documento", func(r chi.Router) {
    r.Get("/", handler.List)
    r.Post("/", handler.Create)

    r.Route("/{id}", func(r chi.Router) {
        r.Get("/", handler.GetByID)
        r.Put("/", handler.Update)
        r.Delete("/", handler.Delete)

        // Sub-recurso: prorateo
        r.Post("/prorateo", handler.AgregarProrateos)
        r.Delete("/prorateo", handler.RemoverProrateos)       // batch
        r.Patch("/prorateo/{prorateoId}", handler.PatchProrateo)
        r.Delete("/prorateo/{prorateoId}", handler.RemoverProrateo)
    })
})
```

Los commands del padre orquestan las operaciones sobre sub-recursos (create prorateo + validar que la suma cuadre + audit). El sub-recurso NO tiene su propio command/query package.

### 11. Modulos de solo lectura

Algunos modulos solo exponen Queries (sin Commands):

```go
// application/locale/queries.go — solo lectura
type Queries struct {
    repo domain.Repository
}

func NewQueries(repo domain.Repository) *Queries { ... }
func (q *Queries) List(ctx context.Context, f domain.Filters) ([]Response, int, error) { ... }
```

El handler solo recibe `*Queries`:

```go
func NewLocaleHandler(q *locale.Queries, l *slog.Logger) *LocaleHandler { ... }
```

Esto no significa que nunca tendra Commands — cuando las reglas de negocio lo requieran, se agrega `commands.go` al package sin romper nada.

### 12. Nombres de archivos

| Capa | Patron | Ejemplo |
|------|--------|---------|
| Dominio | `{entidad}.go` | `proveedor.go`, `documento.go` |
| Repositorio | `{entidad}_postgres.go` | `proveedor_postgres.go` |
| Application | `commands.go`, `queries.go`, `dto.go`, `validators.go` | siempre estos nombres |
| Handler | `{entidad}_handler.go` | `proveedor_handler.go` |
| Test | `{archivo}_test.go` | `proveedor_handler_test.go` |
| Migracion | `{NNN}_{descripcion}.sql` | `001_create_proveedores.sql` |
