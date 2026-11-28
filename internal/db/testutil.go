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
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/crypto"
	"github.com/reliant-labs/forge/pkg/pgtest"
	forgedb "github.com/reliant-labs/reliant/db"
	"github.com/reliant-labs/reliant/internal/credentials/connectionvault"
	"github.com/reliant-labs/reliant/internal/credentials/orgaccounts"
	"github.com/reliant-labs/reliant/internal/credentials/provideraccounts"
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
// project at that path via the projects_org_user_path_key unique index.
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
// RelaxProjectKeysForTest dropped the per-user project keys from a test's
// database, to test release 1's readers on release 2's schema. Release 2's
// migrations drop them for every database now, so it changes nothing; it is
// kept so the tests that call it read the same on both releases.
func RelaxProjectKeysForTest(t *testing.T, repo *Repo) {
	t.Helper()
	for _, stmt := range []string{
		`ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_user_id_path_key`,
		`DROP INDEX IF EXISTS projects_user_remote_url_uniq`,
	} {
		if _, err := repo.ORM().Bun().ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("relax project keys: %s: %v", stmt, err)
		}
	}
}

// HoldProjectOnMachineForTest records that the machine holds a checkout of
// the project, as the connect reconcile would on finding it, unless it has a
// record of it already or the user detached it: a machine's config snapshot
// of a project is stored only while it does (SyncProjectConfigSnapshot). It
// never overwrites a record a test wrote.
func HoldProjectOnMachineForTest(tb testing.TB, repo *Repo, projectID, daemonID string) {
	tb.Helper()
	if _, err := repo.ORM().Bun().ExecContext(context.Background(), `
INSERT INTO project_daemons (project_id, daemon_id, path, install_state)
SELECT ?0, ?1, '/work/' || ?1, 'installed'
WHERE NOT EXISTS (SELECT 1 FROM project_daemon_detachments WHERE project_id = ?0 AND daemon_id = ?1)
ON CONFLICT (project_id, daemon_id) DO NOTHING`, projectID, daemonID); err != nil {
		tb.Fatalf("hold project %s on machine %s: %v", projectID, daemonID, err)
	}
}

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

// PrepareTestDatabases attaches the test server and builds the migrated
// template before any test runs, for a package's TestMain: the server boot and
// the migrations are a one-time cost of the process, and without this the
// first test to ask for a database is timed with them (seconds on a loaded
// machine, against the merge gate's per-test budget). The returned release
// holds the server for the process; call it after m.Run.
//
// It also starts the process's warm supply of test databases (testDBSupply):
// copies made ahead of the tests that will ask for them, and dropped after
// those tests end without holding them up. release stops the supply and waits
// for every drop it started.
func PrepareTestDatabases() (release func(), err error) {
	key, err := migratedTemplateKey()
	if err != nil {
		return nil, fmt.Errorf("test template key: %w", err)
	}
	raw, _, cleanup, err := pgtest.NewFromTemplate(key, func(b *sql.DB, dsn string) error {
		return buildMigratedTemplate(dsn, b)
	})
	if err != nil {
		return nil, fmt.Errorf("test database (template %s): %w", key, err)
	}
	s := startTestDBSupply(key)
	return func() {
		s.stop()
		_ = raw.Close()
		cleanup()
	}, nil
}

// testDBSupply takes the copy and the drop of a test's database off the test's
// own time, for a process whose TestMain called PrepareTestDatabases.
//
// A copy takes ~0.1s on an idle machine and ~0.4s on a loaded one, and a drop
// about half that. A test that runs without t.Parallel() pays both in series
// with every other such test: in internal/workflow/runtime/activities/handlers
// that was ~18s of 26s idle, and ~90s of 107s on a loaded host, before any
// parallel test started. So workers keep a few copies ready while those tests
// run, and a test's cleanup hands its drop to a goroutine. The drops stay
// bounded (dropSlots): past that, a cleanup drops its database itself, so a
// burst of finishing tests cannot leave an unbounded backlog of databases on
// the server. A copy is still the test's own, from the same template, so
// nothing a test can observe changes.
type testDBSupply struct {
	ready     chan preparedTestDB
	done      chan struct{}
	workers   sync.WaitGroup
	dropSlots chan struct{}
	stopOnce  sync.Once

	mu      sync.Mutex
	stopped bool
	drops   sync.WaitGroup
}

type preparedTestDB struct {
	raw     *sql.DB
	dsn     string
	cleanup func()
}

const (
	testDBSupplyWorkers = 2
	testDBSupplyReady   = 2
	testDBSupplyDrops   = 4
)

var (
	activeSupplyMu sync.Mutex
	activeSupply   *testDBSupply
)

func startTestDBSupply(key string) *testDBSupply {
	s := &testDBSupply{
		ready:     make(chan preparedTestDB, testDBSupplyReady),
		done:      make(chan struct{}),
		dropSlots: make(chan struct{}, testDBSupplyDrops),
	}
	for range testDBSupplyWorkers {
		s.workers.Add(1)
		go s.fill(key)
	}
	activeSupplyMu.Lock()
	activeSupply = s
	activeSupplyMu.Unlock()
	return s
}

func currentTestDBSupply() *testDBSupply {
	activeSupplyMu.Lock()
	defer activeSupplyMu.Unlock()
	return activeSupply
}

// fill makes copies until stop. A copy that fails ends the worker: the test
// that next asks makes its own and reports the error under its name.
func (s *testDBSupply) fill(key string) {
	defer s.workers.Done()
	for {
		select {
		case <-s.done:
			return
		default:
		}
		raw, dsn, cleanup, err := pgtest.NewFromTemplate(key, func(b *sql.DB, dsn string) error {
			return buildMigratedTemplate(dsn, b)
		})
		if err != nil {
			return
		}
		select {
		case s.ready <- preparedTestDB{raw: raw, dsn: dsn, cleanup: cleanup}:
		case <-s.done:
			cleanup()
			return
		}
	}
}

// take returns a ready copy, or false when none is ready yet.
func (s *testDBSupply) take() (preparedTestDB, bool) {
	select {
	case d := <-s.ready:
		return d, true
	default:
		return preparedTestDB{}, false
	}
}

// dropLater wraps cleanup to drop the database in the background while a drop
// slot is free, and in the caller otherwise or once the supply has stopped.
func (s *testDBSupply) dropLater(cleanup func()) func() {
	return func() {
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			cleanup()
			return
		}
		select {
		case s.dropSlots <- struct{}{}:
		default:
			s.mu.Unlock()
			cleanup()
			return
		}
		s.drops.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.drops.Done()
			defer func() { <-s.dropSlots }()
			cleanup()
		}()
	}
}

// stop ends the workers, drops the copies nobody took, and waits for every
// background drop. Calls after the first do nothing.
func (s *testDBSupply) stop() { s.stopOnce.Do(s.stopNow) }

func (s *testDBSupply) stopNow() {
	activeSupplyMu.Lock()
	if activeSupply == s {
		activeSupply = nil
	}
	activeSupplyMu.Unlock()
	close(s.done)
	s.workers.Wait()
	close(s.ready)
	for d := range s.ready {
		d.cleanup()
	}
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
	s.drops.Wait()
}

// NewTestVault returns a credential vault over repo's database with a throwaway key,
// for a test of a credential package: the sealed columns exist only sealed.
func NewTestVault(tb testing.TB, repo *Repo) vault.Service {
	tb.Helper()
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		tb.Fatalf("generate test vault key: %v", err)
	}
	ring, err := crypto.ParseKeyring("v1:" + base64.StdEncoding.EncodeToString(keyBytes))
	if err != nil {
		tb.Fatalf("parse test vault key: %v", err)
	}
	return vault.NewInstrumented(vault.Deps{DB: repo.ORM(), Keys: vault.NewEnvKeyWrapper(ring)})
}

// EnableTestCredentialSealing gives repo a vault with a throwaway key: provider
// keys and sign-in tokens exist only sealed, so a test Repo cannot write them
// without one.
func EnableTestCredentialSealing(tb testing.TB, repo *Repo) {
	tb.Helper()
	repo.testVault = NewTestVault(tb, repo)
}

// setupTestRepo is the shared implementation: a Repo, with credential sealing,
// over a fresh copy of the migrated template.
func setupTestRepo(t *testing.T) (*Repo, *sql.DB, func()) {
	t.Helper()
	raw, _, cleanup := newTestDatabase(t)
	return repoOverTestDB(t, raw, cleanup)
}

// setupTestRepoWithDSN is setupTestRepo plus the DSN of the database, for a
// test that opens a pool of its own on it.
func setupTestRepoWithDSN(t *testing.T) (*Repo, *sql.DB, string, func()) {
	t.Helper()
	raw, dsn, cleanup := newTestDatabase(t)
	repo, raw, cleanup := repoOverTestDB(t, raw, cleanup)
	return repo, raw, dsn, cleanup
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
	s := currentTestDBSupply()
	if s == nil {
		return cloneTestTemplate(tb, key, buildMigratedTemplate)
	}
	if d, ok := s.take(); ok {
		return d.raw, d.dsn, s.dropLater(d.cleanup)
	}
	raw, dsn, cleanup := cloneTestTemplate(tb, key, buildMigratedTemplate)
	return raw, dsn, s.dropLater(cleanup)
}

// buildMigratedTemplate migrates and seeds the empty template database at dsn.
func buildMigratedTemplate(dsn string, build *sql.DB) error {
	// Migrate is the path a server boots on; it opens and closes its own
	// connections.
	if _, err := Migrate(context.Background(), dsn); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if _, err := build.Exec(testSeedSQL); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	return nil
}

// cloneTestTemplate copies key's template into a fresh database, building the
// template first with build when no process has yet. build receives the empty
// template database's DSN and an open pool on it, which it must not use once
// it returns. key must change whenever what build produces does.
func cloneTestTemplate(tb testing.TB, key string, build func(dsn string, db *sql.DB) error) (*sql.DB, string, func()) {
	tb.Helper()
	raw, dsn, cleanup, err := pgtest.NewFromTemplate(key, func(b *sql.DB, dsn string) error {
		return build(dsn, b)
	})
	if err != nil {
		tb.Fatalf("test database (template %s): %v", key, err)
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
	accounts, err := provideraccounts.NewInstrumented(provideraccounts.Deps{DB: repo.ORM(), Sealer: repo.testVault})
	if err != nil {
		t.Fatalf("seed %s account: %v", provider, err)
	}
	acct, _, err := accounts.UpsertProviderAccount(context.Background(), u)
	if err != nil {
		t.Fatalf("seed %s account: %v", provider, err)
	}
	return acct
}

// TestAccounts is the provider-account store over repo's database and a throwaway vault: what a
// test uses to seed or read accounts now that the facade does not.
func TestAccounts(tb testing.TB, repo *Repo) provideraccounts.Service {
	tb.Helper()
	accounts, err := provideraccounts.NewInstrumented(provideraccounts.Deps{DB: repo.ORM(), Sealer: repo.testVault})
	if err != nil {
		tb.Fatalf("provider accounts: %v", err)
	}
	return accounts
}

// TestOrgAccounts is the org-credential store over repo's database and a throwaway vault: what a
// test uses to administer an org's accounts, grants and seats now that the facade does not.
func TestOrgAccounts(tb testing.TB, repo *Repo) orgaccounts.Service {
	tb.Helper()
	org, err := orgaccounts.NewInstrumented(orgaccounts.Deps{DB: repo.ORM(), Sealer: repo.testVault})
	if err != nil {
		tb.Fatalf("org accounts: %v", err)
	}
	return org
}

// TestConnections is the connection store over repo's database: what a test uses to read and
// write connections now that the facade does not.
func TestConnections(tb testing.TB, repo *Repo) connectionvault.Service {
	tb.Helper()
	conns, err := connectionvault.NewInstrumented(connectionvault.Deps{DB: repo.ORM()})
	if err != nil {
		tb.Fatalf("connection vault: %v", err)
	}
	return conns
}
