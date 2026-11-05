package appkit

// Tests for the ctx-aware worker wrapping contract (A5 appkit table x
// A6 serverkit.ContextWorker reconciliation).
//
// The serverkit supervisor prefers RunContext over Start via a type
// assertion (`w.(serverkit.ContextWorker)`). Because the generated
// WorkerList wraps every worker, the wrapper must FORWARD ctx-awareness
// faithfully: expose RunContext exactly when the underlying worker has
// it, and never otherwise — a wrapper with an always-present RunContext
// would make legacy workers look ctx-aware and silently change their
// lifecycle.

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

func (w *legacyWorker) Start(ctx context.Context) error { w.started = true; return nil }
func (w *legacyWorker) Stop(ctx context.Context) error  { w.stopped = true; return nil }

// ctxWorker additionally implements the ctx-aware RunContext loop.
type ctxWorker struct {
	legacyWorker
	ran bool
}

func (w *ctxWorker) RunContext(ctx context.Context) error { w.ran = true; return nil }

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

	// This is the exact assertion the serverkit supervisor performs at
	// worker fan-out (pkg/serverkit/run.go) — if it fails, a
	// RunContext-implementing worker wired through the generated
	// bootstrap would be demoted to the legacy Start path.
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
		func(ctx context.Context) error { ranWith = "runContext"; return nil },
		func(ctx context.Context) error { return nil },
	)

	var w serverkit.Worker = inst
	cw, ok := w.(serverkit.ContextWorker)
	if !ok {
		t.Fatal("*ContextWorkerInstance must satisfy serverkit.ContextWorker")
	}
	if err := cw.RunContext(context.Background()); err != nil || ranWith != "runContext" {
		t.Errorf("RunContext delegation broken (err=%v ranWith=%q)", err, ranWith)
	}
	// Start exists for serverkit.Worker completeness and delegates to
	// the same ctx-aware loop.
	ranWith = ""
	if err := inst.Start(context.Background()); err != nil || ranWith != "runContext" {
		t.Errorf("Start should delegate to runContext (err=%v ranWith=%q)", err, ranWith)
	}
}
