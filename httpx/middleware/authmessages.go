package middleware

// AuthMessages holds the user-facing strings returned by Authenticate and
// RequirePermission.
//
// A library must not bake user-facing copy in one language: the source
// systems this package was ported from returned hardcoded Spanish literals
// (in one case, Rioplatense voseo inside a Chilean product — an existing
// inconsistency, not something worth reproducing). DefaultAuthMessages
// provides neutral English defaults; each consuming application overrides
// them via WithAuthMessages to localize.
type AuthMessages struct {
	// Unauthorized is returned on HTTP 401: missing, malformed, or expired
	// credentials.
	Unauthorized string

	// Forbidden is returned on HTTP 403: valid credentials, but the action is
	// not permitted (insufficient role, or a policy denial).
	Forbidden string

	// ServiceUnavailable is returned on HTTP 503: the identity provider or
	// policy decision point could not be reached.
	ServiceUnavailable string
}

// DefaultAuthMessages returns neutral English defaults for AuthMessages.
func DefaultAuthMessages() AuthMessages {
	return AuthMessages{
		Unauthorized:       "Authentication required",
		Forbidden:          "You do not have permission to perform this action",
		ServiceUnavailable: "Service temporarily unavailable",
	}
}

// AuthOption customizes Authenticate or RequirePermission.
type AuthOption func(*AuthMessages)

// WithAuthMessages overrides the default user-facing messages. Any field left
// as the empty string keeps its default.
func WithAuthMessages(m AuthMessages) AuthOption {
	return func(dst *AuthMessages) {
		if m.Unauthorized != "" {
			dst.Unauthorized = m.Unauthorized
		}
		if m.Forbidden != "" {
			dst.Forbidden = m.Forbidden
		}
		if m.ServiceUnavailable != "" {
			dst.ServiceUnavailable = m.ServiceUnavailable
		}
	}
}
