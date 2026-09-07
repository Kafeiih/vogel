// Package zitadel adapts github.com/zitadel/zitadel-go/v3's OIDC authorizer
// to the auth.Authenticator port.
package zitadel

import (
	"context"
	"errors"
	"fmt"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/zitadel-go/v3/pkg/authorization"
	"github.com/zitadel/zitadel-go/v3/pkg/authorization/oauth"
	"github.com/zitadel/zitadel-go/v3/pkg/zitadel"

	"github.com/kafeiih/vogel/auth"
)

// Config holds the settings needed to construct an Authenticator backed by Zitadel.
type Config struct {
	Issuer   string // Zitadel instance issuer URL, e.g. "https://my-instance.zitadel.cloud"
	ClientID string // OIDC client/application ID used for audience validation
}

// Authenticator implements auth.Authenticator using Zitadel's local JWT
// verification: the instance's public keys (JWKS) are downloaded once on
// construction and cached in memory for subsequent requests.
//
// The concrete generic type authorization.Authorizer[*oauth.IntrospectionContext]
// is deliberately kept private to this package: it must never appear in the
// auth port or in httpx/middleware, or importing the port would pull in the
// Zitadel SDK transitively — defeating the whole point of the port/adapter
// split (see the vogel README).
type Authenticator struct {
	authorizer *authorization.Authorizer[*oauth.IntrospectionContext]
}

// New creates an Authenticator that validates tokens issued by the Zitadel
// instance at cfg.Issuer for the audience cfg.ClientID.
func New(ctx context.Context, cfg Config) (*Authenticator, error) {
	if cfg.Issuer == "" {
		return nil, fmt.Errorf("auth/zitadel: issuer is required")
	}
	if cfg.ClientID == "" {
		return nil, fmt.Errorf("auth/zitadel: client ID is required")
	}

	z := zitadel.New(cfg.Issuer)
	authorizer, err := authorization.New(ctx, z, oauth.DefaultJWTAuthorization(cfg.ClientID))
	if err != nil {
		return nil, fmt.Errorf("auth/zitadel: creating authorizer: %w", err)
	}

	return &Authenticator{authorizer: authorizer}, nil
}

// newFromAuthorizer builds an Authenticator around an already-constructed
// authorizer. Unexported: it exists so this package's own tests can inject a
// fake authorization.Verifier and exercise the real error-mapping path
// without a network call or a real Zitadel instance (see zitadel_test.go),
// while keeping authorization.Authorizer[*oauth.IntrospectionContext] out of
// the exported constructor surface.
func newFromAuthorizer(a *authorization.Authorizer[*oauth.IntrospectionContext]) *Authenticator {
	return &Authenticator{authorizer: a}
}

// Authenticate implements auth.Authenticator.
//
// token is the bare bearer token, with any "Bearer " scheme prefix already
// stripped by the caller (httpx/middleware.Authenticate). The Zitadel SDK's
// Authorizer.CheckAuthorization, however, expects the credential in the same
// form it would have arrived in the "Authorization" header, scheme prefix
// included: its internal checkForEmptyorMalformedToken (see
// github.com/zitadel/zitadel-go/v3/pkg/authorization.CheckAuthorization,
// authorization/check.go) does
// `strings.CutPrefix(strings.TrimSpace(tokenHeader), oidc.BearerToken+" ")`
// and treats a missing prefix as a malformed token. So the prefix is added
// back here before the SDK call; this is the one place in the module that
// needs to know that detail of the SDK's contract.
func (a *Authenticator) Authenticate(ctx context.Context, token string) (*auth.Principal, error) {
	authCtx, err := a.authorizer.CheckAuthorization(ctx, oidc.PrefixBearer+token)
	if err != nil {
		return nil, mapError(err)
	}

	return &auth.Principal{
		UserID:   authCtx.UserID(),
		Username: pickUsername(authCtx),
		Roles:    extractRoles(authCtx),
	}, nil
}

// mapError translates the Zitadel SDK's error hierarchy into the auth port's
// sentinel errors, so httpx/middleware never needs to import this package or
// the Zitadel SDK to decide a status code.
func mapError(err error) error {
	var unavailable *authorization.ServiceUnavailableErr
	if errors.As(err, &unavailable) {
		// The identity provider is unreachable or failing (5xx) — this must
		// never collapse into ErrUnauthenticated. See FIX 2 in the vogel README.
		return fmt.Errorf("auth/zitadel: %w: %v", auth.ErrServiceUnavailable, err)
	}

	var forbidden *authorization.PermissionDeniedErr
	if errors.As(err, &forbidden) {
		// Unreachable via New/DefaultJWTAuthorization, which performs no role
		// check, but mapped for completeness: CheckAuthorization's documented
		// contract can return this when a CheckOption (e.g. a required role)
		// is configured.
		return fmt.Errorf("auth/zitadel: %w: %v", auth.ErrForbidden, err)
	}

	// authorization.UnauthorizedErr, and any other verifier failure not
	// covered above, mean the token itself is missing, malformed, or invalid.
	return fmt.Errorf("auth/zitadel: %w: %v", auth.ErrUnauthenticated, err)
}

// pickUsername prefers the OIDC-standard preferred_username claim over the
// legacy Username field.
//
// Fixes a bug carried by go-crucible, which read authCtx.Username directly:
// Zitadel populates the standard preferred_username claim (surfaced here via
// oidc.UserInfoProfile, embedded in IntrospectionContext), so authCtx.Username
// was consistently empty for tokens issued by a standards-conformant flow.
// go-licencias already carried this fix; this port generalizes it.
func pickUsername(c *oauth.IntrospectionContext) string {
	if c.PreferredUsername != "" {
		return c.PreferredUsername
	}
	return c.Username
}

// extractRoles reads the Zitadel role claim into a flat slice of role names.
// Zitadel nests each granted role under urn:zitadel:iam:org:project:roles as
// {role: {orgID: orgName}}; only the role names are kept, since org-based
// scoping was deliberately dropped (see the vogel README).
func extractRoles(c *oauth.IntrospectionContext) []string {
	const rolesClaim = "urn:zitadel:iam:org:project:roles"
	rolesMap, ok := c.Claims[rolesClaim].(map[string]any)
	if !ok {
		return nil
	}
	roles := make([]string, 0, len(rolesMap))
	for role := range rolesMap {
		roles = append(roles, role)
	}
	return roles
}
