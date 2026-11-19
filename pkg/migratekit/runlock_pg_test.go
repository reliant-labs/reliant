package migratekit

import (
	"database/sql"
	"testing"
	"testing/fstest"
)

// replicaRun is one replica's outcome in raceReplicas: Res for a replica that
// went through Migrator.Up, Logs for one that went through AutoMigrate.
type replicaRun struct {
	Res  Result
	Logs string
	Err  error
}

// raceReplicas starts n replicas of one release at once against dsn,
// alternating the two entry points: even ones as an initContainer or the dev
// loop's migrate job would (Open + Up), odd ones as a server booting with
// AUTO_MIGRATE would (AutoMigrate over a pool of its own).
func raceReplicas(t *testing.T, fsys fstest.MapFS, dsn string, n int) []replicaRun {
	t.Helper()
	runs := raceReplicasQuiet(fsys, dsn, n)
	for _, r := range runs {
		if r.Err != nil {
			t.Errorf("replica failed: %v", r.Err)
		}
	}
	return runs
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var ok bool
	if err := db.QueryRow("SELECT to_regclass($1) IS NOT NULL", name).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

// TestReplicasRacingThroughBothEntryPointsTakeTurns: every replica of a
// release starts at once — initContainers and the dev loop's migrate job
// through Migrator.Up, servers through AutoMigrate — against one fresh
// database. Every one must succeed, and each migration must apply once.
//
// Without migratekit's run lock (runlock.go) the two entry points took
// golang-migrate's advisory lock under DIFFERENT keys and did not exclude each
// other at all. Measured over six runs, every run failed: two replicas ran the
// same CREATE TABLE (the second failing on the first's table), replicas met
// "Dirty database version" while a peer was mid-apply, refused a migration a
// peer had just recorded as one the schema had "already passed", or lost a
// race creating schema_migrations itself (duplicate key in
// pg_type_typname_nsp_index).
func TestReplicasRacingThroughBothEntryPointsTakeTurns(t *testing.T) {
	dsn := requirePG(t)
	raceReplicas(t, release(olderFiles), dsn, 6)

	mg, err := Open(Options{FS: release(olderFiles), Dir: "migrations", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mg.Close() }()
	if st, err := mg.State(); err != nil || st != (State{Version: 2, Applied: true}) {
		t.Errorf("state = %+v, %v; want clean at 2", st, err)
	}
	db := openRecordDB(t, dsn)
	for _, table := range []string{"accounts", "plans"} {
		if !tableExists(t, db, table) {
			t.Errorf("table %s missing after the race", table)
		}
	}
}
