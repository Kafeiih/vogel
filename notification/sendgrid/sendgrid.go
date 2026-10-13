// Package sendgrid implements the notification.Notifier port via the SendGrid HTTP API.
package sendgrid

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	netmail "net/mail"

	"github.com/sendgrid/sendgrid-go"
	"github.com/sendgrid/sendgrid-go/helpers/mail"

	"github.com/kafeiih/vogel/notification"
)

// Config holds the settings needed to construct a SendGrid-backed Notifier.
type Config struct {
	APIKey string // SendGrid API key
}

// SendGridNotifier implements notification.Notifier via SendGrid HTTP API.
type SendGridNotifier struct {
	client      *sendgrid.Client
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
	if defaultFrom != "" {
		if _, err := sgEmail(defaultFrom); err != nil {
			return nil, fmt.Errorf("notification/sendgrid: default From is not a valid email address")
		}
	}

	return &SendGridNotifier{
		client:      sendgrid.NewSendClient(cfg.APIKey),
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

	response, err := n.client.SendWithContext(ctx, sgMail)
	if err != nil {
		return fmt.Errorf("notification/sendgrid: sending: %w", err)
	}

	if response.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("notification/sendgrid: API error %d: %s", response.StatusCode, response.Body)
	}

	n.logger.InfoContext(ctx, "email sent via SendGrid",
		"to", msg.To,
		"subject", msg.Subject,
		"status", response.StatusCode,
	)
	return nil
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
