package workflow

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/pgxtx"
)

// fakeRepository is an in-memory Repository used to test Engine without a
// database. Mirrors audit/recorder_test.go's fakeRepository style.
type fakeRepository struct {
	mu      sync.Mutex
	cases   map[uuid.UUID]Case
	byExtID map[string]uuid.UUID
	events  map[uuid.UUID][]Event
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{
		cases:   make(map[uuid.UUID]Case),
		byExtID: make(map[string]uuid.UUID),
		events:  make(map[uuid.UUID][]Event),
	}
}

func extKey(domain, externalID string) string {
	return domain + "\x00" + externalID
}

func (f *fakeRepository) Create(_ context.Context, _ pgxtx.DBTX, c *Case) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	key := extKey(c.Domain, c.ExternalID)
	if _, exists := f.byExtID[key]; exists {
		return ErrCaseExists
	}
	f.cases[c.ID] = *c
	f.byExtID[key] = c.ID
	return nil
}

func (f *fakeRepository) GetByID(_ context.Context, _ pgxtx.DBTX, id uuid.UUID) (*Case, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	c, ok := f.cases[id]
	if !ok {
		return nil, ErrCaseNotFound
	}
	cp := c
	return &cp, nil
}

func (f *fakeRepository) GetByExternalID(_ context.Context, _ pgxtx.DBTX, domain, externalID string) (*Case, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	id, ok := f.byExtID[extKey(domain, externalID)]
	if !ok {
		return nil, ErrCaseNotFound
	}
	c := f.cases[id]
	return &c, nil
}

func (f *fakeRepository) Update(_ context.Context, _ pgxtx.DBTX, c *Case) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.cases[c.ID]; !ok {
		return ErrCaseNotFound
	}
	f.cases[c.ID] = *c
	return nil
}

func (f *fakeRepository) AppendEvent(_ context.Context, _ pgxtx.DBTX, e *Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.events[e.CaseID] = append(f.events[e.CaseID], *e)
	return nil
}

func (f *fakeRepository) ListEvents(_ context.Context, _ pgxtx.DBTX, caseID uuid.UUID) ([]Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]Event, len(f.events[caseID]))
	copy(out, f.events[caseID])
	return out, nil
}

func (f *fakeRepository) ListByEligibility(_ context.Context, _ pgxtx.DBTX, filter InboxFilter) ([]Case, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []Case
	for _, c := range f.cases {
		if filter.Domain != "" && c.Domain != filter.Domain {
			continue
		}
		if len(filter.States) > 0 && !containsString(filter.States, c.State) {
			continue
		}
		if filter.AssignedTo != "" && c.AssignedTo != filter.AssignedTo {
			continue
		}
		if filter.Unassigned && c.AssignedTo != "" {
			continue
		}
		if filter.Overdue && (c.DeadlineAt == nil || !c.DeadlineAt.Before(time.Now())) {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// sentinelDBTX is a minimal pgxtx.DBTX whose only job is to be a distinct,
// identifiable value: it exists so a test can prove the engine passed
// through its own db argument to a guard, rather than a different value
// (such as nil), by pointer identity.
type sentinelDBTX struct{}

func (*sentinelDBTX) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (*sentinelDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (*sentinelDBTX) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testDefinition returns a purchase-request flow: draft -> review ->
// {approved, rejected}. The approve transition is guarded so tests can
// exercise guard registration, rejection, and error propagation.
func testDefinition() Definition {
	return Definition{
		Name:    "purchase",
		Version: 1,
		Nodes: []Node{
			{ID: "draft", Start: true, Eligible: ByPosition("buyer")},
			{ID: "review", Eligible: ByPositionInUnit("approver", "finance"), Deadline: 48 * time.Hour},
			{ID: "approved", Terminal: true},
			{ID: "rejected", Terminal: true},
		},
		Transitions: []Transition{
			{From: "draft", To: "review", Action: "submit"},
			{From: "review", To: "approved", Action: "approve", Guard: "under-budget"},
			{From: "review", To: "rejected", Action: "reject"},
		},
	}
}

func alwaysTrueGuard(context.Context, pgxtx.DBTX, Case) (bool, error)  { return true, nil }
func alwaysFalseGuard(context.Context, pgxtx.DBTX, Case) (bool, error) { return false, nil }

// commentTestDefinition mirrors testDefinition's shape but declares comment
// policies matching the real consumer's intent: approving is an optional
// comment, rejecting requires an observation, and submitting carries no
// comment policy at all (the zero value, CommentNone).
func commentTestDefinition() Definition {
	d := testDefinition()
	d.Transitions[1].Comment = CommentOptional // review -> approved
	d.Transitions[2].Comment = CommentRequired // review -> rejected
	return d
}

// commentGuardTestDefinition extends commentTestDefinition by attaching a
// guard to each comment-policed transition that isn't already guarded: the
// CommentNone submit transition and the CommentRequired reject transition.
// It exists to prove the ordering documented on Engine.Move — the comment
// policy is enforced before the guard runs — since commentTestDefinition
// alone can't: its only guarded transition (approve) uses CommentOptional,
// which never rejects a comment, so the guard would run regardless of
// ordering.
//
// Transitions are looked up by Action rather than by slice index, and each
// one's Comment policy is asserted before the guard is attached: indexing
// into d.Transitions directly would silently stop proving the ordering if
// the transitions were ever reordered, since a guard could then land on the
// wrong (or a differently comment-policed) transition without any test
// failing.
func commentGuardTestDefinition() Definition {
	d := commentTestDefinition()

	submit, ok := findTransitionByAction(d, "draft", "submit")
	if !ok {
		panic("commentGuardTestDefinition: no draft->submit transition")
	}
	if submit.Comment.Canonical() != CommentNone {
		panic("commentGuardTestDefinition: draft->submit must be CommentNone")
	}
	d.Transitions[submit.index].Guard = "count-calls"

	reject, ok := findTransitionByAction(d, "review", "reject")
	if !ok {
		panic("commentGuardTestDefinition: no review->reject transition")
	}
	if reject.Comment != CommentRequired {
		panic("commentGuardTestDefinition: review->reject must be CommentRequired")
	}
	d.Transitions[reject.index].Guard = "count-calls"

	return d
}

// indexedTransition pairs a Transition with its position in Definition.
// Transitions, so a caller that looked it up by (From, Action) can still
// mutate the original slice in place.
type indexedTransition struct {
	Transition
	index int
}

// findTransitionByAction returns the transition leaving from with the given
// action, together with its index in d.Transitions.
func findTransitionByAction(d Definition, from, action string) (indexedTransition, bool) {
	for i, tr := range d.Transitions {
		if tr.From == from && tr.Action == action {
			return indexedTransition{Transition: tr, index: i}, true
		}
	}
	return indexedTransition{}, false
}

// callCountingGuard is a GuardFunc that always allows the transition, and
// records how many times it was invoked, so a test can assert a guard never
// ran when the comment policy should have rejected the Move first.
type callCountingGuard struct {
	mu    sync.Mutex
	calls int
}

func (g *callCountingGuard) fn(context.Context, pgxtx.DBTX, Case) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	return true, nil
}

func (g *callCountingGuard) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

func openTestCase(t *testing.T, e *Engine) *Case {
	t.Helper()
	c, err := e.Open(context.Background(), nil, OpenInput{
		Definition: "purchase",
		Domain:     "compras",
		ExternalID: "solicitud-1",
		Unit:       "finance",
		ActorID:    "u1",
	})
	require.NoError(t, err)
	return c
}

func TestOpen_AtStartNode(t *testing.T) {
	repo := newFakeRepository()
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	e := New(repo, Config{Now: func() time.Time { return now }}, testLogger(), WithDefinitions(testDefinition()))

	c := openTestCase(t, e)

	assert.Equal(t, "draft", c.State)
	assert.Equal(t, StatusOpen, c.Status)
	assert.Equal(t, now, c.OpenedAt)
	assert.Nil(t, c.DeadlineAt) // draft has no deadline configured
	assert.Equal(t, "purchase", c.Definition)
	assert.Equal(t, 1, c.Version)

	events, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, EventOpened, events[0].Kind)
	assert.Equal(t, int64(1), events[0].Seq)
	assert.Equal(t, "draft", events[0].ToState)
}

func TestOpen_UnknownDefinition_ReturnsErrDefinitionNotRegistered(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger())

	_, err := e.Open(context.Background(), nil, OpenInput{Definition: "nope", Domain: "d", ExternalID: "1"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDefinitionNotRegistered))
}

func TestMove_HappyPath(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	require.NoError(t, e.RegisterGuard("under-budget", alwaysTrueGuard))

	c := openTestCase(t, e)

	moved, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	assert.Equal(t, "review", moved.State)
	assert.Equal(t, StatusOpen, moved.Status)
	require.NotNil(t, moved.DeadlineAt)
}

func TestMove_UnknownAction_ReturnsErrInvalidTransition(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	c := openTestCase(t, e)

	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "teleport", ActorID: "u1"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidTransition))
}

func TestMove_OnClosedCase_ReturnsErrCaseClosed(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	require.NoError(t, e.RegisterGuard("under-budget", alwaysTrueGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.NoError(t, err)

	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "reject", ActorID: "u2"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCaseClosed))
}

func TestMove_BlockedByGuard_ReturnsErrGuardRejected(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	require.NoError(t, e.RegisterGuard("under-budget", alwaysFalseGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrGuardRejected))
}

func TestMove_UnregisteredGuard_ReturnsErrGuardNotRegistered(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	// deliberately never register "under-budget"

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrGuardNotRegistered))
}

func TestMove_IntoTerminalNode_ClosesCaseAndClearsAssignment(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	_, err = e.Claim(context.Background(), nil, c.ID, "approver-1")
	require.NoError(t, err)

	closed, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "reject", ActorID: "approver-1"})
	require.NoError(t, err)
	assert.Equal(t, StatusClosed, closed.Status)
	assert.Equal(t, "rejected", closed.State)
	assert.Equal(t, "", closed.AssignedTo)
	assert.Nil(t, closed.DeadlineAt)
	require.NotNil(t, closed.ClosedAt)

	events, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)
	last := events[len(events)-1]
	assert.Equal(t, EventClosed, last.Kind)
}

func TestMove_CommentRequired_EmptyComment_ReturnsErrCommentRequired(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(commentTestDefinition()))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "reject", ActorID: "u2"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCommentRequired))

	// No state change: the case must still be resting in "review".
	got, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "review", got.State)
	assert.Equal(t, StatusOpen, got.Status)
}

func TestMove_CommentRequired_WhitespaceOnlyComment_ReturnsErrCommentRequired(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(commentTestDefinition()))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "reject", ActorID: "u2", Comment: "   \t  "})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCommentRequired))

	got, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "review", got.State)
}

func TestMove_CommentNone_WithComment_ReturnsErrCommentNotAllowed(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(commentTestDefinition()))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1", Comment: "not allowed on submit"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCommentNotAllowed))

	// No state change: the case must still be resting at the start node.
	got, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "draft", got.State)

	events, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Len(t, events, 1, "only the opening event: the rejected Move must append nothing")
}

// TestMove_CommentRejected_GuardNeverCalled_CommentRequired proves the
// comment policy is enforced BEFORE the transition's guard runs: a
// CommentRequired transition rejected for a missing comment must never
// invoke its guard, and the case must not move.
func TestMove_CommentRejected_GuardNeverCalled_CommentRequired(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(commentGuardTestDefinition()))
	guard := &callCountingGuard{}
	require.NoError(t, e.RegisterGuard("count-calls", guard.fn))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	assert.Equal(t, 1, guard.count(), "the submit transition's own guard call is expected")

	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "reject", ActorID: "u2"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCommentRequired))
	assert.Equal(t, 1, guard.count(), "the reject transition's guard must not run when the comment check rejects Move")

	got, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "review", got.State, "the case must not have moved")
}

// TestMove_CommentRejected_GuardNeverCalled_CommentNone proves the same
// ordering for a CommentNone transition rejected for carrying a comment it
// disallows.
func TestMove_CommentRejected_GuardNeverCalled_CommentNone(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(commentGuardTestDefinition()))
	guard := &callCountingGuard{}
	require.NoError(t, e.RegisterGuard("count-calls", guard.fn))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1", Comment: "not allowed here"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCommentNotAllowed))
	assert.Equal(t, 0, guard.count(), "the submit transition's guard must not run when the comment check rejects Move")

	got, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "draft", got.State, "the case must not have moved")
}

func TestMove_CommentOptional_AcceptsWithComment(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(commentTestDefinition()))
	require.NoError(t, e.RegisterGuard("under-budget", alwaysTrueGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	moved, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2", Comment: "looks fine"})
	require.NoError(t, err)
	assert.Equal(t, StatusClosed, moved.Status)
}

func TestMove_CommentOptional_AcceptsWithoutComment(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(commentTestDefinition()))
	require.NoError(t, e.RegisterGuard("under-budget", alwaysTrueGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	moved, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.NoError(t, err)
	assert.Equal(t, StatusClosed, moved.Status)
}

func TestMove_CommentStored_VisibleThroughHistory(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(commentTestDefinition()))
	require.NoError(t, e.RegisterGuard("under-budget", alwaysTrueGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2", Comment: "  budget looks fine  "})
	require.NoError(t, err)

	events, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)

	var moved *Event
	for i := range events {
		if events[i].Kind == EventMoved && events[i].Action == "approve" {
			moved = &events[i]
		}
	}
	require.NotNil(t, moved, "expected a moved(approve) event in history")
	assert.Equal(t, "budget looks fine", moved.Comment, "the comment must be trimmed before storage")
}

func TestMove_TerminalClose_EventCarriesNoComment(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(commentTestDefinition()))
	require.NoError(t, e.RegisterGuard("under-budget", alwaysTrueGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2", Comment: "approved!"})
	require.NoError(t, err)

	events, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)
	last := events[len(events)-1]
	require.Equal(t, EventClosed, last.Kind)
	assert.Empty(t, last.Comment, "the automatic close event must never duplicate the moved event's comment")
}

func TestClaim_ThenDoubleClaim_ThenRelease(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	c := openTestCase(t, e)

	claimed, err := e.Claim(context.Background(), nil, c.ID, "u1")
	require.NoError(t, err)
	assert.Equal(t, "u1", claimed.AssignedTo)

	eventsAfterFirstClaim, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)

	// Re-claiming by the same actor is a no-op success: no new event appended.
	sameClaim, err := e.Claim(context.Background(), nil, c.ID, "u1")
	require.NoError(t, err)
	assert.Equal(t, "u1", sameClaim.AssignedTo)

	eventsAfterSecondClaim, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Len(t, eventsAfterSecondClaim, len(eventsAfterFirstClaim))

	// Claiming by a different actor while already assigned fails.
	_, err = e.Claim(context.Background(), nil, c.ID, "u2")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrAlreadyAssigned))

	released, err := e.Release(context.Background(), nil, c.ID, "u1")
	require.NoError(t, err)
	assert.Equal(t, "", released.AssignedTo)
}

func TestRelease_WhenUnassigned_ReturnsErrNotAssigned(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	c := openTestCase(t, e)

	_, err := e.Release(context.Background(), nil, c.ID, "u1")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotAssigned))
}

func TestAvailable_FiltersByGuard(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	require.NoError(t, e.RegisterGuard("under-budget", alwaysFalseGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	available, err := e.Available(context.Background(), nil, c.ID)
	require.NoError(t, err)
	require.Len(t, available, 1)
	assert.Equal(t, "reject", available[0].Action)
}

func TestAvailable_GuardError_Propagates(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	wantErr := errors.New("budget service unavailable")
	require.NoError(t, e.RegisterGuard("under-budget", func(context.Context, pgxtx.DBTX, Case) (bool, error) {
		return false, wantErr
	}))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	_, err = e.Available(context.Background(), nil, c.ID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, wantErr))
}

func TestMove_GuardReceivesCallersDB(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	var gotDB pgxtx.DBTX
	require.NoError(t, e.RegisterGuard("under-budget", func(_ context.Context, db pgxtx.DBTX, _ Case) (bool, error) {
		gotDB = db
		return true, nil
	}))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	// A guard reading domain data must see writes made earlier in the
	// caller's own transaction, so Move must pass through the exact db it
	// received rather than substituting a different one (or nil).
	wantDB := &sentinelDBTX{}
	_, err = e.Move(context.Background(), wantDB, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.NoError(t, err)

	assert.Same(t, wantDB, gotDB, "Move must pass its own db argument through to the guard")
}

func TestAvailable_GuardReceivesCallersDB(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	var gotDB pgxtx.DBTX
	require.NoError(t, e.RegisterGuard("under-budget", func(_ context.Context, db pgxtx.DBTX, _ Case) (bool, error) {
		gotDB = db
		return true, nil
	}))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	wantDB := &sentinelDBTX{}
	_, err = e.Available(context.Background(), wantDB, c.ID)
	require.NoError(t, err)

	assert.Same(t, wantDB, gotDB, "Available must pass its own db argument through to the guard")
}

func TestHistory_OrdersBySeqAscending(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger())

	caseID := uuid.New()
	require.NoError(t, repo.Create(context.Background(), nil, &Case{ID: caseID, Domain: "d", ExternalID: "1"}))

	// Append events out of seq order directly on the repository to prove
	// History sorts rather than relying on insertion order.
	require.NoError(t, repo.AppendEvent(context.Background(), nil, &Event{ID: uuid.New(), CaseID: caseID, Seq: 3, Kind: EventMoved}))
	require.NoError(t, repo.AppendEvent(context.Background(), nil, &Event{ID: uuid.New(), CaseID: caseID, Seq: 1, Kind: EventOpened}))
	require.NoError(t, repo.AppendEvent(context.Background(), nil, &Event{ID: uuid.New(), CaseID: caseID, Seq: 2, Kind: EventAssigned}))

	events, err := e.History(context.Background(), nil, caseID)
	require.NoError(t, err)
	require.Len(t, events, 3)
	assert.Equal(t, int64(1), events[0].Seq)
	assert.Equal(t, int64(2), events[1].Seq)
	assert.Equal(t, int64(3), events[2].Seq)
}

func TestEligibilityFor_ResolvesUnit(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	// draft's eligibility has no explicit unit: falls back to the case's own unit.
	c := openTestCase(t, e)
	elig, err := e.EligibilityFor(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "buyer", elig.Position)
	assert.Equal(t, "finance", elig.Unit) // case.Unit == "finance"

	// review's eligibility pins an explicit unit, overriding the case's unit.
	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	elig, err = e.EligibilityFor(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "approver", elig.Position)
	assert.Equal(t, "finance", elig.Unit)
}

func TestSeqMonotonicity_AcrossMultiStepRun(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	require.NoError(t, e.RegisterGuard("under-budget", alwaysTrueGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	_, err = e.Claim(context.Background(), nil, c.ID, "approver-1")
	require.NoError(t, err)
	_, err = e.Release(context.Background(), nil, c.ID, "approver-1")
	require.NoError(t, err)
	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "approver-1"})
	require.NoError(t, err)

	events, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)
	// opened, moved(submit), assigned, unassigned, moved(approve), closed
	require.Len(t, events, 6)
	for i, ev := range events {
		assert.Equal(t, int64(i+1), ev.Seq, "event %d kind=%s", i, ev.Kind)
	}
}

func TestNextSeqFromEvents(t *testing.T) {
	assert.Equal(t, int64(1), nextSeqFromEvents(nil))
	assert.Equal(t, int64(1), nextSeqFromEvents([]Event{}))
	assert.Equal(t, int64(4), nextSeqFromEvents([]Event{{Seq: 1}, {Seq: 3}, {Seq: 2}}))
}

func TestRegister_InvalidDefinition_ReturnsErrInvalidDefinition(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger())

	err := e.Register(Definition{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidDefinition))
}

func TestRegisterGuard_EmptyNameOrNilFunc_ReturnsError(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger())

	assert.Error(t, e.RegisterGuard("", alwaysTrueGuard))
	assert.Error(t, e.RegisterGuard("x", nil))
}

func TestDefinitions_SortedByNameThenVersion(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger())

	d1 := testDefinition()
	d2 := testDefinition()
	d2.Version = 2
	other := testDefinition()
	other.Name = "onboarding"

	require.NoError(t, e.Register(d2))
	require.NoError(t, e.Register(d1))
	require.NoError(t, e.Register(other))

	defs := e.Definitions()
	require.Len(t, defs, 3)
	assert.Equal(t, "onboarding", defs[0].Name)
	assert.Equal(t, "purchase", defs[1].Name)
	assert.Equal(t, 1, defs[1].Version)
	assert.Equal(t, "purchase", defs[2].Name)
	assert.Equal(t, 2, defs[2].Version)
}

// decisionEngineDefinition mirrors workflow_test.go's decisionDefinition:
// after "review", the decision node "route" sends the case to "sep" when
// the "has-subsidy-sep" guard matches, otherwise falls through to the
// unguarded default "budget" — both of which lead to the terminal
// "approved".
func decisionEngineDefinition() Definition {
	return Definition{
		Name:    "purchase",
		Version: 1,
		Nodes: []Node{
			{ID: "draft", Start: true, Eligible: ByPosition("buyer")},
			{ID: "review", Eligible: ByPositionInUnit("approver", "finance")},
			{ID: "route", Kind: NodeDecision},
			{ID: "sep", Eligible: ByPosition("sep-coordinator")},
			{ID: "budget", Eligible: ByPosition("budget-analyst")},
			{ID: "approved", Terminal: true},
		},
		Transitions: []Transition{
			{From: "draft", To: "review", Action: "submit"},
			{From: "review", To: "route", Action: "approve", Comment: CommentOptional},
			{From: "route", To: "sep", Action: "to-sep", Guard: "has-subsidy-sep"},
			{From: "route", To: "budget", Action: "to-budget"},
			{From: "sep", To: "approved", Action: "clear"},
			{From: "budget", To: "approved", Action: "clear"},
		},
	}
}

// decisionToTerminalDefinition sends a decision node's routes straight to
// terminal nodes, so Move must close the case immediately after the
// automatic hop — proving a decision hop can lead directly to StatusClosed
// without resting anywhere in between.
func decisionToTerminalDefinition() Definition {
	return Definition{
		Name:    "purchase",
		Version: 1,
		Nodes: []Node{
			{ID: "draft", Start: true, Eligible: ByPosition("buyer")},
			{ID: "review", Eligible: ByPositionInUnit("approver", "finance")},
			{ID: "route", Kind: NodeDecision},
			{ID: "approved", Terminal: true},
			{ID: "rejected", Terminal: true},
		},
		Transitions: []Transition{
			{From: "draft", To: "review", Action: "submit"},
			{From: "review", To: "route", Action: "approve"},
			{From: "route", To: "rejected", Action: "to-rejected", Guard: "has-subsidy-sep"},
			{From: "route", To: "approved", Action: "to-approved"},
		},
	}
}

// chainedDecisionDefinition chains two decision nodes: "route" (once its own
// guarded escape hatch fails) unconditionally forwards to "route2", which
// then picks between "sep" and "budget". It proves Move keeps routing across
// multiple decision hops within one call until it rests on a task node.
func chainedDecisionDefinition() Definition {
	return Definition{
		Name:    "purchase",
		Version: 1,
		Nodes: []Node{
			{ID: "draft", Start: true, Eligible: ByPosition("buyer")},
			{ID: "review", Eligible: ByPositionInUnit("approver", "finance")},
			{ID: "route", Kind: NodeDecision},
			{ID: "route2", Kind: NodeDecision},
			{ID: "sep", Eligible: ByPosition("sep-coordinator")},
			{ID: "budget", Eligible: ByPosition("budget-analyst")},
			{ID: "approved", Terminal: true},
		},
		Transitions: []Transition{
			{From: "draft", To: "review", Action: "submit"},
			{From: "review", To: "route", Action: "approve"},
			{From: "route", To: "budget", Action: "skip-to-budget", Guard: "always-false"},
			{From: "route", To: "route2", Action: "to-route2"},
			{From: "route2", To: "sep", Action: "to-sep", Guard: "has-subsidy-sep"},
			{From: "route2", To: "budget", Action: "to-budget"},
			{From: "sep", To: "approved", Action: "clear"},
			{From: "budget", To: "approved", Action: "clear"},
		},
	}
}

// noDefaultRouteDefinition intentionally violates Definition.Validate's
// "exactly one unguarded default route" rule: every route out of "route" is
// guarded. Register (and therefore WithDefinitions) always calls Validate
// and would reject it, so tests using it inject it directly into the
// Engine's internal registry — exercising Engine.Move's ErrNoRoute path
// defensively, against a Definition that somehow reached the engine without
// going through Validate.
func noDefaultRouteDefinition() Definition {
	d := decisionEngineDefinition()
	for i := range d.Transitions {
		if d.Transitions[i].From == "route" && d.Transitions[i].Action == "to-budget" {
			d.Transitions[i].Guard = "always-false"
		}
	}
	return d
}

// cyclicDecisionDefinition intentionally violates Definition.Validate's "no
// cycle made only of decision nodes" rule: "route" and "route2" route back
// and forth with no way out once their (never-taken, in these tests) escape
// guards fail. Injected directly into the Engine's internal registry like
// noDefaultRouteDefinition, it exercises Engine.Move's hop cap
// (maxDecisionHops) defensively.
func cyclicDecisionDefinition() Definition {
	return Definition{
		Name:    "purchase",
		Version: 1,
		Nodes: []Node{
			{ID: "draft", Start: true, Eligible: ByPosition("buyer")},
			{ID: "review", Eligible: ByPositionInUnit("approver", "finance")},
			{ID: "route", Kind: NodeDecision},
			{ID: "route2", Kind: NodeDecision},
			{ID: "approved", Terminal: true},
		},
		Transitions: []Transition{
			{From: "draft", To: "review", Action: "submit"},
			{From: "review", To: "route", Action: "approve"},
			{From: "route", To: "approved", Action: "escape", Guard: "always-false"},
			{From: "route", To: "route2", Action: "to-route2"},
			{From: "route2", To: "approved", Action: "escape2", Guard: "always-false"},
			{From: "route2", To: "route", Action: "to-route"},
		},
	}
}

// stateRecordingRepository wraps fakeRepository to record every State value
// passed to Update, so a test can prove Move writes the case exactly once
// per call — however many decision hops it took internally — and that the
// one persisted State is never a decision node.
type stateRecordingRepository struct {
	*fakeRepository
	updatedStates []string
}

func (r *stateRecordingRepository) Update(ctx context.Context, db pgxtx.DBTX, c *Case) error {
	r.updatedStates = append(r.updatedStates, c.State)
	return r.fakeRepository.Update(ctx, db, c)
}

func TestMove_DecisionNode_RoutesOnFirstTrueGuard(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(decisionEngineDefinition()))
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysTrueGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	moved, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.NoError(t, err)
	assert.Equal(t, "sep", moved.State, "the guarded route must win when its guard matches")
	assert.Equal(t, StatusOpen, moved.Status)
}

func TestMove_DecisionNode_FallsThroughToDefault(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(decisionEngineDefinition()))
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysFalseGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	moved, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.NoError(t, err)
	assert.Equal(t, "budget", moved.State, "the unguarded default route must win when no guarded route matches")
}

func TestMove_ChainedDecisionNodes_RestsOnTaskNode(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(chainedDecisionDefinition()))
	require.NoError(t, e.RegisterGuard("always-false", alwaysFalseGuard))
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysTrueGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	moved, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.NoError(t, err)
	assert.Equal(t, "sep", moved.State, "must chain through both decision nodes before resting on a task node")
	assert.Equal(t, StatusOpen, moved.Status)
}

func TestMove_DecisionNode_LandsOnTerminal_ClosesCaseOnce(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(decisionToTerminalDefinition()))
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysFalseGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	closed, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.NoError(t, err)
	assert.Equal(t, StatusClosed, closed.Status)
	assert.Equal(t, "approved", closed.State)
	require.NotNil(t, closed.ClosedAt)

	events, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)
	closeCount := 0
	for _, ev := range events {
		if ev.Kind == EventClosed {
			closeCount++
		}
	}
	assert.Equal(t, 1, closeCount, "exactly one EventClosed even though the case reached the terminal node via an automatic decision hop")
}

func TestMove_DecisionNode_OneEventMovedPerHop_CommentOnlyOnFirst(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(decisionEngineDefinition()))
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysTrueGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2", Comment: "delegating to SEP"})
	require.NoError(t, err)

	events, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)

	var moved []Event
	for _, ev := range events {
		if ev.Kind == EventMoved {
			moved = append(moved, ev)
		}
	}
	require.Len(t, moved, 3, "moved(submit), plus one human hop and one automatic hop for the approve Move")

	human := moved[len(moved)-2]
	assert.Equal(t, "approve", human.Action)
	assert.Equal(t, "u2", human.ActorID)
	assert.Equal(t, "review", human.FromState)
	assert.Equal(t, "route", human.ToState)
	assert.Equal(t, "delegating to SEP", human.Comment)

	automatic := moved[len(moved)-1]
	assert.Equal(t, "to-sep", automatic.Action, "the automatic hop's Action is the winning route's label")
	assert.Equal(t, "u2", automatic.ActorID, "the automatic hop keeps the original actor")
	assert.Equal(t, "route", automatic.FromState)
	assert.Equal(t, "sep", automatic.ToState)
	assert.Empty(t, automatic.Comment, "the comment belongs only to the human-driven hop")
}

func TestMove_DecisionNode_NoMatchingRoute_ReturnsErrNoRoute_LeavesCaseUntouched(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger())
	d := noDefaultRouteDefinition()
	e.definitions[d.Key()] = d
	e.latest[d.Name] = d
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysFalseGuard))
	require.NoError(t, e.RegisterGuard("always-false", alwaysFalseGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	before, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	eventsBefore, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)

	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoRoute))

	after, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, *before, *after, "a failed route resolution must leave the case completely untouched")

	eventsAfter, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, eventsBefore, eventsAfter, "a failed route resolution must append no events")
}

func TestMove_DecisionNode_RouteGuardError_Propagates(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(decisionEngineDefinition()))
	wantErr := errors.New("subsidy service unavailable")
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", func(context.Context, pgxtx.DBTX, Case) (bool, error) {
		return false, wantErr
	}))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, wantErr), "a route's own guard error must propagate, not collapse into ErrNoRoute")

	after, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "review", after.State, "a route guard error must leave the case untouched")
}

func TestMove_DecisionNode_HopLimitExceeded_LeavesCaseUntouched(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger())
	d := cyclicDecisionDefinition()
	e.definitions[d.Key()] = d
	e.latest[d.Name] = d
	require.NoError(t, e.RegisterGuard("always-false", alwaysFalseGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTooManyDecisionHops))

	after, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "review", after.State, "a hop-limit failure must leave the case resting where it was")
}

func TestMove_DecisionNode_NeverPersistsDecisionState(t *testing.T) {
	repo := &stateRecordingRepository{fakeRepository: newFakeRepository()}
	e := New(repo, Config{}, testLogger(), WithDefinitions(chainedDecisionDefinition()))
	require.NoError(t, e.RegisterGuard("always-false", alwaysFalseGuard))
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysTrueGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	repo.updatedStates = nil // discard the submit move's own Update call

	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.NoError(t, err)

	require.Len(t, repo.updatedStates, 1, "Move must write the case exactly once, however many decision hops it took")
	assert.Equal(t, "sep", repo.updatedStates[0], "the only persisted state must be the final resting task node")

	for _, n := range chainedDecisionDefinition().Nodes {
		if n.Kind == NodeDecision {
			assert.NotEqual(t, n.ID, repo.updatedStates[0], "a case must never be persisted resting on a decision node")
		}
	}
}

func TestAvailable_AfterDecisionRouting_OnlyOffersTaskNodeActions(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(decisionEngineDefinition()))
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysTrueGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.NoError(t, err)

	available, err := e.Available(context.Background(), nil, c.ID)
	require.NoError(t, err)
	require.Len(t, available, 1)
	assert.Equal(t, "clear", available[0].Action, "Available must see the resting task node's own actions, never a decision node's routes")
}

func TestRegister_DecisionStartNode_ReturnsErrInvalidDefinition(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger())

	d := decisionEngineDefinition()
	for i := range d.Nodes {
		if d.Nodes[i].ID == "draft" {
			d.Nodes[i].Kind = NodeDecision
		}
	}

	err := e.Register(d)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidDefinition))

	_, err = e.Open(context.Background(), nil, OpenInput{Definition: "purchase", Domain: "d", ExternalID: "1"})
	assert.True(t, errors.Is(err, ErrDefinitionNotRegistered), "a decision-start Definition must never register, so Open can never resolve it")
}

// returnEngineDefinition mirrors workflow_test.go's returnDefinition, adding
// Deadlines on "review", "sep", and "budget" so a test can prove a return
// recomputes the destination node's deadline exactly like any other move,
// and a "count-calls" guard on return-to-budget so a test can prove the
// check ordering documented on Engine.Move: CommentPolicy, then whether the
// case occupied the return's target, then Guard.
func returnEngineDefinition() Definition {
	return Definition{
		Name:    "purchase",
		Version: 1,
		Nodes: []Node{
			{ID: "draft", Start: true, Eligible: ByPosition("buyer")},
			{ID: "review", Eligible: ByPositionInUnit("approver", "finance"), Deadline: 48 * time.Hour},
			{ID: "route", Kind: NodeDecision},
			{ID: "sep", Eligible: ByPosition("sep-coordinator"), Deadline: 24 * time.Hour},
			{ID: "budget", Eligible: ByPosition("budget-analyst"), Deadline: 12 * time.Hour},
			{ID: "signoff", Eligible: ByPosition("director")},
			{ID: "approved", Terminal: true},
		},
		Transitions: []Transition{
			{From: "draft", To: "review", Action: "submit"},
			{From: "review", To: "route", Action: "approve"},
			{From: "route", To: "sep", Action: "to-sep", Guard: "has-subsidy-sep"},
			{From: "route", To: "budget", Action: "to-budget"},
			{From: "sep", To: "signoff", Action: "clear"},
			{From: "budget", To: "signoff", Action: "clear"},
			{From: "signoff", To: "approved", Action: "approve-final"},
			{From: "signoff", To: "review", Action: "return-to-review", Return: true, Comment: CommentRequired},
			{From: "signoff", To: "sep", Action: "return-to-sep", Return: true, Comment: CommentRequired},
			{From: "signoff", To: "budget", Action: "return-to-budget", Return: true, Comment: CommentRequired, Guard: "count-calls"},
		},
	}
}

// moveToSignoffViaBudget drives a fresh case through draft -> review ->
// route -> budget -> signoff, never visiting "sep" — has-subsidy-sep must be
// registered as alwaysFalseGuard for this path to be taken.
func moveToSignoffViaBudget(t *testing.T, e *Engine, c *Case) {
	t.Helper()
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.NoError(t, err)
	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "clear", ActorID: "u3"})
	require.NoError(t, err)
}

func TestMove_Return_ToVisitedNode_Succeeds(t *testing.T) {
	repo := newFakeRepository()
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	e := New(repo, Config{Now: func() time.Time { return now }}, testLogger(), WithDefinitions(returnEngineDefinition()))
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysFalseGuard))
	guard := &callCountingGuard{}
	require.NoError(t, e.RegisterGuard("count-calls", guard.fn))

	c := openTestCase(t, e)
	moveToSignoffViaBudget(t, e, c)
	_, err := e.Claim(context.Background(), nil, c.ID, "director-1")
	require.NoError(t, err)

	moved, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "return-to-budget", ActorID: "director-1", Comment: "wrong amount"})
	require.NoError(t, err)
	assert.Equal(t, "budget", moved.State)
	assert.Equal(t, "", moved.AssignedTo, "a return clears assignment exactly like any other move")
	require.NotNil(t, moved.DeadlineAt, "a return recomputes the destination node's deadline exactly like any other move")
	assert.Equal(t, now.Add(12*time.Hour), *moved.DeadlineAt)
	assert.Equal(t, 1, guard.count(), "the return's own guard must run once both the comment and visited checks passed")

	events, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)
	last := events[len(events)-1]
	assert.Equal(t, EventMoved, last.Kind)
	assert.Equal(t, "return-to-budget", last.Action)
	assert.Equal(t, "signoff", last.FromState)
	assert.Equal(t, "budget", last.ToState)
	assert.Equal(t, "wrong amount", last.Comment)
}

func TestMove_Return_ToUnvisitedNode_ReturnsErrReturnNotVisited(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(returnEngineDefinition()))
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysFalseGuard)) // case never visits "sep"

	c := openTestCase(t, e)
	moveToSignoffViaBudget(t, e, c)

	before, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	eventsBefore, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)

	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "return-to-sep", ActorID: "director-1", Comment: "send to SEP after all"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrReturnNotVisited))

	after, err := repo.GetByID(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, *before, *after, "a rejected return must leave the case completely untouched")

	eventsAfter, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, eventsBefore, eventsAfter, "a rejected return must append no events")
}

// TestMove_Return_CheckOrdering pins the exact order documented on
// Engine.Move for a return transition: CommentPolicy, then whether the case
// occupied the return's target, then Guard. return-to-budget carries
// CommentRequired and a "count-calls" guard, so each sub-test isolates one
// step of the ordering by construction.
func TestMove_Return_CheckOrdering(t *testing.T) {
	newEngineAtSignoffViaSep := func(t *testing.T) (*Engine, *Case, *callCountingGuard) {
		t.Helper()
		repo := newFakeRepository()
		e := New(repo, Config{}, testLogger(), WithDefinitions(returnEngineDefinition()))
		require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysTrueGuard)) // case visits "sep", never "budget"
		guard := &callCountingGuard{}
		require.NoError(t, e.RegisterGuard("count-calls", guard.fn))

		c := openTestCase(t, e)
		_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
		require.NoError(t, err)
		_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
		require.NoError(t, err)
		_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "clear", ActorID: "sep-coordinator-1"})
		require.NoError(t, err)
		return e, c, guard
	}

	t.Run("missing comment fails before the visited check and the guard", func(t *testing.T) {
		e, c, guard := newEngineAtSignoffViaSep(t)

		// "budget" was never visited on this path, so if the visited check
		// ran first this would fail with ErrReturnNotVisited instead.
		_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "return-to-budget", ActorID: "director-1"})
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrCommentRequired))
		assert.Equal(t, 0, guard.count(), "the guard must not run when the comment check rejects Move first")
	})

	t.Run("unvisited target fails before the guard, even with a valid comment", func(t *testing.T) {
		e, c, guard := newEngineAtSignoffViaSep(t)

		_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "return-to-budget", ActorID: "director-1", Comment: "wrong amount"})
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrReturnNotVisited))
		assert.Equal(t, 0, guard.count(), "the guard must not run when the visited check rejects Move first")
	})

	t.Run("visited target with a valid comment reaches the guard", func(t *testing.T) {
		repo := newFakeRepository()
		e := New(repo, Config{}, testLogger(), WithDefinitions(returnEngineDefinition()))
		require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysFalseGuard)) // case visits "budget", never "sep"
		guard := &callCountingGuard{}
		require.NoError(t, e.RegisterGuard("count-calls", guard.fn))

		c := openTestCase(t, e)
		moveToSignoffViaBudget(t, e, c)

		_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "return-to-budget", ActorID: "director-1", Comment: "wrong amount"})
		require.NoError(t, err)
		assert.Equal(t, 1, guard.count(), "once the comment and visited checks both pass, the guard must run")
	})
}

func TestAvailable_Return_HidesUnvisitedShowsVisited(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(returnEngineDefinition()))
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysFalseGuard)) // case never visits "sep"
	require.NoError(t, e.RegisterGuard("count-calls", alwaysTrueGuard))

	c := openTestCase(t, e)
	moveToSignoffViaBudget(t, e, c)

	available, err := e.Available(context.Background(), nil, c.ID)
	require.NoError(t, err)

	actions := make([]string, len(available))
	for i, tr := range available {
		actions[i] = tr.Action
	}
	assert.ElementsMatch(t, []string{"approve-final", "return-to-review", "return-to-budget"}, actions,
		"return-to-sep must be hidden: the case never occupied \"sep\"")
}

func TestAvailable_Return_StillFiltersByGuardWhenVisited(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(returnEngineDefinition()))
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysFalseGuard))
	require.NoError(t, e.RegisterGuard("count-calls", alwaysFalseGuard)) // return-to-budget's guard rejects

	c := openTestCase(t, e)
	moveToSignoffViaBudget(t, e, c)

	available, err := e.Available(context.Background(), nil, c.ID)
	require.NoError(t, err)

	actions := make([]string, len(available))
	for i, tr := range available {
		actions[i] = tr.Action
	}
	assert.NotContains(t, actions, "return-to-budget", "a return combines with Guard: visited but guard-rejected must still be filtered")
	assert.Contains(t, actions, "return-to-review", "return-to-review has no guard and must remain available")
}

func TestMove_Return_GuardRejected_ReturnsErrGuardRejected_EvenWhenVisited(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(returnEngineDefinition()))
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysFalseGuard))
	require.NoError(t, e.RegisterGuard("count-calls", alwaysFalseGuard))

	c := openTestCase(t, e)
	moveToSignoffViaBudget(t, e, c)

	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "return-to-budget", ActorID: "director-1", Comment: "wrong amount"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrGuardRejected))
}

// listEventsCountingRepository wraps fakeRepository to count ListEvents
// calls, so a test can prove Available and Move read the case's history at
// most once per call for the return-visited check, and only when the
// candidate transitions actually include a return.
type listEventsCountingRepository struct {
	*fakeRepository
	listEventsCalls int
}

func (r *listEventsCountingRepository) ListEvents(ctx context.Context, db pgxtx.DBTX, caseID uuid.UUID) ([]Event, error) {
	r.listEventsCalls++
	return r.fakeRepository.ListEvents(ctx, db, caseID)
}

func TestAvailable_NoReturnCandidates_NeverReadsHistory(t *testing.T) {
	repo := &listEventsCountingRepository{fakeRepository: newFakeRepository()}
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	require.NoError(t, e.RegisterGuard("under-budget", alwaysTrueGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	repo.listEventsCalls = 0
	_, err = e.Available(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, repo.listEventsCalls, "Available must never read history when no candidate transition is a return")
}

func TestAvailable_WithReturnCandidate_ReadsHistoryExactlyOnce(t *testing.T) {
	repo := &listEventsCountingRepository{fakeRepository: newFakeRepository()}
	e := New(repo, Config{}, testLogger(), WithDefinitions(returnEngineDefinition()))
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysFalseGuard))
	require.NoError(t, e.RegisterGuard("count-calls", alwaysTrueGuard))

	c := openTestCase(t, e)
	moveToSignoffViaBudget(t, e, c)

	repo.listEventsCalls = 0
	_, err := e.Available(context.Background(), nil, c.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, repo.listEventsCalls, "Available must read history exactly once, however many return candidates there are")
}

func TestMove_NonReturnTransition_HistoryReadCount(t *testing.T) {
	repo := &listEventsCountingRepository{fakeRepository: newFakeRepository()}
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))

	c := openTestCase(t, e)
	repo.listEventsCalls = 0
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	assert.Equal(t, 1, repo.listEventsCalls, "a non-return move only reads history once, for appendEvent's own nextSeq computation")
}

func TestMove_ReturnTransition_HistoryReadCount(t *testing.T) {
	repo := &listEventsCountingRepository{fakeRepository: newFakeRepository()}
	e := New(repo, Config{}, testLogger(), WithDefinitions(returnEngineDefinition()))
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysFalseGuard))
	require.NoError(t, e.RegisterGuard("count-calls", alwaysTrueGuard))

	c := openTestCase(t, e)
	moveToSignoffViaBudget(t, e, c)

	repo.listEventsCalls = 0
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "return-to-budget", ActorID: "director-1", Comment: "wrong amount"})
	require.NoError(t, err)
	assert.Equal(t, 2, repo.listEventsCalls, "a return move reads history exactly one extra time (the return-visited check) on top of appendEvent's own nextSeq read")
}

// TestMove_DecisionHopIntoTerminal_EventClosedActionIsLastRouteHop pins the
// R2 advisory from T3's review: when a decision hop lands on a terminal
// node, the automatic EventClosed takes its Action from the LAST hop in the
// chain (the winning route's own label), never from the human
// MoveInput.Action that started the move.
func TestMove_DecisionHopIntoTerminal_EventClosedActionIsLastRouteHop(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(decisionToTerminalDefinition()))
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysFalseGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.NoError(t, err)

	events, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)
	last := events[len(events)-1]
	require.Equal(t, EventClosed, last.Kind)
	assert.Equal(t, "to-approved", last.Action, "EventClosed.Action must be the last route hop's label")
	assert.NotEqual(t, "approve", last.Action, "EventClosed.Action must never be the human MoveInput.Action when a route hop followed it")
}

// TestMove_MultiHopIntoTerminal_SeqStrictlyIncreasing pins the R3 advisory
// from T3's review: every event one Move call appends shares one OccurredAt
// timestamp, but Seq still orders them strictly — the human hop, then the
// automatic route hop, then the automatic close.
func TestMove_MultiHopIntoTerminal_SeqStrictlyIncreasing(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(decisionToTerminalDefinition()))
	require.NoError(t, e.RegisterGuard("has-subsidy-sep", alwaysFalseGuard))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)
	_, err = e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "approve", ActorID: "u2"})
	require.NoError(t, err)

	events, err := e.History(context.Background(), nil, c.ID)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(events), 3)
	for i := 1; i < len(events); i++ {
		assert.Equal(t, events[i-1].Seq+1, events[i].Seq, "Seq must increase by exactly one per event, in append order (event %d)", i)
	}

	last3 := events[len(events)-3:]
	assert.Equal(t, EventMoved, last3[0].Kind)
	assert.Equal(t, "approve", last3[0].Action)
	assert.Equal(t, EventMoved, last3[1].Kind)
	assert.Equal(t, "to-approved", last3[1].Action)
	assert.Equal(t, EventClosed, last3[2].Kind)
	assert.Equal(t, last3[0].OccurredAt, last3[1].OccurredAt, "all hops of one Move share one timestamp")
	assert.Equal(t, last3[1].OccurredAt, last3[2].OccurredAt, "the automatic close shares that same timestamp")
	assert.Less(t, last3[0].Seq, last3[1].Seq, "Seq, not OccurredAt, orders same-timestamp events")
	assert.Less(t, last3[1].Seq, last3[2].Seq)
}

func TestOpen_UsesLatestVersionWhenUnpinned(t *testing.T) {
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger())

	d1 := testDefinition()
	d2 := testDefinition()
	d2.Version = 2
	require.NoError(t, e.Register(d1))
	require.NoError(t, e.Register(d2))

	c, err := e.Open(context.Background(), nil, OpenInput{Definition: "purchase", Domain: "d", ExternalID: "1"})
	require.NoError(t, err)
	assert.Equal(t, 2, c.Version)
}
