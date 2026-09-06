package stringutil

import (
	"testing"
	"time"
)

func TestParseFlexibleDate(t *testing.T) {
	want := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		input   string
		want    time.Time
		wantErr bool
	}{
		{"DD/MM/YYYY format", "31/12/2024", want, false},
		{"DD-MM-YYYY format", "31-12-2024", want, false},
		{"YYYY-MM-DD format", "2024-12-31", want, false},
		{"wrong order YYYY-DD-MM", "2024-31-12", time.Time{}, true},
		{"not a date", "not-a-date", time.Time{}, true},
		{"empty string", "", time.Time{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseFlexibleDate(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseFlexibleDate(%q) expected error, got nil", tt.input)
				}
				if !got.IsZero() {
					t.Errorf("ParseFlexibleDate(%q) expected zero time on error, got %v", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseFlexibleDate(%q) unexpected error: %v", tt.input, err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("ParseFlexibleDate(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
