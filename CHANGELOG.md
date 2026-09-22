# Changelog

Todos los cambios relevantes de `vogel`. El formato sigue
[Keep a Changelog](https://keepachangelog.com/es-ES/1.1.0/) y el versionado,
[SemVer](https://semver.org/lang/es/).

## [Sin publicar]

### Agregado

- `workflow.CommentPolicy` (`""`/`"none"` por defecto, `"optional"`,
  `"required"`, con su propio `Valid()`) declara si un `Transition` acepta,
  exige o prohíbe un comentario en el `MoveInput` que lo toma: es historial
  de proceso, nunca dato de dominio. `Transition.Comment` fija la política
  por transición y `Definition.Validate` rechaza un valor no reconocido.
  `MoveInput.Comment` se recorta con `strings.TrimSpace` antes de validarse
  contra esa política y de guardarse (ya recortado) en el `Event.Comment`
  del `EventMoved` correspondiente; el `EventClosed` automático que `Move`
  agrega al entrar a un nodo terminal nunca repite ese comentario (queda
  vacío a propósito). La verificación corre ANTES de mutar el caso o de
  invocar la guarda de la transición, así que un `Move` rechazado por
  comentario no deja al motor a medio camino. Dos centinelas nuevos:
  `workflow.ErrCommentRequired` (política `required` con comentario vacío
  tras recortarlo) y `workflow.ErrCommentNotAllowed` (política `none`/vacía
  con un comentario no vacío, para que la política declarada no mienta).
  Nueva migración `workflow/migrations/002_workflow_event_comment.sql`:
  agrega `workflow_event.comment TEXT NOT NULL DEFAULT ''`. **Todo
  consumidor debe correr esta migración ANTES de desplegar esta versión de
  `vogel`, use o no `Event.Comment`** — no es opcional para quien no le
  interesa el comentario. `workflow/postgres/repository.go` arma
  `eventColumns` incluyendo `comment` de forma incondicional, así que contra
  una base de datos que sólo corrió `001_create_workflow.sql` CUALQUIER
  `AppendEvent`/`ListEvents` falla, y con ellos `Engine.Move`, `Claim`,
  `Release` e `History` — todos, no sólo los que tocan comentarios. El
  sentido seguro es el inverso: código viejo (que nunca lee ni escribe
  `Event.Comment`) sigue funcionando sin cambios contra el esquema ya
  migrado, porque la columna nueva es `NOT NULL DEFAULT ''`. Correrla vía
  `migrate.Up` con `workflow/migrations.FS()`, igual que ya corren
  `001_create_workflow.sql`.

- `workflow.NodeKind` (`""`/`"task"` por defecto, `"decision"`, con su propio
  `Valid()`) y el nuevo campo `Node.Kind` agregan al motor un nodo de
  decisión al estilo compuerta exclusiva BPMN: un caso ya no tiene que fundir
  la acción humana con su destino cuando ese destino depende de datos de
  dominio (p. ej. "¿algún ítem tiene subsidio SEP?" → coordinador SEP;
  si no, Presupuesto). Un `Transition` cuyo `From` es un nodo de decisión se
  llama ruta; `Engine.Move` evalúa las rutas salientes en el orden en que
  aparecen en `Definition.Transitions`, y toma la primera cuya `Guard`
  evalúa verdadero. `Definition.Validate` exige que un nodo de decisión: no
  sea `Start` ni `Terminal`; no declare `Eligible` ni `Deadline`; tenga al
  menos dos rutas salientes; tenga exactamente una ruta sin `Guard` (la
  ruta por defecto), declarada al final; y que ninguna de sus rutas declare
  `CommentPolicy` (una ruta no es una acción humana, no tiene nada que
  comentar). También rechaza cualquier ciclo formado sólo por nodos de
  decisión, porque un caso así nunca llegaría a descansar en ningún lado.
  Cuando `Engine.Move` aterriza en un nodo de decisión sigue enrutando,
  dentro de la misma llamada y la misma transacción `db`, hasta que el caso
  descansa en un nodo de tarea o terminal — un caso nunca se persiste
  descansando en un nodo de decisión. Cada salto agrega su propio
  `EventMoved` (con `Action` igual a la etiqueta de esa transición o ruta y
  `ActorID` igual al actor original de `MoveInput`), pero sólo el primer
  salto (el humano) lleva el comentario; los saltos automáticos siempre
  quedan con `Event.Comment` vacío. Si el caso termina descansando en un
  nodo terminal, el caso se cierra con un único `EventClosed` — pero su
  `Action` toma la etiqueta del ÚLTIMO salto de la cadena (la ruta automática
  ganadora), nunca el `MoveInput.Action` humano original cuando hubo saltos
  de por medio: describe cómo el caso llegó realmente al nodo terminal, no
  lo que pidió el humano. Centinela nuevo `workflow.ErrNoRoute`: si ninguna ruta
  calza (una `Definition` que de algún modo llegó al motor sin pasar por
  `Validate` y le falta la ruta por defecto), `Move` falla con este error y
  no muta el caso ni su historial — toda la cadena de saltos se planea en
  memoria antes de escribir nada. Un error real de una guarda de ruta se
  propaga tal cual, como hoy. Centinela adicional
  `workflow.ErrTooManyDecisionHops`: cinturón y tirantes contra una
  `Definition` patológica que de algún modo evadió el rechazo de ciclos de
  `Validate` — `Move` corta después de un número fijo de saltos
  (`maxDecisionHops`) en vez de girar para siempre.

- `workflow.Transition.Return` (D4): una transición de retorno, para que una
  etapa burocrática (un visto bueno) pueda devolver un caso a CUALQUIER etapa
  de decisión anterior que el caso haya recorrido de verdad — nunca a una que
  saltó. `Engine.Available` la ofrece, y `Engine.Move` la acepta, sólo si el
  caso ocupó su `To` alguna vez, determinado a partir del historial de
  eventos propio del caso (el nodo de apertura más el `FromState`/`ToState`
  de cada `EventMoved`), nunca por alcanzabilidad en el grafo de la
  `Definition` — un caso que saltó al coordinador SEP nunca ve ofrecido
  "volver al coordinador SEP". Centinela nuevo `workflow.ErrReturnNotVisited`
  cuando `Move` recibe un retorno cuyo destino el caso nunca ocupó; falla sin
  mutar el caso ni su historial, igual que una guarda rechazada. El chequeo
  corre en un orden fijo, antes de mutar nada: primero la política de
  comentario (heredada de D2), después si el caso ocupó el destino, y recién
  al final la `Guard` de la transición, si tiene una — ambas condiciones
  (visitado Y guarda) deben pasar. Un retorno que pasa las tres corre
  exactamente como cualquier otro `Move`: se recalculan `Deadline` y
  elegibilidad para el nodo de destino y se limpia la asignación, sin
  semántica especial de "deshacer". `Definition.Validate` rechaza una ruta
  saliente de un nodo de decisión marcada como retorno (una ruta enruta
  automáticamente sobre datos de dominio; un retorno es una acción humana) y
  rechaza un retorno que apunte a un nodo terminal. `Engine.Available` y
  `Engine.Move` leen el historial del caso a lo sumo una vez por llamada, y
  sólo cuando alguna transición candidata es efectivamente un retorno — el
  costo extra es cero para cualquier `Definition` que no use D4.

### Cambiado

- **Cambio incompatible (breaking):** `workflow.GuardFunc` gana un segundo
  parámetro, `db pgxtx.DBTX`: `func(ctx context.Context, db pgxtx.DBTX, c
  Case) (bool, error)`. El motor pasa siempre el mismo `db` que recibió en
  `Engine.Available`/`Engine.Move`, nunca uno distinto ni `nil` salvo que el
  propio caller haya pasado `nil`, para que una guarda que lea datos de
  dominio (p. ej. "¿algún ítem tiene subsidio SEP?") vea las escrituras que
  el caller ya hizo antes en su misma transacción. Migración: agregar el
  parámetro `db pgxtx.DBTX` a la firma de cada `GuardFunc` registrada.

## [0.5.0] — 2026-09-22

Cierra las tres últimas brechas que le impedían a go-crucible borrar
`pkg/decimalutil`, su wrapper local de `Int64Query`/`BoolQuery`/`DecimalQuery`
y su validador go-playground montado a mano, cuyos errores hoy se envían
crudos al cliente (`{"validation": "<err.Error() de Go>"}`). Aditivo: ninguna
API de `v0.4.0` cambia.

### Agregado

- `request.Validator`: `Int64Query(r, param) *int64` y
  `BoolQuery(r, param) *bool`, con la misma forma que los demás `*Query`
  existentes (ausente/vacío → `nil` sin error; valor presente pero inválido →
  `nil` más un error de campo). `request.Messages.InvalidBoolean` (nuevo) e
  `InvalidDecimal` (nuevo, usado por `decimalx.Query` — ver abajo) se suman a
  `DefaultMessages`/`WithMessages`, y `Validator.Messages()` expone los
  mensajes efectivos de un `Validator` para que un paquete externo (como
  `decimalx`) registre un error con la misma redacción configurada, sin que
  `request` tenga que conocerlo ni importar su dependencia.
- Paquete nuevo `decimalx`: parser acotado de decimales anti-DoS, portado de
  `pkg/decimalutil` de go-crucible con identificadores y documentación en
  inglés. `Parse(s)` acota la longitud del string ANTES de construir nada,
  luego `decimal.NewFromString`, luego `ValidateBounds`; `ValidateBounds(d)`
  es la única definición de las cotas (exponente en `[MinExp, MaxExp]`
  primero, O(1); longitud del coeficiente vía `NumDigits` después) para un
  decimal que llegó ya construido (p. ej. por `encoding/json`, sin pasar por
  `Parse`). `Bounds`/`NewBounds` permiten cotas propias, pero `NewBounds`
  rechaza toda configuración que desactive la guarda (longitud no positiva o
  por encima de `MaxLengthLimit`, un techo duro de 1000 caracteres; rango
  invertido; o un exponente que cruce `MaxExponentLimit`, un techo duro
  de ±1000). La regla de dinero, con la escala como PARÁMETRO (no fija en 2
  como en crucible): `ValidateAmount(d, scale)` corre `ValidateBounds`
  primero y rechaza — sin redondear nunca en silencio — lo que no sobrevive a
  `d.Round(scale)`; `ParseAmount(s, scale)` es `Parse` + `ValidateAmount`. La
  propia `scale` está acotada a `[-MaxExponentLimit, MaxExponentLimit]`
  (`ErrInvalidScale`) antes de llegar a `Round`, por la misma razón de
  materialización que las cotas de `Bounds`. `decimalx.Query(v, r, param)`
  vive en este paquete (no en `request`, que no debe importar
  `shopspring/decimal`) y NO rechaza negativos, porque un monto como el total
  de una nota de crédito es legítimamente negativo. Semántica deliberadamente
  conservada de crucible: rellenar un literal con ceros más allá del
  exponente configurado se rechaza en la cota, aunque el valor resultante sea
  exacto a la escala pedida (`"100.500"` se acepta, `"100.000000000"` se
  rechaza a pesar de valer lo mismo a escala 2); y la cota de `Parse` mide
  caracteres del STRING (el signo cuenta) mientras que la de `ValidateBounds`
  mide dígitos del COEFICIENTE (el signo no cuenta), así que la puerta de
  string es más estricta, nunca más laxa.
- Subpaquete nuevo `decimalx/decimalxtest`:
  `AssertNoDirectNewFromString(t, root, dirs...)`, un guardián estructural
  (basado en un regex sobre archivos `.go` no-test) que falla si algún
  archivo fuera de `decimalx` llama a `decimal.NewFromString`,
  `RequireFromString` o `NewFromFormattedString` directamente en lugar de
  pasar por `decimalx.Parse` — portado de
  `pkg/decimalutil/guard_test.go` de go-crucible.
- Paquete nuevo `validation`: traduce los errores de
  `go-playground/validator/v10` a la misma forma que ya usa
  `request.Validator`, `request.FieldErrors`. `validation.New(opts...)`
  construye un `*validator.Validate` con
  `validator.WithRequiredStructEnabled()` y un `RegisterTagNameFunc` que
  nombra cada campo según su tag `json` (el nombre antes de la primera coma;
  `json:"-"` o sin tag cae al nombre del campo de Go, igual que
  `encoding/json`). `(*Validator).Struct(v)` retorna `(nil, nil)` si `v` es
  válido; ante `validator.ValidationErrors` retorna una entrada por campo,
  con clave igual a la ruta JSON (`fe.Namespace()` sin el segmento raíz:
  `"address.street"`, `"items[0].monto"`) y valor un mensaje de
  `validation.Messages` (`Required` — usado también para las variantes
  condicionales `required_if`, `required_unless`, `required_with`,
  `required_with_all`, `required_without` y `required_without_all`, en vez de
  caer en `Default` —, `Min`/`Max` — conscientes del `reflect.Kind` del
  campo, porque "al menos 3" significa algo distinto para un string, una
  colección o un número —, `OneOf`, y `Default` como respaldo para cualquier
  otro tag). Un campo embebido (anónimo) se aplana igual que `encoding/json`:
  sin tag `json` propio, no aporta segmento — `Base.id` nunca se filtra, la
  clave es `id` — pero con un tag `json` explícito (`Base json:"base"`) se
  conserva como segmento ordinario (`base.id`); aplica también embebido por
  puntero, dentro de un struct anidado, y dentro de elementos de un `dive`.
  Cualquier otro error —
  `*validator.InvalidValidationError` si `v` no es un struct o es `nil` — se
  retorna tal cual: nunca se envía `err.Error()` al cliente.
  `(*Validator).Write(w, r, v)` valida y escribe la respuesta:
  `response.ValidationError` con las claves por campo si es inválido,
  `response.Error` (500, `CodeInternalError`) sin exponer el texto del error
  de Go si es un error de programación. Hay instancias y funciones de
  paquete (`validation.Struct`, `validation.Write`) con `DefaultMessages`,
  igual que `request`. Vive separado de `request` para que un consumidor que
  no valida structs no arrastre `go-playground/validator/v10`.
- Nuevas dependencias directas: `github.com/shopspring/decimal` v1.4.0 (para
  `decimalx`) y `github.com/go-playground/validator/v10` v10.30.1 (para
  `validation`). Ninguna de las dos la importa `request`
  (`go list -deps ./request` lo verifica).

## [0.4.0] — 2026-09-22

Cierra las brechas que impedían a go-crucible borrar sus forks locales de
`request` y `httpx/response`. Compatible hacia atrás: con opciones por defecto,
las respuestas son byte a byte iguales a `v0.3.0`.

### Agregado

- `httpx/response`: `CodeExportTooLarge = "EXPORT_TOO_LARGE"`, para HTTP 413
  cuando un export pedido supera el tope de filas del servidor (distinto de
  `CodeBodyTooLarge`, que es sobre el cuerpo de la solicitud).
- `request.JSONOptional`: un cuerpo vacío devuelve `nil` sin escribir nada y
  deja el destino sin tocar; cualquier otro error se comporta igual que `JSON`.
- `request.Messages`, `request.DefaultMessages`, `request.WithMessages` y
  `request.Decoder` (vía `request.New`): todos los textos de decodificación y
  del `Validator` son localizables, configurados una vez por aplicación. Los
  campos vacíos conservan el texto en inglés por defecto. Las funciones de
  paquete (`JSON`, `JSONWithLimit`, `JSONOptional`, `NewValidator`) usan una
  instancia por defecto. Un `Decoder` que no pasó por `New` (valor cero o
  puntero nil) usa los mensajes por defecto en vez de entrar en pánico.
- `Validator.AddError`: la forma soportada de registrar errores desde parsers
  propios de la app que envuelven al `Validator`.

### Notas

- `Validator.MaxInt` no tiene mensaje: solo acota el valor y nunca registra un
  error, igual que en el fork de go-crucible.
