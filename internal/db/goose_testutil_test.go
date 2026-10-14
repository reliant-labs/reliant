// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// The goose-history tests (access-tokens renumber repair, the GitHub sender,
// provider-token sealing, workflow owner and daemon-binding migrations) pin
// what particular goose migrations do to a database in a particular state.
// The goose bridge still runs those migrations, on any database an older goose
// release left behind, so they still matter — but they need a database GOOSE
// built, with goose_db_version, which the baseline-built template is not. This
// is that template, built once per test process the way every goose release
// built a database: RunMigrations on an empty one.

var (
	gooseTemplateOnce sync.Once
	gooseTemplateErr  error
	gooseTemplateName string
)

// setupGooseTestDBWithRawDB is SetupTestDBWithRawDB over a copy of a database
// goose migrated to its frozen head (plus the seeded "test-project").
func setupGooseTestDBWithRawDB(t *testing.T) (*Repo, *sql.DB, func()) {
	t.Helper()
	baseDSN := resolveTestDSN(t)
	// Opens the process-wide admin pool and names this process's templates.
	if err := ensureTemplateDB(baseDSN); err != nil {
		t.Fatalf("failed to prepare test template database: %v", err)
	}
	gooseTemplateOnce.Do(func() { gooseTemplateErr = createGooseTemplateDB(baseDSN) })
	if gooseTemplateErr != nil {
		t.Fatalf("failed to prepare the goose-built template database: %v", gooseTemplateErr)
	}
	return setupTestRepoFrom(t, baseDSN, gooseTemplateName)
}

// startUp runs what the api-server runs at boot, Migrate, against the
// database raw is open on.
func startUp(t *testing.T, raw *sql.DB) error {
	t.Helper()
	var name string
	if err := raw.QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
		t.Fatalf("read the test database's name: %v", err)
	}
	u, err := url.Parse(resolveTestDSN(t))
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	u.Path = "/" + name
	_, err = Migrate(context.Background(), u.String(), slog.New(slog.DiscardHandler))
	return err
}

func createGooseTemplateDB(baseDSN string) error {
	// rlnttest_tplgoose<digest>_<pid>: the template shape, so the reaper
	// collects it once this process is gone.
	gooseTemplateName = strings.Replace(templateName, testDBPrefix+"tpl", testDBPrefix+"tplgoose", 1)

	//nolint:gosec // G701: identifier is locally derived and quoted
	if _, err := adminDB.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, quoteIdent(gooseTemplateName))); err != nil {
		return fmt.Errorf("drop stale goose template database: %w", err)
	}
	//nolint:gosec // G701: identifier is locally derived and quoted
	if _, err := adminDB.Exec(fmt.Sprintf(`CREATE DATABASE %s`, quoteIdent(gooseTemplateName))); err != nil {
		return fmt.Errorf("create goose template database: %w", err)
	}

	u, err := url.Parse(baseDSN)
	if err != nil {
		return fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	u.Path = "/" + gooseTemplateName
	tplDB, err := sql.Open("pgx", u.String())
	if err != nil {
		return fmt.Errorf("open goose template database: %w", err)
	}
	defer func() { _ = tplDB.Close() }()

	if err := RunMigrations(tplDB); err != nil {
		return fmt.Errorf("migrate goose template database: %w", err)
	}
	if _, err := tplDB.Exec(`INSERT INTO projects (id, user_id, name, path, created_at, updated_at, last_active) VALUES ('test-project', 'test-user', 'Test Project', '/tmp/reliant-test-seed-project', NOW(), NOW(), NOW()) ON CONFLICT (id) DO NOTHING`); err != nil {
		return fmt.Errorf("seed goose template database: %w", err)
	}
	// The deferred Close leaves the template with no open connections, which
	// CREATE DATABASE ... TEMPLATE requires.
	return nil
}
