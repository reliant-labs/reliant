package observe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// RequestIDHeader is the canonical correlation header read on inbound
// requests and echoed onto responses. Mirrors the value used by the
// scaffolded pkg/middleware.RequestIDMiddleware (HTTP layer) so the two
// stay consistent end-to-end.
const RequestIDHeader = "X-Request-Id"

type requestIDContextKey struct{}

// ContextWithRequestID attaches id to ctx so downstream handlers and
// log call sites can correlate work across goroutines.
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestIDContextKey{}, id)
}

// RequestIDFromContext returns the request ID stored on ctx (empty when
// absent). Nil-context safe.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(requestIDContextKey{}).(string)
	return v
}

// LoggingInterceptor returns a Connect interceptor that logs RPCs:
// procedure, duration, request_id, and (on failure) error.
//
//   - Every FAILED call is written — "rpc failed" / "stream failed" — with
//     the error, its cause, its error_class and its code, at the level its
//     class earns (see error_class.go): ERROR for a server fault, the
//     user-error level (INFO; WithUserErrorLevel) for a user error or a
//     cancellation.
//   - Every SUCCESSFUL call is written — "rpc completed" / "stream
//     completed", at INFO — unless WithSuccessSampling turns success
//     sampling on for this layer. Sampled, a procedure's first success is
//     written, then at most one per window, carrying `suppressed`, the
//     successes of that procedure since the previous record that were not
//     written. A unary success at or above DefaultSlowThreshold is always
//     written, with slow=true. See log_policy.go.
//   - A failure whose error WithExpectedErrors declared an expected outcome
//     is still "rpc failed" with its error — the client did get one — but
//     is written like a success: at the procedure's success level, sampled
//     with it, carrying expected=true.
//
// opts tune the success half — WithSuccessSampling, WithSlowThreshold,
// WithSuccessLevel for one procedure, WithExpectedErrors — and the failure
// levels: WithUserErrorLevel, WithErrorClassifier. Through Chain /
// DefaultMiddlewares they are passed as Deps.LogOptions.
func LoggingInterceptor(logger *slog.Logger, opts ...LogOption) connect.Interceptor {
	if logger == nil {
		logger = slog.Default()
	}
	return &loggingInterceptor{logger: logger, policy: newLogPolicy(slog.LevelInfo, opts)}
}

type loggingInterceptor struct {
	logger *slog.Logger
	policy *logPolicy
}

func (i *loggingInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return connect.UnaryFunc(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		start := i.policy.now()
		resp, err := next(ctx, req)
		i.log(ctx, req.Spec().Procedure, req.Header(), i.policy.now().Sub(start), err,
			"rpc completed", "rpc failed", true)
		return resp, err
	})
}

func (i *loggingInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *loggingInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return connect.StreamingHandlerFunc(func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		start := i.policy.now()
		err := next(ctx, conn)
		i.log(ctx, conn.Spec().Procedure, conn.RequestHeader(), i.policy.now().Sub(start), err,
			"stream completed", "stream failed", false)
		return err
	})
}

// log writes the record for one finished call, if it gets one. A failure
// always does; a success only when the policy admits it, which is decided
// before any attribute is assembled.
func (i *loggingInterceptor) log(ctx context.Context, procedure string, header interface{ Get(string) string },
	elapsed time.Duration, err error, completedMsg, failedMsg string, slowApplies bool,
) {
	class := i.policy.classOf(err)
	level, msg := i.policy.failureLevel(err, class), failedMsg
	expected := i.policy.isExpected(err)
	var why slog.Attr
	if err == nil || expected {
		var ok bool
		if level, why, ok = i.policy.outcome(ctx, i.logger, procedure, elapsed, slowApplies, expected); !ok {
			return
		}
		if err == nil {
			msg = completedMsg
		}
	}
	attrs := []slog.Attr{
		slog.String("procedure", procedure),
		slog.Duration("duration", elapsed),
	}
	if rid := requestIDFromCtxOrHeader(ctx, header); rid != "" {
		attrs = append(attrs, slog.String("request_id", rid))
	}
	if err != nil {
		attrs = append(attrs, errorAttrs(err, class)...)
		if expected {
			attrs = append(attrs, slog.Bool("expected", true))
		}
	}
	if why.Key != "" {
		attrs = append(attrs, why)
	}
	i.logger.LogAttrs(ctx, level, msg, attrs...)
}

// requestIDFromCtxOrHeader resolves the correlation ID by preferring the
// value already stored on ctx (most accurate — the request-id interceptor
// or HTTP middleware put it there) and falling back to the raw header.
// This makes log records correlatable even in partial deployments where
// only one of the two layers is wired.
func requestIDFromCtxOrHeader(ctx context.Context, header interface{ Get(string) string }) string {
	if rid := RequestIDFromContext(ctx); rid != "" {
		return rid
	}
	if header != nil {
		return header.Get(RequestIDHeader)
	}
	return ""
}

// LevelForError is the single place that decides how loud a failed call is,
// under the default policy (see error_class.go).
//
// Every failure used to log at WARN, including the ones that mean the
// SERVER is broken. A total database outage produced a stream of WARN
// records and a 500 for every request, so the standard alert rule —
// level=ERROR — stayed silent through the whole incident. Meanwhile a
// client sending a malformed field also logged WARN, which is why raising
// everything to ERROR is not the fix either: it just moves the noise.
//
// The split is fault attribution — svcerr.Classify — not HTTP status:
//
//   - ERROR: the server failed and someone should be paged. Internal (a
//     bug or a dependency down), Unavailable (a dependency refused),
//     DataLoss, Unknown, and any error nothing classified.
//   - WARN: a server-side failure that says "retry", not "page" —
//     DeadlineExceeded, Aborted, Unimplemented.
//   - DefaultUserErrorLevel (INFO): the request failed for a reason the
//     server correctly detected, or the caller went away. NotFound,
//     InvalidArgument, PermissionDenied, FailedPrecondition,
//     ResourceExhausted, cancellations, and anything marked
//     svcerr.WithClass(err, svcerr.ClassUser). These are the API working.
//
// Exported so the audit interceptor and any project-owned logging site
// classify identically — two log streams disagreeing about severity for
// the same RPC is its own incident. A layer configured WithUserErrorLevel
// uses its own level for the user half.
func LevelForError(err error) slog.Level {
	return defaultPolicy.failureLevel(err, defaultPolicy.classOf(err))
}

// defaultPolicy is the policy LevelForError answers for: no classifiers, the
// default user-error level. Read-only.
var defaultPolicy = newLogPolicy(slog.LevelInfo, nil)

// TracingInterceptor returns a Connect interceptor that creates one
// OpenTelemetry span per RPC. The span name is the full procedure
// ("/service.v1.Foo/Bar"); errors are recorded via span.RecordError +
// span.SetStatus(codes.Error, …).
//
// A nil tracer disables tracing (interceptor is a pass-through). This
// keeps DefaultMiddlewares safe to wire in test harnesses that don't
// configure OTel.
func TracingInterceptor(tracer trace.Tracer) connect.Interceptor {
	if tracer == nil {
		return &noopInterceptor{}
	}
	return &tracingInterceptor{tracer: tracer}
}

type tracingInterceptor struct {
	tracer trace.Tracer
}

func (i *tracingInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return connect.UnaryFunc(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, span := i.tracer.Start(ctx, req.Spec().Procedure)
		defer span.End()
		var resp connect.AnyResponse
		var err error
		withSpanProfileLabels(ctx, func(ctx context.Context) { resp, err = next(ctx, req) })
		RecordSpanError(span, err)
		return resp, err
	})
}

func (i *tracingInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *tracingInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return connect.StreamingHandlerFunc(func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, span := i.tracer.Start(ctx, conn.Spec().Procedure)
		defer span.End()
		var err error
		withSpanProfileLabels(ctx, func(ctx context.Context) { err = next(ctx, conn) })
		RecordSpanError(span, err)
		return err
	})
}

// MetricsInterceptor returns a Connect interceptor that records three
// OpenTelemetry metrics per RPC:
//
//   - rpc.server.calls    (counter, attribute: procedure)
//   - rpc.server.errors   (counter, attribute: procedure)
//   - rpc.server.duration (histogram seconds, attribute: procedure)
//
// Streaming handlers record one duration sample per stream end. A nil
// meter disables metrics (interceptor is a pass-through), matching
// TracingInterceptor's behaviour for tracer == nil.
func MetricsInterceptor(meter metric.Meter) connect.Interceptor {
	if meter == nil {
		return &noopInterceptor{}
	}
	// Unit and description come from the declared built-ins, so the schema
	// (`<bin> metrics schema`) cannot disagree with what is emitted.
	calls, _ := meter.Int64Counter("rpc.server.calls", declaredOpts[metric.Int64CounterOption]("rpc.server.calls")...)
	errs, _ := meter.Int64Counter("rpc.server.errors", declaredOpts[metric.Int64CounterOption]("rpc.server.errors")...)
	dur, _ := meter.Float64Histogram("rpc.server.duration", declaredOpts[metric.Float64HistogramOption]("rpc.server.duration")...)
	return &metricsInterceptor{
		calls:    calls,
		errs:     errs,
		duration: dur,
	}
}

type metricsInterceptor struct {
	calls    metric.Int64Counter
	errs     metric.Int64Counter
	duration metric.Float64Histogram
}

func (i *metricsInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return connect.UnaryFunc(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		start := time.Now()
		attr := metric.WithAttributes(attribute.String("procedure", req.Spec().Procedure))
		if i.calls != nil {
			i.calls.Add(ctx, 1, attr)
		}
		resp, err := next(ctx, req)
		if i.duration != nil {
			i.duration.Record(ctx, time.Since(start).Seconds(), attr)
		}
		if err != nil && i.errs != nil {
			i.errs.Add(ctx, 1, errorMetricAttrs(req.Spec().Procedure, err))
		}
		return resp, err
	})
}

// errorMetricAttrs labels one failed RPC: its procedure, the code the client
// received, and who must act on it. User errors no longer log at ERROR, and
// the counter is what keeps their volume visible — split by kind — without
// paging anyone. Both labels are closed vocabularies, so the series count is
// bounded by procedures × codes.
func errorMetricAttrs(procedure string, err error) metric.MeasurementOption {
	return metric.WithAttributes(
		attribute.String("procedure", procedure),
		attribute.String("code", svcerr.Code(err).String()),
		attribute.String(ErrorClassKey, svcerr.Classify(err).String()),
	)
}

func (i *metricsInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *metricsInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return connect.StreamingHandlerFunc(func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		start := time.Now()
		attr := metric.WithAttributes(attribute.String("procedure", conn.Spec().Procedure))
		if i.calls != nil {
			i.calls.Add(ctx, 1, attr)
		}
		err := next(ctx, conn)
		if i.duration != nil {
			i.duration.Record(ctx, time.Since(start).Seconds(), attr)
		}
		if err != nil && i.errs != nil {
			i.errs.Add(ctx, 1, errorMetricAttrs(conn.Spec().Procedure, err))
		}
		return err
	})
}

// RecoveryInterceptor returns a Connect interceptor that recovers from
// panics inside downstream handlers, logs the recovered value plus the
// stack, and returns connect.CodeInternal so the client never sees a
// torn connection.
//
// Place this FIRST in the chain so it observes panics from every
// subsequent interceptor and the handler itself.
func RecoveryInterceptor(logger *slog.Logger) connect.Interceptor {
	if logger == nil {
		logger = slog.Default()
	}
	return &recoveryInterceptor{logger: logger}
}

type recoveryInterceptor struct {
	logger *slog.Logger
}

func (i *recoveryInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return connect.UnaryFunc(func(ctx context.Context, req connect.AnyRequest) (resp connect.AnyResponse, err error) {
		defer func() {
			if r := recover(); r != nil {
				LogPanic(ctx, i.logger, "panic recovered", r,
					slog.String("procedure", req.Spec().Procedure))
				err = connect.NewError(connect.CodeInternal, panicError(r))
				resp = nil
			}
		}()
		return next(ctx, req)
	})
}

func (i *recoveryInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *recoveryInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return connect.StreamingHandlerFunc(func(ctx context.Context, conn connect.StreamingHandlerConn) (err error) {
		defer func() {
			if r := recover(); r != nil {
				LogPanic(ctx, i.logger, "panic recovered in stream", r,
					slog.String("procedure", conn.Spec().Procedure))
				err = connect.NewError(connect.CodeInternal, panicError(r))
			}
		}()
		return next(ctx, conn)
	})
}

// panicError wraps a recovered value as an error, preserving the
// original error chain (errors.Is / errors.As work) when the panic
// value is itself an error.
//
// The result is REDACTED: a panic value is the most internal thing a
// process has — "panic: assignment to entry in nil map", a nil-pointer
// dereference naming an unexported field, or an error carrying whatever
// the failing library put in it — and connect.Error.Message() is
// verbatim err.Error(), so returning it raw published the crash detail
// to whoever triggered it. The client gets svcerr.InternalMessage; the
// panic value and its stack are already logged by the caller, and
// svcerr.Cause / errors.As still reach the original server-side.
func panicError(r any) error {
	var inner error
	if rerr, ok := r.(error); ok {
		inner = fmt.Errorf("panic: %w", rerr)
	} else {
		inner = fmt.Errorf("panic: %v", r)
	}
	return svcerr.WithCause(errors.New(svcerr.InternalMessage), inner)
}

// RequestIDInterceptor returns a Connect interceptor that ensures every
// inbound request has a correlation ID. It resolves the ID in this
// order, and the order is the whole point:
//
//  1. An ID ALREADY ON THE CONTEXT wins. The HTTP edge
//     (pkg/middleware.RequestIDMiddleware) sits outside this interceptor,
//     and it has already picked the ID, written it to the RESPONSE
//     header, and put it on ctx. It does not write it back onto the
//     INBOUND request header, so an interceptor that only consulted
//     req.Header() found nothing and minted a SECOND id — the response
//     came back with two X-Request-Id values, every standard client read
//     the first, and only the second ever reached the logs. Quoting an
//     ID at support and grepping for it returned nothing.
//  2. Otherwise the inbound RequestIDHeader, so an edge proxy or calling
//     service can stitch one ID across hops.
//  3. Otherwise a fresh 16-byte crypto/rand hex token.
//
// ECHO OWNERSHIP follows from the same rule: whichever layer CHOSE the
// ID echoes it. When the ID came off the context, an outer layer chose
// it and this interceptor writes no response header at all — that is
// what keeps exactly one X-Request-Id on the wire. When this interceptor
// chose the ID (no HTTP middleware in the stack, e.g. a bare Connect
// mux) it echoes, so the client is never left without one.
//
// Place this AFTER RecoveryInterceptor (so panics still get the ID in
// their log line) and BEFORE LoggingInterceptor (so log records inherit
// the ID).
func RequestIDInterceptor() connect.Interceptor {
	return &requestIDInterceptor{}
}

type requestIDInterceptor struct{}

func (i *requestIDInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return connect.UnaryFunc(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		// An outer layer already chose and echoed an ID: adopt it and stay
		// out of the response headers.
		if RequestIDFromContext(ctx) != "" {
			return next(ctx, req)
		}
		id := req.Header().Get(RequestIDHeader)
		if id == "" {
			id = newRequestID()
		}
		ctx = ContextWithRequestID(ctx, id)
		resp, err := next(ctx, req)
		// Guard against typed-nil: connect handlers that return an error
		// typically also return a typed `*Response[T](nil)` boxed in the
		// AnyResponse interface, so `resp != nil` is true while the
		// underlying pointer is nil and Header() panics. Skip the header
		// write whenever next() returned an error — connect drops the
		// response body in that case so the missing request-id echo is
		// observationally invisible to the client.
		if err == nil && resp != nil {
			resp.Header().Set(RequestIDHeader, id)
		}
		return resp, err
	})
}

func (i *requestIDInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *requestIDInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return connect.StreamingHandlerFunc(func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if RequestIDFromContext(ctx) != "" {
			return next(ctx, conn)
		}
		id := conn.RequestHeader().Get(RequestIDHeader)
		if id == "" {
			id = newRequestID()
		}
		ctx = ContextWithRequestID(ctx, id)
		conn.ResponseHeader().Set(RequestIDHeader, id)
		return next(ctx, conn)
	})
}

// newRequestID generates a 16-byte random hex string. Avoids the heavier
// ULID dep in pkg/observe (the scaffolded HTTP middleware uses ULID;
// the interceptor only needs a unique-per-request token).
func newRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failure is exceptional; fall back to a
		// monotonically-distinguishable token rather than panicking.
		return fmt.Sprintf("rid-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

// noopInterceptor is the pass-through used when a tracer or meter is
// nil. Returning a real interceptor (rather than nil) keeps the
// DefaultMiddlewares chain a fixed length, so callers can index into it
// or rely on its position-stable order.
type noopInterceptor struct{}

func (n *noopInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc { return next }
func (n *noopInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}
func (n *noopInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
