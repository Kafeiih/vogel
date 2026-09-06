package config

import (
	"fmt"
	"strings"
	"time"
)

// Errors accumulates configuration problems found while reading and
// validating environment variables, so a boot failure can report every
// missing or invalid variable at once instead of the caller restarting the
// process once per problem to discover them one at a time.
//
// The zero value is ready to use.
type Errors struct {
	errs []string
}

// Add records err if it is non-nil.
func (e *Errors) Add(err error) {
	if err != nil {
		e.errs = append(e.errs, err.Error())
	}
}

// Addf records a formatted message.
func (e *Errors) Addf(format string, args ...any) {
	e.errs = append(e.errs, fmt.Sprintf(format, args...))
}

// HasErrors reports whether any problem has been recorded.
func (e *Errors) HasErrors() bool {
	return len(e.errs) > 0
}

// Err returns nil if no problem was recorded, or a single error listing every
// recorded problem, one per line.
func (e *Errors) Err() error {
	if len(e.errs) == 0 {
		return nil
	}
	return fmt.Errorf("config errors:\n  %s", strings.Join(e.errs, "\n  "))
}

// Require reads key via Require and records an error if it is missing,
// returning "" in that case. Use this for every required variable so a single
// Err() call at the end reports all of them together.
func (e *Errors) Require(key string) string {
	v, err := Require(key)
	if err != nil {
		e.Add(err)
		return ""
	}
	return v
}

// Int reads key via Int and records an error (returning defaultValue) if the
// value is present but invalid.
func (e *Errors) Int(key string, defaultValue int) int {
	v, err := Int(key, defaultValue)
	if err != nil {
		e.Add(err)
		return defaultValue
	}
	return v
}

// Int32 reads key via Int32 and records an error (returning defaultValue) if
// the value is present but invalid.
func (e *Errors) Int32(key string, defaultValue int32) int32 {
	v, err := Int32(key, defaultValue)
	if err != nil {
		e.Add(err)
		return defaultValue
	}
	return v
}

// Duration reads key via Duration and records an error (returning
// defaultValue) if the value is present but invalid.
func (e *Errors) Duration(key string, defaultValue time.Duration) time.Duration {
	v, err := Duration(key, defaultValue)
	if err != nil {
		e.Add(err)
		return defaultValue
	}
	return v
}
