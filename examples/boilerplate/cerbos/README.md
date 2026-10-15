# Cerbos PDP

Everything needed to build, validate and ship the Cerbos Policy Decision
Point (PDP) that answers this service's `RequirePermission` checks. For the
API side (resources, actions, adding an endpoint) see
[`docs/CERBOS_POLICIES.md`](../docs/CERBOS_POLICIES.md).

```
cerbos/
  Dockerfile     # base -> validate (compile + tests) -> final image
  .cerbos.yaml   # server config baked into the image
  .dockerignore
  policies/      # Cerbos policies (YAML only)
  tests/         # Cerbos test suites (*_test.yaml, YAML only)
  test.sh        # make cerbos-test
  smoke.sh       # make cerbos-smoke
```

`policies/` and `tests/` may hold only Cerbos policy, test or schema YAML:
`cerbos compile` reads every YAML or JSON file there and fails on anything
else.

## Commands

| Command             | What it proves                                         |
|---------------------|--------------------------------------------------------|
| `make cerbos-test`  | The policies compile and every test suite passes.     |
| `make cerbos-smoke` | The built image serves those policies (needs `curl`, `jq`). |

## The build is the validation

The final stage copies the policies from the `validate` stage, which runs
`cerbos compile` over `policies/` and `tests/`. BuildKit cannot build the
final image without running that stage, so an image that exists is an image
whose policies compiled and whose tests passed. The tests stay in that stage
and never reach the image.

The build does not prove the image serves the policies: the final stage
copies `.cerbos.yaml`, which nothing validates, and a PDP with an empty policy
store starts, reports healthy and denies everything. `smoke.sh` starts the
image and asserts known ALLOW and DENY answers; the ALLOW is the check that
catches an empty store. Its assertions mirror
`policies/resource_audit_entry.yaml`, so update them together.

## Version pin

The Cerbos image is pinned by tag and digest in two places that must stay
equal: the `base` stage of the `Dockerfile` and the `cerbos` service of
`deploy/docker-compose.developer.yaml`. Dependabot bumps them in separate
PRs (its `docker` and `docker-compose` updaters); merge them together, or
local development runs a different Cerbos than the image you ship.

## Development vs. deploy

- **Development** (`make dev-infra`) bind-mounts `.cerbos.yaml` and
  `policies/` into the upstream image, read-only, with `watchForChanges`, so a
  saved policy applies without a restart.
- **Deploy** runs the built image with the policies baked in and no mounts. A
  bind mount to a host path that does not exist does not fail: Docker creates
  an empty directory and the PDP silently denies every request. Baking the
  validated policies in removes that failure mode.

The policies live at `/bundle/policies`, not at the upstream `/policies`. The
upstream image declares `VOLUME ["/policies", "/tmp", "/.cache"]`, which a
Dockerfile cannot undo, and `docker compose up -d` carries a container's
anonymous volumes over when it recreates it for a new image. Policies baked
into `/policies` would therefore keep serving the previous release's rules
after an upgrade, so a revoked permission would stay granted. `/bundle` is not
a volume, so the PDP always serves the policies of the image it runs. The
unused anonymous volumes still accumulate: `docker compose down -v` or a
periodic `docker volume prune` removes them.

After a deploy, a green healthcheck is not enough: check the policy count in
the PDP logs. A count of 0 means every request is being denied.

## Non-root

The upstream image runs as root. The final image runs as UID `65532`: the PDP
only reads `/config` and `/policies`.

## Attribute-based policies

`RequirePermission` sends only the principal's id and roles and a resource
with no attributes, so a policy that compares attributes (for example a
`derivedRoles` policy on `request.principal.attr.org_id ==
request.resource.attr.org_id`) sees empty values and denies. Send those
attributes first (an attributes resolver for the principal, a loaded entity
for the resource) before relying on them.
