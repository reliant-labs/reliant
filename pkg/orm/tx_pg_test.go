package orm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// These tests pin the context-carried transaction (RunTx) against a REAL
// postgres: isolation, retry and visibility are properties of the database,
// and a fake would only restate what the code already assumes.

type txNote struct {
	bun.BaseModel `bun:"table:tx_notes,alias:tx_notes"`

	ID   string `bun:"id,pk"`
	Body string `bun:"body,notnull"`
}

func newTxTestClient(t *testing.T) *Client {
	t.Helper()
	if testing.Short() {
		t.Skip("RunTx tests boot embedded postgres; skipped under -short")
	}
	sqldb, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	client, err := NewClientWithDB(sqldb, "postgres")
	if err != nil {
		t.Fatalf("NewClientWithDB: %v", err)
	}
	if _, err := client.Exec(context.Background(),
		`CREATE TABLE tx_notes (id TEXT PRIMARY KEY, body TEXT NOT NULL)`); err != nil {
		t.Fatalf("create tx_notes: %v", err)
	}
	return client
}

// countNotes counts committed rows as an OUTSIDE observer: a fresh
// background context, so it reads through the pool and sees only what has
// been committed.
func countNotes(t *testing.T, c *Client) int64 {
	t.Helper()
	n, err := c.Bun().NewSelect().Model((*txNote)(nil)).Count(context.Background())
	if err != nil {
		t.Fatalf("count notes: %v", err)
	}
	return n
}

func insertNote(ctx context.Context, c *Client, id string) error {
	_, err := c.Bun().NewInsert().Model(&txNote{ID: id, Body: "b"}).Exec(ctx)
	return err
}

// Every execution path off the plain *Client — bun builders, bun's own
// ExecContext/QueryRowContext, and the raw Exec/QueryRow escape hatch — must
// run inside the transaction the ctx carries. The outside observer proves it:
// nothing is visible until commit.
func TestRunTx_EveryClientPathJoinsTheCtxTransaction(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()

	err := c.RunTx(ctx, Serializable, func(ctx context.Context) error {
		if err := insertNote(ctx, c, "builder"); err != nil {
			return fmt.Errorf("builder insert: %w", err)
		}
		if _, err := c.Bun().ExecContext(ctx,
			"INSERT INTO tx_notes (id, body) VALUES (?, ?)", "bun-exec", "b"); err != nil {
			return fmt.Errorf("bun ExecContext: %w", err)
		}
		if _, err := c.Exec(ctx,
			"INSERT INTO tx_notes (id, body) VALUES ($1, $2)", "raw-exec", "b"); err != nil {
			return fmt.Errorf("raw Exec: %w", err)
		}

		// Inside, all three writes are visible through every read path.
		builderCount, err := c.Bun().NewSelect().Model((*txNote)(nil)).Count(ctx)
		if err != nil {
			return err
		}
		var bunCount, rawCount int
		if err := c.Bun().QueryRowContext(ctx, "SELECT count(*) FROM tx_notes").Scan(&bunCount); err != nil {
			return err
		}
		if err := c.QueryRow(ctx, "SELECT count(*) FROM tx_notes").Scan(&rawCount); err != nil {
			return err
		}
		if builderCount != 3 || bunCount != 3 || rawCount != 3 {
			t.Errorf("inside RunTx: builder=%d bun=%d raw=%d, want 3 each", builderCount, bunCount, rawCount)
		}

		// Outside, none of them are: they are uncommitted writes of THIS
		// transaction, not autocommitted statements on the pool.
		if got := countNotes(t, c); got != 0 {
			t.Errorf("outside observer saw %d rows before commit, want 0", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunTx: %v", err)
	}
	if got := countNotes(t, c); got != 3 {
		t.Errorf("after commit: %d rows, want 3", got)
	}
}

func TestRunTx_RollsBackOnErrorAndOnPanic(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()

	sentinel := errors.New("business rule failed")
	err := c.RunTx(ctx, Serializable, func(ctx context.Context) error {
		if err := insertNote(ctx, c, "a"); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("RunTx must return fn's error verbatim, got %v", err)
	}
	if got := countNotes(t, c); got != 0 {
		t.Fatalf("error path left %d rows, want 0", got)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("a panic in fn must propagate out of RunTx")
			}
		}()
		_ = c.RunTx(ctx, Serializable, func(ctx context.Context) error {
			if err := insertNote(ctx, c, "b"); err != nil {
				return err
			}
			panic("boom")
		})
	}()
	if got := countNotes(t, c); got != 0 {
		t.Fatalf("panic path left %d rows, want 0", got)
	}
}

func showSetting(ctx context.Context, t *testing.T, c *Client, name string) string {
	t.Helper()
	var v string
	if err := c.QueryRow(ctx, "SHOW "+name).Scan(&v); err != nil {
		t.Fatalf("SHOW %s: %v", name, err)
	}
	return v
}

func TestRunTx_IsolationAndReadOnlyModes(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()

	cases := []struct {
		name       string
		run        func(func(context.Context) error) error
		isolation  string
		readOnly   string
		deferrable string
	}{
		{"Serializable is serializable", func(fn func(context.Context) error) error {
			return c.RunTx(ctx, Serializable, fn)
		}, "serializable", "off", "off"},
		{"RunTxReadOnly is serializable read only deferrable", func(fn func(context.Context) error) error {
			return c.RunTxReadOnly(ctx, fn)
		}, "serializable", "on", "on"},
		{"read committed with a retry budget", func(fn func(context.Context) error) error {
			return c.RunTx(ctx, ReadCommitted.WithMaxAttempts(DefaultTxMaxAttempts), fn)
		}, "read committed", "off", "off"},
		{"repeatable read", func(fn func(context.Context) error) error {
			return c.RunTx(ctx, RepeatableRead, fn)
		}, "repeatable read", "off", "off"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(func(ctx context.Context) error {
				if got := showSetting(ctx, t, c, "transaction_isolation"); got != tc.isolation {
					t.Errorf("transaction_isolation = %q, want %q", got, tc.isolation)
				}
				if got := showSetting(ctx, t, c, "transaction_read_only"); got != tc.readOnly {
					t.Errorf("transaction_read_only = %q, want %q", got, tc.readOnly)
				}
				if got := showSetting(ctx, t, c, "transaction_deferrable"); got != tc.deferrable {
					t.Errorf("transaction_deferrable = %q, want %q", got, tc.deferrable)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("run: %v", err)
			}
		})
	}

	// A write inside a read-only transaction is rejected by postgres
	// (25006 read_only_sql_transaction) — and not retried.
	attempts := 0
	err := c.RunTxReadOnly(ctx, func(ctx context.Context) error {
		attempts++
		return insertNote(ctx, c, "nope")
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "25006" {
		t.Fatalf("write in RunTxReadOnly: got %v, want SQLSTATE 25006", err)
	}
	if attempts != 1 {
		t.Errorf("read-only violation ran fn %d times, want 1 (not retryable)", attempts)
	}
}

func TestRunTx_RetriesSerializationFailureAndDeadlock(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()

	for _, code := range []pq.ErrorCode{"40001", "40P01"} {
		t.Run(string(code), func(t *testing.T) {
			attempts := 0
			err := c.RunTx(ctx, Serializable, func(ctx context.Context) error {
				attempts++
				if err := insertNote(ctx, c, fmt.Sprintf("%s-%d", code, attempts)); err != nil {
					return err
				}
				if attempts == 1 {
					// Wrapped, as a service would: classification must see
					// through %w.
					return fmt.Errorf("load thing: %w", &pq.Error{Code: code})
				}
				return nil
			})
			if err != nil {
				t.Fatalf("RunTx: %v", err)
			}
			if attempts != 2 {
				t.Errorf("fn ran %d times, want 2 (one retry)", attempts)
			}
			// The failed attempt's insert was rolled back with it; only the
			// retry's row survives.
			var ids []string
			if err := c.Bun().NewSelect().Model((*txNote)(nil)).Column("id").
				Where("id LIKE ?", string(code)+"-%").Scan(ctx, &ids); err != nil {
				t.Fatal(err)
			}
			if len(ids) != 1 || ids[0] != fmt.Sprintf("%s-2", code) {
				t.Errorf("surviving rows = %v, want only the retry's %s-2", ids, code)
			}
		})
	}

	t.Run("exhausted budget is Aborted and keeps the driver error", func(t *testing.T) {
		attempts := 0
		err := c.RunTx(ctx, Serializable.WithMaxAttempts(3), func(ctx context.Context) error {
			attempts++
			return &pq.Error{Code: "40001", Message: "could not serialize access"}
		})
		if attempts != 3 {
			t.Errorf("fn ran %d times, want MaxAttempts=3", attempts)
		}
		if !svcerr.IsAborted(err) {
			t.Errorf("exhausted retries must classify as svcerr Aborted, got %v", err)
		}
		var pqErr *pq.Error
		if !errors.As(err, &pqErr) || pqErr.Code != "40001" {
			t.Errorf("driver error must stay reachable via errors.As, got %v", err)
		}
		if msg := svcerr.ToConnect(err).Message(); msg == "" || strings.Contains(msg, "serialize") {
			t.Errorf("client message must be the safe summary, not driver prose: %q", msg)
		}
	})

	t.Run("non-retryable errors run once", func(t *testing.T) {
		attempts := 0
		err := c.RunTx(ctx, Serializable, func(ctx context.Context) error {
			attempts++
			return &pq.Error{Code: "23505"} // unique_violation is an answer, not a conflict
		})
		if attempts != 1 || err == nil {
			t.Errorf("unique violation: attempts=%d err=%v, want 1 attempt and the error", attempts, err)
		}
	})

	t.Run("cancelled ctx stops the retry loop", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		attempts := 0
		err := c.RunTx(cctx, Serializable, func(ctx context.Context) error {
			attempts++
			cancel()
			return &pq.Error{Code: "40001"}
		})
		if !errors.Is(err, context.Canceled) || attempts != 1 {
			t.Errorf("cancelled: attempts=%d err=%v, want 1 attempt and context.Canceled", attempts, err)
		}
	})
}

// A RunTx inside a RunTx on the same database JOINS: the inner call neither
// commits nor rolls back, so the outer transaction's fate decides the inner
// write too. The inner call's options are ignored.
func TestRunTx_JoinsAnAmbientTransaction(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()
	rollback := errors.New("outer rolls back")

	err := c.RunTx(ctx, Serializable, func(ctx context.Context) error {
		if err := c.RunTx(ctx, Serializable, func(ctx context.Context) error {
			return insertNote(ctx, c, "inner-runtx")
		}); err != nil {
			return err
		}
		// A read-only request joining a read-write transaction just joins:
		// the write below succeeds because the ambient transaction is RW.
		if err := c.RunTxReadOnly(ctx, func(ctx context.Context) error {
			return insertNote(ctx, c, "inner-readonly")
		}); err != nil {
			return fmt.Errorf("joined read-only call: %w", err)
		}
		// The legacy handle API joins too, and its handle is the same tx.
		if err := c.RunTransaction(ctx, ReadCommitted, func(tx Context) error {
			_, err := tx.Exec(ctx, "INSERT INTO tx_notes (id, body) VALUES ($1, 'b')", "inner-runtransaction")
			return err
		}); err != nil {
			return err
		}
		if got := countNotes(t, c); got != 0 {
			t.Errorf("a joined call committed on its own: outside observer sees %d rows", got)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("RunTx: %v", err)
	}
	if got := countNotes(t, c); got != 0 {
		t.Fatalf("outer rollback must undo every joined write; %d rows survived", got)
	}
}

// A handle from the legacy RunTransaction can call into code written with
// RunTx: handle.Join gives fn a ctx carrying the handle's transaction, so
// queries made with the plain client join it.
func TestTxHandle_RunTxCarriesTheHandlesTransaction(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()

	err := c.RunTransaction(ctx, ReadCommitted, func(tx Context) error {
		if _, err := tx.Exec(ctx, "INSERT INTO tx_notes (id, body) VALUES ('via-handle', 'b')"); err != nil {
			return err
		}
		return tx.RunTx(ctx, ReadCommitted, func(ctx context.Context) error {
			// The plain client, with the derived ctx, sees the handle's
			// uncommitted row.
			n, err := c.Bun().NewSelect().Model((*txNote)(nil)).Count(ctx)
			if err != nil {
				return err
			}
			if n != 1 {
				t.Errorf("client query inside handle.Join saw %d rows, want 1", n)
			}
			return insertNote(ctx, c, "via-client")
		})
	})
	if err != nil {
		t.Fatalf("RunTransaction: %v", err)
	}
	if got := countNotes(t, c); got != 2 {
		t.Fatalf("after commit: %d rows, want 2", got)
	}
}

func TestAfterCommit(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()

	t.Run("fires once after commit, with a ctx that carries no transaction", func(t *testing.T) {
		var fired atomic.Int32
		var callbackErr error
		err := c.RunTx(ctx, Serializable, func(ctx context.Context) error {
			if err := insertNote(ctx, c, "committed"); err != nil {
				return err
			}
			return AfterCommit(ctx, func(cbCtx context.Context) {
				fired.Add(1)
				// The callback runs AFTER commit: the row is visible to an
				// outside observer, and the ctx it gets is not transactional.
				if got := countNotes(t, c); got != 1 {
					t.Errorf("callback ran before the commit was visible (%d rows)", got)
				}
				callbackErr = AfterCommit(cbCtx, func(context.Context) {})
			})
		})
		if err != nil {
			t.Fatalf("RunTx: %v", err)
		}
		if fired.Load() != 1 {
			t.Errorf("after-commit callback fired %d times, want 1", fired.Load())
		}
		if !errors.Is(callbackErr, ErrNoTransaction) {
			t.Errorf("callback ctx must not carry the finished transaction; AfterCommit returned %v", callbackErr)
		}
	})

	t.Run("never fires on rollback", func(t *testing.T) {
		var fired atomic.Int32
		_ = c.RunTx(ctx, Serializable, func(ctx context.Context) error {
			if err := AfterCommit(ctx, func(context.Context) { fired.Add(1) }); err != nil {
				return err
			}
			return errors.New("roll back")
		})
		if fired.Load() != 0 {
			t.Errorf("callback fired %d times after a rollback, want 0", fired.Load())
		}
	})

	t.Run("a retried attempt's callbacks are discarded", func(t *testing.T) {
		var firedFor []int
		attempts := 0
		err := c.RunTx(ctx, Serializable, func(ctx context.Context) error {
			attempts++
			attempt := attempts
			if err := AfterCommit(ctx, func(context.Context) { firedFor = append(firedFor, attempt) }); err != nil {
				return err
			}
			if attempt == 1 {
				return &pq.Error{Code: "40001"}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("RunTx: %v", err)
		}
		if len(firedFor) != 1 || firedFor[0] != 2 {
			t.Errorf("callbacks fired for attempts %v, want only [2]", firedFor)
		}
	})

	t.Run("a joined call's callback waits for the outermost commit", func(t *testing.T) {
		var fired atomic.Int32
		err := c.RunTx(ctx, Serializable, func(ctx context.Context) error {
			if err := c.RunTx(ctx, Serializable, func(ctx context.Context) error {
				return AfterCommit(ctx, func(context.Context) { fired.Add(1) })
			}); err != nil {
				return err
			}
			if fired.Load() != 0 {
				t.Error("callback fired when the joined inner RunTx returned, before the outer commit")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("RunTx: %v", err)
		}
		if fired.Load() != 1 {
			t.Errorf("callback fired %d times, want 1 after the outer commit", fired.Load())
		}
	})

	t.Run("outside a transaction it is an error", func(t *testing.T) {
		if err := AfterCommit(ctx, func(context.Context) {}); !errors.Is(err, ErrNoTransaction) {
			t.Errorf("AfterCommit outside a transaction = %v, want ErrNoTransaction", err)
		}
	})

	t.Run("RunTransaction commits run callbacks registered through the handle", func(t *testing.T) {
		var fired atomic.Int32
		err := c.RunTransaction(ctx, ReadCommitted, func(tx Context) error {
			return tx.RunTx(ctx, ReadCommitted, func(ctx context.Context) error {
				return AfterCommit(ctx, func(context.Context) { fired.Add(1) })
			})
		})
		if err != nil {
			t.Fatalf("RunTransaction: %v", err)
		}
		if fired.Load() != 1 {
			t.Errorf("callback fired %d times, want 1", fired.Load())
		}
	})
}

// A transaction carried for database A must never execute a statement sent
// to database B. B's write inside A's RunTx is an ordinary autocommitted
// statement on B's pool: visible at once, and untouched by A's rollback.
func TestRunTx_TransactionNeverLeaksAcrossDatabases(t *testing.T) {
	a := newTxTestClient(t)
	b := newTxTestClient(t)
	ctx := context.Background()

	err := a.RunTx(ctx, Serializable, func(ctx context.Context) error {
		if err := insertNote(ctx, a, "on-a"); err != nil {
			return err
		}
		if err := insertNote(ctx, b, "on-b"); err != nil {
			return fmt.Errorf("write to b inside a's transaction: %w", err)
		}
		if got := countNotes(t, b); got != 1 {
			t.Errorf("b's write should be autocommitted on b's pool; outside observer sees %d rows", got)
		}
		return errors.New("roll a back")
	})
	if err == nil {
		t.Fatal("expected a's rollback error")
	}
	if got := countNotes(t, a); got != 0 {
		t.Errorf("a: %d rows after rollback, want 0", got)
	}
	if got := countNotes(t, b); got != 1 {
		t.Errorf("b: %d rows, want 1 — a's rollback must not reach b", got)
	}

	// Nested the other way round, each database finds its OWN transaction:
	// b's RunTx inside a's does not shadow a's.
	err = a.RunTx(ctx, Serializable, func(ctx context.Context) error {
		return b.RunTx(ctx, Serializable, func(ctx context.Context) error {
			if err := insertNote(ctx, a, "a-in-nested"); err != nil {
				return err
			}
			if got := countNotes(t, a); got != 0 {
				t.Errorf("a's write inside the nested b transaction escaped a's transaction (%d visible)", got)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatalf("nested RunTx: %v", err)
	}
	if got := countNotes(t, a); got != 1 {
		t.Errorf("a: %d rows after commit, want 1", got)
	}
}

// With no transaction in ctx, the client behaves exactly as before: every
// statement autocommits on the pool.
func TestRunTx_NoCtxTransactionPathUnchanged(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()

	if err := insertNote(ctx, c, "plain"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if got := countNotes(t, c); got != 1 {
		t.Fatalf("plain insert not visible: %d rows", got)
	}
	if got := showSetting(ctx, t, c, "transaction_isolation"); got != "read committed" {
		t.Errorf("outside RunTx the pool's default isolation applies; got %q", got)
	}
}

// A ctx captured inside fn and used after RunTx returned still works: the
// finished transaction is inert, so queries fall back to the pool instead of
// failing with sql: transaction has already been committed or rolled back.
func TestRunTx_FinishedTransactionInCtxFallsBackToPool(t *testing.T) {
	c := newTxTestClient(t)
	var leaked context.Context
	if err := c.RunTx(context.Background(), Serializable, func(ctx context.Context) error {
		leaked = ctx
		return insertNote(ctx, c, "x")
	}); err != nil {
		t.Fatalf("RunTx: %v", err)
	}
	if err := insertNote(leaked, c, "after"); err != nil {
		t.Fatalf("query with a ctx carrying a finished transaction: %v", err)
	}
	if got := countNotes(t, c); got != 2 {
		t.Errorf("%d rows, want 2", got)
	}
}

// db.Bun().ExecContext must join the ctx transaction. If it went to the pool
// instead, an UPDATE of a row the transaction already locked would wait on a
// second connection for a lock held by the first, inside one goroutine — a
// deadlock postgres cannot detect, ended only by the ctx deadline.
func TestRunTx_BunExecContextJoinsInsteadOfSelfDeadlocking(t *testing.T) {
	c := newTxTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := insertNote(ctx, c, "row"); err != nil {
		t.Fatal(err)
	}

	err := c.RunTx(ctx, Serializable, func(ctx context.Context) error {
		if _, err := c.Bun().NewUpdate().Model((*txNote)(nil)).
			Set("body = ?", "locked").Where("id = ?", "row").Exec(ctx); err != nil {
			return err
		}
		_, err := c.Bun().ExecContext(ctx, "UPDATE tx_notes SET body = ? WHERE id = ?", "again", "row")
		return err
	})
	if err != nil {
		t.Fatalf("bun ExecContext inside RunTx: %v", err)
	}
}
