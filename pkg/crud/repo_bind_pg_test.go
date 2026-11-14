package crud

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // the driver production opens its pool with
	"github.com/uptrace/bun"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

// newPgxRepoTestDB is newRepoTestDB on pgx with ONE pooled connection, so
// pg_prepared_statements — which is per session — sees every statement the
// repo prepares. pgx's default mode (QueryExecModeCacheStatement) is what
// serverkit.OpenDB runs; its cache is raised from 512 so that the count is
// every statement ever prepared, not a full cache's worth.
func newPgxRepoTestDB(tb testing.TB) (*orm.Client, *sql.DB) {
	tb.Helper()
	if testing.Short() {
		tb.Skip("repo pg tests boot embedded postgres; skipped under -short")
	}
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		tb.Fatalf("pgtest.NewURL: %v", err)
	}
	tb.Cleanup(cleanup)
	return openPgxSession(tb, dsn)
}

// openPgxSession opens a one-connection pgx pool on dsn: a fresh session
// with an empty statement cache.
func openPgxSession(tb testing.TB, dsn string) (*orm.Client, *sql.DB) {
	tb.Helper()
	sqldb, err := sql.Open("pgx", dsn+"&statement_cache_capacity=100000")
	if err != nil {
		tb.Fatalf("open pgx: %v", err)
	}
	sqldb.SetMaxOpenConns(1)
	tb.Cleanup(func() { _ = sqldb.Close() })
	client, err := orm.NewClientWithDB(sqldb, "postgres")
	if err != nil {
		tb.Fatalf("orm.NewClientWithDB: %v", err)
	}
	return client, sqldb
}

// preparedStatements counts the statements prepared on the pool's one
// session. The counting query is itself prepared once, on first use.
func preparedStatements(tb testing.TB, sqldb *sql.DB) int {
	tb.Helper()
	var n int
	if err := sqldb.QueryRow(`SELECT count(*) FROM pg_prepared_statements`).Scan(&n); err != nil {
		tb.Fatalf("count prepared statements: %v", err)
	}
	return n
}

// The by-PK statements bind their values, so their text — and the prepared
// statement pgx caches for it — is one per statement shape, however many
// distinct ids and values pass through. bun interpolated every value into
// the text, so each distinct id or value was a new prepared statement: an
// extra Parse/Describe round trip, and churn through the connection's
// 512-entry cache.
func TestRepoByPK_StatementTextIsStableAcrossValues(t *testing.T) {
	db, sqldb := newPgxRepoTestDB(t)
	ctx := context.Background()
	createWidgetsTable(t, db)
	repo := NewRepo[widget]()

	const n = 20
	ids := make([]int64, n)
	for i := range ids {
		w := &widget{Name: fmt.Sprintf("w%d", i)}
		if err := repo.Create(ctx, db, w); err != nil {
			t.Fatalf("Create: %v", err)
		}
		ids[i] = w.ID
	}

	preparedStatements(t, sqldb) // prepare the counting query itself
	before := preparedStatements(t, sqldb)
	for i, id := range ids {
		w, err := repo.Get(ctx, db, id)
		if err != nil {
			t.Fatalf("Get(%d): %v", id, err)
		}
		w.Name = fmt.Sprintf("renamed-%d", i)
		if err := repo.UpdateMasked(ctx, db, w, []string{"name"}); err != nil {
			t.Fatalf("UpdateMasked(%d): %v", id, err)
		}
		w.Tags = []string{fmt.Sprintf("t%d", i)}
		if err := repo.Update(ctx, db, w); err != nil {
			t.Fatalf("Update(%d): %v", id, err)
		}
		if err := repo.Delete(ctx, db, id); err != nil {
			t.Fatalf("Delete(%d): %v", id, err)
		}
	}
	// One shape each for Get, UpdateMasked{name}, Update and Delete.
	if got := preparedStatements(t, sqldb) - before; got > 4 {
		t.Fatalf("%d by-PK calls over %d distinct ids prepared %d statements, want at most 4 (one per shape)", 4*n, n, got)
	}

	for _, id := range ids {
		if _, err := repo.Get(ctx, db, id); !errors.Is(err, orm.ErrNoRows) {
			t.Fatalf("Get(%d) after Delete: %v, want orm.ErrNoRows", id, err)
		}
	}
	if err := repo.Delete(ctx, db, ids[0]); !errors.Is(err, orm.ErrNoRows) {
		t.Fatalf("second Delete: %v, want orm.ErrNoRows", err)
	}
}

// bindProbe carries one column of each shape whose literal form bun owns, so
// the bind path is checked against bun's own interpolation for each.
type bindProbe struct {
	bun.BaseModel `bun:"table:bind_probes,alias:bind_probes"`

	ID      string     `bun:"id,pk"`
	Name    string     `bun:"name,notnull"`
	Count   int64      `bun:"count,notnull"`
	Ratio   float64    `bun:"ratio,notnull"`
	Flag    bool       `bun:"flag,notnull"`
	At      time.Time  `bun:"at,notnull"`
	Payload string     `bun:"payload,notnull"`
	Tags    []string   `bun:"tags,array,notnull"`
	Blob    []byte     `bun:"blob,notnull"`
	Note    *string    `bun:"note"`
	Seen    *time.Time `bun:"seen"`
	Version int64      `bun:"version,notnull" forge:"version"`
}

func TestRepoByPK_BoundValuesRoundTripLikeBunLiterals(t *testing.T) {
	db, _ := newPgxRepoTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `CREATE TABLE bind_probes (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, count BIGINT NOT NULL,
		ratio DOUBLE PRECISION NOT NULL, flag BOOLEAN NOT NULL,
		at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, tags TEXT[] NOT NULL,
		blob BYTEA NOT NULL, note TEXT DEFAULT 'none', seen TIMESTAMPTZ,
		version BIGINT NOT NULL DEFAULT 0)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	repo := NewRepo[bindProbe]()

	// An id with a quote and a question mark: the PK itself is bound.
	const id = `it's ?an \id`
	at := time.Date(2026, 10, 19, 12, 30, 45, 123456000, time.UTC)
	p := &bindProbe{ID: id, Name: "a", Payload: `{}`, Tags: []string{}, Blob: []byte{}, At: at}
	if err := repo.Create(ctx, db, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	note := "it''s \\ a ? note 'quoted' — ünïcode"
	seen := at.Add(time.Hour)
	p.Name = `O'Brien ?0 $1 \\ "x"`
	p.Count = -42
	p.Ratio = 0.1 + 0.2
	p.Flag = true
	p.Payload = `{"q": "it's", "n": [1, 2.5, null], "?": true}`
	p.Tags = []string{"a,b", `c"d`, "e'f", "", "NULL", `g\h`, "{i}"}
	p.Blob = []byte{0x00, 0x5c, 0x78, 0xff, '\''}
	p.Note = &note
	p.Seen = &seen
	if err := repo.Update(ctx, db, p); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if p.Version != 1 {
		t.Fatalf("version = %d after one Update, want 1", p.Version)
	}

	got, err := repo.Get(ctx, db, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// The same row read through bun's interpolating builder.
	var viaBun bindProbe
	if err := db.Bun().NewSelect().Model(&viaBun).Where("id = ?", id).Scan(ctx); err != nil {
		t.Fatalf("bun select: %v", err)
	}
	for _, r := range []*bindProbe{got, &viaBun} {
		switch {
		case r.Name != p.Name, r.Count != p.Count, r.Ratio != p.Ratio, r.Flag != p.Flag,
			!r.At.Equal(at), !slices.Equal(r.Tags, p.Tags), !slices.Equal(r.Blob, p.Blob),
			r.Note == nil || *r.Note != note, r.Seen == nil || !r.Seen.Equal(seen), r.Version != 1:
			t.Errorf("stored row differs from what was written:\n got  %+v\n want %+v", r, p)
		}
		var gotJSON, wantJSON string
		if err := db.QueryRow(ctx, `SELECT $1::text::jsonb::text, $2::text::jsonb::text`, r.Payload, p.Payload).Scan(&gotJSON, &wantJSON); err != nil || gotJSON != wantJSON {
			t.Errorf("payload = %s, want %s (%v)", gotJSON, wantJSON, err)
		}
	}

	// A masked write of a nil pointer is DEFAULT, as bun writes it on
	// postgres; of a nil NOT NULL slice, the empty value. A stale version is
	// refused.
	p.Note, p.Blob = nil, nil
	if err := repo.UpdateMasked(ctx, db, p, []string{"note", "blob"}); err != nil {
		t.Fatalf("UpdateMasked: %v", err)
	}
	got, err = repo.Get(ctx, db, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Note == nil || *got.Note != "none" || len(got.Blob) != 0 || got.Version != 2 {
		t.Errorf("after masked clear: note=%v blob=%v version=%d, want the 'none' default, empty, 2", got.Note, got.Blob, got.Version)
	}
	stale := *got
	stale.Version = 1
	if err := repo.UpdateMasked(ctx, db, &stale, []string{"name"}); err == nil {
		t.Error("UpdateMasked with a stale version succeeded")
	}
}

// A model that implements a bun model hook keeps running through bun's own
// executor, which is what calls the hook.
type hookedWidget struct {
	bun.BaseModel `bun:"table:hooked_widgets,alias:hooked_widgets"`

	ID   int64  `bun:"id,pk,autoincrement"`
	Name string `bun:"name,notnull"`
}

var hookedUpdates int

func (*hookedWidget) BeforeUpdate(context.Context, *bun.UpdateQuery) error {
	hookedUpdates++
	return nil
}

func TestRepoByPK_ModelHooksStayOnBun(t *testing.T) {
	db, _ := newPgxRepoTestDB(t)
	ctx := context.Background()
	if _, err := db.Bun().NewCreateTable().Model((*hookedWidget)(nil)).Exec(ctx); err != nil {
		t.Fatalf("create: %v", err)
	}
	repo := NewRepo[hookedWidget]()
	w := &hookedWidget{Name: "a"}
	if err := repo.Create(ctx, db, w); err != nil {
		t.Fatalf("Create: %v", err)
	}
	hookedUpdates = 0
	w.Name = "b"
	if err := repo.UpdateMasked(ctx, db, w, []string{"name"}); err != nil {
		t.Fatalf("UpdateMasked: %v", err)
	}
	if hookedUpdates != 1 {
		t.Fatalf("BeforeUpdate ran %d times, want 1", hookedUpdates)
	}
}

// BenchmarkRepoByPK is the P5b spike's shape on forge's own repository:
// allocations, latency, and the statements prepared per call on a pgx pool
// in its default (statement-cache) mode. Before bind parameters, every call
// with a new value prepared a new statement (stmts/op ≈ 1).
func BenchmarkRepoByPK(b *testing.B) {
	if testing.Short() {
		b.Skip("repo pg benchmarks boot embedded postgres; skipped under -short")
	}
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		b.Fatalf("pgtest.NewURL: %v", err)
	}
	b.Cleanup(cleanup)
	db, _ := openPgxSession(b, dsn)
	ctx := context.Background()
	if _, err := db.Bun().NewCreateTable().Model((*widget)(nil)).Exec(ctx); err != nil {
		b.Fatalf("create widgets: %v", err)
	}
	repo := NewRepo[widget]()
	const rows = 64
	ids := make([]int64, rows)
	for i := range ids {
		w := &widget{Name: fmt.Sprintf("w%d", i), Tags: []string{"x"}}
		if err := repo.Create(ctx, db, w); err != nil {
			b.Fatalf("Create: %v", err)
		}
		ids[i] = w.ID
	}

	// Each sub-benchmark runs on a fresh session, so stmts/op counts the
	// statements IT prepared: ~1/N when the text is stable, ~1 when every
	// call's text is new.
	run := func(b *testing.B, op func(db *orm.Client, i int) error) {
		db, sqldb := openPgxSession(b, dsn)
		preparedStatements(b, sqldb)
		before := preparedStatements(b, sqldb)
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			if err := op(db, i); err != nil {
				b.Fatal(err)
			}
			i++
		}
		b.ReportMetric(float64(preparedStatements(b, sqldb)-before)/float64(i), "stmts/op")
	}
	b.Run("Get", func(b *testing.B) {
		run(b, func(db *orm.Client, i int) error {
			_, err := repo.Get(ctx, db, ids[i%rows])
			return err
		})
	})
	b.Run("UpdateMasked-stamp", func(b *testing.B) {
		w, err := repo.Get(ctx, db, ids[0])
		if err != nil {
			b.Fatal(err)
		}
		run(b, func(db *orm.Client, i int) error {
			w.Name = fmt.Sprintf("n%d", i)
			return repo.UpdateMasked(ctx, db, w, []string{"name"})
		})
	})
	b.Run("Update", func(b *testing.B) {
		w, err := repo.Get(ctx, db, ids[1])
		if err != nil {
			b.Fatal(err)
		}
		run(b, func(db *orm.Client, i int) error {
			w.Name = fmt.Sprintf("n%d", i)
			return repo.Update(ctx, db, w)
		})
	})
}
