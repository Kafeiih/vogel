# Decisiones de arquitectura

Cada ADR registra una decisión que no se deduce leyendo el código, o que costaría
revertir. Leerlos antes de cambiar el área que tocan.

| ADR | Decisión | Área |
|---|---|---|
| [0001](0001-puertos-sin-dependencias.md) | Puertos sin dependencias, adaptadores en subpaquetes | Módulo |
| [0002](0002-un-solo-modulo-go.md) | Un solo módulo de Go, sin submódulos | Módulo |
| [0003](0003-append-only-en-la-base.md) | Sólo de agregado, garantizado por la base de datos | Datos |
| [0004](0004-roles-separados-para-migrar-y-servir.md) | Un rol para migrar, otro para servir | Datos |
| [0005](0005-esquema-versionado-de-eventos.md) | Esquema versionado de eventos | `workflow` |
| [0006](0006-cadena-de-hash-en-sql.md) | Cadena de hash por caso, calculada en SQL | `workflow` |
| [0007](0007-tokens-estructurados-en-bloques.md) | Tokens estructurados en bloques, serializados por caso | `workflow` |
| [0008](0008-alcance-del-motor-de-workflow.md) | Alcance del motor y random walk | `workflow` |
| [0009](0009-idempotencia-en-dos-capas.md) | Idempotencia en dos capas | `idempotency` |
| [0010](0010-registro-de-intentos-fuera-del-job.md) | Registro de intentos fuera del trabajo del job | `action` |
| [0011](0011-tenant-como-columna-copiada.md) | Tenant como columna copiada, sin RLS | `workflow` |
| [0012](0012-niveles-de-estabilidad.md) | Niveles de estabilidad y regla de datos en producción | Política |

## Cómo se escribe un ADR

- **Cuándo:** una decisión difícil de revertir, o donde se descartó una alternativa
  que alguien podría volver a proponer. Un arreglo de rutina no lo necesita.
- **Número:** el siguiente libre, en un archivo `NNNN-titulo-corto.md`. Agregarlo
  también a la tabla de arriba.
- **Cambios de decisión:** no reescribir la historia. Marcar el ADR como
  «Reemplazado por ADR NNNN» y escribir uno nuevo.
- **Motivos:** registrar solo lo que realmente se decidió. Si un motivo o una
  alternativa no quedó documentado, decirlo en vez de suponerlo.

```markdown
# ADR NNNN: título

**Estado:** Vigente | Reemplazado por ADR NNNN

## Decisión

## Motivo

## Alternativas descartadas

## Consecuencias

## Fuentes
```

Los ADR 0001 a 0012 se escribieron después de tomar esas decisiones, a partir de
las fuentes que cita cada uno ([#201](https://github.com/Kafeiih/vogel/issues/201)).
