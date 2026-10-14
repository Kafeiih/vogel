package main

import (
	"net"
	"os"
	"strings"
	"testing"
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
