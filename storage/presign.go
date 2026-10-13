package storage

import "strings"

// DefaultContentDisposition is the Content-Disposition a presigned GET carries
// when the caller passes no disposition option. It is "attachment" on purpose:
// a browser that follows the URL downloads the object instead of rendering it,
// so an uploaded HTML or SVG file can never execute script on the bucket's
// origin. Pass WithContentDisposition("inline") to opt into inline rendering.
const DefaultContentDisposition = "attachment"

// PresignGetOptions holds the response-header overrides a presigned GET URL
// asks the object store to send back. Adapters map these onto their native
// request (for S3: response-content-disposition and response-content-type).
//
// Build a value with ApplyPresignGetOptions rather than by hand so the secure
// default disposition is always applied.
type PresignGetOptions struct {
	// ContentDisposition is the Content-Disposition header the store returns
	// with the object. Never empty after ApplyPresignGetOptions.
	ContentDisposition string

	// ContentType, when non-empty, overrides the Content-Type the store
	// returns with the object. Empty means "whatever was stored".
	ContentType string
}

// PresignGetOption customizes a presigned GET URL. See AsAttachment,
// WithContentDisposition and WithResponseContentType.
type PresignGetOption func(*PresignGetOptions)

// ApplyPresignGetOptions folds opts over the defaults and returns the result.
// Adapters implementing Storage.PresignedGetURL call this so every adapter
// shares the same defaults: ContentDisposition is DefaultContentDisposition
// unless an option sets a non-empty value. nil options are ignored.
func ApplyPresignGetOptions(opts ...PresignGetOption) PresignGetOptions {
	o := PresignGetOptions{ContentDisposition: DefaultContentDisposition}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if o.ContentDisposition == "" {
		o.ContentDisposition = DefaultContentDisposition
	}
	return o
}

// WithContentDisposition sets the Content-Disposition the store returns with
// the object, verbatim. Use it for "inline" when the consumer genuinely wants
// the browser to render the object on the bucket origin, or for a disposition
// built elsewhere. An empty value keeps DefaultContentDisposition.
//
// The value is not sanitized; prefer AsAttachment when the string embeds a
// filename that came from user input.
func WithContentDisposition(disposition string) PresignGetOption {
	return func(o *PresignGetOptions) {
		o.ContentDisposition = disposition
	}
}

// AsAttachment sets Content-Disposition to attachment; filename="<name>" so
// the browser saves the object under name instead of the object key.
//
// name is sanitized for the quoted-string: CR, LF, double quotes and
// backslashes are removed and surrounding whitespace is trimmed, so a name
// taken from user input cannot close the quoted-string or split the header.
// When nothing is left after sanitizing, the disposition is a bare
// "attachment" and the browser falls back to the object key.
func AsAttachment(name string) PresignGetOption {
	return func(o *PresignGetOptions) {
		clean := strings.TrimSpace(strings.Map(func(r rune) rune {
			switch r {
			case '\r', '\n', '"', '\\':
				return -1
			}
			return r
		}, name))

		if clean == "" {
			o.ContentDisposition = DefaultContentDisposition
			return
		}
		o.ContentDisposition = `attachment; filename="` + clean + `"`
	}
}

// WithResponseContentType overrides the Content-Type the store returns with
// the object. Use it when the stored metadata is missing or wrong and the
// consumer knows the real type. An empty value means "whatever was stored".
func WithResponseContentType(contentType string) PresignGetOption {
	return func(o *PresignGetOptions) {
		o.ContentType = contentType
	}
}
