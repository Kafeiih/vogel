package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chainedTestCase opens a case and records two failures on it, so it holds
// three chained events (opened, no_route, failed) in the fake repository.
func chainedTestCase(t *testing.T) (*Engine, *fakeRepository, *Case) {
	t.Helper()
	repo := newFakeRepository()
	e := New(repo, Config{}, testLogger(), WithDefinitions(testDefinition()))
	c := openTestCase(t, e)
	actor := Actor{Kind: ActorHuman, ID: "u1"}
	for _, kind := range []FailureKind{FailureNoRoute, FailureGuardRejected} {
		_, err := e.RecordFailure(context.Background(), nil, FailureInput{CaseID: c.ID, Actor: actor, Action: "submit", Kind: kind})
		require.NoError(t, err)
	}
	return e, repo, c
}

// requireChainBroken asserts err is a *ChainBrokenError for caseID at seq
// that also matches ErrChainBroken.
func requireChainBroken(t *testing.T, err error, caseID uuid.UUID, seq int64) {
	t.Helper()
	require.ErrorIs(t, err, ErrChainBroken)
	var broken *ChainBrokenError
	require.True(t, errors.As(err, &broken), "expected a *ChainBrokenError, got %T: %v", err, err)
	assert.Equal(t, caseID, broken.CaseID)
	assert.Equal(t, seq, broken.Seq)
}

func TestAppendEvent_ReturnsChainedDigests(t *testing.T) {
	_, repo, c := chainedTestCase(t)

	events := storedEvents(t, repo, c.ID)
	require.Len(t, events, 3)
	assert.Nil(t, events[0].PrevDigest, "the first event starts the chain")
	for i, ev := range events {
		assert.Len(t, ev.Digest, 32, "seq %d", ev.Seq)
		if i > 0 {
			assert.Equal(t, events[i-1].Digest, ev.PrevDigest, "seq %d links to seq %d", ev.Seq, events[i-1].Seq)
		}
	}
}

func TestVerifyChain_IntactChainPasses(t *testing.T) {
	e, _, c := chainedTestCase(t)

	require.NoError(t, e.VerifyChain(context.Background(), nil, c.ID))
}

func TestVerifyChain_DetectsAlteredEvent(t *testing.T) {
	e, repo, c := chainedTestCase(t)

	repo.mutateEvent(c.ID, 2, func(ev *Event) { ev.Actor.ID = "mallory" })

	requireChainBroken(t, e.VerifyChain(context.Background(), nil, c.ID), c.ID, 2)
}

func TestVerifyChain_DetectsBrokenLink(t *testing.T) {
	e, repo, c := chainedTestCase(t)

	// A removed event leaves seq 3 pointing at a digest that is no longer
	// its predecessor's.
	repo.mu.Lock()
	repo.events[c.ID] = append(repo.events[c.ID][:1:1], repo.events[c.ID][2])
	repo.mu.Unlock()

	requireChainBroken(t, e.VerifyChain(context.Background(), nil, c.ID), c.ID, 3)
}

func TestVerifyChain_DetectsDigestRemovedAfterChainStart(t *testing.T) {
	e, repo, c := chainedTestCase(t)

	repo.mutateEvent(c.ID, 2, func(ev *Event) { ev.PrevDigest, ev.Digest = nil, nil })

	requireChainBroken(t, e.VerifyChain(context.Background(), nil, c.ID), c.ID, 2)
}

func TestVerifyChain_DetectsPrevDigestWithoutDigest(t *testing.T) {
	e, repo, c := chainedTestCase(t)

	// The chain never stores a link without its digest; clearing only the
	// digest of the first event must not make it pass as unchained.
	repo.mutateEvent(c.ID, 1, func(ev *Event) { ev.PrevDigest, ev.Digest = []byte("x"), nil })

	requireChainBroken(t, e.VerifyChain(context.Background(), nil, c.ID), c.ID, 1)
}

func TestVerifyChain_LegacyPrefixIsUnchained(t *testing.T) {
	e, repo, c := chainedTestCase(t)

	// Events stored before the chain existed carry no digest; the first
	// chained event after them has no predecessor digest.
	repo.mutateEvent(c.ID, 1, func(ev *Event) { ev.Digest = nil })
	repo.mutateEvent(c.ID, 2, func(ev *Event) { ev.PrevDigest, ev.Digest = nil, nil })
	repo.mutateEvent(c.ID, 3, func(ev *Event) { ev.PrevDigest = nil })
	repo.rechain(c.ID, 3)

	require.NoError(t, e.VerifyChain(context.Background(), nil, c.ID))
}

func TestVerifyChain_UnknownCaseIsNotFound(t *testing.T) {
	e, _, _ := chainedTestCase(t)

	require.ErrorIs(t, e.VerifyChain(context.Background(), nil, uuid.New()), ErrCaseNotFound)
}
