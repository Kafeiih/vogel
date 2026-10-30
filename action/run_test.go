package action_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/kafeiih/vogel/action"
)

func TestRun_IdempotencyKey_Part0MatchesDocflowSourceKey(t *testing.T) {
	r := action.Run{Effect: "document.generate", CaseID: uuid.New(), Seq: 4}

	// The literal docflow used as its source key before Run.IdempotencyKey.
	assert.Equal(t, fmt.Sprintf("%s/%d/%s", r.CaseID, r.Seq, r.Effect), r.IdempotencyKey())
}

func TestRun_IdempotencyKey_IncludesPart(t *testing.T) {
	r := action.Run{Effect: "notify", CaseID: uuid.New(), Seq: 7, Part: 2}

	assert.Equal(t, r.CaseID.String()+"/7/notify/2", r.IdempotencyKey())
}
