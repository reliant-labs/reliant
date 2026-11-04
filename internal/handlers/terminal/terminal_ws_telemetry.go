// Copyright (c) 2025 Reliant Labs
package terminal

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// The terminal websocket is a plain HTTP route outside the Connect chain, so
// none of the chain's otelconnect span, rpc.server.* metrics or trace-stamped
// logs reach it. This file gives it the equivalent: one server span per
// authenticated connection, continuing the browser's trace, and a
// reliant.terminal.* meter. A request refused before authentication is only
// counted (sessions.rejected): until the token is valid the caller chooses
// nothing this process records, neither a trace parent nor an attribute.
//
// Both come from the process's global providers, which forge's observe.Setup
// installs in every server role: the same tracer and meter providers the
// chain's interceptors use, exported the same way (OTLP, and the Prometheus
// endpoint as reliant_terminal_*).
//
// Nothing a user typed or saw is recorded: no terminal contents, no
// environment, no working directory. Byte counts, ids and the outcome only.

const terminalTelemetryScope = "github.com/reliant-labs/reliant/internal/handlers/terminal"

// Outcomes: why a terminal connection ended. One per connection, recorded on
// its span (terminal.outcome) and as the outcome attribute of the
// sessions.ended and session.duration metrics. A closed set, so it is safe as
// a metric attribute.
const (
	// Refused before the upgrade: no session started. An unauthenticated
	// request has no span, only the rejected metric (see reject).
	outcomeUnauthenticated = "unauthenticated"
	outcomeUpgradeFailed   = "upgrade_failed"

	// The workspace could not be placed on a machine.
	outcomeCheckoutMissing   = wsErrorCheckoutMissing // the project is not checked out on the machine
	outcomeWorkspaceNotFound = "workspace_not_found"  // the chat, project or worktree is not the caller's
	outcomePlacementFailed   = "placement_failed"     // resolving the workspace's machine failed

	// The daemon could not open the shell.
	outcomeWorkingDirUnavailable = wsErrorWorkingDirUnavailable
	outcomeWorkingDirMissing     = wsErrorWorkingDirMissing
	outcomeMachineUnavailable    = "machine_unavailable" // terminal.machine_state says which
	outcomeCreateFailed          = "create_failed"
	outcomeSubscribeFailed       = "subscribe_failed"

	// An open session ended.
	outcomeClientClosed   = "client_closed"   // the browser sent a normal or going-away close
	outcomeClientGone     = "client_gone"     // the connection dropped without a close frame
	outcomePingTimeout    = "ping_timeout"    // no pong (or anything else) within the pong timeout
	outcomeShellExited    = "shell_exited"    // the shell exited; terminal.exit_code says how
	outcomeDaemonError    = "daemon_error"    // the daemon reported an error on the output stream
	outcomeOutputEnded    = "output_ended"    // the daemon's output stream ended without an exit
	outcomeInputFailed    = "input_failed"    // forwarding input or a resize to the daemon failed
	outcomeServerDraining = "server_draining" // this server is shutting down (close 1012)
)

// errorOutcomes are the outcomes that are a fault of ours or the daemon's,
// rather than the user's state or a normal end: their span's status is Error.
var errorOutcomes = map[string]bool{
	outcomeUpgradeFailed:   true,
	outcomePlacementFailed: true,
	outcomeCreateFailed:    true,
	outcomeSubscribeFailed: true,
	outcomeDaemonError:     true,
	outcomeInputFailed:     true,
}

// Span attributes. terminal.* for what is specific to a terminal connection;
// user.id is the one the Connect chain's server spans carry.
const (
	attrOutcome      = "terminal.outcome"
	attrReconnect    = "terminal.reconnect"
	attrSessionID    = "terminal.session_id"
	attrProjectID    = "terminal.project_id"
	attrWorktreeID   = "terminal.worktree_id"
	attrMachineState = "terminal.machine_state"
	attrExitCode     = "terminal.exit_code"
	attrBytesIn      = "terminal.bytes_in"
	attrBytesOut     = "terminal.bytes_out"
	attrDurationMS   = "terminal.duration_ms"
)

// terminalSessionDurationBuckets are the session.duration histogram's bounds,
// in seconds. A terminal lives from a refused attempt (milliseconds) to a tab
// left open all day; the SDK's default bounds stop at ~2.8h.
var terminalSessionDurationBuckets = []float64{
	0.1, 1, 5, 30, 60, 300, 900, 1800, 3600, 7200, 14400, 28800, 86400,
}

// terminalWSTelemetry is the tracer, propagator and instruments of one
// TerminalWSHandler.
type terminalWSTelemetry struct {
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator

	active       metric.Int64UpDownCounter
	started      metric.Int64Counter
	ended        metric.Int64Counter
	rejected     metric.Int64Counter
	duration     metric.Float64Histogram
	bytes        metric.Int64Counter
	pingTimeouts metric.Int64Counter

	bytesIn, bytesOut metric.AddOption
}

// terminalTelemetryProviders are where the telemetry goes. Unset fields are
// the process's global providers.
type terminalTelemetryProviders struct {
	tracer     trace.TracerProvider
	meter      metric.MeterProvider
	propagator propagation.TextMapPropagator
}

// newTerminalWSTelemetry builds the handler's telemetry. The global providers
// are read here, at construction; otel's globals delegate to the providers
// observe.Setup installs even when that runs later.
func newTerminalWSTelemetry(p terminalTelemetryProviders) *terminalWSTelemetry {
	if p.tracer == nil {
		p.tracer = otel.GetTracerProvider()
	}
	if p.meter == nil {
		p.meter = otel.GetMeterProvider()
	}
	if p.propagator == nil {
		p.propagator = otel.GetTextMapPropagator()
	}
	meter := p.meter.Meter(terminalTelemetryScope)
	t := &terminalWSTelemetry{
		tracer:     p.tracer.Tracer(terminalTelemetryScope),
		propagator: p.propagator,
		bytesIn:    metric.WithAttributeSet(attribute.NewSet(attribute.String("direction", "in"))),
		bytesOut:   metric.WithAttributeSet(attribute.NewSet(attribute.String("direction", "out"))),
	}
	// An instrument the SDK refuses still comes back usable (a no-op), and
	// the error goes to otel's handler: telemetry never fails the handler.
	var err error
	t.active, err = meter.Int64UpDownCounter("reliant.terminal.sessions.active",
		metric.WithUnit("{session}"),
		metric.WithDescription("Terminal websocket connections currently open."))
	otel.Handle(err)
	t.started, err = meter.Int64Counter("reliant.terminal.sessions.started",
		metric.WithUnit("{session}"),
		metric.WithDescription("Terminal websocket connections upgraded, by whether the browser was reconnecting."))
	otel.Handle(err)
	t.ended, err = meter.Int64Counter("reliant.terminal.sessions.ended",
		metric.WithUnit("{session}"),
		metric.WithDescription("Terminal websocket connections ended, by outcome."))
	otel.Handle(err)
	t.rejected, err = meter.Int64Counter("reliant.terminal.sessions.rejected",
		metric.WithUnit("{session}"),
		metric.WithDescription("Terminal websocket requests refused before the upgrade, by outcome."))
	otel.Handle(err)
	t.duration, err = meter.Float64Histogram("reliant.terminal.session.duration",
		metric.WithUnit("s"),
		metric.WithDescription("How long a terminal websocket connection stayed open, by outcome."),
		metric.WithExplicitBucketBoundaries(terminalSessionDurationBuckets...))
	otel.Handle(err)
	t.bytes, err = meter.Int64Counter("reliant.terminal.bytes",
		metric.WithUnit("By"),
		metric.WithDescription("Terminal bytes relayed: in is browser input to the shell, out is shell output to the browser."))
	otel.Handle(err)
	t.pingTimeouts, err = meter.Int64Counter("reliant.terminal.ping_timeouts",
		metric.WithUnit("{timeout}"),
		metric.WithDescription("Terminal websocket connections closed because the browser stopped answering pings."))
	otel.Handle(err)
	return t
}

// traceCarrier is where the browser's trace context is on an upgrade
// request. A browser WebSocket cannot set request headers, so the web client
// sends traceparent and tracestate as query parameters; a non-browser client
// that can send the headers is read from them first.
func traceCarrier(r *http.Request) propagation.TextMapCarrier {
	if r.Header.Get("traceparent") != "" {
		return propagation.HeaderCarrier(r.Header)
	}
	q := r.URL.Query()
	carrier := propagation.MapCarrier{}
	for _, key := range []string{"traceparent", "tracestate"} {
		if v := q.Get(key); v != "" {
			carrier[key] = v
		}
	}
	return carrier
}

// terminalWSSession is the telemetry of one terminal connection, from the
// upgrade request arriving to the handler returning.
type terminalWSSession struct {
	t     *terminalWSTelemetry
	span  trace.Span
	start time.Time

	reconnect bool
	upgraded  bool
	machine   string // the machine state of a machine_unavailable outcome

	bytesIn, bytesOut atomic.Int64

	mu       sync.Mutex
	outcome  string
	err      error
	exitCode *int32
}

// reject counts a request refused before authentication. It starts no span
// and reads nothing from the request: an unauthenticated caller must not be
// able to choose a trace parent or put an identifier into telemetry.
func (t *terminalWSTelemetry) reject(ctx context.Context, outcome string) {
	t.rejected.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// begin starts an authenticated connection's server span as a child of the
// browser's trace context and returns the context the connection runs under,
// which every log line and daemon command of the connection carries. Called
// only after the token is valid: an authenticated caller's traceparent is
// adopted the way the Connect user chain adopts it
// (otelconnect.WithTrustRemote in internal/grpc/user_chain.go).
func (t *terminalWSTelemetry) begin(r *http.Request, reconnect bool) (context.Context, *terminalWSSession) {
	ctx := t.propagator.Extract(r.Context(), traceCarrier(r))
	route := r.Pattern
	if route == "" {
		route = r.URL.Path
	}
	// Never the URL's query: it carries the user's token.
	attrs := []attribute.KeyValue{
		attribute.String("http.request.method", r.Method),
		attribute.String("http.route", route),
		attribute.Bool(attrReconnect, reconnect),
	}
	ctx, span := t.tracer.Start(ctx, "terminal.session",
		trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attrs...))
	return ctx, &terminalWSSession{t: t, span: span, start: time.Now(), reconnect: reconnect}
}

// placed records the project and worktree once placement has checked they
// are the caller's: before that they are only what the query claimed.
func (s *terminalWSSession) placed(projectID, worktreeID string) {
	if projectID != "" {
		s.span.SetAttributes(attribute.String(attrProjectID, projectID))
	}
	if worktreeID != "" {
		s.span.SetAttributes(attribute.String(attrWorktreeID, worktreeID))
	}
}

// opened records the upgrade: from here the connection counts as a session.
func (s *terminalWSSession) opened(ctx context.Context) {
	s.upgraded = true
	s.t.active.Add(ctx, 1)
	s.t.started.Add(ctx, 1, metric.WithAttributes(attribute.Bool("reconnect", s.reconnect)))
}

// created records the daemon's session id.
func (s *terminalWSSession) created(sessionID string) {
	s.span.SetAttributes(attribute.String(attrSessionID, sessionID))
}

// machineState names the machine state a machine_unavailable outcome was.
func (s *terminalWSSession) machineState(state string) {
	s.machine = state
	s.span.SetAttributes(attribute.String(attrMachineState, state))
}

// shellExited records the shell's exit code for a shell_exited outcome.
func (s *terminalWSSession) shellExited(code int32) {
	s.exitCode = &code
}

// input and output count relayed bytes, as they are relayed, so a session
// open for hours shows its traffic before it closes.
func (s *terminalWSSession) input(ctx context.Context, n int) {
	s.bytesIn.Add(int64(n))
	s.t.bytes.Add(ctx, int64(n), s.t.bytesIn)
}

func (s *terminalWSSession) output(ctx context.Context, n int) {
	s.bytesOut.Add(int64(n))
	s.t.bytes.Add(ctx, int64(n), s.t.bytesOut)
}

// end names why the connection ended. The first call wins: once one pump has
// ended the connection, the others' errors are its consequence, not its
// cause. It reports whether this call was the first.
func (s *terminalWSSession) end(outcome string, err error) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outcome != "" {
		return false
	}
	s.outcome, s.err = outcome, err
	return true
}

// ended is the outcome and error end recorded ("" before it is called).
func (s *terminalWSSession) ended() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outcome, s.err
}

// finish records the connection's end: the span's outcome attributes and
// status, and the ended/duration metrics. Called once, as the handler
// returns.
func (s *terminalWSSession) finish(ctx context.Context) {
	outcome, _ := s.ended()
	if outcome == "" {
		// Every return names its outcome; this is a path that forgot to.
		outcome = "unknown"
	}
	elapsed := time.Since(s.start)
	attrs := []attribute.KeyValue{
		attribute.String(attrOutcome, outcome),
		attribute.Int64(attrBytesIn, s.bytesIn.Load()),
		attribute.Int64(attrBytesOut, s.bytesOut.Load()),
		attribute.Int64(attrDurationMS, elapsed.Milliseconds()),
	}
	if s.exitCode != nil {
		attrs = append(attrs, attribute.Int(attrExitCode, int(*s.exitCode)))
	}
	s.span.SetAttributes(attrs...)
	// A connected machine that did not answer is logged as an error too
	// (logTerminalMachineState); a machine that is not up is not one.
	if errorOutcomes[outcome] || (outcome == outcomeMachineUnavailable && s.machine == terminalMachineNotAnswering) {
		// The outcome, not the error's text: a daemon's error can name a
		// path on the user's machine. The connection's log line carries it.
		s.span.SetAttributes(attribute.String("error.type", outcome))
		s.span.SetStatus(codes.Error, outcome)
	}

	byOutcome := metric.WithAttributes(attribute.String("outcome", outcome))
	if s.upgraded {
		s.t.active.Add(ctx, -1)
		s.t.ended.Add(ctx, 1, byOutcome)
		s.t.duration.Record(ctx, elapsed.Seconds(), byOutcome)
		if outcome == outcomePingTimeout {
			s.t.pingTimeouts.Add(ctx, 1)
		}
	} else {
		s.t.rejected.Add(ctx, 1, byOutcome)
	}
	s.span.End()
}

// logFields are the session's end-of-connection log fields.
func (s *terminalWSSession) logFields() []any {
	outcome, _ := s.ended()
	return []any{
		"outcome", outcome,
		"duration_ms", time.Since(s.start).Milliseconds(),
		"bytes_in", s.bytesIn.Load(),
		"bytes_out", s.bytesOut.Load(),
		"reconnect", s.reconnect,
	}
}
