package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"testing"
)

func mustPrefixes(t *testing.T, s string) []netip.Prefix {
	t.Helper()
	p, err := ParseTrustedProxies(s)
	if err != nil {
		t.Fatalf("ParseTrustedProxies(%q): %v", s, err)
	}
	return p
}

func TestRealIP(t *testing.T) {
	const proxies = "10.0.0.0/8, 192.168.1.1, fd00::/8"

	tests := []struct {
		name       string
		trusted    string
		remoteAddr string
		headers    map[string][]string
		want       string
	}{
		{
			name:       "untrusted peer with spoofed headers is left unchanged",
			trusted:    proxies,
			remoteAddr: "203.0.113.7:4242",
			headers: map[string][]string{
				"X-Forwarded-For": {"1.2.3.4"},
				"X-Real-Ip":       {"1.2.3.4"},
				"True-Client-Ip":  {"1.2.3.4"},
			},
			want: "203.0.113.7:4242",
		},
		{
			name:       "no trusted prefixes never trusts headers",
			trusted:    "",
			remoteAddr: "10.0.0.1:4242",
			headers:    map[string][]string{"X-Forwarded-For": {"1.2.3.4"}},
			want:       "10.0.0.1:4242",
		},
		{
			name:       "trusted peer with single XFF entry",
			trusted:    proxies,
			remoteAddr: "10.0.0.1:4242",
			headers:    map[string][]string{"X-Forwarded-For": {"198.51.100.9"}},
			want:       "198.51.100.9",
		},
		{
			name:       "trusted peer takes rightmost untrusted across multiple proxies",
			trusted:    proxies,
			remoteAddr: "10.0.0.1:4242",
			headers: map[string][]string{
				"X-Forwarded-For": {"6.6.6.6, 198.51.100.9, 192.168.1.1, 10.1.2.3"},
			},
			want: "198.51.100.9",
		},
		{
			name:       "multiple XFF header lines are walked as one list",
			trusted:    proxies,
			remoteAddr: "10.0.0.1:4242",
			headers: map[string][]string{
				"X-Forwarded-For": {"6.6.6.6, 198.51.100.9", "10.1.2.3"},
			},
			want: "198.51.100.9",
		},
		{
			name:       "all-trusted chain resolves to the leftmost entry",
			trusted:    proxies,
			remoteAddr: "10.0.0.1:4242",
			headers:    map[string][]string{"X-Forwarded-For": {"10.9.9.9, 10.1.2.3"}},
			want:       "10.9.9.9",
		},
		{
			name:       "invalid rightmost entry leaves RemoteAddr unchanged",
			trusted:    proxies,
			remoteAddr: "10.0.0.1:4242",
			headers:    map[string][]string{"X-Forwarded-For": {"198.51.100.9, not-an-ip"}},
			want:       "10.0.0.1:4242",
		},
		{
			name:       "invalid entry reached during the walk stops it",
			trusted:    proxies,
			remoteAddr: "10.0.0.1:4242",
			headers:    map[string][]string{"X-Forwarded-For": {"198.51.100.9, bogus, 10.1.2.3"}},
			want:       "10.0.0.1:4242",
		},
		{
			name:       "empty XFF entry is invalid",
			trusted:    proxies,
			remoteAddr: "10.0.0.1:4242",
			headers:    map[string][]string{"X-Forwarded-For": {"198.51.100.9,,10.1.2.3"}},
			want:       "10.0.0.1:4242",
		},
		{
			name:       "untrusted entry left of the client is ignored",
			trusted:    proxies,
			remoteAddr: "10.0.0.1:4242",
			headers:    map[string][]string{"X-Forwarded-For": {"garbage, 198.51.100.9"}},
			want:       "198.51.100.9",
		},
		{
			name:       "XFF entry with port is accepted",
			trusted:    proxies,
			remoteAddr: "10.0.0.1:4242",
			headers:    map[string][]string{"X-Forwarded-For": {"198.51.100.9:5555"}},
			want:       "198.51.100.9",
		},
		{
			name:       "IPv6 client behind IPv6 trusted proxy",
			trusted:    proxies,
			remoteAddr: "[fd00::1]:4242",
			headers:    map[string][]string{"X-Forwarded-For": {"2001:db8::1, fd00::2"}},
			want:       "2001:db8::1",
		},
		{
			name:       "bracketed IPv6 entry with port and zone is normalized",
			trusted:    proxies,
			remoteAddr: "10.0.0.1:4242",
			headers:    map[string][]string{"X-Forwarded-For": {"[2001:db8::1%eth0]:443"}},
			want:       "2001:db8::1",
		},
		{
			name:       "IPv4-mapped IPv6 peer matches an IPv4 prefix",
			trusted:    proxies,
			remoteAddr: "[::ffff:10.0.0.1]:4242",
			headers:    map[string][]string{"X-Forwarded-For": {"198.51.100.9"}},
			want:       "198.51.100.9",
		},
		{
			name:       "IPv4-mapped XFF entry is unmapped and checked against IPv4 prefixes",
			trusted:    proxies,
			remoteAddr: "10.0.0.1:4242",
			headers:    map[string][]string{"X-Forwarded-For": {"::ffff:198.51.100.9, ::ffff:10.1.2.3"}},
			want:       "198.51.100.9",
		},
		{
			name:       "X-Real-IP fallback when XFF is absent",
			trusted:    proxies,
			remoteAddr: "10.0.0.1:4242",
			headers:    map[string][]string{"X-Real-Ip": {"198.51.100.9"}},
			want:       "198.51.100.9",
		},
		{
			name:       "X-Real-IP is ignored when XFF is present",
			trusted:    proxies,
			remoteAddr: "10.0.0.1:4242",
			headers: map[string][]string{
				"X-Forwarded-For": {"198.51.100.9"},
				"X-Real-Ip":       {"6.6.6.6"},
			},
			want: "198.51.100.9",
		},
		{
			name:       "invalid X-Real-IP leaves RemoteAddr unchanged",
			trusted:    proxies,
			remoteAddr: "10.0.0.1:4242",
			headers:    map[string][]string{"X-Real-Ip": {"nope"}},
			want:       "10.0.0.1:4242",
		},
		{
			name:       "True-Client-IP is never consulted",
			trusted:    proxies,
			remoteAddr: "10.0.0.1:4242",
			headers:    map[string][]string{"True-Client-Ip": {"198.51.100.9"}},
			want:       "10.0.0.1:4242",
		},
		{
			name:       "trusted peer without forwarding headers is unchanged",
			trusted:    proxies,
			remoteAddr: "10.0.0.1:4242",
			want:       "10.0.0.1:4242",
		},
		{
			name:       "unparseable RemoteAddr is unchanged",
			trusted:    proxies,
			remoteAddr: "@unix",
			headers:    map[string][]string{"X-Forwarded-For": {"198.51.100.9"}},
			want:       "@unix",
		},
		{
			name:       "single IP prefix only trusts that exact IP",
			trusted:    "192.168.1.1",
			remoteAddr: "192.168.1.2:4242",
			headers:    map[string][]string{"X-Forwarded-For": {"198.51.100.9"}},
			want:       "192.168.1.2:4242",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			h := RealIP(mustPrefixes(t, tt.trusted)...)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = r.RemoteAddr
			}))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			for k, vs := range tt.headers {
				for _, v := range vs {
					req.Header.Add(k, v)
				}
			}
			h.ServeHTTP(httptest.NewRecorder(), req)

			if got != tt.want {
				t.Errorf("RemoteAddr = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRealIP_FeedsRequestContext(t *testing.T) {
	var ip string
	h := RealIP(mustPrefixes(t, "10.0.0.0/8")...)(RequestContext(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		ip = r.RemoteAddr
	})))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:4242"
	req.Header.Set("X-Forwarded-For", "2001:db8::1")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if ip != "2001:db8::1" {
		t.Errorf("RemoteAddr = %q, want %q", ip, "2001:db8::1")
	}
}

func TestParseTrustedProxies(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{name: "empty string yields no prefixes", in: "", want: nil},
		{name: "whitespace and empty items are skipped", in: " , ,", want: nil},
		{name: "CIDR list", in: "10.0.0.0/8,172.16.0.0/12", want: []string{"10.0.0.0/8", "172.16.0.0/12"}},
		{name: "single IPv4 becomes /32", in: "192.168.1.1", want: []string{"192.168.1.1/32"}},
		{name: "single IPv6 becomes /128", in: "2001:db8::1", want: []string{"2001:db8::1/128"}},
		{name: "host bits are masked", in: "10.1.2.3/8", want: []string{"10.0.0.0/8"}},
		{name: "surrounding spaces are trimmed", in: "  10.0.0.0/8 ,  fd00::/8 ", want: []string{"10.0.0.0/8", "fd00::/8"}},
		{name: "IPv4-mapped IPv6 address is unmapped", in: "::ffff:10.0.0.1", want: []string{"10.0.0.1/32"}},
		{name: "IPv4-mapped IPv6 prefix is unmapped", in: "::ffff:10.0.0.0/104", want: []string{"10.0.0.0/8"}},
		{name: "invalid IP is an error", in: "10.0.0.0/8,not-an-ip", wantErr: true},
		{name: "invalid prefix length is an error", in: "10.0.0.0/33", wantErr: true},
		{name: "zone is an error", in: "fe80::1%eth0", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTrustedProxies(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseTrustedProxies(%q) = %v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTrustedProxies(%q): unexpected error %v", tt.in, err)
			}
			var gotStr []string
			for _, p := range got {
				gotStr = append(gotStr, p.String())
			}
			if !slices.Equal(gotStr, tt.want) {
				t.Errorf("ParseTrustedProxies(%q) = %v, want %v", tt.in, gotStr, tt.want)
			}
		})
	}
}
