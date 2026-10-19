// Package logtext holds helpers for bounding text that goes into log lines.
package logtext

import "unicode/utf8"

// Truncate returns s cut to at most n bytes without splitting a UTF-8 rune,
// and whether it was cut. On valid UTF-8 the cut backs off at most 3 bytes.
func Truncate(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}
