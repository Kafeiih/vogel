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
		{"migrate", cmdMigrate},
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
	_, err := parseCommand("start")
	if err == nil {
		t.Fatal("expected error for unknown subcommand")
	}
	for _, valid := range []string{"server", "start-from-migrate", "migrate"} {
		if !strings.Contains(err.Error(), valid) {
			t.Errorf("error %q does not list %q", err, valid)
		}
	}
}
