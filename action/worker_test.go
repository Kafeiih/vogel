package action_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/action"
	"github.com/kafeiih/vogel/workflow"
)

func job(args action.Args) *river.Job[action.Args] {
	return &river.Job[action.Args]{JobRow: &rivertype.JobRow{Attempt: 2}, Args: args}
}

func TestWorker_RunsRegisteredHandlerWithRun(t *testing.T) {
	r, _ := newRegistry(t)
	var got action.Run
	require.NoError(t, r.Register("on_draft", action.HandlerFunc(func(_ context.Context, run action.Run) error {
		got = run
		return nil
	})))
	args := action.Args{
		CaseID: uuid.New(), TokenID: uuid.New(), Seq: 3, Effect: "on_draft", Definition: "purchase", Version: 2,
		Domain: "compras", ExternalID: "s-1", Unit: "finanzas", ActorID: "u1",
	}

	require.NoError(t, action.NewWorker(r).Work(context.Background(), job(args)))

	assert.Equal(t, action.Run{
		CaseID: args.CaseID, TokenID: args.TokenID, Seq: 3, Effect: "on_draft", Definition: "purchase", Version: 2,
		Domain: "compras", ExternalID: "s-1", Unit: "finanzas", ActorID: "u1", Attempt: 2,
		Actor: workflow.Actor{Kind: workflow.ActorUnspecified, ID: "u1"},
	}, got, "a job enqueued before Args carried Actor reads its actor as unspecified, like an old event")
}

func TestWorker_PassesStructuredActor(t *testing.T) {
	r, _ := newRegistry(t)
	var got action.Run
	require.NoError(t, r.Register("on_draft", action.HandlerFunc(func(_ context.Context, run action.Run) error {
		got = run
		return nil
	})))
	actor := workflow.Actor{Kind: workflow.ActorHuman, ID: "u1", Via: "web"}

	require.NoError(t, action.NewWorker(r).Work(context.Background(), job(action.Args{Effect: "on_draft", Actor: actor, ActorID: "u1"})))

	assert.Equal(t, actor, got.Actor)
	assert.Equal(t, "u1", got.ActorID)
}

func TestWorker_UnknownEffect_IsRetried(t *testing.T) {
	r, _ := newRegistry(t)

	err := action.NewWorker(r).Work(context.Background(), job(action.Args{Effect: "missing"}))

	require.ErrorIs(t, err, workflow.ErrUnknownEffect)
	var cancel *rivertype.JobCancelError
	assert.False(t, errors.As(err, &cancel), "a worker from an older deploy may not know the effect yet: retry, do not cancel")
}

func TestWorker_HandlerError_IsReturnedForRetry(t *testing.T) {
	r, _ := newRegistry(t)
	boom := errors.New("smtp down")
	require.NoError(t, r.Register("notify", action.HandlerFunc(func(context.Context, action.Run) error { return boom })))

	err := action.NewWorker(r).Work(context.Background(), job(action.Args{Effect: "notify"}))

	require.ErrorIs(t, err, boom)
	var cancel *rivertype.JobCancelError
	assert.False(t, errors.As(err, &cancel), "an ordinary handler error must be retried, not cancelled")
}

func TestAddWorker_RegistersOnRiverWorkers(t *testing.T) {
	r, _ := newRegistry(t)
	workers := river.NewWorkers()

	require.NoError(t, action.AddWorker(workers, action.NewWorker(r)))
	require.Error(t, action.AddWorker(workers, action.NewWorker(r)), "a second worker for the same kind is an error")
}
