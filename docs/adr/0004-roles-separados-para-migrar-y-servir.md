# ADR 0004: un rol para migrar, otro para servir

**Estado:** Vigente

## Decisión

En producción, `app_migrator` es dueño del esquema y solo lo usa el paso de migración
del despliegue. `app_runtime` atiende la API y el worker, no es dueño de nada y sobre
`audit_log` solo tiene `SELECT` e `INSERT`.

## Motivo

Quien corre las migraciones queda como dueño de las tablas, y el dueño puede
deshabilitar los triggers. Sin esta separación se pierde la garantía de
[ADR 0003](0003-append-only-en-la-base.md).

## Alternativas descartadas

**Migrar al arrancar la aplicación con su propio `DATABASE_URL`.** El proceso que
atiende requests quedaría como dueño y la garantía se perdería.

## Consecuencias

- El despliegue necesita dos credenciales y un paso de migración aparte.
- En RDS, los roles se crean una vez con el usuario maestro, con el mismo script.

## Fuentes

- [`README.md`](../../README.md), «Un rol para migrar, otro para servir»
- `odd/tasks/boilerplate-deploy-rds.md`, S5
