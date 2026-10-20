package main

import (
	"strings"
	"testing"
)

func TestParseCommand(t *testing.T) {
	cases := []struct {
		subcmd string
		want   command
	}{
		{"", cmdServer},
		{"server", cmdServer},
		{"start-from-migrate", cmdStartFromMigrate},
	}
	for _, tc := range cases {
		t.Run("subcommand "+tc.subcmd, func(t *testing.T) {
			got, err := parseCommand(tc.subcmd)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("parseCommand(%q) = %d, want %d", tc.subcmd, got, tc.want)
			}
		})
	}
}

func TestParseCommand_UnknownListsValidSubcommands(t *testing.T) {
	for _, subcmd := range []string{"start", "migrate"} {
		_, err := parseCommand(subcmd)
		if err == nil {
			t.Fatalf("expected error for unknown subcommand %q", subcmd)
		}
		for _, valid := range []string{"server", "start-from-migrate"} {
			if !strings.Contains(err.Error(), valid) {
				t.Errorf("error %q does not list %q", err, valid)
			}
		}
	}
}
