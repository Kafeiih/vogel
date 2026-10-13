package middleware

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// RealIP rewrites r.RemoteAddr to the originating client IP, but only when
// the request arrived through one of the trusted proxies.
//
// Unlike chi's middleware.RealIP, which believes X-Forwarded-For, X-Real-IP
// and True-Client-IP from any peer, this middleware first checks the direct
// peer (the host part of r.RemoteAddr). If that peer is not inside one of the
// trusted prefixes the request is passed through untouched, so a client that
// connects directly cannot forge its own IP — and with it the reqctx client
// IP, audit_log.ip and any IP-keyed rate limit downstream. With no trusted
// prefixes at all, forwarding headers are never consulted.
//
// For a trusted peer, the client IP is resolved as follows:
//
//   - X-Forwarded-For (every header line, joined in order) is walked right to
//     left. Entries inside a trusted prefix are proxies and are skipped; the
//     first entry that is not trusted is the client. If every entry is
//     trusted, the leftmost one is used (nginx real_ip_recursive semantics).
//     An empty or unparseable entry reached during the walk stops it and
//     leaves r.RemoteAddr unchanged: nothing to its left can be attributed to
//     a trusted hop.
//   - Only when no X-Forwarded-For header is present is X-Real-IP consulted;
//     an unparseable value leaves r.RemoteAddr unchanged.
//   - True-Client-IP is never consulted: it is a CDN-specific header that a
//     generic reverse proxy passes through verbatim from the client.
//
// Entries may carry a port ("1.2.3.4:80", "[2001:db8::1]:443") and an IPv6
// zone; both are stripped. IPv4-mapped IPv6 addresses are unmapped before
// matching, so "::ffff:10.0.0.1" matches 10.0.0.0/8. The rewritten
// r.RemoteAddr is the bare IP with no port, like chi's middleware.RealIP;
// RequestContext and net.SplitHostPort-with-fallback consumers handle both
// forms.
//
// Mount it before RequestContext and before any rate limiter keyed on
// r.RemoteAddr. Build the prefixes with ParseTrustedProxies.
func RealIP(trusted ...netip.Prefix) func(http.Handler) http.Handler {
	prefixes := make([]netip.Prefix, 0, len(trusted))
	for _, p := range trusted {
		if p.IsValid() {
			prefixes = append(prefixes, normalizePrefix(p))
		}
	}

	isTrusted := func(addr netip.Addr) bool {
		for _, p := range prefixes {
			if p.Contains(addr) {
				return true
			}
		}
		return false
	}

	return func(next http.Handler) http.Handler {
		if len(prefixes) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if peer, ok := parsePeer(r.RemoteAddr); ok && isTrusted(peer) {
				if client, ok := forwardedClient(r.Header, isTrusted); ok {
					r.RemoteAddr = client.String()
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// forwardedClient resolves the client IP from the forwarding headers of a
// request whose direct peer is already known to be trusted.
func forwardedClient(h http.Header, isTrusted func(netip.Addr) bool) (netip.Addr, bool) {
	if xff := h.Values("X-Forwarded-For"); len(xff) > 0 {
		entries := strings.Split(strings.Join(xff, ","), ",")
		var leftmost netip.Addr
		for i := len(entries) - 1; i >= 0; i-- {
			addr, ok := parseForwardedAddr(entries[i])
			if !ok {
				return netip.Addr{}, false
			}
			if !isTrusted(addr) {
				return addr, true
			}
			leftmost = addr
		}
		return leftmost, true
	}

	if xrip := h.Get("X-Real-IP"); xrip != "" {
		return parseForwardedAddr(xrip)
	}
	return netip.Addr{}, false
}

// parsePeer extracts the IP from r.RemoteAddr ("ip:port" or a bare IP).
func parsePeer(remoteAddr string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.WithZone("").Unmap(), true
}

// parseForwardedAddr parses one forwarding-header entry: a bare IP or an
// "ip:port" / "[ipv6]:port" pair, with surrounding whitespace.
func parseForwardedAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Addr{}, false
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		ap, apErr := netip.ParseAddrPort(s)
		if apErr != nil {
			return netip.Addr{}, false
		}
		addr = ap.Addr()
	}
	return addr.WithZone("").Unmap(), true
}

// normalizePrefix masks host bits and turns an IPv4-mapped IPv6 prefix
// (::ffff:a.b.c.d/96+n) into its IPv4 form, because addresses are unmapped
// before matching.
func normalizePrefix(p netip.Prefix) netip.Prefix {
	if p.Addr().Is4In6() && p.Bits() >= 96 {
		p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
	}
	return p.Masked()
}

// ParseTrustedProxies parses a comma-separated list of CIDR prefixes and/or
// single IPs (for example the value of a TRUSTED_PROXIES environment
// variable) into prefixes for RealIP. A single IP becomes a /32 (IPv4) or
// /128 (IPv6) prefix; host bits are masked and IPv4-mapped IPv6 values are
// converted to IPv4. Empty items are skipped, so an empty string yields no
// prefixes — the safe default under which RealIP trusts no header. Any
// invalid item, including one with an IPv6 zone, is an error naming it.
func ParseTrustedProxies(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for item := range strings.SplitSeq(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}

		var p netip.Prefix
		if strings.Contains(item, "/") {
			parsed, err := netip.ParsePrefix(item)
			if err != nil {
				return nil, fmt.Errorf("trusted proxy %q: %w", item, err)
			}
			p = parsed
		} else {
			addr, err := netip.ParseAddr(item)
			if err != nil {
				return nil, fmt.Errorf("trusted proxy %q: %w", item, err)
			}
			if addr.Zone() != "" {
				return nil, fmt.Errorf("trusted proxy %q: IPv6 zones are not allowed", item)
			}
			p = netip.PrefixFrom(addr, addr.BitLen())
		}
		out = append(out, normalizePrefix(p))
	}
	return out, nil
}
