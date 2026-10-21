# examples/bpms

Un ejemplo ejecutable de cómo montar un modelo de procesos sobre el motor
`workflow` de vogel: un **formulario de consulta** ciudadana que llega a la
bandeja del encargado de la unidad de atención.

El motor sigue siendo una máquina de estados. Como pide el paquete `workflow`,
todo lo que no es flujo vive en este ejemplo: la definición en JSON, los
formularios, los datos del caso y la publicación de versiones.

Es un módulo Go aparte (`go.mod` propio con `replace ../..`): depende de
[`cel-go`](https://github.com/google/cel-go), y esa dependencia no debe llegar
a quien importa vogel.

## Arranque rápido

```bash
cd examples/bpms
docker compose up -d
DATABASE_URL='postgres://vogel_bpms:vogel_bpms@localhost:55433/vogel_bpms?sslmode=disable' go run .
```

Al arrancar aplica las migraciones, carga todas las versiones publicadas y, si
no existe ninguna, publica [`consulta.json`](consulta.json) como versión 1.
Escucha en `PORT` (8080 por defecto).

No hay autenticación: cada pedido dice quién es en la cabecera `X-Actor`. Un
servicio real la toma del principal autenticado, como hace
[`examples/api`](../api/).

## Qué muestra

| Pieza | Dónde | Qué resuelve |
|---|---|---|
| Definición versionada | `process.go`, `catalog.go`, `consulta.json` | Un proceso es un JSON con nodos, transiciones y un formulario por nodo. Publicar valida todo y guarda la siguiente versión inmutable; cada caso corre siempre con la versión con que se abrió. |
| Registro de tipos de campo | `fields.go` | Cada tipo (`text`, `textarea`, `email`, `select`) es un plugin: valida su configuración al publicar, el valor al enviar y declara su tipo para las reglas. Sumar un tipo no toca el motor. |
| Reglas CEL | `form.go` | `visibleWhen` se compila al publicar contra todos los campos del proceso. Un campo oculto no se valida ni se guarda. |
| Responsables por perfil | `profiles.go`, `service.go` | El nodo nombra un perfil (`encargado` de `atencion`), nunca a una persona. `ProfileResolver` dice quién lo ocupa al momento de preguntar; aquí lo simula `FakeSAFO`, con la regla de que el subrogante vigente gana sobre el titular. |
| Datos con historial | `store.go` | Cada valor guardado registra campo, nodo, actor y fecha. |

Dos reglas que el motor no impone y este ejemplo sí:

- **Tomar fija a la persona.** Solo quien ocupa el perfil ahora puede tomar la
  tarea, y una vez tomada queda con esa persona aunque el perfil cambie.
- **Solo quien tomó la tarea la avanza.** `Engine.Move` no compara su actor con
  el asignado del caso, así que el servicio lo verifica antes de mover.

## Un recorrido

```bash
api=http://localhost:8080

# Ana envía una consulta (abre el caso en la última versión).
curl -s -X POST $api/processes/consulta/cases -H 'X-Actor: ana' \
  -d '{"action":"enviar","values":{"nombre":"Ana","correo":"ana@example.cl","tema":"general","consulta":"¿Horario de atención?"}}'

# Luis es el encargado titular: la ve en su bandeja.
curl -s $api/inbox -H 'X-Actor: luis'

# Empieza una subrogación: la misma tarea pasa a la bandeja de María, sin reasignar nada.
curl -s -X PUT $api/fake-safo/subrogation -H 'X-Actor: admin' \
  -d '{"position":"encargado","unit":"atencion","actor":"maria"}'
curl -s $api/inbox -H 'X-Actor: maria'

# María toma la tarea y responde.
curl -s -X POST $api/cases/<id>/claim -H 'X-Actor: maria'
curl -s -X POST $api/cases/<id>/submit -H 'X-Actor: maria' \
  -d '{"action":"responder","values":{"respuesta":"De 9 a 14 h."}}'

# Ana ve su caso con los valores y su historial.
curl -s $api/cases/<id> -H 'X-Actor: ana'
```

Con `"tema":"reclamo"` aparece el campo obligatorio `numero_atencion`: su
`visibleWhen` es `tema == "reclamo"`.

## Rutas

| Método y ruta | Qué hace |
|---|---|
| `POST /processes` | Publica un spec como la siguiente versión de su nombre (422 si no compila). |
| `GET /processes/{name}` | La última versión publicada y su spec. |
| `POST /processes/{name}/cases` | Abre un caso, guarda el formulario del nodo inicial y toma `action`. |
| `GET /inbox` | Casos sin tomar cuyo perfil ocupa el actor ahora, más los que el actor tomó. |
| `POST /cases/{id}/claim` | Toma la tarea si el actor ocupa el perfil del nodo actual. |
| `POST /cases/{id}/submit` | Guarda el formulario del nodo actual y toma `action`; solo quien tomó la tarea. |
| `GET /cases/{id}` | El caso con sus valores e historial, para quien actuó sobre él (lo abrió, lo tomó o lo avanzó) u ocupa el perfil del nodo actual. |
| `PUT`, `DELETE /fake-safo/subrogation` | Inicia o termina una subrogación en el directorio falso. |

Los errores de formulario responden 400 con un mensaje por campo en `fields`.

## Pruebas

```bash
go test ./...                     # unitarias: tipos de campo, reglas, compilación del proceso
go test -tags=integration ./...   # Postgres real con testcontainers: publicación, bandeja, subrogación, versiones
```

## Fuera de alcance

Clasificación de cambios entre versiones y migración de casos en curso,
documentos, correos y autenticación real.
