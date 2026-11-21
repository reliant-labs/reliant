// File: trust_remote_v2.go — TrustRemoteBy / TrustRemoteFrom for a
// connect-go v2 server.
//
// The v1 classifiers take a connect.Peer, which v2 does not have, and an
// http.Header, which v2 replaced with its own *connect.Header. otelconnect
// v0.12 offers no per-request trust hook of its own for either major (only
// the all-or-nothing WithTrustRemote), so the v2 classifiers take what both
// majors can supply with no connect type at all: the peer's address and a
// read-only view of the request headers. The threat model and the three trust
// levels are trust_remote.go's, unchanged.

package observe

import (
	"context"
	"fmt"

	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/otelconnect"
)

// HeaderReader is a read-only view of request headers. Both http.Header and
// connect-go v2's *connect.Header satisfy it.
type HeaderReader interface {
	Get(key string) string
	Values(key string) []string
}

// TraceTrustClassifier classifies the caller of one inbound request from its
// peer address (connect-go v2's CallInfo.PeerAddr, "" when the transport has
// no network peer) and its request headers. It is TraceTrustFunc without a
// connect type, used by TrustRemoteByV2; see trust_remote.go for what it must
// verify before returning anything but TrustNone.
type TraceTrustClassifier func(ctx context.Context, peerAddr string, header HeaderReader) TraceTrust

// TraceTrustPredicate reports whether the caller of one inbound request is an
// authenticated internal service whose trace context may be adopted whole. It
// is TrustPredicate without a connect type, used by TrustRemoteFromV2.
type TraceTrustPredicate func(ctx context.Context, peerAddr string, header HeaderReader) bool

// TrustRemoteFromV2 is TrustRemoteFrom for a connect-go v2 server: it
// continues the caller's whole trace context when trusted reports the caller
// authenticated, and otherwise starts a new trace linked to the caller's. Pass
// it as DepsV2.Trace. A nil trusted trusts no one.
func TrustRemoteFromV2(trusted TraceTrustPredicate, opts ...otelconnect.Option) (connectv2.ServerInterceptor, error) {
	if trusted == nil {
		return TrustRemoteByV2(nil, opts...)
	}
	return TrustRemoteByV2(func(ctx context.Context, peerAddr string, header HeaderReader) TraceTrust {
		if trusted(ctx, peerAddr, header) {
			return TrustAll
		}
		return TrustNone
	}, opts...)
}

// TrustRemoteByV2 is TrustRemoteBy for a connect-go v2 server: it adopts as
// much of each caller's trace context as classify grants it — all of it
// (TrustAll), its trace and parent span ids only (TrustIDs, sampled by the
// server's own head-sampling policy), or none (TrustNone). Pass it as
// DepsV2.Trace so it runs outermost. opts are applied to every underlying
// otelconnect server interceptor (do not pass otelconnect.WithTrustRemote
// yourself). A nil classify trusts no one, which is otelconnect's default.
func TrustRemoteByV2(classify TraceTrustClassifier, opts ...otelconnect.Option) (connectv2.ServerInterceptor, error) {
	linked, err := otelconnect.NewServerInterceptor(opts...)
	if err != nil {
		return nil, fmt.Errorf("otelconnect interceptor: %w", err)
	}
	if classify == nil {
		return linked, nil
	}
	adopted, err := otelconnect.NewServerInterceptor(append(append([]otelconnect.Option(nil), opts...), otelconnect.WithTrustRemote())...)
	if err != nil {
		return nil, fmt.Errorf("otelconnect interceptor: %w", err)
	}
	sample := newHeadSampler()
	return func(next connectv2.ServerFunc) connectv2.ServerFunc {
		adoptedNext, linkedNext := adopted(next), linked(next)
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			info := callInfoV2(ctx)
			header := info.RequestHeader()
			if ctx, adopt := routeRemoteParent(ctx, classify(ctx, info.PeerAddr, header), header, sample); adopt {
				return adoptedNext(ctx, spec, stream)
			}
			return linkedNext(ctx, spec, stream)
		}
	}, nil
}
