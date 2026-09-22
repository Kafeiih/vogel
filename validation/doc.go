// Package validation turns go-playground/validator's struct-tag errors into
// the same shape request.Validator already uses: request.FieldErrors, one
// message per JSON field, ready for response.ValidationError.
//
// # Why this exists
//
// go-playground/validator's own Error() text is meant for a developer
// reading a log, not for a client reading a JSON response. Sent as-is (for
// example map[string]string{"validation": err.Error()}), it has three
// problems:
//
//   - It leaks Go identifiers. A struct field named ResourceID reports as
//     "ResourceID", not the JSON key resource_id a client actually sent.
//   - It is a single opaque string, not a map keyed by field, so a client
//     cannot point a form error at the right input without parsing English
//     prose.
//   - It cannot be localized: the wording is baked into the library, not
//     configurable per deployment.
//
// This package fixes all three: Struct maps each failing field to its JSON
// key (via a validator.RegisterTagNameFunc backed by the json tag, matching
// how encoding/json itself names fields) and a message drawn from Messages,
// which — like request.Messages — a consuming application configures once
// via WithMessages.
//
// # Usage
//
// Build one *Validator per application (or use the package-level functions,
// which use a default instance with DefaultMessages) and call Struct or
// Write from a handler:
//
//	if !validation.Write(w, r, &in) {
//	    return
//	}
//
// Write reports true when in is valid; on a validation failure it writes a
// response.ValidationError with the per-field messages and returns false. A
// non-struct or nil argument is a programming error, not a client mistake:
// Write logs nothing (it has no logger dependency) and instead writes a
// generic 500 via response.Error, leaving the underlying error for the
// caller to inspect by calling Struct directly if it wants to log it.
package validation
