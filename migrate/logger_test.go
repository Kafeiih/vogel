package migrate

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"
)

// TestSlogAdapter_PrintfIsStructured pins that goose's output keeps its
// format string as the message and its values as structured attributes, so
// the logger's key-based redaction and log queries apply to it instead of
// one pre-formatted string.
func TestSlogAdapter_PrintfIsStructured(t *testing.T) {
	var buf bytes.Buffer
	a := newSlogAdapter(slog.New(slog.NewJSONHandler(&buf, nil)))

	a.Printf("goose: %s (%d)\n", "OK 0001_init.sql", 42)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("decode log line %q: %v", buf.String(), err)
	}
	if line["msg"] != "goose: %s (%d)" {
		t.Errorf("msg = %q, want the format string without its newline", line["msg"])
	}
	args, _ := line["args"].([]any)
	if len(args) != 2 || args[0] != "OK 0001_init.sql" || args[1] != float64(42) {
		t.Errorf("args = %v, want [OK 0001_init.sql 42]", line["args"])
	}
	if line["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", line["level"])
	}
}

func TestSlogAdapter_PrintfWithoutArgsOmitsArgs(t *testing.T) {
	var buf bytes.Buffer
	a := newSlogAdapter(slog.New(slog.NewJSONHandler(&buf, nil)))

	a.Printf("goose: no migrations to run")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("decode log line %q: %v", buf.String(), err)
	}
	if line["msg"] != "goose: no migrations to run" {
		t.Errorf("msg = %q", line["msg"])
	}
	if _, ok := line["args"]; ok {
		t.Errorf("args present for a message without values: %v", line["args"])
	}
}

func TestSlogAdapter_RendersErrorsAndStringersAsText(t *testing.T) {
	var buf bytes.Buffer
	a := newSlogAdapter(slog.New(slog.NewJSONHandler(&buf, nil)))

	a.Printf("OK   %s (%s): %v", "0001_init.sql", 1500*time.Millisecond, errors.New("lock held"))

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("decode log line %q: %v", buf.String(), err)
	}
	args, _ := line["args"].([]any)
	if len(args) != 3 || args[1] != "1.5s" || args[2] != "lock held" {
		t.Errorf("args = %v, want [0001_init.sql 1.5s lock held]", line["args"])
	}
}
