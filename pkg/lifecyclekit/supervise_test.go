package lifecyclekit_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/lifecyclekit"
	"github.com/reliant-labs/forge/pkg/serverkit"
)

// journal records lifecycle events in order, safely across goroutines.
type journal struct {
	mu     sync.Mutex
	events []string
}

func (j *journal) add(e string) {
	j.mu.Lock()
	j.events = append(j.events, e)
	j.mu.Unlock()
}

func (j *journal) list() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return slices.Clone(j.events)
}

// fakeComponent implements all four lifecycle interfaces, like a listener:
// Ready once Run is running, and Run blocks until Drain (or ctx) ends it.
type fakeComponent struct {
	name    string
	j       *journal
	drained chan struct{}
	once    sync.Once
	running atomic.Bool
}

func newFake(name string, j *journal) *fakeComponent {
	return &fakeComponent{name: name, j: j, drained: make(chan struct{})}
}

func (f *fakeComponent) Run(ctx context.Context) error {
	f.j.add("run " + f.name)
	f.running.Store(true)
	select {
	case <-f.drained:
	case <-ctx.Done():
		f.j.add("run-ctx-ended " + f.name)
	}
	return nil
}

func (f *fakeComponent) Drain(context.Context) error {
	f.j.add("drain " + f.name)
	f.once.Do(func() { close(f.drained) })
	return nil
}

func (f *fakeComponent) Close(context.Context) error {
	f.j.add("close " + f.name)
	return nil
}

func (f *fakeComponent) Ready(context.Context) error {
	if !f.running.Load() {
		return errors.New("not running yet")
	}
	return nil
}

// fastDrainer has no pre-stop pause and a short budget, logging to buf.
func fastDrainer(buf *syncBuffer) *serverkit.Drainer {
	return &serverkit.Drainer{
		PreStopDelay:    -1,
		ShutdownTimeout: 2 * time.Second,
		Logger:          slog.New(slog.NewTextHandler(buf, nil)),
	}
}

// superviseInBackground runs Supervise until the returned cancel.
func superviseInBackground(t *testing.T, policy lifecyclekit.Policy, nodes ...lifecyclekit.Node) (stop context.CancelFunc, done <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- lifecyclekit.Supervise(ctx, policy, nodes...) }()
	t.Cleanup(cancel)
	return cancel, errCh
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func await(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Supervise did not return")
		return nil
	}
}

// Start runs in the given order; stop walks it in reverse, and each node is
// Drained, then has its Run context ended and Run awaited, then Closed —
// before the next node back is touched. The flush runs after everything.
func TestSupervise_StartInOrderStopInReverse(t *testing.T) {
	j := &journal{}
	var logs syncBuffer
	drainer := fastDrainer(&logs)
	pool := lifecyclekit.Node{Name: "pool", Close: func(context.Context) error { j.add("close pool"); return nil }}
	api, daemon := newFake("api", j), newFake("daemon", j)
	stop, done := superviseInBackground(t, lifecyclekit.Policy{
		Drainer: drainer,
		Flush:   func(context.Context) error { j.add("flush"); return nil },
	}, pool, lifecyclekit.Component("api", api), lifecyclekit.Component("daemon", daemon))

	waitFor(t, "the process to be ready", drainer.Ready)
	if got := j.list(); !slices.Equal(got, []string{"run api", "run daemon"}) {
		t.Fatalf("start = %v", got)
	}
	stop()
	if err := await(t, done); err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	want := []string{
		"run api", "run daemon",
		"drain daemon", "close daemon",
		"drain api", "close api",
		"close pool",
		"flush",
	}
	if got := j.list(); !slices.Equal(got, want) {
		t.Fatalf("events =\n  %v\nwant\n  %v", got, want)
	}
	for _, line := range []string{"supervise: started", "drain: readiness flipped to draining", "drain: node stopped", "node=daemon", "drain: complete"} {
		if !strings.Contains(logs.String(), line) {
			t.Errorf("logs lack %q:\n%s", line, logs.String())
		}
	}
	if strings.Index(logs.String(), "node=daemon") > strings.Index(logs.String(), "node=api") {
		t.Errorf("daemon's phase line should precede api's:\n%s", logs.String())
	}
}

// A node that reports Ready is waited for before the next node starts, so a
// listener accepts traffic only once everything before it is up; the process
// is marked ready only after every node.
func TestSupervise_WaitsForReadyBeforeTheNextNode(t *testing.T) {
	j := &journal{}
	var logs syncBuffer
	drainer := fastDrainer(&logs)
	var poolUp sync.WaitGroup
	poolUp.Add(1)
	ready := make(chan struct{})
	go func() { poolUp.Wait(); close(ready) }()
	pool := lifecyclekit.Node{Name: "pool", Ready: func(context.Context) error {
		select {
		case <-ready:
			return nil
		default:
			return errors.New("dialing")
		}
	}}
	api := newFake("api", j)
	stop, done := superviseInBackground(t, lifecyclekit.Policy{Drainer: drainer}, pool, lifecyclekit.Component("api", api))

	time.Sleep(100 * time.Millisecond)
	if got := j.list(); len(got) != 0 {
		t.Fatalf("api started before the pool was ready: %v", got)
	}
	if drainer.Ready() {
		t.Fatal("the process is ready before every node started")
	}
	poolUp.Done()
	waitFor(t, "api to start", func() bool { return len(j.list()) > 0 })
	waitFor(t, "the process to be ready", drainer.Ready)
	stop()
	if err := await(t, done); err != nil {
		t.Fatalf("Supervise: %v", err)
	}
}

// Under FailProcess a runner's error stops every node and is what Supervise
// returns; the nodes that started still drain and close.
func TestSupervise_FailingRunnerStopsTheProcess(t *testing.T) {
	j := &journal{}
	var logs syncBuffer
	api := newFake("api", j)
	boom := errors.New("lost the broker")
	worker := lifecyclekit.Node{Name: "worker", Run: func(context.Context) error {
		time.Sleep(20 * time.Millisecond)
		return boom
	}}
	_, done := superviseInBackground(t, lifecyclekit.Policy{Drainer: fastDrainer(&logs)}, lifecyclekit.Component("api", api), worker)
	err := await(t, done)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "worker failed") {
		t.Fatalf("Supervise = %v, want the worker's failure", err)
	}
	if got := j.list(); !slices.Contains(got, "drain api") || !slices.Contains(got, "close api") {
		t.Fatalf("api was not stopped after the worker failed: %v", got)
	}
}

// Under Ignore a failing runner is logged and the rest keep running until the
// stop.
func TestSupervise_IgnoreKeepsRunning(t *testing.T) {
	j := &journal{}
	var logs syncBuffer
	failed := make(chan struct{})
	worker := lifecyclekit.Node{Name: "worker", Run: func(context.Context) error {
		defer close(failed)
		return errors.New("best effort")
	}}
	stop, done := superviseInBackground(t, lifecyclekit.Policy{Drainer: fastDrainer(&logs), FailurePolicy: serverkit.Ignore},
		worker, lifecyclekit.Component("api", newFake("api", j)))
	<-failed
	time.Sleep(50 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("Supervise returned %v under Ignore", err)
	default:
	}
	stop()
	if err := await(t, done); err != nil {
		t.Fatalf("Supervise: %v", err)
	}
}

// A runner that ignores its context cannot hold the process past the budget:
// the walk moves on when the budget ends and says which node it gave up on.
func TestSupervise_StuckRunnerIsBoundedByTheBudget(t *testing.T) {
	var logs syncBuffer
	drainer := fastDrainer(&logs)
	drainer.ShutdownTimeout = 200 * time.Millisecond
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	stuck := lifecyclekit.Node{Name: "stuck", Run: func(context.Context) error { <-release; return nil }}
	stop, done := superviseInBackground(t, lifecyclekit.Policy{Drainer: drainer}, stuck)
	waitFor(t, "the process to be ready", drainer.Ready)
	begun := time.Now()
	stop()
	if err := await(t, done); err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	if d := time.Since(begun); d > 2*time.Second {
		t.Fatalf("stop took %v against a 200ms budget", d)
	}
	if !strings.Contains(logs.String(), "did not stop within the shutdown budget") {
		t.Fatalf("no line names the stuck node:\n%s", logs.String())
	}
}

// Once one node has spent the budget, a node behind it in the walk that
// stops the instant it is cancelled is not reported as stuck: its Run gets a
// short grace to return, and only the node that really ignored its cancel is
// named at ERROR.
func TestSupervise_NodeAfterTheBudgetIsSpentIsNotFalselyStuck(t *testing.T) {
	var logs syncBuffer
	drainer := fastDrainer(&logs)
	drainer.ShutdownTimeout = 100 * time.Millisecond
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	// The walk is reverse order: "stuck" is stopped first and spends the
	// whole budget; "prompt" is stopped after, with the budget already gone.
	prompt := lifecyclekit.Node{Name: "prompt", Run: func(ctx context.Context) error { <-ctx.Done(); return nil }}
	stuck := lifecyclekit.Node{Name: "stuck", Run: func(context.Context) error { <-release; return nil }}
	stop, done := superviseInBackground(t, lifecyclekit.Policy{Drainer: drainer}, prompt, stuck)
	waitFor(t, "the process to be ready", drainer.Ready)
	stop()
	if err := await(t, done); err != nil {
		t.Fatalf("Supervise: %v", err)
	}

	var stuckLines, promptLines []string
	for _, line := range strings.Split(logs.String(), "\n") {
		if !strings.Contains(line, "did not stop within the shutdown budget") && !strings.Contains(line, "run_still_running") {
			continue
		}
		switch {
		case strings.Contains(line, "node=stuck"):
			stuckLines = append(stuckLines, line)
		case strings.Contains(line, "node=prompt"):
			promptLines = append(promptLines, line)
		}
	}
	if len(stuckLines) == 0 {
		t.Errorf("the stuck node is not reported:\n%s", logs.String())
	}
	if len(promptLines) > 0 {
		t.Errorf("a node that stopped as soon as it was cancelled is reported as stuck:\n%s", strings.Join(promptLines, "\n"))
	}
}

func TestSupervise_RejectsInvalidNodes(t *testing.T) {
	noop := func(context.Context) error { return nil }
	for name, tc := range map[string]struct {
		nodes []lifecyclekit.Node
		want  string
	}{
		"unnamed":   {[]lifecyclekit.Node{{Run: noop}}, "has no name"},
		"duplicate": {[]lifecyclekit.Node{{Name: "a", Run: noop}, {Name: "a", Close: noop}}, `"a" is listed twice`},
		"empty":     {[]lifecyclekit.Node{lifecyclekit.Component("x", struct{}{})}, "nothing to supervise"},
	} {
		t.Run(name, func(t *testing.T) {
			err := lifecyclekit.Supervise(context.Background(), lifecyclekit.Policy{}, tc.nodes...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Supervise = %v, want %q", err, tc.want)
			}
		})
	}
}

// startStopWorker is a legacy serverkit.Worker.
type startStopWorker struct{ j *journal }

func (w startStopWorker) Name() string { return "legacy" }
func (w startStopWorker) Start(ctx context.Context) error {
	w.j.add("start legacy")
	<-ctx.Done()
	return ctx.Err()
}
func (w startStopWorker) Stop(context.Context) error { w.j.add("stop legacy"); return nil }

// contextWorker is a serverkit.ContextWorker.
type contextWorker struct{ startStopWorker }

func (w contextWorker) Name() string { return "ctx" }
func (w contextWorker) RunContext(ctx context.Context) error {
	w.j.add("runcontext ctx")
	<-ctx.Done()
	return nil
}
func (w contextWorker) Stop(context.Context) error { w.j.add("stop ctx"); return nil }

// WorkerNode adapts today's Worker: RunContext when it has one, Start
// otherwise, and Stop after the run context ends.
func TestWorkerNode_AdaptsStartStop(t *testing.T) {
	j := &journal{}
	var logs syncBuffer
	drainer := fastDrainer(&logs)
	stop, done := superviseInBackground(t, lifecyclekit.Policy{Drainer: drainer},
		lifecyclekit.WorkerNode(startStopWorker{j}), lifecyclekit.WorkerNode(contextWorker{startStopWorker{j}}))
	waitFor(t, "both workers to run", func() bool { return len(j.list()) == 2 })
	stop()
	if err := await(t, done); err != nil {
		t.Fatalf("Supervise: %v", err)
	}
	want := []string{"start legacy", "runcontext ctx", "stop ctx", "stop legacy"}
	got := j.list()
	if !slices.Equal(got[2:], want[2:]) || !slices.Contains(got[:2], "start legacy") || !slices.Contains(got[:2], "runcontext ctx") {
		t.Fatalf("events = %v, want %v (the two starts in either order)", got, want)
	}
}

func TestReadyChecks_OnePerReadyNode(t *testing.T) {
	ok := func(context.Context) error { return nil }
	checks := lifecyclekit.ReadyChecks(
		lifecyclekit.Node{Name: "pool", Ready: ok, Close: ok},
		lifecyclekit.Node{Name: "loop", Run: ok},
		lifecyclekit.Component("api", newFake("api", &journal{})),
	)
	var names []string
	for _, c := range checks {
		names = append(names, c.Name)
	}
	if !slices.Equal(names, []string{"pool", "api"}) {
		t.Fatalf("checks = %v", names)
	}
}
