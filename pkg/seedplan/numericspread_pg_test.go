package seedplan

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// docvaultMigration is the shape the third dogfood run seeded: a file-size
// column whose declared range is three orders of magnitude wider than the
// numeric-range expansion cap, a quota column wider still, and a view-count
// column narrow enough to fit under it.
//
// documents also carries the two things the previous fix was about, so this
// test measures the spread WITHOUT relaxing what that fix established: a
// GENERATED column reading size_bytes, and an ordering CHECK pairing
// scanned_at with quarantined_at. If a spread-seeking change breaks either,
// postgres rejects the INSERT and the whole seed rolls back — which is the
// worse outcome and the reason the assertions below are on real rows.
const docvaultMigration = `
CREATE TABLE organizations (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    storage_quota_bytes BIGINT NOT NULL
);

CREATE TABLE documents (
    id TEXT PRIMARY KEY,
    org_id TEXT NOT NULL REFERENCES organizations(id),
    title TEXT NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes > 0),
    size_mb DOUBLE PRECISION
        GENERATED ALWAYS AS (size_bytes::double precision / 1048576.0) STORED NOT NULL,
    scanned_at TIMESTAMPTZ,
    quarantined_at TIMESTAMPTZ,
    CONSTRAINT documents_scan_window
        CHECK (scanned_at IS NULL OR quarantined_at IS NULL OR quarantined_at > scanned_at)
);

CREATE TABLE share_links (
    id TEXT PRIMARY KEY,
    document_id TEXT NOT NULL REFERENCES documents(id),
    view_count INTEGER NOT NULL CHECK (view_count >= 0)
);
`

// docvaultVocab is the operator's overlay verbatim from the run: two ranges
// far wider than the expansion cap and one narrower than it.
const docvaultVocab = `
columns:
  documents.size_bytes: {min: 20480, max: 52428800}
  organizations.storage_quota_bytes: {min: 1073741824, max: 107374182400}
  share_links.view_count: {min: 0, max: 480}
`

// TestSeededNumericRanges_SpanTheDeclaredRange measures the OBSERVED span of
// each seeded column as a fraction of the span its vocab.yaml declared.
//
// This is the assertion the dogfood run makes by hand. Before the fix, all 20
// documents.size_bytes values landed in [20481, 20987] — a 506-wide observed
// span against a 52,408,320-wide declaration, 0.001% — so every document was
// ~20KB, size_mb rendered 0.02 for every row, and any UI that buckets or sorts
// by size was testing nothing. The rows were schema-valid throughout, which is
// why nothing warned and why a min/max assertion alone would have passed.
//
// share_links.view_count is the CONTROL: it was already correct, because its
// 481 values fit under the cap. Asserting both in one test is what stops a
// future change from spreading one by truncating the other.
func TestSeededNumericRanges_SpanTheDeclaredRange(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, migDir := setupDocvaultDB(t)

	const rows = 20
	if _, err := Materialize(ctx, db, migDir, "", Config{Rows: rows, Salt: 1}); err != nil {
		t.Fatalf("Materialize (a CHECK violation surfaces here): %v", err)
	}
	assertCount(t, db, "organizations", rows)
	assertCount(t, db, "documents", rows)
	assertCount(t, db, "share_links", rows)

	for _, tc := range []struct {
		table, column string
		min, max      float64
	}{
		{"documents", "size_bytes", 20480, 52428800},
		{"organizations", "storage_quota_bytes", 1073741824, 107374182400},
		{"share_links", "view_count", 0, 480},
	} {
		t.Run(tc.table+"."+tc.column, func(t *testing.T) {
			var lo, hi float64
			q := `SELECT min(` + tc.column + `)::double precision, ` +
				`max(` + tc.column + `)::double precision FROM ` + tc.table
			if err := db.QueryRow(q).Scan(&lo, &hi); err != nil {
				t.Fatal(err)
			}
			t.Logf("%s.%s seeded [%.0f, %.0f]; declared [%.0f, %.0f]",
				tc.table, tc.column, lo, hi, tc.min, tc.max)

			// The guarantee the previous fix established: nothing escapes the
			// declaration. A spread that violates this is a worse outcome than
			// clustering, since a rejected INSERT rolls back the whole seed.
			if lo < tc.min || hi > tc.max {
				t.Errorf("seeded [%.0f,%.0f] escapes the declared range [%.0f,%.0f]",
					lo, hi, tc.min, tc.max)
			}
			// The guarantee this fix adds: the values USE the declaration.
			// 20 rows drawn from a strided pool cannot be expected to hit both
			// endpoints, so the bar is a third of the declared span — which
			// the pre-fix 0.001% misses by four orders of magnitude.
			declared, observed := tc.max-tc.min, hi-lo
			if frac := observed / declared; frac < 0.33 {
				t.Errorf("observed span %.0f of declared span %.0f = %.4f%% — want >=33%%; values cluster at the floor",
					observed, declared, frac*100)
			}
		})
	}

	// The ordering CHECK and the generated column still hold — the previous
	// fix's guarantees, re-proven on the same rows rather than assumed.
	var bad int
	if err := db.QueryRow(`SELECT count(*) FROM documents
        WHERE scanned_at IS NOT NULL AND quarantined_at IS NOT NULL
          AND quarantined_at <= scanned_at`).Scan(&bad); err != nil {
		t.Fatal(err)
	}
	if bad != 0 {
		t.Errorf("%d document(s) violate documents_scan_window", bad)
	}
	// size_mb tracks size_bytes, so a spread in the source column is a spread
	// in the derived one — the thing the run reported as "0.02 for all 20".
	var distinctMB int
	if err := db.QueryRow(`SELECT count(DISTINCT round(size_mb::numeric, 2)) FROM documents`).Scan(&distinctMB); err != nil {
		t.Fatal(err)
	}
	if distinctMB < 10 {
		t.Errorf("documents.size_mb has only %d distinct values across %d rows — the derived column is as flat as the source was", distinctMB, rows)
	}
}

// setupDocvaultDB applies the migration to a pgtest database and lays out the
// project-shaped tree (db/migrations + db/seeds) the vocab overlay resolves
// against in a real project.
func setupDocvaultDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(docvaultMigration); err != nil {
		t.Fatalf("apply docvault migration to target: %v", err)
	}
	base := t.TempDir()
	migDir := filepath.Join(base, "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migDir, "00001_init.up.sql"), []byte(docvaultMigration), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "seeds"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(VocabPath(migDir), []byte(docvaultVocab), 0o644); err != nil {
		t.Fatal(err)
	}
	return db, migDir
}
