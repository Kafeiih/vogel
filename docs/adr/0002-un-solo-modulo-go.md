# ADR 0002: un solo módulo de Go, sin submódulos

**Estado:** Vigente, decidido el 2026-11-02

## Decisión

Los adaptadores siguen siendo paquetes del módulo `github.com/kafeiih/vogel`. Un
consumidor que no los importa no los compila.

## Motivo

Medición sobre v0.22.0, con un módulo temporal que importa solo `workflow` y
`workflow/postgres`:

| Medida | Consumidor | `vogel` completo |
|---|---|---|
| Líneas de `go.mod` | 17 | 172 |
| Líneas de `go.sum` | 129 | 432 |

Ese consumidor compila 36 paquetes externos, ninguno de las dependencias pesadas.
Quedan 39 módulos de ellas en el grafo, sin compilarse.

## Alternativas descartadas

**Separar los adaptadores en submódulos.** Implicaría versiones y releases
coordinados y cambios en las rutas de import, para un beneficio chico.

## Consecuencias

- **Sin verificar:** no se corrieron Trivy ni `govulncheck` sobre el módulo de prueba.
  Si en una app real alertan por módulos que no se compilan, conviene reabrir #199.
- Después de la v1.0, cambiar esas rutas rompe a los consumidores.
- `examples/boilerplate` es la excepción: es un módulo aparte, con un `replace`
  hacia la raíz y sus propias dependencias.

## Fuentes

- [#199](https://github.com/Kafeiih/vogel/issues/199), comentario de cierre
- [`docs/STABILITY.md`](../STABILITY.md), «Categorías de paquetes»
