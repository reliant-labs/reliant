package orm

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// The client's escape hatches (Exec/Query/QueryRow/BunDB/DB/Close) and the
// explicit-transaction handle (Tx) against a real postgres: each runs where
// it says it runs, and the refusals come back as errors rather than
// reaching the server.

func TestClient_RawEscapeHatches(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()

	if c.DB() == nil || c.BunDB() == nil || c.Dialect().Name() != "postgres" {
		t.Fatalf("accessors: DB=%v BunDB=%v dialect=%v", c.DB(), c.BunDB(), c.Dialect())
	}
	if _, err := c.Exec(ctx, `INSERT INTO tx_notes (id, body) VALUES ($1, $2)`, "a", "hello"); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	rows, err := c.Query(ctx, `SELECT id, body FROM tx_notes`)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !rows.Next() {
		t.Fatal("Query returned no row")
	}
	var id, body string
	if err := rows.Scan(&id, &body); err != nil || id != "a" || body != "hello" {
		t.Fatalf("Scan = %q %q %v", id, body, err)
	}
	_ = rows.Close()

	// bun-style `?` with arguments is refused before it reaches postgres, on
	// all three raw paths; QueryRow's refusal surfaces from Scan.
	if _, err := c.Exec(ctx, `UPDATE tx_notes SET body = ? WHERE id = ?`, "x", "a"); !errors.Is(err, ErrQuestionPlaceholders) {
		t.Errorf("Exec with ? = %v, want ErrQuestionPlaceholders", err)
	}
	if _, err := c.Query(ctx, `SELECT body FROM tx_notes WHERE id = ?`, "a"); !errors.Is(err, ErrQuestionPlaceholders) {
		t.Errorf("Query with ? = %v, want ErrQuestionPlaceholders", err)
	}
	var got string
	if err := c.QueryRow(ctx, `SELECT body FROM tx_notes WHERE id = ?`, "a").Scan(&got); !errors.Is(err, ErrQuestionPlaceholders) {
		t.Errorf("QueryRow with ? = %v, want ErrQuestionPlaceholders", err)
	}
	if err := c.QueryRow(ctx, `SELECT body FROM tx_notes WHERE id = $1`, "a").Scan(&got); err != nil || got != "hello" {
		t.Errorf("QueryRow with $1 = %q, %v", got, err)
	}
	// jsonb's `?` operator with a $N parameter is postgres's, and runs.
	var has bool
	if err := c.QueryRow(ctx, `SELECT '{"k":1}'::jsonb ? $1`, "k").Scan(&has); err != nil || !has {
		t.Errorf("jsonb ? operator: %v, %v", has, err)
	}
}

func TestClient_BunDBBuildersJoinTheContextTransaction(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()
	err := c.RunTx(ctx, Serializable, func(ctx context.Context) error {
		// BunDB's builders resolve the transaction through the client's resolver…
		if _, err := c.BunDB().NewInsert().Model(&txNote{ID: "in-tx", Body: "b"}).Exec(ctx); err != nil {
			return err
		}
		if got := countNotes(t, c); got != 0 {
			t.Errorf("an outside observer sees %d rows before commit", got)
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("RunTx = %v", err)
	}
	if got := countNotes(t, c); got != 0 {
		t.Fatalf("%d rows survived the rollback", got)
	}
}

func TestClient_CloseRefusesFurtherQueries(t *testing.T) {
	c := newBareClient(t)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := c.Exec(context.Background(), `SELECT 1`); err == nil {
		t.Fatal("Exec on a closed client succeeded")
	}
}

func TestNewClient_OpensAndPingsTheDSN(t *testing.T) {
	if testing.Short() {
		t.Skip("boots postgres; skipped under -short")
	}
	if _, err := NewClient("mysql", "ignored"); !errors.Is(err, ErrInvalidDialect) {
		t.Fatalf("NewClient(mysql) = %v, want ErrInvalidDialect", err)
	}
	// Nothing listens on port 1: the ping fails and says so.
	if _, err := NewClient("postgres", "postgres://nobody@127.0.0.1:1/none?connect_timeout=1"); err == nil || !strings.Contains(err.Error(), "failed to ping database") {
		t.Fatalf("NewClient on a dead DSN = %v", err)
	}
	dsn := newDSN(t)
	c, err := NewClient("postgres", dsn)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close()
	var one int
	if err := c.QueryRow(context.Background(), `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("SELECT 1 = %d, %v", one, err)
	}
}

// Tx handle: Query/QueryRow run inside the transaction, and the join helpers
// hand fn a ctx that carries it.
func TestTx_HandleRunsStatementsInsideTheTransaction(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()

	tx, err := c.BeginDetached(ctx, ReadCommitted)
	if err != nil {
		t.Fatalf("BeginDetached: %v", err)
	}
	if tx.Dialect().Name() != "postgres" {
		t.Errorf("Tx.Dialect = %v", tx.Dialect())
	}
	if _, err := tx.Exec(ctx, `INSERT INTO tx_notes (id, body) VALUES ($1, 'b')`, "t1"); err != nil {
		t.Fatalf("Tx.Exec: %v", err)
	}
	rows, err := tx.Query(ctx, `SELECT id FROM tx_notes`)
	if err != nil {
		t.Fatalf("Tx.Query: %v", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if len(ids) != 1 || ids[0] != "t1" {
		t.Errorf("Tx.Query saw %v, want its own uncommitted row", ids)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM tx_notes`).Scan(&n); err != nil || n != 1 {
		t.Errorf("Tx.QueryRow = %d, %v", n, err)
	}
	if _, err := tx.Query(ctx, `SELECT ?`, 1); !errors.Is(err, ErrQuestionPlaceholders) {
		t.Errorf("Tx.Query with ? = %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT ?`, 1).Scan(&n); !errors.Is(err, ErrQuestionPlaceholders) {
		t.Errorf("Tx.QueryRow with ? = %v", err)
	}
	if got := countNotes(t, c); got != 0 {
		t.Errorf("outside observer sees %d rows before commit", got)
	}

	// On a *Tx, RunTxReadOnly and RunTransaction join rather than nest.
	nested := 0
	if err := tx.RunTxReadOnly(ctx, func(ctx context.Context) error {
		nested++
		if !c.InTx(ctx) {
			t.Error("RunTxReadOnly on a Tx did not join: ctx carries no transaction")
		}
		return nil
	}); err != nil || nested != 1 {
		t.Fatalf("Tx.RunTxReadOnly = %v (ran %d times)", err, nested)
	}
	if err := tx.RunTransaction(ctx, ReadCommitted, func(inner Context) error {
		if inner != Context(tx) {
			t.Error("Tx.RunTransaction opened a nested transaction instead of joining")
		}
		return nil
	}); err != nil {
		t.Fatalf("Tx.RunTransaction: %v", err)
	}
	if err := tx.RunTransaction(ctx, TxOptions{}, func(Context) error { return nil }); !errors.Is(err, ErrInvalidTxOptions) {
		t.Errorf("Tx.RunTransaction without a level = %v, want ErrInvalidTxOptions", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := countNotes(t, c); got != 1 {
		t.Errorf("after commit: %d rows, want 1", got)
	}
}

func TestTx_BunBuildersArePinnedToTheTransaction(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()
	tx, err := c.BeginDetached(ctx, ReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	b := tx.Bun()
	if _, err := b.NewInsert().Model(&txNote{ID: "p1", Body: "b"}).Exec(ctx); err != nil {
		t.Fatalf("NewInsert: %v", err)
	}
	if _, err := b.NewUpdate().Model((*txNote)(nil)).Set("body = ?", "changed").Where("id = ?", "p1").Exec(ctx); err != nil {
		t.Fatalf("NewUpdate: %v", err)
	}
	var body string
	if err := b.NewSelect().Model((*txNote)(nil)).Column("body").Where("id = ?", "p1").Scan(ctx, &body); err != nil || body != "changed" {
		t.Fatalf("NewSelect = %q, %v", body, err)
	}
	var raw string
	if err := b.NewRaw("SELECT body FROM tx_notes WHERE id = ?", "p1").Scan(ctx, &raw); err != nil || raw != "changed" {
		t.Fatalf("NewRaw = %q, %v", raw, err)
	}
	var fromValues string
	if err := b.NewSelect().With("v", b.NewValues(&[]*txNote{{ID: "v", Body: "vb"}})).
		TableExpr("v").ColumnExpr("body").Scan(ctx, &fromValues); err != nil || fromValues != "vb" {
		t.Fatalf("NewValues = %q, %v", fromValues, err)
	}
	if _, err := b.NewMerge().Model((*txNote)(nil)).
		Using("(SELECT 'p1' AS id) AS s").On("tx_notes.id = s.id").WhenDelete("MATCHED").Exec(ctx); err != nil {
		t.Fatalf("NewMerge: %v", err)
	}
	var left int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM tx_notes`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("after the merge-delete %d rows remain, err %v", left, err)
	}
	if _, err := b.NewInsert().Model(&txNote{ID: "p1", Body: "b"}).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.NewDelete().Model((*txNote)(nil)).Where("id = ?", "p1").Exec(ctx); err != nil {
		t.Fatalf("NewDelete: %v", err)
	}
	if got := countNotes(t, c); got != 0 {
		t.Errorf("outside observer sees %d rows from an open transaction", got)
	}
}

// Inline values: a statement run under InlineValues is sent with its values in
// the text, so a statement binding cannot express still runs.
func TestInlineValues_StatementStillRuns(t *testing.T) {
	c := newTxTestClient(t)
	ctx := InlineValues(context.Background())
	if _, err := c.Bun().NewInsert().Model(&txNote{ID: "inl", Body: "it's"}).Exec(ctx); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var body string
	if err := c.Bun().NewSelect().Model((*txNote)(nil)).Column("body").Where("id = ?", "inl").Scan(ctx, &body); err != nil || body != "it's" {
		t.Fatalf("select = %q, %v", body, err)
	}
	var one int
	if err := c.Bun().QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("QueryRowContext = %d, %v", one, err)
	}
	rows, err := c.Bun().QueryContext(ctx, `SELECT 1`)
	if err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
}

func TestRunTxReadOnly_RejectsWrites(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()
	err := c.RunTxReadOnly(ctx, func(ctx context.Context) error { return insertNote(ctx, c, "ro") })
	if err == nil {
		t.Fatal("a write in a read-only transaction succeeded")
	}
	if got := countNotes(t, c); got != 0 {
		t.Fatalf("%d rows written by a read-only transaction", got)
	}
}

// sql.ErrTxDone after Commit: a handle used past its end fails, never silently
// runs on the pool.
func TestTx_UsedAfterCommitFails(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()
	tx, err := c.BeginDetached(ctx, ReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1`); !errors.Is(err, sql.ErrTxDone) {
		t.Errorf("Exec after Commit = %v, want sql.ErrTxDone", err)
	}
	if err := tx.Rollback(); !errors.Is(err, sql.ErrTxDone) {
		t.Errorf("Rollback after Commit = %v, want sql.ErrTxDone", err)
	}
}

func newDSN(t *testing.T) string {
	t.Helper()
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("pgtest.NewURL: %v", err)
	}
	t.Cleanup(cleanup)
	return dsn
}

// DBTX is the executor sqlc's Queries takes: Prepare, Query and QueryRow each
// resolve the ctx's transaction, like Exec does.
func TestDBTX_PrepareQueryAndQueryRowJoinTheTransaction(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()
	db := c.DBTX()

	err := c.RunTx(ctx, ReadCommitted, func(ctx context.Context) error {
		stmt, err := db.PrepareContext(ctx, `INSERT INTO tx_notes (id, body) VALUES ($1, 'b')`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		if _, err := stmt.ExecContext(ctx, "prepared"); err != nil {
			return err
		}
		var id string
		if err := db.QueryRowContext(ctx, `SELECT id FROM tx_notes`).Scan(&id); err != nil || id != "prepared" {
			t.Errorf("QueryRowContext in the tx = %q, %v", id, err)
		}
		rows, err := db.QueryContext(ctx, `SELECT count(*) FROM tx_notes`)
		if err != nil {
			return err
		}
		defer rows.Close()
		var n int
		if rows.Next() {
			_ = rows.Scan(&n)
		}
		if n != 1 {
			t.Errorf("QueryContext in the tx counted %d, want 1", n)
		}
		if got := countNotes(t, c); got != 0 {
			t.Errorf("outside observer sees %d rows before commit", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunTx: %v", err)
	}
	if got := countNotes(t, c); got != 1 {
		t.Fatalf("after commit %d rows, want 1", got)
	}
}
