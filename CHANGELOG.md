# Changelog

Todos los cambios relevantes de `vogel`. El formato sigue
[Keep a Changelog](https://keepachangelog.com/es-ES/1.1.0/) y el versionado,
[SemVer](https://semver.org/lang/es/).

## [Unreleased] — v0.4.0

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
  instancia por defecto.
- `Validator.AddError`: la forma soportada de registrar errores desde parsers
  propios de la app que envuelven al `Validator`.

### Notas

- `Validator.MaxInt` no tiene mensaje: solo acota el valor y nunca registra un
  error, igual que en el fork de go-crucible.
