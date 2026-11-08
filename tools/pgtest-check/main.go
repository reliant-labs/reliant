// Copyright (c) 2025 Reliant Labs

// Command pgtest-check proves forge pgtest's shared Postgres can serve a
// database before the merge gate spends minutes on phases that need one.
//
// Every DB-backed test (internal/db/testutil.go) and forge generate's schema
// read take their database from the same place: pgtest's shared embedded
// server (booted on first use, shared by every process on the machine), or
// FORGE_TEST_POSTGRES_URL's. When that server cannot be reached or booted,
// those phases do not fail until they first need it, and then every test
// fails "connection refused" — in control-plane#2189, 15 of 60 merge gates in
// one day spent their CPU that way. So this does what a test does, through
// pgtest itself rather than a probe that could disagree with it: create a
// scratch database, run a query in it, drop it. It either succeeds in
// seconds or exits 1 with a message that names the shared server.
//
//	go run ./tools/pgtest-check [-timeout 150s]
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

func main() {
	// A fresh machine downloads the Postgres binaries first, and pgtest waits
	// up to 30s on a shared server that is alive but slow to answer before it
	// boots one of its own: the default leaves room for both.
	timeout := flag.Duration("timeout", 150*time.Second, "give up after this long")
	flag.Parse()
	start := time.Now()
	err := check(context.Background(), *timeout, pgtest.New)
	pgtest.Shutdown()
	if err != nil {
		fmt.Fprintln(os.Stderr, failure(err))
		os.Exit(1)
	}
	fmt.Printf("pgtest-check: forge pgtest's shared Postgres served a scratch database in %s\n", time.Since(start).Round(time.Millisecond))
}

// check creates a scratch database with newDB (pgtest.New), queries it and
// drops it, within timeout.
func check(ctx context.Context, timeout time.Duration, newDB func() (*sql.DB, func(), error)) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		db, cleanup, err := newDB()
		if err != nil {
			done <- err
			return
		}
		defer cleanup()
		var one int
		if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
			done <- fmt.Errorf("query the scratch database: %w", err)
			return
		}
		done <- nil
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("no scratch database after %s: %w", timeout, ctx.Err())
	}
}

// failure is the message the gate prints: what failed, that it is the
// machine's shared server and not the change, and where to look.
func failure(err error) string {
	where := "forge pgtest's shared embedded server (state and data under " + os.TempDir() + "/forge-pgtest*)"
	if u := os.Getenv(pgtest.EnvBaseURL); u != "" {
		where = pgtest.EnvBaseURL + "'s server"
	}
	msg := fmt.Sprintf(`pgtest-check: FAILED: %s could not serve a database:
  %v
Every DB-backed Go test and forge generate's schema read would fail on it
("connection refused"), so the gate stops here instead of running them.
This is the machine's shared test Postgres, not this change: rerun the gate
once it serves again.`, where, err)
	if errors.Is(err, context.DeadlineExceeded) {
		msg += "\nIt did not fail outright: it did not answer in time (a loaded host, or a wedged server)."
	}
	return msg
}
