// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Test-database isolation model
// -----------------------------
// The DB-backed tests are written against an EMPTY database: they hardcode IDs
// like "thread-1" and assume nothing else exists. Three facts about how they
// run shape the isolation strategy:
//
//  1. `go test ./...` builds one binary per package and runs those binaries
//     CONCURRENTLY, all pointed at the same DATABASE_URL.
//  2. Within a package, tests run CONCURRENTLY too: many call t.Parallel(), and
//     they reuse the same constant IDs, so each test needs its own clean slate.
//  3. The SAME package is often run by several processes at once — multiple
//     agents share this checkout, and a developer re-runs a package while CI
//     or another agent is mid-run.
//
// Point 2 is what a per-PACKAGE database cannot survive, and it is the bug this
// model exists to kill. The previous design shared one database across every
// test in a process and TRUNCATEd it at the start of each SetupTestDB call. Any
// test that ran in parallel with another therefore had its rows deleted
// mid-flight by its neighbour's setup. The result was ~30 failures per run of
// `go test ./internal/llm/tools/...`, a DIFFERENT set each time, all wearing
// the costume of an unrelated logic bug: "workflow not found", "sql: no rows in
// result set", foreign-key violations. That misdirection is the expensive part
// — it sends people to debug production code that was never wrong.
//
// So each TEST gets a real database of its own, and nothing is ever truncated.
// Copying is what makes that affordable: migrations run ONCE per process into a
// template database (~1.8s), and each SetupTestDB call issues
// CREATE DATABASE ... TEMPLATE, which Postgres serves as a file-level copy.
// Measured on this repo's schema (47 tables, PG16 on localhost:5433):
//
//	CREATE DATABASE ... TEMPLATE   ~45 ms
//	DROP DATABASE ... WITH (FORCE) ~20 ms
//	TRUNCATE every table (old way) ~370-650 ms
//
// Per-test isolation is thus roughly 6x CHEAPER than the truncation it
// replaces, on top of being correct under t.Parallel(). Copies are dropped at
// test cleanup, and reapStaleTestDBs collects anything a crash left behind.

var (
	pkgDBOnce sync.Once
	pkgDBErr  error

	// templateDSN/templateName identify the migrated, seeded database that
	// every per-test database is copied from. It is deliberately left with NO
	// open connections: CREATE DATABASE ... TEMPLATE is refused while another
	// session is connected to the template.
	templateDSN  string
	templateName string

	// adminDB is a process-wide pool on the maintenance database, used for the
	// CREATE/DROP DDL that cannot run inside the target database. Reused so a
	// per-test setup does not pay a fresh connect handshake.
	adminDB *sql.DB

	// testDBSeq numbers per-test databases within this process.
	testDBSeq atomic.Uint64
)

// testDBPrefix marks a database as belonging to this harness. Only databases
// carrying it are ever considered for reaping.
const testDBPrefix = "rlnttest_"

// NewTestRepo creates a test Repo for use in unit tests.
// It requires DATABASE_URL to be set in the environment pointing to a Postgres instance.
// Tests are skipped if DATABASE_URL is not set.
func NewTestRepo(t *testing.T) *Repo {
	t.Helper()
	repo, cleanup := SetupTestDB(t)
	t.Cleanup(cleanup)
	return repo
}

// SetupTestDB returns a Repo backed by this package's isolated test database,
// reset to an empty state (plus the seeded "test-project"). It requires
// DATABASE_URL to be set; tests are skipped otherwise.
//
// Exported for use by other packages that need to test against a real database.
func SetupTestDB(t *testing.T) (*Repo, func()) {
	t.Helper()
	repo, _, cleanup := setupTestRepo(t)
	return repo, cleanup
}

// SetupTestDBWithRawDB is like SetupTestDB but also returns the underlying *sql.DB
// for tests that need to run raw SQL queries (e.g., verifying side effects).
func SetupTestDBWithRawDB(t *testing.T) (*Repo, *sql.DB, func()) {
	t.Helper()
	return setupTestRepo(t)
}

// setupTestRepo is the shared implementation: it copies this process's migrated
// template into a database owned solely by THIS test, opens a connection to it,
// and returns a Repo. The copy already contains the seeded test project, so no
// truncation and no seeding happen here — which is precisely why the result is
// safe to use from a t.Parallel() test.
func setupTestRepo(t *testing.T) (*Repo, *sql.DB, func()) {
	t.Helper()

	baseDSN := resolveTestDSN(t)

	if err := ensureTemplateDB(baseDSN); err != nil {
		t.Fatalf("failed to prepare test template database: %v", err)
	}

	dsn, name, err := cloneTemplateDB(baseDSN)
	if err != nil {
		t.Fatalf("failed to create per-test database: %v", err)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		dropTestDB(name)
		t.Fatalf("failed to open database: %v", err)
	}

	repo := NewRepoWithDriver(db, DriverPostgres)
	cleanup := func() {
		db.Close()
		dropTestDB(name)
	}
	return repo, db, cleanup
}

// cloneTemplateDB copies the migrated template into a fresh database dedicated
// to one test, and returns its DSN and name.
//
// The name carries the pid and a per-process counter rather than t.Name():
// subtests, table-driven cases and helpers that call SetupTestDB more than once
// would all collide on a name derived from the test alone, and a name is not
// allowed to be longer than 63 bytes anyway.
func cloneTemplateDB(baseDSN string) (dsn string, name string, err error) {
	u, err := url.Parse(baseDSN)
	if err != nil {
		return "", "", fmt.Errorf("parse DATABASE_URL: %w", err)
	}

	name = fmt.Sprintf("%st%d_%d", testDBPrefix, os.Getpid(), testDBSeq.Add(1))

	//nolint:gosec // G701: both identifiers are locally derived and quoted.
	stmt := fmt.Sprintf(`CREATE DATABASE %s TEMPLATE %s`, quoteIdent(name), quoteIdent(templateName))
	if _, err := adminDB.Exec(stmt); err != nil {
		return "", "", fmt.Errorf("copy template database: %w", err)
	}

	cloneURL := *u
	cloneURL.Path = "/" + name
	return cloneURL.String(), name, nil
}

// dropTestDB removes a per-test database. WITH (FORCE) terminates any
// connection the test left behind — a pool that outlives its cleanup would
// otherwise block the drop and leak the database.
func dropTestDB(name string) {
	//nolint:gosec // G701: name is locally derived and quoted.
	_, _ = adminDB.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, quoteIdent(name)))
}

// defaultTestDSN points at this repo's own docker-compose Postgres
// (`make postgres-up`), published on 5433. It matches E2E_DATABASE_URL in the
// Makefile.
//
// 5433 and ONLY 5433. The control-plane dev stack runs a SEPARATE Postgres on
// 5434 that also hosts a `reliant` database, and it holds real data. This
// harness resets state with TRUNCATE ... CASCADE across every table in the
// public schema, so a default aimed at the wrong port would turn a routine
// test run into data loss.
const defaultTestDSN = "postgres://postgres:postgres@localhost:5433/reliant?sslmode=disable"

// resolveTestDSN returns the DSN for DB-backed tests.
//
// Skipping when no database is reachable is a deliberate convenience for
// contributors without a local Postgres — but a SILENT skip reports `ok` for a
// package whose tests never ran, which is indistinguishable from passing. That
// is how an untested change ships. So: the skip is loud, and CI can forbid it
// outright by setting REQUIRE_TEST_DB=1, which turns the skip into a failure.
func resolveTestDSN(t *testing.T) string {
	t.Helper()

	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	source := "DATABASE_URL"
	if dsn == "" {
		dsn = defaultTestDSN
		source = "default (DATABASE_URL unset)"
	}

	if err := probeDSN(dsn); err != nil {
		msg := fmt.Sprintf(
			"no test database reachable via %s (%s): %v\n"+
				"Start one with `make postgres-up`, or set DATABASE_URL.",
			source, redactDSN(dsn), err)
		if requireTestDB() {
			t.Fatalf("REQUIRE_TEST_DB=1 but %s", msg)
		}
		// Loud on purpose: this line is the only signal that an `ok` package
		// result covers zero assertions.
		t.Skipf("SKIPPING DB-BACKED TEST — %s", msg)
	}
	return dsn
}

// requireTestDB reports whether a missing database must fail rather than skip.
func requireTestDB() bool {
	v := strings.TrimSpace(os.Getenv("REQUIRE_TEST_DB"))
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}

// probeDSN checks that the server is actually reachable. sql.Open is lazy and
// never contacts the server, so without this a dead database surfaces later as
// a confusing failure inside migration or truncation.
func probeDSN(dsn string) error {
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return conn.PingContext(ctx)
}

// redactDSN strips the password so a failure message can be pasted into an
// issue without leaking a credential.
func redactDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return "<unparseable dsn>"
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			u.User = url.UserPassword(u.User.Username(), "****")
		}
	}
	return u.String()
}

// ensureTemplateDB builds, once per test process, the migrated and seeded
// database that every per-test database is copied from. It also opens the
// process-wide admin pool used for the CREATE/DROP DDL.
func ensureTemplateDB(baseDSN string) error {
	pkgDBOnce.Do(func() {
		pkgDBErr = createTemplateDB(baseDSN)
	})
	return pkgDBErr
}

func createTemplateDB(baseDSN string) error {
	u, err := url.Parse(baseDSN)
	if err != nil {
		return fmt.Errorf("parse DATABASE_URL: %w", err)
	}

	// A short, valid name unique to this package AND this process. go test sets
	// the working directory to the package directory, so the digest separates
	// packages; the pid separates concurrent runs of the same package.
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	sum := sha256.Sum256([]byte(wd))
	templateName = fmt.Sprintf("%stpl%s_%d", testDBPrefix, hex.EncodeToString(sum[:5]), os.Getpid())

	// CREATE DATABASE cannot run inside the target database, so all DDL goes
	// through a pool on the maintenance database. This pool lives for the whole
	// process: every per-test create and drop reuses it.
	admin, err := sql.Open("pgx", baseDSN)
	if err != nil {
		return fmt.Errorf("open admin connection: %w", err)
	}
	adminDB = admin

	// Databases from earlier runs are disposable; drop them before adding more.
	// Best-effort — a reaping failure must never fail a test run.
	reapStaleTestDBs(admin)

	// A template left over from a crashed run of this same pid would be stale,
	// so start from a known-empty database rather than trusting its schema.
	//nolint:gosec // G701: identifier is locally derived and quoted
	if _, err := admin.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, quoteIdent(templateName))); err != nil {
		return fmt.Errorf("drop stale template database: %w", err)
	}
	//nolint:gosec // G701: identifier is locally derived and quoted
	if _, err := admin.Exec(fmt.Sprintf(`CREATE DATABASE %s`, quoteIdent(templateName))); err != nil {
		return fmt.Errorf("create template database: %w", err)
	}

	tplURL := *u
	tplURL.Path = "/" + templateName
	templateDSN = tplURL.String()

	// Migrate and seed the template ONCE. Everything below is inherited by
	// every per-test copy, which is what makes the copies cheap.
	tplDB, err := sql.Open("pgx", templateDSN)
	if err != nil {
		return fmt.Errorf("open template database: %w", err)
	}
	if err := RunMigrations(tplDB); err != nil {
		tplDB.Close()
		return fmt.Errorf("migrate template database: %w", err)
	}
	// Seed the shared "test-project" that many tests reference by ID to satisfy
	// the projects FK on chats. Its path is a unique sentinel (not the common
	// "/tmp/test") so it never collides with tests that create their own project
	// at that path via the projects_user_id_path_key unique index.
	if _, err := tplDB.Exec(`INSERT INTO projects (id, user_id, name, path, created_at, updated_at, last_active) VALUES ('test-project', 'test-user', 'Test Project', '/tmp/reliant-test-seed-project', NOW(), NOW(), NOW()) ON CONFLICT (id) DO NOTHING`); err != nil {
		tplDB.Close()
		return fmt.Errorf("seed template database: %w", err)
	}

	// The template must have NO open connections: Postgres refuses
	// CREATE DATABASE ... TEMPLATE while another session is connected to it.
	if err := tplDB.Close(); err != nil {
		return fmt.Errorf("close template connection: %w", err)
	}

	return nil
}

// reapStaleTestDBs drops databases left behind by test processes that have
// exited.
//
// Every run leaves databases behind if it crashes, so without reaping a shared
// box accumulates hundreds of them (124 were found on the dev Postgres when
// this was written). Three guards keep it from ever touching something real:
//
//   - Only names carrying testDBPrefix are considered. A real database cannot
//     match, because the prefix is chosen by this file.
//   - Only databases owned by the CURRENT user are considered.
//   - Only databases whose creating PROCESS IS GONE are dropped.
//
// The process-liveness guard replaced a "no open connections" guard, and the
// difference matters now that databases are per-TEST rather than per-process.
// A per-test database is created and only then connected to, so there is a
// window in which it legitimately has zero connections while very much in use.
// Under the old rule a concurrent test process could drop another run's
// database inside that window — the previous design hit it rarely because it
// created one database per process; this one creates hundreds, which would
// turn a microsecond race into a routine one. Liveness has no such window: the
// owning pid is encoded in the name, and a database is garbage exactly when
// that process no longer exists.
//
// Every failure is ignored: reaping is housekeeping, and a test run must not
// fail because a concurrent process dropped the same database first.
func reapStaleTestDBs(admin *sql.DB) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rows, err := admin.QueryContext(ctx, `
		SELECT d.datname
		FROM pg_database d
		WHERE d.datname LIKE $1
		  AND pg_catalog.pg_get_userbyid(d.datdba) = current_user
	`, testDBPrefix+"%")
	if err != nil {
		return
	}

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return
		}
		if pid, ok := ownerPIDFromDBName(name); ok && !processAlive(pid) {
			names = append(names, name)
		}
	}
	rows.Close()

	for _, name := range names {
		//nolint:gosec // G701: name came from pg_database, filtered by our own
		// prefix, and is quoted.
		_, _ = admin.ExecContext(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, quoteIdent(name)))
	}
}

// ownerPIDFromDBName extracts the pid of the test process that created a
// database this harness named. Every shape it has ever used encodes one:
//
//	rlnttest_t<pid>_<seq>        per-test copy
//	rlnttest_tpl<digest>_<pid>   template, one per process
//	rlnttest_schemasql_<pid>     schema-drift scratch database
//	rlnttest_<digest>_<pid>      legacy per-package database
//
// The legacy shape is still parsed because databases created before this change
// are otherwise unreachable garbage: nothing else knows their naming scheme, so
// dropping the case would strand them on the server permanently.
//
// The per-test shape is the only one whose pid is not the final field, and it
// is distinguishable without ambiguity: a digest is hex, so a legacy or
// template name can never begin with "t".
//
// A name that does not parse returns false and is left alone, so an unfamiliar
// naming scheme is never guessed at.
func ownerPIDFromDBName(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, testDBPrefix)
	if !ok {
		return 0, false
	}

	var pidField string
	if strings.HasPrefix(rest, "t") && !strings.HasPrefix(rest, "tpl") {
		// rlnttest_t<pid>_<seq>: pid is the first field, minus the "t".
		idx := strings.Index(rest, "_")
		if idx < 0 {
			return 0, false
		}
		pidField = rest[1:idx]
	} else {
		// Every other shape puts the pid last.
		idx := strings.LastIndex(rest, "_")
		if idx < 0 {
			return 0, false
		}
		pidField = rest[idx+1:]
	}

	pid, err := strconv.Atoi(pidField)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// processAlive reports whether a pid currently exists. Signal 0 performs the
// permission and existence checks without delivering anything. A pid owned by
// another user reports EPERM, which still proves the process EXISTS — so that
// case must be treated as alive, not reaped.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}

// quoteIdent double-quotes a Postgres identifier for safe interpolation into
// DDL (database names cannot be parameterized).
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
