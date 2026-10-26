package request

import (
	"log/slog"
	"net/http"
	"slices"

	"github.com/kafeiih/vogel/httpx/response"
	"github.com/kafeiih/vogel/reqctx"
)

// validationFailedMsg is the message of the security event logged for every
// input rejected by this package (OWASP ASVS: log input validation failures).
const validationFailedMsg = "request validation failed"

// WithLogger sets the logger that receives the security event this package
// logs, at Warn level, whenever it rejects a request's input: a Validator's
// WriteErrors, or a JSON body refused with 400, 413 or 415. Without it, the
// event goes to slog.Default() as it is at the time of the failure.
//
// The event carries the method, the path (never the query string), the client
// IP and request ID from reqctx, the HTTP status, the response code and, for
// a Validator, the names of the failing fields. It never carries the body,
// the field values or the error messages.
func WithLogger(l *slog.Logger) Option {
	return func(d *Decoder) {
		d.logger = l
	}
}

// loggerOrDefault returns l, or slog.Default() when l is nil.
func loggerOrDefault(l *slog.Logger) *slog.Logger {
	if l == nil {
		return slog.Default()
	}
	return l
}

// LogValidationFailure logs the security event described in WithLogger for a
// request rejected with a 400 VALIDATION_ERROR over fields, to l or, when l
// is nil, to slog.Default(). Validator.WriteErrors calls it; a package that
// writes its own response.ValidationError (such as validation) calls it so
// the rejection is logged the same way. Only the field names are logged.
func LogValidationFailure(l *slog.Logger, r *http.Request, fields FieldErrors) {
	logValidationFailure(l, r, http.StatusBadRequest, response.CodeValidationError, sortedFields(fields))
}

// logValidationFailure logs the security event for a rejected request. fields
// holds the failing field names, or is nil when the whole body was refused.
func logValidationFailure(l *slog.Logger, r *http.Request, status int, code string, fields []string) {
	ctx := r.Context()
	info, _ := reqctx.RequestInfoFromContext(ctx)
	attrs := []any{
		"status", status,
		"code", code,
		"method", r.Method,
		"path", r.URL.Path,
		"ip", info.IP,
		"request_id", reqctx.RequestIDFromContext(ctx),
	}
	if fields != nil {
		attrs = append(attrs, "fields", fields)
	}
	loggerOrDefault(l).WarnContext(ctx, validationFailedMsg, attrs...)
}

// fail logs the security event and writes the error response.
func (d *Decoder) fail(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	var l *slog.Logger
	if d != nil {
		l = d.logger
	}
	logValidationFailure(l, r, status, code, nil)
	response.Error(w, r, status, code, message)
}

// sortedFields returns the field names of errs in a stable order.
func sortedFields(errs FieldErrors) []string {
	fields := make([]string, 0, len(errs))
	for f := range errs {
		fields = append(fields, f)
	}
	slices.Sort(fields)
	return fields
}
