package config

import (
	"strings"
	"testing"
	"time"

	"github.com/kafeiih/vogel/config"
)

func parseBPMS(t *testing.T) (BPMSConfig, error) {
	t.Helper()
	var errs config.Errors
	cfg := parseBPMSConfig(&errs)
	return cfg, errs.Err()
}

func TestParseBPMSConfig_Defaults(t *testing.T) {
	t.Setenv("GOTENBERG_URL", "http://gotenberg:3000")
	unsetEnv(t, "GOTENBERG_TIMEOUT")
	unsetEnv(t, "BPMS_DOCUMENT_STORAGE")

	cfg, err := parseBPMS(t)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Gotenberg.BaseURL != "http://gotenberg:3000" {
		t.Errorf("BaseURL = %q, want http://gotenberg:3000", cfg.Gotenberg.BaseURL)
	}
	if cfg.Gotenberg.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s", cfg.Gotenberg.Timeout)
	}
	if cfg.DocumentStorage != "documents" {
		t.Errorf("DocumentStorage = %q, want documents", cfg.DocumentStorage)
	}
}

func TestParseBPMSConfig_ReadsOverrides(t *testing.T) {
	t.Setenv("GOTENBERG_URL", "https://pdf.internal")
	t.Setenv("GOTENBERG_TIMEOUT", "45s")
	t.Setenv("BPMS_DOCUMENT_STORAGE", "private")

	cfg, err := parseBPMS(t)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Gotenberg.Timeout != 45*time.Second || cfg.DocumentStorage != "private" {
		t.Errorf("got %+v, want a 45s timeout and the private storage", cfg)
	}
}

func TestParseBPMSConfig_Invalid(t *testing.T) {
	for _, tc := range []struct {
		name, url, timeout, want string
	}{
		{"missing url", "", "", "GOTENBERG_URL"},
		{"relative url", "gotenberg:3000", "", "GOTENBERG_URL: gotenberg: base url"},
		{"no host", "http://", "", "GOTENBERG_URL: gotenberg: base url"},
		{"zero timeout", "http://gotenberg:3000", "0s", "GOTENBERG_TIMEOUT: must be greater than zero"},
		{"negative timeout", "http://gotenberg:3000", "-1s", "GOTENBERG_TIMEOUT: must be greater than zero"},
		{"unparsable timeout", "http://gotenberg:3000", "soon", "GOTENBERG_TIMEOUT"},
		{"timeout past the job timeout", "http://gotenberg:3000", "46s", "GOTENBERG_TIMEOUT: must be at most 45s, got 46s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.url == "" {
				unsetEnv(t, "GOTENBERG_URL")
			} else {
				t.Setenv("GOTENBERG_URL", tc.url)
			}
			if tc.timeout == "" {
				unsetEnv(t, "GOTENBERG_TIMEOUT")
			} else {
				t.Setenv("GOTENBERG_TIMEOUT", tc.timeout)
			}

			_, err := parseBPMS(t)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestLoad_BPMSRequiresGotenbergURL(t *testing.T) {
	setRequiredEnv(t, "development")
	unsetEnv(t, "GOTENBERG_URL")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "GOTENBERG_URL") {
		t.Fatalf("Load() error = %v, want one naming GOTENBERG_URL", err)
	}
}

func TestLoad_BPMSDocumentStorageMustBeConfigured(t *testing.T) {
	setRequiredEnv(t, "development")
	t.Setenv("STORAGE_CONFIGS", "public")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), `BPMS_DOCUMENT_STORAGE: "documents" is not listed in STORAGE_CONFIGS`) {
		t.Fatalf("Load() error = %v, want one naming BPMS_DOCUMENT_STORAGE", err)
	}
}

func TestLoad_BPMS(t *testing.T) {
	setRequiredEnv(t, "development")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.BPMS.Gotenberg.BaseURL == "" || cfg.BPMS.DocumentStorage != "documents" {
		t.Errorf("BPMS = %+v, want the Gotenberg URL and the documents storage", cfg.BPMS)
	}
}

func TestParseBPMSConfig_ProfileHolders(t *testing.T) {
	t.Setenv("GOTENBERG_URL", "http://gotenberg:3000")
	t.Setenv("BPMS_PROFILE_HOLDERS", " encargado@atencion=123 , encargado@atencion=456,jefe@ti=789 ")

	cfg, err := parseBPMS(t)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []ProfileHolder{
		{Position: "encargado", Unit: "atencion", UserID: "123"},
		{Position: "encargado", Unit: "atencion", UserID: "456"},
		{Position: "jefe", Unit: "ti", UserID: "789"},
	}
	if len(cfg.ProfileHolders) != len(want) {
		t.Fatalf("ProfileHolders = %+v, want %+v", cfg.ProfileHolders, want)
	}
	for i := range want {
		if cfg.ProfileHolders[i] != want[i] {
			t.Errorf("ProfileHolders[%d] = %+v, want %+v", i, cfg.ProfileHolders[i], want[i])
		}
	}
}

func TestParseBPMSConfig_ProfileHoldersUnsetIsEmpty(t *testing.T) {
	t.Setenv("GOTENBERG_URL", "http://gotenberg:3000")
	unsetEnv(t, "BPMS_PROFILE_HOLDERS")

	cfg, err := parseBPMS(t)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.ProfileHolders) != 0 {
		t.Errorf("ProfileHolders = %+v, want none", cfg.ProfileHolders)
	}
}

func TestParseBPMSConfig_InvalidProfileHolders(t *testing.T) {
	for _, v := range []string{"encargado=123", "encargado@atencion", "@atencion=123", "encargado@=123", "encargado@atencion="} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("GOTENBERG_URL", "http://gotenberg:3000")
			t.Setenv("BPMS_PROFILE_HOLDERS", v)

			_, err := parseBPMS(t)
			if err == nil || !strings.Contains(err.Error(), "BPMS_PROFILE_HOLDERS: entry "+`"`+v+`"`+" must look like position@unit=user-id") {
				t.Fatalf("error = %v, want a BPMS_PROFILE_HOLDERS format error", err)
			}
		})
	}
}
