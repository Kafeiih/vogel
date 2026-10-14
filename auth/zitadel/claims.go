package zitadel

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/zitadel/zitadel-go/v3/pkg/authorization/oauth"
)

// The SDK's local JWT verifier (oauth.WithJWT → op.VerifyAccessToken in
// github.com/zitadel/oidc/v3/pkg/op/verifier_access_token.go) checks the
// issuer, the signature and exp, and oauth.JWTVerification then checks aud.
// It does not check azp or nbf, and has no notion of a maximum lifetime, so
// checkClaims adds those after the SDK has accepted the token. The azp check
// is opt-in (Config.AllowedAuthorizedParties): the OIDC rule "azp equals the
// client ID" is for ID tokens at the relying party; an access token presented
// to a resource server carries the azp of the client that requested it.
//
// Every rejection wraps errClaimsRejected plus one specific sentinel; the
// Authenticator wraps both under auth.ErrUnauthenticated, so clients see the
// same generic 401 as for any other invalid token.
var (
	errClaimsRejected   = errors.New("token claims rejected")
	errAuthorizedParty  = errors.New("azp is not an allowed authorized party")
	errNotYetValid      = errors.New("token not valid yet (nbf)")
	errLifetimeExceeded = errors.New("token lifetime exceeds the configured maximum")
)

// notBeforeLeeway tolerates clock skew between Zitadel and this service when
// checking nbf (Zitadel sets nbf to the issue time). One minute matches
// go-jose's jwt.DefaultLeeway.
const notBeforeLeeway = time.Minute

// tokenClaims holds the verified claim values checkClaims needs, decoupled
// from the SDK types so the checks are a pure function.
type tokenClaims struct {
	authorizedParty          string
	hasAuthorizedParty       bool // the azp key is present in the token
	authorizedPartyMalformed bool // azp is present but not a JSON string
	issuedAt                 time.Time
	notBefore                time.Time
	expiration               time.Time
}

// claimsOf extracts the claim values from the SDK's verified context. The
// typed fields come straight from the JWT (oauth.JWTVerification copies
// iat, nbf and exp); azp has no typed field in IntrospectionResponse, but
// AccessTokenClaims.UnmarshalJSON also decodes the whole payload into the
// Claims map, so it is read from there.
func claimsOf(c *oauth.IntrospectionContext) tokenClaims {
	tc := tokenClaims{
		issuedAt:   c.IssuedAt.AsTime(),
		notBefore:  c.NotBefore.AsTime(),
		expiration: c.Expiration.AsTime(),
	}
	if raw, ok := c.Claims["azp"]; ok {
		tc.hasAuthorizedParty = true
		if s, isString := raw.(string); isString {
			tc.authorizedParty = s
		} else {
			tc.authorizedPartyMalformed = true
		}
	}
	return tc
}

// checkClaims applies the claim checks the SDK leaves out:
//
//   - azp, when present, must be a string (otherwise the token is malformed);
//   - when allowedParties is non-empty, azp must be present, non-empty and
//     one of them; when it is empty, the azp value is not checked;
//   - nbf, when present, must not be later than now plus notBeforeLeeway;
//   - when maxLifetime is positive, exp - iat must not exceed it, and a token
//     without iat or exp is rejected because its lifetime cannot be proven.
func checkClaims(c tokenClaims, allowedParties []string, maxLifetime time.Duration, now time.Time) error {
	if c.authorizedPartyMalformed {
		return fmt.Errorf("%w: %w", errClaimsRejected, errAuthorizedParty)
	}
	if len(allowedParties) > 0 && (c.authorizedParty == "" || !slices.Contains(allowedParties, c.authorizedParty)) {
		return fmt.Errorf("%w: %w", errClaimsRejected, errAuthorizedParty)
	}
	if !c.notBefore.IsZero() && c.notBefore.After(now.Add(notBeforeLeeway)) {
		return fmt.Errorf("%w: %w", errClaimsRejected, errNotYetValid)
	}
	if maxLifetime > 0 {
		if c.issuedAt.IsZero() || c.expiration.IsZero() || c.expiration.Sub(c.issuedAt) > maxLifetime {
			return fmt.Errorf("%w: %w", errClaimsRejected, errLifetimeExceeded)
		}
	}
	return nil
}
