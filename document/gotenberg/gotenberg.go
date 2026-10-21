// Package gotenberg is a document.Renderer backed by a Gotenberg server's
// Chromium HTML route. It talks plain HTTP (no Go SDK), so it depends only
// on the standard library and the document package.
//
// The request shape follows the Gotenberg docs
// (https://gotenberg.dev/docs/convert-with-chromium/convert-html-to-pdf,
// verified 2026-10-20): POST {base}/forms/chromium/convert/html with a
// multipart form whose "files" field carries a file named "index.html"; a
// 200 response body is the PDF. Gotenberg answers 400 on a main-page network
// error, 403 on a forbidden URL, and 409 when a page returns a failing HTTP
// status.
package gotenberg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kafeiih/vogel/document"
)

// htmlRoute is Gotenberg's Chromium HTML-to-PDF route, relative to the base
// URL.
const htmlRoute = "/forms/chromium/convert/html"

const (
	// DefaultTimeout bounds one conversion when Config.Timeout is zero.
	DefaultTimeout = 30 * time.Second
	// DefaultMaxResponseBytes bounds the PDF read from Gotenberg when
	// Config.MaxResponseBytes is zero.
	DefaultMaxResponseBytes int64 = 32 << 20
	// errorBodyLimit bounds how much of a non-2xx body is quoted in the error.
	errorBodyLimit = 512
)

// Sentinel errors callers can branch on via errors.Is.
var (
	// ErrResponseTooLarge reports a PDF larger than Config.MaxResponseBytes;
	// the body is discarded. It also wraps document.ErrRenderRejected.
	ErrResponseTooLarge = errors.New("gotenberg: response exceeds the configured size limit")
	// ErrUnexpectedStatus reports a non-2xx response. The error text carries
	// the status code and the start of Gotenberg's plain-text message. A 400,
	// 409 or 422 also wraps document.ErrRenderRejected.
	ErrUnexpectedStatus = errors.New("gotenberg: unexpected response status")
)

// Config configures a Renderer.
type Config struct {
	// BaseURL is the Gotenberg server, e.g. "http://gotenberg:3000". Required;
	// it must be an absolute http or https URL. A trailing slash is ignored.
	BaseURL string
	// HTTPClient sends the requests. Nil uses a new *http.Client; inject one
	// to add tracing (otel.Transport), proxies or TLS settings.
	HTTPClient *http.Client
	// Timeout bounds one conversion, on top of any deadline already on the
	// caller's ctx. Zero uses DefaultTimeout.
	Timeout time.Duration
	// MaxResponseBytes bounds the PDF read into memory. Zero uses
	// DefaultMaxResponseBytes.
	MaxResponseBytes int64
}

// Renderer converts HTML to PDF through Gotenberg. It is safe for
// concurrent use.
type Renderer struct {
	endpoint string
	client   *http.Client
	timeout  time.Duration
	maxBytes int64
}

// New validates cfg and returns a Renderer.
func New(cfg Config) (*Renderer, error) {
	u, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("gotenberg: base url %q must be an absolute http(s) url", cfg.BaseURL)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxBytes := cfg.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxResponseBytes
	}
	return &Renderer{endpoint: u.String() + htmlRoute, client: client, timeout: timeout, maxBytes: maxBytes}, nil
}

// Render posts html as index.html and returns the PDF bytes.
func (r *Renderer) Render(ctx context.Context, html []byte) ([]byte, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("files", "index.html")
	if err != nil {
		return nil, fmt.Errorf("gotenberg: build request: %w", err)
	}
	if _, err := part.Write(html); err != nil {
		return nil, fmt.Errorf("gotenberg: build request: %w", err)
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("gotenberg: build request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, &body)
	if err != nil {
		return nil, fmt.Errorf("gotenberg: build request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gotenberg: convert html: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		err := fmt.Errorf("%w: %d: %s", ErrUnexpectedStatus, resp.StatusCode, strings.TrimSpace(string(msg)))
		if permanentStatus(resp.StatusCode) {
			return nil, rejected{err}
		}
		return nil, err
	}

	pdf, err := io.ReadAll(io.LimitReader(resp.Body, r.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("gotenberg: read response: %w", err)
	}
	if int64(len(pdf)) > r.maxBytes {
		return nil, rejected{fmt.Errorf("%w (%d bytes)", ErrResponseTooLarge, r.maxBytes)}
	}
	return pdf, nil
}

// permanentStatus reports whether a status means Gotenberg cannot convert
// this HTML, so sending it again cannot help. Every other status is retried,
// including 401, 403, 404 and 413: a proxy, a WAF or a wrong base URL in
// front of Gotenberg returns them too, and an operator can fix that while
// the job is still retrying.
func permanentStatus(code int) bool {
	switch code {
	case http.StatusBadRequest, http.StatusConflict, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// rejected keeps err's text and adds document.ErrRenderRejected to its chain.
type rejected struct{ err error }

func (e rejected) Error() string   { return e.err.Error() }
func (e rejected) Unwrap() []error { return []error{e.err, document.ErrRenderRejected} }
