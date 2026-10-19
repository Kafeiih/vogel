package logger

import (
	"log/slog"
	"strings"
)

// RedactedValue replaces the value of every attribute whose key is on the
// redaction denylist. The key itself is kept, so a log line still shows that
// the field was there without showing its value. It is the same marker
// vogel/audit stores.
const RedactedValue = "[REDACTED]"

// defaultRedactedKeys mirrors audit's default denylist. logger cannot import
// audit (it would pull auth and pgx into a dependency-free package), so
// TestDefaultRedactedKeys_MatchAudit in package audit fails if the two lists
// drift.
var defaultRedactedKeys = []string{
	"password", "passwd", "token", "secret", "authorization",
	"api_key", "private_key", "credential", "cookie",
}

// DefaultRedactedKeys returns a copy of the denylist every Logger starts
// with: password, passwd, token, secret, authorization, api_key, private_key,
// credential and cookie, the same keys vogel/audit redacts by default. See
// WithRedactedKeys for the matching rules.
func DefaultRedactedKeys() []string {
	return append([]string(nil), defaultRedactedKeys...)
}

// Option customizes a Logger at construction time.
type Option func(*config)

type config struct {
	redactedKeys []string
}

// WithRedactedKeys adds keys to the Logger's redaction denylist, on top of
// the default one (see DefaultRedactedKeys). The value of every attribute
// whose key matches is replaced with RedactedValue, the key kept.
//
// Matching follows vogel/audit: it is case-insensitive and ignores '_', '-',
// '.' and spaces, and a denylist entry matches any key that contains it:
// "token" redacts "access_token", "refreshToken" and "X-Csrf-Token";
// "api_key" redacts "apiKey" and "X-Api-Key". Substring matching errs on the
// side of over-redaction ("token_count" is redacted too). Empty keys are
// ignored.
//
// Redaction applies to the attributes of every record and to those bound
// with With or inside WithGroup, at any group depth. A sensitive key whose
// value is a group (slog.Group, or a WithGroup name) has every attribute
// inside it redacted. The top-level built-in keys (time, level, msg, source)
// are never redacted.
//
// Out of scope: a secret hidden inside a single attribute's value — a struct,
// a map or a slice logged as one attribute (slog.Any("user", u)), or text
// embedded in the message — is not inspected; log the sensitive fields as
// their own attributes, or not at all. The SQL text that postgres's
// SlowQueryTracer logs is not redacted either; that tracer never logs query
// arguments.
func WithRedactedKeys(keys ...string) Option {
	return func(c *config) {
		c.redactedKeys = append(c.redactedKeys, keys...)
	}
}

// keyMatcher implements the denylist matching rules documented on
// WithRedactedKeys. It is a copy of audit's keyMatcher.
type keyMatcher struct {
	needles []string
}

func newKeyMatcher(keys []string) keyMatcher {
	m := keyMatcher{needles: make([]string, 0, len(keys))}
	for _, k := range keys {
		if n := normalizeKey(k); n != "" {
			m.needles = append(m.needles, n)
		}
	}
	return m
}

func (m keyMatcher) matches(key string) bool {
	k := normalizeKey(key)
	for _, n := range m.needles {
		if strings.Contains(k, n) {
			return true
		}
	}
	return false
}

func normalizeKey(key string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '_', '-', '.', ' ':
			return -1
		}
		return r
	}, strings.ToLower(key))
}

// redactAttr returns a slog.HandlerOptions.ReplaceAttr that replaces the
// value of a denylisted attribute, or of any attribute inside a group whose
// name is denylisted. slog calls ReplaceAttr for every non-group attribute
// with the path of open groups (WithGroup and slog.Group alike), including
// the ones bound with WithAttrs, so this reaches every depth.
func redactAttr(m keyMatcher) func(groups []string, a slog.Attr) slog.Attr {
	return func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) == 0 && isBuiltInKey(a.Key) {
			return a
		}
		if m.matches(a.Key) || matchesAny(m, groups) {
			a.Value = slog.StringValue(RedactedValue)
		}
		return a
	}
}

func matchesAny(m keyMatcher, groups []string) bool {
	for _, g := range groups {
		if m.matches(g) {
			return true
		}
	}
	return false
}

// isBuiltInKey reports whether key is one of the keys the slog handlers add
// to every record. A caller attribute with one of these keys at the top level
// cannot be told apart from the built-in one, so it is not redacted either.
func isBuiltInKey(key string) bool {
	switch key {
	case slog.TimeKey, slog.LevelKey, slog.MessageKey, slog.SourceKey:
		return true
	}
	return false
}
