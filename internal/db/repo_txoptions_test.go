// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// The zero value must remain the SAFE choice: read-write SERIALIZABLE, exactly
// what every transaction did before TxOptions existed. If the zero value ever
// became READ COMMITTED, every caller that never thought about isolation would
// silently lose serializability — which is why the enum is ordered so that
// "default" and "serializable" are the same thing.
func TestTxOptions_ZeroValueIsSerializableReadWrite(t *testing.T) {
	var opts TxOptions

	if opts.ReadOnly {
		t.Fatal("zero value must be read-write")
	}
	if got := opts.Isolation.sqlLevel(); got != sql.LevelSerializable {
		t.Fatalf("zero value must be SERIALIZABLE, got %v", got)
	}
	if IsolationDefault.sqlLevel() != IsolationSerializable.sqlLevel() {
		t.Fatal("IsolationDefault and IsolationSerializable must be the same level")
	}
}

// What the database actually applied, not what we asked for.
func txSettings(t *testing.T, ctx context.Context) (isolation string, readOnly string) {
	t.Helper()
	repo := repoFromCtx(t, ctx)
	if err := repo.DB.DB(ctx).QueryRowContext(ctx,
		"SELECT current_setting('transaction_isolation'), current_setting('transaction_read_only')",
	).Scan(&isolation, &readOnly); err != nil {
		t.Fatalf("read transaction settings: %v", err)
	}
	return isolation, readOnly
}

// ctxRepo carries the repo through to the helper above without a global.
type ctxRepoKey struct{}

func repoFromCtx(t *testing.T, ctx context.Context) *Repo {
	t.Helper()
	repo, ok := ctx.Value(ctxRepoKey{}).(*Repo)
	if !ok {
		t.Fatal("repo missing from context")
	}
	return repo
}

func TestRunTx_AppliesRequestedIsolationAndMode(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	tests := []struct {
		name          string
		opts          TxOptions
		wantIsolation string
		wantReadOnly  string
	}{
		{"default", TxOptions{}, "serializable", "off"},
		{"explicit serializable", TxOptions{Isolation: IsolationSerializable}, "serializable", "off"},
		{"read committed", TxOptions{Isolation: IsolationReadCommitted}, "read committed", "off"},
		{"read only", TxOptions{ReadOnly: true}, "serializable", "on"},
		{"read only + read committed", TxOptions{ReadOnly: true, Isolation: IsolationReadCommitted}, "read committed", "on"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := context.WithValue(context.Background(), ctxRepoKey{}, repo)
			err := repo.RunTxWithOptions(base, tc.opts, func(ctx context.Context) error {
				iso, ro := txSettings(t, context.WithValue(ctx, ctxRepoKey{}, repo))
				if iso != tc.wantIsolation {
					t.Errorf("isolation = %q, want %q", iso, tc.wantIsolation)
				}
				if ro != tc.wantReadOnly {
					t.Errorf("read_only = %q, want %q", ro, tc.wantReadOnly)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("RunTxWithOptions: %v", err)
			}
		})
	}
}

// RunTxReadOnly is sugar and must actually be read-only — the whole point is
// that it drops out of the serialization graph.
func TestRunTxReadOnly_IsReadOnly(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	base := context.WithValue(context.Background(), ctxRepoKey{}, repo)
	err := repo.RunTxReadOnly(base, func(ctx context.Context) error {
		_, ro := txSettings(t, context.WithValue(ctx, ctxRepoKey{}, repo))
		if ro != "on" {
			t.Errorf("RunTxReadOnly must be read-only, got %q", ro)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunTxReadOnly: %v", err)
	}
}

// A write inside a read-only transaction must FAIL LOUDLY. This is what makes
// ReadOnly safe to apply broadly: mislabelling a path that writes produces an
// immediate, obvious error rather than silent data loss.
func TestRunTxReadOnly_RejectsWrites(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-readonly-write"
	createActivityTestChat(t, repo, chatID)

	err := repo.RunTxReadOnly(context.Background(), func(ctx context.Context) error {
		_, execErr := repo.DB.DB(ctx).ExecContext(ctx,
			"UPDATE chats SET title = 'nope' WHERE id = $1", chatID)
		return execErr
	})

	if err == nil {
		t.Fatal("a write inside a read-only transaction must fail")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "read-only") {
		t.Fatalf("expected a read-only transaction error, got: %v", err)
	}
}

// The payoff, asserted against a real database: a SERIALIZABLE READ ONLY
// DEFERRABLE transaction takes NO predicate locks. Those locks are what make
// two transactions on disjoint rows abort each other with 40001, so a read path
// that holds none can neither suffer nor cause a serialization failure.
//
// Compared against a read-write transaction running the same query, so the test
// measures the MODE rather than the query.
func TestReadOnlyTransactionTakesNoPredicateLocks(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-predlocks"
	createActivityTestChat(t, repo, chatID)
	for i := 0; i < 50; i++ {
		if _, err := repo.SaveMessageToThread(context.Background(), chatID, chatID, 1, "m", nil, nil, nil); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	countLocks := func(opts TxOptions) int {
		var locks int
		base := context.WithValue(context.Background(), ctxRepoKey{}, repo)
		err := repo.RunTxWithOptions(base, opts, func(ctx context.Context) error {
			// Force a scan that would take predicate locks under SERIALIZABLE.
			var n int
			if err := repo.DB.DB(ctx).QueryRowContext(ctx,
				"SELECT count(*) FROM messages WHERE chat_id = $1", chatID).Scan(&n); err != nil {
				return err
			}
			return repo.DB.DB(ctx).QueryRowContext(ctx,
				"SELECT count(*) FROM pg_locks WHERE mode = 'SIReadLock' AND pid = pg_backend_pid()",
			).Scan(&locks)
		})
		if err != nil {
			t.Fatalf("RunTxWithOptions(%+v): %v", opts, err)
		}
		return locks
	}

	readWrite := countLocks(TxOptions{})
	readOnly := countLocks(TxOptions{ReadOnly: true})

	t.Logf("SIReadLocks held — read-write: %d, read-only deferrable: %d", readWrite, readOnly)

	if readOnly != 0 {
		t.Fatalf("a READ ONLY DEFERRABLE transaction must take zero predicate locks, got %d", readOnly)
	}
	if readWrite == 0 {
		t.Skip("read-write transaction took no predicate locks either — the query was " +
			"served in a way that avoids them, so this comparison proves nothing")
	}
}
