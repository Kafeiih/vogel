package config

import (
	"strings"
	"testing"
	"time"
)

// Unset, DB_ACQUIRE_TIMEOUT and DB_LEAK_THRESHOLD default to 3s and 10s.
func TestLoad_PoolTracingDefaults(t *testing.T) {
	setRequiredEnv(t, "development")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Database.AcquireTimeout != 3*time.Second {
		t.Errorf("Database.AcquireTimeout = %s, want 3s", cfg.Database.AcquireTimeout)
	}
	if cfg.Database.LeakThreshold != 10*time.Second {
		t.Errorf("Database.LeakThreshold = %s, want 10s", cfg.Database.LeakThreshold)
	}
}

func TestLoad_PoolTracingValidValues(t *testing.T) {
	setRequiredEnv(t, "development")
	t.Setenv("DB_ACQUIRE_TIMEOUT", "2s")
	t.Setenv("DB_LEAK_THRESHOLD", "1m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Database.AcquireTimeout != 2*time.Second {
		t.Errorf("Database.AcquireTimeout = %s, want 2s", cfg.Database.AcquireTimeout)
	}
	if cfg.Database.LeakThreshold != time.Minute {
		t.Errorf("Database.LeakThreshold = %s, want 1m", cfg.Database.LeakThreshold)
	}
}

// An unparsable, zero or negative value fails the boot naming the variable.
func TestLoad_PoolTracingInvalidValues_FailBoot(t *testing.T) {
	for _, key := range []string{"DB_ACQUIRE_TIMEOUT", "DB_LEAK_THRESHOLD"} {
		for _, tc := range []struct{ value, want string }{
			{"3", key},
			{"soon", key},
			{"0s", key + ": must be greater than zero"},
			{"-1s", key + ": must be greater than zero"},
		} {
			t.Run(key+"="+tc.value, func(t *testing.T) {
				setRequiredEnv(t, "development")
				t.Setenv(key, tc.value)

				_, err := Load()
				if err == nil {
					t.Fatalf("Load() error = nil, want an error for %s=%q", key, tc.value)
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Errorf("error %q does not contain %q", err, tc.want)
				}
			})
		}
	}
}
