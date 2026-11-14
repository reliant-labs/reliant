package dbanalyze_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/reliant-labs/forge/pkg/dbanalyze"
)

// source is a target with a serial, so a migration's INSERT advances a
// sequence the copy shares nothing with.
var source = []dbanalyze.MigrationFile{base[0], {
	File: "db/migrations/2_serial.up.sql", Version: 2,
	SQL: `CREATE TABLE s (id serial PRIMARY KEY, v int);
INSERT INTO s (v) VALUES (1), (2);`,
}}

// sourceTarget is a database standing in for a snapshot: migrated, and with
// no session connected, as CloneDatabase requires.
func sourceTarget(t *testing.T) string {
	t.Helper()
	dsn, cleanup, err := dbanalyze.PrepareScratch(context.Background(), source)
	if err != nil {
		t.Fatalf("scratch: %v", err)
	}
	t.Cleanup(cleanup)
	return dsn
}

// fingerprint is everything a run could change in a database, as one string:
// every user relation with its storage file (a rewrite changes it), every
// column and index definition, the sequence's state, and row counts.
func fingerprint(t *testing.T, dsn string) string {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var rels, cols, idx string
	var last int64
	var called bool
	var p, tt, s int
	err = db.QueryRow(`SELECT
		(SELECT string_agg(c.relname || ':' || c.relkind::text || ':' || c.relfilenode, ',' ORDER BY c.relname)
		   FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public'),
		(SELECT string_agg(table_name || '.' || column_name || ' ' || data_type || ' ' || COALESCE(column_default, ''), ',' ORDER BY table_name, ordinal_position)
		   FROM information_schema.columns WHERE table_schema = 'public'),
		(SELECT string_agg(indexdef, ';' ORDER BY indexname) FROM pg_indexes WHERE schemaname = 'public'),
		(SELECT last_value FROM s_id_seq), (SELECT is_called FROM s_id_seq),
		(SELECT count(*) FROM p), (SELECT count(*) FROM t), (SELECT count(*) FROM s)`).
		Scan(&rels, &cols, &idx, &last, &called, &p, &tt, &s)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s\n%s\n%s\nseq %d %v\nrows %d %d %d", rels, cols, idx, last, called, p, tt, s)
}

func databaseExists(t *testing.T, dsn, name string) bool {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var ok bool
	if err := db.QueryRow("SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

// The target is read and copied, never written: a run whose migrations
// commit, create and rewrite tables, insert through a serial and build an
// index concurrently leaves it exactly as it was, and the copy is dropped.
func TestTheTargetIsNeverWritten(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := sourceTarget(t)
	before := fingerprint(t, src)

	c, err := dbanalyze.CloneDatabase(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	info := c.Info()
	if !strings.HasPrefix(info.Database, "forge_analyze_") || info.SizeBytes <= 0 || info.DurationMS <= 0 {
		t.Errorf("clone info = %+v", info)
	}
	s, err := dbanalyze.Open(ctx, c.DSN(), dbanalyze.SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ti, err := s.Info(ctx, dbanalyze.TargetDSN, "copy")
	if err != nil {
		t.Fatal(err)
	}
	migs, _, err := dbanalyze.AnalyzeMigrations(ctx, s, ti, []dbanalyze.MigrationFile{
		{File: "a.up.sql", Version: 3, SQL: "CREATE TABLE n (id serial PRIMARY KEY);\nINSERT INTO s (v) VALUES (3), (4);\nINSERT INTO p VALUES (1000);"},
		{File: "b.up.sql", Version: 4, SQL: "ALTER TABLE t ALTER COLUMN a TYPE bigint;\nTRUNCATE t;"},
		{File: "c.up.sql", Version: 5, SQL: "CREATE INDEX CONCURRENTLY t_s ON t (s);"},
	}, dbanalyze.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migs {
		for _, st := range m.Statements {
			if !st.Executed {
				t.Errorf("%s line %d was not executed: %+v", m.File, st.Line, st)
			}
		}
	}
	// The copy holds what the migrations committed, for a later pass.
	var n int
	if err := s.Conn().QueryRowContext(ctx, "SELECT count(*) FROM s").Scan(&n); err != nil || n != 4 {
		t.Errorf("the copy's s has %d rows (%v), want 4", n, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	if after := fingerprint(t, src); after != before {
		t.Errorf("the target changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if databaseExists(t, src, info.Database) {
		t.Errorf("the copy %s was not dropped", info.Database)
	}
}

// A copy is dropped even when the analysis was cancelled mid-statement (an
// interrupt): Close runs on its own deadline.
func TestACancelledRunStillDropsItsCopy(t *testing.T) {
	t.Parallel()
	src := sourceTarget(t)
	c, err := dbanalyze.CloneDatabase(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s, err := dbanalyze.Open(ctx, c.DSN(), dbanalyze.SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := dbanalyze.AnalyzeMigrations(ctx, s, dbanalyze.TargetInfo{Kind: dbanalyze.TargetDSN}, []dbanalyze.MigrationFile{
			{File: "a.up.sql", Version: 3, SQL: "SELECT pg_sleep(30);"},
		}, dbanalyze.Config{})
		done <- err
	}()
	waitForSleep(t, src, c.Info().Database)
	cancel()
	if err := <-done; err == nil {
		t.Error("a cancelled analysis returned no error")
	}
	_ = s.Close()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if databaseExists(t, src, c.Info().Database) {
		t.Errorf("the copy %s was not dropped", c.Info().Database)
	}
}

// waitForSleep polls, until a deadline, for a session on db running pg_sleep.
func waitForSleep(t *testing.T, dsn, db string) {
	t.Helper()
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		var running bool
		if err := conn.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname = $1 AND query LIKE '%pg_sleep%' AND state = 'active')", db).Scan(&running); err != nil {
			t.Fatalf("the analysis never started its statement: %v", err)
		}
		if running {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("the analysis never started its statement")
		case <-tick.C:
		}
	}
}

// postgres copies a database only while nobody is connected to it: the error
// says so, and who is.
func TestACopyNeedsTheTargetToItself(t *testing.T) {
	t.Parallel()
	src := sourceTarget(t)
	other, err := pgx.Connect(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close(context.Background()) }()
	if _, err := other.Exec(context.Background(), "SET application_name = 'someone-else'"); err != nil {
		t.Fatal(err)
	}
	_, err = dbanalyze.CloneDatabase(context.Background(), src)
	if err == nil || !strings.Contains(err.Error(), "other sessions are connected") || !strings.Contains(err.Error(), "someone-else") {
		t.Fatalf("err = %v", err)
	}
}

// The marker is a DATABASE setting: a role's setting in the database, or a
// session's SET, does not mark it.
func TestTheMarkerIsTheDatabases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := sourceTarget(t)
	marked := func() bool {
		t.Helper()
		s, err := dbanalyze.OpenSource(ctx, src)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		ok, err := s.Marked(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	exec := func(q string) {
		t.Helper()
		conn, err := pgx.Connect(ctx, src)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close(ctx) }()
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if marked() {
		t.Fatal("an unmarked database reads as marked")
	}
	exec("DO $$ BEGIN EXECUTE format('ALTER ROLE CURRENT_USER IN DATABASE %I SET forge.analyze_target = %L', current_database(), 'on'); END $$")
	if marked() {
		t.Error("a role's setting marked the database")
	}
	exec("DO $$ BEGIN EXECUTE format('ALTER DATABASE %I SET forge.analyze_target = %L', current_database(), 'on'); END $$")
	if !marked() {
		t.Error("ALTER DATABASE … SET forge.analyze_target = 'on' did not mark the database")
	}
}

// A target the copy cannot be made from is an error, not a copy.
func TestCloneOfAnUnreachableTargetIsAnError(t *testing.T) {
	t.Parallel()
	_, err := dbanalyze.CloneDatabase(context.Background(), "postgres://x@127.0.0.1:1/app?connect_timeout=2")
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

// A connection string that does not parse is reported by its cause, never
// echoed: url and pgx parse errors carry the string, and pgx masks only the
// positions it knows hold a password.
func TestUnparseableTargetsAreNotEchoed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, dsn := range []string{
		"postgres://u:hunter2@db/app?%70assword=hunter2&sslmode=disable#hunter2",
		"postgres://u:hunter2@db:badport/app?%70assword=hunter2",
	} {
		if _, err := dbanalyze.CloneDatabase(ctx, dsn); err == nil || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("CloneDatabase(%q) = %v", dsn, err)
		}
		if _, err := dbanalyze.OpenSource(ctx, dsn); err == nil || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("OpenSource(%q) = %v", dsn, err)
		}
		if _, err := dbanalyze.Open(ctx, dsn, dbanalyze.SessionOptions{}); err == nil || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("Open(%q) = %v", dsn, err)
		}
	}
}
