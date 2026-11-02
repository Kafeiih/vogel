# ADR 0003: sólo de agregado, garantizado por la base de datos

**Estado:** Vigente

## Decisión

`audit_log`, `workflow_event` y `action_attempt` rechazan `UPDATE`, `DELETE` y
`TRUNCATE` con triggers que fallan con SQLSTATE 23001.

## Motivo

No quedó registrado. El patrón nació en `audit_log` y las otras dos tablas lo copian.

## Alternativas descartadas

**`ON DELETE CASCADE` en `workflow_event.case_id`.** Pasó a `ON DELETE RESTRICT`, así
que borrar un caso con eventos falla. No quedó registrado el motivo.

## Consecuencias

- Los tests deben usar una base nueva en vez de limpiar con `DELETE` o `TRUNCATE`.
- Una purga por retención sigue la receta de la cabecera de la migración 005 de
  `workflow`.
- Los triggers solo protegen si la aplicación no es dueña de la tabla: ver
  [ADR 0004](0004-roles-separados-para-migrar-y-servir.md).

## Fuentes

- [`docs/workflow.md`](../workflow.md), «Persistencia y migraciones»
- `odd/tasks/workflow-trace-metadata.md`, S9
- `odd/tasks/action-effect-attempts.md`, S4
- `action/migrations/001_create_action_attempt.sql`
