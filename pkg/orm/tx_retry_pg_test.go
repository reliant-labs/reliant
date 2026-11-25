package orm

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// WithRetryOn against a REAL postgres, through the driver forge opens (pgx
// stdlib): the errors a swallowed statement failure really produces reach
// the retry loop, and only a classifier makes them retryable.

// swallowFailedStatement runs a statement that fails, aborting the
// transaction, and ignores the error — the best-effort read that logs and
// carries on.
func swallowFailedStatement(ctx context.Context, c *Client) {
	_, _ = c.Exec(ctx, `SELECT 1/0`)
}

func TestRunTx_WithRetryOn(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()

	t.Run("a COMMIT that rolled back is not retried by default", func(t *testing.T) {
		attempts := 0
		err := c.RunTx(ctx, Serializable, func(ctx context.Context) error {
			attempts++
			swallowFailedStatement(ctx, c)
			return nil
		})
		if attempts != 1 || !errors.Is(err, pgx.ErrTxCommitRollback) || svcerr.IsAborted(err) {
			t.Fatalf("attempts=%d err=%v; want 1 attempt and pgx.ErrTxCommitRollback reaching the caller", attempts, err)
		}
	})

	t.Run("RetryOnCommitRollback retries it and the clean attempt commits", func(t *testing.T) {
		attempts := 0
		err := c.RunTx(ctx, Serializable.WithRetryOn(RetryOnCommitRollback), func(ctx context.Context) error {
			attempts++
			if err := insertNote(ctx, c, fmt.Sprintf("commit-rollback-%d", attempts)); err != nil {
				return err
			}
			if attempts == 1 {
				swallowFailedStatement(ctx, c)
			}
			return nil
		})
		if err != nil || attempts != 2 {
			t.Fatalf("attempts=%d err=%v; want the second attempt to commit", attempts, err)
		}
		var ids []string
		if err := c.Bun().NewSelect().Model((*txNote)(nil)).Column("id").
			Where("id LIKE 'commit-rollback-%'").Scan(ctx, &ids); err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || ids[0] != "commit-rollback-2" {
			t.Errorf("surviving rows = %v, want only the retry's commit-rollback-2", ids)
		}
	})

	t.Run("a 25P02 is retried only when named, and exhausts to Aborted", func(t *testing.T) {
		body := func(attempts *int) func(ctx context.Context) error {
			return func(ctx context.Context) error {
				*attempts++
				swallowFailedStatement(ctx, c)
				_, err := c.Exec(ctx, `SELECT 1`) // 25P02: the transaction is already aborted
				return err
			}
		}
		var plain int
		if err := c.RunTx(ctx, Serializable, body(&plain)); plain != 1 || svcerr.IsAborted(err) {
			t.Errorf("default: attempts=%d err=%v; want 1 attempt and the 25P02 itself", plain, err)
		}

		policy := Serializable.WithMaxAttempts(3).WithRetryOn(RetryOnSQLState("25P02"))
		for name, run := range map[string]func(*int) error{
			"RunTx": func(n *int) error { return c.RunTx(ctx, policy, body(n)) },
			"RunTransaction": func(n *int) error {
				return c.RunTransaction(ctx, policy, func(tx Context) error {
					return tx.(*Tx).Join(ctx, body(n))
				})
			},
		} {
			var attempts int
			err := run(&attempts)
			var pgErr *pgconn.PgError
			if attempts != 3 || !svcerr.IsAborted(err) || !errors.As(err, &pgErr) || pgErr.Code != "25P02" {
				t.Errorf("%s: attempts=%d err=%v; want MaxAttempts=3 then svcerr Aborted with the 25P02 as cause", name, attempts, err)
			}
		}
	})
}
