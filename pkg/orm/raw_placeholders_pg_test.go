package orm

import (
	"context"
	"errors"
	"testing"
)

// Against a real postgres: the raw Exec/Query/QueryRow of a Client and of a
// Tx refuse bun-style `?` with ErrQuestionPlaceholders — before, the
// statement reached postgres and failed as a bare 42601 — while $N
// statements, and the jsonb `?` operator beside them, run as written.
func TestRawPath_QuestionPlaceholders(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()
	if _, err := c.Exec(ctx, `INSERT INTO tx_notes (id, body) VALUES ($1, $2)`, "a", `{"k":1}`); err != nil {
		t.Fatalf("insert with $N: %v", err)
	}

	check := func(t *testing.T, db Context) {
		t.Helper()
		if _, err := db.Exec(ctx, `UPDATE tx_notes SET body = ? WHERE id = ?`, "x", "a"); !errors.Is(err, ErrQuestionPlaceholders) {
			t.Errorf("Exec with ?: err = %v, want ErrQuestionPlaceholders", err)
		}
		rows, err := db.Query(ctx, `SELECT body FROM tx_notes WHERE id = ?`, "a")
		if !errors.Is(err, ErrQuestionPlaceholders) {
			t.Errorf("Query with ?: err = %v, want ErrQuestionPlaceholders", err)
		}
		if rows != nil {
			rows.Close()
		}
		var body string
		if err := db.QueryRow(ctx, `SELECT body FROM tx_notes WHERE id = ?`, "a").Scan(&body); !errors.Is(err, ErrQuestionPlaceholders) {
			t.Errorf("QueryRow with ?: Scan err = %v, want ErrQuestionPlaceholders", err)
		}

		// $N, with postgres's own `?` operator beside it, runs as written.
		var hasKey bool
		if err := db.QueryRow(ctx, `SELECT body::jsonb ? 'k' FROM tx_notes WHERE id = $1`, "a").Scan(&hasKey); err != nil || !hasKey {
			t.Errorf("QueryRow with $1 and jsonb ?: hasKey=%v err=%v", hasKey, err)
		}
		if err := db.QueryRow(ctx, `SELECT body FROM tx_notes WHERE body::jsonb ? $1`, "k").Scan(&body); err != nil {
			t.Errorf("QueryRow with jsonb ? $1: %v", err)
		}
		if _, err := db.Exec(ctx, `UPDATE tx_notes SET body = body WHERE id = $1`, "a"); err != nil {
			t.Errorf("Exec with $1: %v", err)
		}
	}

	t.Run("client", func(t *testing.T) { check(t, c) })
	t.Run("tx", func(t *testing.T) {
		err := c.RunTransaction(ctx, ReadCommitted, func(tx Context) error {
			check(t, tx)
			return nil
		})
		if err != nil {
			t.Fatalf("RunTransaction: %v", err)
		}
	})
	// The refused QueryRow sent nothing: the pool's connection is healthy.
	var n int
	if err := c.QueryRow(ctx, `SELECT count(*) FROM tx_notes`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("after refusals: n=%d err=%v", n, err)
	}
}
