// Package logtext holds helpers for bounding text that goes into log lines.
package logtext

import "unicode/utf8"

// Truncate returns s cut to at most n bytes without splitting a UTF-8 rune,
// and whether it was cut. The cut backs off at most utf8.UTFMax-1 bytes,
// which is enough for valid UTF-8; on invalid input (a run of continuation
// bytes) it stops there instead of discarding the whole text.
func Truncate(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	for back := 0; n > 0 && back < utf8.UTFMax-1 && !utf8.RuneStart(s[n]); back++ {
		n--
	}
	return s[:n], true
}
