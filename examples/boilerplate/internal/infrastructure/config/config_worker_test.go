package config

import (
	"os"
	"testing"
)

func setWorkerEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
}

func TestWorkerConfig_Defaults(t *testing.T) {
	// Clear any worker env vars
	os.Unsetenv("WORKER_SCHEMA")
	os.Unsetenv("WORKER_DEFAULT_MAX_WORKERS")
	os.Unsetenv("WORKER_QUEUES")

	cfg := parseWorkerConfig()

	if cfg.Schema != "river" {
		t.Errorf("Schema: got %q, want %q", cfg.Schema, "river")
	}
	if cfg.DefaultMaxWorkers != 100 {
		t.Errorf("DefaultMaxWorkers: got %d, want %d", cfg.DefaultMaxWorkers, 100)
	}
	if len(cfg.Queues) != 0 {
		t.Errorf("Queues: got %d entries, want 0 (empty = use default queue)", len(cfg.Queues))
	}
}

func TestWorkerConfig_CustomValues(t *testing.T) {
	setWorkerEnv(t, map[string]string{
		"WORKER_SCHEMA":              "jobs",
		"WORKER_DEFAULT_MAX_WORKERS": "50",
		"WORKER_QUEUES":              "critical:20,default:100,low:5",
	})

	cfg := parseWorkerConfig()

	if cfg.Schema != "jobs" {
		t.Errorf("Schema: got %q, want %q", cfg.Schema, "jobs")
	}
	if cfg.DefaultMaxWorkers != 50 {
		t.Errorf("DefaultMaxWorkers: got %d, want %d", cfg.DefaultMaxWorkers, 50)
	}

	wantQueues := map[string]int{"critical": 20, "default": 100, "low": 5}
	if len(cfg.Queues) != len(wantQueues) {
		t.Fatalf("Queues: got %d entries, want %d", len(cfg.Queues), len(wantQueues))
	}
	for name, wantMax := range wantQueues {
		gotMax, ok := cfg.Queues[name]
		if !ok {
			t.Errorf("Queues: missing queue %q", name)
			continue
		}
		if gotMax != wantMax {
			t.Errorf("Queues[%q]: got %d, want %d", name, gotMax, wantMax)
		}
	}
}

func TestWorkerConfig_InvalidMaxWorkers(t *testing.T) {
	setWorkerEnv(t, map[string]string{
		"WORKER_DEFAULT_MAX_WORKERS": "not-a-number",
	})

	cfg := parseWorkerConfig()

	// Should fall back to default on invalid input
	if cfg.DefaultMaxWorkers != 100 {
		t.Errorf("DefaultMaxWorkers: got %d, want %d (fallback)", cfg.DefaultMaxWorkers, 100)
	}
}

func TestWorkerConfig_MalformedQueues(t *testing.T) {
	setWorkerEnv(t, map[string]string{
		"WORKER_QUEUES": "critical:abc,valid:10,malformed",
	})

	cfg := parseWorkerConfig()

	// Only "valid:10" should parse successfully
	if _, ok := cfg.Queues["valid"]; !ok {
		t.Error("Queues: expected 'valid' queue to be present")
	}
	if _, ok := cfg.Queues["critical"]; ok {
		t.Error("Queues: 'critical' with non-numeric max should be skipped")
	}
	if _, ok := cfg.Queues["malformed"]; ok {
		t.Error("Queues: 'malformed' without colon should be skipped")
	}
}
