package action

import (
	"context"
	"fmt"

	"github.com/riverqueue/river"

	"github.com/kafeiih/vogel/notification"
)

// NotifyHandler returns a Handler that sends, through n, every message build
// returns for the Run. build decides who is notified and what they read: this
// package has no templates or recipients of its own. A build error is
// returned so River retries. Every message is validated with
// notification.Message.Validate before any is sent; an invalid one cancels
// the job, since it cannot become valid on retry. A send error is retried,
// which resends the messages that already went out: delivery is
// at-least-once.
func NotifyHandler(n notification.Notifier, build func(ctx context.Context, r Run) ([]*notification.Message, error)) Handler {
	return HandlerFunc(func(ctx context.Context, r Run) error {
		msgs, err := build(ctx, r)
		if err != nil {
			return fmt.Errorf("action: notify case %s: build messages: %w", r.CaseID, err)
		}
		for i, m := range msgs {
			if m == nil {
				return river.JobCancel(fmt.Errorf("action: notify case %s: message %d is nil", r.CaseID, i))
			}
			if err := m.Validate(); err != nil {
				return river.JobCancel(fmt.Errorf("action: notify case %s: message %d: %w", r.CaseID, i, err))
			}
		}
		for i, m := range msgs {
			if err := n.Send(ctx, m); err != nil {
				return fmt.Errorf("action: notify case %s: send message %d: %w", r.CaseID, i, err)
			}
		}
		return nil
	})
}
