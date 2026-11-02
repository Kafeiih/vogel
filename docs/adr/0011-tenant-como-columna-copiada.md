# ADR 0011: tenant como columna copiada, sin RLS

**Estado:** Vigente

## Decisión

`Case.Tenant` se copia a cada evento y la unicidad de un caso es
`(tenant, domain, external_id)`. Un valor vacío corresponde a un consumidor de un solo tenant.

## Motivo

Copiarlo permite que las exportaciones y una futura seguridad por fila no necesiten
un join.

## Alternativas descartadas

No quedaron registradas.

## Consecuencias

- **`vogel` no incluye políticas de seguridad por fila:** ninguna migración crea
  `CREATE POLICY`. La columna solo deja preparado ese camino, así que hoy filtrar por
  tenant depende de cada consumidor.
- En `InboxFilter`, un `Tenant` vacío significa cualquiera.

## Fuentes

- `odd/tasks/workflow-trace-metadata.md`, S3
- [`docs/workflow.md`](../workflow.md), migración 004
