package orm_test

import (
	"context"
	"strings"
	"testing"

	"github.com/uptrace/bun"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/querycapture"
)

type captureNote struct {
	bun.BaseModel `bun:"table:capture_notes,alias:n"`

	ID     string `bun:"id,pk"`
	Status string `bun:"status,notnull"`
	Body   string `bun:"body,notnull"`
}

// A Client built with FORGE_QUERY_CAPTURE_DIR set records each statement as
// it is SENT — bound, with $n where bun's own hooks see the values inline —
// with this file as the caller, on every path a statement takes: bun's
// builders, the raw passthrough pkg/crud's by-PK statements use, a
// transaction, and bun's raw ExecContext.
func TestCapture_RecordsBoundStatementsWithTheirCaller(t *testing.T) {
	if testing.Short() {
		t.Skip("boots embedded postgres; skipped under -short")
	}
	dir := t.TempDir()
	t.Setenv(querycapture.EnvDir, dir)
	sqldb, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	c, err := orm.NewClientWithDB(sqldb, "postgres")
	if err != nil {
		t.Fatalf("NewClientWithDB: %v", err)
	}
	ctx := context.Background()

	if _, err := c.Exec(ctx, `CREATE TABLE capture_notes (id TEXT PRIMARY KEY, status TEXT NOT NULL, body TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	notes := []captureNote{{ID: "a", Status: "open", Body: "secret-a"}, {ID: "b", Status: "open", Body: "secret-b"}}
	if _, err := c.Bun().NewInsert().Model(&notes).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} { // two runs, one shape, one caller
		var got []captureNote
		if err := c.Bun().NewSelect().Model(&got).Where("n.id = ?", id).Where("n.status IN (?)", bun.In([]string{"open", "closed"})).Scan(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Exec(ctx, `UPDATE capture_notes SET body = $1 WHERE id = $2`, "secret-c", "a"); err != nil {
		t.Fatal(err)
	}
	if err := c.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		_, err := c.Bun().NewDelete().Model((*captureNote)(nil)).Where("id = ?", "b").Exec(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Bun().ExecContext(ctx, "UPDATE capture_notes SET status = ? WHERE id = ?", "closed", "a"); err != nil {
		t.Fatal(err)
	}

	capture, err := querycapture.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	find := func(prefix string) querycapture.Shape {
		t.Helper()
		for _, s := range capture.Shapes {
			if strings.HasPrefix(s.SQL, prefix) {
				return s
			}
		}
		var all []string
		for _, s := range capture.Shapes {
			all = append(all, s.SQL)
		}
		t.Fatalf("no captured statement starts with %q; captured:\n%s", prefix, strings.Join(all, "\n"))
		return querycapture.Shape{}
	}

	for _, tc := range []struct{ prefix, want string }{
		{`INSERT INTO "capture_notes"`, `INSERT INTO "capture_notes" ("id", "status", "body") VALUES ($1, $2, $3)`},
		{`SELECT "n"."id"`, `SELECT "n"."id", "n"."status", "n"."body" FROM "capture_notes" AS "n" WHERE (n.id = $1) AND (n.status IN ($2))`},
		{`UPDATE capture_notes SET body`, `UPDATE capture_notes SET body = $1 WHERE id = $2`},
		{`DELETE FROM "capture_notes"`, `DELETE FROM "capture_notes" AS "n" WHERE (id = $1)`},
		// bun's raw ExecContext formats its values inline and is sent so.
		{`UPDATE capture_notes SET status`, `UPDATE capture_notes SET status = 'closed' WHERE id = 'a'`},
	} {
		s := find(tc.prefix)
		if s.SQL != tc.want {
			t.Errorf("captured\n  %s\nwant\n  %s", s.SQL, tc.want)
		}
		if strings.Contains(s.SQL, "secret") {
			t.Errorf("a bound value reached the capture: %s", s.SQL)
		}
		if len(s.Callers) != 1 || s.Count != 1 {
			t.Fatalf("%s: want one record from one caller, got count %d callers %+v", tc.prefix, s.Count, s.Callers)
		}
		cs := s.Callers[0]
		if cs.Caller.File != "pkg/orm/capture_pg_test.go" || cs.Caller.Line == 0 {
			t.Errorf("%s: caller %+v, want this file", tc.prefix, cs.Caller)
		}
		if len(cs.Tests) != 1 || cs.Tests[0] != "TestCapture_RecordsBoundStatementsWithTheirCaller" {
			t.Errorf("%s: tests %v, want this test", tc.prefix, cs.Tests)
		}
	}
	if s := find(`SELECT "n"."id"`); s.Operation != "SELECT" || len(s.Tables) != 1 || s.Tables[0] != "capture_notes" {
		t.Errorf("select: operation %q tables %v", s.Operation, s.Tables)
	}
}
