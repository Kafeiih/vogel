# Contribuir a vogel

Esta guía permite preparar el entorno, enviar un cambio y publicar una versión
sin depender de quien mantiene el proyecto hoy. Para reportar una vulnerabilidad,
no abrir un issue: seguir [`SECURITY.md`](SECURITY.md).

## Camino rápido

1. Instalar Go (la versión está en `go.mod`). Docker hace falta para los tests de
   integración y para los scripts del boilerplate.
2. Crear una rama desde `master`, hacer el cambio con sus tests y correr
   `go test ./... -race`.
3. Abrir un PR con la plantilla del repositorio. Si el cambio toca código, agregar
   el label `run-integration` antes de mergear.

## Comandos

| Qué comprueba | Comando |
|---|---|
| Tests unitarios | `go test ./... -race` |
| Tests de integración (Docker) | `go test -tags=integration ./... -race` |
| Compilación, incluidos los paquetes sin tests | `go build ./...` |
| Lint (`golangci-lint` v2.13.2) | `golangci-lint run --build-tags=integration` |
| Vulnerabilidades (`govulncheck` v1.8.0) | `govulncheck ./...` |
| Boilerplate, que es un módulo aparte | `cd examples/boilerplate && go test ./... -race` |
| Integración del boilerplate (Docker) | `cd examples/boilerplate && go test -tags=integration ./... -race` |
| Proyectos que genera `create-vogel-app` | `cmd/create-vogel-app/check-generated.sh` |

`go test ./...` desde la raíz no alcanza a `examples/boilerplate`: tiene su propio
`go.mod`, con un `replace` hacia la raíz. El job `Boilerplate example` de
[`ci.yaml`](.github/workflows/ci.yaml) lista el resto de sus comprobaciones.

Los tests de integración levantan PostgreSQL con testcontainers: no hace falta
preparar una base de datos.

## Enviar un cambio

1. **Rama y commits.** Una rama por cambio, con commits en formato
   [Conventional Commits](https://www.conventionalcommits.org/es/v1.0.0/).
2. **Idioma.** La documentación va en español. El código y sus comentarios nuevos,
   los commits y los PR van en inglés.
3. **CHANGELOG.** Todo cambio relevante suma una entrada bajo `[Unreleased]` en
   [`CHANGELOG.md`](CHANGELOG.md).
4. **PR.** Completar la plantilla, incluido el checklist de seguridad.
5. **Integración.** Agregar el label después de crear el PR:

   ```bash
   gh pr edit <número> --add-label run-integration
   ```

   Pasarlo con `--label` a `gh pr create` puede dejar la integración sin correr.

6. **Checks.** `gh pr checks --watch` puede terminar sin error mientras informa
   "no checks reported" si el PR es muy reciente. Eso no significa que esté en
   verde: volver a consultar.

### Qué exige `master`

| Check | ¿Obligatorio? | Cuándo corre |
|---|---|---|
| `Lint + Unit + Build` | Sí | Salvo en PR solo de documentación |
| `Boilerplate example` | Sí | Salvo en PR solo de documentación |
| `govulncheck` | Sí | Salvo en PR solo de documentación |
| `gitleaks` | Sí | Siempre |
| `Trivy image + config` | Sí | Siempre |
| `Integration` | **No** | Solo con el label `run-integration` y si el PR toca código |

Como `Integration` no es obligatorio, GitHub no frena un PR que no la corrió:
comprobar que haya pasado antes de mergear un cambio de código.

Un PR es solo de documentación cuando todos sus archivos son `*.md` o están bajo
`docs/` o `.atl/`.

`govulncheck` consulta una base que cambia sola: un advisory nuevo puede poner en
rojo un PR que no tocó dependencias. El arreglo es actualizar esa dependencia, si
hace falta en un PR aparte.

### Revisión

[`CODEOWNERS`](.github/CODEOWNERS) lista las rutas sensibles: autenticación,
autorización, `workflow`, migraciones, CI y dependencias, entre otras. Hoy tienen
un solo responsable y la regla no se aplica a los administradores, de modo que un
cambio en esas rutas puede mergearse sin una segunda revisión. El
[issue #201](https://github.com/Kafeiih/vogel/issues/201) sigue abierto por eso.

## Reglas al cambiar el código

| Si el cambio… | Entonces… |
|---|---|
| Rompe la API o un formato persistido | Seguir [`docs/STABILITY.md`](docs/STABILITY.md) y anunciarlo en el `CHANGELOG.md`. |
| Agrega o modifica una migración | No editar una ya publicada. Si `workflow` ya corre en producción, incluir un camino de actualización ([ADR 0012](docs/adr/0012-niveles-de-estabilidad.md)). |
| Agrega una operación que mueve tokens | Sumarla a `workflow/random_walk_test.go` ([ADR 0008](docs/adr/0008-alcance-del-motor-de-workflow.md)). |
| Agrega una dependencia | Cumplir [`docs/security/DEPENDENCIES.md`](docs/security/DEPENDENCIES.md). |
| Hace que un puerto importe un adaptador | CI falla a propósito ([ADR 0001](docs/adr/0001-puertos-sin-dependencias.md)). |
| Toma una decisión difícil de revertir | Agregar un ADR ([`docs/adr/`](docs/adr/README.md)). |

## Publicar una versión

`X` es la versión nueva, por ejemplo `v0.23.0`.

1. Crear la rama `release/vX` desde `master` actualizado.
2. Hacer un único commit, `docs: point adoption instructions at vX`, que:
   - agregue `## [X sin la v] — AAAA-MM-DD` en `CHANGELOG.md`, justo después de
     `## [Unreleased]`, de modo que las entradas pendientes queden en la versión
     nueva;
   - actualice la versión en `README.md`, `docs/MIGRATION_GUIDE.md`,
     `examples/boilerplate/README.md` y `examples/boilerplate/go.mod`.
3. Abrir un PR titulado `release: vX`, agregar `run-integration` y mergearlo cuando
   esté en verde.
4. Crear y publicar el tag anotado sobre el commit de merge:

   ```bash
   git tag -a vX -m vX <sha-del-merge>
   git push origin vX
   ```

5. Comprobar el resultado. El workflow `Release` crea la GitHub Release y le adjunta
   los SBOM y `SHA256SUMS`, con su atestación de procedencia:

   ```bash
   gh release view vX --json name,assets
   ```

## Dónde están las decisiones

| Lugar | Qué contiene |
|---|---|
| [`docs/adr/`](docs/adr/README.md) | Las decisiones de arquitectura vigentes y sus motivos. **Empezar por acá.** |
| `odd/tasks/*.md` | La especificación y la bitácora de cada funcionalidad: el detalle y su historia. |
| [`CHANGELOG.md`](CHANGELOG.md) | Qué cambió en cada versión. |
| [`docs/THREAT_MODEL.md`](docs/THREAT_MODEL.md) | Qué cubre `vogel` en seguridad y qué queda a cargo de cada servicio. |
