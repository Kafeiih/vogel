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
// the job, since it cannot become valid on retry.
//
// Run by Worker, a Run with more than one message is fanned out: the effect
// job enqueues one job per message and sends nothing, and each of those jobs
// calls build again and sends only its own message, so a failed send retries
// that message alone instead of resending the ones that already went out.
// build must therefore return the same messages in the same order for the
// same Run; a message job whose message build no longer returns is
// cancelled. Errors number messages from 1, like Run.Part. Called directly,
// outside Worker, the Handler sends every message
// in order and a send error resends them all on retry. Delivery of each
// message is at-least-once.
func NotifyHandler(n notification.Notifier, build func(ctx context.Context, r Run) ([]*notification.Message, error)) Handler {
	return HandlerFunc(func(ctx context.Context, r Run) error {
		msgs, err := build(ctx, r)
		if err != nil {
			return fmt.Errorf("action: notify case %s: build messages: %w", r.CaseID, err)
		}
		if r.Part > 0 {
			return sendPart(ctx, n, r, msgs)
		}
		for i, m := range msgs {
			if err := invalidMessage(r, i+1, m); err != nil {
				return river.JobCancel(err)
			}
		}
		if enqueue, ok := ctx.Value(partsKey{}).(enqueuePartsFunc); ok && len(msgs) > 1 {
			if err := enqueue(ctx, len(msgs)); err != nil {
				return fmt.Errorf("action: notify case %s: %w", r.CaseID, err)
			}
			return nil
		}
		for i, m := range msgs {
			if err := n.Send(ctx, m); err != nil {
				return fmt.Errorf("action: notify case %s: send message %d: %w", r.CaseID, i+1, err)
			}
		}
		return nil
	})
}

func sendPart(ctx context.Context, n notification.Notifier, r Run, msgs []*notification.Message) error {
	if r.Part > len(msgs) {
		return river.JobCancel(fmt.Errorf("action: notify case %s: message %d: build returned %d messages", r.CaseID, r.Part, len(msgs)))
	}
	m := msgs[r.Part-1]
	if err := invalidMessage(r, r.Part, m); err != nil {
		return river.JobCancel(err)
	}
	if err := n.Send(ctx, m); err != nil {
		return fmt.Errorf("action: notify case %s: send message %d: %w", r.CaseID, r.Part, err)
	}
	return nil
}

func invalidMessage(r Run, n int, m *notification.Message) error {
	if m == nil {
		return fmt.Errorf("action: notify case %s: message %d is nil", r.CaseID, n)
	}
	if err := m.Validate(); err != nil {
		return fmt.Errorf("action: notify case %s: message %d: %w", r.CaseID, n, err)
	}
	return nil
}
