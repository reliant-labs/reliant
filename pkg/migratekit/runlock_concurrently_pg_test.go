package migratekit

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

// concurrentlyFiles is a release whose second migration builds an index
// CONCURRENTLY over a table the first fills, so the build takes long enough
// for every other replica to be waiting its turn while it runs.
var concurrentlyFiles = map[string]string{
	"1_items.up.sql": "CREATE TABLE items AS SELECT g AS id, md5(g::text) AS v FROM generate_series(1, 200000) g;",
	"2_items_v.up.sql": "-- forge:backward-compatible\n" +
		"CREATE INDEX CONCURRENTLY items_v ON items (v);",
}

// TestReplicasWaitingForTheRunLockDoNotDeadlockCreateIndexConcurrently:
// replicas of one release boot together, and the migration the first one
// runs is a CREATE INDEX CONCURRENTLY. Every replica must succeed.
//
// A replica waiting its turn used to wait INSIDE `SELECT pg_advisory_lock`: a
// statement that runs until the lock is free, holding a snapshot the whole
// time. CREATE INDEX CONCURRENTLY waits for every snapshot older than its own
// to go away, so the index build waited on the waiters, and the waiters on
// the run lock its session held — a cycle postgres breaks by killing one:
// `take the migration run lock: … deadlock detected (40P01)`. Reliant
// measured it with four concurrent Apply runs, 3 of 3 trials: one replica
// migrated, every other one failed its boot. A waiter now polls
// pg_try_advisory_lock and holds nothing between attempts.
//
// Both entry points race (raceReplicas): Open+Up and AutoMigrate. The hard
// deadline turns a hang into a failure rather than a stuck test binary.
func TestReplicasWaitingForTheRunLockDoNotDeadlockCreateIndexConcurrently(t *testing.T) {
	dsn := requirePG(t)
	const replicas = 4

	done := make(chan []replicaRun, 1)
	go func() {
		done <- raceReplicasQuiet(release(concurrentlyFiles), dsn, replicas)
	}()
	var runs []replicaRun
	select {
	case runs = <-done:
	case <-time.After(3 * time.Minute):
		t.Fatal("replicas racing a CREATE INDEX CONCURRENTLY did not finish within 3 minutes")
	}
	for i, r := range runs {
		if r.Err != nil {
			t.Errorf("replica %d failed: %v", i, r.Err)
		}
	}

	db := openRecordDB(t, dsn)
	var valid bool
	if err := db.QueryRow(`SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = 'items_v'`).Scan(&valid); err != nil {
		t.Fatalf("read items_v: %v", err)
	}
	if !valid {
		t.Error("items_v was left INVALID")
	}
}

// TestRunLockWaitHonoursTheContext: a run that cannot get the run lock gives
// up when its context does, instead of waiting in the database forever.
func TestRunLockWaitHonoursTheContext(t *testing.T) {
	dsn := requirePG(t)
	holder := openRecordDB(t, dsn)
	hconn, err := holder.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hconn.Close() }()
	if err := lockRun(context.Background(), hconn); err != nil {
		t.Fatal(err)
	}
	defer unlockRun(context.Background(), hconn)

	pool, err := sql.Open("pgx/v5", sqlDSN(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pool.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = Apply(ctx, pool, release(olderFiles), "migrations")
	if err == nil {
		t.Fatal("Apply took a run lock another session holds")
	}
	if waited := time.Since(start); waited > 10*time.Second {
		t.Errorf("Apply gave up %s after its context ended", waited)
	}
	if ctx.Err() == nil {
		t.Errorf("Apply failed before its context ended: %v", err)
	}
}

// raceReplicasQuiet is raceReplicas without a *testing.T, so it can run
// under the deadline above: n replicas at once, alternating Open+Up and
// AutoMigrate.
func raceReplicasQuiet(fsys fstest.MapFS, dsn string, n int) []replicaRun {
	runs := make([]replicaRun, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				mg, err := Open(Options{FS: fsys, Dir: "migrations", DSN: dsn})
				if err != nil {
					runs[i].Err = fmt.Errorf("Open: %w", err)
					return
				}
				defer func() { _ = mg.Close() }()
				res, err := mg.Up()
				if err != nil {
					err = fmt.Errorf("Up: %w", err)
				}
				runs[i] = replicaRun{Res: res, Err: err}
				return
			}
			var logs bytes.Buffer
			pool, err := sql.Open("pgx/v5", sqlDSN(dsn))
			if err == nil {
				defer func() { _ = pool.Close() }()
				err = AutoMigrate(fsys, "migrations")(pool, slog.New(slog.NewTextHandler(&logs, nil)))
			}
			if err != nil {
				err = fmt.Errorf("AutoMigrate: %w", err)
			}
			runs[i] = replicaRun{Logs: logs.String(), Err: err}
		}(i)
	}
	wg.Wait()
	return runs
}
