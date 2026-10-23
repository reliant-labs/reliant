// Copyright (c) 2025 Reliant Labs

// Package storetest is the shared Postgres harness for internal/store: every
// test that proves a generated store or a _repo_ext.go query against a real
// database opens its database here.
//
// New hands a test its own database, migrated the production way, reachable
// through BOTH data layers over ONE pgx pool: the forge ORM client the
// internal/store queries take, and the hand db.Repo facade over them. The
// query rules are in internal/db/SCHEMA.md.
//
// The database is internal/db's test database (db.NewTestDatabase): a clone
// of the template built once per migration set on forge pgtest's server — its
// shared embedded Postgres, or FORGE_TEST_POSTGRES_URL — so a test pays a
// CREATE DATABASE … TEMPLATE, not a migration replay, and both harnesses
// share one template.
//
//forge:exclude-contract: test harness over a real database, imported only by tests; nothing to fake behind it
package storetest

import (
	"database/sql"
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db"
)

// The template is seeded with the project internal/db's harness seeds, so
// fixtures satisfy the projects foreign key on chats, worktrees and the rest.
const (
	SeedProjectID = "test-project"
	SeedUserID    = "test-user"
)

// Env is one test's private database.
type Env struct {
	// ORM is the forge ORM client: the orm.Context every generated delegate
	// and _repo_ext.go query takes, and the source of store.NewStore. It is
	// Repo.ORM(): the two share one transaction, opened from either side
	// (internal/db/SCHEMA.md, "One transaction, either layer").
	ORM *orm.Client
	// Repo is the hand Repository over the same pool, whose methods wrap
	// the internal/store queries. Credential sealing is on, with a
	// throwaway key, as in db.SetupTestDB.
	Repo *db.Repo
	// SQL is the shared pgx pool, for raw assertions.
	SQL *sql.DB
	// DSN names this test's database, for a second pool on it (Probe).
	DSN string
}

// New returns a fresh migrated database for one test, dropped when it ends.
// It runs in every tier, -short included, and never skips: a server that
// cannot boot fails the test.
func New(tb testing.TB) *Env {
	tb.Helper()
	raw, dsn := db.NewTestDatabase(tb)
	repo, err := db.NewRepoWithDriver(raw, db.DriverPostgres)
	if err != nil {
		tb.Fatalf("storetest: repo: %v", err)
	}
	db.EnableTestCredentialSealing(tb, repo)
	return &Env{ORM: repo.ORM(), Repo: repo, SQL: raw, DSN: dsn}
}
