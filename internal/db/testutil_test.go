// Copyright (c) 2025 Reliant Labs
package db

import (
	"net/url"
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

	_, rawA, cleanupA := SetupTestDBWithRawDB(t)
	t.Cleanup(cleanupA)
	_, rawB, cleanupB := SetupTestDBWithRawDB(t)
	t.Cleanup(cleanupB)

	dsnA, err := testDSNOf(rawA)
	if err != nil {
		t.Fatal(err)
	}
	dsnB, err := testDSNOf(rawB)
	if err != nil {
		t.Fatal(err)
	}
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
