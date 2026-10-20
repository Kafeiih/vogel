package otelpgx

import (
	"context"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// connect returns a *pgx.Conn to db.internal:6432/app backed by a fake
// server over net.Pipe: it accepts the startup message and nothing else,
// enough for the tracer, which only reads the connection's config.
func connect(t *testing.T) *pgx.Conn {
	t.Helper()
	cfg, err := pgx.ParseConfig("postgres://u@db.internal:6432/app?sslmode=disable")
	require.NoError(t, err)
	cfg.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }
	cfg.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go serveStartup(server)
		return client, nil
	}
	conn, err := pgx.ConnectConfig(context.Background(), cfg)
	require.NoError(t, err)
	return conn
}

func serveStartup(c net.Conn) {
	defer c.Close()
	be := pgproto3.NewBackend(c, c)
	if _, err := be.ReceiveStartupMessage(); err != nil {
		return
	}
	be.Send(&pgproto3.AuthenticationOk{})
	be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if err := be.Flush(); err != nil {
		return
	}
	for {
		if _, err := be.Receive(); err != nil {
			return
		}
	}
}

func cached(tr *Tracer) int {
	n := 0
	tr.conns.Range(func(_, _ any) bool { n++; return true })
	return n
}

func TestQuery_CarriesTheConnectionAttributesFromTheCache(t *testing.T) {
	tr, rec := newRecorder(t)
	conn := connect(t)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	for _, sql := range []string{"select 1", "update users set name = $1"} {
		ctx := tr.TraceQueryStart(context.Background(), conn, pgx.TraceQueryStartData{SQL: sql})
		tr.TraceQueryEnd(ctx, conn, pgx.TraceQueryEndData{})
	}

	spans := rec.Ended()
	require.Len(t, spans, 2)
	for i, sql := range []string{"select 1", "update users set name = $1"} {
		a := attrs(spans[i])
		assert.Equal(t, "app", a["db.namespace"].AsString(), "span %d", i)
		assert.Equal(t, "db.internal", a["server.address"].AsString(), "span %d", i)
		assert.Equal(t, int64(6432), a["server.port"].AsInt64(), "span %d", i)
		assert.Equal(t, sql, a["db.query.text"].AsString(), "span %d", i)
		// Every key once: a span that appended to the cached slice would
		// leak its query attributes into the next span on this connection.
		assert.Len(t, spans[i].Attributes(), len(a), "span %d has a duplicate attribute", i)
	}
	assert.Equal(t, 1, cached(tr), "one cache entry per connection")
}

func TestQuery_DropsTheCacheEntryOnceTheConnectionIsCollected(t *testing.T) {
	tr, _ := newRecorder(t)
	func() {
		conn := connect(t)
		ctx := tr.TraceQueryStart(context.Background(), conn, pgx.TraceQueryStartData{SQL: "select 1"})
		tr.TraceQueryEnd(ctx, conn, pgx.TraceQueryEndData{})
		require.NoError(t, conn.Close(context.Background()))
	}()
	require.Equal(t, 1, cached(tr))

	deadline := time.Now().Add(5 * time.Second)
	for cached(tr) != 0 && time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, 0, cached(tr), "the entry of a collected connection must be dropped")
}
