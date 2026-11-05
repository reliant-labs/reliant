package serverkit

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/reliant-labs/forge/pkg/serverkit/drainbudget"
)

// drain.go — the graceful-shutdown sequence, as pieces a hand-built main can
// use before (or instead of) Run.
//
// A pod leaves the Service's EndpointSlices, and a cloud load balancer's
// backend set, ASYNCHRONOUSLY with respect to SIGTERM. A process that stops
// serving the moment it is signalled is still a routing target for several
// seconds, and every request sent its way in that window fails. So the order
// is:
//
//  1. Begin: /readyz answers 503 "draining", then keep serving for
//     PreStopDelay so every load balancer observes the failing probe.
//  2. ShutdownContext: everything after the pause shares ONE budget,
//     ShutdownTimeout, derived from context.Background — the run context is
//     already cancelled by the signal that started the drain.
//  3. DrainHTTP under that budget: http.Server.Shutdown stops accepting and
//     drains the unary requests while the listener's Streams drain
//     soft-then-hard (streams.go), so long-lived streams end with a status
//     their clients reconnect on instead of holding the whole budget.
//  4. The caller's own teardown, innermost dependency last.
//
// forge derives a pod's terminationGracePeriodSeconds from PRE_STOP_DELAY +
// SHUTDOWN_TIMEOUT, so a process that ignores either cannot stay inside the
// grace period it was given.

// Drainer is a process's readiness gate and drain budget: the part of the
// shutdown sequence Run executes that is not specific to one http.Server.
// Run uses one; a main that runs its own listener (a worker, a gateway)
// uses the same type so its probes and budget behave identically.
//
// The zero value is usable. Both durations follow Config's rule: zero is the
// default (5s / 30s), negative is none — no pause, or a shutdown window that
// is over as it opens. It is safe for concurrent use:
// readiness handlers read it from request goroutines while the shutdown path
// writes it.
//
// It writes the shutdown's own phase lines (drain_phases.go): Begin the
// readiness flip and the pre-stop pause, Complete the total.
type Drainer struct {
	// PreStopDelay is how long the process keeps serving after readiness
	// flips. See Config.PreStopDelay.
	PreStopDelay time.Duration
	// ShutdownTimeout bounds every teardown step after the pre-stop pause.
	// See Config.ShutdownTimeout.
	ShutdownTimeout time.Duration
	// Logger receives the phase lines; nil is slog.Default().
	Logger *slog.Logger
	// Meter records the phases in forge.server.drain.duration; nil is the
	// global OpenTelemetry meter.
	Meter metric.Meter

	ready    atomic.Bool
	draining atomic.Bool

	mu     sync.Mutex
	phases *drainPhases // set by Begin; nil again once Complete has run
	begun  time.Time    // when Begin ran
}

// NewDrainer returns a Drainer carrying cfg's drain budget.
func NewDrainer(cfg Config) *Drainer {
	cfg.Normalize()
	return &Drainer{PreStopDelay: cfg.PreStopDelay, ShutdownTimeout: cfg.ShutdownTimeout}
}

// MarkReady opens the readiness gate: the listener is bound and accepting.
// /readyz still runs its dependency checks after this.
func (d *Drainer) MarkReady() { d.ready.Store(true) }

// Ready reports whether the gate is open: bound, and not draining.
func (d *Drainer) Ready() bool { return d.ready.Load() && !d.draining.Load() }

// Draining reports whether Begin has run.
func (d *Drainer) Draining() bool { return d.draining.Load() }

// Begin flips readiness to "draining" and then blocks for PreStopDelay so load
// balancers observe the failing probe before anything stops serving. The
// listener stays open for the whole pause — that is the point: requests
// already in flight, and requests a balancer has not yet stopped sending, are
// still answered normally.
//
// It logs the flip ("drain: readiness flipped to draining", phase readiness)
// and the end of the pause (phase pre_stop, with its elapsed).
func (d *Drainer) Begin() {
	p := newDrainPhases(d.Logger, d.Meter)
	start := time.Now()
	d.mu.Lock()
	d.phases, d.begun = p, start
	d.mu.Unlock()

	d.draining.Store(true)
	delay := max(d.preStopDelay(), 0)
	p.logger.Info("drain: readiness flipped to draining", "phase", "readiness", "pre_stop_delay", delay)
	if delay > 0 {
		time.Sleep(delay)
	}
	p.done("pre_stop", start, "drain: pre-stop pause over; draining the listener")
}

// Complete ends the shutdown's log: one "drain: complete" line with the
// total since Begin and the ShutdownTimeout it ran against, recorded as the
// phase total. Call it when the teardown is done and before telemetry is
// flushed, so the point is exported. It does nothing before Begin or after
// the first call.
func (d *Drainer) Complete() {
	d.mu.Lock()
	p, begun := d.phases, d.begun
	d.phases = nil
	d.mu.Unlock()
	if p != nil {
		p.done("total", begun, "drain: complete", "shutdown_timeout", d.shutdownTimeout())
	}
}

// Phase records one phase of the shutdown this drainer began — Run's
// workers, operators and teardown, or a supervisor's own — begun at start:
// one INFO line, msg plus attrs, with `phase` and its `elapsed`, and the
// duration in forge.server.drain.duration, through the drainer's Logger and
// Meter. It returns the phase's end, the next one's start. Before Begin it
// records nothing and returns now.
func (d *Drainer) Phase(name string, start time.Time, msg string, attrs ...any) time.Time {
	d.mu.Lock()
	p := d.phases
	d.mu.Unlock()
	if p == nil {
		return time.Now()
	}
	return p.done(name, start, msg, attrs...)
}

// ShutdownContext returns the context that bounds every teardown step after
// the pre-stop pause. Derived from context.Background, not from the run
// context: that one is already cancelled by the signal that started the drain.
func (d *Drainer) ShutdownContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d.shutdownTimeout())
}

// ReadinessHandler builds GET /readyz over this gate: 503 "not_ready" before
// MarkReady, 503 "draining" once Begin has run, and otherwise the live
// dependency checks — 200 when every one passes. All checks share one
// timeout, so the probe's worst case stays bounded however many are
// registered. The draining answer short-circuits the checks: no dependency
// can make a replica that is shutting down routable again. See readiness.go
// for the response's no-leak contract.
func (d *Drainer) ReadinessHandler(checks []ReadyCheck, timeout time.Duration, logger *slog.Logger) http.HandlerFunc {
	if timeout <= 0 {
		timeout = defaultReadinessTimeout
	}
	if logger == nil {
		logger = slog.Default()
	}
	return readyzHandler(d, checks, timeout, logger)
}

func (d *Drainer) preStopDelay() time.Duration {
	return drainDuration(d.PreStopDelay, defaultPreStopDelay)
}

func (d *Drainer) shutdownTimeout() time.Duration {
	return drainDuration(d.ShutdownTimeout, defaultShutdownTimeout)
}

// drainDuration is the length of a shutdown phase by Config's rule for
// durations: zero is the default, negative is none (drainbudget.Effective).
func drainDuration(d, def time.Duration) time.Duration {
	return drainbudget.Effective(d, def)
}

// NewHTTPServer builds the serving http.Server Run uses around handler: the
// protocols (HTTP/1.1, HTTP/2 over TLS, and h2c), the write-stall guard, the
// connection timeouts and HTTP/2 stream cap from cfg — and, when streams is
// non-nil, the listener's drain registry: installed into every request's
// context (Streams.Install), so LongLived routes and StreamDrainInterceptor
// bind to it, and its soft phase registered to begin at the start of
// Shutdown, so even a bare srv.Shutdown ends the streams that did not claim
// it. That registration is the ONE thing that runs before Shutdown waits;
// there is no separate drain hook. It is exported for a process that owns its
// own listener and lifecycle; such a process calls DrainHTTP(ctx, srv,
// streams) after a Drainer's Begin.
//
// There is deliberately NO WriteTimeout. It is a deadline on the whole
// response, so it cuts every long-lived response at the same mark — SSE,
// Connect and gRPC server streams, reverse-proxied LLM completions, and
// websockets. Slow-read protection instead belongs to WriteStallGuard, which
// bounds a write that cannot complete without capping a response that takes a
// long time.
//
// h2c is net/http's own (Protocols.SetUnencryptedHTTP2), not x/net's
// h2c.NewHandler. That handler HIJACKS each h2c connection, and a hijacked
// connection is invisible to http.Server.Shutdown: Run's graceful drain
// returned at once while every h2c request was still in flight, so a rolling
// deploy behind an h2c load balancer — every Connect and gRPC call — cut them
// all. A native h2c connection is the server's own, so Shutdown sends it
// GOAWAY and waits for its streams, and each stream is a request the drain
// registry sees. (Native h2c takes prior knowledge, which is what Connect,
// gRPC and load balancers send; an HTTP/1.1 `Upgrade: h2c` request is served
// as HTTP/1.1.)
func NewHTTPServer(cfg Config, handler http.Handler, streams *Streams) *http.Server {
	return newHTTPServer(cfg.Listener(), handler, streams)
}

// newHTTPServer is NewHTTPServer over one listener's block, which a Listener
// carries; cfg must be normalized.
func newHTTPServer(cfg ListenerConfig, handler http.Handler, streams *Streams) *http.Server {
	if streams != nil {
		handler = streams.Install(handler)
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           WriteStallGuard(handler, cfg.WriteStallTimeout),
		Protocols:         protocols,
		HTTP2:             &http.HTTP2Config{MaxConcurrentStreams: cfg.HTTP2MaxConcurrentStreams},
		ReadHeaderTimeout: serverTimeout(cfg.ReadHeaderTimeout),
		ReadTimeout:       serverTimeout(cfg.ReadTimeout),
		IdleTimeout:       serverTimeout(cfg.IdleTimeout),
		MaxHeaderBytes:    1 << 20,
	}
	if streams != nil {
		srv.RegisterOnShutdown(func() { streams.BeginDrain() })
	}
	return srv
}

// serverTimeout maps a normalized Config timeout onto http.Server's field,
// where zero means none: a negative Config value disables the timeout.
func serverTimeout(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}
