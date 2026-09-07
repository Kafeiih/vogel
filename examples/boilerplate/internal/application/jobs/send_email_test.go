package jobs

import (
	"context"
	"testing"

	"github.com/riverqueue/river"

	"github.com/kafeiih/vogel/notification"
)

// mockNotifier records calls to Send for testing.
type mockNotifier struct {
	lastMsg *notification.Message
	err     error
}

func (m *mockNotifier) Send(_ context.Context, msg *notification.Message) error {
	m.lastMsg = msg
	return m.err
}

func TestSendEmailJobArgs_Kind(t *testing.T) {
	args := SendEmailJobArgs{}
	if got := args.Kind(); got != "send_email" {
		t.Errorf("Kind(): got %q, want %q", got, "send_email")
	}
}

func TestSendEmailWorker_Work(t *testing.T) {
	mock := &mockNotifier{}
	w := &SendEmailWorker{Notifier: mock}

	job := &river.Job[SendEmailJobArgs]{
		Args: SendEmailJobArgs{
			Message: notification.Message{
				To:      "user@example.com",
				From:    "noreply@inst.cl",
				Subject: "Test",
				HTML:    "<p>hello</p>",
			},
		},
	}

	err := w.Work(context.Background(), job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mock.lastMsg == nil {
		t.Fatal("expected Send to be called")
	}
	if mock.lastMsg.To != "user@example.com" {
		t.Errorf("To: got %q, want %q", mock.lastMsg.To, "user@example.com")
	}
}

func TestSendEmailWorker_WorkInvalidMessage(t *testing.T) {
	mock := &mockNotifier{}
	w := &SendEmailWorker{Notifier: mock}

	job := &river.Job[SendEmailJobArgs]{
		Args: SendEmailJobArgs{
			Message: notification.Message{
				To: "", // invalid
			},
		},
	}

	err := w.Work(context.Background(), job)
	if err == nil {
		t.Fatal("expected error for invalid message")
	}
}
