// File: middleware_v2.go — DefaultMiddlewares and Chain for a connect-go v2
// server.
//
// The chains are the v1 chains, layer for layer and in the same order; only
// the interceptor type differs. Two things change shape because a v2
// interceptor is a func, which can be neither compared nor type-switched:
//
//   - the Deadline slot is named by fields (DepsV2.Deadline, DeadlineOverride,
//     NoDeadline) instead of by the dynamic type of one interceptor value;
//   - svcerr.InterceptorV2 is installed innermost by position
//     (svcerr.WithInterceptorV2) and cannot be found in Extras and moved
//     there. It is idempotent, so one passed in Extras as well only runs twice.
//
// A nil func anywhere — a disabled limiter's nil from
// middleware.RateLimitInterceptorV2, a nil Extras entry — is dropped, so the
// result can go straight into connectv2.NewServer, which a nil would panic.

package observe

import (
	"log/slog"

	connectv2 "connectrpc.com/connect/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// DefaultMiddlewareDepsV2 is DefaultMiddlewareDeps for DefaultMiddlewaresV2.
type DefaultMiddlewareDepsV2 struct {
	// Logger feeds RecoveryInterceptorV2 and LoggingInterceptorV2. nil falls
	// back to slog.Default.
	Logger *slog.Logger

	// Tracer feeds TracingInterceptorV2. nil disables tracing.
	Tracer trace.Tracer

	// Meter feeds MetricsInterceptorV2. nil disables metrics.
	Meter metric.Meter

	// LogOptions tune LoggingInterceptorV2, as DefaultMiddlewareDeps.LogOptions.
	LogOptions []LogOption

	// Extras are appended after the observability layer in the order
	// supplied; nil entries are dropped.
	Extras []connectv2.ServerInterceptor
}

// DefaultMiddlewaresV2 is DefaultMiddlewares for a connect-go v2 server:
// Recovery, RequestID, Logging, Tracing, Metrics (a nil tracer or meter is a
// pass-through, so the observability prefix keeps its length), then Extras,
// then svcerr.InterceptorV2 innermost. Pass the result to connectv2.NewServer.
func DefaultMiddlewaresV2(deps DefaultMiddlewareDepsV2) []connectv2.ServerInterceptor {
	chain := []connectv2.ServerInterceptor{
		RecoveryInterceptorV2(deps.Logger),
		RequestIDInterceptorV2(),
		LoggingInterceptorV2(deps.Logger, deps.LogOptions...),
		TracingInterceptorV2(deps.Tracer),
		MetricsInterceptorV2(deps.Meter),
	}
	return append(chain, svcerr.WithInterceptorV2(dropNilV2(deps.Extras))...)
}

// DepsV2 is Deps for ChainV2: the same collaborators, as connect-go v2 server
// interceptors. A nil interceptor field skips its layer, as in Deps.
type DepsV2 struct {
	// Logger feeds RecoveryInterceptorV2, LoggingInterceptorV2 and a Deadline
	// that names no Logger. nil falls back to slog.Default.
	Logger *slog.Logger

	// Tracer feeds TracingInterceptorV2. nil disables tracing.
	Tracer trace.Tracer

	// Meter feeds MetricsInterceptorV2. nil is the global MeterProvider's
	// meter, as Deps.Meter.
	Meter metric.Meter

	// LogOptions tune LoggingInterceptorV2, as Deps.LogOptions.
	LogOptions []LogOption

	// Trace opens the server span, outermost — otelconnect's
	// NewServerInterceptor, or TrustRemoteByV2 / TrustRemoteFromV2. See
	// Deps.Trace. nil opens no span here. Set this OR Tracer, not both.
	Trace connectv2.ServerInterceptor

	// ServerSpan tunes how a failed call reads on its server span, as
	// Deps.ServerSpan.
	ServerSpan []ServerSpanOption

	// Deadline configures the Deadline slot: the RPC's time budget and
	// slow-call watchdog, after observability and before Auth (Deps.Deadline).
	// The zero value is the default a nil Deps.Deadline gets: every unary RPC
	// bounded by DefaultUnaryDeadline, streams unbounded, a unary RPC still
	// running after DefaultSlowAfter logged at WARN. A Deadline naming no
	// Logger gets Logger.
	Deadline Deadline

	// DeadlineOverride, when non-nil, replaces forge's Deadline interceptor
	// and runs in the same position — Deps.Deadline set to an interceptor
	// that is not a Deadline. Deadline is then unused.
	DeadlineOverride connectv2.ServerInterceptor

	// NoDeadline drops the Deadline slot: serverkit.NoDeadline in Deps.
	// It wins over Deadline and DeadlineOverride.
	NoDeadline bool

	// Auth is the authentication interceptor — authn.NewInterceptorV2(policy).
	// nil skips the auth layer.
	Auth connectv2.ServerInterceptor

	// Caller returns the caller Auth authenticated, for the server span's
	// user.id and the sign-in heartbeat. With authn.NewInterceptorV2 pass the
	// Policy's ClaimsReader(). nil reads forge's own claims
	// (authn.ClaimsFromContext). Unused when Auth is nil.
	Caller CallerLookup

	// AuthProvider names the provider in the sign-in heartbeat, as
	// Deps.AuthProvider. Unused when Auth is nil.
	AuthProvider string

	// Audit is the audit interceptor — middleware.AuditInterceptorV2 or
	// audit.InterceptorV2. nil skips audit.
	Audit connectv2.ServerInterceptor

	// RateLimit is the rate-limit interceptor —
	// middleware.RateLimitInterceptorV2, whose nil (rate limiting off) skips
	// the layer.
	RateLimit connectv2.ServerInterceptor

	// Extras are further interceptors, after RateLimit, in the order supplied;
	// nil entries are dropped.
	Extras []connectv2.ServerInterceptor
}

// ChainV2 is Chain for a connect-go v2 server, in Chain's order:
//
//  0. Trace (when non-nil), then the pprof labels naming its span,
//     TraceResponseInterceptorV2 and RequestSpanInterceptorWithV2.
//  1. RecoveryInterceptorV2.
//  2. RequestIDInterceptorV2, then EdgeContextInterceptorV2 behind a trusted
//     edge.
//  3. LoggingInterceptorV2.
//  4. TracingInterceptorV2.
//  5. MetricsInterceptorV2.
//  6. The Deadline slot (Deadline, DeadlineOverride, NoDeadline).
//  7. Auth (when non-nil), then IdentitySpanInterceptorV2 and
//     AuthHeartbeatInterceptorV2.
//  8. Audit (when non-nil).
//  9. RateLimit (when non-nil).
//  10. Extras.
//  11. svcerr.InterceptorV2, always last, next to the handler.
//
// Chain's documentation gives the reason for each position. Pass the result to
// connectv2.NewServer.
func ChainV2(deps DepsV2) []connectv2.ServerInterceptor {
	var chain []connectv2.ServerInterceptor
	if deps.Trace != nil {
		chain = append(chain, deps.Trace, profileLabelsInterceptorV2, TraceResponseInterceptorV2(), RequestSpanInterceptorWithV2(deps.ServerSpan...))
	}
	meter := deps.Meter
	if meter == nil {
		meter = otel.Meter(chainMeterScope)
	}
	chain = append(chain,
		RecoveryInterceptorV2(deps.Logger),
		RequestIDInterceptorV2(),
	)
	if edgeTrusted() {
		chain = append(chain, EdgeContextInterceptorV2())
	}
	chain = append(chain,
		LoggingInterceptorV2(deps.Logger, deps.LogOptions...),
		newTracingInterceptorV2(deps.Tracer, newServerSpanPolicy(deps.ServerSpan)),
		MetricsInterceptorV2(meter),
	)
	if deadline := deadlineSlotV2(deps); deadline != nil {
		chain = append(chain, deadline)
	}
	if deps.Auth != nil {
		chain = append(chain, deps.Auth, IdentitySpanInterceptorV2(deps.Caller), AuthHeartbeatInterceptorV2(deps.Caller, deps.AuthProvider))
	}
	if deps.Audit != nil {
		chain = append(chain, deps.Audit)
	}
	if deps.RateLimit != nil {
		chain = append(chain, deps.RateLimit)
	}
	return append(chain, svcerr.WithInterceptorV2(dropNilV2(deps.Extras))...)
}

// deadlineSlotV2 resolves DepsV2's Deadline slot by deadlineSlot's rule:
// dropped, overridden, or forge's Deadline with the chain's Logger when it
// names none.
func deadlineSlotV2(deps DepsV2) connectv2.ServerInterceptor {
	switch {
	case deps.NoDeadline:
		return nil
	case deps.DeadlineOverride != nil:
		return deps.DeadlineOverride
	}
	d := deps.Deadline
	if d.Logger == nil {
		d.Logger = deps.Logger
	}
	return d.InterceptorV2()
}

// dropNilV2 returns ics without its nil entries, as a fresh slice.
func dropNilV2(ics []connectv2.ServerInterceptor) []connectv2.ServerInterceptor {
	out := make([]connectv2.ServerInterceptor, 0, len(ics))
	for _, ic := range ics {
		if ic != nil {
			out = append(out, ic)
		}
	}
	return out
}
