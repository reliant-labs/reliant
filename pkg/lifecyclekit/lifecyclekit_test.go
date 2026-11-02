package lifecyclekit

// Tests for the worker/operator lifecycle machinery extracted from appkit
// (lib-boundary extraction — FORGE_SHAPE_REDESIGN §2). The generated
// internal/app/lifecycle_gen.go is thin data over these helpers, so the
// contracts tested here are the ones the generated WorkerList / OperatorList /
// HasOperators / RunOperators depend on.

import (
	"context"
	"testing"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// legacyWorker has only the classic Start/Stop lifecycle.
type legacyWorker struct {
	started bool
	stopped bool
}

func (w *legacyWorker) Start(context.Context) error { w.started = true; return nil }
func (w *legacyWorker) Stop(context.Context) error  { w.stopped = true; return nil }

// ctxWorker additionally implements the ctx-aware RunContext loop.
type ctxWorker struct {
	legacyWorker
	ran bool
}

func (w *ctxWorker) RunContext(context.Context) error { w.ran = true; return nil }

func TestWrapWorker_LegacyWorkerIsNotContextWorker(t *testing.T) {
	w := &legacyWorker{}
	wrapped := WrapWorker("legacy", w)

	if _, ok := wrapped.(serverkit.ContextWorker); ok {
		t.Fatal("legacy worker wrapper must NOT satisfy serverkit.ContextWorker — the supervisor would silently switch its lifecycle")
	}
	if wrapped.Name() != "legacy" {
		t.Errorf("Name() = %q, want %q", wrapped.Name(), "legacy")
	}
	if err := wrapped.Start(context.Background()); err != nil || !w.started {
		t.Errorf("Start must delegate to the underlying worker (err=%v started=%v)", err, w.started)
	}
	if err := wrapped.Stop(context.Background()); err != nil || !w.stopped {
		t.Errorf("Stop must delegate to the underlying worker (err=%v stopped=%v)", err, w.stopped)
	}
}

func TestWrapWorker_ContextWorkerSurvivesWrapping(t *testing.T) {
	w := &ctxWorker{}
	wrapped := WrapWorker("ctx", w)

	// The exact assertion the serverkit supervisor performs at worker
	// fan-out: if it fails, a RunContext-implementing worker wired through
	// the generated bootstrap would be demoted to the legacy Start path.
	cw, ok := wrapped.(serverkit.ContextWorker)
	if !ok {
		t.Fatal("wrapper of a RunContext-implementing worker must satisfy serverkit.ContextWorker")
	}
	if err := cw.RunContext(context.Background()); err != nil {
		t.Fatalf("RunContext() error = %v", err)
	}
	if !w.ran {
		t.Error("wrapper RunContext must delegate to the underlying worker's RunContext")
	}
	if w.started {
		t.Error("wrapper RunContext must not fall back to the underlying Start")
	}
}

func TestNewContextWorkerInstance_SatisfiesContextWorker(t *testing.T) {
	ranWith := ""
	inst := NewContextWorkerInstance("manual",
		func(context.Context) error { ranWith = "runContext"; return nil },
		func(context.Context) error { return nil },
	)

	var w serverkit.Worker = inst
	cw, ok := w.(serverkit.ContextWorker)
	if !ok {
		t.Fatal("*ContextWorkerInstance must satisfy serverkit.ContextWorker")
	}
	if err := cw.RunContext(context.Background()); err != nil || ranWith != "runContext" {
		t.Errorf("RunContext delegation broken (err=%v ranWith=%q)", err, ranWith)
	}
	// Start exists for serverkit.Worker completeness and delegates to the
	// same ctx-aware loop.
	ranWith = ""
	if err := inst.Start(context.Background()); err != nil || ranWith != "runContext" {
		t.Errorf("Start should delegate to runContext (err=%v ranWith=%q)", err, ranWith)
	}
}

func TestNewWorkerInstance_LegacyLifecycle(t *testing.T) {
	var startedCtx, stoppedCtx bool
	inst := NewWorkerInstance("legacy",
		func(context.Context) error { startedCtx = true; return nil },
		func(context.Context) error { stoppedCtx = true; return nil },
	)
	if inst.Name() != "legacy" {
		t.Errorf("Name() = %q, want legacy", inst.Name())
	}
	// A *WorkerInstance must NOT satisfy ContextWorker — only the ctx-aware
	// sibling does.
	var w serverkit.Worker = inst
	if _, ok := w.(serverkit.ContextWorker); ok {
		t.Fatal("*WorkerInstance must not satisfy serverkit.ContextWorker")
	}
	_ = inst.Start(context.Background())
	_ = inst.Stop(context.Background())
	if !startedCtx || !stoppedCtx {
		t.Errorf("Start/Stop delegation broken (started=%v stopped=%v)", startedCtx, stoppedCtx)
	}
}

func TestHasOperators(t *testing.T) {
	if HasOperators(nil) {
		t.Error("HasOperators(nil) must be false")
	}
	if HasOperators([]Operator{}) {
		t.Error("HasOperators(empty) must be false")
	}
	if !HasOperators([]Operator{stubOperator{}}) {
		t.Error("HasOperators(one) must be true")
	}
}

type stubOperator struct{}

func (stubOperator) Name() string { return "stub" }
