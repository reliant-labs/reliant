package serverkit_test

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// dbconnect_test.go — the startup connect used to be one 15s ping whose
// failure was fatal. In prod a pooler that came up a minute after its
// clients crash-looped three services. These tests pin the replacement: it
// retries, stays alive and not-ready while it does, logs once per bucket, and
// fails only after its budget.

// fastPolicy is a policy with millisecond timings so retries are observable
// without sleeping.
func fastPolicy(logger *slog.Logger) serverkit.ConnectPolicy {
	return serverkit.ConnectPolicy{
		Timeout:        5 * time.Second,
		AttemptTimeout: time.Second,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
		LogEvery:       time.Hour, // one WARN bucket for the whole test
		Logger:         logger,
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func bufLogger(buf *syncBuf) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// fakeDriver is a database/sql driver whose Open fails the first `failures`
// times with a refused-connection error, then succeeds — a stand-in for a
// pooler that is not up yet.
type fakeDriver struct {
	failures atomic.Int32
	opens    atomic.Int32
}

func (d *fakeDriver) Open(string) (driver.Conn, error) {
	n := d.opens.Add(1)
	if n <= d.failures.Load() {
		return nil, fmt.Errorf("dial tcp 10.0.0.5:6432: %w", syscall.ECONNREFUSED)
	}
	return fakeConn{}, nil
}

type fakeConn struct{}

func (fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (fakeConn) Close() error                        { return nil }
func (fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("unsupported") }

var fakeDriverSeq atomic.Int32

func registerFake(t *testing.T, failures int32) (name string, d *fakeDriver) {
	t.Helper()
	d = &fakeDriver{}
	d.failures.Store(failures)
	name = fmt.Sprintf("serverkit-fake-%d", fakeDriverSeq.Add(1))
	sql.Register(name, d)
	return name, d
}

func TestOpenDB_RetriesThenSucceeds(t *testing.T) {
	t.Parallel()
	name, d := registerFake(t, 4)
	buf := &syncBuf{}

	start := time.Now()
	db, err := serverkit.OpenDB(t.Context(), name, "ignored", serverkit.DBPoolTuning{}, fastPolicy(bufLogger(buf)))
	if err != nil {
		t.Fatalf("OpenDB: %v — a database that is up after 4 refusals must be reached", err)
	}
	defer func() { _ = db.Close() }()

	if got := d.opens.Load(); got != 5 {
		t.Errorf("dial attempts = %d, want 5 (4 failures then success)", got)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("took %s — backoff is not capped", time.Since(start))
	}

	out := buf.String()
	// ONE warn for the whole bucket, however many attempts failed in it.
	if n := strings.Count(out, "level=WARN"); n != 1 {
		t.Errorf("WARN lines = %d, want 1 per bucket (4 failed attempts):\n%s", n, out)
	}
	if !strings.Contains(out, "error_class=connection_refused") {
		t.Errorf("WARN does not carry the error class:\n%s", out)
	}
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "database connected") ||
		!strings.Contains(out, "elapsed=") || !strings.Contains(out, "attempts=5") {
		t.Errorf("success must log INFO with elapsed and attempts:\n%s", out)
	}
}

func TestOpenDB_FirstTrySuccessLogsNoWarn(t *testing.T) {
	t.Parallel()
	name, _ := registerFake(t, 0)
	buf := &syncBuf{}
	db, err := serverkit.OpenDB(t.Context(), name, "ignored", serverkit.DBPoolTuning{}, fastPolicy(bufLogger(buf)))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	_ = db.Close()
	if strings.Contains(buf.String(), "WARN") {
		t.Errorf("a healthy first connect must not warn:\n%s", buf.String())
	}
}

func TestOpenDB_GivesUpAfterTheTotalTimeout(t *testing.T) {
	t.Parallel()
	name, d := registerFake(t, 1<<30)
	p := fastPolicy(bufLogger(&syncBuf{}))
	p.Timeout = 200 * time.Millisecond

	start := time.Now()
	db, err := serverkit.OpenDB(t.Context(), name, "ignored", serverkit.DBPoolTuning{}, p)
	elapsed := time.Since(start)
	if err == nil {
		_ = db.Close()
		t.Fatal("OpenDB succeeded against a database that never comes up")
	}
	if db != nil {
		t.Error("a failed OpenDB must not return a pool")
	}

	var te *serverkit.ConnectTimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("error = %v (%T), want *ConnectTimeoutError so the exit is explainable", err, err)
	}
	if te.Attempts < 2 {
		t.Errorf("attempts = %d — it did not retry before giving up", te.Attempts)
	}
	if te.Class != "connection_refused" {
		t.Errorf("class = %q, want connection_refused", te.Class)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Errorf("the last attempt's error must stay reachable via errors.Is: %v", err)
	}
	if !strings.Contains(err.Error(), "not reachable after") {
		t.Errorf("error does not say what happened: %v", err)
	}
	if elapsed < p.Timeout || elapsed > p.Timeout+time.Second {
		t.Errorf("gave up after %s, want ~%s", elapsed, p.Timeout)
	}
	if d.opens.Load() < 2 {
		t.Errorf("only %d dials", d.opens.Load())
	}
}

func TestRetryConnect_StopsWhenContextIsCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	var calls atomic.Int32
	p := fastPolicy(bufLogger(&syncBuf{}))
	p.Timeout = time.Minute

	done := make(chan error, 1)
	go func() {
		done <- serverkit.RetryConnect(ctx, p, func(context.Context) error {
			calls.Add(1)
			return errors.New("still down")
		})
	}()
	for calls.Load() < 2 {
		time.Sleep(time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled — SIGTERM during the wait must end it promptly", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RetryConnect ignored cancellation")
	}
}

func TestRetryConnect_BoundsEachAttempt(t *testing.T) {
	t.Parallel()
	// A blackholed host never errors; without a per-attempt bound the first
	// attempt would eat the whole budget and nothing would retry.
	var attempts atomic.Int32
	p := fastPolicy(bufLogger(&syncBuf{}))
	p.AttemptTimeout = 50 * time.Millisecond
	p.Timeout = 400 * time.Millisecond

	err := serverkit.RetryConnect(t.Context(), p, func(ctx context.Context) error {
		attempts.Add(1)
		<-ctx.Done()
		return ctx.Err()
	})
	var te *serverkit.ConnectTimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("error = %v, want *ConnectTimeoutError", err)
	}
	if attempts.Load() < 3 {
		t.Errorf("attempts = %d — a hung attempt consumed the budget", attempts.Load())
	}
	if te.Class != "timeout" {
		t.Errorf("class = %q, want timeout", te.Class)
	}
}

func TestRetryConnect_WarnsOncePerBucket(t *testing.T) {
	t.Parallel()
	buf := &syncBuf{}
	p := fastPolicy(bufLogger(buf))
	p.LogEvery = 40 * time.Millisecond
	p.InitialBackoff, p.MaxBackoff = 2*time.Millisecond, 2*time.Millisecond

	var n atomic.Int32
	err := serverkit.RetryConnect(t.Context(), p, func(context.Context) error {
		if n.Add(1) > 60 {
			return nil
		}
		return errors.New("down")
	})
	if err != nil {
		t.Fatalf("RetryConnect: %v", err)
	}
	warns := strings.Count(buf.String(), "level=WARN")
	if warns < 1 || warns >= 60/2 {
		t.Errorf("WARN lines = %d for 60 failed attempts — want one per bucket, not per attempt", warns)
	}
}

func TestAutoMigrateContext_RetriesTheConnectThenMigrates(t *testing.T) {
	t.Parallel()
	name, d := registerFake(t, 3)
	var migrated atomic.Bool
	cfg := serverkit.Config{
		AutoMigrate: true, DatabaseURL: "ignored", DBDriver: name,
		DBConnectTimeout: 5 * time.Second,
	}
	err := serverkit.AutoMigrateContext(t.Context(), cfg, bufLogger(&syncBuf{}),
		func(*sql.DB, *slog.Logger) error { migrated.Store(true); return nil })
	if err != nil {
		t.Fatalf("AutoMigrateContext: %v", err)
	}
	if !migrated.Load() {
		t.Error("migration did not run after the connect recovered")
	}
	if d.opens.Load() < 4 {
		t.Errorf("dials = %d, want >= 4", d.opens.Load())
	}
}

func TestAutoMigrateContext_ExitsAfterTheTimeout(t *testing.T) {
	t.Parallel()
	name, _ := registerFake(t, 1<<30)
	cfg := serverkit.Config{
		AutoMigrate: true, DatabaseURL: "ignored", DBDriver: name,
		DBConnectTimeout: 150 * time.Millisecond,
	}
	err := serverkit.AutoMigrateContext(t.Context(), cfg, bufLogger(&syncBuf{}),
		func(*sql.DB, *slog.Logger) error { t.Error("migrate ran without a database"); return nil })
	var te *serverkit.ConnectTimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("error = %v, want a *ConnectTimeoutError wrapped by AutoMigrate", err)
	}
	if !strings.Contains(err.Error(), "failed to connect to database for migration") {
		t.Errorf("error lost its context: %v", err)
	}
}

func TestAutoMigrateContext_MigrationFailureIsNotRetried(t *testing.T) {
	t.Parallel()
	name, d := registerFake(t, 0)
	var calls atomic.Int32
	cfg := serverkit.Config{AutoMigrate: true, DatabaseURL: "ignored", DBDriver: name}
	err := serverkit.AutoMigrateContext(t.Context(), cfg, bufLogger(&syncBuf{}),
		func(*sql.DB, *slog.Logger) error { calls.Add(1); return errors.New("schema is dirty") })
	if err == nil || !strings.Contains(err.Error(), "schema is dirty") {
		t.Fatalf("error = %v, want the migration failure", err)
	}
	if calls.Load() != 1 {
		t.Errorf("migrate ran %d times — a dirty schema is for a human, not a retry", calls.Load())
	}
	_ = d
}

// TestAwaitMigrationsContext_NamesTheSchemaWhileItWaits: a migrate=wait
// process dials (retrying), then blocks in the await with "schema" pending on
// the boot /readyz — whatever AutoMigrate says — and proceeds once it returns.
func TestAwaitMigrationsContext_NamesTheSchemaWhileItWaits(t *testing.T) {
	t.Parallel()
	addr := freeAddr(t)
	name, _ := registerFake(t, 2)
	cfg := serverkit.Config{Addr: addr, AutoMigrate: false, DatabaseURL: "ignored", DBDriver: name, DBConnectTimeout: 5 * time.Second}
	ctx, boot, err := serverkit.StartBootProbes(t.Context(), cfg, bufLogger(&syncBuf{}))
	if err != nil {
		t.Fatalf("StartBootProbes: %v", err)
	}
	defer boot.Close()

	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- serverkit.AwaitMigrationsContext(ctx, cfg, bufLogger(&syncBuf{}), func(ctx context.Context, db *sql.DB, _ *slog.Logger) error {
			if db == nil {
				return errors.New("no database handed to the await")
			}
			<-release
			return nil
		})
	}()

	var raw string
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if _, _, raw = getJSON(t, "http://"+addr+"/readyz"); strings.Contains(raw, `"name":"schema"`) {
			break
		}
	}
	if !strings.Contains(raw, `"name":"schema"`) {
		t.Fatalf("readyz does not name the schema while the process waits for it: %s", raw)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("AwaitMigrationsContext: %v", err)
	}
	if _, _, raw = getJSON(t, "http://"+addr+"/readyz"); strings.Contains(raw, `"name":"schema"`) {
		t.Errorf("the schema stays pending after the wait returned: %s", raw)
	}

	if err := serverkit.AwaitMigrationsContext(t.Context(), serverkit.Config{}, nil, nil); err == nil || !strings.Contains(err.Error(), "DatabaseURL") {
		t.Errorf("a waiter with no database must say so; got %v", err)
	}
}

func TestConfigNormalize_FillsDBConnectTimeout(t *testing.T) {
	t.Parallel()
	var cfg serverkit.Config
	cfg.Normalize()
	if cfg.DBConnectTimeout != serverkit.DefaultDBConnectTimeout {
		t.Errorf("DBConnectTimeout = %s, want %s", cfg.DBConnectTimeout, serverkit.DefaultDBConnectTimeout)
	}
	if serverkit.DefaultDBConnectTimeout < 2*time.Minute {
		t.Errorf("DefaultDBConnectTimeout = %s — too short to ride out a pooler rollout", serverkit.DefaultDBConnectTimeout)
	}
}

// TestBootProbes_AliveButNotReadyWhileTheDatabaseIsDown is the incident in
// one test: while the database is unreachable the process listens, liveness
// passes, readiness fails NAMING the database, and when the database appears
// and Run takes the address over, readiness becomes true.
func TestBootProbes_AliveButNotReadyWhileTheDatabaseIsDown(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — Run sends SIGTERM.
	addr := freeAddr(t)
	cfg := baseConfig(addr)
	name, d := registerFake(t, 1<<30)

	ctx, boot, err := serverkit.StartBootProbes(t.Context(), cfg, bufLogger(&syncBuf{}))
	if err != nil {
		t.Fatalf("StartBootProbes: %v", err)
	}
	defer boot.Close()

	p := fastPolicy(bufLogger(&syncBuf{}))
	p.Timeout = 30 * time.Second
	p.InitialBackoff, p.MaxBackoff = 5*time.Millisecond, 20*time.Millisecond
	type openResult struct {
		db  *sql.DB
		err error
	}
	opened := make(chan openResult, 1)
	go func() {
		db, err := serverkit.OpenDB(ctx, name, "ignored", serverkit.DBPoolTuning{}, p)
		opened <- openResult{db, err}
	}()

	// The pending wait is registered asynchronously; poll until it shows.
	var raw string
	var status int
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		status, _, raw = getJSON(t, "http://"+addr+"/readyz")
		if strings.Contains(raw, `"name":"database"`) {
			break
		}
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("readyz = %d while the database is down, want 503 (body %s)", status, raw)
	}
	if !strings.Contains(raw, `"name":"database"`) || !strings.Contains(raw, `"ok":false`) {
		t.Errorf("readyz does not name the database: %s", raw)
	}
	if strings.Contains(raw, "10.0.0.5") || strings.Contains(raw, "refused") {
		t.Errorf("readyz leaked connection detail: %s", raw)
	}
	hStatus, _, hRaw := getJSON(t, "http://"+addr+"/healthz")
	if hStatus != http.StatusOK || strings.TrimSpace(hRaw) != "ok" {
		t.Errorf("healthz = %d %q while waiting, want 200 ok — liveness must not follow a dependency", hStatus, hRaw)
	}

	// The database comes up.
	d.failures.Store(0)
	var res openResult
	select {
	case res = <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("OpenDB never recovered after the database came up")
	}
	if res.err != nil {
		t.Fatalf("OpenDB: %v", res.err)
	}
	defer func() { _ = res.db.Close() }()

	// Connected but not yet composed: still not ready, and no longer pending.
	if status, _, raw = getJSON(t, "http://"+addr+"/readyz"); status != http.StatusServiceUnavailable {
		t.Errorf("readyz = %d between connect and Run, want 503 until the server is composed (body %s)", status, raw)
	}

	// Composition done: Run takes the address over and readiness turns true.
	srv := serverkit.Server{Handler: emptyHandler()}
	srv.AddReadyCheck(serverkit.DBReadyCheck("database", res.db))
	errCh := make(chan error, 1)
	go func() { errCh <- serverkit.Run(ctx, cfg, srv) }()
	waitReady(t, addr, 5*time.Second)

	status, body, raw := getJSON(t, "http://"+addr+"/readyz")
	if status != http.StatusOK || body["status"] != "ready" {
		t.Errorf("readyz = %d %s after the database came up, want 200/ready", status, raw)
	}
	if err := shutdownAndWait(t, errCh, 10*time.Second); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestBootProbes_NilIsInert(t *testing.T) {
	t.Parallel()
	var b *serverkit.BootProbes
	b.Close()
	if serverkit.BootProbesFrom(t.Context()) != nil {
		t.Error("BootProbesFrom on a bare context must be nil")
	}
	// RetryConnect without boot probes must work (a CLI, a test).
	if err := serverkit.RetryConnect(t.Context(), fastPolicy(bufLogger(&syncBuf{})),
		func(context.Context) error { return nil }); err != nil {
		t.Fatalf("RetryConnect without boot probes: %v", err)
	}
}

func TestBootProbes_ListenErrorIsReported(t *testing.T) {
	t.Parallel()
	addr := freeAddr(t)
	cfg := baseConfig(addr)
	_, first, err := serverkit.StartBootProbes(t.Context(), cfg, nil)
	if err != nil {
		t.Fatalf("StartBootProbes: %v", err)
	}
	defer first.Close()
	if _, _, err := serverkit.StartBootProbes(t.Context(), cfg, nil); err == nil {
		t.Fatal("second bind on the same address succeeded")
	}
}
