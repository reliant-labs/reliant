package serverkit_test

import (
	"context"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// drainingOperator's RunOperators blocks until ctx is done, then does a
// brief "drain" (the controller-runtime ctx-cancel unwind) before
// returning. drained flips true only AFTER that drain completes, so a
// test can prove Run waited for the operator goroutine to finish before
// returning instead of exiting mid-reconcile.
type drainingOperator struct {
	name    string
	drained atomic.Bool
}

func (o *drainingOperator) Name() string { return o.name }

// TestRun_WaitsForOperatorDrainOnShutdown pins FIX 1: the operator/
// controller-manager goroutine is supervised on a WaitGroup, so Run must
// not return until RunOperators has fully drained.
func TestRun_WaitsForOperatorDrainOnShutdown(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — sends SIGTERM.
	addr := freeAddr(t)
	op := &drainingOperator{name: "draining-op"}
	srv := serverkit.Server{
		Handler:   emptyHandler(),
		Operators: []serverkit.Operator{op},
		RunOperators: func(ctx context.Context, _ *slog.Logger, _ string) error {
			<-ctx.Done()
			// Simulate controller-runtime's own ctx-cancel drain taking a
			// non-trivial moment to finish.
			time.Sleep(100 * time.Millisecond)
			op.drained.Store(true)
			return nil
		},
	}
	errCh, _ := runInBackground(t, baseConfig(addr), srv)
	waitReady(t, addr, 2*time.Second)

	if err := shutdownAndWait(t, errCh, 5*time.Second); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !op.drained.Load() {
		t.Fatal("Run returned before the operator goroutine finished draining — operator must be supervised on the shutdown WaitGroup")
	}
}

// TestRun_OperatorFailureAfterShutdownStartIsNotLost pins the other half
// of FIX 1: a failComponent write from RunOperators that lands during the
// shutdown drain must still surface as Run's return value (the WaitGroup
// wait happens BEFORE the final componentFailure read).
func TestRun_OperatorFailureAfterShutdownStartIsNotLost(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — sends SIGTERM.
	addr := freeAddr(t)
	srv := serverkit.Server{
		Handler:   emptyHandler(),
		Operators: []serverkit.Operator{&stubOperator{name: "late-fail-op"}},
		RunOperators: func(ctx context.Context, _ *slog.Logger, _ string) error {
			// Don't fail at boot — fail only after shutdown has begun, the
			// window where a fire-and-forget goroutine's error would be
			// silently dropped.
			<-ctx.Done()
			time.Sleep(50 * time.Millisecond)
			return context.DeadlineExceeded // a non-Canceled error
		},
	}
	errCh, _ := runInBackground(t, baseConfig(addr), srv)
	waitReady(t, addr, 2*time.Second)

	err := shutdownAndWait(t, errCh, 5*time.Second)
	if err == nil || !contains(err.Error(), "controller manager") {
		t.Fatalf("operator failure during drain must surface as Run's error, got %v", err)
	}
}

// TestRun_PprofBindFailureNeverTakesTheServiceDown pins the pprof failure
// contract: a pprof listener-bind failure must cost you the PROFILER, never
// the PROCESS. Run logs it and serves on.
//
// This supersedes the older "route the bind failure through graceful
// shutdown" behaviour. That rule was written when PprofAddr was empty by
// default, so a non-empty value meant an operator had deliberately asked for
// pprof and a silent miss was worth a restart. pprof is now ON BY DEFAULT in
// the scaffold, which makes the ordinary cause of a bind failure banal — a
// second copy of the binary on the same dev box, or anything else already
// holding :6060 — and "your debug port was taken, so your service is down"
// is a strictly worse outcome than no debug port. FailurePolicy does not
// change this: it governs SUPERVISED components (workers, operators), and
// pprof is not one.
//
// The original defect the old test guarded against is still guarded: the
// failure path must not bare-return past the already-serving main listener.
// It does not — it falls through, and the assertions below prove the service
// stayed up and then shut down normally, OnShutdown and all.
func TestRun_PprofBindFailureNeverTakesTheServiceDown(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — exercises the shared signal/shutdown path.
	mainAddr := freeAddr(t)

	// Occupy the pprof address so the bind inside Run fails.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy pprof addr: %v", err)
	}
	defer blocker.Close()
	pprofAddr := blocker.Addr().String()

	var shutdownRan atomic.Bool
	cfg := baseConfig(mainAddr)
	cfg.PprofAddr = pprofAddr
	// FailProcess is the zero value and the deployed default; pin it
	// explicitly so this test reads as "even under the strictest policy".
	cfg.FailurePolicy = serverkit.FailProcess
	srv := serverkit.Server{
		Handler: emptyHandler(),
		OnShutdown: func(context.Context) error {
			shutdownRan.Store(true)
			return nil
		},
	}
	errCh, _ := runInBackground(t, cfg, srv)

	// The service must come up and STAY up despite the dead pprof listener.
	waitReady(t, mainAddr, 2*time.Second)
	select {
	case runErr := <-errCh:
		t.Fatalf("Run returned %v — a pprof bind failure must never terminate the process", runErr)
	case <-time.After(200 * time.Millisecond):
	}

	// And it must still shut down cleanly, with no pprof error leaking into
	// Run's result.
	if runErr := shutdownAndWait(t, errCh, 5*time.Second); runErr != nil {
		t.Fatalf("Run returned %v on a normal shutdown after a pprof bind failure, want nil", runErr)
	}
	if !shutdownRan.Load() {
		t.Fatal("OnShutdown did not run — the pprof bind failure disturbed the shutdown path")
	}
}

// TestRun_ConfiguredCORSWithoutFactoryIsBootError pins FIX 3: CORS origins
// set but no factory wired must fail closed (loud boot error), not
// silently serve with CORS unenforced.
func TestRun_ConfiguredCORSWithoutFactoryIsBootError(t *testing.T) {
	t.Parallel()
	cfg := baseConfig(":0")
	cfg.CORSOrigins = []string{"https://example.com"}
	// CORSMiddleware left nil.
	err := serverkit.Run(context.Background(), cfg, serverkit.Server{Handler: emptyHandler()})
	if err == nil || !contains(err.Error(), "CORSMiddleware is nil") {
		t.Fatalf("expected fail-closed CORS boot error, got %v", err)
	}
}

// TestRun_ConfiguredSecurityHeadersWithoutFactoryIsBootError: same
// fail-closed contract for the security-headers layer.
func TestRun_ConfiguredSecurityHeadersWithoutFactoryIsBootError(t *testing.T) {
	t.Parallel()
	cfg := baseConfig(":0")
	cfg.SecurityHeaders = true
	// SecurityHeadersMiddleware left nil.
	err := serverkit.Run(context.Background(), cfg, serverkit.Server{Handler: emptyHandler()})
	if err == nil || !contains(err.Error(), "SecurityHeadersMiddleware is nil") {
		t.Fatalf("expected fail-closed security-headers boot error, got %v", err)
	}
}
