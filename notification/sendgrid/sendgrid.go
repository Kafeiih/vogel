// Package sendgrid implements the notification.Notifier port via the SendGrid HTTP API.
package sendgrid

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

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
func NewSendGridNotifier(cfg Config, defaultFrom string, logger *slog.Logger) (*SendGridNotifier, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("notification/sendgrid: API key is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("notification/sendgrid: logger is required")
	}

	return &SendGridNotifier{
		client:      sendgrid.NewSendClient(cfg.APIKey),
		defaultFrom: defaultFrom,
		logger:      logger,
	}, nil
}

// Send delivers an email via SendGrid HTTP API.
func (n *SendGridNotifier) Send(ctx context.Context, msg *notification.Message) error {
	if err := msg.Validate(); err != nil {
		return err
	}

	from := msg.From
	if from == "" {
		from = n.defaultFrom
	}

	sgFrom := mail.NewEmail("", from)
	sgTo := mail.NewEmail("", msg.To)

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
