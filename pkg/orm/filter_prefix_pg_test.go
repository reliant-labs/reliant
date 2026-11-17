package orm

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"
)

// TestWherePrefix_MatchesLiterally runs the prefix and range filters a
// generated List op applies against a real postgres: a prefix holding LIKE's
// own wildcards matches them literally, and a time bound compares as time.
func TestWherePrefix_MatchesLiterally(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()
	if _, err := c.Exec(ctx, `INSERT INTO tx_notes (id, body) VALUES
		('a', 'weekly.signups'), ('b', 'weekly_signups'), ('c', 'weeklyXsignups'),
		('d', '50% off'), ('e', '500 off'), ('f', 'C:\dir\x')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	ids := func(t *testing.T, opts ...QueryOption) []string {
		t.Helper()
		q := c.Bun().NewSelect().Table("tx_notes").Column("id")
		for _, o := range opts {
			o(q)
		}
		var got []string
		if err := q.Scan(ctx, &got); err != nil {
			t.Fatalf("select: %v", err)
		}
		sort.Strings(got)
		return got
	}
	for _, tc := range []struct {
		prefix string
		want   []string
	}{
		{"weekly.", []string{"a"}},
		{"weekly_", []string{"b"}}, // `_` is not "any one character"
		{"50%", []string{"d"}},     // `%` is not "anything"
		{`C:\dir`, []string{"f"}},  // `\` is not an escape
		{"weekly", []string{"a", "b", "c"}},
		{"", []string{"a", "b", "c", "d", "e", "f"}},
	} {
		if got := ids(t, WherePrefix("body", tc.prefix)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("WherePrefix(%q) = %v, want %v", tc.prefix, got, tc.want)
		}
	}

	if _, err := c.Exec(ctx, `CREATE TABLE prefix_events (id TEXT PRIMARY KEY, at TIMESTAMPTZ NOT NULL)`); err != nil {
		t.Fatalf("create prefix_events: %v", err)
	}
	if _, err := c.Exec(ctx, `INSERT INTO prefix_events VALUES
		('old', '2026-10-01T00:00:00Z'), ('edge', '2026-11-01T00:00:00Z'), ('new', '2026-11-04T12:00:00Z')`); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	var got []string
	q := c.Bun().NewSelect().Table("prefix_events").Column("id").Order("id")
	WhereGte("at", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))(q)
	WhereLt("at", time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))(q)
	if err := q.Scan(ctx, &got); err != nil {
		t.Fatalf("select events: %v", err)
	}
	if want := []string{"edge", "new"}; !reflect.DeepEqual(got, want) {
		t.Errorf("time range = %v, want %v", got, want)
	}
}
