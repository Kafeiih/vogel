package audit

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Actions and Statuses exist so no other package repeats these literals. That
// only holds while the lists stay complete, and Go cannot check completeness
// of a hand-written slice — so these tests pin the exact expected sets. A
// constant added to the block above without being added to its list fails
// here, next to the declaration, rather than silently narrowing what an API
// accepts somewhere downstream.
func TestActions_ReturnsEveryDeclaredAction(t *testing.T) {
	assert.Equal(t,
		[]Action{ActionCreate, ActionUpdate, ActionDelete, ActionExecute},
		Actions(),
	)
}

func TestStatuses_ReturnsEveryDeclaredStatus(t *testing.T) {
	assert.Equal(t,
		[]Status{StatusSuccess, StatusFailed, StatusPartial, StatusNoop},
		Statuses(),
	)
}
