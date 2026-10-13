package riverschema

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

// These cases fail before any database round trip, so they need no Postgres.
// The happy path (schema created, River tables migrated) is exercised by
// vogel/worker's own integration tests.
func TestMigrate_FailsBeforeTouchingTheDatabase(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		name   string
		schema string
	}{
		{"empty schema name", ""},
		{"schema name with injection attempt", "river; DROP TABLE x"},
		{"valid schema name but no pool", "river"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Migrate(context.Background(), nil, tc.schema, logger); err == nil {
				t.Fatalf("Migrate(nil pool, %q) = nil, want error", tc.schema)
			}
		})
	}
}
