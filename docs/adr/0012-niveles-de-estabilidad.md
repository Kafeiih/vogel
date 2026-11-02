# ADR 0012: niveles de estabilidad y regla de datos en producción

**Estado:** Vigente, decidido el 2026-11-02

## Decisión

`workflow`, `action` e `idempotency` son experimentales: su API puede cambiar con
aviso, incluso después de la v1.0. Desde el primer despliegue en producción, toda
migración o cambio de formato persistido en esos paquetes debe incluir un camino de
actualización sobre los datos existentes.

## Motivo

Se quiere usar `workflow` en producción sin congelar todavía su API. Por eso la
categoría limita los cambios de la API, no el uso en producción, y los datos ya
guardados quedan protegidos con una regla aparte.

## Alternativas descartadas

No quedaron registradas.

## Consecuencias

- El primer despliegue cambia las reglas: las salidas sin compatibilidad, como la de
  [ADR 0006](0006-cadena-de-hash-en-sql.md), dejan de estar disponibles.
- Un paquete pasa a Estable cuando una app en producción lo usa y no tuvo cambios que
  rompan en las últimas tres versiones menores.

## Fuentes

- [`docs/STABILITY.md`](../STABILITY.md)
- [#197](https://github.com/Kafeiih/vogel/issues/197)
