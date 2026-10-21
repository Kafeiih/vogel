package action_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/action"
	"github.com/kafeiih/vogel/notification"
)

type recordingNotifier struct {
	sent []*notification.Message
	err  error
}

func (n *recordingNotifier) Send(_ context.Context, m *notification.Message) error {
	if n.err != nil {
		return n.err
	}
	n.sent = append(n.sent, m)
	return nil
}

func msg(to string) *notification.Message {
	return &notification.Message{To: to, Subject: "Caso actualizado", Text: "hola"}
}

func TestNotifyHandler_SendsEveryBuiltMessage(t *testing.T) {
	n := &recordingNotifier{}
	var gotRun action.Run
	h := action.NotifyHandler(n, func(_ context.Context, r action.Run) ([]*notification.Message, error) {
		gotRun = r
		return []*notification.Message{msg("a@example.cl"), msg("b@example.cl")}, nil
	})

	run := action.Run{Effect: "notify", ExternalID: "s-1"}
	require.NoError(t, h.Handle(context.Background(), run))

	assert.Equal(t, run, gotRun)
	require.Len(t, n.sent, 2)
	assert.Equal(t, "a@example.cl", n.sent[0].To)
	assert.Equal(t, "b@example.cl", n.sent[1].To)
}

func TestNotifyHandler_InvalidMessage_CancelsAndSendsNothing(t *testing.T) {
	n := &recordingNotifier{}
	h := action.NotifyHandler(n, func(context.Context, action.Run) ([]*notification.Message, error) {
		return []*notification.Message{msg("a@example.cl"), {To: "not-an-address", Subject: "x", Text: "y"}}, nil
	})

	err := h.Handle(context.Background(), action.Run{})

	require.Error(t, err)
	assert.True(t, action.IsCancel(err), "an invalid message never becomes valid on retry")
	assert.Empty(t, n.sent, "messages are validated before any is sent")
}

func TestNotifyHandler_PropagatesBuildAndSendErrors(t *testing.T) {
	buildErr := errors.New("lookup failed")
	h := action.NotifyHandler(&recordingNotifier{}, func(context.Context, action.Run) ([]*notification.Message, error) {
		return nil, buildErr
	})
	require.ErrorIs(t, h.Handle(context.Background(), action.Run{}), buildErr)

	sendErr := errors.New("smtp down")
	h = action.NotifyHandler(&recordingNotifier{err: sendErr}, func(context.Context, action.Run) ([]*notification.Message, error) {
		return []*notification.Message{msg("a@example.cl")}, nil
	})
	err := h.Handle(context.Background(), action.Run{})
	require.ErrorIs(t, err, sendErr)
	assert.False(t, action.IsCancel(err), "a send failure is retried")
}

func TestNotifyHandler_NilMessage_NamesItsPosition(t *testing.T) {
	h := action.NotifyHandler(&recordingNotifier{}, func(context.Context, action.Run) ([]*notification.Message, error) {
		return []*notification.Message{msg("a@example.cl"), nil}, nil
	})

	err := h.Handle(context.Background(), action.Run{})

	require.Error(t, err)
	assert.True(t, action.IsCancel(err))
	assert.Contains(t, err.Error(), "message 2 is nil")
}
