# ADR 0008: alcance del motor y random walk

**Estado:** Vigente

## Decisión

Una operación entra al motor sólo si debe mantenerse consistente con los tokens de un
caso y con su traza encadenada. La autorización, `audit_log`, la resolución de
personas, los datos de dominio y los trabajos periódicos quedan en el consumidor.

Toda operación nueva que mueva tokens se suma a `workflow/random_walk_test.go`.

## Motivo

No quedó registrado.

## Alternativas descartadas

No quedaron registradas.

## Consecuencias

- Una función que no cumple el criterio va en la aplicación.
- Una operación que mueve tokens y no se suma al random walk queda fuera de ese test.

## Fuentes

- [`docs/workflow.md`](../workflow.md), alcance del motor
- `workflow/workflow.go`, sección `Scope` de la documentación del paquete
