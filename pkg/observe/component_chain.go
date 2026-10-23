// File: component_chain.go — the IN-PROCESS component middleware chain.
//
// This is the in-process twin of the Connect edge interceptor chain
// (Chain / DefaultMiddlewares in middleware.go). Those interceptors wrap
// every RPC at the transport boundary; a ComponentChain wraps every method
// of an internal component (a contract.go Service) at the in-process call
// boundary — one span, one metric sample and one log record per method call
// (successes sampled only when sampling is turned on; see log_policy.go) —
// without the method's body knowing anything about it.
//
// A forge-generated decorator (middleware_gen.go) routes each interface
// method through the chain with a single line:
//
//	func (o *forgeMiddlewareService) Do(ctx context.Context, in DoInput) (DoResult, error) {
//	    return o.chain.Around(ctx, "checkout.Do", func(ctx context.Context) (DoResult, error) {
//	        return o.inner.Do(ctx, in)
//	    })
//	}
//
// Every contract package whose constructor returns its Service interface gets
// the decorator unless it opts out with `// forge:no-observe`. Its chain is
// DefaultComponentChain, or — when the package declares the optional owned
// seam observe_chain.go (newObserveChain) — whatever that seam assembles from
// the middlewares below plus any the user adds. The generated decorator never
// names a middleware itself: the chain is a runtime / composition concern,
// exactly like the Connect chain.
package observe

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// ComponentOp is the terminal in-process operation a ComponentChain wraps:
// it runs the inner method call and returns only its error. A method's
// non-error results are captured out of the caller's closure (see Around /
// Run), so a middleware never has to be generic over the return type — it
// observes identity, duration, and error status only, which is all a span,
// metric, or log line needs.
type ComponentOp func(context.Context) error

// ComponentMiddleware wraps one in-process method call. Implementations
// start a span, time the call, recover a panic, emit a log line, etc.,
// then invoke next(ctx) and observe the returned error. method is the
// fully-qualified operation name ("<pkg>.<Method>").
//
// A middleware MUST call next(ctx) exactly once (passing a possibly-
// enriched ctx) and return its error — the chain has no notion of
// short-circuiting a component call.
type ComponentMiddleware interface {
	WrapComponent(ctx context.Context, method string, next ComponentOp) error
}

// ComponentChain is an ordered, reusable stack of ComponentMiddleware shared
// by every method of one component decorator. Build it once in the owned
// observe_chain.go seam (NewComponentChain) and route every method through it
// via Around / Run. The zero value (and a nil *ComponentChain) is a valid
// pass-through: methods still execute, just without instrumentation — so a
// decorator wired in a test harness that skipped chain construction is safe.
//
// mw[0] is the OUTERMOST layer (runs first, sees everything inner); the inner
// method call runs last. This matches the Connect chain's outer-to-inner
// ordering.
type ComponentChain struct {
	mw []ComponentMiddleware
}

// DefaultComponentChain is the chain a generated decorator uses when its
// package declares no observe_chain.go seam of its own: panic recovery, one
// span per call (tracer named scope), the <name>.calls/.errors/.duration
// metrics, and a log line per failed call — successes at successLevel, which
// forge renders from forge.yaml's observability.log_level (default Debug, so
// quiet under a production Info handler). It is exactly what the scaffolded
// seam assembles; write the seam only to change it.
//
// The logger is slog.Default() as of the call, and the tracer and meter come
// from the global providers, which delegate to whatever the process installs
// later — so a decorator built before observe.Setup still reports.
func DefaultComponentChain(scope, name string, successLevel slog.Level) *ComponentChain {
	logger := slog.Default()
	return NewComponentChain(
		RecoverMiddleware(logger),
		TraceMiddleware(otel.Tracer(scope)),
		MetricsMiddleware(otel.Meter(scope), name),
		LogMiddleware(logger, successLevel),
	)
}

// NewComponentChain builds a ComponentChain from mw in outer-to-inner order.
// nil entries are dropped so the owned seam can pass a conditionally-disabled
// middleware straight through (e.g. `enabled ? mw : nil`) without a length
// change, mirroring how Chain treats nil collaborators.
func NewComponentChain(mw ...ComponentMiddleware) *ComponentChain {
	kept := make([]ComponentMiddleware, 0, len(mw))
	for _, m := range mw {
		if m != nil {
			kept = append(kept, m)
		}
	}
	return &ComponentChain{mw: kept}
}

// dispatch threads next through middleware i..end, outermost first. Each
// level allocates one closure per call — the same per-call cost the previous
// hand-written decorator paid for its span-start + RecordCall, and cheap
// relative to any method doing real work (DB / adapter / HTTP).
func (c *ComponentChain) dispatch(ctx context.Context, i int, method string, next ComponentOp) error {
	if c == nil || i >= len(c.mw) {
		return next(ctx)
	}
	return c.mw[i].WrapComponent(ctx, method, func(ctx context.Context) error {
		return c.dispatch(ctx, i+1, method, next)
	})
}

// Run routes an error-only (or result-captured) operation through the chain
// and returns its error. It is the general primitive: the generated decorator
// uses it directly for error-only methods and — capturing results in the
// closure — for methods with multiple non-error results. A nil chain runs
// next directly.
func (c *ComponentChain) Run(ctx context.Context, method string, next ComponentOp) error {
	return c.dispatch(ctx, 0, method, next)
}

// Around routes a value-returning method through the chain. T is the method's
// single non-error result; the value is captured out of the closure so the
// chain only ever sees the error. It keeps the generated per-method body a
// one-liner for the common (T, error) shape.
//
// T is a parameter of the METHOD, not of ComponentChain: one chain instance is
// shared by every method of a component, and those methods return different
// types. Before Go 1.27 that forced this to be a package-level function taking
// the chain as an argument, since only functions could carry type parameters.
func (c *ComponentChain) Around[T any](ctx context.Context, method string, next func(context.Context) (T, error)) (T, error) {
	var out T
	err := c.dispatch(ctx, 0, method, func(ctx context.Context) error {
		var e error
		out, e = next(ctx)
		return e
	})
	return out, err
}

// ─── Standard middlewares ───────────────────────────────────────────────
//
// Each is a small value type constructed by the owned seam. All degrade
// cleanly on a nil dependency (nil tracer/meter → pass-through, nil logger →
// slog.Default), so a decorator wired without a configured OTel SDK still
// works — the same nil-tolerance the Connect interceptors and the LogCall /
// TraceCall / NewCallMetrics helpers already have.

// RecoverMiddleware converts a panic in the inner call (or any inner
// middleware) into an error, logs it with a stack trace, and returns it — so
// one component's panic can't tear down the caller. It is the OUTERMOST layer
// in the canonical seam so it observes panics from every inner middleware too.
func RecoverMiddleware(logger *slog.Logger) ComponentMiddleware {
	return recoverMiddleware{logger: logger}
}

type recoverMiddleware struct{ logger *slog.Logger }

func (m recoverMiddleware) WrapComponent(ctx context.Context, method string, next ComponentOp) (err error) {
	defer func() {
		if r := recover(); r != nil {
			logger := m.logger
			if logger == nil {
				logger = slog.Default()
			}
			LogPanic(ctx, logger, method+": panic recovered", r)
			err = fmt.Errorf("panic in %s: %v", method, r)
		}
	}()
	return next(ctx)
}

// TraceMiddleware opens one OTel span per call named method, threads the
// span's context into the inner call, and records the returned error onto the
// span. nil tracer disables tracing (pass-through) — same degradation as
// TraceCall.
func TraceMiddleware(tracer trace.Tracer) ComponentMiddleware {
	return traceMiddleware{tracer: tracer}
}

type traceMiddleware struct{ tracer trace.Tracer }

func (m traceMiddleware) WrapComponent(ctx context.Context, method string, next ComponentOp) error {
	if m.tracer == nil {
		return next(ctx)
	}
	ctx, span := m.tracer.Start(ctx, method)
	defer span.End()
	err := next(ctx)
	RecordSpanError(span, err)
	return err
}

// MetricsMiddleware records call / error / duration metrics under the
// "<namespace>.{calls,errors,duration}" instruments (see NewCallMetrics),
// tagging each with the method attribute set to the operation name. nil meter
// yields a no-op middleware, so a chain built without a configured OTel SDK
// is safe.
func MetricsMiddleware(meter metric.Meter, namespace string) ComponentMiddleware {
	return metricsMiddleware{metrics: NewCallMetrics(meter, namespace)}
}

type metricsMiddleware struct{ metrics *CallMetrics }

func (m metricsMiddleware) WrapComponent(ctx context.Context, method string, next ComponentOp) error {
	start := time.Now()
	err := next(ctx)
	m.metrics.RecordCall(ctx, method, start, err)
	return err
}

// LogMiddleware logs component calls with the method name as the message and
// the duration as an attribute.
//
//   - Every FAILED call is written with the error and its error_class, at the
//     level its class earns (see error_class.go): ERROR for a server fault —
//     which is every error nothing classified — and the user-error level
//     (INFO; WithUserErrorLevel) for a svcerr user error or a cancellation.
//   - A call whose error the seam declared an EXPECTED OUTCOME
//     (WithExpectedErrors / WithExpectedErrorFunc — a storage adapter's "no
//     such object") is not a failure: it is logged like a success, below,
//     with the error and expected=true attached.
//   - Every SUCCESSFUL call is written at level — which the owned seam wires
//     from forge.yaml's observability.log_level (default Debug: quiet under a
//     production Info handler) — unless success sampling is on. Sampling is
//     opt-in, exactly as at the RPC edge: WithSuccessSampling turns it on
//     for this layer. Sampled, a method's first success is written, then at
//     most one per window, carrying `suppressed`, the successes of that
//     method since the previous record that were not written. A success at
//     or above DefaultSlowThreshold is always written, with slow=true.
//
// Sampling is what keeps this layer readable at DEBUG when a method runs on
// a tight loop: one method on a 10s work loop was two thirds of a dev
// stack's log. See log_policy.go.
//
// opts are the RPC edge's: WithSuccessSampling, WithSlowThreshold,
// WithSuccessLevel keyed by the "<pkg>.<Method>" operation name,
// WithExpectedErrors, WithUserErrorLevel and WithErrorClassifier. nil logger
// falls back to slog.Default.
func LogMiddleware(logger *slog.Logger, level slog.Level, opts ...LogOption) ComponentMiddleware {
	return &logMiddleware{logger: logger, policy: newLogPolicy(level, opts)}
}

type logMiddleware struct {
	logger *slog.Logger
	policy *logPolicy
}

func (m *logMiddleware) WrapComponent(ctx context.Context, method string, next ComponentOp) error {
	start := m.policy.now()
	err := next(ctx)
	elapsed := m.policy.now().Sub(start)
	logger := m.logger
	if logger == nil {
		logger = slog.Default()
	}
	expected := m.policy.isExpected(err)
	if err != nil && !expected {
		class := m.policy.classOf(err)
		logger.LogAttrs(ctx, m.policy.failureLevel(err, class), method,
			slog.Duration("duration", elapsed),
			slog.Any("error", err),
			slog.String(ErrorClassKey, class.String()),
		)
		return err
	}
	level, why, ok := m.policy.outcome(ctx, logger, method, elapsed, true, expected)
	if !ok {
		return err
	}
	attrs := []slog.Attr{slog.Duration("duration", elapsed)}
	if expected {
		attrs = append(attrs, slog.Any("error", err), slog.Bool("expected", true))
	}
	if why.Key != "" {
		attrs = append(attrs, why)
	}
	logger.LogAttrs(ctx, level, method, attrs...)
	return err
}
