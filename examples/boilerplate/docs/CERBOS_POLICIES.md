# Cerbos Policies Reference

This project uses [Cerbos](https://cerbos.dev) as its Policy Decision Point
(PDP). This page covers the API side.
<!-- vogel:component cerbos begin -->
The policies live in this repository, under [`cerbos/`](../cerbos/), next to
the routes that query them: a route's `RequirePermission` call and the policy
that decides it ship in the same release. `cerbos/README.md` covers how the
PDP image is built, validated and deployed.
<!-- vogel:component cerbos end -->

## Resource Types Used

| Resource Kind | Action | Allowed roles      | Used In              |
|---------------|--------|--------------------|----------------------|
| `audit:entry` | `list` | `admin`            | `GET /v1/audit`      |
| `audit:entry` | `view` | `admin`, `auditor` | `GET /v1/audit/{id}` |

`internal/interfaces/http/rolematrix_test.go` pins the same matrix at the
HTTP layer, with a stand-in for the PDP; keep it and the policy in sync.
<!-- vogel:component cerbos begin -->
`rolematrix_policy_test.go` reads `cerbos/policies` and fails when the
stand-in grants something the policies do not, or the other way around.
<!-- vogel:component cerbos end -->

## What the middleware sends

`RequirePermission` sends the principal's `id` (Zitadel `UserID`) and `roles`
(the `urn:zitadel:iam:org:project:roles` claim), and **no attributes**. The
resource is the route's kind with the `{id}` path parameter as its id, or `*`
when the route has none (as for `list`), also with no attributes. Policies can
therefore match on roles only.
<!-- vogel:component cerbos begin -->
See `cerbos/README.md` before writing a policy that needs attributes.
<!-- vogel:component cerbos end -->

## Adding a New Protected Endpoint

1. Pick the resource kind and action (e.g., `documento`, `create`).
2. Add the middleware in `router.go`:
   ```go
   r.With(vmw.RequirePermission(deps.AuthzChecker, "documento", "create", deps.Logger, authMessages)).
       Post("/", handler.Create)
   ```
3. Add a resource policy to the policies the PDP serves:
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
   <!-- vogel:component cerbos begin -->
   Put it under `cerbos/policies/`, with a `*_test.yaml` suite under
   `cerbos/tests/` that has at least one allowed and one denied role, and run
   `make cerbos-test`. With `make dev-infra` running, the local PDP reloads
   the policy on save.
   <!-- vogel:component cerbos end -->
4. Add a row to the role matrix test.

For attribute-based checks (e.g., only allow editing drafts), call
`authz.Checker.IsAllowed` in the handler after loading the entity, with the
attributes the policy needs, instead of using the middleware.
