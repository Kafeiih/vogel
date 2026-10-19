package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slowQueryLine runs one query through tr (threshold 0, so it is always slow)
// and returns the decoded log line.
func slowQueryLine(t *testing.T, tr *SlowQueryTracer, buf *bytes.Buffer, sql string) map[string]any {
	t.Helper()
	ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: sql, Args: []any{1}})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line), "log output: %s", buf.String())
	return line
}

func TestSlowQueryTracer_ShortSQLIsLoggedWhole(t *testing.T) {
	var buf bytes.Buffer
	line := slowQueryLine(t, NewSlowQueryTracer(jsonLogger(&buf), 0), &buf, "SELECT 1")

	assert.Equal(t, "SELECT 1", line["sql"])
	assert.NotContains(t, line, "sql_truncated")
	assert.NotContains(t, line, "sql_hash")
	assert.EqualValues(t, 1, line["args_count"])
}

func TestSlowQueryTracer_LongSQLIsTruncated(t *testing.T) {
	var buf bytes.Buffer
	// A multi-byte rune straddles the cut, so truncation must not split it.
	sql := "SELECT '" + strings.Repeat("a", maxLoggedSQLBytes-9) + "é" + strings.Repeat("b", 100) + "'"
	line := slowQueryLine(t, NewSlowQueryTracer(jsonLogger(&buf), 0), &buf, sql)

	got, _ := line["sql"].(string)
	assert.LessOrEqual(t, len(got), maxLoggedSQLBytes)
	assert.True(t, utf8.ValidString(got), "truncated SQL must stay valid UTF-8")
	assert.True(t, strings.HasPrefix(sql, got))
	assert.Equal(t, true, line["sql_truncated"])
	assert.EqualValues(t, len(sql), line["sql_bytes"])
}

func TestSlowQueryTracer_WithSQLHashLogsOnlyTheHash(t *testing.T) {
	var buf bytes.Buffer
	const sql = "SELECT * FROM users WHERE email = 'alice@example.com'"
	line := slowQueryLine(t, NewSlowQueryTracer(jsonLogger(&buf), 0, WithSlowQuerySQLHash()), &buf, sql)

	sum := sha256.Sum256([]byte(sql))
	assert.Equal(t, hex.EncodeToString(sum[:sqlHashBytes]), line["sql_hash"])
	assert.NotContains(t, line, "sql")
	assert.NotContains(t, buf.String(), "alice@example.com")
	assert.EqualValues(t, 1, line["args_count"])
}

func TestNewPoolConfig_HashSlowQuerySQL(t *testing.T) {
	poolConfig, err := newPoolConfig(Config{
		URL:              "postgres://u:p@localhost:5432/db?sslmode=disable",
		HashSlowQuerySQL: true,
	}, discardLogger())
	require.NoError(t, err)

	tr, ok := poolConfig.ConnConfig.Tracer.(*poolTracer)
	require.True(t, ok)
	assert.True(t, tr.hashSQL)
}

// slowQueryLineWithErr is slowQueryLine for a query that ends with err.
func slowQueryLineWithErr(t *testing.T, tr *SlowQueryTracer, buf *bytes.Buffer, err error) map[string]any {
	t.Helper()
	ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT $1::uuid"})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: err})
	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line), "log output: %s", buf.String())
	return line
}

// A PostgreSQL error message can quote the offending value; hash mode keeps
// it out by logging only the SQLSTATE.
func TestSlowQueryTracer_WithSQLHashLogsOnlyTheSQLState(t *testing.T) {
	var buf bytes.Buffer
	pgErr := &pgconn.PgError{
		Severity: "ERROR", Code: "22P02",
		Message: `invalid input syntax for type uuid: "alice@example.com"`,
	}
	line := slowQueryLineWithErr(t, NewSlowQueryTracer(jsonLogger(&buf), 0, WithSlowQuerySQLHash()), &buf, fmt.Errorf("query: %w", pgErr))

	assert.Equal(t, "22P02", line["error_code"])
	assert.NotContains(t, line, "error_constraint")
	assert.NotContains(t, line, "error")
	assert.NotContains(t, buf.String(), "alice@example.com")
}

// A unique violation names its constraint; its Detail quotes the row values
// and is never logged in hash mode.
func TestSlowQueryTracer_WithSQLHashLogsTheConstraint(t *testing.T) {
	var buf bytes.Buffer
	pgErr := &pgconn.PgError{
		Severity: "ERROR", Code: "23505",
		Message:        `duplicate key value violates unique constraint "users_email_key"`,
		Detail:         "Key (email)=(alice@example.com) already exists.",
		ConstraintName: "users_email_key",
	}
	line := slowQueryLineWithErr(t, NewSlowQueryTracer(jsonLogger(&buf), 0, WithSlowQuerySQLHash()), &buf, pgErr)

	assert.Equal(t, "23505", line["error_code"])
	assert.Equal(t, "users_email_key", line["error_constraint"])
	assert.NotContains(t, buf.String(), "alice@example.com")
}

// A pgx client-side error (encoding an argument, scanning a result) can quote
// a value too, so in hash mode any other error is logged by type only.
func TestSlowQueryTracer_WithSQLHashLogsOtherErrorsByType(t *testing.T) {
	var buf bytes.Buffer
	encErr := fmt.Errorf("failed to encode args[0]: unable to encode %q into binary format for uuid", "alice@example.com")
	line := slowQueryLineWithErr(t, NewSlowQueryTracer(jsonLogger(&buf), 0, WithSlowQuerySQLHash()), &buf, encErr)

	assert.Equal(t, "*errors.errorString", line["error_type"])
	assert.NotContains(t, line, "error")
	assert.NotContains(t, buf.String(), "alice@example.com")
}

// A canceled or timed-out context carries no query data and keeps its fixed
// text in hash mode.
func TestSlowQueryTracer_WithSQLHashKeepsContextErrors(t *testing.T) {
	for _, err := range []error{context.Canceled, fmt.Errorf("query: %w", context.DeadlineExceeded)} {
		var buf bytes.Buffer
		line := slowQueryLineWithErr(t, NewSlowQueryTracer(jsonLogger(&buf), 0, WithSlowQuerySQLHash()), &buf, err)
		want := context.Canceled.Error()
		if errors.Is(err, context.DeadlineExceeded) {
			want = context.DeadlineExceeded.Error()
		}
		assert.Equal(t, want, line["error"])
		assert.NotContains(t, line, "error_type")
	}
}

// Without hash mode the error text is kept, bounded and flagged when cut.
func TestSlowQueryTracer_ErrorTextIsBounded(t *testing.T) {
	var buf bytes.Buffer
	long := errors.New(strings.Repeat("e", maxLoggedErrorBytes) + "tail")
	line := slowQueryLineWithErr(t, NewSlowQueryTracer(jsonLogger(&buf), 0), &buf, long)

	got, _ := line["error"].(string)
	assert.LessOrEqual(t, len(got), maxLoggedErrorBytes)
	assert.NotContains(t, got, "tail")
	assert.Equal(t, true, line["error_truncated"])
}

func TestSlowQueryTracer_ShortErrorIsNotFlagged(t *testing.T) {
	var buf bytes.Buffer
	line := slowQueryLineWithErr(t, NewSlowQueryTracer(jsonLogger(&buf), 0), &buf, errors.New("boom"))
	assert.Equal(t, "boom", line["error"])
	assert.NotContains(t, line, "error_truncated")
}

func TestSlowQueryTracer_SQLHashLength(t *testing.T) {
	var buf bytes.Buffer
	line := slowQueryLine(t, NewSlowQueryTracer(jsonLogger(&buf), 0, WithSlowQuerySQLHash()), &buf, "SELECT 1")
	h, _ := line["sql_hash"].(string)
	assert.Len(t, h, 2*sqlHashBytes)
}
