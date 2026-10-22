// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/migratekit"
	"github.com/reliant-labs/forge/pkg/migratekit/compat"
	"github.com/stretchr/testify/require"

	forgedb "github.com/reliant-labs/reliant/db"
)

// These tests run Migrate — the one path both the api-server's boot and
// `reliant db migrate up` take — against databases in each state it meets:
// fresh; already migrated; migrated the way prod is (adopted from goose by a
// release carrying the goose bridge, goose's table left behind); reliant's
// tables with no record; dirty; and contended. Each run is judged by what the
// database holds afterwards, not by what it logged.

func migrate(dsn string) (MigrateResult, error) {
	return Migrate(context.Background(), dsn)
}

// migrationState reads what the migrator recorded.
func migrationState(t *testing.T, db *sql.DB) migratekit.State {
	t.Helper()
	state, err := ReadMigrationState(db)
	require.NoError(t, err)
	return state
}

// testDSN is the DSN of the named database on the test server.
func testDSN(t *testing.T, name string) string {
	t.Helper()
	u, err := url.Parse(resolveTestDSN(t))
	require.NoError(t, err)
	u.Path = "/" + name
	return u.String()
}

// newEmptyTestDB creates an EMPTY database (no migrations, no seed) owned by
// this test and returns its DSN. Per-test databases normally copy the migrated
// template; these tests need to watch the migrator build one.
func newEmptyTestDB(t *testing.T) string {
	t.Helper()
	// Opens the process-wide admin pool the CREATE/DROP DDL goes through.
	if err := ensureTemplateDB(resolveTestDSN(t)); err != nil {
		t.Fatalf("prepare test template database: %v", err)
	}
	name := fmt.Sprintf("%st%d_%d", testDBPrefix, os.Getpid(), testDBSeq.Add(1))
	if _, err := adminDB.Exec(fmt.Sprintf(`CREATE DATABASE %s`, quoteIdent(name))); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() { dropTestDB(name) })
	return testDSN(t, name)
}

// newMigratedTestDB is newEmptyTestDB, migrated.
func newMigratedTestDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	dsn := newEmptyTestDB(t)
	_, err := migrate(dsn)
	require.NoError(t, err)
	return dsn, openTestDSN(t, dsn)
}

func openTestDSN(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// baseline returns the first embedded migration.
func baseline(t *testing.T) compat.Migration {
	t.Helper()
	ms, err := compat.Scan(forgedb.Source(), forgedb.SourceDir)
	require.NoError(t, err)
	require.NotEmpty(t, ms)
	return ms[0]
}

// latestEmbedded is the newest migration this binary embeds.
func latestEmbedded(t *testing.T) uint {
	t.Helper()
	versions, err := EmbeddedMigrationVersions()
	require.NoError(t, err)
	return uint(versions[len(versions)-1])
}

// schemaFingerprint identifies every relation in public, other than forge's
// migrator's bookkeeping, by oid and name. Running any DDL that creates or
// rebuilds a relation changes it.
func schemaFingerprint(t *testing.T, db *sql.DB) string {
	t.Helper()
	var fp string
	require.NoError(t, db.QueryRow(`
		SELECT count(*) || ':' || md5(string_agg(c.oid::text || '/' || c.relname, ',' ORDER BY c.oid))
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relname NOT LIKE 'schema_migrations%'`).Scan(&fp))
	return fp
}

func hasTable(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var ok bool
	require.NoError(t, db.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, name).Scan(&ok))
	return ok
}

// requireCurrent asserts the database is where a fresh one ends: the newest
// embedded version recorded clean, and nothing pending.
func requireCurrent(t *testing.T, db *sql.DB) {
	t.Helper()
	state := migrationState(t, db)
	require.Equal(t, latestEmbedded(t), state.Version)
	require.False(t, state.Dirty)
	pending, err := PendingMigrations(db)
	require.NoError(t, err)
	require.Empty(t, pending)
}

// A fresh database runs the baseline, and everything after it.
func TestMigrate_FreshDatabaseAppliesEveryMigration(t *testing.T) {
	dsn := newEmptyTestDB(t)

	res, err := migrate(dsn)
	require.NoError(t, err)
	require.True(t, res.Changed)
	require.Nil(t, res.Adopted)
	require.Nil(t, res.Ahead)

	db := openTestDSN(t, dsn)
	requireCurrent(t, db)
	require.True(t, hasTable(t, db, "chats"), "the baseline's schema must exist")

	base := baseline(t)
	var recorded string
	require.NoError(t, db.QueryRow(`SELECT name FROM schema_migrations_applied WHERE version = $1`, base.Version).Scan(&recorded))
	require.Equal(t, base.Descriptor, recorded, "the applied record must name the baseline")
}

// A migrated database is left alone: the next boot applies nothing and runs
// no DDL.
func TestMigrate_CurrentDatabaseIsANoOp(t *testing.T) {
	dsn, db := newMigratedTestDB(t)
	fp := schemaFingerprint(t, db)

	res, err := migrate(dsn)
	require.NoError(t, err)
	require.False(t, res.Changed)
	requireCurrent(t, db)
	require.Equal(t, fp, schemaFingerprint(t, db))
}

// Every database we keep is one forge's migrator adopted from goose: its
// record is forge's, and goose's goose_db_version is still there beside it.
// That leftover table is never read: a run over such a database is a no-op,
// and leaves the table exactly as it was.
func TestMigrate_AdoptedDatabaseIsANoOp(t *testing.T) {
	dsn, db := newMigratedTestDB(t)
	_, err := db.Exec(`CREATE TABLE goose_db_version (id serial PRIMARY KEY, version_id bigint NOT NULL, is_applied boolean NOT NULL, tstamp timestamp DEFAULT now());
		INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, true), (20261014142537, true), (20990101000000, true)`)
	require.NoError(t, err)
	fp := schemaFingerprint(t, db)

	plan, err := PlanMigration(context.Background(), db)
	require.NoError(t, err)
	verdict, refuses := plan.Verdict()
	require.False(t, refuses, verdict)
	require.Equal(t, fmt.Sprintf("up to date (version %d)", latestEmbedded(t)), verdict)

	res, err := migrate(dsn)
	require.NoError(t, err)
	require.False(t, res.Changed)
	require.Nil(t, res.Adopted)
	requireCurrent(t, db)
	require.Equal(t, fp, schemaFingerprint(t, db))
	var rows int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM goose_db_version`).Scan(&rows))
	require.Equal(t, 3, rows, "goose's leftover table is never written")
}

// reliant's tables with no migration record are not a schema this binary can
// carry forward — a goose-era database no bridge-carrying release adopted, or
// one whose history was lost: refused, with nothing applied, naming the release
// that upgrades the first kind, rather than left dirty by a baseline that fails
// on its first CREATE TABLE.
func TestMigrate_RefusesReliantTablesWithoutARecord(t *testing.T) {
	dsn, db := newMigratedTestDB(t)
	_, err := db.Exec(`DROP TABLE IF EXISTS schema_migrations, schema_migrations_applied, schema_migrations_compat`)
	require.NoError(t, err)
	fp := schemaFingerprint(t, db)

	plan, err := PlanMigration(context.Background(), db)
	require.NoError(t, err)
	verdict, refuses := plan.Verdict()
	require.True(t, refuses, "status must say up refuses")
	require.True(t, strings.HasPrefix(verdict, "refuse: "), verdict)

	_, err = migrate(dsn)
	require.Error(t, err)
	require.Contains(t, err.Error(), "NOTHING WAS APPLIED")
	require.Contains(t, err.Error(), gooseBridgeRelease)
	require.Equal(t, fp, schemaFingerprint(t, db), "nothing may change")
	require.False(t, hasTable(t, db, "schema_migrations"), "forge's migrator must not have run")
}

// Concurrent runs — replicas booting together, or a migrate Job beside an
// api-server — take turns and all succeed: exactly one builds, and the rest
// find nothing to do.
func TestMigrate_ConcurrentRunsTakeTurns(t *testing.T) {
	const runners = 4
	dsn := newEmptyTestDB(t)
	var (
		wg      sync.WaitGroup
		results = make([]MigrateResult, runners)
		errs    = make([]error, runners)
	)
	for i := range runners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = migrate(dsn)
		}()
	}
	wg.Wait()
	require.NoError(t, errors.Join(errs...))
	changed := 0
	for _, r := range results {
		if r.Changed {
			changed++
		}
	}
	require.Equal(t, 1, changed)
	requireCurrent(t, openTestDSN(t, dsn))
}

// PlanMigration is what `reliant db migrate status` prints. Its verdict for
// each state must be what Migrate then does.
func TestPlanMigration_Verdicts(t *testing.T) {
	verdict := func(t *testing.T, db *sql.DB) (string, bool) {
		t.Helper()
		// The command runs it in a READ ONLY transaction; so does this.
		tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()
		plan, err := PlanMigration(context.Background(), tx)
		require.NoError(t, err)
		return plan.Verdict()
	}

	t.Run("fresh", func(t *testing.T) {
		v, refuses := verdict(t, openTestDSN(t, newEmptyTestDB(t)))
		require.False(t, refuses)
		require.True(t, strings.HasPrefix(v, "fresh database: apply "+baseline(t).Name), v)
	})
	t.Run("up to date", func(t *testing.T) {
		_, db := newMigratedTestDB(t)
		v, refuses := verdict(t, db)
		require.False(t, refuses)
		require.Equal(t, fmt.Sprintf("up to date (version %d)", latestEmbedded(t)), v)
	})
	t.Run("pending", func(t *testing.T) {
		versions, err := EmbeddedMigrationVersions()
		require.NoError(t, err)
		if len(versions) < 2 {
			t.Skip("only the baseline is embedded: nothing can be pending past it")
		}
		_, db := newMigratedTestDB(t)
		_, err = db.Exec(`UPDATE schema_migrations SET version = $1`, versions[0])
		require.NoError(t, err)
		v, refuses := verdict(t, db)
		require.False(t, refuses)
		require.True(t, strings.HasPrefix(v, fmt.Sprintf("apply %d pending migration(s): ", len(versions)-1)), v)
	})
	t.Run("dirty", func(t *testing.T) {
		_, db := newMigratedTestDB(t)
		_, err := db.Exec(`UPDATE schema_migrations SET dirty = true`)
		require.NoError(t, err)
		v, refuses := verdict(t, db)
		require.True(t, refuses)
		require.True(t, strings.HasPrefix(v, "refuse: the schema is dirty"), v)
	})
}
