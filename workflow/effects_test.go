package workflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/pgxtx"
)

// dispatchCall records one EffectSink.Dispatch invocation.
type dispatchCall struct {
	db     pgxtx.DBTX
	def    Definition
	c      Case
	events []Event
}

// recordingSink is an EffectSink that knows a fixed set of effect names,
// records every Dispatch call, and returns err from Dispatch when set.
type recordingSink struct {
	known map[string]bool
	err   error

	mu    sync.Mutex
	calls []dispatchCall
}

func newRecordingSink(names ...string) *recordingSink {
	known := make(map[string]bool, len(names))
	for _, n := range names {
		known[n] = true
	}
	return &recordingSink{known: known}
}

func (s *recordingSink) HasEffect(name string) bool { return s.known[name] }

func (s *recordingSink) Dispatch(_ context.Context, db pgxtx.DBTX, d Definition, c Case, events []Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]Event, len(events))
	copy(cp, events)
	s.calls = append(s.calls, dispatchCall{db: db, def: d, c: c, events: cp})
	return s.err
}

func (s *recordingSink) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *recordingSink) lastCall(t *testing.T) dispatchCall {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.NotEmpty(t, s.calls)
	return s.calls[len(s.calls)-1]
}

// effectsDefinition is decisionToTerminalDefinition with effects declared on
// the start node, a task node, a terminal node, a human transition, and both
// decision-node routes.
func effectsDefinition() Definition {
	d := decisionToTerminalDefinition()
	for i := range d.Nodes {
		switch d.Nodes[i].ID {
		case "draft":
			d.Nodes[i].Effects = []string{"notify-owner"}
		case "review":
			d.Nodes[i].Effects = []string{"notify-approver"}
		case "approved":
			d.Nodes[i].Effects = []string{"document.generate", "notify-owner"}
		}
	}
	for i := range d.Transitions {
		switch d.Transitions[i].Action {
		case "approve":
			d.Transitions[i].Effects = []string{"audit-approval"}
		case "to-approved", "to-rejected":
			d.Transitions[i].Effects = []string{"webhook"}
		}
	}
	return d
}

func allEffectsSink() *recordingSink {
	return newRecordingSink("notify-owner", "notify-approver", "document.generate", "audit-approval", "webhook")
}

func eventKinds(events []Event) []EventKind {
	out := make([]EventKind, len(events))
	for i := range events {
		out[i] = events[i].Kind
	}
	return out
}

func eventSeqs(events []Event) []int64 {
	out := make([]int64, len(events))
	for i := range events {
		out[i] = events[i].Seq
	}
	return out
}

func TestDefinition_EffectNames_SortedAndDeduped(t *testing.T) {
	assert.Equal(t,
		[]string{"audit-approval", "document.generate", "notify-approver", "notify-owner", "webhook"},
		effectsDefinition().EffectNames())
}

func TestDefinition_EffectNames_EmptyWhenNoEffects(t *testing.T) {
	assert.Empty(t, testDefinition().EffectNames())
}

func TestDefinition_Validate_EffectsDefinition_ReturnsNil(t *testing.T) {
	assert.NoError(t, effectsDefinition().Validate())
}

func TestDefinition_Validate_RejectsEffectViolations(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(d *Definition)
		wantMsg string
	}{
		{
			name:    "empty node effect name",
			mutate:  func(d *Definition) { d.Nodes[1].Effects = []string{"ok", ""} },
			wantMsg: `node "review": effect[1]: empty name`,
		},
		{
			name:    "duplicate node effect name",
			mutate:  func(d *Definition) { d.Nodes[1].Effects = []string{"notify", "notify"} },
			wantMsg: `node "review": duplicate effect "notify"`,
		},
		{
			name:    "empty transition effect name",
			mutate:  func(d *Definition) { d.Transitions[0].Effects = []string{""} },
			wantMsg: `transition[0]: effect[0]: empty name`,
		},
		{
			name:    "duplicate transition effect name",
			mutate:  func(d *Definition) { d.Transitions[0].Effects = []string{"a", "b", "a"} },
			wantMsg: `transition[0]: duplicate effect "a"`,
		},
		{
			name:    "decision node declares effects",
			mutate:  func(d *Definition) { d.Nodes[2].Effects = []string{"notify"} },
			wantMsg: `decision node "route": must not declare effects`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := decisionToTerminalDefinition()
			tt.mutate(&d)
			err := d.Validate()
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalidDefinition))
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}

func TestDefinition_Validate_SameEffectOnDifferentNodesAndTransitions_IsAllowed(t *testing.T) {
	d := testDefinition()
	d.Nodes[0].Effects = []string{"notify"}
	d.Nodes[1].Effects = []string{"notify"}
	d.Transitions[0].Effects = []string{"notify"}
	d.Transitions[1].Effects = []string{"notify"}
	assert.NoError(t, d.Validate())
}

func TestRegister_EffectWithoutSink_ReturnsErrUnknownEffect(t *testing.T) {
	e := New(newFakeRepository(), Config{}, testLogger())

	err := e.Register(effectsDefinition())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnknownEffect))
	assert.Empty(t, e.Definitions())
}

func TestRegister_EffectUnknownToSink_ReturnsErrUnknownEffect(t *testing.T) {
	sink := newRecordingSink("notify-owner", "notify-approver", "audit-approval", "webhook") // no document.generate
	e := New(newFakeRepository(), Config{}, testLogger(), WithEffectSink(sink))

	err := e.Register(effectsDefinition())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnknownEffect))
	assert.Contains(t, err.Error(), "document.generate")
	assert.False(t, strings.Contains(err.Error(), "notify-owner"), "known effects must not be reported: %v", err)
	assert.Empty(t, e.Definitions())
}

func TestRegister_EffectsKnownToSink_Succeeds(t *testing.T) {
	e := New(newFakeRepository(), Config{}, testLogger(), WithEffectSink(allEffectsSink()))

	require.NoError(t, e.Register(effectsDefinition()))
	assert.Len(t, e.Definitions(), 1)
}

func TestNew_EffectSinkHonoredRegardlessOfOptionOrder(t *testing.T) {
	orders := map[string]func(s EffectSink) []Option{
		"sink before definitions": func(s EffectSink) []Option {
			return []Option{WithEffectSink(s), WithDefinitions(effectsDefinition())}
		},
		"sink after definitions": func(s EffectSink) []Option {
			return []Option{WithDefinitions(effectsDefinition()), WithEffectSink(s)}
		},
	}
	for name, opts := range orders {
		t.Run(name, func(t *testing.T) {
			e := New(newFakeRepository(), Config{}, testLogger(), opts(allEffectsSink())...)
			defs := e.Definitions()
			require.Len(t, defs, 1)
			assert.Equal(t, "purchase", defs[0].Name)
		})
	}
}

func TestOpen_DispatchesOnceWithOpenedEvent(t *testing.T) {
	repo := newFakeRepository()
	sink := allEffectsSink()
	e := New(repo, Config{}, testLogger(), WithEffectSink(sink), WithDefinitions(effectsDefinition()))
	db := &sentinelDBTX{}

	c, err := e.Open(context.Background(), db, OpenInput{Definition: "purchase", Domain: "compras", ExternalID: "s-1", ActorID: "u1"})
	require.NoError(t, err)

	require.Equal(t, 1, sink.callCount())
	call := sink.lastCall(t)
	assert.Same(t, db, call.db, "Dispatch must receive the caller's db")
	assert.Equal(t, *c, call.c)
	assert.Equal(t, effectsDefinition().Key(), call.def.Key())
	require.Len(t, call.events, 1)
	assert.Equal(t, EventOpened, call.events[0].Kind)
	assert.Equal(t, int64(1), call.events[0].Seq)
	assert.Equal(t, c.ID, call.events[0].CaseID)

	node, ok := call.def.Node(call.events[0].ToState)
	require.True(t, ok)
	assert.Equal(t, []string{"notify-owner"}, node.Effects)

	history, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, history, call.events, "Dispatch events must be exactly the persisted ones")
}

func TestMove_DispatchesOnceWithAllHopEventsInSeqOrder(t *testing.T) {
	repo := newFakeRepository()
	sink := allEffectsSink()
	e := New(repo, Config{}, testLogger(), WithEffectSink(sink), WithDefinitions(effectsDefinition()))
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysFalseGuard))
	db := &sentinelDBTX{}

	c, err := e.Open(context.Background(), db, OpenInput{Definition: "purchase", Domain: "compras", ExternalID: "s-1", ActorID: "u1"})
	require.NoError(t, err)
	_, err = e.Move(context.Background(), db, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	require.Equal(t, 2, sink.callCount())
	submitCall := sink.lastCall(t)
	assert.Equal(t, []EventKind{EventMoved}, eventKinds(submitCall.events))
	assert.Equal(t, []int64{2}, eventSeqs(submitCall.events))

	moved, err := e.Move(context.Background(), db, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2", Comment: ""})
	require.NoError(t, err)

	require.Equal(t, 3, sink.callCount(), "each Move dispatches exactly once")
	call := sink.lastCall(t)
	assert.Same(t, db, call.db)
	assert.Equal(t, *moved, call.c, "Dispatch receives the final, persisted case")
	assert.Equal(t, StatusClosed, call.c.Status)
	assert.Equal(t, []EventKind{EventMoved, EventMoved, EventClosed}, eventKinds(call.events))
	assert.Equal(t, []int64{3, 4, 5}, eventSeqs(call.events))
	assert.Equal(t, "approve", call.events[0].Action)
	assert.Equal(t, "to-approved", call.events[1].Action)
	assert.Equal(t, "route", call.events[1].FromState)
	assert.Equal(t, "approved", call.events[1].ToState)

	history, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, history[2:], call.events, "Dispatch events must be exactly the ones this Move appended")
}

func TestOpen_DispatchError_Propagates(t *testing.T) {
	sinkErr := errors.New("enqueue failed")
	sink := allEffectsSink()
	sink.err = sinkErr
	e := New(newFakeRepository(), Config{}, testLogger(), WithEffectSink(sink), WithDefinitions(effectsDefinition()))

	c, err := e.Open(context.Background(), nil, OpenInput{Definition: "purchase", Domain: "compras", ExternalID: "s-1"})
	require.Error(t, err)
	assert.Nil(t, c)
	assert.True(t, errors.Is(err, sinkErr))
}

func TestMove_DispatchError_Propagates(t *testing.T) {
	sinkErr := errors.New("enqueue failed")
	sink := allEffectsSink()
	e := New(newFakeRepository(), Config{}, testLogger(), WithEffectSink(sink), WithDefinitions(effectsDefinition()))
	c, err := e.Open(context.Background(), nil, OpenInput{Definition: "purchase", Domain: "compras", ExternalID: "s-1"})
	require.NoError(t, err)

	sink.err = sinkErr
	moved, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.Error(t, err)
	assert.Nil(t, moved)
	assert.True(t, errors.Is(err, sinkErr))
}

func TestMove_FailureBeforePersisting_DoesNotDispatch(t *testing.T) {
	repo := newFakeRepository()
	sink := allEffectsSink()
	e := New(repo, Config{}, testLogger(), WithEffectSink(sink), WithDefinitions(effectsDefinition()))
	guardErr := errors.New("guard boom")
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", func(context.Context, pgxtx.DBTX, Case) (bool, error) {
		return false, guardErr
	}))

	c, err := e.Open(context.Background(), nil, OpenInput{Definition: "purchase", Domain: "compras", ExternalID: "s-1"})
	require.NoError(t, err)
	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit"})
	require.NoError(t, err)
	require.Equal(t, 2, sink.callCount())

	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve"})
	require.ErrorIs(t, err, guardErr)
	assert.Equal(t, 2, sink.callCount(), "a Move that fails before persisting must not dispatch")
}

func TestClaimRelease_DoNotDispatch(t *testing.T) {
	sink := allEffectsSink()
	e := New(newFakeRepository(), Config{}, testLogger(), WithEffectSink(sink), WithDefinitions(effectsDefinition()))
	c, err := e.Open(context.Background(), nil, OpenInput{Definition: "purchase", Domain: "compras", ExternalID: "s-1"})
	require.NoError(t, err)
	require.Equal(t, 1, sink.callCount())

	_, err = e.Claim(context.Background(), nil, c.ID, "u1")
	require.NoError(t, err)
	_, err = e.Release(context.Background(), nil, c.ID, "u1")
	require.NoError(t, err)
	assert.Equal(t, 1, sink.callCount())
}

func TestNoEffectSink_OpenAndMoveUnchanged(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	require.NoError(t, e.RegisterGuard("under-budget", alwaysTrueGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	closed, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.NoError(t, err)
	assert.Equal(t, StatusClosed, closed.Status)

	history, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, []EventKind{EventOpened, EventMoved, EventMoved, EventClosed}, eventKinds(history))
	assert.Equal(t, []int64{1, 2, 3, 4}, eventSeqs(history))
}
