// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/reliant-labs/forge/pkg/migratekit"
	"github.com/reliant-labs/forge/pkg/migratekit/compat"
	"github.com/stretchr/testify/require"

	forgedb "github.com/reliant-labs/reliant/db"
)

// These tests run Migrate — the one path both the api-server's boot and
// `reliant db migrate up` take — against databases in each state the goose →
// forge cutover meets: fresh; goose at its frozen head; goose BEHIND it by one,
// by half its history, and by nearly all of it; goose with a version the
// frozen history does not hold; already migrated; and contended. Every goose
// database here is built by goose itself, over the frozen files, the way the
// goose releases built them. Each run is judged by what the database holds
// afterwards — its schema against a fresh database's, the rows seeded before
// it, the bookkeeping of both tools — not by what it logged.

func migrate(dsn string) (MigrateResult, error) {
	return Migrate(context.Background(), dsn, slog.New(slog.DiscardHandler))
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
// template; these tests need to watch the migrator build or adopt one.
func newEmptyTestDB(t *testing.T) string {
	t.Helper()
	return newTestDBFrom(t, "")
}

// newTestDBFrom creates a database owned by this test, copied from template
// (empty when template is ""), and returns its DSN.
func newTestDBFrom(t *testing.T, template string) string {
	t.Helper()
	// Opens the process-wide admin pool the CREATE/DROP DDL goes through.
	if err := ensureTemplateDB(resolveTestDSN(t)); err != nil {
		t.Fatalf("prepare test template database: %v", err)
	}
	name := fmt.Sprintf("%st%d_%d", testDBPrefix, os.Getpid(), testDBSeq.Add(1))
	stmt := fmt.Sprintf(`CREATE DATABASE %s`, quoteIdent(name))
	if template != "" {
		stmt += " TEMPLATE " + quoteIdent(template)
	}
	if _, err := adminDB.Exec(stmt); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() { dropTestDB(name) })
	return testDSN(t, name)
}

func openTestDSN(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// baseline returns the first embedded migration — the one that adopts goose.
func baseline(t *testing.T) compat.Migration {
	t.Helper()
	ms, err := compat.Scan(forgedb.Source(), forgedb.SourceDir)
	require.NoError(t, err)
	require.NotEmpty(t, ms)
	require.NotNil(t, ms[0].Adopts, "the first migration must declare the goose history it adopts")
	return ms[0]
}

// latestEmbedded is the newest migration this binary embeds.
func latestEmbedded(t *testing.T) uint {
	t.Helper()
	versions, err := EmbeddedMigrationVersions()
	require.NoError(t, err)
	return uint(versions[len(versions)-1])
}

// gooseVersions is goose's frozen history: what the baseline adopts.
func gooseVersions(t *testing.T) []int64 {
	t.Helper()
	return baseline(t).Adopts.Versions
}

var (
	gooseAtMu        sync.Mutex
	gooseAtTemplates = map[int64]string{}
)

// gooseDBAt returns the DSN of a new database goose migrated up to and
// including version, from the frozen files — what a goose release whose newest
// migration was version left behind — with seedRows' rows in it. Each version
// is built once per process, as a template, and copied.
func gooseDBAt(t *testing.T, version int64) string {
	t.Helper()
	// Skips (under -short, or with no database) before the lock is taken: a
	// skip is runtime.Goexit, and a lock it left held would hang every later
	// test.
	resolveTestDSN(t)
	tpl := gooseTemplateAt(t, version)
	return newTestDBFrom(t, tpl)
}

func gooseTemplateAt(t *testing.T, version int64) string {
	t.Helper()
	gooseAtMu.Lock()
	defer gooseAtMu.Unlock()
	tpl, ok := gooseAtTemplates[version]
	if !ok {
		dsn := newEmptyTestDB(t)
		tpl = fmt.Sprintf("%stplgv%d_%d", testDBPrefix, version, os.Getpid())
		db, err := sql.Open("pgx", dsn)
		require.NoError(t, err)
		require.NoError(t, initGoose())
		require.NoError(t, goose.UpTo(db, migrationsDir, version, goose.WithAllowMissing()))
		seedRows(t, db)
		require.NoError(t, db.Close())
		u, err := url.Parse(dsn)
		require.NoError(t, err)
		built := strings.TrimPrefix(u.Path, "/")
		// Rename the built database into a template that outlives this test;
		// the reaper collects it once the process is gone.
		_, err = adminDB.Exec(fmt.Sprintf(`ALTER DATABASE %s RENAME TO %s`, quoteIdent(built), quoteIdent(tpl)))
		require.NoError(t, err)
		gooseAtTemplates[version] = tpl
	}
	return tpl
}

// seedRows writes a project and a chat the way the release that built the
// database would have: the chat's state was text before 20260223000000 turned
// it into an enum.
func seedRows(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO projects (id, user_id, name, path, created_at, updated_at, last_active)
		VALUES ('seeded-project', 'seed-user', 'Seeded before the upgrade', '/seeded', now(), now(), now())`)
	require.NoError(t, err)
	var stateType string
	require.NoError(t, db.QueryRow(`SELECT data_type FROM information_schema.columns WHERE table_name = 'chats' AND column_name = 'state'`).Scan(&stateType))
	state := any(2)
	if stateType == "text" {
		state = "idle"
	}
	_, err = db.Exec(`INSERT INTO chats (id, title, project_id, user_id, state, created_at, updated_at, last_active)
		VALUES ('seeded-chat', 'Seeded chat', 'seeded-project', 'seed-user', $1, now(), now(), now())`, state)
	require.NoError(t, err)
}

// requireSeededRows asserts seedRows' rows survived, in today's shape.
func requireSeededRows(t *testing.T, db *sql.DB) {
	t.Helper()
	var name string
	require.NoError(t, db.QueryRow(`SELECT name FROM projects WHERE id = 'seeded-project'`).Scan(&name), "the seeded project must survive")
	require.Equal(t, "Seeded before the upgrade", name)
	var (
		title string
		state int
	)
	require.NoError(t, db.QueryRow(`SELECT title, state FROM chats WHERE id = 'seeded-chat'`).Scan(&title, &state), "the seeded chat must survive")
	require.Equal(t, "Seeded chat", title)
	require.Equal(t, 2, state, "an idle chat stays idle (CHAT_STATE_IDLE)")
}

// schemaShape renders public's schema — columns in order, constraints,
// indexes, views, functions, triggers, sequences — with neither tool's
// bookkeeping tables, and with nothing that differs between two databases
// built the same way by different routes (oids; the gaps a dropped column
// leaves in attnum). Two databases with the same shape have the same schema.
func schemaShape(t *testing.T, db *sql.DB) string {
	t.Helper()
	const notBookkeeping = `NOT IN ('goose_db_version', 'goose_db_version_id_seq', 'schema_migrations', 'schema_migrations_applied', 'schema_migrations_compat')`
	queries := []string{
		`SELECT table_name || '.' || column_name || ' #' || row_number() OVER (PARTITION BY table_name ORDER BY ordinal_position) || ' ' ||
		        udt_name || ' null=' || is_nullable || ' default=' || coalesce(column_default, '-')
		   FROM information_schema.columns WHERE table_schema = 'public' AND table_name ` + notBookkeeping,
		`SELECT conrelid::regclass::text || ' ' || conname || ' ' || pg_get_constraintdef(c.oid)
		   FROM pg_constraint c JOIN pg_namespace n ON n.oid = c.connamespace
		  WHERE n.nspname = 'public' AND conrelid::regclass::text ` + notBookkeeping,
		`SELECT indexdef FROM pg_indexes WHERE schemaname = 'public' AND tablename ` + notBookkeeping,
		`SELECT viewname || ' ' || definition FROM pg_views WHERE schemaname = 'public'`,
		`SELECT p.proname || ' ' || pg_get_functiondef(p.oid) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'public'`,
		`SELECT pg_get_triggerdef(tg.oid) FROM pg_trigger tg JOIN pg_class c ON c.oid = tg.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		  WHERE n.nspname = 'public' AND NOT tg.tgisinternal`,
		`SELECT sequence_name || ' ' || data_type || ' ' || increment FROM information_schema.sequences
		  WHERE sequence_schema = 'public' AND sequence_name ` + notBookkeeping,
	}
	var lines []string
	for _, q := range queries {
		rows, err := db.Query(q + ` ORDER BY 1`)
		require.NoError(t, err, q)
		for rows.Next() {
			var line string
			require.NoError(t, rows.Scan(&line))
			lines = append(lines, line)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
	}
	return strings.Join(lines, "\n")
}

// freshShape is the schema the baseline builds on an empty database.
func freshShape(t *testing.T) string {
	t.Helper()
	dsn := newEmptyTestDB(t)
	_, err := migrate(dsn)
	require.NoError(t, err)
	return schemaShape(t, openTestDSN(t, dsn))
}

// schemaFingerprint identifies every relation in public, other than forge's
// migrator's bookkeeping, by oid and name. Rebuilding the schema (the baseline's
// CREATEs) changes every oid, so an adoption that ran the baseline's DDL changes
// it; a migration after the baseline that only ALTERs a table does not, because
// adopting the baseline is followed by running those.
// Views are left out: a migration that rebuilds one (chats_with_activity gains a
// column whenever the chat's activity does) gets a new OID, and a view holds no
// data and is not the DDL adoption must not run.
func schemaFingerprint(t *testing.T, db *sql.DB) string {
	t.Helper()
	var fp string
	require.NoError(t, db.QueryRow(`
		SELECT count(*) || ':' || md5(string_agg(c.oid::text || '/' || c.relname, ',' ORDER BY c.oid))
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind <> 'v' AND c.relname NOT LIKE 'schema_migrations%'`).Scan(&fp))
	return fp
}

// gooseRows is goose_db_version, row by row.
func gooseRows(t *testing.T, db *sql.DB) string {
	t.Helper()
	var rows string
	require.NoError(t, db.QueryRow(`SELECT count(*) || ':' || md5(string_agg(id || '/' || version_id || '/' || is_applied, ',' ORDER BY id)) FROM goose_db_version`).Scan(&rows))
	return rows
}

func hasTable(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var ok bool
	require.NoError(t, db.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, name).Scan(&ok))
	return ok
}

// requireCurrent asserts the database ends where a fresh one does: the newest
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

// (a) A fresh database runs the baseline, and everything after it, and never
// meets goose.
func TestMigrate_FreshDatabaseAppliesEveryMigration(t *testing.T) {
	dsn := newEmptyTestDB(t)

	res, err := migrate(dsn)
	require.NoError(t, err)
	require.Empty(t, res.GooseApplied)
	require.Nil(t, res.Forge.Adopted)
	require.True(t, res.Forge.Changed)

	db := openTestDSN(t, dsn)
	requireCurrent(t, db)
	require.True(t, hasTable(t, db, "chats"), "the baseline's schema must exist")
	require.False(t, hasTable(t, db, "goose_db_version"), "a fresh database must never get goose's table")
}

// (b) A database goose migrated to its frozen head is adopted: recorded at the
// baseline without running any of its DDL, goose's table untouched, its data
// kept.
func TestMigrate_AdoptsAGooseHeadDatabaseWithoutRunningItsSQL(t *testing.T) {
	versions := gooseVersions(t)
	dsn := gooseDBAt(t, versions[len(versions)-1])
	db := openTestDSN(t, dsn)
	before, goose := schemaFingerprint(t, db), gooseRows(t, db)

	res, err := migrate(dsn)
	require.NoError(t, err)
	require.Empty(t, res.GooseApplied, "a database at goose's head needs no catch-up")
	require.NotNil(t, res.Forge.Adopted)
	require.Equal(t, len(versions), res.Forge.Adopted.Versions)

	requireCurrent(t, db)
	base := baseline(t)
	var recorded string
	require.NoError(t, db.QueryRow(`SELECT name FROM schema_migrations_applied WHERE version = $1`, base.Version).Scan(&recorded))
	require.Equal(t, base.Descriptor, recorded, "the applied record must name the baseline")

	require.Equal(t, before, schemaFingerprint(t, db), "adoption must not run DDL")
	require.Equal(t, goose, gooseRows(t, db), "goose_db_version is the rollback target and must be left alone")
	requireSeededRows(t, db)
}

// (c), (d), (e) A database an OLDER goose release left behind — one migration
// behind, half the history behind, all but the first few behind — is brought
// to goose's frozen head by goose itself, then adopted, in one run. It ends
// with exactly the schema a fresh database gets, its rows kept and carried
// through every data migration in between.
func TestMigrate_CatchesUpAGooseDatabaseBehindTheFrozenHeadThenAdoptsIt(t *testing.T) {
	versions := gooseVersions(t)
	want := freshShape(t)
	for _, tc := range []struct {
		name    string
		applied int // how many of goose's versions the database has
	}{
		{"head-1", len(versions) - 1},
		{"mid-history", len(versions) / 2},
		{"very old", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsn := gooseDBAt(t, versions[tc.applied-1])
			db := openTestDSN(t, dsn)
			missing := versions[tc.applied:]

			plan, err := PlanMigration(context.Background(), db)
			require.NoError(t, err)
			require.Equal(t, missing, plan.Goose.Missing)
			verdict, refuses := plan.Verdict()
			require.False(t, refuses)
			require.True(t, strings.HasPrefix(verdict, fmt.Sprintf("goose catch-up %d, then adopt", len(missing))), verdict)

			res, err := migrate(dsn)
			require.NoError(t, err)
			require.Equal(t, missing, res.GooseApplied, "the catch-up applies exactly what the database was missing")
			require.NotNil(t, res.Forge.Adopted, "then the migrator adopts it")

			requireCurrent(t, db)
			require.Equal(t, want, schemaShape(t, db), "a caught-up database has exactly a fresh database's schema")
			requireSeededRows(t, db)
			after, err := readGooseHistoryOf(db)
			require.NoError(t, err)
			require.Empty(t, after.Missing, "goose_db_version records goose's whole history, as the last goose release would")

			// And a second run is a no-op.
			fp := schemaFingerprint(t, db)
			res, err = migrate(dsn)
			require.NoError(t, err)
			require.Empty(t, res.GooseApplied)
			require.Nil(t, res.Forge.Adopted)
			require.False(t, res.Forge.Changed)
			require.Equal(t, fp, schemaFingerprint(t, db))
		})
	}
}

func readGooseHistoryOf(db *sql.DB) (*GooseHistory, error) {
	ms, err := embeddedMigrations()
	if err != nil {
		return nil, err
	}
	h, _, err := readGooseHistory(context.Background(), db, ms[0].Adopts.Versions)
	return h, err
}

// (f) A goose version the frozen history does not hold — a branch's migration
// on a shared database — is refused before goose runs: loudly, and with
// NOTHING applied, whether or not the database is also behind.
func TestMigrate_RefusesAGooseVersionTheFrozenHistoryDoesNotHold(t *testing.T) {
	versions := gooseVersions(t)
	const branchOnly int64 = 20990101000000
	for _, tc := range []struct {
		name    string
		applied int
	}{
		{"at goose's head", len(versions)},
		{"behind goose's head", len(versions) / 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsn := gooseDBAt(t, versions[tc.applied-1])
			db := openTestDSN(t, dsn)
			_, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)`, branchOnly)
			require.NoError(t, err)
			before, goose := schemaFingerprint(t, db), gooseRows(t, db)

			plan, err := PlanMigration(context.Background(), db)
			require.NoError(t, err)
			_, refuses := plan.Verdict()
			require.True(t, refuses, "status must say up refuses")

			res, err := migrate(dsn)
			require.Error(t, err)
			require.Contains(t, err.Error(), "NOTHING WAS APPLIED")
			require.Contains(t, err.Error(), fmt.Sprint(branchOnly))
			require.Empty(t, res.GooseApplied)

			require.Equal(t, before, schemaFingerprint(t, db), "nothing may change")
			require.Equal(t, goose, gooseRows(t, db), "goose must not have run")
			require.False(t, hasTable(t, db, "schema_migrations"), "forge's migrator must not have run")
		})
	}
}

// A frozen version whose latest goose row records a rollback: goose would not
// re-run it, so the database could never be adopted. Refused before goose
// runs anything.
func TestMigrate_RefusesAGooseVersionRecordedRolledBack(t *testing.T) {
	versions := gooseVersions(t)
	dsn := gooseDBAt(t, versions[len(versions)/2-1])
	db := openTestDSN(t, dsn)
	_, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, false)`, versions[1])
	require.NoError(t, err)
	before, goose := schemaFingerprint(t, db), gooseRows(t, db)

	_, err = migrate(dsn)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ROLLBACK")
	require.Contains(t, err.Error(), "NOTHING WAS APPLIED")
	require.Equal(t, before, schemaFingerprint(t, db))
	require.Equal(t, goose, gooseRows(t, db))
	require.False(t, hasTable(t, db, "schema_migrations"))
}

// reliant's tables with neither tool's history are not a schema any release
// built: refused, rather than left dirty by a baseline that fails partway.
func TestMigrate_RefusesReliantTablesWithoutAnyHistory(t *testing.T) {
	versions := gooseVersions(t)
	dsn := gooseDBAt(t, versions[len(versions)-1])
	db := openTestDSN(t, dsn)
	_, err := db.Exec(`DROP TABLE goose_db_version`)
	require.NoError(t, err)

	_, err = migrate(dsn)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no migration history")
	require.False(t, hasTable(t, db, "schema_migrations"))
}

// Once forge has recorded a version, goose's table is never read again: a
// goose row added after adoption changes nothing.
func TestMigrate_NeverReadsGooseAgainAfterAdoption(t *testing.T) {
	versions := gooseVersions(t)
	dsn := gooseDBAt(t, versions[len(versions)-1])
	db := openTestDSN(t, dsn)
	_, err := migrate(dsn)
	require.NoError(t, err)
	fp := schemaFingerprint(t, db)

	_, err = db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (20990101000000, true)`)
	require.NoError(t, err)
	res, err := migrate(dsn)
	require.NoError(t, err)
	require.Empty(t, res.GooseApplied)
	require.False(t, res.Forge.Changed)
	requireCurrent(t, db)
	require.Equal(t, fp, schemaFingerprint(t, db))
}

// Concurrent runs — replicas booting together, or a migrate Job beside an
// api-server — take turns and all succeed, in every state: exactly one builds,
// catches up, or adopts, and the rest find nothing to do. The behind case
// crosses goose's CREATE INDEX CONCURRENTLY migrations while the others wait,
// which is what the bridge lock's polling exists for (goose_bridge.go).
func TestMigrate_ConcurrentRunsTakeTurns(t *testing.T) {
	const runners = 4
	versions := gooseVersions(t)
	run := func(t *testing.T, dsn string) []MigrateResult {
		t.Helper()
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
		return results
	}
	count := func(results []MigrateResult, f func(MigrateResult) bool) int {
		n := 0
		for _, r := range results {
			if f(r) {
				n++
			}
		}
		return n
	}

	t.Run("fresh", func(t *testing.T) {
		dsn := newEmptyTestDB(t)
		results := run(t, dsn)
		require.Equal(t, 1, count(results, func(r MigrateResult) bool { return r.Forge.Changed }))
		requireCurrent(t, openTestDSN(t, dsn))
	})
	t.Run("goose head", func(t *testing.T) {
		dsn := gooseDBAt(t, versions[len(versions)-1])
		db := openTestDSN(t, dsn)
		before := schemaFingerprint(t, db)
		results := run(t, dsn)
		require.Equal(t, 1, count(results, func(r MigrateResult) bool { return r.Forge.Adopted != nil }))
		requireCurrent(t, db)
		require.Equal(t, before, schemaFingerprint(t, db), "adopted, not rebuilt")
	})
	t.Run("goose behind, across CREATE INDEX CONCURRENTLY", func(t *testing.T) {
		dsn := gooseDBAt(t, versions[len(versions)/2-1])
		db := openTestDSN(t, dsn)
		results := run(t, dsn)
		require.Equal(t, 1, count(results, func(r MigrateResult) bool { return len(r.GooseApplied) > 0 }), "one process catches up")
		require.Equal(t, 1, count(results, func(r MigrateResult) bool { return r.Forge.Adopted != nil }), "one process adopts")
		requireCurrent(t, db)
		requireSeededRows(t, db)
		var invalid int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM pg_index WHERE NOT indisvalid`).Scan(&invalid))
		require.Zero(t, invalid, "no CREATE INDEX CONCURRENTLY may be left INVALID")
	})
}

// PlanMigration is what `reliant db migrate status` prints. Its verdict for
// each state must be what Migrate then does.
func TestPlanMigration_Verdicts(t *testing.T) {
	versions := gooseVersions(t)
	verdict := func(t *testing.T, dsn string) (string, bool) {
		t.Helper()
		db := openTestDSN(t, dsn)
		// The command runs it in a READ ONLY transaction; so does this.
		tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()
		plan, err := PlanMigration(context.Background(), tx)
		require.NoError(t, err)
		return plan.Verdict()
	}

	t.Run("fresh", func(t *testing.T) {
		v, refuses := verdict(t, newEmptyTestDB(t))
		require.False(t, refuses)
		require.True(t, strings.HasPrefix(v, "fresh database: apply "), v)
	})
	t.Run("goose head", func(t *testing.T) {
		v, refuses := verdict(t, gooseDBAt(t, versions[len(versions)-1]))
		require.False(t, refuses)
		require.True(t, strings.HasPrefix(v, "adopt: "), v)
	})
	t.Run("goose behind", func(t *testing.T) {
		v, refuses := verdict(t, gooseDBAt(t, versions[len(versions)-2]))
		require.False(t, refuses)
		require.True(t, strings.HasPrefix(v, "goose catch-up 1, then adopt"), v)
	})
	t.Run("up to date", func(t *testing.T) {
		dsn := newEmptyTestDB(t)
		_, err := migrate(dsn)
		require.NoError(t, err)
		v, refuses := verdict(t, dsn)
		require.False(t, refuses)
		require.Equal(t, fmt.Sprintf("up to date (version %d)", latestEmbedded(t)), v)
	})
	t.Run("dirty", func(t *testing.T) {
		dsn := newEmptyTestDB(t)
		_, err := migrate(dsn)
		require.NoError(t, err)
		_, err = openTestDSN(t, dsn).Exec(`UPDATE schema_migrations SET dirty = true`)
		require.NoError(t, err)
		v, refuses := verdict(t, dsn)
		require.True(t, refuses)
		require.True(t, strings.HasPrefix(v, "refuse: the schema is dirty"), v)
	})
}
