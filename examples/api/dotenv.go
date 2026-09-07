package main

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"strings"
)

// loadDotEnv reads KEY=VALUE pairs from path into the process environment.
//
// It exists because vogel's config package reads os.Getenv and nothing else:
// it deliberately owns no file format, no search path and no precedence
// rules, so that a consumer is free to feed the environment from a .env
// file, a secrets manager, Kubernetes, or systemd without the library
// having an opinion. This function is that consumer decision, made here for
// local development, in about thirty lines and with no dependency.
//
// Two rules make it safe to call unconditionally:
//
//   - A missing file is not an error. In production there is no .env; the
//     platform injects real environment variables and this call is a no-op.
//   - A variable already present in the environment is never overwritten.
//     The real environment always wins over the file, so `PORT=9000 go run .`
//     does what it says even with a PORT line in .env.
//
// It is intentionally minimal: no interpolation, no multi-line values, no
// escape sequences. If you need those, reach for a real dotenv library in
// your own application -- but note that you only need it in development.
func loadDotEnv(path string) error {
	f, err := os.Open(path) //nolint:gosec // the path is a fixed dev-only constant
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		line = strings.TrimPrefix(line, "export ")

		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}

		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}

		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}

		// The real environment wins: only set what is not already there.
		if _, ok := os.LookupEnv(key); ok {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}

	return scanner.Err()
}
