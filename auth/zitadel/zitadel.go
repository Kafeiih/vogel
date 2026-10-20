// Package zitadel adapts github.com/zitadel/zitadel-go/v3's OIDC authorizer
// to the auth.Authenticator port.
package zitadel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
	"github.com/zitadel/zitadel-go/v3/pkg/authorization"
	"github.com/zitadel/zitadel-go/v3/pkg/authorization/oauth"
	"github.com/zitadel/zitadel-go/v3/pkg/zitadel"

	"github.com/kafeiih/vogel/auth"
)

// Config holds the settings needed to construct an Authenticator backed by Zitadel.
type Config struct {
	// Issuer is the Zitadel instance issuer URL, e.g.
	// "https://my-instance.zitadel.cloud" or, with a port,
	// "https://auth.example.com:8443". It must not have a path, query,
	// fragment or user info. An "http://" URL connects without TLS, so it
	// is accepted only for a loopback host ("localhost", 127.0.0.1, ::1),
	// for local development. A bare host
	// ("my-instance.zitadel.cloud") is also accepted and means https.
	Issuer   string
	ClientID string // OIDC client/application ID used for audience validation

	// HTTPTimeout bounds each request of OIDC discovery, of the JWKS
	// downloads (on construction and whenever an unknown key ID forces a
	// refresh) and, with Introspection set, of every introspection call.
	// Zero means DefaultHTTPTimeout; negative is a construction error.
	// Without it the SDK falls back to http.DefaultClient, which has no
	// timeout, so a stalled issuer could hang token validation.
	HTTPTimeout time.Duration

	// WrapTransport, if set, wraps the http.RoundTripper of every call to the
	// issuer (discovery, JWKS, introspection); it receives
	// http.DefaultTransport. The HTTPTimeout still applies.
	// otelhttp.NewTransport fits it, to trace those calls.
	WrapTransport func(http.RoundTripper) http.RoundTripper

	// MaxTokenLifetime, when positive, rejects tokens whose lifetime
	// (exp - iat) exceeds it, and tokens without an iat claim. Zero disables
	// the check; negative is a construction error. It bounds how long a
	// leaked token stays usable even if the issuer is configured to mint
	// long-lived ones.
	MaxTokenLifetime time.Duration

	// AllowedAuthorizedParties, when non-empty, is the list of client IDs
	// whose tokens are accepted: a token whose azp is missing, empty or not
	// in the list is rejected. Empty (the default) skips the check, because
	// for an access token azp names the client that requested it — in the
	// usual Zitadel setup a frontend app, not this API (ClientID) — so
	// comparing it with ClientID would reject every such request. Audience
	// validation against ClientID applies either way. An empty entry is a
	// construction error.
	AllowedAuthorizedParties []string

	// Introspection, when set, validates every token with Zitadel's OAuth2
	// introspection endpoint (RFC 7662) instead of verifying the JWT locally,
	// so a revoked token or a terminated session stops being accepted
	// without waiting for its exp. Nil (the default) keeps local JWT
	// verification. See IntrospectionConfig for the credential it needs and
	// for the cache that bounds its cost.
	Introspection *IntrospectionConfig
}

// DefaultHTTPTimeout is the per-request timeout used when Config.HTTPTimeout
// is zero, for every call to the issuer: OIDC discovery, JWKS fetches and,
// in introspection mode, introspection calls.
const DefaultHTTPTimeout = 10 * time.Second

// issuerHTTPClient returns the bounded client used for every call to the
// issuer: OIDC discovery, JWKS fetches and introspection calls.
func issuerHTTPClient(cfg Config) *http.Client {
	timeout := cfg.HTTPTimeout
	if timeout == 0 {
		timeout = DefaultHTTPTimeout
	}
	c := &http.Client{Timeout: timeout}
	if cfg.WrapTransport != nil {
		c.Transport = cfg.WrapTransport(http.DefaultTransport)
	}
	return c
}

// Authenticator implements auth.Authenticator. By default it uses Zitadel's
// local JWT verification: the instance's public keys (JWKS) are downloaded
// once on construction and cached in memory for subsequent requests. With
// Config.Introspection set it asks Zitadel's introspection endpoint instead
// (with a short in-memory cache of active results).
//
// The concrete generic type authorization.Authorizer[*oauth.IntrospectionContext]
// is deliberately kept private to this package: it must never appear in the
// auth port or in httpx/middleware, or importing the port would pull in the
// Zitadel SDK transitively — defeating the whole point of the port/adapter
// split (see the vogel README).
type Authenticator struct {
	authorizer               *authorization.Authorizer[*oauth.IntrospectionContext]
	allowedAuthorizedParties []string
	maxTokenLifetime         time.Duration

	// Introspection mode only: the audience every result must carry, and
	// the result cache. cache is non-nil exactly in introspection mode, so it
	// is the single source of truth for the mode (see introspectionMode).
	clientID string
	cache    *introspectionCache
}

// introspectionMode reports whether a validates tokens by introspection.
func (a *Authenticator) introspectionMode() bool { return a.cache != nil }

// New creates an Authenticator that validates tokens issued by the Zitadel
// instance at cfg.Issuer for the audience cfg.ClientID.
func New(ctx context.Context, cfg Config) (*Authenticator, error) {
	cfg, err := validateConfig(cfg)
	if err != nil {
		return nil, err
	}
	z, err := issuerProvider(cfg.Issuer)
	if err != nil {
		return nil, err
	}
	return build(ctx, z, cfg)
}

// issuerProvider returns the zitadel.Zitadel for issuer. zitadel.New takes a
// bare host and always prepends "https://", so an issuer URL is split into
// its host and port here: an https port goes to WithPort, and an http URL to
// WithInsecure (port 80 when it has none; loopback hosts only). A value
// without "://" is passed to zitadel.New unchanged, as before.
func issuerProvider(issuer string) (*zitadel.Zitadel, error) {
	if !strings.Contains(issuer, "://") {
		return zitadel.New(issuer), nil
	}
	u, err := url.Parse(issuer)
	if err != nil {
		return nil, fmt.Errorf("auth/zitadel: invalid issuer %q: %w", issuer, err)
	}
	switch {
	case u.Scheme != "https" && u.Scheme != "http":
		return nil, fmt.Errorf("auth/zitadel: issuer %q: scheme must be \"https\" or \"http\"", issuer)
	case u.Hostname() == "":
		return nil, fmt.Errorf("auth/zitadel: issuer %q has no host", issuer)
	case u.User != nil:
		return nil, fmt.Errorf("auth/zitadel: issuer %q must not have user info", issuer)
	case u.Path != "" && u.Path != "/":
		return nil, fmt.Errorf("auth/zitadel: issuer %q must not have a path", issuer)
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return nil, fmt.Errorf("auth/zitadel: issuer %q must not have a query or fragment", issuer)
	}
	// Over plain http a network attacker could serve a forged JWKS and mint
	// tokens Authenticate accepts, so http is kept to a local issuer.
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("auth/zitadel: issuer %q: http is only allowed for a loopback host; use https", issuer)
	}

	// Hostname drops the brackets of an IPv6 literal, which zitadel.New needs
	// to build a parseable origin once it appends the port.
	host := u.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	hasPort := u.Port() != ""
	var port uint64
	if hasPort {
		port, err = strconv.ParseUint(u.Port(), 10, 16)
		if err != nil {
			return nil, fmt.Errorf("auth/zitadel: invalid issuer %q: port: %w", issuer, err)
		}
		if port == 0 {
			return nil, fmt.Errorf("auth/zitadel: issuer %q: port must be between 1 and 65535", issuer)
		}
	}

	switch {
	case u.Scheme == "http" && !hasPort:
		return zitadel.New(host, zitadel.WithInsecure("80")), nil
	case u.Scheme == "http":
		return zitadel.New(host, zitadel.WithInsecure(strconv.FormatUint(port, 10))), nil
	case !hasPort:
		return zitadel.New(host), nil
	default:
		return zitadel.New(host, zitadel.WithPort(uint16(port))), nil
	}
}

// isLoopbackHost reports whether host is "localhost" or a loopback IP
// address.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// validateConfig rejects an invalid cfg and returns it with the
// introspection defaults applied, on a copy: the caller's
// IntrospectionConfig is never modified.
func validateConfig(cfg Config) (Config, error) {
	if cfg.Issuer == "" {
		return cfg, fmt.Errorf("auth/zitadel: issuer is required")
	}
	if cfg.ClientID == "" {
		return cfg, fmt.Errorf("auth/zitadel: client ID is required")
	}
	if cfg.HTTPTimeout < 0 {
		return cfg, fmt.Errorf("auth/zitadel: HTTP timeout must not be negative")
	}
	if cfg.MaxTokenLifetime < 0 {
		return cfg, fmt.Errorf("auth/zitadel: max token lifetime must not be negative")
	}
	if slices.Contains(cfg.AllowedAuthorizedParties, "") {
		return cfg, fmt.Errorf("auth/zitadel: allowed authorized parties must not contain an empty client ID")
	}
	if cfg.Introspection != nil {
		ic, err := validateIntrospection(*cfg.Introspection)
		if err != nil {
			return cfg, err
		}
		cfg.Introspection = &ic
	}
	return cfg, nil
}

// acceptedSigningAlgorithms returns the only JWS algorithms the local JWT
// verifier accepts, as a fresh slice so no caller can alter the allowlist.
// They equal zitadel/oidc's current default (used when no list is given),
// pinned here so an SDK update cannot widen them silently; "none" and the
// HMAC algorithms are never accepted, which rules out the
// algorithm-confusion attack of an HS256 token keyed with the issuer's
// public key.
func acceptedSigningAlgorithms() []string {
	return []string{"RS256", "ES256", "PS256"}
}

// withJWT is the local JWT verifier accepting only algs; build passes
// acceptedSigningAlgorithms(). oauth.WithJWT is what DefaultJWTAuthorization
// wraps, with a nil client that it replaces by http.DefaultClient; pass a
// bounded one instead.
func withJWT(clientID string, httpClient *http.Client, algs []string) authorization.VerifierInitializer[*oauth.IntrospectionContext] {
	return oauth.WithJWT(clientID, httpClient, op.WithSupportedAccessTokenSigningAlgorithms(algs...))
}

// build constructs the Authenticator for an already validated cfg against z.
// New passes issuerProvider(cfg.Issuer); this package's tests pass a plain-HTTP
// zitadel.Zitadel pointed at an httptest issuer.
func build(ctx context.Context, z *zitadel.Zitadel, cfg Config) (*Authenticator, error) {
	httpClient := issuerHTTPClient(cfg)
	var (
		initVerifier authorization.VerifierInitializer[*oauth.IntrospectionContext]
		cache        *introspectionCache
	)
	if cfg.Introspection != nil {
		cache = newIntrospectionCache(cfg.Introspection.CacheMaxEntries)
		initVerifier = withIntrospection(*cfg.Introspection, cfg.ClientID, httpClient, cache)
	} else {
		initVerifier = withJWT(cfg.ClientID, httpClient, acceptedSigningAlgorithms())
	}
	authorizer, err := authorization.New(ctx, z, initVerifier)
	if err != nil {
		return nil, fmt.Errorf("auth/zitadel: creating authorizer: %w", err)
	}

	return newFromAuthorizer(authorizer, cfg, cache), nil
}

// newFromAuthorizer builds an Authenticator around an already-constructed
// authorizer, taking the claim-check settings (AllowedAuthorizedParties,
// MaxTokenLifetime) from cfg. cache is the introspection result cache, nil
// with local JWT verification; it switches on introspection mode.
// Unexported: it exists so this package's own tests can inject a
// fake authorization.Verifier and exercise the real error-mapping path
// without a network call or a real Zitadel instance (see zitadel_test.go),
// while keeping authorization.Authorizer[*oauth.IntrospectionContext] out of
// the exported constructor surface.
func newFromAuthorizer(a *authorization.Authorizer[*oauth.IntrospectionContext], cfg Config, cache *introspectionCache) *Authenticator {
	return &Authenticator{
		authorizer:               a,
		allowedAuthorizedParties: slices.Clone(cfg.AllowedAuthorizedParties),
		maxTokenLifetime:         cfg.MaxTokenLifetime,
		clientID:                 cfg.ClientID,
		cache:                    cache,
	}
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
//
// After the SDK accepts the token, checkClaims rejects a non-string azp, an
// azp outside Config.AllowedAuthorizedParties (when that list is set), a
// token whose nbf or iat is still in the future, and (when
// Config.MaxTokenLifetime is set) a token whose lifetime exceeds it; those
// rejections wrap auth.ErrUnauthenticated like any other invalid token. In
// introspection mode the same checks run on the introspection result, plus
// an audience check (aud must include Config.ClientID) that local JWT
// verification already performs inside the SDK; an inactive token is the
// SDK's generic unauthorized error.
func (a *Authenticator) Authenticate(ctx context.Context, token string) (*auth.Principal, error) {
	authCtx, err := a.authorizer.CheckAuthorization(ctx, oidc.PrefixBearer+token)
	if err != nil {
		return nil, mapError(err)
	}
	if err := checkClaims(claimsOf(authCtx), a.allowedAuthorizedParties, a.maxTokenLifetime, time.Now()); err != nil {
		return nil, fmt.Errorf("auth/zitadel: %w: %w", auth.ErrUnauthenticated, err)
	}
	if a.introspectionMode() {
		if err := checkAudience(authCtx, a.clientID); err != nil {
			return nil, fmt.Errorf("auth/zitadel: %w: %w", auth.ErrUnauthenticated, err)
		}
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
		return fmt.Errorf("auth/zitadel: %w: %w", auth.ErrServiceUnavailable, err)
	}

	if errors.Is(err, oauth.ErrIntrospectionFailed) {
		// The introspection call itself failed (transport error, timeout,
		// or Zitadel rejecting this API's client credentials). An inactive
		// token is a successful call with active=false, so this failure
		// says nothing about the token: it is an outage or a
		// misconfiguration, not a reason to answer 401.
		return fmt.Errorf("auth/zitadel: %w: %w", auth.ErrServiceUnavailable, err)
	}

	var forbidden *authorization.PermissionDeniedErr
	if errors.As(err, &forbidden) {
		// Unreachable via New/DefaultJWTAuthorization, which performs no role
		// check, but mapped for completeness: CheckAuthorization's documented
		// contract can return this when a CheckOption (e.g. a required role)
		// is configured.
		return fmt.Errorf("auth/zitadel: %w: %w", auth.ErrForbidden, err)
	}

	// authorization.UnauthorizedErr, and any other verifier failure not
	// covered above, mean the token itself is missing, malformed, or invalid.
	return fmt.Errorf("auth/zitadel: %w: %w", auth.ErrUnauthenticated, err)
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
