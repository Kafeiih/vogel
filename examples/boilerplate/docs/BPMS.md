# Base BPMS

El componente `bpms` arma las tres piezas de la base BPMS de `vogel` y, sobre
ellas, un dominio de procesos listo para replicar: definiciones JSON
versionadas con formularios, bandeja por perfil y el proceso `consulta` ya
publicado.

Las tres piezas de `vogel`:

| Pieza | Paquete de `vogel` | Qué hace |
|---|---|---|
| Motor de workflow | `vogel/workflow` + `vogel/workflow/postgres` | Abre casos y los mueve por una `Definition` (nodos, transiciones, guards). Nunca ejecuta un efecto. |
| Acciones en segundo plano | `vogel/action` | `action.Registry` es el `EffectSink` del motor: encola un job de River por cada efecto, en la misma transacción que el `Open`/`Move`. `action.Worker` los corre en el worker, con reintentos. |
| Documentos PDF | `vogel/document` + `document/postgres` + `document/gotenberg` + `document/docflow` | Plantilla HTML + datos → PDF (Gotenberg) → storage, con su fila de metadatos (`document_record`). |

`internal/infrastructure/bpms` las construye con `bpms.New`: la API (que
encola) y el worker (que ejecuta) llaman a la misma función, así que los dos
registran exactamente los mismos efectos. La API no corre ningún efecto:
solo los encola; el worker los ejecuta con `action.AddWorker`.

`migrate up` aplica tres conjuntos de migraciones más, cada uno con su tabla
de versión: `vogel/workflow/migrations` (`workflow_db_version`),
`vogel/document/migrations` (`document_db_version`) y las del dominio de
procesos, `internal/infrastructure/bpms/migrations` (`bpms_db_version`).

## El dominio de procesos

| Capa | Carpeta | Qué contiene |
|---|---|---|
| Dominio | `internal/domain/process` | `ProcessSpec` (el JSON que escribe un autor), `CompileProcess` (valida el grafo, cada campo y cada regla), tipos de campo (`FieldType`, `FieldRegistry`), formularios con reglas `visibleWhen` en CEL, el puerto `ProfileResolver` y los errores del dominio. No conoce la base de datos ni HTTP. |
| Aplicación | `internal/application/process` | `Catalog` (publica y carga versiones, las registra en el motor) y `Service` (iniciar, bandeja, tomar, enviar, ver). Define sus puertos: `VersionRepository`, `ValueRepository`, `CaseReader`. |
| Infraestructura | `internal/infrastructure/bpms` | Repositorios Postgres (`process_version`, `case_value`), migraciones, `MemoryDirectory` (implementa `ProfileResolver`), los procesos semilla en `processes/*.json` y `NewDomain`, que arma todo. |
| Interfaces | `internal/interfaces/http/handler/process_handler.go` | Las rutas HTTP. El actor es el usuario autenticado (`auth.FromContext`). |

### Rutas

Todas exigen autenticación y pasan por Cerbos (`cerbos/policies/resource_bpms_*.yaml`):

| Ruta | Recurso / acción | Roles |
|---|---|---|
| `POST /processes` | `bpms:process` / `publish` | `admin` |
| `GET /processes/{name}` | `bpms:process` / `read` | `admin`, `operator` |
| `POST /processes/{name}/cases` | `bpms:process` / `start` | `admin`, `operator` |
| `GET /inbox` | `bpms:case` / `inbox` | `admin`, `operator` |
| `GET /cases/{id}` | `bpms:case` / `view` | `admin`, `operator` |
| `POST /cases/{id}/claim` | `bpms:case` / `claim` | `admin`, `operator` |
| `POST /cases/{id}/submit` | `bpms:case` / `submit` | `admin`, `operator` |

Cerbos decide por rol. El servicio decide por caso: solo quien tiene hoy el
perfil del nodo actual puede tomar la tarea, solo quien la tomó puede
enviarla (`Engine.Move` no compara el actor con el asignado) y solo ve un caso
quien actuó en él, lo tiene tomado o tiene el perfil de su nodo actual.

### Publicar y versionar

Un proceso es un JSON con nodos, transiciones y un formulario por nodo (ver
`internal/infrastructure/bpms/processes/consulta.json`). Publicar compila todo
y guarda la siguiente versión, inmutable; un caso sigue siempre con la versión
con la que se abrió. Si dos publicaciones del mismo proceso llegan a la vez,
una responde `409 CONFLICT` y se puede reintentar. El arranque recarga todas las versiones guardadas y una
que ya no compila hace fallar el arranque, en vez de dejar casos varados.

Al arrancar, cada `processes/*.json` cuyo nombre todavía no tiene versión se
publica como versión 1; si ya tiene, no se toca. Para cambiar un proceso
publicado, publique una versión nueva con `POST /processes`. Varias réplicas
pueden arrancar a la vez: solo una inserta la semilla.

### Agregar un tipo de campo

Implemente `process.FieldType` (`Key`, `ValidateConfig`, `Validate`,
`CELType`) en `internal/domain/process/field.go` y agréguelo a
`DefaultFieldTypes`. Ni el motor ni las versiones publicadas cambian.

### Quién tiene un perfil

Un nodo nombra un perfil (`{"position": "encargado", "unit": "atencion"}`),
nunca una persona. `ProfileResolver` responde quién lo tiene en el momento de
preguntar, así que un cambio de titular o una subrogancia se ve en la bandeja
al instante, sin reasignar nada. `MemoryDirectory` lo implementa con lo que
dice `BPMS_PROFILE_HOLDERS`; en producción reemplácelo por un cliente del
directorio de la organización (SAFO) que implemente el mismo puerto y páselo a
`bpms.NewDomain` en `cmd/api/main.go`.

## Configuración

| Variable | Default | Efecto |
|---|---|---|
| `GOTENBERG_URL` | — (obligatoria) | URL base de Gotenberg, p. ej. `http://localhost:3001` (local) o `http://gotenberg:3000` (compose de despliegue). |
| `GOTENBERG_TIMEOUT` | `30s` | Tope de una conversión a PDF. Mayor que cero y como máximo `45s`: River cancela el trabajo al minuto y la subida y el insert necesitan su margen. |
| `BPMS_DOCUMENT_STORAGE` | `documents` | Backend de `STORAGE_CONFIGS` donde se guardan los PDF. Si no está en `STORAGE_CONFIGS`, el arranque falla. |
| `BPMS_PROFILE_HOLDERS` | vacío | Quién tiene cada perfil para `MemoryDirectory`, como `posicion@unidad=user-id` separados por coma (el user id es el `sub` de Zitadel), p. ej. `encargado@atencion=312345678901234567`. Repita la entrada para más de una persona. Vacío: nadie tiene perfiles y las bandejas quedan vacías. |

`make dev-infra` levanta Gotenberg en `127.0.0.1:3001` (el 3000 queda libre para el frontend). Chromium tiene prohibido conectarse a cualquier IP, privada o pública, y no ejecuta JavaScript: una plantilla debe ser autocontenida (CSS en línea, imágenes como `data:` URI), y así un dato que llegue a la plantilla no puede hacer que Gotenberg pida una URL (SSRF). No trae un S3 local:
apunte `STORAGE_DOCUMENTS_*` a un endpoint S3-compatible.

## Procesos definidos en código, con efectos

Las definiciones JSON no declaran efectos ni guards. Un proceso que los
necesite (generar un PDF, notificar) se define en Go, en `service`, en
`internal/infrastructure/bpms/process.go`:

```go
var service = Process{
	Effects:     func(r *action.Registry, docs *document.Service, db pgxtx.DBTX) error { ... },
	Guards:      func(e *workflow.Engine, docs *document.Service) error { ... },
	Definitions: func() []workflow.Definition { ... },
}
```

`bpms.New` los registra en ese orden: efectos, guards y después las
`Definition`. `Engine.Register` rechaza una `Definition` que nombra un efecto
que el registro no conoce (`workflow.ErrUnknownEffect`), así que un nombre mal
escrito hace fallar el arranque, no el primer caso. En un despliegue
escalonado, un worker que todavía corre la versión anterior puede tomar el
trabajo de un efecto nuevo: no lo cancela, lo reintenta con backoff hasta que
lo toma un worker nuevo.

### 1. Declarar una `Definition` con efectos

Los efectos se declaran por nombre: `Node.Effects` corren al entrar al nodo,
`Transition.Effects` al tomar la transición.

```go
Definitions: func() []workflow.Definition {
	return []workflow.Definition{{
		Name:    "solicitud",
		Version: 1,
		Nodes: []workflow.Node{
			{ID: "borrador", Start: true, Eligible: workflow.ByPosition("ciudadano")},
			{ID: "revision", Eligible: workflow.ByPosition("funcionario"),
				Effects: []string{"certificado.generar"}},
			{ID: "aprobada", Terminal: true, Effects: []string{"notificar.ciudadano"}},
		},
		Transitions: []workflow.Transition{
			{From: "borrador", To: "revision", Action: "enviar"},
			{From: "revision", To: "aprobada", Action: "aprobar", Guard: "certificado.existe"},
		},
	}}
},
```

### 2. Registrar un guard

Un efecto corre en segundo plano y puede estar pendiente o fallando cuando
llega la siguiente acción humana. Lo que un paso posterior necesita se
modela como guard, nunca se supone. `docflow.Exists` pasa solo cuando el caso
ya tiene un documento de esa plantilla:

```go
Guards: func(e *workflow.Engine, docs *document.Service) error {
	return e.RegisterGuard("certificado.existe", docflow.Exists(docs, "certificado"))
},
```

### 3. Registrar un handler

`docflow.Generate` genera un PDF por corrida, idempotente ante reintentos
(la clave de origen es `<caso>/<seq>/<efecto>`). Cancela el trabajo sin
reintentar cuando reintentar no sirve: entrada inválida, plantilla que falla,
PDF vacío o un documento que Gotenberg rechaza (400, 409 o 422, o un PDF más
grande que el límite). Un Gotenberg caído, lento, saturado o mal configurado
(un 401, 403, 404 o 413 de un proxy, un WAF o una URL equivocada) se
reintenta:

```go
Effects: func(r *action.Registry, docs *document.Service, db pgxtx.DBTX) error {
	return r.Register("certificado.generar", docflow.Generate(docs, db,
		func(ctx context.Context, run action.Run) (document.GenerateInput, error) {
			return document.GenerateInput{
				Template: document.Template{Name: "certificado", Version: 1, Body: certificadoHTML},
				Data:     map[string]any{"Expediente": run.ExternalID},
			}, nil
		}))
},
```

`action.NotifyHandler` adapta un `notification.Notifier` (componente
`notification`): usted decide destinatarios y mensaje. Nada se registra solo,
porque `notification` es opcional y `Process.Effects` no recibe un notifier.
Para usarlo, haga tres cambios en su servicio: agregue un campo
`Notifier notification.Notifier` a `bpms.Deps`, agregue ese notifier como
parámetro de `Process.Effects` (y pásele `d.Notifier` en `build`), y llénelo
en `cmd/worker/main.go` con el `notifier` que ese archivo ya construye. Luego
regístrelo:

```go
r.Register("notificar.ciudadano", action.NotifyHandler(notifier,
	func(ctx context.Context, run action.Run) ([]*notification.Message, error) {
		// buscar el destinatario por run.Domain / run.ExternalID
	}))
```

La API registra el mismo nombre con el notifier en `nil`: solo lo encola, el
handler corre únicamente en el worker. Con más de un mensaje, el trabajo del
efecto encola un trabajo por mensaje y cada uno vuelve a llamar a su función
y envía solo el suyo: un envío fallido no reenvía los que ya salieron, pero la
función debe devolver los mismos mensajes en el mismo orden para la misma
corrida. Un handler debe tolerar correr más de una vez (la entrega es
*at-least-once*); devuelva `river.JobCancel(err)` para un error que ningún
reintento puede corregir.

## La regla de la transacción

`Engine.Open` y `Engine.Move` reciben la base de datos donde escriben, y los
efectos se encolan en esa misma transacción: un rollback no encola nada y un
commit hace visibles el cambio de estado y sus jobs juntos. Por eso hay que
pasarles el `pgx.Tx`, nunca el pool: cuando la `Definition` declara efectos,
`EffectSink.CheckDB` rechaza el pool (error que envuelve `pgxtx.ErrNoTx`)
antes de escribir nada.

```go
txm := pgxtx.NewPgxTxManager(pool)
err := txm.WithTx(ctx, func(ctx context.Context) error {
	tx, err := pgxtx.RequireTx(ctx)
	if err != nil {
		return err
	}
	_, err = base.Engine.Move(ctx, tx, workflow.MoveInput{CaseID: id, Action: "enviar", ActorID: actor})
	return err
})
```

Los PDF quedan en el storage aunque la transacción que registró su fila haga
rollback (el storage no es transaccional), y también si falla el borrado de
limpieza después de un insert fallido. Ni `vogel` ni este boilerplate traen un
barrido de esos objetos huérfanos: si importan, el servicio debe programar uno
que borre los objetos del prefijo `documents/` sin fila en `document_record`.
