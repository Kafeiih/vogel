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

func alwaysTrueGuard(context.Context, Case) (bool, error)  { return true, nil }
func alwaysFalseGuard(context.Context, Case) (bool, error) { return false, nil }

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
	require.NoError(t, e.RegisterGuard("under-budget", func(context.Context, Case) (bool, error) {
		return false, wantErr
	}))

	c := openTestCase(t, e)
	_, err := e.Move(context.Background(), nil, MoveInput{CaseID: c.ID, Action: "submit", ActorID: "u1"})
	require.NoError(t, err)

	_, err = e.Available(context.Background(), nil, c.ID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, wantErr))
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
