package postgres

import (
	"errors"
	"testing"

	"github.com/kafeiih/vogel/audit"
)

// List passes audit.Filters.Page to LIMIT/OFFSET, so these cases pin what a
// caller of the repository can ask Postgres to scan, without a database.
func TestFiltersPage(t *testing.T) {
	tests := []struct {
		name       string
		limit      int
		offset     int
		wantLimit  int
		wantOffset int
		wantErr    bool
	}{
		{name: "zero limit uses the default", limit: 0, wantLimit: audit.DefaultListLimit},
		{name: "negative limit uses the default", limit: -5, wantLimit: audit.DefaultListLimit},
		{name: "limit within the cap is kept", limit: 50, offset: 10, wantLimit: 50, wantOffset: 10},
		{name: "limit at the cap is kept", limit: audit.MaxListLimit, wantLimit: audit.MaxListLimit},
		{name: "limit above the cap is clamped", limit: audit.MaxListLimit + 1, wantLimit: audit.MaxListLimit},
		{name: "offset at the cap is kept", limit: 10, offset: audit.MaxListOffset, wantLimit: 10, wantOffset: audit.MaxListOffset},
		{name: "offset above the cap is rejected", offset: audit.MaxListOffset + 1, wantErr: true},
		{name: "negative offset is rejected", offset: -1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limit, offset, err := audit.Filters{Limit: tt.limit, Offset: tt.offset}.Page()
			if tt.wantErr {
				if !errors.Is(err, audit.ErrOffsetOutOfRange) {
					t.Fatalf("err = %v, want audit.ErrOffsetOutOfRange", err)
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
	if audit.DefaultListLimit != 20 || audit.MaxListLimit != 1000 || audit.MaxListOffset != 100_000 {
		t.Errorf("caps = (%d, %d, %d), want (20, 1000, 100000)", audit.DefaultListLimit, audit.MaxListLimit, audit.MaxListOffset)
	}
}
