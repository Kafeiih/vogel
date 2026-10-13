package config

import (
	"fmt"
	"net/url"
)

// ValidURL reports an error if value does not parse as an absolute URL with
// both a scheme and a host — e.g. "not a url", "localhost:3593" (missing
// scheme), or "https://" (missing host) are all rejected.
//
// This closes a real gap the source applications had: they only ever checked
// that a setting was non-empty, so a malformed endpoint (a missing "https://",
// a stray space) passed validation and failed later inside whatever HTTP
// client used it, with a less specific error further from the actual cause.
//
// The returned error never contains value, not even in part: URLs such as
// database DSNs routinely carry a password in their userinfo, and this error
// is typically logged at boot. url.Parse's own error is dropped for the same
// reason, since it quotes the input and some of its messages (an invalid
// port, for instance) echo fragments of the userinfo. u.Redacted() is not
// enough either: it only masks a password it managed to parse as one.
//
// Instead, the error names the field and one fixed, value-free reason so an
// operator can still tell the failure modes apart: "must be a well-formed
// URL" (url.Parse rejected it), "missing scheme", "missing \"//\" before the
// host" (a bare host:port, which url.Parse reads as scheme:opaque), or
// "missing host".
func ValidURL(fieldName, value string) error {
	u, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("%s must be a well-formed URL", fieldName)
	}
	switch {
	case u.Scheme == "":
		return fmt.Errorf("%s must be an absolute URL: missing scheme", fieldName)
	case u.Opaque != "":
		// "localhost:3593" parses with "localhost" as the scheme and "3593"
		// as opaque data, so "missing host" would point the operator at the
		// wrong mistake.
		return fmt.Errorf(`%s must be an absolute URL: missing "//" before the host (a bare host:port needs a scheme such as https://)`, fieldName)
	case u.Host == "":
		return fmt.Errorf("%s must be an absolute URL: missing host", fieldName)
	}
	return nil
}

// IntRange reports an error if value is outside [min, max].
func IntRange(fieldName string, value, min, max int) error {
	if value < min || value > max {
		return fmt.Errorf("%s must be between %d and %d, got %d", fieldName, min, max, value)
	}
	return nil
}

// OneOf reports an error if value is not exactly one of allowed.
//
// This is the check the audit found missing: NOTIFICATION_PROVIDER was read
// with a plain default-on-empty getter, so "smtp" vs "sendgrid" vs a typo like
// "sendgrdi" were all indistinguishable at boot time — the typo silently fell
// back to the SMTP default and disabled email delivery, with nothing but a
// Warn-level log to notice by. OneOf turns that into a boot-time config error.
func OneOf(fieldName, value string, allowed ...string) error {
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("%s must be one of %v, got %q", fieldName, allowed, value)
}

// MinMax reports an error if min is greater than max — e.g. a pool's
// MinConns/MaxConns, or a rate limiter's burst/limit pair.
func MinMax(minFieldName string, min int, maxFieldName string, max int) error {
	if min > max {
		return fmt.Errorf("%s (%d) must be less than or equal to %s (%d)", minFieldName, min, maxFieldName, max)
	}
	return nil
}

// ValidURL records an error on e if value does not parse as an absolute URL.
func (e *Errors) ValidURL(fieldName, value string) {
	e.Add(ValidURL(fieldName, value))
}

// IntRange records an error on e if value is outside [min, max].
func (e *Errors) IntRange(fieldName string, value, min, max int) {
	e.Add(IntRange(fieldName, value, min, max))
}

// OneOf records an error on e if value is not one of allowed.
func (e *Errors) OneOf(fieldName, value string, allowed ...string) {
	e.Add(OneOf(fieldName, value, allowed...))
}

// MinMax records an error on e if min is greater than max.
func (e *Errors) MinMax(minFieldName string, min int, maxFieldName string, max int) {
	e.Add(MinMax(minFieldName, min, maxFieldName, max))
}
