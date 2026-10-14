package zitadel

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/zitadel-go/v3/pkg/authorization/oauth"
	"github.com/zitadel/zitadel-go/v3/pkg/zitadel"

	"github.com/kafeiih/vogel/auth"
)

const (
	testAPIClientID     = "api-client"
	testAPIClientSecret = "api-secret"
)

// testIntrospector is an in-process OIDC issuer that serves discovery and an
// RFC 7662 introspection endpoint. Each token maps to a canned response; an
// unknown token is reported inactive, as Zitadel does. It counts the
// introspection calls so tests can prove the cache avoids round trips.
type testIntrospector struct {
	srv   *httptest.Server
	calls atomic.Int32

	mu        sync.Mutex
	responses map[string]map[string]any
	status    int           // when non-zero, the introspection endpoint answers with it
	stall     time.Duration // when non-zero, the introspection endpoint waits this long
	gotAuth   []string      // "basic" or "jwt-profile", one per call

	done chan struct{} // closed on cleanup, before the server closes, to release stalled handlers
}

func newTestIntrospector(t *testing.T) *testIntrospector {
	t.Helper()
	ti := &testIntrospector{responses: map[string]map[string]any{}, done: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 ti.srv.URL,
			"token_endpoint":         ti.srv.URL + "/oauth/v2/token",
			"introspection_endpoint": ti.srv.URL + "/oauth/v2/introspect",
		})
	})
	mux.HandleFunc("/oauth/v2/introspect", func(w http.ResponseWriter, r *http.Request) {
		ti.calls.Add(1)
		ti.mu.Lock()
		status, stall := ti.status, ti.stall
		ti.mu.Unlock()
		if stall > 0 {
			select {
			case <-r.Context().Done():
			case <-ti.done:
			case <-time.After(stall):
			}
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		method := ""
		if id, secret, ok := r.BasicAuth(); ok {
			if id != testAPIClientID || secret != testAPIClientSecret {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
				return
			}
			method = "basic"
		} else if r.PostForm.Get("client_assertion") != "" {
			method = "jwt-profile"
		} else {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
			return
		}
		ti.mu.Lock()
		ti.gotAuth = append(ti.gotAuth, method)
		resp, ok := ti.responses[r.PostForm.Get("token")]
		ti.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		if !ok {
			resp = map[string]any{"active": false}
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	ti.srv = httptest.NewServer(mux)
	t.Cleanup(ti.srv.Close)
	// Cleanups run last-in first-out: stalled handlers are released before
	// srv.Close waits for them.
	t.Cleanup(func() { close(ti.done) })
	return ti
}

// activeResponse is a typical Zitadel introspection response for an active
// access token, with a one-hour iat/exp window, merged with extra.
func activeResponse(extra map[string]any) map[string]any {
	now := time.Now()
	resp := map[string]any{
		"active":             true,
		"sub":                "user-1",
		"aud":                []string{testAPIClientID, "spa-client"},
		"client_id":          "spa-client",
		"iat":                now.Unix(),
		"exp":                now.Add(time.Hour).Unix(),
		"preferred_username": "alice",
		"urn:zitadel:iam:org:project:roles": map[string]any{
			"admin": map[string]any{"org-1": "example.org"},
		},
	}
	for k, v := range extra {
		resp[k] = v
	}
	return resp
}

func (ti *testIntrospector) set(token string, resp map[string]any) {
	ti.mu.Lock()
	defer ti.mu.Unlock()
	ti.responses[token] = resp
}

// authenticator builds an Authenticator through the same validation and
// construction path New uses, against this plain-HTTP issuer.
func (ti *testIntrospector) authenticator(t *testing.T, cfg Config) *Authenticator {
	t.Helper()
	if cfg.ClientID == "" {
		cfg.ClientID = testAPIClientID
	}
	if cfg.Introspection == nil {
		cfg.Introspection = &IntrospectionConfig{ClientSecret: testAPIClientSecret}
	}
	cfg.Issuer = ti.srv.URL
	cfg, err := validateConfig(cfg)
	if err != nil {
		t.Fatalf("validateConfig: %v", err)
	}
	u, err := url.Parse(ti.srv.URL)
	if err != nil {
		t.Fatalf("parse issuer URL: %v", err)
	}
	a, err := build(context.Background(), zitadel.New(u.Hostname(), zitadel.WithInsecure(u.Port())), cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return a
}

func TestIntrospection_ActiveToken_ReturnsPrincipal(t *testing.T) {
	ti := newTestIntrospector(t)
	ti.set("tok-active", activeResponse(nil))
	a := ti.authenticator(t, Config{})

	p, err := a.Authenticate(context.Background(), "tok-active")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if p.UserID != "user-1" || p.Username != "alice" || !slices.Equal(p.Roles, []string{"admin"}) {
		t.Errorf("principal = %+v, want user-1/alice/[admin]", p)
	}
	if got := ti.calls.Load(); got != 1 {
		t.Errorf("introspection calls = %d, want 1", got)
	}
	ti.mu.Lock()
	defer ti.mu.Unlock()
	if !slices.Equal(ti.gotAuth, []string{"basic"}) {
		t.Errorf("client authentication = %v, want [basic]", ti.gotAuth)
	}
}

func TestIntrospection_InactiveToken_ReturnsErrUnauthenticated(t *testing.T) {
	ti := newTestIntrospector(t)
	ti.set("tok-revoked", map[string]any{"active": false})
	a := ti.authenticator(t, Config{})

	_, err := a.Authenticate(context.Background(), "tok-revoked")
	if !errors.Is(err, auth.ErrUnauthenticated) || errors.Is(err, auth.ErrServiceUnavailable) {
		t.Fatalf("error = %v, want auth.ErrUnauthenticated only", err)
	}
}

func TestIntrospection_InactiveResultIsNotCached(t *testing.T) {
	ti := newTestIntrospector(t)
	a := ti.authenticator(t, Config{})

	for range 2 {
		if _, err := a.Authenticate(context.Background(), "tok-unknown"); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("error = %v, want auth.ErrUnauthenticated", err)
		}
	}
	if got := ti.calls.Load(); got != 2 {
		t.Errorf("introspection calls = %d, want 2 (inactive results must not be cached)", got)
	}
}

func TestIntrospection_CacheHitAvoidsSecondCall(t *testing.T) {
	ti := newTestIntrospector(t)
	ti.set("tok-active", activeResponse(nil))
	a := ti.authenticator(t, Config{})

	for range 3 {
		if _, err := a.Authenticate(context.Background(), "tok-active"); err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
	}
	if got := ti.calls.Load(); got != 1 {
		t.Errorf("introspection calls = %d, want 1 within the cache TTL", got)
	}
}

func TestIntrospection_CacheExpiryTriggersNewCall(t *testing.T) {
	ti := newTestIntrospector(t)
	ti.set("tok-active", activeResponse(nil))
	a := ti.authenticator(t, Config{Introspection: &IntrospectionConfig{ClientSecret: testAPIClientSecret, CacheTTL: 10 * time.Second}})

	now := time.Now()
	var mu sync.Mutex
	a.cache.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }

	if _, err := a.Authenticate(context.Background(), "tok-active"); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	// The token is revoked in Zitadel; within the TTL the cached result is
	// still used (the documented trade-off).
	ti.set("tok-active", map[string]any{"active": false})
	if _, err := a.Authenticate(context.Background(), "tok-active"); err != nil {
		t.Fatalf("Authenticate within TTL: %v", err)
	}
	if got := ti.calls.Load(); got != 1 {
		t.Fatalf("introspection calls = %d, want 1 within the TTL", got)
	}

	mu.Lock()
	now = now.Add(11 * time.Second)
	mu.Unlock()

	if _, err := a.Authenticate(context.Background(), "tok-active"); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("error after TTL = %v, want auth.ErrUnauthenticated for the revoked token", err)
	}
	if got := ti.calls.Load(); got != 2 {
		t.Errorf("introspection calls = %d, want 2 after the TTL elapsed", got)
	}
}

func TestIntrospection_CacheNeverHoldsRawToken(t *testing.T) {
	ti := newTestIntrospector(t)
	const token = "tok-secret-value"
	ti.set(token, activeResponse(nil))
	a := ti.authenticator(t, Config{})

	for range 2 {
		if _, err := a.Authenticate(context.Background(), token); err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
	}

	a.cache.mu.Lock()
	defer a.cache.mu.Unlock()
	if len(a.cache.entries) != 1 {
		t.Fatalf("cache entries = %d, want 1", len(a.cache.entries))
	}
	for key, el := range a.cache.entries {
		if key != sha256.Sum256([]byte(token)) {
			t.Errorf("cache key is not the SHA-256 of the token")
		}
		if got := el.Value.(*cacheEntry).value.GetToken(); got != "" {
			t.Errorf("cached value holds token %q, want none (the SDK's SetToken must hit a copy)", got)
		}
	}
}

func TestIntrospection_AudienceWithoutClientID_ReturnsErrUnauthenticated(t *testing.T) {
	ti := newTestIntrospector(t)
	ti.set("tok-other-api", activeResponse(map[string]any{"aud": []string{"other-api"}}))
	a := ti.authenticator(t, Config{})

	_, err := a.Authenticate(context.Background(), "tok-other-api")
	if !errors.Is(err, auth.ErrUnauthenticated) || !errors.Is(err, errAudience) {
		t.Fatalf("error = %v, want auth.ErrUnauthenticated wrapping errAudience", err)
	}
}

func TestIntrospection_ClaimChecksStillApply(t *testing.T) {
	ti := newTestIntrospector(t)
	now := time.Now()
	ti.set("tok-long-lived", activeResponse(map[string]any{"iat": now.Unix(), "exp": now.Add(48 * time.Hour).Unix()}))
	ti.set("tok-future", activeResponse(map[string]any{"nbf": now.Add(time.Hour).Unix()}))
	ti.set("tok-other-azp", activeResponse(map[string]any{"azp": "other-client"}))

	tests := []struct {
		name    string
		token   string
		cfg     Config
		wantErr error
	}{
		{name: "lifetime above MaxTokenLifetime", token: "tok-long-lived", cfg: Config{MaxTokenLifetime: 24 * time.Hour}, wantErr: errLifetimeExceeded},
		{name: "nbf in the future", token: "tok-future", wantErr: errNotYetValid},
		{name: "azp outside the allowlist", token: "tok-other-azp", cfg: Config{AllowedAuthorizedParties: []string{"spa-client"}}, wantErr: errAuthorizedParty},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := ti.authenticator(t, tt.cfg)
			_, err := a.Authenticate(context.Background(), tt.token)
			if !errors.Is(err, auth.ErrUnauthenticated) || !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want auth.ErrUnauthenticated wrapping %v", err, tt.wantErr)
			}
		})
	}
}

func TestIntrospection_EndpointFailure_ReturnsErrServiceUnavailable(t *testing.T) {
	tests := []struct {
		name   string
		status int
		secret string
	}{
		{name: "5xx from the introspection endpoint", status: http.StatusBadGateway, secret: testAPIClientSecret},
		{name: "client authentication rejected (misconfiguration)", secret: "wrong-secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ti := newTestIntrospector(t)
			ti.set("tok-active", activeResponse(nil))
			ti.mu.Lock()
			ti.status = tt.status
			ti.mu.Unlock()
			a := ti.authenticator(t, Config{Introspection: &IntrospectionConfig{ClientSecret: tt.secret}})

			_, err := a.Authenticate(context.Background(), "tok-active")
			if !errors.Is(err, auth.ErrServiceUnavailable) {
				t.Fatalf("error = %v, want auth.ErrServiceUnavailable (a failed introspection says nothing about the token)", err)
			}
		})
	}
}

func TestIntrospection_StalledEndpoint_BoundedByHTTPTimeout(t *testing.T) {
	ti := newTestIntrospector(t)
	ti.mu.Lock()
	ti.stall = 5 * time.Second
	ti.mu.Unlock()
	a := ti.authenticator(t, Config{HTTPTimeout: 200 * time.Millisecond})

	start := time.Now()
	_, err := a.Authenticate(context.Background(), "tok-active")
	if !errors.Is(err, auth.ErrServiceUnavailable) {
		t.Fatalf("error = %v, want auth.ErrServiceUnavailable", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Authenticate took %v, want it bounded by the 200ms HTTPTimeout", elapsed)
	}
}

func TestIntrospection_KeyFileUsesJWTProfile(t *testing.T) {
	ti := newTestIntrospector(t)
	ti.set("tok-active", activeResponse(nil))

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	keyJSON, err := json.Marshal(map[string]string{
		"type": "application", "keyId": "key-1", "key": string(pemKey), "appId": "app-1", "clientId": testAPIClientID,
	})
	if err != nil {
		t.Fatalf("marshal key file: %v", err)
	}
	path := filepath.Join(t.TempDir(), "key.json")
	if err := os.WriteFile(path, keyJSON, 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	a := ti.authenticator(t, Config{Introspection: &IntrospectionConfig{KeyFile: path}})
	if _, err := a.Authenticate(context.Background(), "tok-active"); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	ti.mu.Lock()
	defer ti.mu.Unlock()
	if !slices.Equal(ti.gotAuth, []string{"jwt-profile"}) {
		t.Errorf("client authentication = %v, want [jwt-profile]", ti.gotAuth)
	}
}

func TestNew_InvalidIntrospectionConfig_ReturnsError(t *testing.T) {
	tests := []struct {
		name string
		ic   IntrospectionConfig
	}{
		{name: "no credential", ic: IntrospectionConfig{}},
		{name: "both credentials", ic: IntrospectionConfig{KeyFile: "key.json", ClientSecret: "s"}},
		{name: "negative cache TTL", ic: IntrospectionConfig{ClientSecret: "s", CacheTTL: -time.Second}},
		{name: "negative cache max entries", ic: IntrospectionConfig{ClientSecret: "s", CacheMaxEntries: -1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ic := tt.ic
			_, err := New(context.Background(), Config{Issuer: "auth.example.invalid", ClientID: "c", Introspection: &ic})
			if err == nil {
				t.Fatal("expected a construction error, got nil")
			}
		})
	}
}

func TestValidateConfig_IntrospectionDefaults(t *testing.T) {
	ic := &IntrospectionConfig{ClientSecret: "s"}
	cfg, err := validateConfig(Config{Issuer: "auth.example.invalid", ClientID: "c", Introspection: ic})
	if err != nil {
		t.Fatalf("validateConfig: %v", err)
	}
	if cfg.Introspection.CacheTTL != DefaultIntrospectionCacheTTL || cfg.Introspection.CacheMaxEntries != DefaultIntrospectionCacheMaxEntries {
		t.Errorf("defaults = %v/%d, want %v/%d", cfg.Introspection.CacheTTL, cfg.Introspection.CacheMaxEntries,
			DefaultIntrospectionCacheTTL, DefaultIntrospectionCacheMaxEntries)
	}
	if ic.CacheTTL != 0 {
		t.Error("validateConfig mutated the caller's IntrospectionConfig")
	}
	if DefaultIntrospectionCacheTTL != 30*time.Second {
		t.Errorf("DefaultIntrospectionCacheTTL = %v, want 30s", DefaultIntrospectionCacheTTL)
	}
}

func activeContext(exp time.Time) *oauth.IntrospectionContext {
	return &oauth.IntrospectionContext{IntrospectionResponse: oidc.IntrospectionResponse{
		Active: true, Subject: "user-1", Expiration: oidc.FromTime(exp),
	}}
}

func TestIntrospectionCache_TTLCappedByTokenExpiry(t *testing.T) {
	now := time.Now()
	c := newIntrospectionCache(10)
	c.now = func() time.Time { return now }

	c.Set("tok", activeContext(now.Add(5*time.Second)), 30*time.Second)
	if _, ok := c.Get("tok"); !ok {
		t.Fatal("Get before exp = miss, want hit")
	}
	now = now.Add(6 * time.Second)
	if _, ok := c.Get("tok"); ok {
		t.Error("Get after the token's exp = hit, want miss even though the TTL has not elapsed")
	}
}

func TestIntrospectionCache_SkipsInactiveAndExpired(t *testing.T) {
	now := time.Now()
	c := newIntrospectionCache(10)
	c.now = func() time.Time { return now }

	inactive := activeContext(now.Add(time.Hour))
	inactive.Active = false
	c.Set("inactive", inactive, time.Minute)
	c.Set("expired", activeContext(now.Add(-time.Second)), time.Minute)
	c.Set("nil", nil, time.Minute)

	if n := c.Len(); n != 0 {
		t.Errorf("Len = %d, want 0 (only active, unexpired results are cached)", n)
	}
}

func TestIntrospectionCache_SizeBoundEvictsLeastRecentlyUsed(t *testing.T) {
	now := time.Now()
	c := newIntrospectionCache(2)
	c.now = func() time.Time { return now }
	exp := now.Add(time.Hour)

	c.Set("a", activeContext(exp), time.Minute)
	c.Set("b", activeContext(exp), time.Minute)
	if _, ok := c.Get("a"); !ok { // a becomes the most recently used
		t.Fatal("Get(a) = miss, want hit")
	}
	c.Set("c", activeContext(exp), time.Minute)

	if n := c.Len(); n != 2 {
		t.Errorf("Len = %d, want 2", n)
	}
	if _, ok := c.Get("b"); ok {
		t.Error("Get(b) = hit, want b evicted as the least recently used")
	}
	for _, k := range []string{"a", "c"} {
		if _, ok := c.Get(k); !ok {
			t.Errorf("Get(%s) = miss, want hit", k)
		}
	}
}

func TestIntrospectionCache_GetReturnsCopy(t *testing.T) {
	c := newIntrospectionCache(10)
	c.Set("tok", activeContext(time.Now().Add(time.Hour)), time.Minute)

	v1, _ := c.Get("tok")
	v1.SetToken("Bearer tok")
	v1.Subject = "mutated"
	v2, _ := c.Get("tok")
	if v2.GetToken() != "" || v2.Subject != "user-1" {
		t.Errorf("cached value changed through a returned pointer: token=%q subject=%q", v2.GetToken(), v2.Subject)
	}
}

func TestIntrospectionCache_ConcurrentUse(t *testing.T) {
	c := newIntrospectionCache(8)
	exp := time.Now().Add(time.Hour)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok := string(rune('a' + i))
			for range 100 {
				c.Set(tok, activeContext(exp), time.Minute)
				if v, ok := c.Get(tok); ok {
					v.SetToken("Bearer " + tok)
				}
			}
		}()
	}
	wg.Wait()
	if n := c.Len(); n > 8 {
		t.Errorf("Len = %d, want at most 8", n)
	}
}
