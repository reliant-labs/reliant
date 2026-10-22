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
// The database is a clone of a template built once per migration set
// (forge pgtest.NewFromTemplate), so a test pays a CREATE DATABASE … TEMPLATE,
// not a migration replay. pgtest opens pgx, production's driver.
//
//forge:exclude-contract: test harness over a real database, imported only by tests; nothing to fake behind it
package storetest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/crypto"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/pgtest"

	migrations "github.com/reliant-labs/reliant/db"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/vault"
)

// The template is seeded with the same project internal/db's harness seeds
// (db.SetupTestDB), so fixtures written for either harness satisfy the
// projects foreign key on chats, worktrees and the rest.
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

// New returns a fresh migrated database for one test. It is DB-backed, so
// it skips under -short (unless REQUIRE_TEST_DB=1) and when no server is
// configured (failing instead under REQUIRE_TEST_DB=1), as internal/db's
// harness does.
//
// The server is FORGE_TEST_POSTGRES_URL (a maintenance DSN, database
// "postgres"), or else the server DATABASE_URL names — the variable
// `make test` exports. Like internal/db's harness it never picks a server on
// its own: pgtest would otherwise boot an embedded Postgres.
func New(tb testing.TB) *Env {
	tb.Helper()
	if testing.Short() && !requireTestDB() {
		tb.Skip("DB-backed test; skipped under -short. Run without -short, or set REQUIRE_TEST_DB=1 to include it")
	}
	base := serverDSN(tb)

	clone, cleanup, err := pgtest.NewFromTemplate(templateKey(tb), func(build *sql.DB) error {
		return buildTemplate(build, base)
	})
	if err != nil {
		tb.Fatalf("storetest: template database: %v", err)
	}
	tb.Cleanup(cleanup)

	repo, err := db.NewRepoWithDriver(clone, db.DriverPostgres)
	if err != nil {
		tb.Fatalf("storetest: repo: %v", err)
	}
	enableSealing(tb, repo)
	var name string
	if err := clone.QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
		tb.Fatalf("storetest: database name: %v", err)
	}
	dsn, err := withDatabase(base, name)
	if err != nil {
		tb.Fatalf("storetest: %v", err)
	}
	return &Env{ORM: repo.ORM(), Repo: repo, SQL: clone, DSN: dsn}
}

// buildTemplate migrates the empty template database through db.Migrate —
// the path a server boots on — and seeds the shared project.
func buildTemplate(build *sql.DB, base string) error {
	var name string
	if err := build.QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
		return fmt.Errorf("template name: %w", err)
	}
	dsn, err := withDatabase(base, name)
	if err != nil {
		return err
	}
	if _, err := db.Migrate(context.Background(), dsn); err != nil {
		return fmt.Errorf("migrate template: %w", err)
	}
	_, err = build.Exec(`INSERT INTO projects (id, user_id, name, path, created_at, updated_at, last_active)
		VALUES ($1, $2, 'Test Project', '/tmp/reliant-test-seed-project', NOW(), NOW(), NOW())`, SeedProjectID, SeedUserID)
	if err != nil {
		return fmt.Errorf("seed template: %w", err)
	}
	return nil
}

// enableSealing gives the Repo a throwaway vault: provider keys and sign-in
// tokens exist only sealed, so the twin cannot write them without one.
func enableSealing(tb testing.TB, repo *db.Repo) {
	tb.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		tb.Fatalf("storetest: vault key: %v", err)
	}
	ring, err := crypto.ParseKeyring("v1:" + base64.StdEncoding.EncodeToString(key))
	if err != nil {
		tb.Fatalf("storetest: parse vault key: %v", err)
	}
	repo.EnableCredentialSealing(vault.NewInstrumented(vault.Deps{DB: repo.ORM(), Keys: vault.NewEnvKeyWrapper(ring)}))
}

var (
	serverOnce sync.Once
	serverURL  string
	serverErr  string
)

// serverDSN resolves the maintenance DSN once per process and points pgtest
// at it. pgtest reads FORGE_TEST_POSTGRES_URL when it first boots, so a
// server taken from DATABASE_URL is exported under that name before any
// pgtest call.
func serverDSN(tb testing.TB) string {
	tb.Helper()
	serverOnce.Do(func() {
		if v := strings.TrimSpace(os.Getenv(pgtest.EnvBaseURL)); v != "" { //nolint:forbidigo // test-harness server DSN (pgtest's variable), not process configuration
			serverURL = v
			return
		}
		appDSN := strings.TrimSpace(os.Getenv("DATABASE_URL")) //nolint:forbidigo // test-harness DSN for DB-backed tests, as internal/db's harness reads it
		if appDSN == "" {
			serverErr = fmt.Sprintf("neither %s nor DATABASE_URL is set, and DB-backed tests never pick a database server on their own", pgtest.EnvBaseURL)
			return
		}
		maintenance, err := withDatabase(appDSN, "postgres")
		if err != nil {
			serverErr = err.Error()
			return
		}
		if err := os.Setenv(pgtest.EnvBaseURL, maintenance); err != nil { //nolint:forbidigo // hands pgtest the server, before it boots
			serverErr = fmt.Sprintf("export %s: %v", pgtest.EnvBaseURL, err)
			return
		}
		serverURL = maintenance
	})
	if serverErr != "" {
		if requireTestDB() {
			tb.Fatalf("REQUIRE_TEST_DB=1 but %s", serverErr)
		}
		tb.Skipf("SKIPPING DB-BACKED TEST — %s", serverErr)
	}
	return serverURL
}

// withDatabase is dsn with its database replaced by name.
func withDatabase(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse postgres DSN: %w", err)
	}
	u.Path = "/" + name
	return u.String(), nil
}

// templateKey names the template after the migration set, so an edited or
// added migration never gets a stale template.
func templateKey(tb testing.TB) string {
	tb.Helper()
	src := migrations.Source()
	var names []string
	if err := fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			names = append(names, path)
		}
		return err
	}); err != nil {
		tb.Fatalf("storetest: walk migrations: %v", err)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		body, err := fs.ReadFile(src, n)
		if err != nil {
			tb.Fatalf("storetest: read %s: %v", n, err)
		}
		h.Write([]byte(n))
		h.Write(body)
	}
	return "reliant_store_" + hex.EncodeToString(h.Sum(nil))[:12]
}

// requireTestDB reports whether a missing database must fail rather than skip.
func requireTestDB() bool {
	v := strings.TrimSpace(os.Getenv("REQUIRE_TEST_DB")) //nolint:forbidigo // test-harness switch (REQUIRE_TEST_DB), not process configuration
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}
