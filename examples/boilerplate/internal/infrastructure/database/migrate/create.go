package migrate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// MigrationsDir is the on-disk location of the SQL migration files, relative to
// the repository root. Create writes new migration skeletons here. This is a dev
// convenience path; at runtime migrations are served from the embedded FS.
const MigrationsDir = "internal/infrastructure/database/migrations"

// migrationTemplate is the goose-format skeleton written for new migrations.
const migrationTemplate = `-- +goose Up
-- +goose StatementBegin
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- +goose StatementEnd
`

// Create writes a new empty goose-format migration to dir, named
// NNN_<slug>.sql where NNN is the next zero-padded sequence number. It returns
// the path of the created file. Used by `./api migrate create <name>`.
func Create(dir, name string) (string, error) {
	slug := slugify(name)
	if slug == "" {
		return "", errors.New("migration name is required")
	}

	next, err := nextSequence(dir)
	if err != nil {
		return "", err
	}

	filename := fmt.Sprintf("%03d_%s.sql", next, slug)
	path := filepath.Join(dir, filename)
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("migration %s already exists", path)
	}

	if err := os.WriteFile(path, []byte(migrationTemplate), 0o600); err != nil {
		return "", fmt.Errorf("writing migration file: %w", err)
	}
	return path, nil
}

// nextSequence scans dir for files named NNN_*.sql and returns the highest
// sequence number plus one, or 1 if there are none.
func nextSequence(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("reading migrations dir: %w", err)
	}

	maxSeq := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		prefix, _, found := strings.Cut(e.Name(), "_")
		if !found {
			continue
		}
		n, convErr := strconv.Atoi(prefix)
		if convErr != nil {
			continue
		}
		if n > maxSeq {
			maxSeq = n
		}
	}
	return maxSeq + 1, nil
}

// slugify lowercases name and replaces any run of non-alphanumeric characters
// with a single underscore, trimming leading/trailing underscores.
func slugify(name string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastUnderscore = false
		case !lastUnderscore:
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}
