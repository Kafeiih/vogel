package config

import (
	"os"
	"strings"
	"testing"

	"github.com/kafeiih/vogel/config"
	"github.com/kafeiih/vogel/worker"
)

func mustParseWorkerConfig(t *testing.T) worker.Config {
	t.Helper()
	var errs config.Errors
	cfg := parseWorkerConfig(&errs)
	if err := errs.Err(); err != nil {
		t.Fatalf("parseWorkerConfig: unexpected config error: %v", err)
	}
	return cfg
}

func TestParseWorkerConfig_PollOnly(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    bool
		wantErr bool
	}{
		{name: "unset keeps LISTEN/NOTIFY", value: "", want: false},
		{name: "true polls only", value: "true", want: true},
		{name: "false keeps LISTEN/NOTIFY", value: "false", want: false},
		{name: "unparsable value is a config error", value: "sometimes", want: false, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value == "" {
				unsetEnv(t, "WORKER_POLL_ONLY")
			} else {
				t.Setenv("WORKER_POLL_ONLY", tt.value)
			}

			var errs config.Errors
			got := parseWorkerConfig(&errs)

			err := errs.Err()
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "WORKER_POLL_ONLY") {
					t.Fatalf("errs.Err() = %v, want an error naming WORKER_POLL_ONLY", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected config error: %v", err)
			}
			if got.PollOnly != tt.want {
				t.Errorf("PollOnly = %v, want %v", got.PollOnly, tt.want)
			}
		})
	}
}

func TestLoad_InvalidWorkerPollOnlyFails(t *testing.T) {
	setRequiredEnv(t, "development")
	t.Setenv("WORKER_POLL_ONLY", "sometimes")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "WORKER_POLL_ONLY") {
		t.Fatalf("Load() error = %v, want an error naming WORKER_POLL_ONLY", err)
	}
}

func TestLoad_WorkerPollOnlyReachesWorkerConfig(t *testing.T) {
	setRequiredEnv(t, "development")
	t.Setenv("WORKER_POLL_ONLY", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Worker.PollOnly {
		t.Error("cfg.Worker.PollOnly = false, want true")
	}
}

func setWorkerEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
}

func TestWorkerConfig_Defaults(t *testing.T) {
	os.Unsetenv("WORKER_SCHEMA")
	os.Unsetenv("WORKER_DEFAULT_MAX_WORKERS")
	os.Unsetenv("WORKER_QUEUES")
	unsetEnv(t, "WORKER_POLL_ONLY")

	cfg := mustParseWorkerConfig(t)

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

	cfg := mustParseWorkerConfig(t)

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

	cfg := mustParseWorkerConfig(t)

	if cfg.DefaultMaxWorkers != 100 {
		t.Errorf("DefaultMaxWorkers: got %d, want %d (fallback)", cfg.DefaultMaxWorkers, 100)
	}
}

func TestWorkerConfig_MalformedQueues(t *testing.T) {
	setWorkerEnv(t, map[string]string{
		"WORKER_QUEUES": "critical:abc,valid:10,malformed",
	})

	cfg := mustParseWorkerConfig(t)

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

func TestWorkerSchema_DefaultsToRiver(t *testing.T) {
	t.Setenv("WORKER_SCHEMA", "")

	if got := WorkerSchema(); got != "river" {
		t.Errorf("WorkerSchema() = %q, want %q", got, "river")
	}
}

func TestWorkerSchema_ReadsWorkerSchemaEnv(t *testing.T) {
	t.Setenv("WORKER_SCHEMA", "jobs")

	if got := WorkerSchema(); got != "jobs" {
		t.Errorf("WorkerSchema() = %q, want %q", got, "jobs")
	}
	if got := mustParseWorkerConfig(t).Schema; got != "jobs" {
		t.Errorf("parseWorkerConfig().Schema = %q, want %q", got, "jobs")
	}
}

func TestParseMetricsConfig_WorkerListenAddr(t *testing.T) {
	t.Run("defaults to :9091, apart from the api's :9090", func(t *testing.T) {
		unsetEnv(t, "WORKER_METRICS_LISTEN_ADDR")
		unsetEnv(t, "METRICS_LISTEN_ADDR")
		got := parseMetricsConfig()
		if got.WorkerListenAddr != ":9091" || got.ListenAddr != ":9090" {
			t.Errorf("(WorkerListenAddr, ListenAddr) = (%q, %q), want (\":9091\", \":9090\")", got.WorkerListenAddr, got.ListenAddr)
		}
	})
	t.Run("is read from its own variable", func(t *testing.T) {
		t.Setenv("WORKER_METRICS_LISTEN_ADDR", "127.0.0.1:9191")
		unsetEnv(t, "METRICS_LISTEN_ADDR")
		got := parseMetricsConfig()
		if got.WorkerListenAddr != "127.0.0.1:9191" || got.ListenAddr != ":9090" {
			t.Errorf("(WorkerListenAddr, ListenAddr) = (%q, %q), want (\"127.0.0.1:9191\", \":9090\")", got.WorkerListenAddr, got.ListenAddr)
		}
	})
}
