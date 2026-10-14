package postgres

import (
	"errors"
	"testing"

	"github.com/kafeiih/vogel/workflow"
)

// ListByEligibility passes workflow.InboxFilter.Page to LIMIT/OFFSET, so
// these cases pin what a caller of the repository can ask Postgres to scan,
// without a database.
func TestInboxFilterPage(t *testing.T) {
	tests := []struct {
		name       string
		limit      int
		offset     int
		wantLimit  int
		wantOffset int
		wantErr    bool
	}{
		{name: "zero limit uses the default", limit: 0, wantLimit: workflow.DefaultListLimit},
		{name: "negative limit uses the default", limit: -5, wantLimit: workflow.DefaultListLimit},
		{name: "limit within the cap is kept", limit: 50, offset: 10, wantLimit: 50, wantOffset: 10},
		{name: "limit at the cap is kept", limit: workflow.MaxListLimit, wantLimit: workflow.MaxListLimit},
		{name: "limit above the cap is clamped", limit: workflow.MaxListLimit + 1, wantLimit: workflow.MaxListLimit},
		{name: "offset at the cap is kept", limit: 10, offset: workflow.MaxListOffset, wantLimit: 10, wantOffset: workflow.MaxListOffset},
		{name: "offset above the cap is rejected", offset: workflow.MaxListOffset + 1, wantErr: true},
		{name: "negative offset is rejected", offset: -1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limit, offset, err := workflow.InboxFilter{Limit: tt.limit, Offset: tt.offset}.Page()
			if tt.wantErr {
				if !errors.Is(err, workflow.ErrOffsetOutOfRange) {
					t.Fatalf("err = %v, want workflow.ErrOffsetOutOfRange", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if limit != tt.wantLimit || offset != tt.wantOffset {
				t.Errorf("Page() = (%d, %d), want (%d, %d)", limit, offset, tt.wantLimit, tt.wantOffset)
			}
		})
	}
}

func TestListCaps(t *testing.T) {
	if workflow.DefaultListLimit != 20 || workflow.MaxListLimit != 1000 || workflow.MaxListOffset != 100_000 {
		t.Errorf("caps = (%d, %d, %d), want (20, 1000, 100000)", workflow.DefaultListLimit, workflow.MaxListLimit, workflow.MaxListOffset)
	}
}
