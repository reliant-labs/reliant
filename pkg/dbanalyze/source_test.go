package dbanalyze_test

import (
	"context"
	"testing"

	"github.com/reliant-labs/forge/pkg/dbanalyze"
)

// A Source reads the target's own version, statistics and migration state
// without writing to it.
func TestSourceReadsTheTargetsVersionAndMigrationState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := sourceTarget(t)
	s, err := dbanalyze.OpenSource(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	info, err := s.Info(ctx, dbanalyze.TargetDSN, "h:1/app")
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != dbanalyze.TargetDSN || info.Identity != "h:1/app" || info.ServerVersionNum < 140000 || info.ServerVersion == "" {
		t.Errorf("info = %+v", info)
	}
	if info.HasStatistics {
		t.Error("a database nothing has analyzed has no statistics")
	}
	scratch, err := s.Info(ctx, dbanalyze.TargetScratch, "scratch")
	if err != nil || scratch.HasStatistics || scratch.Kind != dbanalyze.TargetScratch {
		t.Errorf("a scratch target is not asked for statistics: %+v, %v", scratch, err)
	}

	st, err := s.MigrationState(ctx)
	if err != nil || !st.Applied || st.Version != 2 || st.Dirty {
		t.Errorf("state = %+v, %v; want version 2 applied", st, err)
	}
}

func TestSourceAndCloneCloseAreIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := sourceTarget(t)
	s, err := dbanalyze.OpenSource(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("a second Close: %v", err)
	}
	var nilSource *dbanalyze.Source
	if err := nilSource.Close(); err != nil {
		t.Errorf("Close on a nil Source: %v", err)
	}

	c, err := dbanalyze.CloneDatabase(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if c.DSN() == src || c.Info().Database == "" || c.Info().SizeBytes <= 0 {
		t.Errorf("clone = %q %+v", c.DSN(), c.Info())
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("a second Close of a dropped copy: %v", err)
	}
	var nilClone *dbanalyze.Clone
	if err := nilClone.Close(); err != nil {
		t.Errorf("Close on a nil Clone: %v", err)
	}
}

func TestCloneOfADSNWithoutADatabaseIsRefused(t *testing.T) {
	t.Parallel()
	if _, err := dbanalyze.CloneDatabase(context.Background(), "postgres://u@127.0.0.1:1/"); err == nil {
		t.Fatal("want an error for a target that names no database")
	}
}

func TestNegativeSessionTimeoutsMeanNoTimeout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dsn, cleanup, err := dbanalyze.PrepareScratch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	s, err := dbanalyze.Open(ctx, dsn, dbanalyze.SessionOptions{StatementTimeout: -1, LockTimeout: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	// With no timeouts the session's settings are "0": nothing cancels a long statement.
	var stmt, lock string
	if err := s.Conn().QueryRowContext(ctx, "SELECT current_setting('statement_timeout'), current_setting('lock_timeout')").Scan(&stmt, &lock); err != nil {
		t.Fatal(err)
	}
	if stmt != "0" || lock != "0" {
		t.Errorf("negative timeouts mean none, got statement_timeout=%s lock_timeout=%s", stmt, lock)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("a second Close: %v", err)
	}
}

func TestPrepareScratchRefusesBaseMigrationsTheMigratorWouldSkip(t *testing.T) {
	t.Parallel()
	_, _, err := dbanalyze.PrepareScratch(context.Background(), []dbanalyze.MigrationFile{
		{File: "db/migrations/1_ok.up.sql", Version: 1, SQL: "CREATE TABLE a (id int)"},
		{File: "db/migrations/notamigration.sql", Version: 2, SQL: "CREATE TABLE b (id int)"},
	})
	if err == nil {
		t.Fatal("a base file the migrator would silently skip must be an error")
	}
}

func TestPrepareScratchAppliesTheBaseMigrationsAndReportsAFailingOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dsn, cleanup, err := dbanalyze.PrepareScratch(ctx, []dbanalyze.MigrationFile{
		{File: "db/migrations/1_a.up.sql", Version: 1, SQL: "CREATE TABLE a (id int)"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	s, err := dbanalyze.OpenSource(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if st, err := s.MigrationState(ctx); err != nil || !st.Applied || st.Version != 1 {
		t.Errorf("state = %+v, %v", st, err)
	}

	_, _, err = dbanalyze.PrepareScratch(ctx, []dbanalyze.MigrationFile{
		{File: "db/migrations/1_bad.up.sql", Version: 1, SQL: "THIS IS NOT SQL"},
	})
	if err == nil {
		t.Fatal("a base migration that fails must fail the scratch")
	}
}
