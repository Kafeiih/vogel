package scaffold

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"v0.10.0", "v0.10.0", 0},
		{"v0.10.0", "v0.9.0", 1},
		{"v0.9.9", "v0.10.0", -1},
		{"v1.0.0", "v0.99.99", 1},
		{"v0.10.0-rc.1", "v0.10.0", -1},
		{"v0.10.0", "v0.10.0-rc.1", 1},
		{"v0.10.0-rc.2", "v0.10.0-rc.10", -1},
		{"v0.10.0-alpha", "v0.10.0-beta", -1},
		{"v0.10.0-1", "v0.10.0-alpha", -1},
		{"v0.10.0-rc", "v0.10.0-rc.1", -1},
	}
	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			got, err := CompareVersions(tt.a, tt.b)
			if err != nil {
				t.Fatalf("CompareVersions() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("CompareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestCompareVersionsRejectsInvalid(t *testing.T) {
	for _, v := range []string{"", "0.10.0", "v1.2", "v1.2.3.4", "v01.2.3", "latest"} {
		if _, err := CompareVersions(v, "v1.0.0"); err == nil {
			t.Fatalf("CompareVersions(%q) error = nil, want error", v)
		}
	}
}

func TestCheckMinVersion(t *testing.T) {
	if err := CheckMinVersion("v0.10.0", "v0.10.0"); err != nil {
		t.Fatalf("equal versions: %v", err)
	}
	if err := CheckMinVersion("v0.10.0", ""); err != nil {
		t.Fatalf("empty minimum: %v", err)
	}
	err := CheckMinVersion("v0.9.0", "v0.10.0")
	if err == nil || !strings.Contains(err.Error(), "v0.10.0") {
		t.Fatalf("older version error = %v, want mention of minimum", err)
	}
}

func TestBuildVersion(t *testing.T) {
	info := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Path: "github.com/kafeiih/vogel", Version: v}}, true
		}
	}
	got, err := BuildVersion(info("v0.10.0"))
	if err != nil || got != "v0.10.0" {
		t.Fatalf("BuildVersion(tag) = %q, %v", got, err)
	}

	devel := []struct {
		name string
		read func() (*debug.BuildInfo, bool)
	}{
		{"devel", info("(devel)")},
		{"empty", info("")},
		{"pseudo version", info("v0.10.1-0.20261015120000-abcdef123456")},
		{"dirty build", info("v0.10.0+dirty")},
		{"no build info", func() (*debug.BuildInfo, bool) { return nil, false }},
	}
	for _, tt := range devel {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildVersion(tt.read)
			if err == nil {
				t.Fatal("BuildVersion() error = nil, want error")
			}
			if !strings.Contains(err.Error(), "--from") || !strings.Contains(err.Error(), "@vX.Y.Z") {
				t.Fatalf("error %q should mention --from and @vX.Y.Z", err)
			}
		})
	}
}

func TestValidateVersion(t *testing.T) {
	for _, v := range []string{"v0.10.0", "v1.2.3-rc.1"} {
		if err := ValidateVersion(v); err != nil {
			t.Fatalf("ValidateVersion(%q) = %v", v, err)
		}
	}
	for _, v := range []string{"0.10.0", "v0.10", "v0.10.1-0.20261015120000-abcdef123456", "v1.0.0+meta", "main"} {
		if err := ValidateVersion(v); err == nil {
			t.Fatalf("ValidateVersion(%q) = nil, want error", v)
		}
	}
}
