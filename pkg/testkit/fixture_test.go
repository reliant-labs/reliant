package testkit

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
)

func TestParseFixture_CanonicalShape(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"name": "users",
		"description": "seed",
		"tables": {
			"users": [
				{"id": "u1", "email": "a@example.com"},
				{"id": "u2", "email": "b@example.com"}
			]
		}
	}`)
	fx, err := parseFixture(raw)
	if err != nil {
		t.Fatalf("parseFixture: %v", err)
	}
	if fx.Name != "users" {
		t.Fatalf("name: got %q", fx.Name)
	}
	if got := len(fx.Tables["users"]); got != 2 {
		t.Fatalf("rows: got %d, want 2", got)
	}
}

func TestParseFixture_RejectsEmpty(t *testing.T) {
	t.Parallel()
	if _, err := parseFixture([]byte(`{"name":"x","tables":{}}`)); err == nil {
		t.Fatal("expected error for fixture with no tables")
	}
}

func TestBuildInsert_DeterministicSortedColumns(t *testing.T) {
	t.Parallel()
	row := map[string]any{"email": "a@example.com", "id": "u1", "role": "admin"}
	query, args := buildInsert("users", row)

	want := `INSERT INTO "users" ("email", "id", "role") VALUES ($1, $2, $3)`
	if query != want {
		t.Fatalf("query:\n got %q\nwant %q", query, want)
	}
	wantArgs := []any{"a@example.com", "u1", "admin"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args: got %v, want %v", args, wantArgs)
	}
}

func TestBuildInsert_QuotesReservedIdentifiers(t *testing.T) {
	t.Parallel()
	query, _ := buildInsert("order", map[string]any{"select": 1})
	want := `INSERT INTO "order" ("select") VALUES ($1)`
	if query != want {
		t.Fatalf("got %q, want %q", query, want)
	}
}

// recordingExec is a minimal orm.Context that records executed statements,
// so insertFixture's ordering/SQL can be verified without a real database.
type recordingExec struct {
	stmts []recordedStmt
}

type recordedStmt struct {
	query string
	args  []any
}

func (r *recordingExec) Exec(_ context.Context, query string, args ...interface{}) (sql.Result, error) {
	r.stmts = append(r.stmts, recordedStmt{query: query, args: args})
	return nil, nil
}

func TestInsertFixture_OrdersTablesAndRows(t *testing.T) {
	t.Parallel()
	fx := &Fixture{
		Tables: map[string][]map[string]any{
			"users": {
				{"id": "u1"},
				{"id": "u2"},
			},
			"accounts": {
				{"id": "a1"},
			},
		},
	}
	rec := &recordingExec{}
	if err := insertFixture(context.Background(), rec, fx); err != nil {
		t.Fatalf("insertFixture: %v", err)
	}
	if len(rec.stmts) != 3 {
		t.Fatalf("expected 3 inserts, got %d", len(rec.stmts))
	}
	// accounts sorts before users; rows keep file order within a table.
	gotArgs := []any{rec.stmts[0].args[0], rec.stmts[1].args[0], rec.stmts[2].args[0]}
	want := []any{"a1", "u1", "u2"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("insert order: got %v, want %v", gotArgs, want)
	}
}

func TestInsertFixture_SkipsEmptyRow(t *testing.T) {
	t.Parallel()
	fx := &Fixture{Tables: map[string][]map[string]any{"t": {{}}}}
	rec := &recordingExec{}
	if err := insertFixture(context.Background(), rec, fx); err != nil {
		t.Fatalf("insertFixture: %v", err)
	}
	if len(rec.stmts) != 0 {
		t.Fatalf("empty row should produce no INSERT, got %d", len(rec.stmts))
	}
}
