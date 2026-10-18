package storage

import (
	"errors"
	"strings"
	"unicode"
)

// DefaultContentDisposition is the Content-Disposition a presigned GET carries
// when the caller passes no disposition option. It is "attachment" on purpose:
// a browser that follows the URL downloads the object instead of rendering it,
// so an uploaded HTML or SVG file can never execute script on the bucket's
// origin. Pass WithContentDisposition("inline") to opt into inline rendering.
const DefaultContentDisposition = "attachment"

// DefaultResponseContentType is the Content-Type a presigned GET asks the
// store to return when the caller passes no WithResponseContentType. The
// stored type usually came from the uploading client and is not trusted:
// serving everything as application/octet-stream, together with the
// attachment default, means a browser never renders an uploaded object
// inline unless the consumer opts in. Pass WithResponseContentType with a
// type the application verified (for example with SniffContentType) to
// serve it as that type.
const DefaultResponseContentType = "application/octet-stream"

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

	// ContentType is the Content-Type the store returns with the object,
	// overriding whatever was stored. Never empty after
	// ApplyPresignGetOptions: it defaults to DefaultResponseContentType.
	ContentType string
}

// PresignGetOption customizes a presigned GET URL. See AsAttachment,
// WithContentDisposition and WithResponseContentType.
type PresignGetOption func(*PresignGetOptions)

// ApplyPresignGetOptions folds opts over the defaults and returns the result.
// Adapters implementing Storage.PresignedGetURL call this so every adapter
// shares the same defaults: ContentDisposition is DefaultContentDisposition
// and ContentType is DefaultResponseContentType unless an option sets a
// non-empty value. nil options are ignored.
func ApplyPresignGetOptions(opts ...PresignGetOption) PresignGetOptions {
	o := PresignGetOptions{ContentDisposition: DefaultContentDisposition, ContentType: DefaultResponseContentType}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if o.ContentDisposition == "" {
		o.ContentDisposition = DefaultContentDisposition
	}
	if o.ContentType == "" {
		o.ContentType = DefaultResponseContentType
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
// name is sanitized first: control characters (C0, DEL and C1, which include
// CR, LF and tab), Unicode format characters (such as the bidi overrides that
// disguise an extension), line and paragraph separators, double quotes and
// backslashes are removed, and surrounding whitespace is trimmed, so a name
// taken from user input cannot close the quoted-string or split the header.
// When nothing is left after sanitizing, the disposition is a bare
// "attachment" and the browser falls back to the object key.
//
// The header value is always printable ASCII. A name that is ASCII after
// sanitizing is sent as filename="<name>". A name with non-ASCII characters
// is sent twice, per RFC 6266: a filename="<fallback>" parameter, where each
// non-ASCII character becomes '_', for clients that ignore the extended form,
// and a filename* parameter in the RFC 5987 form (UTF-8, no language tag, the
// name percent-encoded), which current browsers prefer:
//
//	attachment; filename="informe-a_o.pdf"; filename*=UTF-8''informe-a%C3%B1o.pdf
func AsAttachment(name string) PresignGetOption {
	return func(o *PresignGetOptions) {
		clean := strings.TrimSpace(strings.Map(func(r rune) rune {
			switch {
			case r == '"', r == '\\':
				return -1
			case unicode.IsControl(r), unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp):
				return -1
			}
			return r
		}, name))

		if clean == "" {
			o.ContentDisposition = DefaultContentDisposition
			return
		}

		fallback, ascii := asciiFallback(clean)
		o.ContentDisposition = `attachment; filename="` + fallback + `"`
		if !ascii {
			o.ContentDisposition += "; filename*=UTF-8''" + rfc5987Encode(clean)
		}
	}
}

// asciiFallback replaces every non-ASCII rune of a sanitized name with '_'
// and reports whether name was ASCII already.
func asciiFallback(name string) (fallback string, ascii bool) {
	ascii = true
	fallback = strings.Map(func(r rune) rune {
		if r > unicode.MaxASCII {
			ascii = false
			return '_'
		}
		return r
	}, name)
	return fallback, ascii
}

// rfc5987Encode percent-encodes the UTF-8 bytes of s that are not attr-char
// as defined by RFC 5987 section 3.2.1.
func rfc5987Encode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s) * 3)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isAttrChar(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}

func isAttrChar(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", c) >= 0
}

// WithResponseContentType sets the Content-Type the store returns with the
// object instead of DefaultResponseContentType. Use it only with a type the
// application verified itself (for example the one SniffContentType
// detected at upload), never with the type the uploading client declared.
// An empty value keeps DefaultResponseContentType.
func WithResponseContentType(contentType string) PresignGetOption {
	return func(o *PresignGetOptions) {
		o.ContentType = contentType
	}
}

// ErrNotFound is wrapped by Download when the key has no object.
var ErrNotFound = errors.New("storage: object not found")

// ErrPresignExpiryTooLong is returned by a presign method when the requested
// expiry exceeds the adapter's maximum. A presigned URL is a bearer
// credential that nothing can revoke before it expires, so adapters refuse to
// sign one that lives longer than they allow instead of silently shortening
// it.
var ErrPresignExpiryTooLong = errors.New("storage: presign expiry exceeds the maximum")

// ErrInvalidContentLength is returned by PresignedPutURL when
// WithContentLength was given a size that is not positive.
var ErrInvalidContentLength = errors.New("storage: content length must be positive")

// PresignPutOptions holds the constraints a presigned PUT URL signs. Build a
// value with ApplyPresignPutOptions.
type PresignPutOptions struct {
	// ContentLength is the exact body size, in bytes, the URL accepts. It is
	// only signed when HasContentLength is set; adapters reject a size that is
	// not positive, so an explicit zero is an error rather than "no limit".
	ContentLength    int64
	HasContentLength bool
}

// PresignPutOption customizes a presigned PUT URL. See WithContentLength.
type PresignPutOption func(*PresignPutOptions)

// ApplyPresignPutOptions folds opts and returns the result. Adapters
// implementing Storage.PresignedPutURL call this; nil options are ignored.
func ApplyPresignPutOptions(opts ...PresignPutOption) PresignPutOptions {
	var o PresignPutOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}

// WithContentLength signs the exact size of the body the URL accepts. The
// store rejects an upload whose Content-Length differs, so whoever holds the
// URL cannot store an object larger (or smaller) than the size the
// application approved. Validate the size against your limit before signing.
// A size that is not positive makes PresignedPutURL return
// ErrInvalidContentLength: the AWS SDK signs neither Content-Length nor
// Content-Type for a zero length, which would leave the URL less constrained
// than one signed without this option.
func WithContentLength(n int64) PresignPutOption {
	return func(o *PresignPutOptions) {
		o.ContentLength = n
		o.HasContentLength = true
	}
}
