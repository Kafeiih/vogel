# ADR 0006: cadena de hash por caso, calculada en SQL

**Estado:** Vigente. El digest v1 fue reemplazado por v2.

## Decisión

Un trigger `BEFORE INSERT` calcula el digest de cada `workflow_event` sobre la fila
guardada, encadenado con el anterior del mismo caso. `VerifyChain` lo recalcula con
la misma función SQL.

## Motivo

JSONB normaliza `detail`, así que un digest calculado en Go sobre los bytes del motor
no se podría recalcular desde la fila.

## Alternativas descartadas

- **Calcular el digest en `appendEvent`.** Descartado por el motivo de arriba.
- **Hacer que `VerifyChain` distinga v1 de v2.** Ninguna base tenía cadenas v1 que
  conservar, así que v2 reemplazó a v1 sin convivencia. Agrega `token_id`, `scope_id`
  y `deadline_at` al digest.

## Consecuencias

- Una base con digests v1 los reportaría como rotos.
- Esa salida ya no está disponible cuando haya datos en producción: un cambio de
  digest deberá incluir un camino de actualización
  ([ADR 0012](0012-niveles-de-estabilidad.md)).
- Las filas anteriores a la migración 007 quedan sin encadenar.

## Fuentes

- `odd/tasks/workflow-trace-metadata.md`, S11
- `odd/tasks/workflow-event-format-v2.md`, objetivo y L2
- [`CHANGELOG.md`](../../CHANGELOG.md), 0.21.0
