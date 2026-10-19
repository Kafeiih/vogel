package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
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
	assert.Equal(t, hex.EncodeToString(sum[:8]), line["sql_hash"])
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
