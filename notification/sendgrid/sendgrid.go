// Package sendgrid implements the notification.Notifier port via the SendGrid HTTP API.
package sendgrid

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	netmail "net/mail"
	"time"

	"github.com/sendgrid/sendgrid-go"
	"github.com/sendgrid/sendgrid-go/helpers/mail"

	"github.com/kafeiih/vogel/notification"
)

// Config holds the settings needed to construct a SendGrid-backed Notifier.
type Config struct {
	APIKey string // SendGrid API key

	// HTTPTimeout bounds each call to the SendGrid API, from dialing to
	// reading the response body. Zero means DefaultHTTPTimeout; negative is
	// a construction error. It applies even when the caller's context has
	// no deadline, so a stalled API cannot hang the sender forever.
	HTTPTimeout time.Duration
}

// DefaultHTTPTimeout is the per-request timeout used when
// Config.HTTPTimeout is zero.
const DefaultHTTPTimeout = 30 * time.Second

// SendGridNotifier implements notification.Notifier via SendGrid HTTP API.
type SendGridNotifier struct {
	// client is the SDK's request template (endpoint, method, auth headers).
	// It is never sent through the SDK, which would use rest.DefaultClient
	// (an http.Client with no timeout) and mutate the shared template.
	client      *sendgrid.Client
	httpClient  *http.Client
	defaultFrom string
	logger      *slog.Logger
}

// NewSendGridNotifier creates a SendGrid-backed notifier.
//
// defaultFrom is the sender used when a message has no From. It may be empty,
// in which case every message must set From; when set it must be a single
// RFC 5322 address (net/mail.ParseAddress), or construction fails so a bad
// configuration is caught at boot rather than on the first Send.
func NewSendGridNotifier(cfg Config, defaultFrom string, logger *slog.Logger) (*SendGridNotifier, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("notification/sendgrid: API key is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("notification/sendgrid: logger is required")
	}
	if cfg.HTTPTimeout < 0 {
		return nil, fmt.Errorf("notification/sendgrid: HTTP timeout must not be negative")
	}
	if defaultFrom != "" {
		if _, err := sgEmail(defaultFrom); err != nil {
			return nil, fmt.Errorf("notification/sendgrid: default From is not a valid email address")
		}
	}

	timeout := cfg.HTTPTimeout
	if timeout == 0 {
		timeout = DefaultHTTPTimeout
	}

	return &SendGridNotifier{
		client:      sendgrid.NewSendClient(cfg.APIKey),
		httpClient:  &http.Client{Timeout: timeout},
		defaultFrom: defaultFrom,
		logger:      logger,
	}, nil
}

// Send delivers an email via SendGrid HTTP API. The sender is msg.From, or the
// notifier's default when msg.From is empty; if both are empty Send fails
// without calling the API.
func (n *SendGridNotifier) Send(ctx context.Context, msg *notification.Message) error {
	if err := msg.Validate(); err != nil {
		return err
	}

	from, err := resolveFrom(msg.From, n.defaultFrom)
	if err != nil {
		return err
	}

	// net/mail parse errors can quote part of the address, so they are not
	// wrapped: addresses are personal data and must not reach logs.
	sgFrom, err := sgEmail(from)
	if err != nil {
		return fmt.Errorf("notification/sendgrid: From is not a valid email address")
	}
	sgTo, err := sgEmail(msg.To)
	if err != nil {
		return fmt.Errorf("notification/sendgrid: To is not a valid email address")
	}

	sgMail := mail.NewSingleEmail(sgFrom, msg.Subject, sgTo, msg.Text, msg.HTML)

	status, body, err := n.post(ctx, mail.GetRequestBody(sgMail))
	if err != nil {
		return fmt.Errorf("notification/sendgrid: sending: %w", err)
	}

	if status >= http.StatusBadRequest {
		return fmt.Errorf("notification/sendgrid: API error %d: %s", status, body)
	}

	n.logger.InfoContext(ctx, "email sent via SendGrid",
		"to", msg.To,
		"subject", msg.Subject,
		"status", status,
	)
	return nil
}

// maxResponseBody caps how much of a SendGrid response is read; the body is
// only used to describe an API error.
const maxResponseBody = 64 << 10

// post sends body to the mail send endpoint through the notifier's bounded
// HTTP client. It builds a fresh request from the SDK template on every
// call, so concurrent Sends share nothing mutable.
func (n *SendGridNotifier) post(ctx context.Context, body []byte) (int, string, error) {
	tmpl := n.client.Request
	req, err := http.NewRequestWithContext(ctx, string(tmpl.Method), tmpl.BaseURL, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	for k, v := range tmpl.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return 0, "", fmt.Errorf("reading response: %w", err)
	}
	return resp.StatusCode, string(respBody), nil
}

// sgEmail parses an RFC 5322 address into SendGrid's name/address pair. The
// API takes them as separate JSON fields, so passing "Name <a@b.cl>" whole as
// the address would be rejected or delivered to a literal string.
func sgEmail(addr string) (*mail.Email, error) {
	a, err := netmail.ParseAddress(addr)
	if err != nil {
		return nil, err
	}
	return mail.NewEmail(a.Name, a.Address), nil
}

// resolveFrom picks the message sender, falling back to the configured
// default when the message leaves From empty.
func resolveFrom(msgFrom, defaultFrom string) (string, error) {
	if msgFrom != "" {
		return msgFrom, nil
	}
	if defaultFrom != "" {
		return defaultFrom, nil
	}
	return "", fmt.Errorf("notification/sendgrid: From is missing and no default sender is configured")
}
