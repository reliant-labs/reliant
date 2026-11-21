package svcerr

// client_v2.go — reading error metadata on a connect-go v2 client.
//
// A v1 client read the metadata a server sent with a failed call from the
// error itself: `ce.Meta().Get(svcerr.ReasonHeader)`, where ce is the
// *connect.Error the call returned, and Meta held the response headers and
// trailers. A v2 *connect.Error carries none. The server still sends the
// metadata ([InterceptorV2] writes it to the response headers of a unary
// Connect call and to the trailers otherwise), but it lands in the call's
// *connectv2.CallInfo, which code holding only the error cannot reach.
//
// [ClientInterceptorV2] closes that gap on the client: it attaches a copy of
// a failed call's response headers and trailers to the error the call
// returns, and [MetaOf] reads them back. Every forge-built v2 client
// installs it (observe.ClientStackV2, servicecatalog.NewClientV2,
// testkit.NewConnectClientV2).

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"

	connectv2 "connectrpc.com/connect/v2"
)

// ClientInterceptorV2 is the connect-go v2 client interceptor that gives a
// failed call's error the metadata a v1 client read from
// (*connect.Error).Meta(): it attaches a copy of the call's response headers
// and trailers to every *connectv2.Error the call returns — from a unary
// call, and from every operation on a stream (SendHeaders, Send, CloseSend,
// Receive, Close). [MetaOf] reads it.
//
// The error it returns wraps the original, so errors.As into
// *connectv2.Error, connectv2.CodeOf and svcerr's own readers ([Code],
// [CodeV2], [ToConnect]) see exactly what they saw without it. Code that
// type-asserts err.(*connectv2.Error) instead of using errors.As does not;
// use errors.As. A stream's clean end (io.EOF), a nil error and an error
// that is not a *connectv2.Error pass through untouched, and so does an
// error that already carries the metadata, which makes a second copy in the
// chain harmless.
//
// A handler that forwards such an error is unaffected: [InterceptorV2]
// withholds a forwarded error's metadata, as v1 withholds a wire error's.
//
// Install it on every client whose caller reads an error's metadata. Its
// position in the chain does not matter; forge's client builders put it
// first.
func ClientInterceptorV2() connectv2.ClientInterceptor { return interceptClientV2 }

// interceptClientV2 is a top-level func, not a closure, so every
// ClientInterceptorV2() is the same value.
func interceptClientV2(next connectv2.ClientFunc) connectv2.ClientFunc {
	return func(ctx context.Context, spec connectv2.Spec) (connectv2.ClientStream, error) {
		stream, err := next(ctx, spec)
		if err != nil {
			return nil, attachMeta(ctx, err)
		}
		return &metaClientStream{ClientStream: stream, ctx: ctx}, nil
	}
}

// metaClientStream attaches the call's metadata to every error a stream
// operation returns.
type metaClientStream struct {
	connectv2.ClientStream
	ctx context.Context
}

func (s *metaClientStream) SendHeaders() error {
	return attachMeta(s.ctx, s.ClientStream.SendHeaders())
}

func (s *metaClientStream) Send(msg any) error {
	return attachMeta(s.ctx, s.ClientStream.Send(msg))
}

func (s *metaClientStream) CloseSend() error {
	return attachMeta(s.ctx, s.ClientStream.CloseSend())
}

func (s *metaClientStream) Receive(msg any) error {
	return attachMeta(s.ctx, s.ClientStream.Receive(msg))
}

func (s *metaClientStream) Close() error {
	return attachMeta(s.ctx, s.ClientStream.Close())
}

// attachMeta wraps err with the response headers and trailers of the call
// ctx carries, merged as v1 merged them into Meta (headers, then trailers).
func attachMeta(ctx context.Context, err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return err
	}
	var ce *connectv2.Error
	if !errors.As(err, &ce) {
		return err
	}
	var already *receivedMetaError
	if errors.As(err, &already) {
		return err
	}
	info, ok := connectv2.CallInfoForClientContext(ctx)
	if !ok {
		return err
	}
	meta := http.Header{}
	for key, values := range info.ResponseHeader().All() {
		meta[key] = append(meta[key], values...)
	}
	if !trailersOnly(info) {
		for key, values := range info.ResponseTrailer().All() {
			meta[key] = append(meta[key], values...)
		}
	}
	if len(meta) == 0 {
		return err
	}
	return &receivedMetaError{err: err, meta: meta}
}

// trailersOnly reports a gRPC trailers-only response, whose metadata the
// transport keeps in the headers and copies into the trailers. v1 read
// such an error's Meta from the headers alone; merging both would send
// every value twice.
func trailersOnly(info *connectv2.CallInfo) bool {
	trailer := info.ResponseTrailer()
	if trailer.Len() == 0 {
		return false
	}
	for key, values := range trailer.All() {
		if !slices.Equal(info.ResponseHeader().Values(key), values) {
			return false
		}
	}
	return true
}

// receivedMetaError is a client call's error plus the metadata the server
// sent with it.
type receivedMetaError struct {
	err  error
	meta http.Header
}

func (e *receivedMetaError) Error() string { return e.err.Error() }
func (e *receivedMetaError) Unwrap() error { return e.err }

// MetaOf returns the Connect error metadata of err as a client reads it —
// what v1's (*connect.Error).Meta() held — or nil when there is none:
//
//   - For an error a v2 client call returned through [ClientInterceptorV2],
//     the response headers and trailers the server sent with it.
//   - For an error a v1 client call returned (a wire error), its Meta.
//   - For any other error — one that never left the process, such as an
//     in-process handler's svcerr error, or a *connect.Error of either major
//     built locally — the metadata [ToConnect] would send for it: the
//     [ReasonHeader] reason and every [WithMetadata] entry.
//
// So one reader answers on either side of the wire, for either major:
//
//	switch svcerr.MetaOf(err).Get(svcerr.ReasonHeader) {
//	case "person_required":
//	    ...
//	}
//
// A nil result is safe to call Get on. The header is a copy: writing to it
// changes nothing.
//
// [ReasonOf] is the server-side reader: it reports the reason err's own
// annotations put on the wire and never one a client received, so it
// agrees with what InterceptorV2 sends for a forwarded error (nothing).
// Read a received reason with MetaOf.
func MetaOf(err error) http.Header {
	if err == nil {
		return nil
	}
	var received *receivedMetaError
	if errors.As(err, &received) {
		return received.meta.Clone()
	}
	meta := ToConnect(err).Meta()
	if len(meta) == 0 {
		return nil
	}
	return meta.Clone()
}
