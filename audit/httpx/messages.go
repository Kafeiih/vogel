package httpx

// Messages holds the user-facing strings returned by Handler's List and
// GetByID.
//
// A library must not bake user-facing copy in one language: the source
// system this handler was ported from (go-licencias) returned hardcoded
// Spanish literals. DefaultMessages provides neutral English defaults; each
// consuming application overrides them via WithMessages to localize.
type Messages struct {
	// ListFailed is returned on HTTP 500 when List's repository call fails.
	ListFailed string

	// NotFound is returned on HTTP 404 when GetByID finds no entry matching
	// the requested ID.
	NotFound string

	// GetFailed is returned on HTTP 500 when GetByID's repository call
	// fails for a reason other than "not found".
	GetFailed string
}

// DefaultMessages returns neutral English defaults for Messages.
func DefaultMessages() Messages {
	return Messages{
		ListFailed: "Failed to list audit log entries",
		NotFound:   "Audit log entry not found",
		GetFailed:  "Failed to get audit log entry",
	}
}

// Option customizes NewHandler.
type Option func(*Messages)

// WithMessages overrides the default user-facing messages. Any field left
// as the empty string keeps its default.
func WithMessages(m Messages) Option {
	return func(dst *Messages) {
		if m.ListFailed != "" {
			dst.ListFailed = m.ListFailed
		}
		if m.NotFound != "" {
			dst.NotFound = m.NotFound
		}
		if m.GetFailed != "" {
			dst.GetFailed = m.GetFailed
		}
	}
}
