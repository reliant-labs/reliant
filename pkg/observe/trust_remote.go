// File: trust_remote.go — whose traceparent a server adopts.
//
// An inbound traceparent is a claim made by the caller. otelconnect offers two
// fixed answers to it: WithTrustRemote adopts it for everyone (the server span
// becomes the caller's child), and the default adopts it for no one (the server
// starts a new trace and links the caller's). A server reachable from the
// internet needs the default; a server whose real callers are mostly our own
// services wants the first. With only those two answers the control plane chose
// safety, and every workspace-proxy → admin-server request became two
// unrelated traces.
//
// TrustRemoteFrom makes the answer per request: the caller's trace is
// continued only when trusted(...) says the caller is an AUTHENTICATED internal
// service, and linked otherwise.
//
// # Threat model
//
// What adopting a remote parent gives the caller is control over the trace
// identity of the work it causes:
//
//   - it can graft our spans into a trace it owns (an attacker's id, or a
//     victim's), polluting or attributing someone else's trace;
//   - it can set the sampled flag, and under a parent-based sampler force or
//     suppress recording — a cheap amplification lever against the trace
//     pipeline, or a way to hide its own requests from it;
//   - its baggage and tracestate ride along into every downstream hop.
//
// None of that is acceptable from an anonymous or end-user caller, which is why
// the predicate must AUTHENTICATE the caller, not merely recognise it: verify a
// signature (an internal-service token), a constant-time shared-secret compare,
// or an mTLS peer identity. A header that merely says "I am internal", a source
// IP, or a User-Agent is spoofable and must never be the test. The interceptor
// runs OUTERMOST — before the auth interceptor — so it cannot rely on auth
// having run; the predicate does its own verification, and it sees only
// headers and the peer, never the message.
//
// A caller that fails the predicate is not rejected: its request proceeds
// exactly as under otelconnect's default, as a new trace linked to the
// caller's. Trust here decides trace parentage, nothing else — authorization
// is still the auth interceptor's job.
//
// The predicate runs on every request, so keep it cheap (an HMAC verify, not a
// network call) and side-effect free.

package observe

import (
	"context"
	"fmt"
	"net/http"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
)

// TrustPredicate reports whether the caller of one inbound request is an
// authenticated internal service whose trace context may be adopted. It sees
// the request's peer and headers only; see the file header for what it must
// verify.
type TrustPredicate func(ctx context.Context, peer connect.Peer, header http.Header) bool

// TrustRemoteFrom returns the server tracing interceptor (otelconnect) that
// continues the caller's trace when trusted reports the caller authenticated,
// and otherwise starts a new trace linked to the caller's. Pass it as
// observe.Deps.Trace so it runs outermost. opts are applied to both underlying
// interceptors (do not pass otelconnect.WithTrustRemote yourself). A nil
// trusted trusts no one, which is otelconnect's default.
func TrustRemoteFrom(trusted TrustPredicate, opts ...otelconnect.Option) (connect.Interceptor, error) {
	linked, err := otelconnect.NewInterceptor(opts...)
	if err != nil {
		return nil, fmt.Errorf("otelconnect interceptor: %w", err)
	}
	if trusted == nil {
		return linked, nil
	}
	adopted, err := otelconnect.NewInterceptor(append(append([]otelconnect.Option(nil), opts...), otelconnect.WithTrustRemote())...)
	if err != nil {
		return nil, fmt.Errorf("otelconnect interceptor: %w", err)
	}
	return &trustRemoteInterceptor{trusted: trusted, adopted: adopted, linked: linked}, nil
}

type trustRemoteInterceptor struct {
	trusted TrustPredicate
	adopted connect.Interceptor
	linked  connect.Interceptor
}

func (i *trustRemoteInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	adopted, linked := i.adopted.WrapUnary(next), i.linked.WrapUnary(next)
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if !req.Spec().IsClient && i.trusted(ctx, req.Peer(), req.Header()) {
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
		if i.trusted(ctx, conn.Peer(), conn.RequestHeader()) {
			return adopted(ctx, conn)
		}
		return linked(ctx, conn)
	}
}
