//go:build cgo

// TestJSONThroughSQLite needs a real driver, and mattn/go-sqlite3 is cgo-only:
// under CGO_ENABLED=0 it compiles to a stub whose every call fails. The tag
// keeps it running wherever cgo is on (Linux CI) instead of failing the
// cgo-free Windows job on a driver limitation rather than a JSON defect.

package orm

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// TestJSONThroughSQLite drives the helpers through a real database/sql
// driver — the same path the generated scan/insert code uses — so the
// "driver.Valuer in, sql.Scanner out" contract is pinned against an
// actual driver, not just direct calls.
func TestJSONThroughSQLite(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `CREATE TABLE bookmarks (id TEXT PRIMARY KEY, tags JSONB)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	tags := []string{"go", "orm"}
	if _, err := db.ExecContext(ctx, `INSERT INTO bookmarks (id, tags) VALUES (?, ?)`, "b1", JSON(tags)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var got []string
	if err := db.QueryRowContext(ctx, `SELECT tags FROM bookmarks WHERE id = ?`, "b1").Scan(ScanJSON(&got)); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !reflect.DeepEqual(got, tags) {
		t.Errorf("round trip through sqlite = %#v, want %#v", got, tags)
	}

	// NULL column → zero value.
	if _, err := db.ExecContext(ctx, `INSERT INTO bookmarks (id, tags) VALUES (?, ?)`, "b2", JSON([]string(nil))); err != nil {
		t.Fatalf("insert nil: %v", err)
	}
	var empty []string
	if err := db.QueryRowContext(ctx, `SELECT tags FROM bookmarks WHERE id = ?`, "b2").Scan(ScanJSON(&empty)); err != nil {
		t.Fatalf("scan nil: %v", err)
	}
	if empty != nil {
		t.Errorf("NULL tags should scan to nil, got %#v", empty)
	}
}
