package stringutil

import (
	"fmt"
	"time"
)

// supportedDateFormats lists date layouts tried in order by ParseFlexibleDate.
// Formats are tried left-to-right; the first successful parse wins.
var supportedDateFormats = []string{
	"02/01/2006", // DD/MM/YYYY
	"02-01-2006", // DD-MM-YYYY
	"2006-01-02", // YYYY-MM-DD (ISO 8601)
}

// ParseFlexibleDate tries to parse s using each of the supported date formats
// in order: DD/MM/YYYY, DD-MM-YYYY, YYYY-MM-DD. Returns the first successful
// parse as UTC. Returns (time.Time{}, error) if none match.
func ParseFlexibleDate(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("stringutil.ParseFlexibleDate: empty string")
	}
	for _, layout := range supportedDateFormats {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("stringutil.ParseFlexibleDate: %q does not match any supported format (DD/MM/YYYY, DD-MM-YYYY, YYYY-MM-DD)", s)
}
