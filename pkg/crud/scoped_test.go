package crud

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// The Scoped* helpers compose a PK operation with an ownership predicate.
// These tests drive them with an in-memory table so the contract is pinned
// without a database: what they read, what they write, and what they refuse.

type scopedRow struct {
	ID    string
	Owner string
}

// scopedTable fakes the generated delegates over a map. list applies only the
// two predicates the helpers build — the caller's owner filter, and the PK
// — by rendering the QueryOptions against a recording query.
type scopedTable struct {
	rows    map[string]scopedRow
	lists   int
	gets    []string
	deletes []string
	updates []scopedRow
	masked  [][]string
	failOn  string // a method name that returns errBoom
}

var errBoom = errors.New("boom")

func newScopedTable() *scopedTable {
	return &scopedTable{rows: map[string]scopedRow{
		"a": {ID: "a", Owner: "alice"},
		"b": {ID: "b", Owner: "bob"},
	}}
}

func (s *scopedTable) get(_ context.Context, id string) (scopedRow, error) {
	s.gets = append(s.gets, id)
	if s.failOn == "get" {
		return scopedRow{}, errBoom
	}
	return s.rows[id], nil
}

func (s *scopedTable) del(_ context.Context, id string) error {
	s.deletes = append(s.deletes, id)
	if s.failOn == "del" {
		return errBoom
	}
	delete(s.rows, id)
	return nil
}

func (s *scopedTable) update(_ context.Context, e scopedRow) error {
	s.updates = append(s.updates, e)
	if s.failOn == "update" {
		return errBoom
	}
	s.rows[e.ID] = e
	return nil
}

func (s *scopedTable) updateMasked(_ context.Context, e scopedRow, fields []string) error {
	s.masked = append(s.masked, fields)
	if s.failOn == "masked" {
		return errBoom
	}
	s.rows[e.ID] = e
	return nil
}

// list honours the options the helper builds: it renders them onto a bun
// query and reads the WHERE back, then filters the in-memory rows by the
// column predicates it finds.
func (s *scopedTable) list(_ context.Context, opts ...orm.QueryOption) ([]scopedRow, error) {
	s.lists++
	if s.failOn == "list" {
		return nil, errBoom
	}
	where := renderedWhere(opts)
	var out []scopedRow
	for _, id := range slices.Sorted(mapKeys(s.rows)) {
		r := s.rows[id]
		if matches(where, r) {
			out = append(out, r)
		}
	}
	return out, nil
}

func mapKeys(m map[string]scopedRow) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// renderedWhere applies opts to a throwaway bun select and returns its
// rendered text, which holds `"id" = 'a'` / `"owner" = 'alice'` fragments.
func renderedWhere(opts []orm.QueryOption) string {
	db := bun.NewDB(nil, pgdialect.New())
	q := db.NewSelect().TableExpr("t")
	for _, o := range opts {
		o(q)
	}
	return q.String()
}

// matches reports whether r satisfies every equality predicate the rendered
// query carries on a column it knows.
func matches(sql string, r scopedRow) bool {
	for _, p := range []struct{ col, val string }{{"id", r.ID}, {"owner", r.Owner}} {
		if strings.Contains(sql, `"`+p.col+`" = `) && !strings.Contains(sql, `"`+p.col+`" = '`+p.val+`'`) {
			return false
		}
	}
	return true
}

func ownedBy(owner string) []orm.QueryOption { return []orm.QueryOption{orm.WhereEq("owner", owner)} }

func TestScopedGet(t *testing.T) {
	ctx := context.Background()

	t.Run("no options is the plain PK delegate, verbatim", func(t *testing.T) {
		s := newScopedTable()
		got, err := ScopedGet(ctx, "id", "b", nil, s.get, s.list)
		if err != nil || got.Owner != "bob" || s.lists != 0 || !slices.Equal(s.gets, []string{"b"}) {
			t.Fatalf("got %+v, %v; lists=%d gets=%v", got, err, s.lists, s.gets)
		}
	})

	t.Run("the owner's row is read by one scoped SELECT", func(t *testing.T) {
		s := newScopedTable()
		got, err := ScopedGet(ctx, "id", "a", ownedBy("alice"), s.get, s.list)
		if err != nil || got.ID != "a" || s.lists != 1 || len(s.gets) != 0 {
			t.Fatalf("got %+v, %v; lists=%d gets=%v", got, err, s.lists, s.gets)
		}
	})

	t.Run("someone else's row is the same not-found an unknown id gets", func(t *testing.T) {
		s := newScopedTable()
		_, notMine := ScopedGet(ctx, "id", "b", ownedBy("alice"), s.get, s.list)
		_, missing := ScopedGet(ctx, "id", "zzz", ownedBy("alice"), s.get, s.list)
		for name, err := range map[string]error{"not mine": notMine, "missing": missing} {
			if !svcerr.IsNotFound(err) || !errors.Is(err, orm.ErrNoRows) {
				t.Errorf("%s: err = %v, want svcerr.NotFound that is also orm.ErrNoRows", name, err)
			}
		}
	})

	t.Run("the caller cannot displace the PK predicate with its own options", func(t *testing.T) {
		s := newScopedTable()
		// A limit and an unrelated filter, ahead of the helper's own.
		opts := []orm.QueryOption{orm.WithLimit(50), orm.WhereEq("owner", "alice")}
		got, err := ScopedGet(ctx, "id", "a", opts, s.get, s.list)
		if err != nil || got.ID != "a" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})

	t.Run("a failing read is returned, not turned into not-found", func(t *testing.T) {
		s := newScopedTable()
		s.failOn = "list"
		if _, err := ScopedGet(ctx, "id", "a", ownedBy("alice"), s.get, s.list); !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want the read error", err)
		}
	})
}

func TestScopedDelete(t *testing.T) {
	ctx := context.Background()

	t.Run("no options deletes without a scope check", func(t *testing.T) {
		s := newScopedTable()
		if err := ScopedDelete(ctx, "id", "b", nil, s.del, s.list); err != nil {
			t.Fatal(err)
		}
		if s.lists != 0 || !slices.Equal(s.deletes, []string{"b"}) {
			t.Fatalf("lists=%d deletes=%v", s.lists, s.deletes)
		}
	})

	t.Run("the owner deletes their row", func(t *testing.T) {
		s := newScopedTable()
		if err := ScopedDelete(ctx, "id", "a", ownedBy("alice"), s.del, s.list); err != nil {
			t.Fatal(err)
		}
		if _, still := s.rows["a"]; still {
			t.Fatal("the row survived")
		}
	})

	t.Run("a non-owner never reaches the delete", func(t *testing.T) {
		s := newScopedTable()
		err := ScopedDelete(ctx, "id", "b", ownedBy("alice"), s.del, s.list)
		if !svcerr.IsNotFound(err) {
			t.Fatalf("err = %v, want not-found", err)
		}
		if len(s.deletes) != 0 || s.rows["b"].Owner != "bob" {
			t.Fatalf("someone else's row was touched: deletes=%v rows=%v", s.deletes, s.rows)
		}
	})

	t.Run("the delete's own error is returned", func(t *testing.T) {
		s := newScopedTable()
		s.failOn = "del"
		if err := ScopedDelete(ctx, "id", "a", ownedBy("alice"), s.del, s.list); !errors.Is(err, errBoom) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestScopedUpdate(t *testing.T) {
	ctx := context.Background()

	t.Run("no options writes without a scope check", func(t *testing.T) {
		s := newScopedTable()
		if err := ScopedUpdate(ctx, "id", "b", scopedRow{ID: "b", Owner: "eve"}, nil, s.update, s.list); err != nil {
			t.Fatal(err)
		}
		if s.lists != 0 || s.rows["b"].Owner != "eve" {
			t.Fatalf("lists=%d rows=%v", s.lists, s.rows)
		}
	})

	t.Run("the owner writes their row", func(t *testing.T) {
		s := newScopedTable()
		if err := ScopedUpdate(ctx, "id", "a", scopedRow{ID: "a", Owner: "alice"}, ownedBy("alice"), s.update, s.list); err != nil {
			t.Fatal(err)
		}
		if len(s.updates) != 1 {
			t.Fatalf("updates = %v", s.updates)
		}
	})

	t.Run("the check reads the STORED row, not the submitted one", func(t *testing.T) {
		// Eve submits bob's row claiming to be its owner. The submission says
		// "eve"; the database says "bob". The scope is eve's, so it must refuse.
		s := newScopedTable()
		err := ScopedUpdate(ctx, "id", "b", scopedRow{ID: "b", Owner: "eve"}, ownedBy("eve"), s.update, s.list)
		if !svcerr.IsNotFound(err) {
			t.Fatalf("err = %v, want not-found", err)
		}
		if len(s.updates) != 0 || s.rows["b"].Owner != "bob" {
			t.Fatalf("eve took bob's row: updates=%v rows=%v", s.updates, s.rows)
		}
	})

	t.Run("the update's own error is returned", func(t *testing.T) {
		s := newScopedTable()
		s.failOn = "update"
		if err := ScopedUpdate(ctx, "id", "a", scopedRow{ID: "a"}, ownedBy("alice"), s.update, s.list); !errors.Is(err, errBoom) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestScopedUpdateMasked(t *testing.T) {
	ctx := context.Background()
	fields := []string{"owner"}

	t.Run("no options writes the mask without a scope check", func(t *testing.T) {
		s := newScopedTable()
		if err := ScopedUpdateMasked(ctx, "id", "a", scopedRow{ID: "a", Owner: "z"}, fields, nil, s.updateMasked, s.list); err != nil {
			t.Fatal(err)
		}
		if s.lists != 0 || len(s.masked) != 1 || !slices.Equal(s.masked[0], fields) {
			t.Fatalf("lists=%d masked=%v", s.lists, s.masked)
		}
	})

	t.Run("the owner's masked write passes the mask through", func(t *testing.T) {
		s := newScopedTable()
		if err := ScopedUpdateMasked(ctx, "id", "a", scopedRow{ID: "a", Owner: "alice"}, fields, ownedBy("alice"), s.updateMasked, s.list); err != nil {
			t.Fatal(err)
		}
		if len(s.masked) != 1 || !slices.Equal(s.masked[0], fields) {
			t.Fatalf("masked = %v", s.masked)
		}
	})

	t.Run("a non-owner's masked write never happens", func(t *testing.T) {
		s := newScopedTable()
		err := ScopedUpdateMasked(ctx, "id", "b", scopedRow{ID: "b"}, fields, ownedBy("alice"), s.updateMasked, s.list)
		if !svcerr.IsNotFound(err) || len(s.masked) != 0 {
			t.Fatalf("err = %v masked = %v", err, s.masked)
		}
	})

	t.Run("the update's own error is returned", func(t *testing.T) {
		s := newScopedTable()
		s.failOn = "masked"
		if err := ScopedUpdateMasked(ctx, "id", "a", scopedRow{ID: "a"}, fields, ownedBy("alice"), s.updateMasked, s.list); !errors.Is(err, errBoom) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestEntityLabelOf(t *testing.T) {
	if got := entityLabelOf[scopedRow](); got != "scopedrow" {
		t.Errorf("model: %q", got)
	}
	if got := entityLabelOf[*scopedRow](); got != "scopedrow" {
		t.Errorf("pointer to model: %q", got)
	}
}
