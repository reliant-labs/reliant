package pgtest_test

import (
	"database/sql"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// cloneSource builds a database worth copying — a table, a row and a
// sequence that has advanced — and closes every connection to it, which is
// the precondition NewClone and NewCloneAtURL state.
func cloneSource(t *testing.T, newDB func() (*sql.DB, func(), error)) string {
	t.Helper()
	db, cleanup, err := newDB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(`CREATE TABLE items (id BIGSERIAL PRIMARY KEY, v int NOT NULL); INSERT INTO items (v) VALUES (42)`); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := db.QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return name
}

// assertIndependentCopies checks two copies of cloneSource's database: each
// holds the source's row, and a write to one — including the sequence it
// advances — reaches neither the other nor a copy made afterwards.
func assertIndependentCopies(t *testing.T, a, b, later *sql.DB) {
	t.Helper()
	for i, db := range []*sql.DB{a, b} {
		var v int
		if err := db.QueryRow(`SELECT v FROM items WHERE id = 1`).Scan(&v); err != nil || v != 42 {
			t.Fatalf("copy %d lost the source's row: v=%d err=%v", i, v, err)
		}
	}
	if _, err := a.Exec(`INSERT INTO items (v) VALUES (7)`); err != nil {
		t.Fatal(err)
	}
	for name, db := range map[string]*sql.DB{"the sibling copy": b, "a later copy": later} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM items`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("a write to one copy reached %s: rows=%d err=%v", name, n, err)
		}
		var next int64
		if err := db.QueryRow(`SELECT nextval('items_id_seq')`).Scan(&next); err != nil || next != 2 {
			t.Fatalf("%s starts its sequence at %d (err=%v), want 2 — the copy it was made from advanced it", name, next, err)
		}
	}
}

func TestNewClone_CopiesAreIndependent(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	source := cloneSource(t, pgtest.New)
	clone := func() *sql.DB {
		db, cleanup, err := pgtest.NewClone(source)
		if err != nil {
			t.Fatalf("NewClone(%s): %v", source, err)
		}
		t.Cleanup(cleanup)
		return db
	}
	a, b := clone(), clone()
	// Open the third copy only after the first write, so it proves the
	// source itself was untouched rather than that copies are made eagerly.
	assertIndependentCopiesAfterWrite(t, a, b, clone)
}

func TestNewCloneAtURL_CopiesAreIndependent(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	// A scratch database on the shared server stands in for a configured
	// server's maintenance database, the way schemadef's tests do it.
	baseURL, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	source := cloneSource(t, func() (*sql.DB, func(), error) { return pgtest.NewAtURL(baseURL) })
	clone := func() *sql.DB {
		db, cleanup, err := pgtest.NewCloneAtURL(baseURL, source)
		if err != nil {
			t.Fatalf("NewCloneAtURL(%s): %v", source, err)
		}
		t.Cleanup(cleanup)
		return db
	}
	a, b := clone(), clone()
	assertIndependentCopiesAfterWrite(t, a, b, clone)
}

func assertIndependentCopiesAfterWrite(t *testing.T, a, b *sql.DB, clone func() *sql.DB) {
	t.Helper()
	if _, err := a.Exec(`SELECT nextval('items_id_seq')`); err != nil {
		t.Fatal(err)
	}
	later := clone()
	// a's sequence has now moved twice (the nextval above and the insert
	// below); the others must not have moved at all.
	assertIndependentCopies(t, a, b, later)
}

// A clone needs a template to copy; an empty name is a caller bug, not a
// request for an empty database.
func TestNewClone_RefusesAnEmptyTemplateName(t *testing.T) {
	if _, _, err := pgtest.NewClone(""); err == nil {
		t.Error("NewClone(\"\") must refuse")
	}
	if _, _, err := pgtest.NewCloneAtURL("postgres://localhost:1/postgres", ""); err == nil {
		t.Error("NewCloneAtURL with no template must refuse")
	}
}
