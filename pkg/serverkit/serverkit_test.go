package serverkit_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3" // for TestApplyDBPoolTuning_AppliesNonZero

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// freeAddr binds 127.0.0.1:0, captures the assigned port, then closes
// the listener so Run can re-bind. There's a tiny race window but it's
// adequate for in-process tests.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freeAddr listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// emptyHandler is the minimal composed handler: a mux with nothing
// mounted. Tests that don't care about service responses use it.
func emptyHandler() http.Handler { return http.NewServeMux() }

// shutdownRecorder tracks OnShutdown invocations so tests can assert the
// caller-composed teardown ran during graceful shutdown.
type shutdownRecorder struct {
	count atomic.Int32
	err   error
}

func (s *shutdownRecorder) OnShutdown(context.Context) error {
	s.count.Add(1)
	return s.err
}

// stubWorker records Start/Stop hits so tests can assert ordering.
type stubWorker struct {
	name      string
	startedAt atomic.Int64
	stoppedAt atomic.Int64
	startErr  error
}

func (w *stubWorker) Name() string { return w.name }
func (w *stubWorker) Start(ctx context.Context) error {
	w.startedAt.Store(time.Now().UnixNano())
	<-ctx.Done()
	return w.startErr
}
func (w *stubWorker) Stop(context.Context) error {
	w.stoppedAt.Store(time.Now().UnixNano())
	return nil
}

// ctxWorker implements the optional ContextWorker extension. It records
// which lifecycle method the supervisor picked so tests can assert
// RunContext is PREFERRED over the legacy Start when both exist.
type ctxWorker struct {
	name         string
	runCalled    atomic.Bool
	startCalled  atomic.Bool
	cancelledAt  atomic.Int64
	stopCalled   atomic.Bool
	runReturnErr error // returned from RunContext after ctx is done
}

func (w *ctxWorker) Name() string { return w.name }

// Start is the legacy path — it must NOT be called when RunContext is
// available.
func (w *ctxWorker) Start(ctx context.Context) error {
	w.startCalled.Store(true)
	<-ctx.Done()
	return nil
}

func (w *ctxWorker) Stop(context.Context) error {
	w.stopCalled.Store(true)
	return nil
}

func (w *ctxWorker) RunContext(ctx context.Context) error {
	w.runCalled.Store(true)
	<-ctx.Done()
	w.cancelledAt.Store(time.Now().UnixNano())
	return w.runReturnErr
}

// cronishWorker mimics the cron worker scaffold at the supervisor level:
// RunContext schedules ticks and each tick derives a per-tick context
// from the worker lifecycle ctx. The tick body deliberately blocks until
// its per-tick ctx is done — like a long-running cron job that only
// finishes because shutdown interrupted it.
type cronishWorker struct {
	name            string
	tickStarted     atomic.Bool
	tickInterrupted atomic.Bool
	stopCalled      atomic.Bool
}

func (w *cronishWorker) Name() string { return w.name }

func (w *cronishWorker) Start(context.Context) error {
	return errors.New("legacy Start must not be called for a ContextWorker")
}

func (w *cronishWorker) Stop(context.Context) error {
	w.stopCalled.Store(true)
	return nil
}

func (w *cronishWorker) RunContext(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// Per-tick context derived from the worker lifecycle ctx —
			// the same shape the cron scaffold's baseCtx produces.
			tickCtx, cancel := context.WithCancel(ctx)
			w.tickStarted.Store(true)
			<-tickCtx.Done() // "job" runs until shutdown interrupts it
			cancel()
			w.tickInterrupted.Store(true)
			return nil
		}
	}
}

// runInBackground starts serverkit.Run on a goroutine and returns the
// error channel. Tests block on it after driving shutdown.
func runInBackground(t *testing.T, cfg serverkit.Config, srv serverkit.Server) (errCh chan error, addr string) {
	t.Helper()
	addr = cfg.Addr
	errCh = make(chan error, 1)
	go func() {
		errCh <- serverkit.Run(context.Background(), cfg, srv)
	}()
	return errCh, addr
}

// getDrained issues a GET and fully consumes the response body before
// returning, which is what lets net/http return the connection to the idle
// pool instead of leaving it half-read.
//
// This matters at SHUTDOWN, not during the request. http.Server.Shutdown
// closes connections it considers idle and then waits for the remainder to
// finish — so a connection the client left mid-body is indistinguishable from
// a request still being served, and Shutdown blocks on it until the budget
// expires. Under -race the timing widened enough to make that deterministic:
// TestRun_ProbesBypassEdgeMiddleware failed 100% of the time on a clean
// origin/main with `go test -race -count=1`, reporting only "Run did not
// return within 5s".
//
// Callers that then drive shutdown should also CloseIdleConnections, since a
// drained-but-pooled keep-alive is still a connection the server is entitled
// to wait on.
func getDrained(url string) (*http.Response, error) {
	resp, err := http.Get(url) //nolint:gosec,noctx // test-local URL, no context needed
	if err != nil {
		return nil, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp, nil
}

// waitReady polls /readyz until it returns 200 or the deadline expires.
func waitReady(t *testing.T, addr string, deadline time.Duration) {
	t.Helper()
	url := "http://" + addr + "/readyz"
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		resp, err := http.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("readyz did not return 200 within %s", deadline)
}

// requireSelfSIGTERM skips a Run-lifecycle test on Windows, and must be the
// test's FIRST statement — skipping after Run starts would leave it serving in
// the background, and shutdownAndWait is sometimes called from a goroutine,
// where a skip cannot end the test at all.
//
// These tests end Run the way an orchestrator does: SIGTERM, delivered by
// signalling their own process. Windows has no way to deliver it —
// os.Process.Signal(SIGTERM) fails with "not supported by windows" — so the
// trigger itself does not exist there. What the tests assert (readiness flip,
// pre-stop drain, worker and OnShutdown ordering) is platform-independent Go,
// and runs on every Linux job.
func requireSelfSIGTERM(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("ends Run with a self-delivered SIGTERM, which Windows cannot send " +
			"(os.Process.Signal: not supported by windows); the lifecycle is covered on Linux")
	}
}

// shutdownAndWait triggers SIGTERM and waits for Run to return. Every test
// that calls it starts with requireSelfSIGTERM.
func shutdownAndWait(t *testing.T, errCh chan error, within time.Duration) error {
	t.Helper()
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("find self: %v", err)
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	select {
	case e := <-errCh:
		return e
	case <-time.After(within):
		t.Fatalf("Run did not return within %s", within)
	}
	return nil
}

func baseConfig(addr string) serverkit.Config {
	return serverkit.Config{
		Addr:            addr,
		LogFormat:       "text",
		LogLevel:        slog.LevelError,
		PreStopDelay:    10 * time.Millisecond,
		ShutdownTimeout: 5 * time.Second,
	}
}

func TestRun_RequiresHandler(t *testing.T) {
	t.Parallel()
	err := serverkit.Run(context.Background(), serverkit.Config{Addr: ":0"}, serverkit.Server{})
	if err == nil || !contains(err.Error(), "Server.Handler is required") {
		t.Fatalf("expected Handler-required error, got %v", err)
	}
}

func TestRun_RequiresAddr(t *testing.T) {
	t.Parallel()
	err := serverkit.Run(context.Background(), serverkit.Config{}, serverkit.Server{Handler: emptyHandler()})
	if err == nil || !contains(err.Error(), "Addr is required") {
		t.Fatalf("expected Addr-required error, got %v", err)
	}
}

func TestRun_RequiresRunOperatorsWhenOperatorsPresent(t *testing.T) {
	t.Parallel()
	srv := serverkit.Server{
		Handler:   emptyHandler(),
		Operators: []serverkit.Operator{&stubOperator{name: "op"}},
		// RunOperators left nil — config error.
	}
	err := serverkit.Run(context.Background(), baseConfig(":0"), srv)
	if err == nil || !contains(err.Error(), "RunOperators is nil") {
		t.Fatalf("expected RunOperators-required error, got %v", err)
	}
}

func TestRun_StartsAndShutsDownCleanly(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — sends SIGTERM to the test process.
	addr := freeAddr(t)
	rec := &shutdownRecorder{}
	srv := serverkit.Server{Handler: emptyHandler(), OnShutdown: rec.OnShutdown}
	errCh, _ := runInBackground(t, baseConfig(addr), srv)
	waitReady(t, addr, 2*time.Second)
	if err := shutdownAndWait(t, errCh, 5*time.Second); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if rec.count.Load() == 0 {
		t.Fatal("Server.OnShutdown was never called")
	}
}

// TestRun_HealthzReadyzLifecycle proves the probes are served by
// serverkit's own top mux (the caller's Handler need not mount them) and
// that /readyz reflects the lifecycle.
func TestRun_HealthzReadyzLifecycle(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — sends SIGTERM.
	addr := freeAddr(t)
	srv := serverkit.Server{Handler: emptyHandler()}
	errCh, _ := runInBackground(t, baseConfig(addr), srv)
	waitReady(t, addr, 2*time.Second)

	// /healthz returns 200 now.
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.StatusCode)
	}

	if err := shutdownAndWait(t, errCh, 5*time.Second); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

// TestRun_ProbesBypassEdgeMiddleware proves /healthz + /readyz are
// routed by serverkit's top mux IN FRONT of the edge wrap: a CORS
// middleware that would tag every response it sees must NOT touch the
// probe responses, while it DOES wrap the caller's handler.
func TestRun_ProbesBypassEdgeMiddleware(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — sends SIGTERM.
	addr := freeAddr(t)

	inner := http.NewServeMux()
	inner.HandleFunc("/app", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	const marker = "X-Edge-Touched"
	corsFactory := func(_ []string, _ bool) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(marker, "1")
				next.ServeHTTP(w, r)
			})
		}
	}

	cfg := baseConfig(addr)
	cfg.CORSOrigins = []string{"https://example.com"}
	srv := serverkit.Server{Handler: inner, CORSMiddleware: corsFactory}
	errCh, _ := runInBackground(t, cfg, srv)
	waitReady(t, addr, 2*time.Second)

	// Probe: must NOT carry the edge marker.
	probeResp, err := getDrained("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	if probeResp.Header.Get(marker) != "" {
		t.Fatal("/healthz was wrapped by the edge middleware — probes must bypass the edge")
	}

	// App route: MUST carry the edge marker (the edge does wrap the
	// caller's handler).
	appResp, err := getDrained("http://" + addr + "/app")
	if err != nil {
		t.Fatalf("app: %v", err)
	}
	if appResp.Header.Get(marker) == "" {
		t.Fatal("/app was not wrapped by the edge middleware — the caller's handler must sit behind the edge")
	}

	// Hang up before asking the server to stop. Shutdown closes IDLE
	// connections and then waits for the rest, so a client-side keep-alive
	// this test never releases is counted as in-flight work and burns the
	// whole shutdown budget. See getDrained.
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()

	if err := shutdownAndWait(t, errCh, 5*time.Second); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestRun_WorkerLifecycle(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — sends SIGTERM.
	addr := freeAddr(t)
	w := &stubWorker{name: "alpha"}
	srv := serverkit.Server{Handler: emptyHandler(), Workers: []serverkit.Worker{w}}
	errCh, _ := runInBackground(t, baseConfig(addr), srv)
	waitReady(t, addr, 2*time.Second)

	// Worker should have started by now.
	if w.startedAt.Load() == 0 {
		t.Fatal("worker.Start was never called before readyz returned 200")
	}

	if err := shutdownAndWait(t, errCh, 5*time.Second); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if w.stoppedAt.Load() == 0 {
		t.Fatal("worker.Stop was never called")
	}
	if w.startedAt.Load() > w.stoppedAt.Load() {
		t.Fatalf("worker stopped (%d) before it started (%d)?", w.stoppedAt.Load(), w.startedAt.Load())
	}
}

// TestRun_ContextWorkerPreferred proves the supervisor picks RunContext
// over the legacy Start when a worker implements ContextWorker, cancels
// the per-worker ctx on shutdown so the worker exits promptly, and still
// calls Stop afterwards. A legacy worker rides along to prove the
// fallback path is untouched by the new lifecycle.
func TestRun_ContextWorkerPreferred(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — sends SIGTERM.
	addr := freeAddr(t)
	cw := &ctxWorker{name: "ctx-aware", runReturnErr: context.Canceled}
	legacy := &stubWorker{name: "legacy"}
	srv := serverkit.Server{Handler: emptyHandler(), Workers: []serverkit.Worker{cw, legacy}}
	errCh, _ := runInBackground(t, baseConfig(addr), srv)
	waitReady(t, addr, 2*time.Second)

	deadline := time.Now().Add(2 * time.Second)
	for !cw.runCalled.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !cw.runCalled.Load() {
		t.Fatal("RunContext was never called for a ContextWorker")
	}

	start := time.Now()
	if err := shutdownAndWait(t, errCh, 5*time.Second); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if cw.startCalled.Load() {
		t.Fatal("legacy Start was called even though the worker implements ContextWorker")
	}
	if cw.cancelledAt.Load() == 0 {
		t.Fatal("ctx-aware worker never observed ctx cancellation")
	}
	if !cw.stopCalled.Load() {
		t.Fatal("Stop was not called on the ctx-aware worker after RunContext returned")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("shutdown took %s — ctx-aware worker did not exit promptly", elapsed)
	}

	if legacy.startedAt.Load() == 0 {
		t.Fatal("legacy worker Start was never called")
	}
	if legacy.stoppedAt.Load() == 0 {
		t.Fatal("legacy worker Stop was never called")
	}
}

// TestRun_CronShapedContextWorker proves a cron-style RunContext — per-
// tick contexts derived from the worker lifecycle ctx — observes
// shutdown mid-tick.
func TestRun_CronShapedContextWorker(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — sends SIGTERM.
	addr := freeAddr(t)
	cw := &cronishWorker{name: "cronish"}
	srv := serverkit.Server{Handler: emptyHandler(), Workers: []serverkit.Worker{cw}}
	errCh, _ := runInBackground(t, baseConfig(addr), srv)
	waitReady(t, addr, 2*time.Second)

	deadline := time.Now().Add(2 * time.Second)
	for !cw.tickStarted.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !cw.tickStarted.Load() {
		t.Fatal("cron-shaped worker never started a tick")
	}

	if err := shutdownAndWait(t, errCh, 5*time.Second); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !cw.tickInterrupted.Load() {
		t.Fatal("in-flight tick did not observe ctx cancellation on shutdown")
	}
	if !cw.stopCalled.Load() {
		t.Fatal("Stop was not called on the cron-shaped worker")
	}
}

// TestRun_OnShutdownCalledDuringShutdown pins the OnShutdown contract:
// the caller-composed teardown (old Application.Shutdown + OTel flush)
// runs exactly once during graceful shutdown.
func TestRun_OnShutdownCalledDuringShutdown(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — sends SIGTERM.
	addr := freeAddr(t)
	var shutdownObserved atomic.Bool
	srv := serverkit.Server{
		Handler: emptyHandler(),
		OnShutdown: func(context.Context) error {
			shutdownObserved.Store(true)
			return nil
		},
	}
	errCh, _ := runInBackground(t, baseConfig(addr), srv)
	waitReady(t, addr, 2*time.Second)
	if err := shutdownAndWait(t, errCh, 5*time.Second); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !shutdownObserved.Load() {
		t.Fatal("OnShutdown was not called during graceful shutdown")
	}
}

func TestRun_ShutdownWithinBudget(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — sends SIGTERM.
	addr := freeAddr(t)
	srv := serverkit.Server{Handler: emptyHandler()}
	cfg := baseConfig(addr)
	cfg.ShutdownTimeout = 500 * time.Millisecond
	errCh, _ := runInBackground(t, cfg, srv)
	waitReady(t, addr, 2*time.Second)

	start := time.Now()
	if err := shutdownAndWait(t, errCh, cfg.ShutdownTimeout+cfg.PreStopDelay+1*time.Second); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	elapsed := time.Since(start)
	maxAllowed := cfg.ShutdownTimeout + cfg.PreStopDelay + 500*time.Millisecond
	if elapsed > maxAllowed {
		t.Fatalf("shutdown took %s, want <= %s", elapsed, maxAllowed)
	}
}

func TestApplyDBPoolTuning_NilDBNoop(t *testing.T) {
	t.Parallel()
	// Should not panic.
	serverkit.ApplyDBPoolTuning(nil, serverkit.DBPoolTuning{
		MaxOpenConns:    10,
		MaxIdleConns:    5,
		ConnMaxIdleTime: time.Minute,
		ConnMaxLifetime: time.Hour,
	})
}

func TestApplyDBPoolTuning_AppliesNonZero(t *testing.T) {
	t.Parallel()
	// Open an in-memory SQLite handle — we don't actually exercise it,
	// just confirm pool tuning calls don't error.
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Skipf("sqlite3 driver not available: %v", err)
	}
	defer db.Close()
	serverkit.ApplyDBPoolTuning(db, serverkit.DBPoolTuning{
		MaxOpenConns:    7,
		MaxIdleConns:    3,
		ConnMaxIdleTime: 30 * time.Second,
		ConnMaxLifetime: 2 * time.Minute,
	})
	stats := db.Stats()
	if stats.MaxOpenConnections != 7 {
		t.Fatalf("MaxOpenConnections = %d, want 7", stats.MaxOpenConnections)
	}
}

// contains is a tiny strings.Contains shim — kept inline so the test
// file has no non-stdlib runtime deps beyond serverkit.
func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// Compile-time check: stubWorker satisfies Worker.
var _ serverkit.Worker = (*stubWorker)(nil)

// Compile-time checks: the ctx-aware stubs satisfy ContextWorker (and
// therefore Worker, which ContextWorker embeds).
var (
	_ serverkit.ContextWorker = (*ctxWorker)(nil)
	_ serverkit.ContextWorker = (*cronishWorker)(nil)
)

// Used by error-wrap tests that check the message contains a substring.
var _ = fmt.Sprintf
