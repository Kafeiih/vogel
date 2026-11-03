# Guía de `workflow`

`workflow` es un motor genérico de workflow/BPM: `Definition` (nodos, transiciones,
guardas), `Case`, `Event`, el motor de transiciones y el puerto `Repository`.
Depende sólo de `uuid`, `pgxtx` y `reqctx`.

Es un paquete [experimental](STABILITY.md): su API todavía puede cambiar.

| Paquete | Para qué |
|---|---|
| `workflow` | El motor y el puerto `Repository`. |
| `workflow/postgres` | `Repository` sobre PostgreSQL. |
| `workflow/migrations` | Las migraciones embebidas. |
| `workflow/workflowhttp` | Traduce los errores del motor a HTTP. |
| `workflow/calendar` | Plazos en días hábiles. |

Para ver cómo se combina con los demás paquetes, consultar [`WIRING.md`](WIRING.md).

## Alcance

La restricción que define al motor es que **no guarda dato de dominio alguno**: un
`Case` lleva sólo una referencia (`Domain`, `ExternalID`), nunca el objeto de
negocio. No hay form builder, tabla de datos clave/valor ni blob JSON de campos.

Una operación entra al motor sólo si debe mantenerse consistente con los tokens de
un caso y con su traza encadenada. Quedan en el consumidor la autorización,
`audit_log`, la resolución de personas, los datos de dominio y los trabajos
periódicos.

Toda operación nueva que mueva tokens debe sumarse al random walk de
`workflow/random_walk_test.go`.

## Modelo y `Definition`

### Comentarios

Cada `Transition` declara una `CommentPolicy` (`none`, `optional` o `required`)
para el comentario u observación de ese paso. `Engine.Move` la valida contra
`MoveInput.Comment` (recortado) antes de mutar el caso y lo guarda como
`Event.Comment` en el `EventMoved` correspondiente. Es historial de proceso, nunca
dato de dominio, y el `EventClosed` automático nunca lo repite.

### Nodos de decisión

Un `Node` puede ser un nodo de decisión (`NodeKind` `"decision"`, compuerta
exclusiva al estilo BPMN). Sus transiciones salientes son rutas evaluadas en el
orden declarado: la primera cuya `Guard` calza gana, y exactamente una ruta sin
`Guard`, la ruta por defecto, debe ir declarada al final.

`Definition.Validate` lo exige, junto con que el nodo no tenga `Start`, `Terminal`,
`Eligible` ni `Deadline` y que ningún ciclo esté formado sólo por nodos de decisión.

Un `Engine.Move` que aterriza en un nodo de decisión sigue enrutando
automáticamente, dentro de la misma llamada y transacción, hasta descansar en un
nodo de tarea o terminal. Un caso nunca se persiste en un nodo de decisión. Se
agrega un `EventMoved` por salto, y el comentario sólo va en el salto humano.

### Retornos

Una `Transition` puede marcarse como retorno (`Transition.Return`), para que una
etapa burocrática devuelva un caso a cualquier nodo de tarea anterior que haya
ocupado de verdad. `Engine.Available` la ofrece y `Engine.Move` la acepta sólo si el
caso ocupó ese destino alguna vez según su propio historial de eventos, nunca por
alcanzabilidad en el grafo. Así, un caso que saltó una etapa nunca ve ofrecido
volver a ella.

`Definition.Validate` rechaza un retorno que apunte a un nodo terminal o a un nodo
de decisión, o que salga de un nodo de decisión. Un caso nunca descansa en un nodo
de decisión, así que un retorno hacia uno reactivaría el enrutamiento automático y
podría empujar el caso hacia adelante en vez de hacia atrás.

### Efectos

`Node.Effects` y `Transition.Effects` declaran efectos por nombre que el motor nunca
ejecuta. `Open` y `Move` se los entregan, una vez y después de persistir, al
`EffectSink` configurado con `WithEffectSink` (por ejemplo, para encolarlos en la
misma transacción).

- `Register` rechaza con `ErrUnknownEffect` un efecto que el sink no conoce.
- Cuando la definición declara efectos, `Open` y `Move` llaman a
  `EffectSink.CheckDB(db)` antes de escribir, así que un `db` en el que el sink no
  puede despachar falla sin persistir nada.

### Asignación

La asignación es por posición y unidad organizacional (`Eligibility`), nunca por ID
de persona incrustado en la `Definition`. Resolver quién ocupa hoy esa posición,
incluida la subrogación, queda para un servicio de contexto organizacional del
consumidor.

Entrar a un nodo limpia el asignado y deja el token en el pool del nodo. Para que un
nodo lo asigne al entrar, se declara una regla:

```go
workflow.Node{ID: "review", Eligible: workflow.ByPosition("approver"), AssignRule: "assign_from:draft"}

engine := workflow.New(repo, workflow.Config{Assignees: resolver}, logger)
```

- `Node.AssignRule` nombra la regla. Sólo un nodo de tarea no terminal puede declararla, y
  `Engine.Register` falla con `ErrUnknownAssignRule` si no hay `Config.Assignees`.
- `AssigneeResolver.Assignee` responde con el id de una persona, o vacío para dejar el
  token en el pool. Una respuesta agrega un evento `assigned` con
  `AssignmentDetail{To, Reason: "assign_rule:<regla>"}`. Un error hace fallar toda la
  llamada y no persiste nada.
- Corre donde un token descansa en un nodo de tarea: `Move`, splits, joins, retornos y
  `Expire`. `Engine.Open` no aplica reglas.
- Corre dentro de la transacción y no debe llamar a la red, igual que una guarda. El
  historial se lee sólo para los nodos con regla, e incluye lo que la llamada lleva
  planificado.
- `workflow.LastActorOf(history, node)` devuelve la última persona que salió de un nodo
  en todo el caso, ignorando actores de sistema. Una regla como `assign_from:<nodo>` se
  arma con ella.

El motor no interpreta la regla, así que ningún id de persona entra a la `Definition`.
Las reglas pertenecen al consumidor, que decide por ejemplo si esa persona todavía
ocupa el perfil del nodo o a quién le toca el siguiente turno.

### Tenant

Un caso pertenece a un `Tenant` (vacío para un consumidor de un solo tenant), que
cada evento copia y por el que filtra `InboxFilter.Tenant`.

### Consultas

- `Engine.Participated` lista los casos en los que una persona actuó, aunque ya no
  los tenga asignados (paginado como `Inbox`).
- `Case.LastSeq` es el `seq` del último evento.
- `OpenDetailed` y `MoveDetailed` devuelven además los eventos agregados
  (`MoveResult`).

## Tokens y bloques paralelos

Un caso puede correr ramas en paralelo con el modelo de tokens (semántica BPMN).

### Compuertas

- `NodeParallel` (`"parallel"`, AND) toma todas sus salidas, ninguna con guarda.
- `NodeInclusive` (`"inclusive"`, OR) toma cada ruta cuya guarda pasa, o la ruta por
  defecto sin guarda declarada al final si ninguna pasa.

Ambas abren un bloque que cierra el `NodeJoin` que nombran en `Node.Join`.

`Definition.Validate` exige bloques bien anidados:

- Cada rama llega a su propio join sin pasar por un nodo terminal ni salir del
  bloque.
- Los nodos terminales están sólo en el ámbito raíz.
- Un join tiene una única salida sin guarda.
- Los retornos no cruzan el borde de un bloque.
- No hay ciclos formados sólo por compuertas.

### Tokens y scopes

Cada `Token` es una unidad de trabajo que descansa en un nodo de tarea dentro de un
`Scope`. Un caso que nunca se bifurca tiene un único token cuyo id es el del caso.

Un split consume el token que llega y emite uno por rama. El join, cuando los tokens
que esperan más los cancelados suman `Scope.Expected`, los consume y sigue con un
token en el ámbito padre. Puede encadenar un join externo. Con una
[política](#políticas-de-join), el join puede disparar antes.

### Políticas de join

Un bloque `NodeParallel` puede declarar cómo deciden sus ramas con `Node.JoinPolicy`.
Sin política, el join se comporta como siempre: no hay outcomes, tally ni veredicto.

| Política | Cierra cuando | Veredicto | Al cerrar |
|---|---|---|---|
| `JoinAllApprove` | El primer rechazo, o todas las ramas aprobaron | `rejected` o `approved` | Rechazado: cancela las ramas pendientes y el caso vuelve atrás. Aprobado: el caso avanza |
| `JoinAnyApprove` | La primera aprobación, o todas las ramas rechazaron | `approved` o `rejected` | Aprobado: cancela las pendientes y el paso siguiente recibe el tally. Rechazado: el caso vuelve atrás |
| `JoinCollect` | Todas las ramas respondieron | ninguno | Una persona ve los rechazos y aprobaciones y decide |

**Reglas de `Definition.Validate`:**

- Toda transición que entra a un join con política sale de un nodo de tarea y declara
  `Transition.Outcome` (`OutcomeApproved` u `OutcomeRejected`). Ninguna otra transición
  puede declararlo.
- Cada rama declara tanto una aprobación como un rechazo hacia el join, en una misma tarea
  o en tareas distintas de la rama.
- Bajo `JoinAllApprove` y `JoinAnyApprove`, una transición rechazada tiene
  `Comment: CommentRequired`, y la salida del join llega a un nodo de decisión con una ruta
  guardada que puede volver al split. Un rechazo tampoco puede terminar el caso: para eso,
  usar `JoinCollect` y que una persona lo decida.
- Bajo `JoinCollect`, la salida del join llega a un nodo de tarea.
- Un join nombrado por un `NodeInclusive` no puede tener política, porque ese split
  puede ir directo al join y esa rama no tendría decisión.

`Validate` no interpreta guardas, así que no detecta una guarda propia que no mire el
veredicto. Usar `workflow.JoinRejected` en la ruta que retrocede evita ese error. Fuera de la
llamada que disparó el join, esa guarda responde `false`.

**`JoinAllApprove`.** Ana envía; Juan aprueba; Pedro rechaza con "falta el certificado";
la tarea pendiente de María se cancela; el caso vuelve a Ana:

```go
workflow.Definition{
    Name: "approval", Version: 1,
    Nodes: []workflow.Node{
        {ID: "submit", Start: true},
        {ID: "fork", Kind: workflow.NodeParallel, Join: "vote"},
        {ID: "juan"}, {ID: "pedro"}, {ID: "maria"},
        {ID: "vote", Kind: workflow.NodeJoin, JoinPolicy: workflow.JoinAllApprove},
        {ID: "route", Kind: workflow.NodeDecision},
        {ID: "publish"},
        {ID: "done", Terminal: true},
    },
    Transitions: []workflow.Transition{
        {From: "submit", To: "fork", Action: "send"},
        {From: "fork", To: "juan", Action: "to-juan"},
        {From: "fork", To: "pedro", Action: "to-pedro"},
        {From: "fork", To: "maria", Action: "to-maria"},
        // Lo mismo para pedro y maria.
        {From: "juan", To: "vote", Action: "approve", Outcome: workflow.OutcomeApproved},
        {From: "juan", To: "vote", Action: "reject", Outcome: workflow.OutcomeRejected, Comment: workflow.CommentRequired},
        {From: "vote", To: "route", Action: "tallied"},
        {From: "route", To: "submit", Action: "back", Guard: "rejected"},
        {From: "route", To: "publish", Action: "onward"},
        {From: "publish", To: "done", Action: "finish"},
    },
}

engine.RegisterGuard("rejected", workflow.JoinRejected)
```

**`JoinAnyApprove`.** Es la misma definición con
`JoinPolicy: workflow.JoinAnyApprove`. La primera aprobación cierra la ronda; si todas
rechazan, el caso vuelve atrás.

**`JoinCollect`.** El join llega a la tarea donde una persona decide con transiciones
comunes, y el rechazo no exige comentario:

```go
{ID: "vote", Kind: workflow.NodeJoin, JoinPolicy: workflow.JoinCollect},
{ID: "decide"},

{From: "juan", To: "vote", Action: "approve", Outcome: workflow.OutcomeApproved},
{From: "juan", To: "vote", Action: "reject", Outcome: workflow.OutcomeRejected, Comment: workflow.CommentOptional},
{From: "vote", To: "decide", Action: "tallied"},
{From: "decide", To: "publish", Action: "accept"},
{From: "decide", To: "submit", Action: "redo"},
```

**Leer el resultado.** `Engine.JoinResults(ctx, db, caseID)` devuelve una `JoinResult` por
join disparado con política, la más antigua primero, con el actor, el comentario y la
causa de cada rama. `JoinResult.Count(outcome)` da el tally, por ejemplo "rechazado 2,
aprobado 1" o "aprobado 1, sin respuesta 2". El comentario sólo se guarda en su evento
`moved`, para no duplicar en una tabla de sólo agregado un texto que puede ser personal.

- `JoinDetail` del evento `joined` guarda `Policy`, `Verdict` y `Branches`.
  `BranchResult.Seq` apunta al evento `moved` de la rama, o a su `token_cancelled` si no
  respondió.
- Las guardas que enrutan la llamada que disparó el join ven `Case.JoinResult`, sin actor
  ni comentario. En cualquier otro lugar es `nil`, y el repositorio nunca lo guarda.
- `Config.JoinCheck` corre en todo join disparado. `JoinRecord.Verdict` le permite no
  combinar los datos de una ronda rechazada.

**Volver atrás abre otra ronda.** La vuelta es una ruta común: join, decisión y tarea
anterior. No puede ser un `Return`, porque `Validate` lo rechaza desde un nodo automático
y a través de un bloque. Al reenviar se pasa de nuevo por el split, con scope y tokens
nuevos, así que todos vuelven a votar, también quienes ya aprobaron. Los votos de una
ronda nunca cuentan en otra.

**Cerrar antes de tiempo.** En la misma transacción, cada token activo pendiente del bloque
recibe un `token_cancelled` con `CancelDetail.Reason` igual a `join_policy:<política>`, igual
que los tokens activos o en espera de los scopes todavía abiertos dentro de él, que pasan a
`ScopeCancelled`. Una rama que corría un bloque anidado aparece como una sola fila
`unanswered`, y los votos internos quedan en sus propios eventos. Los eventos van en este orden:
el `waiting` que decide, los `token_cancelled` y el `joined`. Un envío tardío sobre una rama
cancelada falla con `ErrTokenNotFound` (404), y esa rama sale del `Inbox`. De dos decisiones
simultáneas, la que pierde recibe `ErrConflict`, y su reintento recibe `ErrTokenNotFound` si
su rama quedó cancelada.

**Casos límite:**

- Una rama cancelada con `CancelToken` no es un voto: aparece como `unanswered`, y la
  política decide con las que quedan. Cancelar dos de tres ramas deja que un solo voto
  apruebe bajo `JoinAllApprove`.
- Se cuenta por rama, no por persona. El motor no impide que una misma persona responda
  varias ramas: separar a los aprobadores le corresponde al consumidor, con la elegibilidad
  de cada nodo y su autorización. Ver [cómo hacerlo](#separar-a-los-aprobadores).
- Una transición `OnExpire` que entra al join lleva su `Outcome` como cualquier otra, sin
  comentario, y `JoinResults` muestra la causa `expired`. Sin `OnExpire`, el token queda
  pendiente, así que `JoinAllApprove` y `JoinCollect` pueden esperar indefinidamente.
- Un token que espera en un join con política sin un outcome válido hace fallar la llamada
  con `ErrInvalidOutcome`, en vez de contar como aprobación. Ocurre al revertir y volver a
  aplicar la migración `011` con rondas abiertas, al agregar la política a una definición ya
  registrada sin subir su versión, o con un `Repository` propio que no guarde
  `Token.Outcome`. La respuesta sigue en su evento `waiting`.

### Separar a los aprobadores

Si las ramas de un bloque comparten perfil, una sola persona podría responderlas todas y
cumplir `JoinAllApprove` por su cuenta. Para impedirlo, el consumidor revisa la ronda antes
de llamar a `Move`, dentro de la misma transacción:

```go
history, err := engine.History(ctx, tx, caseID)
if err != nil {
    return err
}
for i := range history {
    ev := &history[i]
    if ev.Kind == workflow.EventMoved && ev.ScopeID == token.ScopeID &&
        ev.ToState == joinNode && ev.Actor.ID == actor.ID {
        return ErrAlreadyAnswered // Error del consumidor.
    }
}
```

Cada ronda abre su propio scope, así que quien aprobó en una ronda anterior puede votar en la
siguiente. El motor no decide esto porque no sabe resolver personas: el consumidor define qué
hacer con un subrogante o con quien actúa en nombre de otro.

### Concurrencia

Todo cambio de tokens y scopes se escribe después del compare-and-swap de `revision`
del caso, así que dos ramas del mismo caso nunca confirman a la vez: la segunda
recibe `ErrConflict` y su reintento ve a la otra ya esperando. Un join nunca dispara
dos veces, con el respaldo en la base de un índice único sobre el evento `joined` de
cada scope.

`Config.JoinCheck` corre dentro de la transacción antes de que un join se confirme,
para que el consumidor imponga cómo se combinan los datos de las ramas. Un error
aborta el movimiento con `ErrJoinRejected`. `MoveResult.Joined` informa los joins
disparados.

### Invariantes

- Un caso abierto tiene al menos un token activo.
- Un caso cerrado o cancelado no tiene ninguno activo ni esperando.
- Todo token en espera está en el join de un scope abierto.
- Un scope tiene un token por rama.
- Ningún token descansa en una compuerta.
- Hay a lo sumo un `joined` por scope.
- Reproducir los eventos de un caso reconstruye sus tokens y scopes.

### API por token

`MoveInput.TokenID`, `ClaimInput.TokenID` y `ReleaseInput.TokenID` en cero significan
«el único token activo». Con varios tokens fallan con `ErrTokenRequired`, y con un
token que no está activo, con `ErrTokenNotFound`.

También trabajan por token `Engine.AvailableFor`, `Engine.EligibilityForToken` y
`Engine.Tokens`.

`Case.State`, `Case.AssignedTo` y `Case.DeadlineAt` son proyecciones: quedan vacías
mientras hay varios tokens activos, y el plazo es el más próximo.

`Engine.Inbox` devuelve un `WorkItem` (caso y token) por token activo, e
`InboxFilter.Eligibility` filtra en la base por `(posición, unidad)`.

## Plazos y vencimientos

`Node.Deadline` se aplica por token. `Node.DeadlineRule` nombra una regla de plazo
(por ejemplo, días hábiles) que resuelve `Config.Deadlines`. `Event.DeadlineAt`
registra el plazo con el que un token entró a un nodo de tarea, fijo o resuelto por
una regla, así que reproducir la historia lo reconstruye.

| Operación | Qué hace |
|---|---|
| `Engine.SetDeadline` | Con `Reason`, cambia o quita el plazo de un token con un `deadline_changed`. |
| `Engine.Expire` | Sólo para un actor `ActorSystem`: registra un `expired` una vez por plazo (`ErrNotOverdue` antes de vencer, `ErrAlreadyExpired` en un reintento). Si el nodo declara `Node.OnExpire`, mueve el token por esa transición en la misma transacción. |
| `Engine.Overdue` | Lista los tokens vencidos que aún no se marcaron, el plazo más antiguo primero, para el trabajo periódico del consumidor. |

El trabajo periódico pagina con `OverdueFilter.After` (el `OverdueCursor` del último
ítem), para que un token que no logra expirar no tape a los demás.

### Días hábiles con `workflow/calendar`

`BusinessDays(n, loc, holidays)` vence al terminar el n-ésimo día hábil posterior,
contado siempre en la zona horaria explícita `loc`. Saltea los fines de semana y los
feriados que informa el puerto `Holidays` (`IsHoliday(ctx, Date)`); los datos de
feriados son del consumidor.

`Rules` (un mapa de nombre a `DueFunc`) implementa `workflow.DeadlineResolver` para
`Config.Deadlines`. Corre dentro de la transacción, así que no debe llamar a la red.

## Traza y hash chain

### Contenido de cada evento

- **Esquema:** `SchemaVersion`. El motor escribe `CurrentEventSchema`; `History`
  completa los valores por defecto de versiones viejas y falla con
  `ErrUnsupportedEventSchema` ante una más nueva.
- **Detalle:** `Detail`, un objeto JSON por tipo de evento (`AssignmentDetail` en
  `Claim` y `Release`, `WaitingDetail` y `JoinDetail` en un join con política) que sólo
  lleva referencias, nunca dato de dominio.
- **Actor:** un `Actor` estructurado (`Kind`, `ID`, `ActingAs`, `OnBehalfOf`, `Via`).
  `ActingAs` es la autoridad usada, que el llamador resuelve en el momento. Se pasa
  con `OpenInput.Actor` y `MoveInput.Actor`, con `ActorID` como alias obsoleto de un
  actor humano, y con `ClaimWith` y `ReleaseWith` (con `Reason`) para asignar y
  liberar. Un actor inválido falla con `ErrInvalidActor`.
- **Correlación:** `RequestID` (de `reqctx`) y `TraceID`/`SpanID` desde
  `Config.TraceIDs`, un puerto sin dependencia de OpenTelemetry.
- **Token y scope:** `TokenID` y `ScopeID`, con los tipos `split`, `waiting`,
  `joined`, `token_cancelled` y `cancelled`.

### Formularios y fallas

`MoveInput.Step` registra el envío de un formulario como `EventStepSubmitted`: sólo
su referencia y su resumen, nunca el contenido.

`Engine.RecordFailure` deja constancia de un movimiento rechazado (`EventNoRoute` o
`EventFailed` con su `FailureKind`, que `FailureKindOf` obtiene del error) sin
cambiar el caso ni su `revision`.

### Cadena

Cada evento queda encadenado al anterior de su caso (`Event.PrevDigest` y
`Event.Digest`), con un digest v2 que cubre todas las columnas del evento, incluidos
el token, el scope y el plazo de entrada. `Engine.VerifyChain` devuelve
`ErrChainBroken` con el primer `seq` cuyo contenido o enlace ya no coincide.

## Operaciones administrativas

Cada una queda registrada en la traza.

| Operación | Qué hace |
|---|---|
| `Engine.Reassign` | Con `Reason` obligatoria, pasa un token a otra persona con un `assigned` que registra la persona anterior, la nueva y el motivo, sin volver a despachar efectos. |
| `ReleaseWith` | Exige `Reason` cuando quien libera no es quien tiene el token. |
| `Engine.Cancel` | Con `Reason` obligatoria (`ErrReasonRequired`), cancela todos los tokens y deja el caso en `StatusCancelled`. |
| `Engine.CancelToken` | Cancela una rama y cuenta para el join. Falla con `ErrLastBranch` si dejaría su scope sin ramas vivas. |

### Revisión esperada

`MoveInput`, `ClaimInput`, `ReleaseInput`, `ReassignInput`, `SetDeadlineInput` y
`CancelInput` aceptan `ExpectedRevision`. Si no coincide con la revisión guardada, la
llamada falla con `ErrConflict` antes de cualquier otra comprobación o escritura.

## Persistencia y migraciones

### `workflow/postgres`

Todas las operaciones reciben un `pgxtx.DBTX`, de modo que el consumidor decide si
corren dentro de su transacción.

| Operación | Qué hace |
|---|---|
| `Create`, `GetByID`, `Update` | Guardan y leen un caso. |
| `GetByExternalID` | Busca por `tenant`, `domain` y `external_id`. |
| `AppendEvent`, `ListEvents` | Guardan y leen eventos, con `Event.DeadlineAt` en `workflow_event.deadline_at`. `AppendEvent` guarda el `seq` que asignó el motor (desde `Case.LastSeq`), y un `seq` ocupado falla en vez de correrse. |
| `ListWorkItems` | Un `WorkItem` por token activo de un caso abierto, filtrado por nodo, `(posición, unidad)`, asignación y plazo del token. |
| `ListOverdue` | Los tokens vencidos sin marcar, para `Engine.Overdue`, desde `OverdueFilter.After`. |
| `CreateScope`, `UpdateScope`, `ListScopes` | Scopes. |
| `CreateToken`, `UpdateToken`, `ListTokens` | Tokens. |
| `AdvanceLastSeq` | Toma el siguiente `seq` sin tocar `revision`, para `RecordFailure`. |
| `ListParticipated` | Los casos en los que una persona actuó. |
| `ListEventDigests` | Los enlaces de la cadena, con el digest v2 recalculado en Postgres. |

### `workflow/migrations`

Migraciones embebidas expuestas como `fs.FS` con `migrations.FS()`, con su propia
tabla de versión (`workflow_db_version`), independiente de la de `audit`.

| Migración | Qué hace |
|---|---|
| `001_create_workflow.sql` | Crea `workflow_case` y `workflow_event`. |
| `002_workflow_event_comment.sql` | Agrega `workflow_event.comment TEXT NOT NULL DEFAULT ''`, el comentario u observación de cada paso. |
| `003_workflow_case_revision.sql` | Agrega `workflow_case.revision BIGINT NOT NULL DEFAULT 0`, con la que `Move`, `Claim` y `Release` detectan escrituras concurrentes (`workflow.ErrConflict`). |
| `004_workflow_event_metadata.sql` | Agrega `workflow_case.last_seq` (rellenada con el `MAX(seq)` de cada caso), desde la que el motor asigna el `seq` de cada evento, y `workflow_case.tenant`: la unicidad de un caso pasa a ser `(tenant, domain, external_id)`. Agrega `workflow_event.schema_version`, `detail` y `tenant`, las columnas del actor (`actor_kind`, `acting_as`, `on_behalf_of`, `via_client`) y los ids de correlación (`request_id`, `trace_id`, `span_id`). |
| `005_workflow_event_append_only.sql` | Hace que `workflow_event` sea sólo de agregado, como `audit_log`: triggers que rechazan `UPDATE`, `DELETE` y `TRUNCATE` con SQLSTATE 23001, y la clave foránea `case_id` en `ON DELETE RESTRICT` (borrar un caso con eventos falla). |
| `006_workflow_event_actor_index.sql` | Crea con `CREATE INDEX CONCURRENTLY` (sin transacción) el índice `idx_workflow_event_actor` que usa `Engine.Participated`. |
| `007_workflow_event_digest.sql` | Agrega `workflow_event.prev_digest` y `digest`, y el trigger que encadena cada evento nuevo con el anterior de su caso. Los eventos previos quedan sin encadenar. |
| `008_workflow_tokens.sql` | Crea `workflow_scope` y `workflow_token`, agrega `workflow_event.token_id` y `scope_id` (nulos en los eventos previos, que se leen como el token raíz) y el índice único `uq_workflow_event_joined_once`, y materializa un scope raíz y un token raíz por caso existente. |
| `009_workflow_token_expired.sql` | Agrega `workflow_token.expired_deadline_at`, el plazo que `Engine.Expire` marcó como vencido. |
| `010_workflow_event_digest_v2.sql` | Agrega `workflow_event.deadline_at` (el plazo con el que el token entró al nodo) y reemplaza el digest de la cadena por la v2, que además cubre `token_id`, `scope_id` y `deadline_at`. |
| `011_workflow_token_outcome.sql` | Agrega `workflow_token.outcome`, nula y sin relleno: lo que respondió una rama en un join con política. El evento `waiting` guarda lo mismo en su `detail`, que el digest v2 ya cubre, así que no hay digest v3. |

### Cuidados al desplegar

- **003:** la detección de escrituras concurrentes sólo es efectiva cuando ya no queda
  ningún binario con un `vogel` anterior escribiendo `workflow_case`, porque no avanzan
  `revision` y sus escrituras se pueden perder sin conflicto. Aplicar la 003 y luego
  reemplazar todos los binarios viejos (ver su entrada en el
  [`CHANGELOG.md`](../CHANGELOG.md)).
- **004:** un binario anterior agrega eventos sin avanzar `last_seq`, y el siguiente
  binario nuevo falla contra `UNIQUE (case_id, seq)`. Aplicar la 004 y reemplazar todos
  los binarios juntos.
- **005:** los tests deben usar una base nueva en vez de limpiar con `DELETE` o
  `TRUNCATE`. Una purga por retención sigue la receta de la cabecera de la 005.
- **008:** es obligatoria antes de desplegar y todos los binarios se reemplazan juntos
  (ver [`MIGRATION_GUIDE.md`](MIGRATION_GUIDE.md)).
- **010:** es obligatoria antes de desplegar. Una base con eventos encadenados con v1
  los vería como rotos.
- **011:** es obligatoria antes de desplegar, porque el repositorio escribe la columna. No
  hay vuelta atrás simple: `CurrentEventSchema` pasa a 5 en todo evento nuevo, también en
  casos sin política, y un binario anterior falla con `ErrUnsupportedEventSchema` al leer
  esos casos. Reemplazar todos los binarios juntos y corregir hacia adelante. Su `Down` borra
  las respuestas de los tokens en espera, así que correrlo sólo sin rondas abiertas.

## Errores y `workflowhttp`

`Status(err) (status, code, ok)` traduce los errores centinela de `workflow` a un estado
HTTP y a un código del catálogo de `httpx/response`.

| Estado | Errores |
|---|---|
| 404 | `ErrCaseNotFound`, `ErrTokenNotFound` |
| 409 | `ErrConflict`, `ErrAlreadyAssigned`, `ErrCaseClosed`, `ErrInvalidTransition`, `ErrReturnNotVisited`, `ErrNoRoute`, `ErrGuardRejected`, `ErrLastBranch`, `ErrJoinRejected`, `ErrAlreadyExpired`, `ErrNotOverdue`, `ErrCaseExists`, `ErrNotAssigned` |
| 422 | `ErrCommentRequired`, `ErrCommentNotAllowed`, `ErrReasonRequired`, `ErrTokenRequired`, `ErrInvalidActor` |
| 400 | `ErrOffsetOutOfRange` |

Cualquier otro error devuelve `ok = false`: el llamador lo registra y responde 500. Un
test compara la lista con todos los `Err*` exportados de `workflow`, así que un
centinela nuevo no puede volverse un 500 sin que nadie lo decida.

Es un paquete aparte para que `workflow` no dependa de HTTP.
