# ADR 0001: puertos sin dependencias, adaptadores en subpaquetes

**Estado:** Vigente

## Decisión

Las interfaces viven en paquetes sin dependencias pesadas y los adaptadores, en
subpaquetes. `reqctx` es el terreno neutral entre `httpx`, `logger` y `audit`.

## Motivo

Importar un puerto nunca debe arrastrar el AWS SDK, SendGrid o go-mail. `audit`,
además, debe poder correr en un worker que jamás tuvo una request, así que no puede
importar `httpx` ni `chi`.

## Alternativas descartadas

**Esconder River detrás de un puerto.** Un consumidor que define jobs ya importa
River, así que sería «ceremonia sin beneficio». Por eso `worker` es la excepción:
interfaz e implementación viven juntas.

## Consecuencias

- El compilador no detecta esta regla. La hace cumplir el paso «Verificar aislamiento
  de los puertos» de CI.
- Solo pueden depender de River `worker`, `otel/otelriver`, `action`, `action/postgres`,
  `document/docflow` y los ejemplos. Solo `otel` y los ejemplos pueden depender del
  SDK de OpenTelemetry.

## Fuentes

- [`README.md`](../../README.md), introducción y fila `worker` del mapa de paquetes
- [`docs/WIRING.md`](../WIRING.md), sección 1
- [`.github/workflows/ci.yaml`](../../.github/workflows/ci.yaml), job `fast`
