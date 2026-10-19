package tracing

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/kafeiih/vogel/otel"
)

func TestSetup_DisabledWithoutEndpoint(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	tp, err := Setup(context.Background(), otel.Config{}, "api", logger)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if tp.Enabled() {
		t.Error("tracing enabled without an endpoint")
	}
	if !strings.Contains(buf.String(), "tracing disabled") {
		t.Errorf("log = %q, want the disabled line", buf.String())
	}
	Shutdown(tp, logger) // a disabled provider shuts down cleanly
}

// The binary's name is the default service name, and the endpoint, which
// can carry a credential, is not logged.
func TestSetup_DefaultsTheServiceNameToTheBinary(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	tp, err := Setup(context.Background(), otel.Config{Endpoint: "http://user:secret@127.0.0.1:4318"}, "worker", logger)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer Shutdown(tp, logger)

	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
		t.Fatalf("decode %q: %v", buf.String(), err)
	}
	if line["service"] != "worker" || line["sample_ratio"] != otel.DefaultSampleRatio {
		t.Errorf("line = %v, want service worker and the default ratio", line)
	}
	if strings.Contains(buf.String(), "secret") {
		t.Errorf("log leaks the endpoint credential: %q", buf.String())
	}
}

func TestSetup_InvalidConfigFails(t *testing.T) {
	_, err := Setup(context.Background(), otel.Config{Endpoint: "http://127.0.0.1:4318", SampleRatio: 2}, "api", slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("Setup accepted a sample ratio of 2")
	}
}
