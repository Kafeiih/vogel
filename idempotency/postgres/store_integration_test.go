//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/kafeiih/vogel/auth"
	"github.com/kafeiih/vogel/idempotency"
	idemhttp "github.com/kafeiih/vogel/idempotency/httpx"
	"github.com/kafeiih/vogel/idempotency/migrations"
	idempg "github.com/kafeiih/vogel/idempotency/postgres"
	"github.com/kafeiih/vogel/migrate"
)

func startTestPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	pgContainer, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("test_db"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	require.NoError(t, err, "start postgres container")
	t.Cleanup(func() { _ = pgContainer.Terminate(context.Background()) })
	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	return connStr
}

func silentOptions() migrate.Options {
	return migrate.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), TableName: migrations.DefaultTableName}
}

// startTestPool migrates idempotency_key plus a side table the tests mutate
// inside the same transaction as the key, standing for a service's write.
func startTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	connStr := startTestPostgres(t)
	require.NoError(t, migrate.Up(ctx, connStr, migrations.FS(), silentOptions()))
	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `CREATE TABLE side_effect (id SERIAL PRIMARY KEY, note TEXT NOT NULL)`)
	require.NoError(t, err)
	return pool
}

func request(key string) idempotency.Request {
	return idempotency.Request{Scope: "user-1 POST /payments", Key: key, Fingerprint: idempotency.Fingerprint([]byte(`{"amount":100}`))}
}

func sideEffects(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM side_effect`).Scan(&n))
	return n
}

// mutation inserts one side_effect row in tx and answers 201 with body.
func mutation(tx pgx.Tx, calls *atomic.Int32, body string) func(context.Context) (idempotency.Response, error) {
	return func(ctx context.Context) (idempotency.Response, error) {
		calls.Add(1)
		if _, err := tx.Exec(ctx, `INSERT INTO side_effect (note) VALUES ($1)`, body); err != nil {
			return idempotency.Response{}, err
		}
		return idempotency.Response{Status: 201, Header: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(body)}, nil
	}
}

// doInTx runs store.Do in its own transaction, committing unless fn (or Do)
// fails.
func doInTx(t *testing.T, pool *pgxpool.Pool, store *idempg.Store, req idempotency.Request, calls *atomic.Int32, body string) (idempotency.Response, bool, error) {
	t.Helper()
	var (
		resp     idempotency.Response
		replayed bool
	)
	err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
		var err error
		resp, replayed, err = store.Do(context.Background(), tx, req, mutation(tx, calls, body))
		return err
	})
	return resp, replayed, err
}

func TestDo_RunsOnceAndReplaysTheStoredResponse(t *testing.T) {
	pool := startTestPool(t)
	store := idempg.NewStore()
	var calls atomic.Int32

	first, replayed, err := doInTx(t, pool, store, request("k-1"), &calls, `{"id":1}`)
	require.NoError(t, err)
	assert.False(t, replayed)
	assert.Equal(t, 201, first.Status)

	again, replayed, err := doInTx(t, pool, store, request("k-1"), &calls, `{"id":2}`)
	require.NoError(t, err)
	assert.True(t, replayed, "a retry replays")
	assert.Equal(t, first, again, "the retry gets the first response, status, header and body")
	assert.Equal(t, int32(1), calls.Load(), "the mutation ran once")
	assert.Equal(t, 1, sideEffects(t, pool))
}

func TestDo_RollbackReleasesTheKey(t *testing.T) {
	pool := startTestPool(t)
	store := idempg.NewStore()
	var calls atomic.Int32
	boom := errors.New("validation failed")

	err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
		_, _, err := store.Do(context.Background(), tx, request("k-1"), func(ctx context.Context) (idempotency.Response, error) {
			calls.Add(1)
			return idempotency.Response{}, boom
		})
		return err
	})
	require.ErrorIs(t, err, boom, "the mutation's error is returned as is")

	_, replayed, err := doInTx(t, pool, store, request("k-1"), &calls, `{"id":1}`)
	require.NoError(t, err)
	assert.False(t, replayed, "a failed attempt stored nothing, so the retry runs")
	assert.Equal(t, int32(2), calls.Load())
	assert.Equal(t, 1, sideEffects(t, pool))
}

func TestDo_RejectsAKeyReusedWithAnotherRequest(t *testing.T) {
	pool := startTestPool(t)
	store := idempg.NewStore()
	var calls atomic.Int32
	_, _, err := doInTx(t, pool, store, request("k-1"), &calls, `{"id":1}`)
	require.NoError(t, err)

	other := request("k-1")
	other.Fingerprint = idempotency.Fingerprint([]byte(`{"amount":999}`))
	_, _, err = doInTx(t, pool, store, other, &calls, `{"id":2}`)
	require.ErrorIs(t, err, idempotency.ErrKeyReused)
	assert.Equal(t, int32(1), calls.Load())
	assert.Equal(t, 1, sideEffects(t, pool))
}

func TestDo_ScopesKeepKeysApart(t *testing.T) {
	pool := startTestPool(t)
	store := idempg.NewStore()
	var calls atomic.Int32
	_, _, err := doInTx(t, pool, store, request("k-1"), &calls, `{"id":1}`)
	require.NoError(t, err)

	otherUser := request("k-1")
	otherUser.Scope = "user-2 POST /payments"
	_, replayed, err := doInTx(t, pool, store, otherUser, &calls, `{"id":2}`)
	require.NoError(t, err)
	assert.False(t, replayed, "the same key from another caller is another key")
	assert.Equal(t, 2, sideEffects(t, pool))
}

func TestDo_ExpiredKeyRunsAgain(t *testing.T) {
	pool := startTestPool(t)
	store := idempg.NewStore(idempg.WithTTL(time.Hour))
	var calls atomic.Int32
	_, _, err := doInTx(t, pool, store, request("k-1"), &calls, `{"id":1}`)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `UPDATE idempotency_key SET expires_at = now() - interval '1 second'`)
	require.NoError(t, err)

	_, replayed, err := doInTx(t, pool, store, request("k-1"), &calls, `{"id":2}`)
	require.NoError(t, err)
	assert.False(t, replayed, "an expired key counts as new")
	assert.Equal(t, 2, sideEffects(t, pool))

	var expires time.Time
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT expires_at FROM idempotency_key`).Scan(&expires))
	assert.WithinDuration(t, time.Now().Add(time.Hour), expires, time.Minute, "the reclaimed key gets a fresh TTL")
}

func TestDo_ConcurrentDuplicateWaitsForTheFirst(t *testing.T) {
	for _, outcome := range []string{"commit", "rollback"} {
		t.Run(outcome, func(t *testing.T) {
			pool := startTestPool(t)
			store := idempg.NewStore()
			ctx := context.Background()
			var calls atomic.Int32

			tx1, err := pool.Begin(ctx)
			require.NoError(t, err)
			// Registered after the pool's Close, so it runs first: a failing
			// assertion must not leave tx1 holding a connection Close waits on.
			t.Cleanup(func() { _ = tx1.Rollback(context.Background()) })
			_, _, err = store.Do(ctx, tx1, request("k-1"), mutation(tx1, &calls, `{"id":1}`))
			require.NoError(t, err)

			type result struct {
				resp     idempotency.Response
				replayed bool
				err      error
			}
			done := make(chan result, 1)
			go func() {
				resp, replayed, err := doInTx(t, pool, store, request("k-1"), &calls, `{"id":2}`)
				done <- result{resp, replayed, err}
			}()
			select {
			case r := <-done:
				t.Fatalf("the duplicate returned before the first transaction ended: %+v", r)
			case <-time.After(300 * time.Millisecond):
			}

			if outcome == "commit" {
				require.NoError(t, tx1.Commit(ctx))
			} else {
				require.NoError(t, tx1.Rollback(ctx))
			}
			r := <-done
			require.NoError(t, r.err)
			if outcome == "commit" {
				assert.True(t, r.replayed, "after a commit the duplicate replays")
				assert.Equal(t, []byte(`{"id":1}`), r.resp.Body)
				assert.Equal(t, int32(1), calls.Load())
			} else {
				assert.False(t, r.replayed, "after a rollback the duplicate runs")
				assert.Equal(t, []byte(`{"id":2}`), r.resp.Body)
				assert.Equal(t, int32(2), calls.Load())
			}
			assert.Equal(t, 1, sideEffects(t, pool), "exactly one mutation committed")
		})
	}
}

func TestDo_InvalidRequestTouchesNothing(t *testing.T) {
	pool := startTestPool(t)
	store := idempg.NewStore()
	var calls atomic.Int32
	bad := request("")
	_, _, err := doInTx(t, pool, store, bad, &calls, `{}`)
	require.ErrorIs(t, err, idempotency.ErrInvalidRequest)
	assert.Zero(t, calls.Load())
}

func TestDeleteExpired_RemovesOnlyExpiredRowsInBatches(t *testing.T) {
	pool := startTestPool(t)
	store := idempg.NewStore()
	ctx := context.Background()
	var calls atomic.Int32
	for _, k := range []string{"a", "b", "c", "live"} {
		_, _, err := doInTx(t, pool, store, request(k), &calls, `{}`)
		require.NoError(t, err)
	}
	_, err := pool.Exec(ctx, `UPDATE idempotency_key SET expires_at = now() - interval '1 second' WHERE key <> 'live'`)
	require.NoError(t, err)

	n, err := store.DeleteExpired(ctx, pool, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n, "one call deletes at most limit rows")
	n, err = store.DeleteExpired(ctx, pool, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	n, err = store.DeleteExpired(ctx, pool, 2)
	require.NoError(t, err)
	assert.Zero(t, n)

	var left []string
	rows, err := pool.Query(ctx, `SELECT key FROM idempotency_key`)
	require.NoError(t, err)
	left, err = pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	assert.Equal(t, []string{"live"}, left)
}

func TestMigration001_UpDownRoundTrip(t *testing.T) {
	ctx := context.Background()
	connStr := startTestPostgres(t)
	require.NoError(t, migrate.Up(ctx, connStr, migrations.FS(), silentOptions()))
	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	table := func() int {
		var n int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_name = 'idempotency_key'`).Scan(&n))
		return n
	}
	assert.Equal(t, 1, table())
	require.NoError(t, migrate.Down(ctx, connStr, migrations.FS(), silentOptions()))
	assert.Zero(t, table())
	require.NoError(t, migrate.Up(ctx, connStr, migrations.FS(), silentOptions()))
	assert.Equal(t, 1, table())
}

func TestReserve_CompleteThenReplay(t *testing.T) {
	pool := startTestPool(t)
	store := idempg.NewStore()
	ctx := context.Background()

	res, err := store.Reserve(ctx, pool, request("k-1"), time.Minute)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, res.Token, "a new key is reserved")
	assert.Nil(t, res.Replay)

	_, err = store.Reserve(ctx, pool, request("k-1"), time.Minute)
	require.ErrorIs(t, err, idempotency.ErrInProgress, "a live lease blocks a duplicate")

	want := idempotency.Response{Status: 201, Header: map[string][]string{"Location": {"/payments/1"}}, Body: []byte(`{"id":1}`)}
	require.NoError(t, store.Complete(ctx, pool, request("k-1"), res.Token, want))

	again, err := store.Reserve(ctx, pool, request("k-1"), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, again.Replay)
	assert.Equal(t, uuid.Nil, again.Token)
	assert.Equal(t, want, *again.Replay)

	other := request("k-1")
	other.Fingerprint = idempotency.Fingerprint([]byte("other"))
	_, err = store.Reserve(ctx, pool, other, time.Minute)
	require.ErrorIs(t, err, idempotency.ErrKeyReused)
}

func TestReserve_ReleaseLetsARetryRun(t *testing.T) {
	pool := startTestPool(t)
	store := idempg.NewStore()
	ctx := context.Background()
	res, err := store.Reserve(ctx, pool, request("k-1"), time.Minute)
	require.NoError(t, err)
	require.NoError(t, store.Release(ctx, pool, request("k-1"), res.Token))

	retry, err := store.Reserve(ctx, pool, request("k-1"), time.Minute)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, retry.Token)
}

func TestReserve_LapsedLeaseIsTakenOverAndTheOldHolderLosesIt(t *testing.T) {
	pool := startTestPool(t)
	store := idempg.NewStore()
	ctx := context.Background()
	slow, err := store.Reserve(ctx, pool, request("k-1"), time.Minute)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE idempotency_key SET locked_until = now() - interval '1 second'`)
	require.NoError(t, err)

	other := request("k-1")
	other.Fingerprint = idempotency.Fingerprint([]byte("other"))
	_, err = store.Reserve(ctx, pool, other, time.Minute)
	require.ErrorIs(t, err, idempotency.ErrKeyReused, "a lapsed lease is only taken over by the same request")

	taker, err := store.Reserve(ctx, pool, request("k-1"), time.Minute)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, taker.Token)
	assert.NotEqual(t, slow.Token, taker.Token)

	require.Error(t, store.Complete(ctx, pool, request("k-1"), slow.Token, idempotency.Response{Status: 200}),
		"the slow request can no longer store a response")
	require.NoError(t, store.Release(ctx, pool, request("k-1"), slow.Token))
	_, err = store.Reserve(ctx, pool, request("k-1"), time.Minute)
	require.ErrorIs(t, err, idempotency.ErrInProgress, "the slow request's Release left the new holder's reservation alone")

	require.NoError(t, store.Complete(ctx, pool, request("k-1"), taker.Token, idempotency.Response{Status: 200}))
}

func TestReserve_RejectsANonPositiveLease(t *testing.T) {
	pool := startTestPool(t)
	_, err := idempg.NewStore().Reserve(context.Background(), pool, request("k-1"), 0)
	require.ErrorIs(t, err, idempotency.ErrInvalidRequest)
}

func TestMiddleware_EndToEndWithPostgres(t *testing.T) {
	pool := startTestPool(t)
	store := idempg.NewStore()
	var calls atomic.Int32
	release := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-Block") != "" {
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	})
	srv := idemhttp.Middleware(idemhttp.Config{Store: store, DB: pool, Logger: slog.New(slog.DiscardHandler)})(h)
	send := func(key string, block bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/payments", strings.NewReader(`{"amount":100}`))
		r.Header.Set(idemhttp.HeaderKey, key)
		if block {
			r.Header.Set("X-Block", "1")
		}
		r = r.WithContext(auth.WithPrincipal(r.Context(), &auth.Principal{UserID: "user-1"}))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		return rec
	}

	first := send("k-1", false)
	assert.Equal(t, http.StatusCreated, first.Code)
	retry := send("k-1", false)
	assert.Equal(t, http.StatusCreated, retry.Code)
	assert.Equal(t, first.Body.String(), retry.Body.String())
	assert.Equal(t, "true", retry.Header().Get(idemhttp.HeaderReplayed))
	assert.Equal(t, int32(1), calls.Load(), "the retry was replayed, not run")

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- send("k-2", true) }()
	require.Eventually(t, func() bool { return calls.Load() == 2 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, http.StatusConflict, send("k-2", false).Code, "a duplicate while the first runs is rejected")
	close(release)
	assert.Equal(t, http.StatusCreated, (<-done).Code)
	assert.Equal(t, http.StatusCreated, send("k-2", false).Code)
	assert.Equal(t, int32(2), calls.Load(), "k-2 ran once")
}
