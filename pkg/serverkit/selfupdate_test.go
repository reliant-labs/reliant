package serverkit_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// fakeSelfUpdater records how Run drives a self-updater: it hands the test
// the ready and shutdown funcs Run passed, and logs the order of events.
type fakeSelfUpdater struct {
	served  chan struct{}
	execErr error

	mu       sync.Mutex
	ready    func(context.Context) error
	shutdown func()
	events   []string
}

func newFakeSelfUpdater() *fakeSelfUpdater { return &fakeSelfUpdater{served: make(chan struct{})} }

func (f *fakeSelfUpdater) record(e string) {
	f.mu.Lock()
	f.events = append(f.events, e)
	f.mu.Unlock()
}

func (f *fakeSelfUpdater) Serve(ctx context.Context, ready func(context.Context) error, shutdown func()) error {
	f.mu.Lock()
	f.ready, f.shutdown = ready, shutdown
	f.mu.Unlock()
	close(f.served)
	<-ctx.Done()
	f.record("serve-stopped")
	return nil
}

func (f *fakeSelfUpdater) ExecPending() error {
	f.record("exec")
	return f.execErr
}

func (f *fakeSelfUpdater) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func startSelfUpdating(t *testing.T, su *fakeSelfUpdater, checks ...serverkit.ReadyCheck) (chan error, func(context.Context) error, func()) {
	t.Helper()
	srv := serverkit.Server{
		Handler:     emptyHandler(),
		ReadyChecks: checks,
		SelfUpdate:  su,
		OnShutdown: func(context.Context) error {
			su.record("on-shutdown")
			return nil
		},
	}
	errCh, _ := runInBackground(t, baseConfig(freeAddr(t)), srv)
	select {
	case <-su.served:
	case err := <-errCh:
		t.Fatalf("Run returned before starting the self-updater: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run never started the self-updater")
	}
	su.mu.Lock()
	defer su.mu.Unlock()
	return errCh, su.ready, su.shutdown
}

func waitRun(t *testing.T, errCh chan error) error {
	t.Helper()
	select {
	case err := <-errCh:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after the self-updater's shutdown")
		return nil
	}
}

// The self-updater's shutdown is a graceful shutdown like a signal's, and the
// swap is Run's last act: after the teardown, with the updater stopped.
func TestRun_SelfUpdateShutsDownThenExecs(t *testing.T) {
	// Not parallel: Run installs process-wide signal handling and logger.
	su := newFakeSelfUpdater()
	errCh, ready, shutdown := startSelfUpdating(t, su)
	if err := ready(context.Background()); err != nil {
		t.Fatalf("a bound listener with no checks must be ready, got %v", err)
	}
	shutdown()
	if err := waitRun(t, errCh); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := strings.Join(su.log(), ",")
	if got != "serve-stopped,on-shutdown,exec" {
		t.Fatalf("events = %s, want serve-stopped,on-shutdown,exec", got)
	}
	if err := ready(context.Background()); err == nil {
		t.Fatal("a drained server must not report ready")
	}
}

// A new binary is confirmed on the real readiness: a failing dependency
// check keeps it unconfirmed (and the watchdog then reverts it).
func TestRun_SelfUpdateReadinessIncludesChecks(t *testing.T) {
	su := newFakeSelfUpdater()
	var mu sync.Mutex
	dbErr := errors.New("connection refused")
	errCh, ready, shutdown := startSelfUpdating(t, su, serverkit.ReadyCheck{Name: "database", Check: func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		return dbErr
	}})
	if err := ready(context.Background()); err == nil || !strings.Contains(err.Error(), "database") {
		t.Fatalf("ready = %v, want the failing database check", err)
	}
	mu.Lock()
	dbErr = nil
	mu.Unlock()
	if err := ready(context.Background()); err != nil {
		t.Fatalf("ready = %v once the check passes", err)
	}
	shutdown()
	if err := waitRun(t, errCh); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// A swap that fails to start leaves the old binary in place; Run says so, so
// the process exits non-zero for its supervisor.
func TestRun_SelfUpdateExecFailureIsRunsError(t *testing.T) {
	su := newFakeSelfUpdater()
	su.execErr = errors.New("exec format error")
	errCh, _, shutdown := startSelfUpdating(t, su)
	shutdown()
	err := waitRun(t, errCh)
	if err == nil || !strings.Contains(err.Error(), "self-update: exec format error") {
		t.Fatalf("Run = %v, want the exec failure", err)
	}
}
