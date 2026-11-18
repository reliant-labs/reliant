// Package lifecyclekit owns process lifecycle: the four single-method
// component interfaces (Runner, Drainer, Closer, Readier) and Supervise, which
// starts a list of components in order and stops them in reverse
// (supervise.go) — plus the uniform worker/operator supervised-component
// machinery that the generated internal/app/lifecycle_gen.go delegates to.
//
// # Status (FORGE_SHAPE_REDESIGN §2 — lib-boundary extraction)
//
// The generated lifecycle file is the SUPERVISED-component surface: the
// workers and operators serverkit runs over the constructed *Services. Per
// the "generated files are tables, not programs" rule, all the uniform
// worker machinery lives HERE and the generated file shrinks to thin per-app
// DATA:
//
//   - WorkerList(s) returns explicit lifecyclekit.WrapWorker("name", s.Field)
//     rows;
//   - OperatorList(s) returns one row per operator;
//   - RunOperators(s, ...) is a single delegation to operatorkit.Run with one
//     dumb Controller row per operator.
//
// What lives here:
//
//   - [WorkerInstance] / [ContextWorkerInstance] — lifecycle wrappers
//     (Name / Start / Stop, plus the ctx-aware RunContext sibling) that
//     satisfy serverkit.Worker / serverkit.ContextWorker.
//   - [WrapWorker] — the runtime type-switch the generated WorkerList rows
//     call per worker: a worker implementing RunContext gets the ctx-aware
//     wrapper (per-worker cancel-on-shutdown), everything else gets the
//     legacy Start wrapper.
//   - [WorkerLifecycle] — the Start/Stop shape every generated worker exposes.
//   - [Worker] / [Operator] — re-exports of the serverkit contracts the
//     generated lists return.
//   - [HasOperators] — the predicate over a serverkit.Operator slice.
//
// Adding a worker needs no `forge generate` change to this package — the
// type-switch detects RunContext at runtime.
//
// # No controller-runtime here
//
// The controller-manager bridge is NOT in this package: it is
// forge/pkg/appkit/operatorkit ([operatorkit.Run], Controller, Options), and
// only a project that runs operators imports it. lifecyclekit used to
// re-export those three names, which made every importer of lifecyclekit —
// including a binary that only wanted [Supervise] — link
// sigs.k8s.io/controller-runtime and its Kubernetes dependency tree.
// deps_test.go pins that this package's import graph reaches neither.
package lifecyclekit

import (
	"context"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// Worker is the runtime contract for a long-running background task — a
// re-export of serverkit.Worker so the generated WorkerList returns a
// lifecyclekit type and the generated file imports only lifecyclekit.
type Worker = serverkit.Worker

// Operator is the minimal contract serverkit needs to count supervised
// operators — a re-export of serverkit.Operator so the generated
// OperatorList returns a lifecyclekit type.
type Operator = serverkit.Operator

// WorkerInstance wraps a worker with its lifecycle methods (Name / Start /
// Stop). [WrapWorker] returns one for legacy Start/Stop workers.
type WorkerInstance struct {
	name  string
	start func(ctx context.Context) error
	stop  func(ctx context.Context) error
}

// NewWorkerInstance builds a WorkerInstance row from a worker's name and
// Start/Stop methods.
func NewWorkerInstance(name string, start, stop func(ctx context.Context) error) *WorkerInstance {
	return &WorkerInstance{name: name, start: start, stop: stop}
}

// Name returns the worker's identifier.
func (w *WorkerInstance) Name() string { return w.name }

// Start blocks until ctx is cancelled.
func (w *WorkerInstance) Start(ctx context.Context) error { return w.start(ctx) }

// Stop is called during graceful shutdown.
func (w *WorkerInstance) Stop(ctx context.Context) error { return w.stop(ctx) }

// ContextWorkerInstance is the ctx-aware sibling of [WorkerInstance]: it
// additionally exposes RunContext, so a value of this type satisfies
// serverkit.ContextWorker and the serverkit supervisor prefers the ctx-aware
// lifecycle (per-worker cancel-on-shutdown) over legacy Start. It is a
// SEPARATE type — not an always-present RunContext on WorkerInstance that
// sometimes delegates to Start — because the supervisor's preference is a
// type assertion: a universally-present RunContext would make every worker
// look ctx-aware and silently change legacy workers' lifecycle.
type ContextWorkerInstance struct {
	WorkerInstance
	runContext func(ctx context.Context) error
}

// NewContextWorkerInstance builds a ContextWorkerInstance from a worker's
// name and its RunContext/Stop methods. The embedded WorkerInstance's Start
// delegates to runContext — semantically identical for a ctx-aware worker
// (both receive the supervisor's per-worker ctx), and the supervisor never
// takes the Start path when RunContext is present anyway; Start exists only
// so the wrapper still satisfies the base serverkit.Worker interface.
func NewContextWorkerInstance(name string, runContext, stop func(ctx context.Context) error) *ContextWorkerInstance {
	return &ContextWorkerInstance{
		WorkerInstance: WorkerInstance{name: name, start: runContext, stop: stop},
		runContext:     runContext,
	}
}

// RunContext runs the worker's ctx-aware main loop. See serverkit.ContextWorker
// for the full shutdown contract.
func (w *ContextWorkerInstance) RunContext(ctx context.Context) error { return w.runContext(ctx) }

// WorkerLifecycle is the Start/Stop surface every generated worker type
// exposes — the serverkit.Worker shape minus Name, which the generated table
// supplies from forge.yaml rather than from the worker itself.
type WorkerLifecycle interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// contextRunner mirrors the RunContext half of serverkit.ContextWorker for
// the WrapWorker type-switch.
type contextRunner interface {
	RunContext(ctx context.Context) error
}

// WrapWorker wraps a constructed worker for the serverkit supervisor. It is
// the single helper the generated WorkerList rows call — a runtime
// type-switch (chosen over codegen-time AST detection so the generated table
// stays one dumb call per row and adding RunContext to a worker needs no
// `forge generate`):
//
//   - the worker implements RunContext(ctx) → *ContextWorkerInstance, which
//     satisfies serverkit.ContextWorker, so the supervisor's
//     `w.(serverkit.ContextWorker)` assertion sees through the wrapper and
//     uses the ctx-aware lifecycle;
//   - otherwise → *WorkerInstance, which has no RunContext method, so the
//     supervisor's legacy Start path is untouched.
func WrapWorker(name string, w WorkerLifecycle) serverkit.Worker {
	if cr, ok := w.(contextRunner); ok {
		return &ContextWorkerInstance{
			WorkerInstance: WorkerInstance{name: name, start: w.Start, stop: w.Stop},
			runContext:     cr.RunContext,
		}
	}
	return NewWorkerInstance(name, w.Start, w.Stop)
}

// HasOperators reports whether the supplied operator list is non-empty. The
// generated HasOperators(s) delegates here over OperatorList(s).
func HasOperators(operators []Operator) bool { return len(operators) > 0 }

// Leader is the one thing [Singleton] needs from a lease implementation: run
// fn only while this process holds the named lease, cancel fn's context when
// the lease is lost, and keep contending until ctx ends. *lease.Locker
// (forge/pkg/lease) satisfies it.
type Leader interface {
	Run(ctx context.Context, name string, fn func(ctx context.Context) error) error
}

// Singleton wraps a worker so that, across all replicas sharing the Leader's
// database, at most one runs it at a time. The others stand by and take over
// when the holder exits, crashes or loses its session.
//
// Use it for a worker that is NOT safe to run concurrently. A worker that
// already is (a durable JetStream consumer, a SKIP LOCKED queue drain, an
// idempotent set-based sweep) should NOT be wrapped: it would idle replicas for
// no benefit and make the lease a single point of throughput.
//
// The wrapped worker must be restartable: its Start (or RunContext) is called
// again whenever this replica re-acquires the lease after losing it. A worker
// that returns nil while holding the lease is treated as finished (a worker
// that disabled itself), not as a reason to re-contend.
//
// The lease is named "worker/<name>". A nil leader returns w unchanged, for a
// process with no database to lease from.
func Singleton(l Leader, w serverkit.Worker) serverkit.Worker {
	if l == nil {
		return w
	}
	run := w.Start
	if cw, ok := w.(serverkit.ContextWorker); ok {
		run = cw.RunContext
	}
	name := "worker/" + w.Name()
	return &SingletonWorker{
		ContextWorkerInstance: *NewContextWorkerInstance(w.Name(),
			func(ctx context.Context) error { return l.Run(ctx, name, run) },
			w.Stop),
		leaseName: name,
	}
}

// SingletonWorker is what [Singleton] returns: a ContextWorker that runs its
// inner worker under a lease. It is exported so a project can assert, without
// starting anything, which of its workers are leased (see [LeaseNameOf]).
type SingletonWorker struct {
	ContextWorkerInstance
	leaseName string
}

// LeaseName is the lease this worker contends for.
func (w *SingletonWorker) LeaseName() string { return w.leaseName }

// LeaseNameOf reports the lease a worker runs under, or "" when it is not a
// [Singleton]. A project test uses it to pin which workers are leased and which
// run on every replica, so a worker added later must be classified.
func LeaseNameOf(w serverkit.Worker) string {
	if sw, ok := w.(interface{ LeaseName() string }); ok {
		return sw.LeaseName()
	}
	return ""
}
