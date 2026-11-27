package action_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/action"
	"github.com/kafeiih/vogel/workflow"
)

// recordingHandler returns a Handler that appends the Run it receives, tagged
// with label, to *got.
func recordingHandler(label string, got *[]string) action.Handler {
	return action.HandlerFunc(func(_ context.Context, r action.Run) error {
		*got = append(*got, label+"="+r.Effect)
		return nil
	})
}

func TestRegisterPrefix_RejectsEmptyPrefixNilHandlerAndDuplicate(t *testing.T) {
	r, _ := newRegistry(t)

	require.ErrorIs(t, r.RegisterPrefix("", nopHandler()), action.ErrInvalidRegistration)
	require.ErrorIs(t, r.RegisterPrefix("document:", nil), action.ErrInvalidRegistration)
	require.NoError(t, r.RegisterPrefix("document:", nopHandler()))
	require.ErrorIs(t, r.RegisterPrefix("document:", nopHandler()), action.ErrDuplicateEffect)
	require.NoError(t, r.Register("document:", nopHandler()),
		"an exact name and a prefix with the same text live apart")
}

func TestHasEffect_MatchesPrefixWithNonEmptyRest(t *testing.T) {
	r, _ := newRegistry(t)
	require.NoError(t, r.RegisterPrefix("document:", nopHandler()))

	assert.True(t, r.HasEffect("document:comprobante"))
	assert.True(t, r.HasEffect("document:acta"))
	assert.False(t, r.HasEffect("document:"), "the prefix alone names no effect")
	assert.False(t, r.HasEffect("document"))
	assert.False(t, r.HasEffect("notify"))
	assert.False(t, r.HasEffect(""))
}

func TestWorker_ExactNameWinsOverPrefix_LongestPrefixWins(t *testing.T) {
	r, _ := newRegistry(t)
	var got []string
	require.NoError(t, r.RegisterPrefix("doc", recordingHandler("doc", &got)))
	require.NoError(t, r.RegisterPrefix("document:", recordingHandler("document:", &got)))
	require.NoError(t, r.Register("document:acta", recordingHandler("exact", &got)))
	w := action.NewWorker(r)

	for _, effect := range []string{"document:acta", "document:comprobante", "docket"} {
		require.NoError(t, w.Work(context.Background(), job(action.Args{Effect: effect})))
	}

	assert.Equal(t, []string{
		"exact=document:acta",
		"document:=document:comprobante",
		"doc=docket",
	}, got)
}

func TestWorker_PrefixHandlerReceivesFullEffectName(t *testing.T) {
	r, _ := newRegistry(t)
	var got action.Run
	require.NoError(t, r.RegisterPrefix("document:", action.HandlerFunc(func(_ context.Context, run action.Run) error {
		got = run
		return nil
	})))
	caseID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	require.NoError(t, action.NewWorker(r).Work(context.Background(), job(action.Args{CaseID: caseID, Seq: 7, Effect: "document:acta"})))

	assert.Equal(t, "document:acta", got.Effect)
	assert.Equal(t, caseID.String()+"/7/document:acta", got.IdempotencyKey())
	assert.Equal(t, action.Kind+":document:acta", got.SystemActor().ID)
}

func TestWorker_NameOutsideEveryPrefix_IsRetriedAsUnknown(t *testing.T) {
	r, _ := newRegistry(t)
	require.NoError(t, r.RegisterPrefix("document:", nopHandler()))

	err := action.NewWorker(r).Work(context.Background(), job(action.Args{Effect: "pdf:acta"}))

	require.ErrorIs(t, err, workflow.ErrUnknownEffect)
	assert.False(t, action.IsCancel(err))
}

func prefixDefinition(effects ...string) workflow.Definition {
	return workflow.Definition{
		Name:    "bpms",
		Version: 1,
		Nodes: []workflow.Node{
			{ID: "draft", Start: true, Effects: effects},
			{ID: "done", Terminal: true},
		},
		Transitions: []workflow.Transition{{From: "draft", To: "done", Action: "finish"}},
	}
}

func TestEngineRegister_PrefixKeepsFailFastCheck(t *testing.T) {
	r, _ := newRegistry(t, "notify")
	require.NoError(t, r.RegisterPrefix("document:", nopHandler()))
	e := workflow.New(nil, workflow.Config{}, nil, workflow.WithEffectSink(r))

	require.NoError(t, e.Register(prefixDefinition("document:comprobante", "document:acta", "notify")))

	err := e.Register(prefixDefinition("pdf:acta"))
	require.ErrorIs(t, err, workflow.ErrUnknownEffect)
	assert.Contains(t, err.Error(), "pdf:acta")

	err = e.Register(prefixDefinition("document:"))
	require.ErrorIs(t, err, workflow.ErrUnknownEffect, "the bare prefix is not an effect")
}

func TestDispatch_PrefixEffectsEnqueueOneJobPerFullName(t *testing.T) {
	r, q := newRegistry(t)
	require.NoError(t, r.RegisterPrefix("document:", nopHandler()))
	events := []workflow.Event{{Seq: 1, Kind: workflow.EventOpened, ToState: "draft"}}

	err := r.Dispatch(context.Background(), fakeTx{}, prefixDefinition("document:comprobante", "document:acta"), testCase(), events)

	require.NoError(t, err)
	assert.Equal(t, []string{"document:comprobante", "document:acta"}, effectsOf(q.calls))
}
