package zitadel

import (
	"container/list"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zitadel/oidc/v3/pkg/client/rs"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/zitadel-go/v3/pkg/authorization"
	"github.com/zitadel/zitadel-go/v3/pkg/authorization/oauth"
	"github.com/zitadel/zitadel-go/v3/pkg/zitadel"
	"golang.org/x/sync/singleflight"
)

// IntrospectionConfig switches an Authenticator from local JWT verification
// to OAuth2 token introspection (RFC 7662) against Zitadel, so a revoked
// token or a terminated session is rejected before the token's exp.
//
// The introspection endpoint authenticates the calling API application, so
// exactly one credential must be set: KeyFile (JWT profile, the key.json
// Zitadel generates for the API application) or ClientSecret (basic auth
// with Config.ClientID as the client ID).
//
// Each introspection is a network round trip to Zitadel, so active results
// are cached in memory for CacheTTL (never past the token's own exp). The
// trade-off: a token revoked in Zitadel can still be accepted for at most
// CacheTTL after the revocation.
//
// Concurrent cache misses for the same token share a single introspection
// call, so a burst of requests carrying one new token costs one round trip.
// Only active results are cached, though: every distinct unknown or inactive
// token still costs one call, so an unauthenticated flood of random tokens
// turns into one introspection call per request. Put rate limiting in front
// of the authenticated routes (for example httprate keyed by the client's
// real IP, as examples/boilerplate does) to bound that load on Zitadel.
type IntrospectionConfig struct {
	// KeyFile is the path to the API application's key.json (JWT profile
	// authentication). Mutually exclusive with ClientSecret.
	KeyFile string

	// ClientSecret is the API application's client secret, sent with
	// Config.ClientID as basic auth. Mutually exclusive with KeyFile.
	ClientSecret string

	// CacheTTL bounds how long an active introspection result is reused.
	// Zero means DefaultIntrospectionCacheTTL; negative is a construction
	// error. It is also the maximum time a revoked token stays accepted.
	CacheTTL time.Duration

	// CacheMaxEntries bounds the number of cached results; when full, the
	// least recently used entry is evicted. Zero means
	// DefaultIntrospectionCacheMaxEntries; negative is a construction error.
	CacheMaxEntries int
}

const (
	// DefaultIntrospectionCacheTTL is the cache TTL used when
	// IntrospectionConfig.CacheTTL is zero.
	DefaultIntrospectionCacheTTL = 30 * time.Second

	// DefaultIntrospectionCacheMaxEntries is the cache size used when
	// IntrospectionConfig.CacheMaxEntries is zero.
	DefaultIntrospectionCacheMaxEntries = 10000
)

// errAudience is wrapped (under auth.ErrUnauthenticated) when an active
// introspection result does not list Config.ClientID in aud. Local JWT
// verification (oauth.JWTVerification) enforces the same rule, so switching
// to introspection does not widen which tokens are accepted.
var errAudience = errors.New("token audience does not include the client ID")

// validateIntrospection checks ic and returns it with defaults applied.
func validateIntrospection(ic IntrospectionConfig) (IntrospectionConfig, error) {
	if (ic.KeyFile == "") == (ic.ClientSecret == "") {
		return ic, fmt.Errorf("auth/zitadel: introspection needs exactly one of KeyFile or ClientSecret")
	}
	if ic.CacheTTL < 0 {
		return ic, fmt.Errorf("auth/zitadel: introspection cache TTL must not be negative")
	}
	if ic.CacheMaxEntries < 0 {
		return ic, fmt.Errorf("auth/zitadel: introspection cache max entries must not be negative")
	}
	if ic.CacheTTL == 0 {
		ic.CacheTTL = DefaultIntrospectionCacheTTL
	}
	if ic.CacheMaxEntries == 0 {
		ic.CacheMaxEntries = DefaultIntrospectionCacheMaxEntries
	}
	return ic, nil
}

// withIntrospection mirrors the SDK's oauth.WithIntrospection initializer
// (same issuer, zitadel.Origin()), but builds the resource server with
// rs.WithClient so discovery and every introspection call use the bounded
// httpClient (the SDK's IntrospectionAuthentication helpers take no options
// and fall back to the oidc package's 30s default client), and verifies
// tokens with introspectionVerifier, which adds this package's token cache
// and merges concurrent misses.
func withIntrospection(ic IntrospectionConfig, clientID string, httpClient *http.Client, cache *introspectionCache) authorization.VerifierInitializer[*oauth.IntrospectionContext] {
	return func(ctx context.Context, z *zitadel.Zitadel) (authorization.Verifier[*oauth.IntrospectionContext], error) {
		var (
			server rs.ResourceServer
			err    error
		)
		if ic.KeyFile != "" {
			server, err = rs.NewResourceServerFromKeyFile(ctx, z.Origin(), ic.KeyFile, rs.WithClient(httpClient))
		} else {
			server, err = rs.NewResourceServerClientCredentials(ctx, z.Origin(), clientID, ic.ClientSecret, rs.WithClient(httpClient))
		}
		if err != nil {
			return nil, fmt.Errorf("introspection resource server: %w", err)
		}
		if server.IntrospectionURL() == "" {
			return nil, errors.New("introspection resource server: issuer discovery has no introspection_endpoint")
		}
		return &introspectionVerifier{server: server, cache: cache, ttl: ic.CacheTTL}, nil
	}
}

// introspectionVerifier implements authorization.Verifier with OAuth2
// introspection. It behaves like the SDK's
// oauth.IntrospectionVerificationWithCache (same header parsing, same
// oauth.ErrIntrospectionFailed wrapping, active results cached for ttl), and
// additionally merges concurrent cache misses for the same token into one
// introspection call (singleflight keyed by the token's SHA-256, never the
// raw token).
//
// The shared call runs detached from any single caller's cancellation
// (context.WithoutCancel; the bounded HTTP client still caps it), so one
// caller giving up does not fail the others waiting on it. Each caller
// waits with its own context: a cancelled caller returns its context error
// at once, wrapped in oauth.ErrIntrospectionFailed (an aborted check says
// nothing about the token), while the shared call carries on.
//
// Every caller gets its own shallow copy of the result, because the SDK's
// Authorizer calls SetToken on the context it returns. The copy is shallow:
// reference-typed fields of the introspection response (the audience slice,
// the claims map) stay shared between callers and the cached entry, so code
// on this path must only read them, as checkAudience and claimsOf do.
type introspectionVerifier struct {
	server rs.ResourceServer
	cache  *introspectionCache
	ttl    time.Duration
	group  singleflight.Group
}

var _ authorization.Verifier[*oauth.IntrospectionContext] = (*introspectionVerifier)(nil)

// CheckAuthorization implements authorization.Verifier. authorizationToken
// must start with the "Bearer" scheme word; surrounding whitespace around
// the token is trimmed. This is exactly the parsing the SDK's own verifiers
// use (strings.CutPrefix with oidc.BearerToken, which has no trailing
// space), so switching to introspection does not change which headers are
// accepted. Authenticate always passes "Bearer <token>".
func (v *introspectionVerifier) CheckAuthorization(ctx context.Context, authorizationToken string) (*oauth.IntrospectionContext, error) {
	token, ok := strings.CutPrefix(authorizationToken, oidc.BearerToken)
	if !ok {
		return nil, oauth.ErrInvalidAuthorizationHeader
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, oauth.ErrInvalidAuthorizationHeader
	}
	if cached, found := v.cache.Get(token); found {
		return cached, nil
	}

	key := cacheKey(token)
	callCtx := context.WithoutCancel(ctx)
	results := v.group.DoChan(string(key[:]), func() (any, error) {
		// A caller that missed the cache just before a previous call for
		// this token stored its result lands here after that call ended.
		if cached, found := v.cache.Get(token); found {
			return cached, nil
		}
		resp, err := rs.Introspect[*oauth.IntrospectionContext](callCtx, v.server, token)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", oauth.ErrIntrospectionFailed, err)
		}
		v.cache.Set(token, resp, v.ttl)
		return resp, nil
	})
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", oauth.ErrIntrospectionFailed, ctx.Err())
	case r := <-results:
		if r.Err != nil {
			return nil, r.Err
		}
		c := *r.Val.(*oauth.IntrospectionContext)
		return &c, nil
	}
}

// introspectionCache implements oauth.TokenCache for introspection results.
//
//   - Keys are the SHA-256 of the token, so the raw bearer token is never
//     held in memory by the cache.
//   - Only active results are stored, and an entry never outlives the
//     token's own exp, whatever TTL the caller passes.
//   - The cache holds at most maxEntries entries and evicts the least
//     recently used one when full.
//   - Values are copied on Set and on Get: the SDK's Authorizer calls
//     SetToken on the context it returns, so handing out the stored pointer
//     would both race between concurrent requests and leave the raw token
//     inside the cached value.
//
// It is safe for concurrent use.
type introspectionCache struct {
	mu         sync.Mutex
	maxEntries int
	now        func() time.Time
	entries    map[[sha256.Size]byte]*list.Element
	lru        *list.List // front is the most recently used
}

type cacheEntry struct {
	key     [sha256.Size]byte
	value   oauth.IntrospectionContext
	expires time.Time
}

var _ oauth.TokenCache[*oauth.IntrospectionContext] = (*introspectionCache)(nil)

func newIntrospectionCache(maxEntries int) *introspectionCache {
	return &introspectionCache{
		maxEntries: maxEntries,
		now:        time.Now,
		entries:    make(map[[sha256.Size]byte]*list.Element),
		lru:        list.New(),
	}
}

func cacheKey(token string) [sha256.Size]byte {
	return sha256.Sum256([]byte(token))
}

// Get implements oauth.TokenCache.
func (c *introspectionCache) Get(token string) (*oauth.IntrospectionContext, bool) {
	key := cacheKey(token)
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	e := entryOf(el)
	if !c.now().Before(e.expires) {
		c.remove(el)
		return nil, false
	}
	c.lru.MoveToFront(el)
	v := e.value
	return &v, true
}

// Set implements oauth.TokenCache. An inactive (or nil) result is never
// stored, and the TTL is capped by the token's exp.
func (c *introspectionCache) Set(token string, value *oauth.IntrospectionContext, ttl time.Duration) {
	if value == nil || !value.Active || ttl <= 0 {
		return
	}
	now := c.now()
	expires := now.Add(ttl)
	if exp := value.Expiration.AsTime(); !exp.IsZero() && exp.Before(expires) {
		expires = exp
	}
	if !expires.After(now) {
		return
	}

	key := cacheKey(token)
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		e := entryOf(el)
		e.value = *value
		e.expires = expires
		c.lru.MoveToFront(el)
		return
	}
	c.entries[key] = c.lru.PushFront(&cacheEntry{key: key, value: *value, expires: expires})
	for c.lru.Len() > c.maxEntries {
		c.remove(c.lru.Back())
	}
}

// Len returns the number of cached entries, expired ones included until
// they are looked up or evicted.
func (c *introspectionCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len()
}

func (c *introspectionCache) remove(el *list.Element) {
	c.lru.Remove(el)
	delete(c.entries, entryOf(el).key)
}

// entryOf returns the entry el holds. The LRU list only ever holds
// *cacheEntry, so any other value is a bug in this file.
func entryOf(el *list.Element) *cacheEntry {
	e, ok := el.Value.(*cacheEntry)
	if !ok {
		panic(fmt.Sprintf("zitadel: introspection cache element holds %T, want *cacheEntry", el.Value))
	}
	return e
}

// checkAudience rejects an introspection result whose aud does not include
// clientID.
func checkAudience(c *oauth.IntrospectionContext, clientID string) error {
	if !slices.Contains([]string(c.Audience), clientID) {
		return fmt.Errorf("%w: %w", errClaimsRejected, errAudience)
	}
	return nil
}
