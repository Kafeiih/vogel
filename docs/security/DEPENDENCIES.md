# Criterios para dependencias

Una dependencia nueva de `vogel` llega a cada servicio que importa la
librería o que se genera con `create-vogel-app`. Por eso, sumarla o
mantenerla es una decisión de seguridad y la revisa el dueño del repositorio
(`.github/CODEOWNERS` cubre `go.mod` y `go.sum`, en la raíz y en
`examples/boilerplate`). Este documento dice qué mira esa revisión.

Cubre los módulos Go de los dos `go.mod` y las acciones de GitHub de
`.github/workflows/` y `examples/boilerplate/.github/workflows/`.

## Antes de agregar una dependencia

El PR que agrega un módulo o una acción explica en su descripción por qué
hace falta y revisa estos puntos. Si alguno no se cumple, el PR dice por qué
se acepta igual.

| Criterio | Qué se revisa |
|---|---|
| Necesidad | La biblioteca estándar o una dependencia que ya está no lo resuelven en unas pocas líneas propias |
| Identidad | La ruta del módulo coincide con el repositorio oficial del proyecto (no un *fork* ni un nombre parecido: `typosquatting`); para una acción, el dueño es el publicador real |
| Mantenimiento | Hubo un commit o un release en los últimos 12 meses, y los issues de seguridad reciben respuesta |
| Seguridad | `govulncheck` no informa vulnerabilidades alcanzables; el proyecto tiene una forma de reportarlas (`SECURITY.md` o similar) |
| Licencia | Compatible con la licencia de `vogel` (MIT, BSD, Apache-2.0, ISC o similares); nada con licencias *copyleft* fuertes (GPL, AGPL) |
| Alcance | Las dependencias transitivas que agrega son razonables para lo que aporta; se mira el diff de `go.sum` |
| Versión | Una versión publicada (tag semver), no un pseudo-versión de una rama, salvo que el PR lo justifique |

Las acciones de GitHub se fijan por SHA de commit, con la versión en un
comentario (`uses: owner/action@<sha> # vX.Y.Z`). Dependabot las actualiza
igual que los módulos.

## Mantenimiento

Dependabot abre cada semana los PRs de actualización de los dos `go.mod` y de
las acciones; `govulncheck`, Trivy y las alertas de Dependabot avisan de
vulnerabilidades conocidas. En cada versión menor de `vogel`, junto con la
revisión de [`OWASP_CHECKLIST.md`](OWASP_CHECKLIST.md), se repasan las
dependencias directas.

Una dependencia se considera **abandonada** cuando cumple cualquiera de
estas condiciones:

- El repositorio está archivado o el proyecto anuncia que no sigue.
- No tiene commits ni releases en los últimos 24 meses.
- Tiene una vulnerabilidad conocida sin corregir, con más de 90 días desde
  que se publicó.

Una dependencia abandonada se reemplaza por una alternativa mantenida o por
código propio. Hasta que eso pase, la deuda queda en un issue que dice qué
se usa de ella y cuál es el plan.
