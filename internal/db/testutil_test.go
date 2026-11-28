// Copyright (c) 2025 Reliant Labs
package db

import (
	"fmt"
	"net/url"
	"os"
	"testing"
)

// The harness's contract: every test gets a database of its own, in every
// tier (-short included), from pgtest's server — and DATABASE_URL, which is
// process configuration and may name a server with real data in it, is never
// read.
func TestTestDatabaseIgnoresDatabaseURLAndIsPerTest(t *testing.T) {
	// Not parallel: t.Setenv. Port 1 refuses every connection, so a harness
	// that read DATABASE_URL would fail here instead of handing out a database.
	t.Setenv("DATABASE_URL", "postgres://nobody:nothing@127.0.0.1:1/real_data?sslmode=disable")

	_, rawA, dsnA, cleanupA := setupTestRepoWithDSN(t)
	t.Cleanup(cleanupA)
	_, rawB, dsnB, cleanupB := setupTestRepoWithDSN(t)
	t.Cleanup(cleanupB)

	if dsnA == dsnB {
		t.Fatalf("two SetupTestDB calls shared database %s; each must get its own", dsnA)
	}
	u, err := url.Parse(dsnA)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host == "127.0.0.1:1" || u.Path == "/real_data" {
		t.Fatalf("the test database %s is DATABASE_URL's server", dsnA)
	}

	// Each copy carries the template's seed, and a write to one is invisible
	// to the other.
	if _, err := rawA.Exec(`UPDATE projects SET name = 'renamed in A' WHERE id = 'test-project'`); err != nil {
		t.Fatalf("write to A: %v", err)
	}
	var name string
	if err := rawB.QueryRow(`SELECT name FROM projects WHERE id = 'test-project'`).Scan(&name); err != nil {
		t.Fatalf("B lacks the seeded test-project: %v", err)
	}
	if name != "Test Project" {
		t.Fatalf("B's test-project is named %q after a write to A; the databases must be independent", name)
	}
}

// The warm supply PrepareTestDatabases starts hands each caller a migrated,
// seeded database of its own, and leaves none behind: neither the ones it
// handed out (dropped in the background) nor the ready ones nobody took.
func TestTestDBSupplyHandsOutOwnDatabasesAndLeavesNoneBehind(t *testing.T) {
	// Not parallel: it installs the process's supply.
	key, err := migratedTemplateKey()
	if err != nil {
		t.Fatal(err)
	}
	probe, _ := NewTestDatabase(t) // watches pg_database; made before the supply
	ownDatabases := func() int {
		t.Helper()
		var n int
		pattern := fmt.Sprintf("%%\\_%d\\_%%", os.Getpid())
		if err := probe.QueryRow(`SELECT count(*) FROM pg_database WHERE datname LIKE $1`, pattern).Scan(&n); err != nil {
			t.Fatalf("count this process's databases: %v", err)
		}
		return n
	}
	before := ownDatabases()

	s := startTestDBSupply(key)
	t.Cleanup(s.stop)
	seen := map[string]bool{}
	for i := range 4 {
		raw, dsn, cleanup := newTestDatabase(t)
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		if seen[u.Path] {
			t.Fatalf("database %s was handed out twice", u.Path)
		}
		seen[u.Path] = true
		var seeded int
		if err := raw.QueryRow(`SELECT count(*) FROM projects WHERE id = 'test-project'`).Scan(&seeded); err != nil || seeded != 1 {
			t.Fatalf("copy %d is not the migrated, seeded template: %d rows, %v", i, seeded, err)
		}
		cleanup()
	}
	s.stop()

	if after := ownDatabases(); after != before {
		t.Fatalf("this process has %d databases after the supply stopped, %d before it started", after, before)
	}
}
