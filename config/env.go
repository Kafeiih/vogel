// Package config provides primitives for reading and validating configuration
// from environment variables: typed readers with defaults, a required-variable
// reader that fails with a clear message, an error accumulator so a boot
// failure reports every problem at once instead of one per restart, and
// semantic validators (well-formed URL, int range, allowed set, min<=max
// pairs) for cases where "non-empty" is not enough to catch a misconfiguration.
//
// This package intentionally does NOT define application config structs (like
// a DatabaseConfig or a NotificationConfig) — those are shaped by whatever the
// consuming application needs and stay in the application. What lives here is
// only the plumbing every application reaches for to build such structs.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// String returns the environment variable named key, or defaultValue if it is
// unset or empty.
func String(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

// Require returns the environment variable named key, or an error naming the
// missing variable if it is unset or empty.
//
// Prefer Errors.Require when reading several required variables, so every
// missing one is reported together instead of failing the boot sequence on
// the first one found.
func Require(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("environment variable %s is required", key)
	}
	return v, nil
}

// Bool returns the environment variable named key parsed with strconv.ParseBool,
// or defaultValue if it is unset, empty, or not a valid boolean.
//
// Bool never reports a parse failure: a typo silently falls back to the
// default. That is fine for genuinely optional toggles, but a variable whose
// value picks between meaningfully different behaviors (e.g. selecting a
// provider) should use Require plus OneOf instead, so a typo is reported
// rather than silently substituting a default.
func Bool(key string, defaultValue bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return defaultValue
	}
	return b
}

// Int parses the environment variable named key as an int. It returns
// defaultValue if the variable is unset or empty, and an error if it is
// present but not a valid integer.
func Int(key string, defaultValue int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid int for %s: %w", key, err)
	}
	return n, nil
}

// Int32 parses the environment variable named key as an int32. It returns
// defaultValue if the variable is unset or empty, and an error if it is
// present but not a valid int32.
func Int32(key string, defaultValue int32) (int32, error) {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue, nil
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid int32 for %s: %w", key, err)
	}
	return int32(n), nil
}

// Duration parses the environment variable named key with time.ParseDuration.
// It returns defaultValue if the variable is unset or empty, and an error if
// it is present but not a valid duration.
func Duration(key string, defaultValue time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid duration for %s: %w", key, err)
	}
	return d, nil
}

// StringSlice splits the environment variable named key on sep, trims
// whitespace from each entry, and drops empty entries. It returns nil if the
// variable is unset or empty.
func StringSlice(key, sep string) []string {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	var result []string
	for _, part := range strings.Split(v, sep) {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
