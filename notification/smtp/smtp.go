// Package smtp implements the notification.Notifier port via SMTP using go-mail.
package smtp

import (
	"context"
	"fmt"
	"log/slog"

	gomail "github.com/wneessen/go-mail"

	"github.com/kafeiih/vogel/notification"
)

// Config holds the settings needed to construct an SMTP-backed Notifier.
type Config struct {
	Host     string // SMTP server host
	Port     int    // SMTP server port (default: 587)
	Username string // SMTP auth username
	Password string // SMTP auth password
}

// SMTPNotifier implements notification.Notifier via SMTP using go-mail.
//
// It dials, authenticates, and sends over a fresh TCP+TLS connection on every
// Send call — there is no connection pooling or client reuse. This is the
// simple, always-correct behavior; it costs a full handshake per email.
// If SMTP send volume grows enough for that cost to matter, add a pooled
// client (e.g. keep a small set of long-lived *gomail.Client and hand out
// leases from a channel-backed pool) rather than assuming this type already
// does so.
type SMTPNotifier struct {
	host        string
	port        int
	username    string
	password    string
	defaultFrom string
	logger      *slog.Logger
}

// NewSMTPNotifier creates an SMTP-backed notifier.
func NewSMTPNotifier(cfg Config, defaultFrom string, logger *slog.Logger) (*SMTPNotifier, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("notification/smtp: host is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("notification/smtp: logger is required")
	}

	return &SMTPNotifier{
		host:        cfg.Host,
		port:        cfg.Port,
		username:    cfg.Username,
		password:    cfg.Password,
		defaultFrom: defaultFrom,
		logger:      logger,
	}, nil
}

// Send delivers an email via SMTP. Each call dials a new connection — see the
// SMTPNotifier doc comment.
func (n *SMTPNotifier) Send(ctx context.Context, msg *notification.Message) error {
	if err := msg.Validate(); err != nil {
		return err
	}

	from := msg.From
	if from == "" {
		from = n.defaultFrom
	}

	m := gomail.NewMsg()
	if err := m.From(from); err != nil {
		return fmt.Errorf("notification/smtp: invalid from %q: %w", from, err)
	}
	if err := m.To(msg.To); err != nil {
		return fmt.Errorf("notification/smtp: invalid to %q: %w", msg.To, err)
	}

	m.Subject(msg.Subject)

	if msg.HTML != "" {
		m.SetBodyString(gomail.TypeTextHTML, msg.HTML)
	}
	if msg.Text != "" {
		if msg.HTML != "" {
			m.AddAlternativeString(gomail.TypeTextPlain, msg.Text)
		} else {
			m.SetBodyString(gomail.TypeTextPlain, msg.Text)
		}
	}

	client, err := gomail.NewClient(n.host,
		gomail.WithPort(n.port),
		gomail.WithSMTPAuth(gomail.SMTPAuthPlain),
		gomail.WithUsername(n.username),
		gomail.WithPassword(n.password),
	)
	if err != nil {
		return fmt.Errorf("notification/smtp: creating client: %w", err)
	}

	if err := client.DialAndSendWithContext(ctx, m); err != nil {
		return fmt.Errorf("notification/smtp: sending: %w", err)
	}

	n.logger.InfoContext(ctx, "email sent via SMTP",
		"to", msg.To,
		"subject", msg.Subject,
	)
	return nil
}
