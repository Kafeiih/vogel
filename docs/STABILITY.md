# Estabilidad de la API

Qué promete `vogel` a quien lo consume, qué cuenta como cambio que rompe y qué
falta para publicar una v1.0.

## Hoy, antes de la v1.0

Cada versión menor (`0.x`) puede traer cambios que rompen, siempre anunciados
en la sección «Cambios que rompen» del [`CHANGELOG.md`](../CHANGELOG.md). Las
versiones de parche (`0.x.y`) nunca rompen.

Las categorías de abajo ya indican cuánto puede moverse cada paquete, aunque la
garantía sólo se vuelve obligatoria desde la v1.0.

## Categorías de paquetes

| Categoría | Garantía desde la v1.0 | Paquetes |
|---|---|---|
| **Estable** | Sin cambios que rompan hasta una v2. | `access`, `audit`, `audit/httpx`, `audit/postgres`, `auth`, `authz`, `config`, `decimalx`, `decimalx/decimalxtest`, `document`, `document/docflow`, `document/postgres`, `httpx/middleware`, `httpx/openapi`, `httpx/openapi/openapitest`, `httpx/response`, `logger`, `migrate`, `notification`, `pgxtx`, `postgres`, `reqctx`, `request`, `storage`, `stringutil`, `validation` |
| **Adaptador** | La misma que Estable, pero su ruta de import puede cambiar antes de la v1.0 si se separa en un submódulo ([#199](https://github.com/Kafeiih/vogel/issues/199)). | `auth/zitadel`, `authz/cerbos`, `document/gotenberg`, `notification/sendgrid`, `notification/smtp`, `otel`, `otel/otelpgx`, `otel/otelriver`, `storage/s3`, `worker` |
| **Experimental** | Puede romper en una versión menor, también después de la v1.0, siempre anunciado en el `CHANGELOG.md`. En producción, sus datos siguen la regla de abajo. | `action`, `action/postgres`, `idempotency`, `idempotency/httpx`, `idempotency/postgres`, `workflow`, `workflow/calendar`, `workflow/postgres`, `workflow/workflowhttp` |
| **Sin garantía** | Ninguna: pueden cambiar en cualquier versión. | `internal/...`, `examples/...`, `cmd/create-vogel-app` y el código que genera |

Un paquete experimental pasa a Estable cuando cumple dos condiciones: una app en
producción lo usa y no tuvo cambios que rompan en las últimas tres versiones
menores. El cambio de categoría se anuncia en el `CHANGELOG.md`.

### Datos de un paquete experimental en producción

Experimental describe cuánto puede cambiar la API, no si el paquete sirve para
producción. Lo que cambia es qué se protege según su uso:

- **Mientras ninguna app lo use en producción**, un cambio puede romper sin
  compatibilidad ni backfill.
- **Desde el primer despliegue en producción**, la API de Go y los handlers HTTP
  todavía pueden cambiar con aviso, pero toda migración o cambio de formato
  persistido debe incluir un camino de actualización sobre los datos existentes.

## Qué cuenta como cambio que rompe

- **API de Go:** quitar o renombrar un identificador exportado, cambiar una
  firma o agregar un método a una interfaz que el consumidor implementa.
- **Migraciones:** editar una migración ya publicada o exigir una migración
  nueva antes de desplegar un binario.
- **Formatos persistidos:** cambiar un formato ya guardado de modo que los datos
  existentes dejen de leerse o de verificarse, como el digest de la cadena de
  `workflow_event` o `CurrentEventSchema`.
- **HTTP:** cambiar el código de estado o la forma del JSON de una respuesta de
  un handler exportado.
- **Operación:** renombrar o quitar una métrica o una variable de entorno, o
  cambiar un valor por defecto de forma que altere el comportamiento existente.

No rompen: agregar paquetes, funciones, opciones, campos opcionales o métricas;
corregir un bug hacia el comportamiento documentado; cambiar el texto de un
mensaje de error, salvo los valores que se comparan con `errors.Is` o
`errors.As`.

## Cómo se depreca

1. El identificador se marca con un comentario `// Deprecated:` que nombra su
   reemplazo, y el `CHANGELOG.md` lo anuncia.
2. Se retira en una versión posterior, bajo «Cambios que rompen»:
   - **Antes de la v1.0**, en una versión menor siguiente. Por ejemplo, el
     camino de compatibilidad de tokens, obsoleto desde v0.19.0, se retiró en
     v0.21.0.
   - **Después de la v1.0**, sólo en una v2 para los paquetes Estable y
     Adaptador. En Go, una versión mayor cambia la ruta de import
     (`github.com/kafeiih/vogel/v2`), así que obliga a cada consumidor a
     actualizar sus imports.
   - **En paquetes experimentales**, en una versión menor siguiente, también
     después de la v1.0.

## Criterios para publicar la v1.0

- [ ] [#199](https://github.com/Kafeiih/vogel/issues/199) está decidido, porque
      separar adaptadores cambia las rutas de import.
- [ ] Al menos una app consume `vogel` en producción.
- [ ] Los paquetes Estable y Adaptador no tuvieron cambios que rompan en las
      últimas tres versiones menores.
