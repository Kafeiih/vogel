# ADR 0005: esquema versionado de eventos

**Estado:** Vigente. `CurrentEventSchema` vale 5.

## Decisión

Cada `workflow_event` guarda su `schema_version`. Las filas nunca se reescriben: al
leer, un upcaster completa los valores por defecto de las versiones viejas. Una
versión mayor que la que conoce el binario falla con `ErrUnsupportedEventSchema`.

## Motivo

Un binario no debe leer un evento más nuevo perdiendo campos sin avisar. No quedó
registrada una justificación más extensa.

## Alternativas descartadas

**Descartar en silencio los campos desconocidos.** Es justamente lo que el error
evita.

## Consecuencias

- Subir `CurrentEventSchema` cuenta como cambio que rompe
  ([`docs/STABILITY.md`](../STABILITY.md)).
- Un campo nuevo que deba quedar cubierto por la cadena de hash conviene que sea una
  columna: `deadline_at` se agregó así porque el `Detail` de un `moved` ya lleva la
  causa del vencimiento y una columna entra directo al digest.
- Los detalles de un join con política (versión 5) van en el `Detail` de `waiting` y
  `joined`, tipos que no llevaban otro dato, y el digest v2 ya cubre `detail`. Por eso
  no hizo falta una columna ni un digest v3.

## Fuentes

- `odd/tasks/workflow-trace-metadata.md`, S1
- `odd/tasks/workflow-event-format-v2.md`, L3
