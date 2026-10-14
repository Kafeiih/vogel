# Política de seguridad

**Para reportar una vulnerabilidad, no abra un issue público.** Use el reporte
privado de GitHub: pestaña **Security** del repositorio → **Report a
vulnerability** (o directamente
<https://github.com/Kafeiih/vogel/security/advisories/new>). El reporte solo
lo ven los mantenedores.

## Versiones con soporte

`vogel` está antes de 1.0: cada versión menor puede traer cambios que rompen,
siempre anunciados en el `CHANGELOG.md`.

| Versión | Recibe correcciones de seguridad |
|---|---|
| La última versión menor publicada (`0.x` más alta) | Sí |
| Versiones menores anteriores | No: actualizar a la última |
| `master` sin publicar | Se corrige ahí antes de publicar |

## Qué incluir en el reporte

- Paquete y versión (o commit) afectados.
- Descripción del problema y su impacto (qué puede hacer un atacante).
- Pasos o código mínimo para reproducirlo.
- Si ya es público o se está explotando.

No incluya datos personales reales ni secretos vigentes; si son necesarios
para reproducir, descríbalos.

## Plazos de respuesta

| Paso | Plazo |
|---|---|
| Acuse de recibo | 3 días hábiles |
| Evaluación inicial (confirmado o descartado, con severidad CVSS) | 10 días hábiles |
| Corrección o plan de mitigación, severidad alta o crítica | 30 días corridos |
| Corrección, severidad media o baja | Próxima versión menor planificada |

Si un plazo no se puede cumplir, se avisa en el mismo advisory con la causa y
una fecha nueva.

## Divulgación

Divulgación coordinada:

1. La corrección se prepara en el advisory privado (con un fork temporal si
   hace falta).
2. Se publica la versión corregida y, el mismo día, el advisory de GitHub
   (con CVE cuando corresponda) y la entrada del `CHANGELOG.md`.
3. Se pide no divulgar el detalle hasta esa publicación, o hasta 90 días
   desde el reporte, lo que ocurra primero. Quien reporta figura en el
   advisory, salvo que pida lo contrario.

## Alcance

| Dentro del alcance | Fuera del alcance |
|---|---|
| Los paquetes de la librería (todo lo que no está bajo `examples/`) | Servicios consumidores: su configuración, despliegue y lógica de negocio |
| Valores por defecto inseguros o documentación que lleve a un uso inseguro | Vulnerabilidades de Zitadel, Cerbos, PostgreSQL, S3, River, SendGrid o el proveedor SMTP en sí (reportarlas a cada proyecto) |
| `examples/api` y `examples/boilerplate`, como plantillas que se copian: un fallo ahí se replica en cada servicio creado desde ellas | Hallazgos que requieren acceso de administrador a la infraestructura del consumidor |
| Los workflows de `.github/` (cadena de suministro) | Escaneos automáticos sin un impacto demostrado |

Las vulnerabilidades conocidas en dependencias las detecta `govulncheck` en
CI y las mantiene al día Dependabot; si una afecta a `vogel` de forma
alcanzable, también puede reportarse por esta vía.

El modelo de amenazas de la librería, con lo que `vogel` controla y lo que
queda a cargo de cada servicio, está en
[`docs/THREAT_MODEL.md`](docs/THREAT_MODEL.md); la auditoría contra la lista de
OWASP, en [`docs/security/OWASP_CHECKLIST.md`](docs/security/OWASP_CHECKLIST.md).
