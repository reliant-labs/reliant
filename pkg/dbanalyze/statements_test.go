package dbanalyze

import (
	"slices"
	"testing"
)

func TestStaticLocksNameTheRelationAsPostgresDoes(t *testing.T) {
	cases := map[string]string{
		"CREATE INDEX CONCURRENTLY i ON Orders (a)":                        "orders",
		"CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS i ON ONLY s.T (a)": "s.t",
		`CREATE INDEX CONCURRENTLY i ON public."Mixed" USING btree (a)`:    "Mixed",
		"DROP INDEX CONCURRENTLY IF EXISTS orders_a_idx":                   "orders_a_idx",
		"REINDEX INDEX CONCURRENTLY orders_pkey":                           "orders_pkey",
	}
	for sql, want := range cases {
		got := SplitStatements(sql)[0].staticLocks()
		wantLocks := []Lock{{Relation: want, Mode: "ShareUpdateExclusiveLock", Static: true}}
		if !slices.Equal(got, wantLocks) {
			t.Errorf("%s: locks = %+v, want %+v", sql, got, wantLocks)
		}
	}
	if got := SplitStatements("VACUUM orders")[0].staticLocks(); got != nil {
		t.Errorf("VACUUM: %+v, want none", got)
	}
}
