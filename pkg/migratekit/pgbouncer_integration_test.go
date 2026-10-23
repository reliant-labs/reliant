//go:build integration

package migratekit

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The shape the session cap was found in, for real: PG 16 behind a
// session-mode PgBouncer allowing two server connections — reliant's prod
// pool per PgBouncer replica — and concurrent runs against a fresh database.
// A Migrator held two sessions, so two `db migrate up` runs at once deadlocked
// until PgBouncer's query_wait_timeout (45s here, as in prod) killed one with
// "open migrator: failed to open database: driver: bad connection".
//
// Tagged integration because it starts two containers (named
// migratekit-pgb-<nanos>-*, removed with their volumes on every exit path).
// sessions_pg_test.go pins the same invariant without docker.

const (
	pgbouncerImage = "edoburu/pgbouncer:latest"
	pgbouncerPG    = "postgres:16-alpine"
	// pgbouncerWait is PgBouncer's query_wait_timeout. The test's own
	// deadline is shorter, so a regression fails here instead of being
	// rescued by PgBouncer killing one of the runs.
	pgbouncerWait = 45 * time.Second
)

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// sessionPooler starts postgres and a session-mode PgBouncer capped at two
// server connections in front of it, and returns a DSN through PgBouncer.
func sessionPooler(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: starts a postgres and a pgbouncer container")
	}
	if exec.Command("docker", "version").Run() != nil {
		t.Skip("docker is not available")
	}
	for _, image := range []string{pgbouncerImage, pgbouncerPG} {
		if exec.Command("docker", "image", "inspect", image).Run() != nil {
			if out, err := exec.Command("docker", "pull", image).CombinedOutput(); err != nil {
				t.Skipf("cannot pull %s: %v\n%s", image, err, out)
			}
		}
	}
	name := fmt.Sprintf("migratekit-pgb-%d", time.Now().UnixNano())
	pg, bouncer := name+"-pg", name+"-bouncer"
	docker(t, "network", "create", name)
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-fv", bouncer, pg).Run()
		_ = exec.Command("docker", "network", "rm", name).Run()
	})
	docker(t, "run", "-d", "--name", pg, "--network", name, "-e", "POSTGRES_PASSWORD=pw", pgbouncerPG)
	// The image's init phase runs a server on the unix socket only; TCP
	// answering means the real server is up.
	waitFor(t, "postgres", func() error {
		return exec.Command("docker", "exec", pg, "pg_isready", "-h", "127.0.0.1", "-U", "postgres").Run()
	})
	docker(t, "run", "-d", "--name", bouncer, "--network", name, "-p", "127.0.0.1::5432",
		"-e", "DB_HOST="+pg, "-e", "DB_USER=postgres", "-e", "DB_PASSWORD=pw", "-e", "AUTH_TYPE=scram-sha-256",
		"-e", "POOL_MODE=session", "-e", "DEFAULT_POOL_SIZE=2", "-e", "MAX_DB_CONNECTIONS=2",
		"-e", fmt.Sprintf("QUERY_WAIT_TIMEOUT=%d", int(pgbouncerWait.Seconds())),
		pgbouncerImage)
	addr := docker(t, "port", bouncer, "5432/tcp")
	dsn := fmt.Sprintf("postgres://postgres:pw@%s/postgres?sslmode=disable", strings.Split(addr, "\n")[0])
	waitFor(t, "pgbouncer", func() error {
		db, err := sql.Open("pgx/v5", dsn)
		if err != nil {
			return err
		}
		defer func() { _ = db.Close() }()
		return db.Ping()
	})
	return dsn
}

func waitFor(t *testing.T, what string, ready func() error) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		err := ready()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s not ready after 60s: %v", what, err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func TestConcurrentRunsBehindASessionModePgBouncer(t *testing.T) {
	dsn := sessionPooler(t)

	// Three `db migrate up` runs as the scaffolded command makes them (Open,
	// State, Up) and a server booting through the boot path, all at once,
	// for two server connections.
	migrateUp := func() (Result, error) {
		mg, err := Open(Options{FS: release(olderFiles), Dir: "migrations", DSN: dsn})
		if err != nil {
			return Result{}, fmt.Errorf("Open: %w", err)
		}
		defer func() { _ = mg.Close() }()
		if _, err := mg.State(); err != nil {
			return Result{}, fmt.Errorf("State: %w", err)
		}
		return mg.Up()
	}
	boot := func() (Result, error) {
		pool, err := sql.Open("pgx/v5", dsn)
		if err != nil {
			return Result{}, err
		}
		defer func() { _ = pool.Close() }()
		return Apply(context.Background(), pool, release(olderFiles), "migrations")
	}
	runs := []func() (Result, error){migrateUp, migrateUp, migrateUp, boot}

	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, len(runs))
	start := time.Now()
	for _, run := range runs {
		go func() {
			res, err := run()
			done <- outcome{res, err}
		}()
	}
	deadline := time.After(pgbouncerWait - 10*time.Second)
	changed := 0
	for range runs {
		select {
		case o := <-done:
			if o.err != nil {
				t.Errorf("run failed: %v", o.err)
				continue
			}
			if o.res.Changed {
				changed++
			}
			if o.res.After.Version != 2 {
				t.Errorf("run ended at %+v; want version 2", o.res.After)
			}
		case <-deadline:
			t.Fatalf("runs still waiting after %s behind a two-connection session pool: each run must hold one session, including while it waits for the run lock", pgbouncerWait-10*time.Second)
		}
	}
	t.Logf("%d runs behind two server connections finished in %s", len(runs), time.Since(start).Round(time.Millisecond))
	if changed != 1 {
		t.Errorf("%d runs applied the migrations; want exactly 1", changed)
	}
}

// The run-lock wait behind a session-mode PgBouncer, with the migration the
// polling wait exists for: a CREATE INDEX CONCURRENTLY. One run builds the
// index on one server connection while another polls for the run lock on the
// second, holding no snapshot between polls, and the rest queue in PgBouncer
// for a server connection, holding none at all.
func TestConcurrentRunsOverCreateIndexConcurrentlyBehindASessionModePgBouncer(t *testing.T) {
	dsn := sessionPooler(t)
	done := make(chan []replicaRun, 1)
	go func() { done <- raceReplicasQuiet(release(concurrentlyFiles), dsn, 4) }()
	select {
	case runs := <-done:
		for i, r := range runs {
			if r.Err != nil {
				t.Errorf("replica %d failed: %v", i, r.Err)
			}
		}
	case <-time.After(pgbouncerWait - 10*time.Second):
		t.Fatalf("runs over a CREATE INDEX CONCURRENTLY still waiting after %s behind a two-connection session pool",
			pgbouncerWait-10*time.Second)
	}
}
