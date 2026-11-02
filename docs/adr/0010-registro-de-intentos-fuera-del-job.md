# ADR 0010: registro de intentos fuera del trabajo del job

**Estado:** Vigente

## Decisión

Con la opción `WithAttemptLog`, el worker inserta una fila por intento en
`action_attempt`, en su propia sentencia y nunca dentro del trabajo del job. Un insert
fallido no hace fallar ni reintentar el job: se registra en el log y en una métrica.

`error_class` guarda el tipo de Go y el centinela, nunca el mensaje.

## Motivo

`error_class` no guarda el mensaje porque puede llevar datos personales. No quedó
registrado por qué el insert corre aparte del trabajo del job.

## Alternativas descartadas

- **Clave foránea a las tablas de `workflow`.** Sin ella, `action` se puede usar sin
  esas tablas.
- **`UNIQUE (job_id, attempt)`.** River devuelve el intento al posponer un job, así que
  se perdería la fila de la corrida siguiente. La clave es
  `UNIQUE (job_id, attempt, snooze)`.

## Consecuencias

- Un job puede terminar sin fila si el insert falla:
  `vogel_action_attempt_log_errors_total` lo hace visible.
- El insert corre con un contexto separado del job y un plazo de 5 segundos, que
  `WithAttemptLogTimeout` puede cambiar.

## Fuentes

- `odd/tasks/action-effect-attempts.md`, S2 y L4
- `action/worker.go`
