package storage

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
)

// sniffLen is how many leading bytes http.DetectContentType considers.
const sniffLen = 512

// ErrContentTypeNotAllowed is returned (wrapped) by SniffContentType when the
// detected type is not in the caller's allowlist.
var ErrContentTypeNotAllowed = errors.New("storage: content type not allowed")

// SniffContentType detects the type of an upload from its content instead of
// trusting the type the client declared, and checks it against allowed.
//
// It reads up to the first 512 bytes of r, detects their type with
// http.DetectContentType and returns that type (with any parameters it
// reports, e.g. "text/plain; charset=utf-8") together with a reader that
// yields the full content, the peeked bytes included, so the caller can pass
// it on to Upload. Input shorter than 512 bytes is sniffed as is; empty input
// is detected as "text/plain; charset=utf-8", as DetectContentType does.
//
// Matching compares media types only: parameters such as charset are ignored
// on both sides, and the comparison is case-insensitive. An allowlist entry
// that is not a valid media type never matches. When the detected type is not
// allowed (an empty allowlist allows nothing) the error wraps
// ErrContentTypeNotAllowed and the detected type is still returned, for
// logging; the body is nil. A read error from r is returned as is.
//
// DetectContentType recognizes a fixed set of signatures, so this is a gate
// against content that is plainly not what the application accepts (HTML,
// scripts, executables posing as images), not a full validator of the file.
func SniffContentType(r io.Reader, allowed ...string) (contentType string, body io.Reader, err error) {
	head := make([]byte, sniffLen)
	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", nil, fmt.Errorf("storage: sniffing content type: %w", err)
	}
	head = head[:n]

	contentType = http.DetectContentType(head)
	detected := mediaType(contentType)
	for _, a := range allowed {
		if m := mediaType(a); m != "" && m == detected {
			return contentType, io.MultiReader(bytes.NewReader(head), r), nil
		}
	}
	return contentType, nil, fmt.Errorf("%w: %s", ErrContentTypeNotAllowed, detected)
}

// mediaType returns the lower-cased media type of v without parameters, or
// "" when v does not parse.
func mediaType(v string) string {
	m, _, err := mime.ParseMediaType(v)
	if err != nil {
		return ""
	}
	return strings.ToLower(m)
}
