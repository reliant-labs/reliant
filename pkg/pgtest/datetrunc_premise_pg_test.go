package pgtest_test

import (
	"fmt"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// TestDateTruncSessionTimezonePremise is a PROBE, not a product test. It
// exists to confirm, against a real postgres, the mechanism a lint rule is
// about to be built on: that two-argument date_trunc over a TIMESTAMPTZ is
// session-timezone dependent, that the three-argument form is not, and that
// a TIMESTAMP WITHOUT TIME ZONE column is not.
func TestDateTruncSessionTimezonePremise(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a real postgres")
	}
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	defer cleanup()

	if _, err := db.Exec(`CREATE TABLE samples (
		id text PRIMARY KEY,
		recorded_at TIMESTAMPTZ NOT NULL,
		naive_at TIMESTAMP NOT NULL,
		distance_m BIGINT NOT NULL
	)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Five samples spanning exactly two UTC calendar days, matching the
	// dogfood run's shape: one early on day 1, three on day 2, one late
	// on day 2 (past 05:00Z of day 3 in no sense — deliberately before).
	if _, err := db.Exec(`INSERT INTO samples (id, recorded_at, naive_at, distance_m) VALUES
		('a', '2026-03-01T06:00:00Z', '2026-03-01T06:00:00', 0),
		('b', '2026-03-02T01:00:00Z', '2026-03-02T01:00:00', 100000),
		('c', '2026-03-02T09:00:00Z', '2026-03-02T09:00:00', 200000),
		('d', '2026-03-02T15:00:00Z', '2026-03-02T15:00:00', 250000),
		('e', '2026-03-02T23:00:00Z', '2026-03-02T23:00:00', 150000)`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	count := func(t *testing.T, query string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(query).Scan(&n); err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		return n
	}

	twoArgTZ := `SELECT count(*) FROM (
		SELECT date_trunc('day', recorded_at) AS b FROM samples GROUP BY 1) s`
	threeArgTZ := `SELECT count(*) FROM (
		SELECT date_trunc('day', recorded_at, 'UTC') AS b FROM samples GROUP BY 1) s`
	twoArgNaive := `SELECT count(*) FROM (
		SELECT date_trunc('day', naive_at) AS b FROM samples GROUP BY 1) s`

	for _, zone := range []string{"UTC", "America/New_York", "Asia/Tokyo"} {
		t.Run(zone, func(t *testing.T) {
			if _, err := db.Exec(fmt.Sprintf("SET TIME ZONE %q", zone)); err != nil {
				t.Fatalf("set time zone: %v", err)
			}
			t.Logf("zone=%s two-arg TIMESTAMPTZ buckets=%d three-arg=%d two-arg TIMESTAMP=%d",
				zone, count(t, twoArgTZ), count(t, threeArgTZ), count(t, twoArgNaive))

			// Dump the buckets so the boundary is visible, not inferred.
			rows, err := db.Query(`SELECT date_trunc('day', recorded_at) AT TIME ZONE 'UTC',
				sum(distance_m), count(*) FROM samples GROUP BY 1 ORDER BY 1`)
			if err != nil {
				t.Fatalf("dump: %v", err)
			}
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var b, sum, n any
				if err := rows.Scan(&b, &sum, &n); err != nil {
					t.Fatalf("scan: %v", err)
				}
				t.Logf("  BUCKET %v dist=%v n=%v", b, sum, n)
			}
		})
	}
}
