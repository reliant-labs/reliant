package lifecyclekit

// supervise.go — running a process's components in order, and stopping them
// in reverse.
//
// A component is a struct built from its Deps. Some of them have a lifecycle,
// and it is always some of four single-method shapes: a listener serves
// (Run), stops taking work (Drain), lets go of its connections (Close) and
// reports whether it can take traffic (Ready); a database pool only Closes
// and is Ready; a background loop only Runs. Supervise runs any such list:
//
//	start, in the given order — dependencies first:
//	    Run each runner; when the node also reports Ready, wait for it
//	    before starting the next, so a listener accepts traffic only once
//	    everything before it is up. Then mark the process ready.
//	stop, when ctx ends or a runner fails:
//	    the root's policy first: readiness flips to draining and the
//	    pre-stop pause runs (serverkit.Drainer.Begin); then, in REVERSE
//	    order, for each node: Drain, cancel its Run context and wait for
//	    Run to return, Close — all inside one shutdown budget; then the
//	    total, and the telemetry flush last. Once the budget is spent, each
//	    remaining Run still gets a short grace (runStopGrace) to return
//	    after its cancel, so only a node that really does not stop is
//	    reported as stuck.
//
// Dependents stop before what they depend on, so a listener drains while the
// pool its handlers read is still open. Each phase writes the same "drain: …"
// lines serverkit.Run does, plus one "drain: node stopped" per node.
//
// Nothing here is generated or registered: every node is a value the caller
// built, so a hand-written main adopts it one component at a time.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// Runner is a long-lived component: Run returns when ctx ends — or, for a
// component that also Drains, once its drain is done.
type Runner interface {
	Run(ctx context.Context) error
}

// Drainer is a component that stops taking new work and finishes what it has
// in flight by ctx's deadline.
type Drainer interface {
	Drain(ctx context.Context) error
}

// Closer is a component that releases resources — a pool, a connection, a
// client — bounded by ctx.
type Closer interface {
	Close(ctx context.Context) error
}

// Readier is a component that contributes to the process's readiness: nil
// when it can take its share of traffic.
type Readier interface {
	Ready(ctx context.Context) error
}

// Node is one supervised component: a name for the logs and whichever of the
// four lifecycle methods it has. A nil method is a phase the node does not
// take part in. Build one with Component, WorkerNode, or a literal.
type Node struct {
	Name  string
	Run   func(ctx context.Context) error
	Drain func(ctx context.Context) error
	Close func(ctx context.Context) error
	Ready func(ctx context.Context) error
}

// Component is the Node for a component value: its Run, Drain, Close and
// Ready methods, for whichever of Runner, Drainer, Closer and Readier it
// implements. It is how a hand-written main hands its components to
// Supervise:
//
//	lifecyclekit.Component("api", apiListener)
//
// A method with another signature (a Close() error, say) is not one of the
// four and is not called; wrap it in a Node literal.
func Component(name string, c any) Node {
	n := Node{Name: name}
	if r, ok := c.(Runner); ok {
		n.Run = r.Run
	}
	if d, ok := c.(Drainer); ok {
		n.Drain = d.Drain
	}
	if cl, ok := c.(Closer); ok {
		n.Close = cl.Close
	}
	if r, ok := c.(Readier); ok {
		n.Ready = r.Ready
	}
	return n
}

// WorkerNode adapts a serverkit.Worker: it Runs RunContext when the worker is
// a serverkit.ContextWorker and Start otherwise, and Closes with Stop — which
// the Worker contract already calls after the run context ends.
func WorkerNode(w serverkit.Worker) Node {
	run := w.Start
	if cw, ok := w.(serverkit.ContextWorker); ok {
		run = cw.RunContext
	}
	return Node{Name: w.Name(), Run: run, Close: w.Stop}
}

// ReadyChecks are the /readyz checks for the nodes that report Ready, one per
// node, named after it — for serverkit.Drainer.ReadinessHandler, which a
// root's probe route serves.
func ReadyChecks(nodes ...Node) []serverkit.ReadyCheck {
	var checks []serverkit.ReadyCheck
	for _, n := range nodes {
		if n.Ready != nil {
			checks = append(checks, serverkit.ReadyCheck{Name: n.Name, Check: n.Ready})
		}
	}
	return checks
}

// Policy is the root's part of a stop: what happens around the nodes.
type Policy struct {
	// Drainer is the process's readiness gate and shutdown budget. Supervise
	// marks it ready once every node has started; on stop it Begins it —
	// readiness flips to draining, then PreStopDelay — and bounds the whole
	// reverse walk by its ShutdownContext. Serve its ReadinessHandler on a
	// probe route. nil is serverkit's defaults (5s pre-stop, 30s budget).
	Drainer *serverkit.Drainer

	// Logger receives the supervisor's lines; nil is the Drainer's Logger,
	// then slog.Default(). A nil Drainer.Logger is set to it, so the drain
	// phases go to the same place.
	Logger *slog.Logger

	// FailurePolicy governs a runner that returns an error before the stop:
	// serverkit.FailProcess (the default) stops every node and makes
	// Supervise return that error; serverkit.Ignore logs it and keeps
	// running the rest.
	FailurePolicy serverkit.FailurePolicy

	// Flush runs LAST, after every node has stopped and the shutdown's total
	// is recorded, inside what is left of the budget — the telemetry flush,
	// so it exports the shutdown's own spans, metrics and logs. Optional.
	Flush func(ctx context.Context) error
}

// readyPoll bounds how often Supervise asks a starting node whether it is
// Ready: from the first value, doubling, up to the second.
var readyPoll = [2]time.Duration{10 * time.Millisecond, 500 * time.Millisecond}

// Supervise starts nodes in order, waits until ctx ends or a runner fails,
// then stops them in reverse order (see the file comment for both
// sequences). It returns the first runner failure under FailProcess, nil for
// a stop that ctx began, and an error for an invalid node list — names must
// be non-empty and unique, and every node must have a lifecycle method.
//
// ctx is the stop signal — a signal.NotifyContext in a main — and nothing
// more: every Run gets a context of its own that ends only after its node
// has drained, so a listener keeps serving through the pre-stop pause.
func Supervise(ctx context.Context, policy Policy, nodes ...Node) error {
	if err := validateNodes(nodes); err != nil {
		return err
	}
	drainer := policy.Drainer
	if drainer == nil {
		drainer = serverkit.NewDrainer(serverkit.Config{})
	}
	logger := policy.Logger
	if logger == nil {
		logger = drainer.Logger
	}
	if logger == nil {
		logger = slog.Default()
	}
	if drainer.Logger == nil {
		drainer.Logger = logger
	}

	s := &supervisor{
		logger:  logger,
		policy:  policy.FailurePolicy,
		nodes:   nodes,
		runs:    make([]*run, len(nodes)),
		stopped: make(chan struct{}),
	}

	begun := time.Now()
	if s.start(ctx) {
		drainer.MarkReady()
		logger.InfoContext(ctx, "supervise: started", "nodes", len(nodes), "elapsed", time.Since(begun).Round(time.Millisecond))
		select {
		case <-ctx.Done():
		case <-s.stopped:
		}
	}
	logger.InfoContext(ctx, "supervise: stopping", "reason", s.reason(ctx))
	s.stopping()

	drainer.Begin()
	shutdownCtx, cancel := drainer.ShutdownContext()
	defer cancel()
	for i := len(nodes) - 1; i >= 0; i-- {
		s.stopNode(shutdownCtx, drainer, i)
	}
	drainer.Complete()
	if policy.Flush != nil {
		if err := policy.Flush(shutdownCtx); err != nil {
			logger.ErrorContext(ctx, "telemetry flush error", "error", err)
		}
	}
	return s.failure()
}

func validateNodes(nodes []Node) error {
	var errs []error
	seen := map[string]bool{}
	for i, n := range nodes {
		switch {
		case n.Name == "":
			errs = append(errs, fmt.Errorf("node %d has no name", i))
		case seen[n.Name]:
			errs = append(errs, fmt.Errorf("node %q is listed twice", n.Name))
		}
		seen[n.Name] = true
		if n.Run == nil && n.Drain == nil && n.Close == nil && n.Ready == nil {
			errs = append(errs, fmt.Errorf("node %q has no Run, Drain, Close or Ready: there is nothing to supervise", n.Name))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("lifecyclekit.Supervise: %w", errors.Join(errs...))
	}
	return nil
}

// supervisor is one Supervise call's state.
type supervisor struct {
	logger *slog.Logger
	policy serverkit.FailurePolicy
	nodes  []Node
	runs   []*run // nil for a node with no Run, or one never started

	stopOnce sync.Once
	stopped  chan struct{} // closed when a failure asks for the stop

	mu       sync.Mutex
	isStop   bool  // the stop has begun: a Run returning now is expected
	failed   error // the first failure, under FailProcess
	failedBy string
}

// run is one started Run.
type run struct {
	cancel context.CancelFunc
	done   chan struct{} // closed when Run returned
}

// start starts every node in order and reports whether all of them did; it
// stops early when ctx ends or a runner fails.
func (s *supervisor) start(ctx context.Context) bool {
	for i, n := range s.nodes {
		if ctx.Err() != nil || s.failedNow() {
			return false
		}
		if n.Run != nil {
			s.runs[i] = s.launch(ctx, n)
		}
		if n.Ready != nil && !s.awaitReady(ctx, i) {
			return false
		}
	}
	return !s.failedNow() && ctx.Err() == nil
}

// launch starts n.Run on a context of its own, which only the stop walk
// ends: the signal that ends ctx is what STARTS the stop, and the node must
// keep running through the readiness flip and the pre-stop pause.
func (s *supervisor) launch(ctx context.Context, n Node) *run {
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r := &run{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		err := n.Run(runCtx)
		s.mu.Lock()
		stopping := s.isStop
		s.mu.Unlock()
		switch {
		case err == nil || errors.Is(err, context.Canceled):
			if !stopping {
				s.logger.InfoContext(ctx, "supervise: node finished", "node", n.Name)
			}
		case stopping:
			s.logger.ErrorContext(ctx, "supervise: node failed while stopping", "node", n.Name, "error", err)
		default:
			s.fail(n.Name, err)
		}
	}()
	return r
}

// awaitReady polls node i's Ready until it reports nil. It gives up — and
// reports false — when ctx ends, a runner fails, or node i's own Run returns
// (it will never be ready).
func (s *supervisor) awaitReady(ctx context.Context, i int) bool {
	n := s.nodes[i]
	var done <-chan struct{}
	if r := s.runs[i]; r != nil {
		done = r.done
	}
	wait := readyPoll[0]
	waiting := time.Now()
	logged := false
	for {
		err := n.Ready(ctx)
		if err == nil {
			return true
		}
		if !logged && time.Since(waiting) > 5*time.Second {
			s.logger.InfoContext(ctx, "supervise: waiting for node to be ready", "node", n.Name, "error", err)
			logged = true
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-s.stopped:
			timer.Stop()
			return false
		case <-done:
			timer.Stop()
			s.fail(n.Name, fmt.Errorf("its Run returned before it was ready (last readiness error: %w)", err))
			return false
		case <-timer.C:
		}
		wait = min(2*wait, readyPoll[1])
	}
}

// fail records a runner failure under the FailurePolicy.
func (s *supervisor) fail(name string, err error) {
	if s.policy == serverkit.Ignore {
		s.logger.Error("component failed — continuing (FailurePolicy=Ignore)", "component", name, "error", err)
		return
	}
	s.logger.Error("component failed — terminating process (FailurePolicy=FailProcess)", "component", name, "error", err)
	s.mu.Lock()
	if s.failed == nil {
		s.failed, s.failedBy = fmt.Errorf("%s failed: %w", name, err), name
	}
	s.mu.Unlock()
	s.stopOnce.Do(func() { close(s.stopped) })
}

func (s *supervisor) failedNow() bool {
	select {
	case <-s.stopped:
		return true
	default:
		return false
	}
}

func (s *supervisor) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

func (s *supervisor) reason(ctx context.Context) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return "component " + s.failedBy + " failed"
	}
	if ctx.Err() != nil {
		return "stop requested"
	}
	return "start did not complete"
}

func (s *supervisor) stopping() {
	s.mu.Lock()
	s.isStop = true
	s.mu.Unlock()
}

// runStopGrace is how long the reverse walk waits for a node's Run to return
// after its context is cancelled once the shutdown budget is already spent.
//
// Without it, a node stopping after the budget ran out had no time at all:
// one slow node ahead of it in the walk spent the budget, and every node
// behind it was reported as "did not stop" — an ERROR per node — even when
// its Run returned the instant it was cancelled, because returning takes a
// scheduler tick that a zero wait never grants. The grace is per node, so a
// walk overruns the budget by at most runStopGrace for each node whose Run
// really is stuck; a node that returns promptly costs nothing. It is short
// against the pod's headroom past the budget (terminationGracePeriodSeconds
// is PRE_STOP_DELAY + SHUTDOWN_TIMEOUT + 5s).
var runStopGrace = 250 * time.Millisecond

// awaitRun waits for r's Run to return: until ctx — the shutdown budget —
// ends, then up to runStopGrace more. It reports whether Run returned.
func awaitRun(ctx context.Context, r *run) bool {
	select {
	case <-r.done:
		return true
	case <-ctx.Done():
	}
	grace := time.NewTimer(runStopGrace)
	defer grace.Stop()
	select {
	case <-r.done:
		return true
	case <-grace.C:
		return false
	}
}

// stopNode is one step of the reverse walk: Drain, end Run and wait for it,
// Close — each bounded by ctx, the shutdown budget — then the node's phase
// line.
func (s *supervisor) stopNode(ctx context.Context, drainer *serverkit.Drainer, i int) {
	n, r := s.nodes[i], s.runs[i]
	start := time.Now()
	attrs := []any{"node", n.Name}
	if n.Drain != nil {
		if err := n.Drain(ctx); err != nil {
			s.logger.ErrorContext(ctx, "supervise: drain error", "node", n.Name, "error", err)
			attrs = append(attrs, "drain_error", err.Error())
		}
	}
	if r != nil {
		r.cancel()
		if !awaitRun(ctx, r) {
			s.logger.ErrorContext(ctx, "supervise: node did not stop within the shutdown budget", "node", n.Name)
			attrs = append(attrs, "run_still_running", true)
		}
	}
	if n.Close != nil {
		if err := n.Close(ctx); err != nil {
			s.logger.ErrorContext(ctx, "supervise: close error", "node", n.Name, "error", err)
			attrs = append(attrs, "close_error", err.Error())
		}
	}
	drainer.Phase("node", start, "drain: node stopped", attrs...)
}
