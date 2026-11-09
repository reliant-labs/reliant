package serverkit_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// failingWorker returns an error from Start immediately — the shape of
// a worker that can't acquire its queue/DB/socket at boot.
type failingWorker struct {
	name string
	err  error
}

func (w *failingWorker) Name() string                { return w.name }
func (w *failingWorker) Start(context.Context) error { return w.err }
func (w *failingWorker) Stop(context.Context) error  { return nil }

// failingCtxWorker is the ContextWorker variant: RunContext returns a
// non-cancellation error mid-flight.
type failingCtxWorker struct {
	failingWorker
}

func (w *failingCtxWorker) RunContext(context.Context) error { return w.err }

// TestRun_WorkerStartErrorKillsProcessByDefault pins the default
// failure policy: a worker whose Start returns an error must take the
// process down (Run returns the worker error) instead of logging and
// serving on with the worker silently dead.
func TestRun_WorkerStartErrorKillsProcessByDefault(t *testing.T) {
	// Not parallel — exercises the shared signal/shutdown path.
	addr := freeAddr(t)
	boom := errors.New("queue connection refused")
	srv := serverkit.Server{
		Handler: emptyHandler(),
		Workers: []serverkit.Worker{&failingWorker{name: "dead-on-arrival", err: boom}},
	}
	errCh, _ := runInBackground(t, baseConfig(addr), srv)
	select {
	case err := <-errCh:
		if err == nil || !contains(err.Error(), "queue connection refused") {
			t.Fatalf("Run should return the worker error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept serving with a dead worker — default policy must fail the process")
	}
}

// TestRun_ContextWorkerErrorKillsProcessByDefault: same contract for the
// RunContext lifecycle.
func TestRun_ContextWorkerErrorKillsProcessByDefault(t *testing.T) {
	addr := freeAddr(t)
	boom := errors.New("ctx worker exploded")
	w := &failingCtxWorker{failingWorker{name: "ctx-dead", err: boom}}
	srv := serverkit.Server{Handler: emptyHandler(), Workers: []serverkit.Worker{w}}
	errCh, _ := runInBackground(t, baseConfig(addr), srv)
	select {
	case err := <-errCh:
		if err == nil || !contains(err.Error(), "ctx worker exploded") {
			t.Fatalf("Run should return the ContextWorker error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept serving with a dead ContextWorker — default policy must fail the process")
	}
}

// TestRun_OperatorErrorKillsProcessByDefault: RunOperators errors get
// the same policy.
func TestRun_OperatorErrorKillsProcessByDefault(t *testing.T) {
	addr := freeAddr(t)
	opErr := errors.New("not running in-cluster")
	srv := serverkit.Server{
		Handler:   emptyHandler(),
		Operators: []serverkit.Operator{&stubOperator{name: "op"}},
		RunOperators: func(context.Context, *slog.Logger, string) error {
			return opErr
		},
	}
	errCh, _ := runInBackground(t, baseConfig(addr), srv)
	select {
	case err := <-errCh:
		if err == nil || !contains(err.Error(), "not running in-cluster") {
			t.Fatalf("Run should return the operator error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept serving with a dead controller manager — default policy must fail the process")
	}
}

type stubOperator struct{ name string }

func (o *stubOperator) Name() string { return o.name }

// TestRun_WorkerErrorIgnorePolicyKeepsServing pins the explicit opt-out:
// FailurePolicy=Ignore restores log-and-continue for deployments whose
// workers are genuinely best-effort.
func TestRun_WorkerErrorIgnorePolicyKeepsServing(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — sends SIGTERM.
	addr := freeAddr(t)
	srv := serverkit.Server{
		Handler: emptyHandler(),
		Workers: []serverkit.Worker{&failingWorker{name: "best-effort", err: errors.New("optional dep down")}},
	}
	cfg := baseConfig(addr)
	cfg.FailurePolicy = serverkit.Ignore
	errCh, _ := runInBackground(t, cfg, srv)
	waitReady(t, addr, 2*time.Second)

	// The worker has already failed; the server must still be serving.
	select {
	case err := <-errCh:
		t.Fatalf("Run exited under Ignore policy: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if err := shutdownAndWait(t, errCh, 5*time.Second); err != nil {
		t.Fatalf("clean shutdown should return nil under Ignore, got %v", err)
	}
}
