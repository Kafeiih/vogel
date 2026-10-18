package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/reqctx"
)

// insert runs the middleware's insert hook over params and fails the test if
// the inner insert is not called.
func insert(t *testing.T, ctx context.Context, params ...*rivertype.JobInsertParams) {
	t.Helper()
	called := false
	_, err := (&requestIDMiddleware{}).InsertMany(ctx, params, func(context.Context) ([]*rivertype.JobInsertResult, error) {
		called = true
		return nil, nil
	})
	require.NoError(t, err)
	require.True(t, called, "inner insert must run")
}

// work runs the middleware's work hook over a job with the given metadata
// and returns the request ID the job's context carried.
func work(t *testing.T, ctx context.Context, metadata []byte) string {
	t.Helper()
	var got string
	err := (&requestIDMiddleware{}).Work(ctx, &rivertype.JobRow{Metadata: metadata}, func(ctx context.Context) error {
		got = reqctx.RequestIDFromContext(ctx)
		return nil
	})
	require.NoError(t, err)
	return got
}

func metadataOf(t *testing.T, p *rivertype.JobInsertParams) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(p.Metadata, &m))
	return m
}

// A job enqueued inside a request is worked with that request's ID, so the
// worker's logs and audit rows correlate with the request that caused them.
func TestRequestIDMiddleware_RoundTripsRequestIDThroughMetadata(t *testing.T) {
	ctx := reqctx.WithRequestID(context.Background(), "req-1")
	p := &rivertype.JobInsertParams{Metadata: []byte(`{}`)}

	insert(t, ctx, p)

	assert.Equal(t, "req-1", work(t, context.Background(), p.Metadata))
}

// Every job of a batch insert carries the ID.
func TestRequestIDMiddleware_Insert_StampsEveryJob(t *testing.T) {
	ctx := reqctx.WithRequestID(context.Background(), "req-2")
	a := &rivertype.JobInsertParams{}
	b := &rivertype.JobInsertParams{Metadata: []byte(`{}`)}

	insert(t, ctx, a, b)

	assert.Equal(t, "req-2", metadataOf(t, a)[requestIDMetadataKey])
	assert.Equal(t, "req-2", metadataOf(t, b)[requestIDMetadataKey])
}

// Metadata the caller set through InsertOpts is kept.
func TestRequestIDMiddleware_Insert_KeepsExistingMetadata(t *testing.T) {
	ctx := reqctx.WithRequestID(context.Background(), "req-3")
	p := &rivertype.JobInsertParams{Metadata: []byte(`{"tenant":"acme"}`)}

	insert(t, ctx, p)

	m := metadataOf(t, p)
	assert.Equal(t, "acme", m["tenant"])
	assert.Equal(t, "req-3", m[requestIDMetadataKey])
}

// Outside a request (a periodic job, a CLI) the metadata is left untouched.
func TestRequestIDMiddleware_Insert_NoRequestID_LeavesMetadataAlone(t *testing.T) {
	p := &rivertype.JobInsertParams{Metadata: []byte(`{"tenant":"acme"}`)}

	insert(t, context.Background(), p)

	assert.JSONEq(t, `{"tenant":"acme"}`, string(p.Metadata))
}

// Metadata River cannot decode is not this middleware's to fix: it is passed
// through unchanged rather than failing the insert.
func TestRequestIDMiddleware_Insert_InvalidMetadata_PassedThrough(t *testing.T) {
	ctx := reqctx.WithRequestID(context.Background(), "req-4")
	p := &rivertype.JobInsertParams{Metadata: []byte(`not json`)}

	insert(t, ctx, p)

	assert.Equal(t, `not json`, string(p.Metadata))
}

// Jobs inserted before this middleware existed, or outside a request, are
// worked with no request ID rather than failing.
func TestRequestIDMiddleware_Work_WithoutRequestID(t *testing.T) {
	for name, metadata := range map[string][]byte{
		"empty":   nil,
		"no key":  []byte(`{"tenant":"acme"}`),
		"invalid": []byte(`not json`),
		"non-str": []byte(`{"vogel_request_id":42}`),
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, "", work(t, context.Background(), metadata))
		})
	}
}

// The middleware and the app's logger reach River's config in both the API
// (insert-only) and the worker process, since the ID is written by the first
// and read by the second.
func TestBuildRiverConfig_InstallsRequestIDMiddlewareAndLogger(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	for name, workers := range map[string]*river.Workers{
		"insert-only": nil,
		"worker":      river.NewWorkers(),
	} {
		t.Run(name, func(t *testing.T) {
			rc := buildRiverConfig(Config{Schema: "river"}, workers, nil, logger)

			require.NotNil(t, rc.Logger)
			require.Len(t, rc.Middleware, 1)
			assert.IsType(t, &requestIDMiddleware{}, rc.Middleware[0])
		})
	}
}

// River validates Config in NewClient; this proves it accepts the middleware
// and logger without needing a database. Worker mode cannot be built without
// a pool, but buildRiverConfig sets the same two fields in both modes (see
// TestBuildRiverConfig_InstallsRequestIDMiddlewareAndLogger).
func TestBuildRiverConfig_AcceptedByRiverClient(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	_, err := river.NewClient(riverpgxv5.New(nil), buildRiverConfig(Config{Schema: "river"}, nil, nil, logger))
	require.NoError(t, err)
}

// River logs a lot at info and debug. Through the app's handler it keeps the
// app's format but only from warn up, the level River's own default logger
// used, so wiring the logger in does not make a worker noisier.
func TestBuildRiverConfig_RiverLoggerStartsAtWarn(t *testing.T) {
	var buf bytes.Buffer
	appLogger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	rc := buildRiverConfig(Config{Schema: "river"}, nil, nil, appLogger)
	rc.Logger.Debug("river debug")
	rc.Logger.Info("river info")
	rc.Logger.Warn("river warn")
	rc.Logger.Error("river error")

	out := buf.String()
	assert.NotContains(t, out, "river debug")
	assert.NotContains(t, out, "river info")
	assert.Contains(t, out, "river warn")
	assert.Contains(t, out, "river error")
}

// An app logger set above warn keeps its own, higher floor.
func TestBuildRiverConfig_RiverLoggerKeepsHigherAppLevel(t *testing.T) {
	var buf bytes.Buffer
	appLogger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))

	rc := buildRiverConfig(Config{Schema: "river"}, nil, nil, appLogger)
	rc.Logger.Warn("river warn")

	assert.Empty(t, buf.String())
}

// With no logger River keeps its own default.
func TestBuildRiverConfig_NilLogger_LeavesRiverDefault(t *testing.T) {
	rc := buildRiverConfig(Config{Schema: "river"}, nil, nil, nil)
	assert.Nil(t, rc.Logger)
}

// A request ID the caller put in InsertOpts.Metadata wins over the one in
// the context, as an explicit value does in logger.
func TestRequestIDMiddleware_Insert_ExplicitMetadataWins(t *testing.T) {
	ctx := reqctx.WithRequestID(context.Background(), "from-ctx")
	p := &rivertype.JobInsertParams{Metadata: []byte(`{"vogel_request_id":"explicit"}`)}

	insert(t, ctx, p)

	assert.Equal(t, "explicit", metadataOf(t, p)[requestIDMetadataKey])
}
