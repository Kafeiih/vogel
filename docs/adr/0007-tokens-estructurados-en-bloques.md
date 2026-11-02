# ADR 0007: tokens estructurados en bloques, serializados por caso

**Estado:** Vigente

## Decisión

Las ramas paralelas usan tokens con semántica BPMN, en bloques: cada split nombra su
join. Toda mutación de un token o de un scope incrementa la `revision` del caso en el
mismo CAS, y un índice único parcial impide un segundo join aunque la lógica de la
aplicación falle.

## Motivo

- Con estructura de bloques, el join solo espera los tokens que el split emitió: no
  hace falta eliminar caminos muertos.
- El CAS de la `revision`, que ya existía, da corrección sin tipos de lock nuevos.

## Alternativas descartadas

- **Joins sobre un grafo libre con eliminación de caminos muertos.**
- **Locks por token.**

Las dos se descartaron en la propuesta de diseño, sin más motivos registrados que
los de arriba.

## Consecuencias

- Las ramas de un mismo caso no pueden confirmar a la vez: dos finales concurrentes
  dan un éxito y un `ErrConflict`, y el reintento ve la otra rama ya esperando. La
  especificación lo da por aceptable en tareas a ritmo humano, marcado como inferido.
- Un caso abierto sin tokens falla con `ErrCaseWithoutTokens`: el motor dejó de
  sintetizar el token raíz en la v0.21.0.
- **Pregunta abierta:** si hará falta paralelismo no estructurado o un discriminador
  «gana el primero». Depende de los procesos reales que se migren.

## Fuentes

- `odd/tasks/workflow-tokens.md`, S5, S6, L3 y L4
- [`CHANGELOG.md`](../../CHANGELOG.md), 0.21.0
