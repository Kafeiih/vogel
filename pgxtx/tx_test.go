package pgxtx

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestTxFromContext_WithoutTx(t *testing.T) {
	ctx := context.Background()

	tx := TxFromContext(ctx)
	if tx != nil {
		t.Error("TxFromContext: expected nil when no tx in context")
	}
}

func TestTxFromContext_WithTx(t *testing.T) {
	// Simulate a tx stored in context using the same ctxKey
	fakeTx := mockTx{}
	ctx := context.WithValue(context.Background(), ctxKey{}, pgx.Tx(fakeTx))

	tx := TxFromContext(ctx)
	if tx == nil {
		t.Fatal("TxFromContext: expected non-nil tx from context")
	}
}

// mockTx is a minimal stub to satisfy pgx.Tx interface for context storage.
// We only need it to be stored/retrieved — no methods are called.
type mockTx struct {
	pgx.Tx
}

// fakeTx is a pgx.Tx stub for WithTx tests: it records whether Commit or
// Rollback was called, without needing a real database connection.
type fakeTx struct {
	pgx.Tx
	committed  bool
	rolledBack bool
}

func (f *fakeTx) Commit(ctx context.Context) error {
	f.committed = true
	return nil
}

func (f *fakeTx) Rollback(ctx context.Context) error {
	f.rolledBack = true
	return nil
}

// fakeBeginner is a txBeginner stub that always returns the same *fakeTx.
type fakeBeginner struct {
	tx *fakeTx
}

func (b fakeBeginner) Begin(ctx context.Context) (pgx.Tx, error) {
	return b.tx, nil
}

func TestPgxTxManager_WithTx_Success_Commits(t *testing.T) {
	tx := &fakeTx{}
	m := &PgxTxManager{pool: fakeBeginner{tx: tx}}

	err := m.WithTx(context.Background(), func(ctx context.Context) error {
		if TxFromContext(ctx) == nil {
			t.Error("expected tx to be present in callback context")
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !tx.committed {
		t.Error("expected transaction to be committed")
	}
	if tx.rolledBack {
		t.Error("expected transaction NOT to be rolled back")
	}
}

func TestPgxTxManager_WithTx_CallbackError_RollsBack(t *testing.T) {
	tx := &fakeTx{}
	m := &PgxTxManager{pool: fakeBeginner{tx: tx}}
	wantErr := errors.New("boom")

	err := m.WithTx(context.Background(), func(ctx context.Context) error {
		return wantErr
	})

	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if !tx.rolledBack {
		t.Error("expected transaction to be rolled back")
	}
	if tx.committed {
		t.Error("expected transaction NOT to be committed")
	}
}

// TestPgxTxManager_WithTx_PanicInCallback_RollsBackAndPropagates is the
// regression test for the panic-safety fix: a panic inside the callback must
// not leak an open transaction, and must still propagate to the caller.
func TestPgxTxManager_WithTx_PanicInCallback_RollsBackAndPropagates(t *testing.T) {
	tx := &fakeTx{}
	m := &PgxTxManager{pool: fakeBeginner{tx: tx}}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic to propagate out of WithTx")
		}
		if r != "boom" {
			t.Errorf("panic value = %v, want %q", r, "boom")
		}
		if !tx.rolledBack {
			t.Error("expected transaction to be rolled back after panic")
		}
		if tx.committed {
			t.Error("expected transaction NOT to be committed after panic")
		}
	}()

	_ = m.WithTx(context.Background(), func(ctx context.Context) error {
		panic("boom")
	})

	t.Fatal("expected panic to unwind this test before reaching here")
}
