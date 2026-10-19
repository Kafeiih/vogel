package logtext

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		name      string
		s         string
		n         int
		want      string
		truncated bool
	}{
		{name: "shorter than the limit", s: "abc", n: 5, want: "abc"},
		{name: "exactly the limit", s: "abcde", n: 5, want: "abcde"},
		{name: "ASCII over the limit", s: "abcdef", n: 5, want: "abcde", truncated: true},
		{name: "two-byte rune straddling the cut", s: "abcdé", n: 5, want: "abcd", truncated: true},
		{name: "three-byte rune straddling the cut", s: "ab日本", n: 4, want: "ab", truncated: true},
		{name: "empty", s: "", n: 0, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, truncated := Truncate(tt.s, tt.n)
			if got != tt.want || truncated != tt.truncated {
				t.Errorf("Truncate(%q, %d) = (%q, %v), want (%q, %v)", tt.s, tt.n, got, truncated, tt.want, tt.truncated)
			}
			if !utf8.ValidString(got) {
				t.Errorf("result %q is not valid UTF-8", got)
			}
			if !strings.HasPrefix(tt.s, got) {
				t.Errorf("result %q is not a prefix of %q", got, tt.s)
			}
		})
	}
}

// On invalid UTF-8 the back-off is bounded, so the text is not lost.
func TestTruncate_InvalidUTF8KeepsMostOfTheText(t *testing.T) {
	s := strings.Repeat("\x80", 100)
	got, truncated := Truncate(s, 50)
	if !truncated || len(got) < 50-(utf8.UTFMax-1) {
		t.Errorf("Truncate(100 continuation bytes, 50) = %d bytes, want at least %d", len(got), 50-(utf8.UTFMax-1))
	}
}
