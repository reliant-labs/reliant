package orm

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// forge projects an array column onto a native Go slice on the generated
// entity struct and lets Bun's `,array` tag bind and scan it. Until this
// test, only TEXT[] and BIGINT[] were ever projected that way — every other
// array column collapsed to []string in the generator, so nothing had ever
// asked whether Bun can actually move a BOOLEAN[], a DOUBLE PRECISION[], a
// BYTEA[] or a TIMESTAMPTZ[].
//
// A unit test over the generator's type table cannot answer that. The
// generator can be perfectly self-consistent and still emit a struct the
// driver cannot bind, which is the failure the projection fix would
// otherwise trade for the one it removes. So this goes to a real postgres:
// declare the columns, insert through Bun with the tags the generator
// emits, read back, compare byte for byte.

// arrayRow carries one column per array shape entity birth can emit, tagged
// exactly as internal/generator's writeEntityStruct tags them.
type arrayRow struct {
	bun.BaseModel `bun:"table:array_rows,alias:array_rows"`

	ID     string      `bun:"id,pk"`
	Tags   []string    `bun:"tags,array,notnull"`
	Counts []int64     `bun:"counts,array,notnull"`
	Flags  []bool      `bun:"flags,array,notnull"`
	Scores []float64   `bun:"scores,array,notnull"`
	Chunks [][]byte    `bun:"chunks,array,notnull"`
	Marks  []time.Time `bun:"marks,array,notnull"`
}

// arrayRowDDL is the column set entity birth emits for
// `repeated <scalar>` — `<scalar SQL type>[] NOT NULL DEFAULT '{}'`.
const arrayRowDDL = `CREATE TABLE array_rows (
	id TEXT PRIMARY KEY,
	tags TEXT[] NOT NULL DEFAULT '{}',
	counts BIGINT[] NOT NULL DEFAULT '{}',
	flags BOOLEAN[] NOT NULL DEFAULT '{}',
	scores DOUBLE PRECISION[] NOT NULL DEFAULT '{}',
	chunks BYTEA[] NOT NULL DEFAULT '{}',
	marks TIMESTAMPTZ[] NOT NULL DEFAULT '{}'
)`

func TestArrayColumns_RoundTripThroughBunAndPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("boots postgres; skipped under -short")
	}
	sqldb, cleanup, err := pgtest.New()
	if err != nil {
		t.Skipf("no reachable postgres (%v) — this test is the only proof Bun can bind the "+
			"array column types forge projects to native slices; do not leave it skipped in CI", err)
	}
	t.Cleanup(cleanup)
	client, err := NewClientWithDB(sqldb, "postgres")
	if err != nil {
		t.Fatalf("NewClientWithDB: %v", err)
	}
	ctx := context.Background()
	if _, err := client.Bun().ExecContext(ctx, arrayRowDDL); err != nil {
		t.Fatalf("create array_rows: %v", err)
	}

	// Values chosen to catch the encodings that go wrong quietly: a false
	// between two trues (a text-array parser that drops empties reorders
	// them), a negative and a zero float, an EMPTY byte slice next to one
	// holding 0x00 and 0xff (the bytea escape/hex boundary), and sub-second
	// precision on the instants.
	want := &arrayRow{
		ID:     "r1",
		Tags:   []string{"alpha", "b,c", `d"e`, ""},
		Counts: []int64{-9223372036854775808, 0, 9223372036854775807},
		Flags:  []bool{true, false, true},
		Scores: []float64{1.5, -2.25, 0},
		Chunks: [][]byte{{0x01, 0x02}, {}, {0xff, 0x00, 0x7f}},
		Marks: []time.Time{
			time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC),
			time.Date(2020, 1, 1, 0, 0, 0, 123456000, time.UTC),
		},
	}
	if _, err := client.Bun().NewInsert().Model(want).Exec(ctx); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got := new(arrayRow)
	if err := client.Bun().NewSelect().Model(got).Where("id = ?", "r1").Scan(ctx); err != nil {
		t.Fatalf("select: %v", err)
	}

	assertSlice(t, "tags", got.Tags, want.Tags)
	assertSlice(t, "counts", got.Counts, want.Counts)
	assertSlice(t, "flags", got.Flags, want.Flags)
	assertSlice(t, "scores", got.Scores, want.Scores)

	if len(got.Chunks) != len(want.Chunks) {
		t.Fatalf("chunks: got %d elements, want %d (%#v)", len(got.Chunks), len(want.Chunks), got.Chunks)
	}
	for i := range want.Chunks {
		if !bytes.Equal(got.Chunks[i], want.Chunks[i]) {
			t.Errorf("chunks[%d] = %#v, want %#v", i, got.Chunks[i], want.Chunks[i])
		}
	}

	if len(got.Marks) != len(want.Marks) {
		t.Fatalf("marks: got %d elements, want %d (%#v)", len(got.Marks), len(want.Marks), got.Marks)
	}
	for i := range want.Marks {
		// TIMESTAMPTZ stores microseconds and normalizes to UTC on read.
		if !got.Marks[i].UTC().Equal(want.Marks[i]) {
			t.Errorf("marks[%d] = %s, want %s", i, got.Marks[i].UTC(), want.Marks[i])
		}
	}

	// A NIL slice binds as SQL NULL — Bun does not invent an empty array —
	// so every one of these NOT NULL columns rejects an unset field. That
	// is not a defect in the projection; it is why forge/pkg/crud's Repo
	// nil-normalizes slice columns before writing (see
	// TestRepoCreate_NormalizesNilSliceColumns). Pinned here so the
	// normalization can never be mistaken for redundant.
	if _, err := client.Bun().NewInsert().Model(&arrayRow{ID: "r2"}).Exec(ctx); err == nil {
		t.Error("a nil slice bound successfully into a NOT NULL array column — if Bun has started " +
			"normalizing nil slices itself, pkg/crud's normalizeArrays is now dead weight and should go")
	}
}

func assertSlice[T comparable](t *testing.T, name string, got, want []T) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: got %d elements %v, want %d %v", name, len(got), got, len(want), want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %v, want %v", name, i, got[i], want[i])
		}
	}
}
