package serverkit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
)

// Run starts the server and blocks until SIGINT/SIGTERM or a fatal
// error. It takes an ALREADY-COMPOSED Server (handler with services
// mounted, selected workers/operators, an OnShutdown closure) and owns
// only the uniform lifecycle: logger init, health probes, the HTTP edge
// (CORS/security/request-id driven by Config + Server's factory
// fields), listener bind, worker supervision, operator-manager gating,
// graceful shutdown, and pprof.
//
// Service SELECTION (the old args/names filter) and composition (mux
// build, service mount, interceptor chain, OTel setup, auto-migrate) all
// happen ABOVE serverkit in the generated cmd-server shim. serverkit no
// longer receives names and knows nothing about which services run.
//
// Run is the only entry point projects call. Every other type and
// helper exists to shape the Server surface or document the lifecycle.
func Run(ctx context.Context, cfg Config, srv Server) error {
	cfg.Normalize()

	// Handler is required, but the Mux ergonomics let a composition root
	// leave it nil and rely on Server.Mux as the handler (the common case:
	// no REST swap, no outer wrapper). Fall back to Mux before failing.
	if srv.Handler == nil {
		if srv.Mux != nil {
			srv.Handler = srv.Mux
		} else {
			return fmt.Errorf("serverkit.Run: Server.Handler is required (or set Server.Mux)")
		}
	}
	if cfg.Addr == "" {
		return fmt.Errorf("serverkit.Run: Config.Addr is required")
	}
	if err := cfg.Probes.Validate(); err != nil {
		return fmt.Errorf("serverkit.Run: Config.Probes: %w", err)
	}
	if len(srv.Operators) > 0 && srv.RunOperators == nil {
		return fmt.Errorf("serverkit.Run: Server.Operators is non-empty but Server.RunOperators is nil")
	}

	// Fail closed on configured-but-unwired edge layers. CORS and security
	// headers are SECURITY controls: a composition root that configures
	// them (origins set, SecurityHeaders=true) but forgets to supply the
	// factory must NOT silently serve with the layer absent. Reject
	// loudly instead.
	if cfg.CORSEnabled() && srv.CORSMiddleware == nil {
		return fmt.Errorf("serverkit.Run: CORS is enabled (origins set, or Environment=%s) but Server.CORSMiddleware is nil — refusing to serve with CORS unenforced", EnvDevelopment)
	}
	if cfg.SecurityHeaders && srv.SecurityHeadersMiddleware == nil {
		return fmt.Errorf("serverkit.Run: Config.SecurityHeaders is enabled but Server.SecurityHeadersMiddleware is nil — refusing to serve without security headers")
	}

	// Logger: prefer the caller's (so mount-time and run-time logs share
	// attrs); build one from Config only when the caller passed nil.
	logger := srv.Logger
	if logger == nil {
		logger = newLogger(cfg)
	}
	slog.SetDefault(logger)

	// Warn loudly when running in development mode. Development mode enables
	// permissive defaults (e.g. origin-reflecting CORS, security headers off)
	// for local ergonomics — this must never be used in production. The
	// permissive behavior is selected by the caller (at mount time) based on
	// cfg.Environment.
	if cfg.IsDevelopment() {
		logger.WarnContext(ctx, "running in development mode — permissive edge defaults are enabled (CORS reflects any origin, HSTS off). NEVER set ENVIRONMENT=development in production.")
	}

	// OTel: serverkit OWNS OpenTelemetry setup (the generated cmd/otel.go
	// shim is gone). observe.Setup installs the global trace/metric
	// providers from cfg.OTLPEndpoint + cfg.ServiceName and the standard
	// OTEL_* environment, always wires the Prometheus reader, and returns
	// the /metrics handler (mounted on the top mux below, IN FRONT of the
	// edge so scrapers bypass CORS/auth) and a shutdown fn (flushed in the graceful-shutdown sequence). A setup
	// error is logged, not fatal — projects depending on OTLP fail config
	// validation before Run.
	otelShutdown, metricsHandler, otelErr := SetupTelemetry(ctx, cfg)
	if otelErr != nil {
		logger.ErrorContext(ctx, "failed to initialize OpenTelemetry", "error", otelErr)
	}

	// Readiness gate and drain budget: opened AFTER the listener binds (see
	// ln/Serve below) and flipped to draining at the top of shutdown. It is
	// only the FIRST half of /readyz's answer — the dependency checks in
	// srv.ReadyChecks are the other half, and they run per request.
	// Its Meter, and the Streams' below, stay nil: the global meter that
	// observe.Setup installed above.
	drainer := NewDrainer(cfg)
	drainer.Logger = logger

	// The edge: serverkit no longer owns the service mux, so it wraps the
	// CALLER's handler only. The Listener routes /healthz + /readyz (and
	// /flow-health, /metrics) to their own handlers IN FRONT of it — they are
	// never gated behind CORS, auth, or security-headers middleware.
	//
	// CORS and security headers are project-supplied factories (the matching
	// logic stays in pkg/middleware); serverkit owns only the gating, and a
	// configured-but-unwired factory was already rejected above (fail
	// closed). The request id is outermost of serverkit's layers, and the
	// project's HTTPMiddleware wraps them all, inside the write-stall guard.
	edge, err := Edge{
		CORS:            srv.CORSMiddleware,
		SecurityHeaders: srv.SecurityHeadersMiddleware,
		RequestID:       srv.RequestIDMiddleware,
		Outer:           srv.HTTPMiddleware,
	}.Wrap(cfg)
	if err != nil {
		return err
	}

	// Liveness and readiness on cfg.Probes (/healthz, /readyz by default).
	// Readiness is the ROUTABILITY answer: listener bound, not draining, and
	// every registered dependency reachable. It is what a rolling deploy
	// believes, so it runs the checks live on each request rather than
	// reporting a value latched at boot. See readiness.go.
	front := cfg.Probes.routes(drainer.ReadinessHandler(srv.ReadyChecks, cfg.ReadinessTimeout, logger))
	// /flow-health: the APP-FLOW assertion endpoint. Mounted only when the
	// caller registered FlowChecks. Like /healthz + /readyz it sits IN FRONT
	// of the edge (no CORS/auth) and is STATUS-ONLY — 200 when every check
	// passes, 503 when any fails, plus a terse non-sensitive aggregate. This
	// is what `forge env smoke` curls to catch green-while-broken app flows.
	if len(srv.FlowChecks) > 0 {
		front["/flow-health"] = flowHealthHandler(srv.FlowChecks)
	}
	// /metrics is mounted in front of the edge so Prometheus scrapers reach
	// it without CORS/auth/security-headers. serverkit owns it now that it
	// owns OTel setup.
	if metricsHandler != nil {
		front["/metrics"] = metricsHandler
	}

	// The process's one Listener: the address, TLS and timeouts from cfg, its
	// own Streams registry (installed into every request's context, so a
	// LongLived route or StreamDrainInterceptor binds to it without the
	// composition root handing it around, and drained soft-then-hard by
	// Listener.Drain below), and the caller's already-mounted handler. With a
	// Server.Chain the listener also checks every mounted service's declared
	// audience against the chain's, and refuses to serve a mismatch.
	listener, err := newListener(ListenerDeps{
		Config: cfg.Listener(),
		Chain:  srv.Chain,
		Routes: &handlerRoutes{handler: srv.Handler, services: srv.MountedNames()},
		Logger: logger,
		Edge:   edge,
		Front:  front,
	})
	if err != nil {
		return err
	}

	// Graceful shutdown: serverkit owns the signal context. The caller-
	// supplied ctx governed composition (mount, migrate, OTel setup)
	// before Run was entered; from here the signal-derived ctx drives the
	// serve loop and shutdown.
	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// failComponent dispatches a supervised-component failure (worker
	// Start/RunContext error, RunOperators error) on Config.FailurePolicy.
	// FailProcess (the default) records the first failure and cancels
	// runCtx so the normal graceful-shutdown path executes and Run
	// returns the error — the pod restarts loudly instead of serving on
	// with a silently-dead component. Ignore logs and continues.
	var (
		componentMu      sync.Mutex
		componentFailure error
	)
	failComponent := func(component string, err error) {
		if cfg.FailurePolicy == Ignore {
			logger.ErrorContext(ctx, "component failed — continuing (FailurePolicy=Ignore)",
				"component", component, "error", err)
			return
		}
		logger.ErrorContext(ctx, "component failed — terminating process (FailurePolicy=FailProcess)",
			"component", component, "error", err)
		componentMu.Lock()
		if componentFailure == nil {
			componentFailure = fmt.Errorf("%s failed: %w", component, err)
		}
		componentMu.Unlock()
		stop()
	}

	// Composition is done: hand the address from the boot probes (if the
	// composition root started them) to the real listener.
	boot := BootProbesFrom(ctx)
	boot.release()

	listenCtx := runCtx
	if boot != nil {
		listenCtx = context.WithValue(runCtx, bootKey{}, boot)
	}
	if err := listener.Listen(listenCtx); err != nil {
		return err
	}

	// The listener is bound and accepting connections; flip readiness
	// so /readyz starts returning 200. Run below hands off to the
	// accept loop without a second bind step.
	drainer.MarkReady()

	// The listener's Run context ends only AFTER its Drain (below): ending
	// it first is a hard stop, and the signal that ends runCtx is what
	// STARTS the graceful drain.
	listenerCtx, listenerCancel := context.WithCancel(context.WithoutCancel(runCtx))
	defer listenerCancel()
	errCh := make(chan error, 1)
	go func() {
		if serveErr := listener.Run(listenerCtx); serveErr != nil {
			errCh <- serveErr
		}
	}()

	// The pprof endpoint, on a listener of its own. pprof must never be
	// mounted on the public handler — it exposes heap/goroutine/profile
	// endpoints that can leak memory contents and stall a running process —
	// and the scaffold therefore defaults PprofAddr to a LOOPBACK address
	// (127.0.0.1:6060) rather than leaving it empty. Always-on is the point:
	// a process being OOMKilled can report how much memory it holds but
	// never what, and the answer only exists while the process is still
	// alive. Loopback is what makes always-on safe.
	//
	//	go tool pprof http://localhost:6060/debug/pprof/heap
	//	kubectl port-forward <pod> 6060:6060   # then the line above
	//
	// It is the same component a composition graph runs (Pprof): a bind
	// failure is logged and survived, never a component failure, whatever
	// the FailurePolicy says — a debug surface that can take the service
	// down is worse than none. Its context outlives the drain, so the
	// teardown below shuts it down gracefully, last.
	pprofSrv := NewPprof(cfg, logger)
	pprofCtx, pprofCancel := context.WithCancel(context.WithoutCancel(runCtx))
	defer pprofCancel()
	go func() { _ = pprofSrv.Run(pprofCtx) }()

	// Start workers in background goroutines. Each worker gets its own
	// context derived from the shared supervisor context: cancelling
	// workerCtx on shutdown fans out to every per-worker ctx, and one
	// worker exiting early can't disturb its siblings.
	//
	// Selection already happened above serverkit — srv.Workers holds
	// exactly the workers this process should run, so there's no name
	// filter here. Workers that implement the optional ContextWorker
	// interface get RunContext (the ctx-aware lifecycle, preferred);
	// everything else falls back to the legacy Start signature
	// unchanged. See the ContextWorker doc for the full shutdown
	// contract.
	workerCtx, workerCancel := context.WithCancel(runCtx)
	defer workerCancel()
	var workerWg sync.WaitGroup

	for _, w := range srv.Workers {
		workerWg.Add(1)
		go func(w Worker) {
			defer workerWg.Done()
			wctx, wcancel := context.WithCancel(workerCtx)
			defer wcancel()
			if cw, ok := w.(ContextWorker); ok {
				logger.InfoContext(ctx, "worker starting", "worker", w.Name(), "lifecycle", "run-context")
				if workErr := cw.RunContext(wctx); workErr != nil && !errors.Is(workErr, context.Canceled) {
					failComponent("worker "+w.Name(), workErr)
				}
				return
			}
			logger.InfoContext(ctx, "worker starting", "worker", w.Name())
			if startErr := w.Start(wctx); startErr != nil && !errors.Is(startErr, context.Canceled) {
				failComponent("worker "+w.Name(), startErr)
			}
		}(w)
	}

	// Start controller manager for operators (if any).
	//
	// Operator gating is the caller's, entirely: whether this process runs
	// a controller manager is decided by whether it populated
	// srv.Operators, in the composition root, in code. serverkit adds no
	// gate of its own — a deployment that must not run a manager runs the
	// command that populates no operators, so the same binary never
	// behaves differently for reasons its own source does not show.
	//
	// The operator goroutine is supervised on operatorWg — the SAME
	// WaitGroup pattern as workers — so Run WAITS for RunOperators to drain
	// before returning. Without this wait, runCtx cancellation on shutdown
	// would let Run return mid-reconcile (the process could exit before
	// controller-runtime finishes its own ctx-cancel drain) AND a
	// failComponent write that lands after the final componentFailure read
	// would be silently lost.
	var operatorWg sync.WaitGroup
	operatorsSupervised := len(srv.Operators) > 0
	if operatorsSupervised {
		operatorWg.Add(1)
		go func() {
			defer operatorWg.Done()
			if opErr := srv.RunOperators(runCtx, logger, cfg.OperatorHealthProbeAddr); opErr != nil && !errors.Is(opErr, context.Canceled) {
				failComponent("controller manager", opErr)
			}
		}()
	}

	var runErr error
	select {
	case err := <-errCh:
		logger.ErrorContext(ctx, "server error", "error", err)
		runErr = fmt.Errorf("server failed: %w", err)
	case <-runCtx.Done():
	}
	logger.InfoContext(ctx, "server stopping")

	// Graceful shutdown sequence (drain.go):
	//   1. Flip readiness to "draining" so /readyz starts failing.
	//   2. Sleep pre_stop_delay so load balancers observe the failing
	//      probe and stop routing new traffic to this replica.
	//   3. DrainHTTP, bounded by shutdown_timeout: httpSrv.Shutdown stops
	//      accepting and drains in-flight unary requests while the
	//      long-lived streams drain soft-then-hard (streams.go), so a stream
	//      that would never finish on its own cannot hold the drain for the
	//      whole budget and then be cut without a status.
	// Without step 1 + 2, httpSrv.Shutdown stops accepting new conns
	// immediately but the LB keeps sending them until it next polls
	// /readyz — producing brief but real 502s on every rollout.
	// Each phase writes one "drain: ..." INFO line carrying `phase` and its
	// `elapsed`, and records forge.server.drain.duration (drain_phases.go):
	// readiness, pre_stop and total from the drainer, listener from
	// Listener.Drain, and only workers, operators and teardown from here.
	drainer.Begin()

	shutdownCtx, cancel := drainer.ShutdownContext()
	defer cancel()

	// TEARDOWN ORDER, innermost dependency last. Every step below is
	// something a step ABOVE it may still be using:
	//
	//  1. Drain in-flight HTTP. Those requests are still reading the DB and
	//     handing work to workers, so nothing they depend on may be torn
	//     down first. Long-lived streams end inside the same window, by
	//     DrainReserve before its deadline.
	//  2. Stop workers, then operators.
	//  3. OnShutdown — the caller's teardown, which is where the serving
	//     *sql.DB gets closed. Closing it before (1) would fail the very
	//     requests the drain exists to complete.
	//  4. Flush telemetry LAST, so the drain window's own spans, metrics
	//     and error records are included. Flushing before the drain exported
	//     everything up to that instant and then discarded whatever the
	//     shutdown itself produced — precisely the window an operator reads
	//     when a deploy goes wrong.
	if shutErr := listener.Drain(shutdownCtx); shutErr != nil {
		logger.ErrorContext(ctx, "server shutdown error", "error", shutErr)
	}
	listenerCancel()
	mark := time.Now()

	workerCancel()
	if len(srv.Workers) > 0 && !waitWithin(shutdownCtx, &workerWg) {
		logger.ErrorContext(ctx, "workers did not drain within the shutdown budget",
			"shutdown_timeout", drainer.shutdownTimeout())
	}
	for _, w := range srv.Workers {
		if stopErr := w.Stop(shutdownCtx); stopErr != nil {
			logger.ErrorContext(ctx, "worker stop error", "worker", w.Name(), "error", stopErr)
		}
	}
	mark = drainer.Phase("workers", mark, "drain: workers stopped", "workers", len(srv.Workers))

	// Wait for the operator/controller-manager goroutine to drain. runCtx
	// is already cancelled (it's what dropped us out of the serve select),
	// so RunOperators is unwinding controller-runtime's own ctx-cancel
	// drain; this Wait ensures Run does not return before that finishes and
	// that any failComponent write from RunOperators lands before the final
	// componentFailure read below. Bounded by shutdownCtx: an operator that
	// never returns must not hold the process past ShutdownTimeout, because
	// the platform's own grace period is about to SIGKILL it anyway and a
	// clean-ish exit beats being shot mid-flush.
	if operatorsSupervised && !waitWithin(shutdownCtx, &operatorWg) {
		logger.ErrorContext(ctx, "controller manager did not drain within the shutdown budget",
			"shutdown_timeout", drainer.shutdownTimeout())
	}
	if operatorsSupervised {
		mark = drainer.Phase("operators", mark, "drain: operators stopped", "operators", len(srv.Operators))
	}

	// OnShutdown is the caller-composed teardown (closing the serving DB
	// pool, flushing app-owned buffers). Runs after HTTP has drained and
	// workers have stopped, so nothing it closes is still in use.
	if srv.OnShutdown != nil {
		if shutErr := srv.OnShutdown(shutdownCtx); shutErr != nil {
			logger.ErrorContext(ctx, "shutdown error", "error", shutErr)
		}
	}

	if shutErr := pprofSrv.Close(shutdownCtx); shutErr != nil {
		logger.ErrorContext(ctx, "pprof shutdown error", "error", shutErr)
	}
	pprofCancel()
	drainer.Phase("teardown", mark, "drain: teardown done")
	drainer.Complete()

	// OTel flush — serverkit owns it now (folded out of the cmd shim). LAST,
	// so it exports everything the steps above produced.
	if otelShutdown != nil {
		if shutErr := otelShutdown(shutdownCtx); shutErr != nil {
			LogTelemetryFlushError(ctx, logger, "otel shutdown error", shutErr)
		}
	}

	// A component failure (worker/operator under FailProcess) initiated
	// this shutdown: surface it as Run's return so the process exits
	// non-zero and the platform supervisor restarts it loudly.
	componentMu.Lock()
	if runErr == nil && componentFailure != nil {
		runErr = componentFailure
	}
	componentMu.Unlock()
	return runErr
}

// waitWithin blocks until wg drains or ctx expires, reporting whether the
// drain finished. sync.WaitGroup.Wait takes no context, so a bare Wait sits
// OUTSIDE ShutdownTimeout entirely: one worker that ignores its cancelled
// context pins the process until the platform SIGKILLs it, skipping every
// remaining teardown step — including the telemetry flush that would have
// explained what happened.
func waitWithin(ctx context.Context, wg *sync.WaitGroup) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
	}
	// The deadline can expire in the same instant the group drains, and
	// select picks a ready case at random. Re-check before reporting a
	// timeout so a clean shutdown is never logged as a stuck one.
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// NewLogger builds the slog.Logger serverkit would use from Config:
// LOG_FORMAT picks between structured JSON (for log aggregators) and
// human-friendly text (for local dev tails); anything other than "text"
// emits JSON. Exported so the cmd layer — which now composes the server
// and bootstraps BEFORE calling Run — can build the SAME logger and pass
// it as Server.Logger, keeping mount-time and run-time logs consistent.
//
// Every record goes through observe.NewErrorClassHandler, so a hand-written
// logger.Error carrying a user error (svcerr.Classify) is written at INFO
// rather than paging. A project that wraps this handler with something that
// routes on level — a Sentry forwarder — wraps the result in
// observe.NewErrorClassHandler again, OUTSIDE its forwarder; the inner one
// then sees an already-classified record and leaves it alone.
func NewLogger(cfg Config) *slog.Logger { return newLogger(cfg) }

// newLogger builds the slog.Logger Run dispatches on.
func newLogger(cfg Config) *slog.Logger {
	var handler slog.Handler
	switch cfg.LogFormat {
	case "text":
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})
	default:
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})
	}
	// The error-class handler is outermost so the trace-id / OTLP log
	// bridge sees the record at its lowered (user-error) level. The observe
	// config is the one SetupTelemetry passes Setup, instance id included, so
	// the resource on a line logged before Setup is the one it builds.
	instanceID, _ := os.Hostname()
	return slog.New(observe.NewErrorClassHandler(observe.NewLogHandler(handler, observeConfig(cfg, instanceID))))
}

// observeConfig projects the serverkit Config onto observe.Config.
func observeConfig(cfg Config, instanceID string) observe.Config {
	return observe.Config{
		Development:    cfg.IsDevelopment(),
		ServiceName:    cfg.ServiceName,
		ServiceVersion: cfg.ServiceVersion,
		OTLPEndpoint:   cfg.OTLPEndpoint,
		InstanceID:     instanceID,
	}
}

// schemaVersion reports an unstamped build as "dev", the same word the version
// command prints, instead of an empty string a consumer must special-case.
func schemaVersion(v string) string {
	if v == "" {
		return "dev"
	}
	return v
}
