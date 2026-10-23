package crud

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

// sentStatements records every statement pgx sends to postgres, with its
// arguments: the text the server parses, which is what binding is about.
type sentStatements struct {
	mu    sync.Mutex
	stmts []sentStatement
}

type sentStatement struct {
	sql  string
	args []any
}

func (s *sentStatements) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stmts = append(s.stmts, sentStatement{sql: d.SQL, args: slices.Clone(d.Args)})
	return ctx
}

func (*sentStatements) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (s *sentStatements) take() []sentStatement {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.stmts
	s.stmts = nil
	return out
}

// newTracedRepoTestDB is a Client on a pgx pool that records what it sends.
func newTracedRepoTestDB(t *testing.T) (*orm.Client, *sentStatements) {
	t.Helper()
	if testing.Short() {
		t.Skip("repo pg tests boot embedded postgres; skipped under -short")
	}
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("pgtest.NewURL: %v", err)
	}
	t.Cleanup(cleanup)
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	sent := &sentStatements{}
	cfg.Tracer = sent
	sqldb := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = sqldb.Close() })
	client, err := orm.NewClientWithDB(sqldb, "postgres")
	if err != nil {
		t.Fatalf("orm.NewClientWithDB: %v", err)
	}
	return client, sent
}

// Every statement the ORM sends carries its values as bind parameters: the
// text postgres parses holds no value a caller supplied — no name, no
// timestamp, no integer passed through orm.Arg — so it is one text per
// statement shape.
func TestStatementsSentToPostgresCarryNoValues(t *testing.T) {
	db, sent := newTracedRepoTestDB(t)
	ctx := context.Background()
	createWidgetsTable(t, db)
	repo := NewRepo[widget]()

	const name, other, tag = "name-sentinel-q7", "name-sentinel-z3", "tag-sentinel-k9"
	const bigID = 987654321
	year := fmt.Sprintf("%d-", time.Now().UTC().Year())
	w := &widget{Name: name, Tags: []string{tag}}

	for _, tc := range []struct {
		op    string
		run   func() error
		value any // a value that must reach postgres as an argument
	}{
		{"Create", func() error { return repo.Create(ctx, db, w) }, name},
		{"List", func() error {
			_, err := repo.List(ctx, db, orm.WhereEq("name", name), orm.WhereGt("id", bigID-bigID), orm.WhereIn("name", []string{name, other}))
			return err
		}, name},
		{"Count", func() error { _, err := repo.Count(ctx, db, orm.WhereEq("name", name)); return err }, name},
		{"ListAll", func() error { _, err := repo.ListAll(ctx, db, orm.WhereEq("name", name)); return err }, name},
		{"Upsert", func() error { w.Name = other; return repo.Upsert(ctx, db, w) }, other},
		{"builder select", func() error {
			var rows []widget
			return db.Bun().NewSelect().Model(&rows).Where("name = ?", other).Where("id < ?", orm.Arg(bigID)).Scan(ctx)
		}, other},
		{"builder update", func() error {
			_, err := db.Bun().NewUpdate().Model((*widget)(nil)).Set("name = ?", name).Where("id = ?", orm.Arg(w.ID)).Exec(ctx)
			return err
		}, name},
		{"builder exists", func() error {
			_, err := db.Bun().NewSelect().Model((*widget)(nil)).Where("name = ?", name).Exists(ctx)
			return err
		}, name},
		{"NewRaw", func() error {
			var ids []int64
			return db.Bun().NewRaw("SELECT id FROM widgets WHERE name = ? AND id < ?", name, orm.Arg(bigID)).Scan(ctx, &ids)
		}, name},
		{"RunTx", func() error {
			return db.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
				_, err := repo.List(ctx, db, orm.WhereEq("name", other))
				return err
			})
		}, other},
		{"explicit Tx handle", func() error {
			return db.RunTransaction(ctx, orm.ReadCommitted, func(tx orm.Context) error {
				var rows []widget
				return tx.Bun().NewSelect().Model(&rows).Where("name = ?", other).Scan(ctx)
			})
		}, other},
	} {
		sent.take()
		if err := tc.run(); err != nil {
			t.Fatalf("%s: %v", tc.op, err)
		}
		stmts := sent.take()
		reached := false
		for _, s := range stmts {
			for _, leak := range []string{name, other, tag, fmt.Sprint(bigID), year} {
				if strings.Contains(s.sql, leak) {
					t.Errorf("%s: %q reached postgres in the statement text:\n%s", tc.op, leak, s.sql)
				}
			}
			for _, a := range s.args {
				reached = reached || a == tc.value
			}
		}
		if !reached {
			t.Errorf("%s: %v never reached postgres as an argument; sent %+v", tc.op, tc.value, stmts)
		}
	}
}

// The statement text is one per shape however many values pass through, so
// pgx prepares each shape once per session: Create, List, Count and a
// builder query each prepare a bounded number of statements over many
// distinct values. Inline values (orm.InlineValues) prepare one per call —
// the cost this removes.
func TestStatementTextIsStableAcrossValues(t *testing.T) {
	db, sqldb := newPgxRepoTestDB(t)
	createWidgetsTable(t, db)
	repo := NewRepo[widget]()
	const n = 20

	for _, tc := range []struct {
		op  string
		run func(ctx context.Context, i int) error
	}{
		{"Create", func(ctx context.Context, i int) error {
			return repo.Create(ctx, db, &widget{Name: fmt.Sprintf("w%d", i), Tags: []string{fmt.Sprint(i)}})
		}},
		{"List", func(ctx context.Context, i int) error {
			_, err := repo.List(ctx, db, orm.WhereEq("name", fmt.Sprintf("w%d", i)), orm.WhereGt("id", i))
			return err
		}},
		{"Count", func(ctx context.Context, i int) error {
			_, err := repo.Count(ctx, db, orm.WhereEq("name", fmt.Sprintf("w%d", i)))
			return err
		}},
		{"builder", func(ctx context.Context, i int) error {
			var rows []widget
			return db.Bun().NewSelect().Model(&rows).Where("name = ?", fmt.Sprintf("w%d", i)).Where("id > ?", orm.Arg(i)).Scan(ctx)
		}},
	} {
		prepared := func(ctx context.Context) int {
			before := preparedStatements(t, sqldb)
			for i := range n {
				if err := tc.run(ctx, i); err != nil {
					t.Fatalf("%s: %v", tc.op, err)
				}
			}
			return preparedStatements(t, sqldb) - before
		}
		preparedStatements(t, sqldb) // prepare the counting query itself
		if got := prepared(context.Background()); got > 2 {
			t.Errorf("%s: %d calls with distinct values prepared %d statements; want one text per shape", tc.op, n, got)
		}
		if got := prepared(orm.InlineValues(context.Background())); got < n {
			t.Errorf("%s inline: %d calls prepared only %d statements — the inline baseline should prepare one per call", tc.op, n, got)
		}
	}
}

// BenchmarkBoundStatements is the parse/plan reuse binding buys, per call:
// stmts/op is statements pgx prepared (a Parse/Describe round trip and a new
// plan each), on one session, for the bound form and the inline one.
func BenchmarkBoundStatements(b *testing.B) {
	if testing.Short() {
		b.Skip("repo pg benchmarks boot embedded postgres; skipped under -short")
	}
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		b.Fatalf("pgtest.NewURL: %v", err)
	}
	b.Cleanup(cleanup)
	setup, _ := openPgxSession(b, dsn)
	if _, err := setup.Bun().NewCreateTable().Model((*widget)(nil)).Exec(context.Background()); err != nil {
		b.Fatalf("create widgets: %v", err)
	}
	repo := NewRepo[widget]()

	ops := []struct {
		name string
		op   func(ctx context.Context, db *orm.Client, i int) error
	}{
		{"Create", func(ctx context.Context, db *orm.Client, i int) error {
			return repo.Create(ctx, db, &widget{Name: fmt.Sprintf("w%d", i), Tags: []string{"x"}})
		}},
		{"List", func(ctx context.Context, db *orm.Client, i int) error {
			_, err := repo.List(ctx, db, orm.WhereEq("name", fmt.Sprintf("w%d", i)), orm.WithLimit(10))
			return err
		}},
		{"Count", func(ctx context.Context, db *orm.Client, i int) error {
			_, err := repo.Count(ctx, db, orm.WhereEq("name", fmt.Sprintf("w%d", i)))
			return err
		}},
		{"NewRaw", func(ctx context.Context, db *orm.Client, i int) error {
			_, err := db.Bun().NewRaw("UPDATE widgets SET name = ? WHERE id = ?", fmt.Sprintf("n%d", i), orm.Arg(i%64+1)).Exec(ctx)
			return err
		}},
	}
	for _, o := range ops {
		for _, mode := range []string{"bound", "inline"} {
			b.Run(o.name+"/"+mode, func(b *testing.B) {
				db, sqldb := openPgxSession(b, dsn)
				ctx := context.Background()
				if mode == "inline" {
					ctx = orm.InlineValues(ctx)
				}
				preparedStatements(b, sqldb)
				before := preparedStatements(b, sqldb)
				b.ReportAllocs()
				i := 0
				for b.Loop() {
					if err := o.op(ctx, db, i); err != nil {
						b.Fatal(err)
					}
					i++
				}
				b.ReportMetric(float64(preparedStatements(b, sqldb)-before)/float64(i), "stmts/op")
			})
		}
	}
}
