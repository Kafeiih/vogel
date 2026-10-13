package notification

import "testing"

func TestMessage_Validate_Valid(t *testing.T) {
	msg := &Message{
		To:      "user@example.com",
		From:    "noreply@institution.cl",
		Subject: "Welcome",
		HTML:    "<h1>Hello</h1>",
	}

	if err := msg.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestMessage_Validate_EmptyTo(t *testing.T) {
	msg := &Message{
		To:      "",
		From:    "noreply@institution.cl",
		Subject: "Welcome",
	}

	if err := msg.Validate(); err == nil {
		t.Error("expected error for empty To")
	}
}

func TestMessage_Validate_EmptyFrom(t *testing.T) {
	msg := &Message{
		To:      "user@example.com",
		From:    "",
		Subject: "Welcome",
	}

	if err := msg.Validate(); err == nil {
		t.Error("expected error for empty From")
	}
}

func TestMessage_Validate_EmptySubject(t *testing.T) {
	msg := &Message{
		To:      "user@example.com",
		From:    "noreply@institution.cl",
		Subject: "",
	}

	if err := msg.Validate(); err == nil {
		t.Error("expected error for empty Subject")
	}
}

func TestMessage_Validate_NoContent(t *testing.T) {
	msg := &Message{
		To:      "user@example.com",
		From:    "noreply@institution.cl",
		Subject: "Hello",
		HTML:    "",
		Text:    "",
	}

	if err := msg.Validate(); err == nil {
		t.Error("expected error when both HTML and Text are empty")
	}
}

func TestMessage_Validate_TextOnly(t *testing.T) {
	msg := &Message{
		To:      "user@example.com",
		From:    "noreply@institution.cl",
		Subject: "Hello",
		Text:    "plain text content",
	}

	if err := msg.Validate(); err != nil {
		t.Errorf("unexpected error for text-only message: %v", err)
	}
}

func TestMessage_Validate_RejectsMalformedAddresses(t *testing.T) {
	base := Message{
		To:      "user@example.com",
		From:    "noreply@institution.cl",
		Subject: "Welcome",
		Text:    "Hello",
	}
	cases := []struct {
		name     string
		to, from string
	}{
		{"to without at sign", "user.example.com", base.From},
		{"to with header injection", "user@example.com\r\nBcc: victim@example.com", base.From},
		{"to with two addresses", "a@example.com, b@example.com", base.From},
		{"from without domain", base.To, "noreply@"},
		{"from with header injection", base.To, "noreply@institution.cl\nBcc: victim@example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := base
			msg.To, msg.From = tc.to, tc.from
			if err := msg.Validate(); err == nil {
				t.Errorf("expected error for To=%q From=%q", tc.to, tc.from)
			}
		})
	}
}

func TestMessage_Validate_AcceptsDisplayName(t *testing.T) {
	msg := &Message{
		To:      "Ana Pérez <ana@example.com>",
		From:    `"Institución" <noreply@institution.cl>`,
		Subject: "Welcome",
		Text:    "Hello",
	}
	if err := msg.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
