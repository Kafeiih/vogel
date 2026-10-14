package main

import (
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// The default METRICS_ADDR stays on loopback and off 9090, which is
// Prometheus's own default port: a Prometheus running on the same host
// would otherwise make this example fail to boot.
func TestLoadConfig_MetricsAddrDefault(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db?sslmode=disable")
	t.Setenv("METRICS_ADDR", "") // registers the restore
	if err := os.Unsetenv("METRICS_ADDR"); err != nil {
		t.Fatalf("os.Unsetenv: %v", err)
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.MetricsAddr != "127.0.0.1:2112" {
		t.Errorf("MetricsAddr = %q, want %q", cfg.MetricsAddr, "127.0.0.1:2112")
	}
}

// Binding the metrics listener before serving makes an occupied address a
// boot failure that names METRICS_ADDR's value, instead of an API that
// starts and silently exports nothing.
func TestListenMetrics_OccupiedAddressFails(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer func() { _ = busy.Close() }()
	addr := busy.Addr().String()

	ln, err := listenMetrics(addr)
	if err == nil {
		_ = ln.Close()
		t.Fatalf("listenMetrics(%q) succeeded on an address already in use", addr)
	}
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("error %q does not name the address %q", err, addr)
	}
}

func TestListenMetrics_FreeAddressBinds(t *testing.T) {
	ln, err := listenMetrics("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listenMetrics: %v", err)
	}
	_ = ln.Close()
}

// httpTimeoutVars are the HTTP server timeout variables LoadConfig reads.
var httpTimeoutVars = []string{
	"HTTP_READ_HEADER_TIMEOUT", "HTTP_READ_TIMEOUT", "HTTP_WRITE_TIMEOUT", "HTTP_IDLE_TIMEOUT",
}

// unsetEnv clears key for the duration of the test, restoring it afterwards.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "") // registers the restore
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("os.Unsetenv(%s): %v", key, err)
	}
}

// With no override, every HTTP server timeout has a finite default: a zero
// timeout in net/http means "no timeout", which is exactly what lets a slow
// client hold a connection open forever.
func TestLoadConfig_HTTPTimeoutDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db?sslmode=disable")
	for _, k := range httpTimeoutVars {
		unsetEnv(t, k)
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	want := map[string][2]time.Duration{
		"ReadHeaderTimeout": {cfg.ReadHeaderTimeout, 5 * time.Second},
		"ReadTimeout":       {cfg.ReadTimeout, 15 * time.Second},
		"WriteTimeout":      {cfg.WriteTimeout, 30 * time.Second},
		"IdleTimeout":       {cfg.IdleTimeout, 60 * time.Second},
	}
	for name, got := range want {
		if got[0] != got[1] {
			t.Errorf("%s = %v, want %v", name, got[0], got[1])
		}
	}
}

// A zero or negative timeout would silently disable it, so LoadConfig
// rejects one and names the variable.
func TestLoadConfig_RejectsNonPositiveHTTPTimeouts(t *testing.T) {
	for _, key := range httpTimeoutVars {
		for _, val := range []string{"0s", "-1s"} {
			t.Run(key+"="+val, func(t *testing.T) {
				t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db?sslmode=disable")
				for _, k := range httpTimeoutVars {
					unsetEnv(t, k)
				}
				t.Setenv(key, val)

				_, err := LoadConfig()
				if err == nil {
					t.Fatalf("LoadConfig() with %s=%s succeeded, want an error", key, val)
				}
				if !strings.Contains(err.Error(), key) {
					t.Errorf("error %q does not name %s", err, key)
				}
			})
		}
	}
}

// newAPIServer copies every configured timeout onto the http.Server and
// bounds the header size, so none of them is left at net/http's unbounded
// zero value.
func TestNewAPIServer_SetsTimeoutsAndHeaderLimit(t *testing.T) {
	cfg := Config{
		Port:              8081,
		ReadHeaderTimeout: 1 * time.Second,
		ReadTimeout:       2 * time.Second,
		WriteTimeout:      3 * time.Second,
		IdleTimeout:       4 * time.Second,
	}
	handler := http.NotFoundHandler()

	srv := newAPIServer(cfg, handler)

	if srv.Addr != ":8081" {
		t.Errorf("Addr = %q, want %q", srv.Addr, ":8081")
	}
	if srv.Handler == nil {
		t.Error("Handler is nil")
	}
	if srv.ReadHeaderTimeout != cfg.ReadHeaderTimeout {
		t.Errorf("ReadHeaderTimeout = %v, want %v", srv.ReadHeaderTimeout, cfg.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != cfg.ReadTimeout {
		t.Errorf("ReadTimeout = %v, want %v", srv.ReadTimeout, cfg.ReadTimeout)
	}
	if srv.WriteTimeout != cfg.WriteTimeout {
		t.Errorf("WriteTimeout = %v, want %v", srv.WriteTimeout, cfg.WriteTimeout)
	}
	if srv.IdleTimeout != cfg.IdleTimeout {
		t.Errorf("IdleTimeout = %v, want %v", srv.IdleTimeout, cfg.IdleTimeout)
	}
	if srv.MaxHeaderBytes != maxHeaderBytes || maxHeaderBytes <= 0 || maxHeaderBytes >= http.DefaultMaxHeaderBytes {
		t.Errorf("MaxHeaderBytes = %d (maxHeaderBytes %d), want a positive limit below net/http's default %d",
			srv.MaxHeaderBytes, maxHeaderBytes, http.DefaultMaxHeaderBytes)
	}
}

func TestLoadConfig_RateLimitDefaultsAndValidation(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db?sslmode=disable")
	unsetEnv(t, "RATE_LIMIT_REQUESTS")
	unsetEnv(t, "RATE_LIMIT_WINDOW")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.RateLimitRequests != 300 || cfg.RateLimitWindow != time.Minute {
		t.Errorf("rate limit = %d per %v, want 300 per 1m0s", cfg.RateLimitRequests, cfg.RateLimitWindow)
	}

	for key, val := range map[string]string{"RATE_LIMIT_REQUESTS": "0", "RATE_LIMIT_WINDOW": "0s"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, val)
			if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), key) {
				t.Errorf("LoadConfig() with %s=%s error = %v, want one naming %s", key, val, err, key)
			}
		})
	}
}
