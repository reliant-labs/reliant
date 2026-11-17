package orm

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/uptrace/bun"
)

// argRow has an array column and a jsonb column, the two shapes a Go slice
// can be meant for.
type argRow struct {
	bun.BaseModel `bun:"table:arg_rows,alias:arg_rows"`

	ID     string   `bun:"id,pk"`
	Seq    int64    `bun:"seq,notnull"`
	Tags   []string `bun:"tags,array,notnull"`
	Labels []string `bun:"labels,type:jsonb,notnull"`
}

// The rule bind.go documents, against a real postgres: a slice passed to Arg
// is an ARRAY wherever a `?` argument goes — a builder's Where, NewRaw, the
// WithWhere filters generated List ops are made of — and JSON is spelled
// JSON(v). Before, Arg sent a slice as bun does, as JSON text, which an
// array operand rejects ("malformed array literal").
func TestArg_SliceBindsAsAPostgresArray(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()
	if _, err := c.Exec(ctx, `CREATE TABLE arg_rows (
		id TEXT PRIMARY KEY, seq BIGINT NOT NULL, tags TEXT[] NOT NULL, labels JSONB NOT NULL)`); err != nil {
		t.Fatalf("create arg_rows: %v", err)
	}
	rows := []*argRow{
		{ID: "a", Seq: 1, Tags: []string{"red", "b,c"}, Labels: []string{"urgent"}},
		{ID: "b", Seq: 2, Tags: []string{"blue"}, Labels: []string{"later"}},
		{ID: "c", Seq: 3, Tags: []string{"red", "blue"}, Labels: []string{"urgent", "later"}},
	}
	if _, err := c.Bun().NewInsert().Model(&rows).Exec(ctx); err != nil {
		t.Fatalf("insert: %v", err)
	}

	ids := func(t *testing.T, q *bun.SelectQuery) []string {
		t.Helper()
		var got []argRow
		if err := q.Scan(ctx, &got); err != nil {
			t.Fatalf("select: %v", err)
		}
		out := make([]string, 0, len(got))
		for _, r := range got {
			out = append(out, r.ID)
		}
		sort.Strings(out)
		return out
	}
	sel := func() *bun.SelectQuery { return c.Bun().NewSelect().Model((*argRow)(nil)) }

	for _, tc := range []struct {
		name string
		q    *bun.SelectQuery
		want []string
	}{
		{"text = ANY(array)", sel().Where("id = ANY(?)", Arg([]string{"a", "c"})), []string{"a", "c"}},
		{"bigint = ANY(array)", sel().Where("seq = ANY(?)", Arg([]int64{2, 3})), []string{"b", "c"}},
		{"array containment", sel().Where("tags @> ?", Arg([]string{"red", "blue"})), []string{"c"}},
		{"an element with a comma", sel().Where("tags && ?", Arg([]string{"b,c"})), []string{"a"}},
		{"pointer to a slice", sel().Where("tags && ?", Arg(&[]string{"blue"})), []string{"b", "c"}},
		{"List filter on an array column", applyOpts(sel(), WhereEq("tags", []string{"blue"})), []string{"b"}},
		{"JSON is explicit", sel().Where("labels @> ?", JSON([]string{"urgent"})), []string{"a", "c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ids(t, tc.q); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("NewRaw", func(t *testing.T) {
		var got []string
		if err := c.Bun().NewRaw("SELECT id FROM arg_rows WHERE id = ANY(?) ORDER BY id", Arg([]string{"b", "c"})).
			Scan(ctx, &got); err != nil {
			t.Fatalf("raw: %v", err)
		}
		if want := []string{"b", "c"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("an array write through Set", func(t *testing.T) {
		if _, err := c.Bun().NewUpdate().Model((*argRow)(nil)).Set("tags = ?", Arg([]string{"green"})).
			Where("id = ?", "b").Exec(ctx); err != nil {
			t.Fatalf("update: %v", err)
		}
		var got argRow
		if err := c.Bun().NewSelect().Model(&got).Where("id = ?", "b").Scan(ctx); err != nil {
			t.Fatalf("read back: %v", err)
		}
		if want := []string{"green"}; !reflect.DeepEqual(got.Tags, want) {
			t.Fatalf("tags %v, want %v", got.Tags, want)
		}
	})
}
