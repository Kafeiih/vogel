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
		CaseID: uuid.New(), Seq: 3, Effect: "on_draft", Definition: "purchase", Version: 2,
		Domain: "compras", ExternalID: "s-1", Unit: "finanzas", ActorID: "u1",
	}

	require.NoError(t, action.NewWorker(r).Work(context.Background(), job(args)))

	assert.Equal(t, action.Run{
		CaseID: args.CaseID, Seq: 3, Effect: "on_draft", Definition: "purchase", Version: 2,
		Domain: "compras", ExternalID: "s-1", Unit: "finanzas", ActorID: "u1", Attempt: 2,
	}, got)
}

func TestWorker_UnknownEffect_CancelsJob(t *testing.T) {
	r, _ := newRegistry(t)

	err := action.NewWorker(r).Work(context.Background(), job(action.Args{Effect: "missing"}))

	var cancel *rivertype.JobCancelError
	require.ErrorAs(t, err, &cancel, "an unknown effect can never succeed: cancel, do not retry")
	assert.ErrorIs(t, err, workflow.ErrUnknownEffect)
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
