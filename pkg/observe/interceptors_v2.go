// File: interceptors_v2.go — the connect-go v2 forms of observe's server
// interceptors.
//
// Each XV2 here is the connect-go v2 (connectrpc.com/connect/v2) form of the
// v1 interceptor X, with the same behaviour: the same log records and fields,
// span names and attributes, metric names and labels, request-id and
// traceresponse headers, deadline budget and recovery. They share X's
// internals, so the two majors cannot drift. In v2 a server interceptor is one
// function for every RPC type; where v1 split unary from streaming, the v2
// form branches on Spec.StreamType: StreamTypeUnary takes v1's WrapUnary path
// and every streaming type its WrapStreamingHandler path. They are server
// interceptors only: v2 has separate client interceptors, and
// ClientRequestIDInterceptorV2 is the one observe ships.
//
// Response headers. A v2 handler has no response value to set headers on:
// they live on the call's connectv2.CallInfo and go out with the first
// message, or when the handler returns if it sent none (an error). So where
// v1 set a unary header on the returned response — present on success only —
// the v2 form sets it just before the handler's first send, which only a
// successful unary call makes.

package observe

import (
	"context"
	"log/slog"
	"time"

	connectv2 "connectrpc.com/connect/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// callInfoV2 is the server-side CallInfo of ctx. A call that did not come
// through connectv2.Server.Call (a ServerFunc driven directly, in a test) has
// none; it gets an empty one, so a header read finds nothing and a header
// write goes nowhere — what a v1 interceptor saw from an empty request.
func callInfoV2(ctx context.Context) *connectv2.CallInfo {
	if info, ok := connectv2.CallInfoForServerContext(ctx); ok && info != nil {
		return info
	}
	return &connectv2.CallInfo{}
}

// errorMetadataV2 is where a failed unary call's metadata goes so that it
// reaches the wire where v1's *connect.Error.Meta did: the Connect protocol
// merges an error's metadata into the response headers, gRPC and gRPC-Web
// write it as trailers (the same rule svcerr.InterceptorV2 delivers error
// metadata by).
func errorMetadataV2(info *connectv2.CallInfo) *connectv2.Header {
	if info.Protocol == connectv2.ProtocolNameConnect {
		return info.ResponseHeader()
	}
	return info.ResponseTrailer()
}

// isUnaryV2 reports whether spec takes v1's unary path.
func isUnaryV2(spec connectv2.Spec) bool { return spec.StreamType == connectv2.StreamTypeUnary }

// passThroughV2 is the identity interceptor: v1's noopInterceptor. A nil
// tracer or meter gets it rather than a nil func, so a v2 chain keeps its
// position-stable length and never hands connectv2.NewServer a nil.
func passThroughV2(next connectv2.ServerFunc) connectv2.ServerFunc { return next }

// beforeFirstSend wraps a unary call's stream so fn runs once, right before
// the handler's response message flushes the response headers. A unary
// handler sends only on success, so a header fn sets is on a successful
// response only — where v1 set it on the returned response. (A unary
// SendHeaders flushes nothing, so it does not fire fn.)
func beforeFirstSend(stream connectv2.ServerStream, fn func()) connectv2.ServerStream {
	return &firstSendStream{ServerStream: stream, fn: fn}
}

type firstSendStream struct {
	connectv2.ServerStream
	fn   func()
	done bool
}

func (s *firstSendStream) Send(msg any) error {
	if !s.done {
		s.done = true
		s.fn()
	}
	return s.ServerStream.Send(msg)
}

// LoggingInterceptorV2 is LoggingInterceptor for a connect-go v2 server: the
// same records ("rpc completed" / "rpc failed" for a unary call, "stream
// completed" / "stream failed" for a stream), fields, levels, sampling and
// options.
func LoggingInterceptorV2(logger *slog.Logger, opts ...LogOption) connectv2.ServerInterceptor {
	if logger == nil {
		logger = slog.Default()
	}
	i := &loggingInterceptor{logger: logger, policy: newLogPolicy(slog.LevelInfo, opts)}
	return func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			start := i.policy.now()
			err := next(ctx, spec, stream)
			header := callInfoV2(ctx).RequestHeader()
			if isUnaryV2(spec) {
				i.log(ctx, spec.Procedure, header, i.policy.now().Sub(start), err, "rpc completed", "rpc failed", true)
			} else {
				i.log(ctx, spec.Procedure, header, i.policy.now().Sub(start), err, "stream completed", "stream failed", false)
			}
			return err
		}
	}
}

// TracingInterceptorV2 is TracingInterceptor for a connect-go v2 server: one
// span per RPC named by its procedure, settled as RequestSpanInterceptor
// settles it. A nil tracer is a pass-through.
func TracingInterceptorV2(tracer trace.Tracer) connectv2.ServerInterceptor {
	return newTracingInterceptorV2(tracer, serverSpanPolicy{})
}

func newTracingInterceptorV2(tracer trace.Tracer, policy serverSpanPolicy) connectv2.ServerInterceptor {
	if tracer == nil {
		return passThroughV2
	}
	return func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			ctx, span := tracer.Start(ctx, spec.Procedure)
			defer span.End()
			var err error
			withSpanProfileLabels(ctx, func(ctx context.Context) { err = next(ctx, spec, stream) })
			policy.settle(span, err)
			return err
		}
	}
}

// MetricsInterceptorV2 is MetricsInterceptor for a connect-go v2 server: the
// same rpc.server.calls / .errors / .duration instruments and labels, and a
// declared health probe (IsHealthProbe) records nothing. A nil meter is a
// pass-through.
func MetricsInterceptorV2(meter metric.Meter) connectv2.ServerInterceptor {
	if meter == nil {
		return passThroughV2
	}
	calls, _ := meter.Int64Counter("rpc.server.calls", declaredOpts[metric.Int64CounterOption]("rpc.server.calls")...)
	errs, _ := meter.Int64Counter("rpc.server.errors", declaredOpts[metric.Int64CounterOption]("rpc.server.errors")...)
	dur, _ := meter.Float64Histogram("rpc.server.duration", declaredOpts[metric.Float64HistogramOption]("rpc.server.duration")...)
	return func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			// v1 checks for a probe on its unary path only; so does this.
			if isUnaryV2(spec) && isHealthProbe(spec.Procedure, callInfoV2(ctx).RequestHeader()) {
				return next(ctx, spec, stream)
			}
			start := time.Now()
			attr := metric.WithAttributes(attribute.String("procedure", spec.Procedure))
			if calls != nil {
				calls.Add(ctx, 1, attr)
			}
			err := next(ctx, spec, stream)
			if dur != nil {
				dur.Record(ctx, time.Since(start).Seconds(), attr)
			}
			if countsAsError(err) && errs != nil {
				errs.Add(ctx, 1, errorMetricAttrs(spec.Procedure, err))
			}
			return err
		}
	}
}

// RecoveryInterceptorV2 is RecoveryInterceptor for a connect-go v2 server: a
// panic below it is logged with its stack ("panic recovered", or "panic
// recovered in stream") and the call fails with CodeInternal and
// svcerr.InternalMessage, the panic value kept as the error's local cause.
func RecoveryInterceptorV2(logger *slog.Logger) connectv2.ServerInterceptor {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) (err error) {
			defer func() {
				if r := recover(); r != nil {
					msg := "panic recovered"
					if !isUnaryV2(spec) {
						msg = "panic recovered in stream"
					}
					LogPanic(ctx, logger, msg, r, slog.String("procedure", spec.Procedure))
					perr := panicError(r)
					err = connectv2.NewError(connectv2.CodeInternal, perr.Error()).WithCause(perr)
				}
			}()
			return next(ctx, spec, stream)
		}
	}
}

// RequestIDInterceptorV2 is RequestIDInterceptor for a connect-go v2 server,
// with the same resolution order (context, then the inbound RequestIDHeader,
// then a fresh id) and echo ownership: an id taken from the context is not
// echoed; one this interceptor chose is, on a successful unary response and
// on every stream.
func RequestIDInterceptorV2() connectv2.ServerInterceptor {
	return func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			if RequestIDFromContext(ctx) != "" {
				return next(ctx, spec, stream)
			}
			info := callInfoV2(ctx)
			id := info.RequestHeader().Get(RequestIDHeader)
			if id == "" {
				id = newRequestID()
			}
			ctx = ContextWithRequestID(ctx, id)
			if isUnaryV2(spec) {
				stream = beforeFirstSend(stream, func() { info.ResponseHeader().Set(RequestIDHeader, id) })
			} else {
				info.ResponseHeader().Set(RequestIDHeader, id)
			}
			return next(ctx, spec, stream)
		}
	}
}

// TraceResponseInterceptorV2 is TraceResponseInterceptor for a connect-go v2
// server: the traceresponse header names the server span on a successful
// unary response, on a failed one, and on every stream's response headers.
//
// A failed unary call carries it where v1 put it. v1 set it on the
// *connect.Error's metadata, which the Connect protocol sends as response
// headers and gRPC and gRPC-Web send as trailers. A v2 error carries no
// metadata of its own, so the value goes on the CallInfo instead: its
// response headers for Connect, its trailers for every other protocol.
func TraceResponseInterceptorV2() connectv2.ServerInterceptor {
	return func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			v := TraceResponseValue(trace.SpanContextFromContext(ctx))
			if v == "" {
				return next(ctx, spec, stream)
			}
			info := callInfoV2(ctx)
			if !isUnaryV2(spec) {
				info.ResponseHeader().Set(TraceResponseHeader, v)
				return next(ctx, spec, stream)
			}
			err := next(ctx, spec, beforeFirstSend(stream, func() { info.ResponseHeader().Set(TraceResponseHeader, v) }))
			if err != nil {
				errorMetadataV2(info).Set(TraceResponseHeader, v)
			}
			return err
		}
	}
}

// RequestSpanInterceptorV2 is RequestSpanInterceptor for a connect-go v2
// server.
func RequestSpanInterceptorV2() connectv2.ServerInterceptor { return RequestSpanInterceptorWithV2() }

// RequestSpanInterceptorWithV2 is RequestSpanInterceptorWith for a connect-go
// v2 server.
func RequestSpanInterceptorWithV2(opts ...ServerSpanOption) connectv2.ServerInterceptor {
	r := requestSpan{policy: newServerSpanPolicy(opts)}
	return func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			ctx = r.enter(ctx, callInfoV2(ctx).RequestHeader())
			err := next(ctx, spec, stream)
			r.settle(ctx, err)
			return err
		}
	}
}

// IdentitySpanInterceptorV2 is IdentitySpanInterceptor for a connect-go v2
// server. A nil caller reads forge's own claims (authn.ClaimsFromContext);
// behind authn.NewInterceptorV2 pass the Policy's ClaimsReader.
func IdentitySpanInterceptorV2(caller CallerLookup) connectv2.ServerInterceptor {
	i := IdentitySpanInterceptor(caller).(identitySpan)
	return func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			i.stamp(ctx)
			return next(ctx, spec, stream)
		}
	}
}

// AuthHeartbeatInterceptorV2 is AuthHeartbeatInterceptor for a connect-go v2
// server: one auth_signed_in {kind: resumed} per user per UTC day, shared with
// every v1 heartbeat in the process.
func AuthHeartbeatInterceptorV2(caller CallerLookup, provider string) connectv2.ServerInterceptor {
	return authHeartbeatV2(AuthHeartbeatInterceptor(caller, provider).(authHeartbeat))
}

// authHeartbeatV2 is h as a v2 server interceptor; a test hands it a
// heartbeat with its own clock and memory.
func authHeartbeatV2(h authHeartbeat) connectv2.ServerInterceptor {
	return func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			h.beat(ctx)
			return next(ctx, spec, stream)
		}
	}
}

// EdgeContextInterceptorV2 is EdgeContextInterceptor for a connect-go v2
// server: behind a trusted edge (EnvTrustedEdge) it reads the edge headers
// into the context; elsewhere it is a pass-through.
func EdgeContextInterceptorV2() connectv2.ServerInterceptor {
	return edgeContextInterceptorV2(edgeTrusted())
}

func edgeContextInterceptorV2(trusted bool) connectv2.ServerInterceptor {
	if !trusted {
		return passThroughV2
	}
	return func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			return next(edgeContextFromHeader(ctx, callInfoV2(ctx).RequestHeader(), true), spec, stream)
		}
	}
}

// profileLabelsInterceptorV2 is profileLabelsInterceptor for a connect-go v2
// server.
func profileLabelsInterceptorV2(next connectv2.ServerFunc) connectv2.ServerFunc {
	return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) (err error) {
		withSpanProfileLabels(ctx, func(ctx context.Context) { err = next(ctx, spec, stream) })
		return err
	}
}

// InterceptorV2 is d as a connect-go v2 server interceptor — the Deadline
// slot of ChainV2: the same budget (unary by default, a stream only when
// Procedures names it), the same slow-call watchdog (unary only) and the same
// marking of whose deadline a DeadlineExceeded ran into. Unlike ChainV2's
// slot it does not default Logger: a nil Logger logs to slog.Default().
func (d Deadline) InterceptorV2() connectv2.ServerInterceptor {
	return func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			unary := isUnaryV2(spec)
			if b, ok := d.budget(spec.Procedure, !unary); ok {
				var cancel context.CancelFunc
				ctx, cancel = d.bound(ctx, b)
				defer cancel()
			}
			if !unary {
				return markDeadline(ctx, next(ctx, spec, stream))
			}
			stop := d.watch(ctx, spec.Procedure)
			defer stop(errHandlerPanicked) // see errHandlerPanicked
			err := next(ctx, spec, stream)
			stop(err)
			return markDeadline(ctx, err)
		}
	}
}
