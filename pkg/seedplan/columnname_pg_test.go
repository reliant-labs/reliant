package seedplan

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// twinColumnMigration declares the SAME table twice: once with a column per
// domain meaning the seeder used to read off an identifier, once with those
// names replaced by neutral ones. Types, order and constraints are identical,
// so postgres holds both to exactly the same contract and any value forge
// derives from a column's NAME shows up in exactly one of them.
//
// `contacts.reachable_at` is the other half of the story: an email-format
// CHECK on a column NOT spelled `email`. The old heuristics satisfied that
// CHECK when the column happened to be called `email` and violated it here,
// which aborts the whole transactional seed.
const twinColumnMigration = `
CREATE TABLE declared (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    first_name TEXT NOT NULL,
    email TEXT NOT NULL,
    phone TEXT NOT NULL,
    price_cents BIGINT NOT NULL,
    last4 TEXT NOT NULL,
    date_of_birth TEXT NOT NULL,
    role TEXT NOT NULL,
    currency TEXT NOT NULL,
    theme_primary TEXT NOT NULL,
    avatar_url TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    deleted_at TIMESTAMPTZ
);

CREATE TABLE renamed (
    id UUID PRIMARY KEY,
    c01 TEXT NOT NULL,
    c02 TEXT NOT NULL,
    c03 TEXT NOT NULL,
    c04 TEXT NOT NULL,
    c05 BIGINT NOT NULL,
    c06 TEXT NOT NULL,
    c07 TEXT NOT NULL,
    c08 TEXT NOT NULL,
    c09 TEXT NOT NULL,
    c10 TEXT NOT NULL,
    c11 TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    deleted_at TIMESTAMPTZ
);

CREATE TABLE contacts (
    id UUID PRIMARY KEY,
    reachable_at TEXT NOT NULL CHECK (reachable_at ~ '^[^@\s]+@[^@\s]+\.[^@\s]+$')
);
`

// twinColumns pairs each meaning-carrying column with its neutral twin.
var twinColumns = [][2]string{
	{"name", "c01"},
	{"first_name", "c02"},
	{"email", "c03"},
	{"phone", "c04"},
	{"price_cents", "c05"},
	{"last4", "c06"},
	{"date_of_birth", "c07"},
	{"role", "c08"},
	{"currency", "c09"},
	{"theme_primary", "c10"},
	{"avatar_url", "c11"},
}

// The same declaration under two vocabularies seeds the same data on a real
// postgres, and a format CHECK is satisfied on a column nobody named after it.
//
// The INSERT succeeding at all is half the point: every constraint in this
// migration is enforced by the database, so a value forge invents for the
// wrong reason either lands in one twin only or takes the whole dataset down
// with it — `forge db seed` is one transaction.
func TestMaterialize_ColumnNameDoesNotChangeTheSeedOnRealPostgres(t *testing.T) {
	requirePG(t)
	ctx := context.Background()

	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(twinColumnMigration); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte(twinColumnMigration), 0o644); err != nil {
		t.Fatalf("write migration: %v", err)
	}

	if _, err := Materialize(ctx, db, dir, "", Config{Rows: 8, Salt: 4}); err != nil {
		t.Fatalf("Materialize: %v\n\nA seeded value postgres rejects aborts the WHOLE dataset — "+
			"the seed is one transaction.", err)
	}

	// Column by column, the two tables hold the same values with the column's
	// own name substituted — the only role a name is allowed to play.
	if len(twinColumns) == 0 {
		t.Fatal("the twin-column sweep is empty — every assertion below would hold vacuously")
	}
	for _, pair := range twinColumns {
		got := columnValues(t, db, "declared", pair[0])
		want := columnValues(t, db, "renamed", pair[1])
		if len(got) == 0 || len(want) == 0 {
			t.Fatalf("%s/%s seeded no rows — the pair proves nothing", pair[0], pair[1])
		}
		for i := range got {
			got[i] = strings.ReplaceAll(got[i], pair[0], pair[1])
		}
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("declared.%s = %v\nrenamed.%s  = %v\n"+
				"the same declaration under two names produced two different datasets — forge "+
				"read domain meaning off the identifier",
				pair[0], got, pair[1], want)
		}
	}

	// The format CHECK on a column nobody named `email`. Postgres already
	// proved this by accepting the INSERT; re-checking here names the reason
	// if it ever regresses.
	shape := regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	reach := columnValues(t, db, "contacts", "reachable_at")
	if len(reach) == 0 {
		t.Fatal("contacts seeded no rows — the format-CHECK half proves nothing")
	}
	for _, v := range reach {
		if !shape.MatchString(v) {
			t.Errorf("contacts.reachable_at = %q does not satisfy the CHECK the schema declares", v)
		}
	}

	// Managed columns still behave, and they do it from the conventions
	// schemadef detects rather than from the seeder matching a name.
	assertNone := func(desc, query string) {
		t.Helper()
		var n int
		if err := db.QueryRow(query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%d row(s) violate: %s", n, desc)
		}
	}
	assertNone("declared.deleted_at is NULL (a soft-deleting table seeds live rows)",
		`SELECT count(*) FROM declared WHERE deleted_at IS NOT NULL`)
	assertNone("declared.created_at precedes updated_at",
		`SELECT count(*) FROM declared WHERE created_at >= updated_at`)
}

// columnValues reads one column's seeded values as text, NULL included as the
// literal "NULL" so a nulled cell is compared rather than dropped.
func columnValues(t *testing.T, db *sql.DB, table, column string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT coalesce(` + column + `::text, 'NULL') FROM ` + table)
	if err != nil {
		t.Fatalf("select %s.%s: %v", table, column, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan %s.%s: %v", table, column, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s.%s: %v", table, column, err)
	}
	return out
}
