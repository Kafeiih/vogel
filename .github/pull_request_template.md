## Summary

<!-- What changes and why. Link the issue if there is one. -->

## Testing

<!-- Commands you ran and their result (e.g. go test ./..., the boilerplate
module, integration tests with the run-integration label). -->

## Security checklist

<!-- Tick what applies; write "N/A" next to an item that does not apply to
this change. See docs/THREAT_MODEL.md for what vogel covers and what each
consumer must do. -->

- [ ] New or changed routes go through `Authenticate` and an authorization check (`RequirePermission` or `RequireAccess`).
- [ ] All new input (body, params, query, headers, files) is validated and bounded.
- [ ] No secrets, tokens or personal data are logged, returned in errors or stored unredacted in `audit_log`.
- [ ] SQL uses parameters only; no query is built by concatenating input.
- [ ] New dependencies meet `docs/security/DEPENDENCIES.md` (need, identity, maintenance, `govulncheck`, license).
- [ ] `CHANGELOG.md` describes every consumer-visible change, breaking changes called out.
- [ ] `docs/THREAT_MODEL.md` is updated if this adds an entry point, an external service or a new kind of personal data.
