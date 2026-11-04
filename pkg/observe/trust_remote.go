// File: trust_remote.go — whose traceparent a server adopts, and how much of it.
//
// An inbound traceparent is a claim made by the caller. otelconnect offers two
// fixed answers to it: WithTrustRemote adopts it for everyone (the server span
// becomes the caller's child), and the default adopts it for no one (the server
// starts a new trace and links the caller's). A server reachable from the
// internet needs the default; a server whose real callers are mostly our own
// services wants the first. With only those two answers the control plane chose
// safety, and every workspace-proxy → admin-server request became two
// unrelated traces — and every console click became a browser trace with the
// server's spans filed under another one.
//
// TrustRemoteBy makes the answer per request, in three levels (TraceTrust):
//
//   - TrustAll: the caller's whole trace context is adopted — trace id, parent
//     span id, sampled flag, tracestate and baggage. For an AUTHENTICATED
//     internal service, whose sampling decision the server should follow.
//   - TrustIDs: the caller's trace id and parent span id are adopted, so the
//     server's spans join the caller's trace, and NOTHING ELSE: the server
//     makes its own sampling decision, and the caller's tracestate and baggage
//     are dropped. For an AUTHENTICATED end user — a signed-in browser.
//   - TrustNone: a new trace linked to the caller's (otelconnect's default).
//     For everyone else.
//
// TrustRemoteFrom is the two-level form (TrustAll or TrustNone) for servers
// whose only adopted callers are internal services.
//
// # Threat model
//
// What adopting a remote parent gives the caller is control over the trace
// identity of the work it causes:
//
//  1. it can graft our spans into a trace it owns (an attacker's id, or a
//     victim's), polluting or attributing someone else's trace;
//  2. it can set the sampled flag, and under a parent-based sampler force or
//     suppress recording — a cheap amplification lever against the trace
//     pipeline, or a way to hide its own requests from it;
//  3. its baggage and tracestate ride along into every downstream hop, where
//     other services read them as facts.
//
// TrustAll hands over all three, so it is for internal services only.
//
// TrustIDs hands over only (1), and only to a caller that proved who it is.
// That is the residual risk it accepts in exchange for a browser's spans and
// the server's spans sharing one trace: a signed-in user can file our spans
// under a trace id of their choosing — including one they copied from someone
// else's trace — and the request is still attributable to THEM (the audit
// record and the request's log lines carry both its trace id and the
// authenticated caller), so a polluted trace is attributable, not anonymous. Reading the trace back
// is a separate permission the trace store enforces; adopting an id grants no
// access to it. (2) is closed: the sampled flag is replaced with the server's
// own head-sampling decision — the one it would make for a trace it started
// itself (see rootSampleRatio), taken on a fresh coin rather than on the
// caller-chosen trace id, so a caller that picks its id cannot pick the
// decision. (3) is closed: tracestate is not adopted and baggage is not
// extracted. Only the W3C traceparent header is read on this path.
//
// The classifier must AUTHENTICATE the caller, not merely recognise it: verify
// a signature (an internal-service token, a session JWT), a constant-time
// shared-secret compare, or an mTLS peer identity. A header that merely says
// "I am internal" or "I am signed in", a source IP, or a User-Agent is
// spoofable and must never be the test. The interceptor runs OUTERMOST —
// before the auth interceptor — so it cannot rely on auth having run; the
// classifier does its own verification, and it sees only headers and the peer,
// never the message.
//
// A caller the classifier does not trust is not rejected: its request proceeds
// exactly as under otelconnect's default, as a new trace linked to the
// caller's. Trust here decides trace parentage, nothing else — authorization
// is still the auth interceptor's job.
//
// The classifier runs on every request, so keep it cheap (an HMAC or one
// signature verify against cached keys, not a network call) and side-effect
// free.

package observe

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// TraceTrust is how much of one inbound request's trace context a server
// adopts. See the file header for what each level hands the caller.
type TraceTrust uint8

const (
	// TrustNone starts a new trace linked to the caller's. The zero value.
	TrustNone TraceTrust = iota
	// TrustIDs continues the caller's trace (trace id and parent span id) but
	// makes the server's own sampling decision and drops the caller's
	// tracestate and baggage. For an authenticated end user.
	TrustIDs
	// TrustAll adopts the caller's whole trace context. For an authenticated
	// internal service.
	TrustAll
)

// TraceTrustFunc classifies the caller of one inbound request. It sees the
// request's peer and headers only; see the file header for what it must
// verify before returning anything but TrustNone.
type TraceTrustFunc func(ctx context.Context, peer connect.Peer, header http.Header) TraceTrust

// TrustPredicate reports whether the caller of one inbound request is an
// authenticated internal service whose trace context may be adopted whole. It
// sees the request's peer and headers only; see the file header for what it
// must verify.
type TrustPredicate func(ctx context.Context, peer connect.Peer, header http.Header) bool

// TrustRemoteFrom returns the server tracing interceptor (otelconnect) that
// continues the caller's whole trace context (TrustAll) when trusted reports
// the caller authenticated, and otherwise starts a new trace linked to the
// caller's. Pass it as observe.Deps.Trace so it runs outermost. opts are
// applied to both underlying interceptors (do not pass
// otelconnect.WithTrustRemote yourself). A nil trusted trusts no one, which is
// otelconnect's default. TrustRemoteBy is the three-level form.
func TrustRemoteFrom(trusted TrustPredicate, opts ...otelconnect.Option) (connect.Interceptor, error) {
	if trusted == nil {
		return TrustRemoteBy(nil, opts...)
	}
	return TrustRemoteBy(func(ctx context.Context, peer connect.Peer, header http.Header) TraceTrust {
		if trusted(ctx, peer, header) {
			return TrustAll
		}
		return TrustNone
	}, opts...)
}

// TrustRemoteBy returns the server tracing interceptor (otelconnect) that
// adopts as much of each caller's trace context as classify grants it: all of
// it (TrustAll), its trace and parent span ids only (TrustIDs), or none — a
// new trace linked to the caller's (TrustNone). Pass it as observe.Deps.Trace
// so it runs outermost. opts are applied to every underlying interceptor (do
// not pass otelconnect.WithTrustRemote yourself). A nil classify trusts no
// one, which is otelconnect's default.
//
// A TrustIDs request is sampled by the server's own head-sampling policy
// (OTEL_TRACES_SAMPLER / OTEL_TRACES_SAMPLER_ARG, as the SDK reads them),
// whatever the caller's sampled flag says.
func TrustRemoteBy(classify TraceTrustFunc, opts ...otelconnect.Option) (connect.Interceptor, error) {
	linked, err := otelconnect.NewInterceptor(opts...)
	if err != nil {
		return nil, fmt.Errorf("otelconnect interceptor: %w", err)
	}
	if classify == nil {
		return linked, nil
	}
	adopted, err := otelconnect.NewInterceptor(append(append([]otelconnect.Option(nil), opts...), otelconnect.WithTrustRemote())...)
	if err != nil {
		return nil, fmt.Errorf("otelconnect interceptor: %w", err)
	}
	ratio := rootSampleRatio()
	return &trustRemoteInterceptor{
		classify: classify,
		adopted:  adopted,
		linked:   linked,
		sample:   func() bool { return ratio >= 1 || (ratio > 0 && rand.Float64() < ratio) }, //nolint:gosec // a sampling coin, not a secret: it only has to be one the caller cannot call.
	}, nil
}

type trustRemoteInterceptor struct {
	classify TraceTrustFunc
	adopted  connect.Interceptor
	linked   connect.Interceptor
	// sample is the server's head-sampling decision for a trace it adopts
	// the ids of but not the sampled flag (TrustIDs).
	sample func() bool
}

// route returns the context a request is traced in, and whether its remote
// parent is adopted (true) or linked (false).
//
// TrustIDs needs no third otelconnect interceptor: otelconnect extracts the
// inbound headers only when the context carries no span context yet, and
// otherwise parents the server span on the one it finds. So the sanitized
// remote parent goes into the context here, and otelconnect never reads the
// caller's tracestate or baggage at all.
func (i *trustRemoteInterceptor) route(ctx context.Context, peer connect.Peer, header http.Header) (context.Context, bool) {
	switch i.classify(ctx, peer, header) {
	case TrustAll:
		return ctx, true
	case TrustIDs:
		if parent := i.idsOnly(header); parent.IsValid() {
			return trace.ContextWithRemoteSpanContext(ctx, parent), true
		}
	case TrustNone:
	}
	return ctx, false
}

// idsOnly is the caller's W3C traceparent reduced to what TrustIDs adopts: its
// trace id and parent span id, with the server's own sampling decision in place
// of the caller's flags and no tracestate. Invalid when the caller sent no
// valid traceparent.
func (i *trustRemoteInterceptor) idsOnly(header http.Header) trace.SpanContext {
	remote := trace.SpanContextFromContext(propagation.TraceContext{}.Extract(context.Background(), propagation.HeaderCarrier(header)))
	if !remote.IsValid() {
		return trace.SpanContext{}
	}
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    remote.TraceID(),
		SpanID:     remote.SpanID(),
		TraceFlags: trace.TraceFlags(0).WithSampled(i.sample()),
		Remote:     true,
	})
}

func (i *trustRemoteInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	adopted, linked := i.adopted.WrapUnary(next), i.linked.WrapUnary(next)
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if req.Spec().IsClient {
			return linked(ctx, req)
		}
		if ctx, adopt := i.route(ctx, req.Peer(), req.Header()); adopt {
			return adopted(ctx, req)
		}
		return linked(ctx, req)
	}
}

// WrapStreamingClient is the outbound side, where there is no remote parent
// to trust: the caller's own span context is injected as usual.
func (i *trustRemoteInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return i.linked.WrapStreamingClient(next)
}

func (i *trustRemoteInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	adopted, linked := i.adopted.WrapStreamingHandler(next), i.linked.WrapStreamingHandler(next)
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if ctx, adopt := i.route(ctx, conn.Peer(), conn.RequestHeader()); adopt {
			return adopted(ctx, conn)
		}
		return linked(ctx, conn)
	}
}
