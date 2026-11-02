# ADR 0009: idempotencia en dos capas

**Estado:** Vigente, experimental. Publicado en la v0.22.0.

## Decisión

- `idempotency/postgres.Store.Do` corre dentro de la transacción de la mutación: la
  clave, la mutación y su respuesta confirman o se deshacen juntas.
- `idempotency/httpx.Middleware` envuelve cualquier otro handler y reserva la clave
  con un plazo.

## Motivo

La capa transaccional da exactamente una vez a las rutas que lo necesitan. El
middleware es la opción genérica para el resto.

## Alternativas descartadas

- **Solo una de las capas.** La bitácora registra la elección de «Ambas capas», pero
  no el detalle de las otras opciones.
- **Usar el patrón de ruta en el `Scope`.** Se usa el path concreto, para distinguir
  `/cases/1/claim` de `/cases/2/claim` sin depender de un router.

## Consecuencias

- **Brecha documentada del middleware:** si el proceso cae antes de guardar la
  respuesta, al vencer el plazo el siguiente pedido la toma y ejecuta otra vez. Una
  ruta que necesita exactamente una vez usa `Store.Do`.
- Un token `reservation` impide que un pedido lento pise a quien tomó su reserva.
- El consumidor programa la limpieza de las claves vencidas.

## Fuentes

- `odd/tasks/idempotency-keys.md`, S3 y L2 a L5
- [`CHANGELOG.md`](../../CHANGELOG.md), 0.22.0
