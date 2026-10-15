# Cerbos Policies Reference

This project uses [Cerbos](https://cerbos.dev) as its Policy Decision Point
(PDP). The policies live in this repository, under [`cerbos/`](../cerbos/),
next to the routes that query them: a route's `RequirePermission` call and the
policy that decides it ship in the same release. `cerbos/README.md` covers how
the PDP image is built, validated and deployed; this page covers the API side.

## Resource Types Used

| Resource Kind | Action | Allowed roles      | Used In              |
|---------------|--------|--------------------|----------------------|
| `audit:entry` | `list` | `admin`            | `GET /v1/audit`      |
| `audit:entry` | `view` | `admin`, `auditor` | `GET /v1/audit/{id}` |

`internal/interfaces/http/rolematrix_test.go` pins the same matrix at the
HTTP layer, with a stand-in for the PDP; keep it and the policy in sync.

## What the middleware sends

`RequirePermission` sends the principal's `id` (Zitadel `UserID`) and `roles`
(the `urn:zitadel:iam:org:project:roles` claim), and **no attributes**. The
resource is the route's kind with the `{id}` path parameter as its id, or `*`
when the route has none (as for `list`), also with no attributes. Policies can
therefore match on roles only; see `cerbos/README.md` before writing a policy
that needs attributes.

## Adding a New Protected Endpoint

1. Pick the resource kind and action (e.g., `documento`, `create`).
2. Add the middleware in `router.go`:
   ```go
   r.With(vmw.RequirePermission(deps.AuthzChecker, "documento", "create", deps.Logger, authMessages)).
       Post("/", handler.Create)
   ```
3. Add a resource policy under `cerbos/policies/`:
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
4. Add a `*_test.yaml` suite under `cerbos/tests/` with at least one allowed
   and one denied role, and a row to the role matrix test.
5. Run `make cerbos-test`. With `make dev-infra` running, the local PDP
   reloads the policy on save.

For attribute-based checks (e.g., only allow editing drafts), call
`authz.Checker.IsAllowed` in the handler after loading the entity, with the
attributes the policy needs, instead of using the middleware.
