# Cerbos Policies Reference

This project uses [Cerbos](https://cerbos.dev) as a centralized, external Policy Decision Point (PDP).
Policies are NOT stored in this repository — they are managed in a dedicated Cerbos instance.

## Resource Types Used

| Resource Kind     | Actions              | Used In                          |
|-------------------|----------------------|----------------------------------|
| `audit:entry`     | `list`, `view`       | `GET /v1/audit`, `GET /v1/audit/{id}` |

## Principal Attributes

The `RequirePermission` middleware sends these attributes to Cerbos:

| Attribute   | Source                        |
|-------------|-------------------------------|
| `id`        | Zitadel `UserID`              |
| `roles`     | Zitadel `urn:zitadel:iam:org:project:roles` claim |
| `subject`   | Zitadel `Subject` (sub claim) |

## Adding a New Protected Endpoint

1. Define the resource kind and action (e.g., `documento`, `create`)
2. Add the middleware in `router.go`:
   ```go
   r.With(middleware.RequirePermission(deps.AuthzChecker, "documento", "create", deps.Logger)).
       Post("/", handler.Create)
   ```
3. Create the corresponding policy in your Cerbos instance:
   ```yaml
   apiVersion: api.cerbos.dev/v1
   resourcePolicy:
     version: default
     resource: documento
     rules:
       - actions: ["create"]
         effect: EFFECT_ALLOW
         roles: ["admin", "operator"]
   ```
4. For attribute-based checks (e.g., only allow editing drafts), call `authz.Checker.IsAllowed` directly in the handler after loading the entity — don't use the middleware for those.

## Testing Policies

Use the Cerbos CLI to validate policies locally:

```bash
cerbos compile policies/
cerbos run --set policies/ -- test/
```
