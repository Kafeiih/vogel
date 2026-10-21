package action_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/action"
	"github.com/kafeiih/vogel/notification"
)

type partsQueue struct {
	recordingQueue
	parts []enqueued
	err   error
}

func (q *partsQueue) Enqueue(_ context.Context, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	if q.err != nil {
		return nil, q.err
	}
	a, ok := args.(action.Args)
	if !ok {
		q.t.Fatalf("unexpected job args type %T", args)
	}
	q.parts = append(q.parts, enqueued{args: a, opts: opts})
	return &rivertype.JobInsertResult{Job: &rivertype.JobRow{}}, nil
}

func (q *partsQueue) EnqueueTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	q.t.Fatal("parts are enqueued outside a transaction")
	return nil, nil
}

type flakyNotifier struct {
	sent   []string
	failTo string
}

func (n *flakyNotifier) Send(_ context.Context, m *notification.Message) error {
	if m.To == n.failTo {
		return errors.New("smtp down")
	}
	n.sent = append(n.sent, m.To)
	return nil
}

func notifyRegistry(t *testing.T, n notification.Notifier, to ...string) (*action.Registry, *partsQueue) {
	t.Helper()
	q := &partsQueue{recordingQueue: recordingQueue{t: t}}
	r := action.NewRegistry(q)
	require.NoError(t, r.Register("notify", action.NotifyHandler(n, func(context.Context, action.Run) ([]*notification.Message, error) {
		msgs := make([]*notification.Message, 0, len(to))
		for _, addr := range to {
			msgs = append(msgs, msg(addr))
		}
		return msgs, nil
	})))
	return r, q
}

func TestNotifyWorker_SeveralMessages_EnqueuesOneJobPerMessage(t *testing.T) {
	n := &flakyNotifier{}
	r, q := notifyRegistry(t, n, "a@example.cl", "b@example.cl")
	args := action.Args{CaseID: uuid.New(), Seq: 4, Effect: "notify", Domain: "compras", ExternalID: "s-1"}

	require.NoError(t, action.NewWorker(r).Work(context.Background(), job(args)))

	assert.Empty(t, n.sent, "the effect job only fans out; each message job sends its own")
	require.Len(t, q.parts, 2)
	for i, p := range q.parts {
		want := args
		want.Part = i + 1
		assert.Equal(t, want, p.args)
		require.NotNil(t, p.opts)
		assert.True(t, p.opts.UniqueOpts.ByArgs, "a retried effect job must not enqueue its message jobs twice")
	}
}

func TestNotifyWorker_MessageJob_SendsOnlyItsMessage(t *testing.T) {
	n := &flakyNotifier{}
	r, q := notifyRegistry(t, n, "a@example.cl", "b@example.cl")

	require.NoError(t, action.NewWorker(r).Work(context.Background(), job(action.Args{Effect: "notify", Part: 2})))

	assert.Equal(t, []string{"b@example.cl"}, n.sent)
	assert.Empty(t, q.parts)
}

func TestNotifyWorker_FailedMessageJob_RetriesOnlyThatMessage(t *testing.T) {
	n := &flakyNotifier{failTo: "b@example.cl"}
	r, _ := notifyRegistry(t, n, "a@example.cl", "b@example.cl")
	w := action.NewWorker(r)

	require.NoError(t, w.Work(context.Background(), job(action.Args{Effect: "notify", Part: 1})))
	err := w.Work(context.Background(), job(action.Args{Effect: "notify", Part: 2}))

	require.Error(t, err)
	assert.False(t, action.IsCancel(err), "a send failure is retried")
	assert.Equal(t, []string{"a@example.cl"}, n.sent, "the message that went out is not sent again")
}

func TestNotifyWorker_MessageJobPastTheBuiltMessages_IsCancelled(t *testing.T) {
	n := &flakyNotifier{}
	r, _ := notifyRegistry(t, n, "a@example.cl")

	err := action.NewWorker(r).Work(context.Background(), job(action.Args{Effect: "notify", Part: 3}))

	require.Error(t, err)
	assert.True(t, action.IsCancel(err), "build no longer returns that message")
	assert.Contains(t, err.Error(), "message 3: build returned 1 messages", "the error names the job's 1-based part")
	assert.Empty(t, n.sent)
}

func TestNotifyWorker_SingleMessage_IsSentWithoutFanOut(t *testing.T) {
	n := &flakyNotifier{}
	r, q := notifyRegistry(t, n, "a@example.cl")

	require.NoError(t, action.NewWorker(r).Work(context.Background(), job(action.Args{Effect: "notify"})))

	assert.Equal(t, []string{"a@example.cl"}, n.sent)
	assert.Empty(t, q.parts)
}

func TestNotifyWorker_FanOutEnqueueError_IsRetried(t *testing.T) {
	n := &flakyNotifier{}
	r, q := notifyRegistry(t, n, "a@example.cl", "b@example.cl")
	q.err = errors.New("db down")

	err := action.NewWorker(r).Work(context.Background(), job(action.Args{Effect: "notify"}))

	require.ErrorIs(t, err, q.err)
	assert.False(t, action.IsCancel(err))
	assert.Empty(t, n.sent)
}
