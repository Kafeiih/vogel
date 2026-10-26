package handler

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// captureLogger returns a JSON logger writing to a buffer, and a func that
// decodes every record written so far.
func captureLogger(t *testing.T) (*slog.Logger, func() []map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, nil))
	return l, func() []map[string]any {
		var records []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("decode log %q: %v", line, err)
			}
			records = append(records, rec)
		}
		return records
	}
}
