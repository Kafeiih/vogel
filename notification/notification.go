// Package notification defines the contracts for email delivery.
//
// The Notifier interface abstracts email sending. Concrete adapters (SMTP via
// go-mail, SendGrid via its HTTP API) live in subpackages so that a consumer
// importing only this port does not pull in an SDK it does not need.
//
// Usage:
//
//	notifier.Send(ctx, &notification.Message{
//	    To: "user@example.com", From: "noreply@inst.cl",
//	    Subject: "Welcome", HTML: "<h1>Hello</h1>",
//	})
package notification

import (
	"context"
	"fmt"
)

// Message represents an email to be sent.
// JSON tags are included for serialization as a background job payload.
type Message struct {
	To      string `json:"to"`      // Recipient email address
	From    string `json:"from"`    // Sender email address
	Subject string `json:"subject"` // Email subject line
	HTML    string `json:"html"`    // HTML body (optional if Text is set)
	Text    string `json:"text"`    // Plain text body (optional if HTML is set)
}

// Validate checks that required fields are present.
func (m *Message) Validate() error {
	if m.To == "" {
		return fmt.Errorf("notification: To is required")
	}
	if m.From == "" {
		return fmt.Errorf("notification: From is required")
	}
	if m.Subject == "" {
		return fmt.Errorf("notification: Subject is required")
	}
	if m.HTML == "" && m.Text == "" {
		return fmt.Errorf("notification: HTML or Text content is required")
	}
	return nil
}

// Notifier abstracts email delivery.
//
// Implementations:
//   - notification/smtp.SMTPNotifier — SMTP via go-mail
//   - notification/sendgrid.SendGridNotifier — SendGrid HTTP API
type Notifier interface {
	// Send delivers an email message.
	// Returns an error if delivery fails or the message is invalid.
	Send(ctx context.Context, msg *Message) error
}
