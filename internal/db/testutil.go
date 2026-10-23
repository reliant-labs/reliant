// Copyright (c) 2025 Reliant Labs
package db

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
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/crypto"
	"github.com/reliant-labs/forge/pkg/pgtest"
	forgedb "github.com/reliant-labs/reliant/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/vault"
)

// Test databases
// --------------
// Every DB-backed test gets a database of its own, on a server it never has to
// be told about.
//
// The server is forge pgtest's: an embedded Postgres of the major forge.yaml
// declares (database.postgres_version), booted on a free port on first use and
// SHARED by every test process of this user on the machine — every package
// binary of a `go test ./...`, every worktree, every agent — with a
// cross-process refcount, so a fan-out runs one server rather than one per
// process. No docker, no fixed port, no setup step. FORGE_TEST_POSTGRES_URL (a
// maintenance DSN, database "postgres") points the run at a server of your
// own instead, and must run the declared major (pgtest checks).
//
// DATABASE_URL is never read here. It is the server's process configuration,
// and a dev shell that exported the control-plane stack's (5434, real data)
// would otherwise point `go test` at it.
//
// The DB-backed tests are written against an EMPTY database — they hardcode IDs
// like "thread-1" — and run under t.Parallel() alongside one another, so a
// database shared by two tests corrupts both (the per-package TRUNCATE design
// this replaced failed ~30 different tests per run of internal/llm/tools). So
// each TEST gets a copy: the migrations run once per migration set, into a
// template on the server, and each test gets CREATE DATABASE … TEMPLATE, a
// file-level copy (~45ms, against ~1.8s to migrate). pgtest publishes the
// template under a key that hashes what built it, so an edited migration never
// gets a stale template, and builds it once between all the processes that
// want it. Copies are dropped at cleanup.
//
// The tests run in every tier, -short included: the migrations reach the test
// binary through go:embed, so Go's test cache sees an edit to them, and nothing
// a test reads lives outside the binary and its own database.

// testSeedSQL seeds the shared "test-project" that many tests reference by ID
// to satisfy the projects FK on chats. Its path is a unique sentinel (not the
// common "/tmp/test") so it never collides with tests that create their own
// project at that path via the projects_user_id_path_key unique index.
const testSeedSQL = `INSERT INTO projects (id, user_id, name, path, created_at, updated_at, last_active)
	VALUES ('test-project', 'test-user', 'Test Project', '/tmp/reliant-test-seed-project', NOW(), NOW(), NOW())
	ON CONFLICT (id) DO NOTHING`

// NewTestRepo returns a Repo over a fresh database of this test's own,
// dropped when the test ends. See "Test databases" above.
func NewTestRepo(t *testing.T) *Repo {
	t.Helper()
	repo, cleanup := SetupTestDB(t)
	t.Cleanup(cleanup)
	return repo
}

// SetupTestDB returns a Repo over a fresh database of its own — migrated, plus
// the seeded "test-project" — and the cleanup that drops it. The caller owns
// the cleanup, so a database can outlive the test that made it (a stack shared
// by a whole test binary).
//
// Exported for use by other packages that need to test against a real database.
func SetupTestDB(t *testing.T) (*Repo, func()) {
	t.Helper()
	repo, _, cleanup := setupTestRepo(t)
	return repo, cleanup
}

// RetryBackoffs is the record RecordRetryBackoffs keeps: one entry per backoff
// RunTx would have slept before a retry.
type RetryBackoffs struct {
	mu       sync.Mutex
	backoffs []time.Duration
}

// Len is how many retries RunTx has started since recording began.
func (b *RetryBackoffs) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.backoffs)
}

// RecordRetryBackoffs makes repo record RunTx's backoffs instead of sleeping
// them, so a test can pin how many retries a call ran (none, for an error that
// must not be retried) by counting rather than by timing it against a wall
// clock a loaded machine can stretch past any bound. Set it before the calls it
// observes.
//
// Exported for use by other packages' tests; call it only on a test's own Repo.
func RecordRetryBackoffs(repo *Repo) *RetryBackoffs {
	rec := &RetryBackoffs{}
	repo.retrySleep = func(d time.Duration) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		rec.backoffs = append(rec.backoffs, d)
	}
	return rec
}

// SetupTestDBWithRawDB is like SetupTestDB but also returns the underlying *sql.DB
// for tests that need to run raw SQL queries (e.g., verifying side effects).
func SetupTestDBWithRawDB(t *testing.T) (*Repo, *sql.DB, func()) {
	t.Helper()
	return setupTestRepo(t)
}

// NewTestDatabase returns a fresh database of this test's own — migrated, plus
// the seeded "test-project" — as an open pool and its DSN, dropped when tb
// ends. It is the database SetupTestDB builds its Repo over, for a harness that
// assembles its own (internal/store/storetest).
func NewTestDatabase(tb testing.TB) (raw *sql.DB, dsn string) {
	tb.Helper()
	raw, dsn, cleanup := newTestDatabase(tb)
	tb.Cleanup(cleanup)
	return raw, dsn
}

// EnableTestCredentialSealing gives repo a vault with a throwaway key: provider
// keys and sign-in tokens exist only sealed, so a test Repo cannot write them
// without one.
func EnableTestCredentialSealing(tb testing.TB, repo *Repo) {
	tb.Helper()
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		tb.Fatalf("generate test vault key: %v", err)
	}
	ring, err := crypto.ParseKeyring("v1:" + base64.StdEncoding.EncodeToString(keyBytes))
	if err != nil {
		tb.Fatalf("parse test vault key: %v", err)
	}
	repo.EnableCredentialSealing(vault.NewInstrumented(vault.Deps{DB: repo.ORM(), Keys: vault.NewEnvKeyWrapper(ring)}))
}

// setupTestRepo is the shared implementation: a Repo, with credential sealing,
// over a fresh copy of the migrated template.
func setupTestRepo(t *testing.T) (*Repo, *sql.DB, func()) {
	t.Helper()
	raw, _, cleanup := newTestDatabase(t)
	return repoOverTestDB(t, raw, cleanup)
}

// repoOverTestDB builds a sealing Repo over raw, a database cleanup drops.
func repoOverTestDB(t *testing.T, raw *sql.DB, cleanup func()) (*Repo, *sql.DB, func()) {
	t.Helper()
	repo, err := NewRepoWithDriver(raw, DriverPostgres)
	if err != nil {
		cleanup()
		t.Fatalf("failed to build repo: %v", err)
	}
	EnableTestCredentialSealing(t, repo)
	return repo, raw, cleanup
}

// newTestDatabase copies the migrated, seeded template into a fresh database
// and returns it, its DSN, and the cleanup that drops it.
func newTestDatabase(tb testing.TB) (*sql.DB, string, func()) {
	tb.Helper()
	key, err := migratedTemplateKey()
	if err != nil {
		tb.Fatalf("test template key: %v", err)
	}
	return cloneTestTemplate(tb, key, func(dsn string, build *sql.DB) error {
		// Migrate is the path a server boots on; it opens and closes its
		// own connections.
		if _, err := Migrate(context.Background(), dsn); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		if _, err := build.Exec(testSeedSQL); err != nil {
			return fmt.Errorf("seed: %w", err)
		}
		return nil
	})
}

// cloneTestTemplate copies key's template into a fresh database, building the
// template first with build when no process has yet. build receives the empty
// template database's DSN and an open pool on it, which it must not use once
// it returns. key must change whenever what build produces does.
func cloneTestTemplate(tb testing.TB, key string, build func(dsn string, db *sql.DB) error) (*sql.DB, string, func()) {
	tb.Helper()
	raw, cleanup, err := pgtest.NewFromTemplate(key, func(b *sql.DB) error {
		dsn, err := testDSNOf(b)
		if err != nil {
			return err
		}
		return build(dsn, b)
	})
	if err != nil {
		tb.Fatalf("test database (template %s): %v", key, err)
	}
	dsn, err := testDSNOf(raw)
	if err != nil {
		cleanup()
		tb.Fatalf("test database: %v", err)
	}
	return raw, dsn, cleanup
}

// migratedTemplateKey names the migrated template after everything that
// builds it: the embedded migration set and the seed.
var migratedTemplateKey = sync.OnceValues(func() (string, error) {
	return testTemplateKey("reliant_", forgedb.Source(), ".", testSeedSQL)
})

// testTemplateKey is prefix plus a digest of every file under dir in fsys and
// of extra.
func testTemplateKey(prefix string, fsys fs.FS, dir, extra string) (string, error) {
	var names []string
	if err := fs.WalkDir(fsys, dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			names = append(names, path)
		}
		return err
	}); err != nil {
		return "", fmt.Errorf("walk migrations: %w", err)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		body, err := fs.ReadFile(fsys, n)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", n, err)
		}
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write(body)
		h.Write([]byte{0})
	}
	h.Write([]byte(extra))
	return prefix + hex.EncodeToString(h.Sum(nil))[:12], nil
}

// testServerURL is the maintenance DSN of the test server: pgtest's shared
// embedded one, or FORGE_TEST_POSTGRES_URL. The reference it takes is held for
// the life of the process, as pgtest holds its own; the pool forgets a process
// once it exits.
var testServerURL = sync.OnceValues(func() (string, error) {
	base, _, err := pgtest.AcquireShared()
	return base, err
})

// testServerDSN is the DSN of the database named name on the test server.
func testServerDSN(name string) (string, error) {
	base, err := testServerURL()
	if err != nil {
		return "", err
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse the test server's DSN: %w", err)
	}
	u.Path = "/" + name
	return u.String(), nil
}

// testDSNOf is the DSN of the database db is connected to, read back from the
// connection.
func testDSNOf(db *sql.DB) (string, error) {
	var name string
	if err := db.QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
		return "", fmt.Errorf("read the test database's name: %w", err)
	}
	return testServerDSN(name)
}

// SeedProviderAccount connects userID to provider with secret, the way a sign-in or an added key
// would: managed for "reliant", a device token for "copilot", an OAuth session (with refresh
// token) for the other subscription providers, an API key for the rest. For tests that need a
// connected provider and care about nothing else.
func SeedProviderAccount(t *testing.T, repo *Repo, userID, provider, secret string) core.ProviderAccount {
	t.Helper()
	u := core.AccountUpsert{UserID: userID, Provider: provider, Secrets: core.AccountSecrets{Secret: secret}}
	switch provider {
	case "reliant":
		u.AuthKind, u.IdentityKey = core.AuthKindManaged, core.ManagedIdentityKey
	case "copilot":
		u.AuthKind, u.IdentityKey = core.AuthKindDevice, core.GitHubIdentityKey("seed")
	case "claude", "codex", "antigravity":
		u.AuthKind, u.IdentityKey = core.AuthKindOAuth, core.PlaceholderIdentityKey("seed-"+provider)
		u.Secrets.Refresh = provider + "-refresh"
		u.ExpiresAt = time.Now().Add(time.Hour)
	default:
		u.AuthKind, u.IdentityKey = core.AuthKindAPIKey, core.APIKeyIdentityKey(userID, provider, secret)
		u.KeyHint = core.KeyHint(secret)
	}
	acct, _, err := repo.UpsertProviderAccount(context.Background(), u)
	if err != nil {
		t.Fatalf("seed %s account: %v", provider, err)
	}
	return acct
}
