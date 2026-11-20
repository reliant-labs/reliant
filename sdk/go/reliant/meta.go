package reliant

import (
	"context"
	"errors"
	"io"
	"net/http"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
)

// newRPCClient is the connect client every service client of the SDK is built
// on: no limit on a received message's size (connect-go v2 caps it at 4 MiB by
// default; the server answers with transcripts and bodies larger than that),
// and errors that keep the metadata the server sent with them (errorMeta).
func newRPCClient(hc *http.Client, base string) *connect.Client {
	return connect.NewClient(connecthttp.NewTransport(hc, base, connecthttp.WithReadMaxBytes(0)), metaInterceptor())
}

// metaInterceptor attaches a failed call's response headers and trailers to
// its error. A connect-go v2 *connect.Error carries no metadata, but the
// server's reason (Reliant-Error-Reason), scopes and compaction watermark
// travel there; errorMeta reads them back. The wrapper unwraps to the
// original error, so errors.As and connect.CodeOf are unaffected.
func metaInterceptor() connect.ClientInterceptor {
	return func(next connect.ClientFunc) connect.ClientFunc {
		return func(ctx context.Context, spec connect.Spec) (connect.ClientStream, error) {
			stream, err := next(ctx, spec)
			if err != nil {
				return nil, attachMeta(ctx, err)
			}
			return &metaStream{ClientStream: stream, ctx: ctx}, nil
		}
	}
}

type metaStream struct {
	connect.ClientStream
	ctx context.Context
}

func (s *metaStream) Send(msg any) error    { return attachMeta(s.ctx, s.ClientStream.Send(msg)) }
func (s *metaStream) CloseSend() error      { return attachMeta(s.ctx, s.ClientStream.CloseSend()) }
func (s *metaStream) Receive(msg any) error { return attachMeta(s.ctx, s.ClientStream.Receive(msg)) }
func (s *metaStream) Close() error          { return attachMeta(s.ctx, s.ClientStream.Close()) }

func attachMeta(ctx context.Context, err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return err
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return err
	}
	var already *metaError
	if errors.As(err, &already) {
		return err
	}
	info, ok := connect.CallInfoForClientContext(ctx)
	if !ok {
		return err
	}
	meta := http.Header{}
	for k, v := range info.ResponseHeader().All() {
		meta[k] = append(meta[k], v...)
	}
	for k, v := range info.ResponseTrailer().All() {
		meta[k] = append(meta[k], v...)
	}
	if len(meta) == 0 {
		return err
	}
	return &metaError{err: err, meta: meta}
}

type metaError struct {
	err  error
	meta http.Header
}

func (e *metaError) Error() string { return e.err.Error() }
func (e *metaError) Unwrap() error { return e.err }

// errorMeta is the metadata the server sent with err: what a connect-go v1
// error's Meta() held. It is empty, never nil, for an error that carries none.
func errorMeta(err error) http.Header {
	var me *metaError
	if errors.As(err, &me) {
		return me.meta
	}
	return http.Header{}
}
