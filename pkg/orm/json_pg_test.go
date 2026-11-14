package orm

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// TestJSONThroughPostgres drives the helpers through the real database/sql
// driver (pgx) against a jsonb column — the same path the generated
// scan/insert code uses — so the "driver.Valuer in, sql.Scanner out" contract
// is pinned against what the driver actually hands back, not just direct
// calls. pgx hands json/jsonb (a column or a `->` projection) back as []byte
// and the same document selected as text as string; both must scan.
func TestJSONThroughPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("boots postgres; skipped under -short")
	}
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `CREATE TABLE bookmarks (id TEXT PRIMARY KEY, tags JSONB, tags_json JSON)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	tags := []string{"go", "orm"}
	if _, err := db.ExecContext(ctx, `INSERT INTO bookmarks (id, tags, tags_json) VALUES ($1, $2, $3)`, "b1", JSON(tags), JSON(tags)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	for _, column := range []string{"tags", "tags_json", `jsonb_build_object('t', tags) -> 't'`, "tags::text"} {
		var got []string
		if err := db.QueryRowContext(ctx, `SELECT `+column+` FROM bookmarks WHERE id = $1`, "b1").Scan(ScanJSON(&got)); err != nil {
			t.Fatalf("scan %s: %v", column, err)
		}
		if !reflect.DeepEqual(got, tags) {
			t.Errorf("round trip through %s = %#v, want %#v", column, got, tags)
		}
	}

	// NULL column → zero value.
	if _, err := db.ExecContext(ctx, `INSERT INTO bookmarks (id, tags) VALUES ($1, $2)`, "b2", JSON([]string(nil))); err != nil {
		t.Fatalf("insert nil: %v", err)
	}
	var isNull bool
	if err := db.QueryRowContext(ctx, `SELECT tags IS NULL FROM bookmarks WHERE id = $1`, "b2").Scan(&isNull); err != nil {
		t.Fatalf("check NULL: %v", err)
	}
	if !isNull {
		t.Error("JSON(nil slice) stored a JSON value, want SQL NULL")
	}
	var empty []string
	if err := db.QueryRowContext(ctx, `SELECT tags FROM bookmarks WHERE id = $1`, "b2").Scan(ScanJSON(&empty)); err != nil {
		t.Fatalf("scan nil: %v", err)
	}
	if empty != nil {
		t.Errorf("NULL tags should scan to nil, got %#v", empty)
	}
}

// TestNullTimeThroughPostgres pins what the pgx driver hands NullTime: a
// timestamptz column arrives as time.Time, and the same instant selected as
// text arrives in postgres's own output format, which NullTime must parse.
func TestNullTimeThroughPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("boots postgres; skipped under -short")
	}
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	ctx := context.Background()
	// One connection, so each case's session TimeZone (which decides the
	// offset in the text form) applies to its query.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	const lit = `'2026-06-11 14:30:05.25+00'::timestamptz`
	for _, tc := range []struct {
		name, expr string
		zone       string
	}{
		{"timestamptz", lit, "UTC"},
		{"timestamptz as text", lit + `::text`, "UTC"},
		{"timestamptz as text, half-hour offset", lit + `::text`, "Asia/Kolkata"},
		{"NULL", `NULL::timestamptz`, "UTC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := conn.ExecContext(ctx, `SELECT set_config('TimeZone', $1, false)`, tc.zone); err != nil {
				t.Fatalf("set zone: %v", err)
			}
			var n NullTime
			if err := conn.QueryRowContext(ctx, `SELECT `+tc.expr).Scan(&n); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if tc.expr == `NULL::timestamptz` {
				if n.Valid {
					t.Fatalf("NULL scanned as valid: %v", n.Time)
				}
				return
			}
			want := time.Date(2026, 6, 11, 14, 30, 5, 250_000_000, time.UTC)
			if !n.Valid || !n.Time.Equal(want) {
				t.Fatalf("scanned %v (valid=%v), want %v", n.Time, n.Valid, want)
			}
		})
	}
}
