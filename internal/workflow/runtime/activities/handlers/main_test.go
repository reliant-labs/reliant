// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"fmt"
	"os"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
)

// TestMain boots the test Postgres and builds its migrated template before any
// test runs, so the first DB-backed serial test is not timed with the
// process's one-time setup (the parallel ones wait for the serial ones).
func TestMain(m *testing.M) {
	release, err := db.PrepareTestDatabases()
	if err != nil {
		// The DB-backed tests fail with the same error, naming themselves;
		// the rest of the package still runs.
		fmt.Fprintf(os.Stderr, "prepare test databases: %v\n", err)
	}
	code := m.Run()
	if release != nil {
		release()
	}
	os.Exit(code)
}
