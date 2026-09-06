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
