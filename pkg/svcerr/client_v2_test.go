package svcerr_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// refusal is what the servers in these tests fail with: a reason and one
// extra metadata entry, the two things a v1 client read from ce.Meta().
func refusal() error {
	return svcerr.WithMetadata(
		svcerr.WithReason(svcerr.PermissionDenied("not yours"), "not_owner"),
		"X-Forge-Error-Scopes", "deploy:write")
}

// protocols are the client transports a v2 server answers; the metadata
// rides the response headers on unary Connect and the trailers otherwise.
var protocols = map[string][]connecthttp.Option{
	"connect":  nil,
	"grpc":     {connecthttp.WithGRPC()},
	"grpc-web": {connecthttp.WithGRPCWeb()},
}

func serveV2(t *testing.T, handlerErr func() error) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(muxV2(handlerErr, svcerr.InterceptorV2()))
	srv.EnableHTTP2 = true // gRPC needs it
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func clientV2(srv *httptest.Server, opts []connecthttp.Option, interceptors ...connectv2.ClientInterceptor) *connectv2.Client {
	return connectv2.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL, opts...), interceptors...)
}

func assertRefusalMeta(t *testing.T, err error) {
	t.Helper()
	if got := connectv2.CodeOf(err); got != connectv2.CodePermissionDenied {
		t.Fatalf("code = %v, want permission_denied (err %v)", got, err)
	}
	var ce *connectv2.Error
	if !errors.As(err, &ce) || ce.Message() != "not yours" {
		t.Fatalf("errors.As(*connectv2.Error) lost the server's error: %T %v", err, err)
	}
	if svcerr.CodeV2(err) != connectv2.CodePermissionDenied {
		t.Errorf("svcerr.CodeV2 = %v, want permission_denied", svcerr.CodeV2(err))
	}
	meta := svcerr.MetaOf(err)
	if got := meta.Values(svcerr.ReasonHeader); !slices.Equal(got, []string{"not_owner"}) {
		t.Errorf("MetaOf reason = %q, want exactly [not_owner]", got)
	}
	if got := meta.Get("x-forge-error-scopes"); got != "deploy:write" {
		t.Errorf("MetaOf scopes = %q, want deploy:write", got)
	}
}

// TestClientInterceptorV2_Unary is the v1 contract a v2 client lost: the
// reason a server refused for, and its other metadata, read from the error
// alone, on every protocol.
func TestClientInterceptorV2_Unary(t *testing.T) {
	srv := serveV2(t, refusal)
	for name, opts := range protocols {
		t.Run(name, func(t *testing.T) {
			err := clientV2(srv, opts).CallUnary(context.Background(), unarySpecV2, &emptypb.Empty{}, &emptypb.Empty{})
			if got := svcerr.MetaOf(err).Get(svcerr.ReasonHeader); got != "" {
				t.Fatalf("precondition: without the interceptor MetaOf reads reason %q, want none", got)
			}
			err = clientV2(srv, opts, svcerr.ClientInterceptorV2()).
				CallUnary(context.Background(), unarySpecV2, &emptypb.Empty{}, &emptypb.Empty{})
			assertRefusalMeta(t, err)
		})
	}
}

// TestClientInterceptorV2_ServerStream reads the metadata of a stream that
// fails after a message (the end-of-stream trailers) and of one that fails
// before any (a trailers-only response on gRPC).
func TestClientInterceptorV2_ServerStream(t *testing.T) {
	afterMessage := serveV2(t, refusal)

	server := connectv2.NewServer(svcerr.InterceptorV2())
	server.Register(connectv2.Method{Spec: streamSpecV2, Handler: func(context.Context, connectv2.Spec, connectv2.ServerStream) error {
		return refusal()
	}})
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)
	beforeMessage := httptest.NewUnstartedServer(mux)
	beforeMessage.EnableHTTP2 = true
	beforeMessage.StartTLS()
	t.Cleanup(beforeMessage.Close)

	for name, opts := range protocols {
		t.Run(name+"/after a message", func(t *testing.T) {
			stream, err := clientV2(afterMessage, opts, svcerr.ClientInterceptorV2()).
				CallServerStream(context.Background(), streamSpecV2, &emptypb.Empty{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stream.Close() }()
			if err := stream.Receive(&emptypb.Empty{}); err != nil {
				t.Fatalf("first message: %v", err)
			}
			assertRefusalMeta(t, stream.Receive(&emptypb.Empty{}))
		})
		t.Run(name+"/before any message", func(t *testing.T) {
			stream, err := clientV2(beforeMessage, opts, svcerr.ClientInterceptorV2()).
				CallServerStream(context.Background(), streamSpecV2, &emptypb.Empty{})
			if err == nil {
				defer func() { _ = stream.Close() }()
				err = stream.Receive(&emptypb.Empty{})
			}
			assertRefusalMeta(t, err)
		})
	}
}

// TestClientInterceptorV2_TwoCopiesAttachOnce: a second copy in the chain
// (a caller's own beside a builder's) neither re-wraps nor doubles a value.
func TestClientInterceptorV2_TwoCopiesAttachOnce(t *testing.T) {
	srv := serveV2(t, refusal)
	err := clientV2(srv, nil, svcerr.ClientInterceptorV2(), svcerr.ClientInterceptorV2()).
		CallUnary(context.Background(), unarySpecV2, &emptypb.Empty{}, &emptypb.Empty{})
	assertRefusalMeta(t, err)
}

// TestClientInterceptorV2_SuccessAndEOFPassThrough: a successful call and a
// stream's clean end are untouched.
func TestClientInterceptorV2_SuccessAndEOFPassThrough(t *testing.T) {
	srv := serveV2(t, func() error { return nil })
	c := clientV2(srv, nil, svcerr.ClientInterceptorV2())
	if err := c.CallUnary(context.Background(), unarySpecV2, &emptypb.Empty{}, &emptypb.Empty{}); err != nil {
		t.Fatalf("unary: %v", err)
	}
	stream, err := c.CallServerStream(context.Background(), streamSpecV2, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if err := stream.Receive(&emptypb.Empty{}); err != nil {
		t.Fatalf("first message: %v", err)
	}
	if err := stream.Receive(&emptypb.Empty{}); !errors.Is(err, io.EOF) || svcerr.MetaOf(err) != nil {
		t.Fatalf("end of stream = %T %v, want a bare io.EOF", err, err)
	}
}

// TestClientInterceptorV2_ForwardedErrorKeepsItsMetadataHome: a handler that
// returns the error an upstream call gave it sends the upstream's code and
// message but not its metadata, as v1 withheld a forwarded wire error's.
// Attaching the metadata on the client must not change that.
func TestClientInterceptorV2_ForwardedErrorKeepsItsMetadataHome(t *testing.T) {
	upstream := serveV2(t, refusal)
	forward := func() error {
		err := clientV2(upstream, nil, svcerr.ClientInterceptorV2()).
			CallUnary(context.Background(), unarySpecV2, &emptypb.Empty{}, &emptypb.Empty{})
		if svcerr.MetaOf(err).Get(svcerr.ReasonHeader) != "not_owner" {
			t.Errorf("precondition: the forwarding handler reads no upstream reason from %v", err)
		}
		if _, ok := svcerr.ReasonOf(err); ok {
			t.Errorf("ReasonOf(received error) reports a reason: it is the server-side reader")
		}
		return err
	}
	downstream := serveV2(t, forward)
	err := clientV2(downstream, nil, svcerr.ClientInterceptorV2()).
		CallUnary(context.Background(), unarySpecV2, &emptypb.Empty{}, &emptypb.Empty{})
	if code, msg := remoteCode(t, err); code != connectv2.CodePermissionDenied || msg != "not yours" {
		t.Fatalf("forwarded error reads %v %q, want the upstream's permission_denied \"not yours\"", code, msg)
	}
	if got := svcerr.MetaOf(err).Get(svcerr.ReasonHeader); got != "" {
		t.Fatalf("forwarded error carries reason %q, want none (v1 withholds a forwarded error's metadata)", got)
	}
}

// TestMetaOf_LocalAndV1 covers MetaOf on errors that never crossed a v2
// client: it answers what ToConnect would send, and a v1 client's wire
// error answers with its Meta, so one reader serves either side and either
// major.
func TestMetaOf_LocalAndV1(t *testing.T) {
	if svcerr.MetaOf(nil) != nil {
		t.Error("MetaOf(nil) != nil")
	}
	if got := svcerr.MetaOf(errors.New("plain")); got != nil {
		t.Errorf("MetaOf(plain) = %v, want nil", got)
	}
	if got := svcerr.MetaOf(nil).Get(svcerr.ReasonHeader); got != "" {
		t.Errorf("a nil MetaOf reads %q", got)
	}
	for name, err := range map[string]error{
		"svcerr kind":       refusal(),
		"ToConnectV2":       svcerr.ToConnectV2(refusal()),
		"ToConnect (v1)":    svcerr.ToConnect(refusal()),
		"wrapped v2 result": fmt.Errorf("ctx: %w", svcerr.ToConnectV2(refusal())),
	} {
		t.Run(name, func(t *testing.T) {
			meta := svcerr.MetaOf(err)
			if got := meta.Get(svcerr.ReasonHeader); got != "not_owner" {
				t.Errorf("reason = %q, want not_owner", got)
			}
			if got := meta.Get("X-Forge-Error-Scopes"); got != "deploy:write" {
				t.Errorf("scopes = %q, want deploy:write", got)
			}
		})
	}

	t.Run("v1 client", func(t *testing.T) {
		srv := httptest.NewServer(muxV1(refusal))
		t.Cleanup(srv.Close)
		c := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+unaryProcedure)
		_, err := c.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{}))
		if !connect.IsWireError(err.(*connect.Error)) {
			t.Fatalf("precondition: %v is not a wire error", err)
		}
		if got := svcerr.MetaOf(err).Get(svcerr.ReasonHeader); got != "not_owner" {
			t.Errorf("reason = %q, want not_owner", got)
		}
	})

	t.Run("a copy", func(t *testing.T) {
		err := svcerr.ToConnect(refusal())
		svcerr.MetaOf(err).Set(svcerr.ReasonHeader, "changed")
		if got := svcerr.MetaOf(err).Get(svcerr.ReasonHeader); got != "not_owner" {
			t.Errorf("writing MetaOf's result changed the error: %q", got)
		}
	})
}

// TestMetaOf_LocalProjectionMatchesTheWire: for an error that never left the
// process, MetaOf answers exactly what a client of a svcerr.InterceptorV2
// server receives for it, so a handler unit test reads a reason and every
// WithMetadata entry without a network, and the same assertion holds over
// the wire.
func TestMetaOf_LocalProjectionMatchesTheWire(t *testing.T) {
	for name, local := range map[string]error{
		"reason and metadata": refusal(),
		"repeated key, nearest wins": svcerr.WithMetadata(
			svcerr.WithMetadata(svcerr.FailedPrecondition("later"), "X-Retry", "outer-loses"), "X-Retry", "inner"),
		"handler-built v1 error with meta": func() error {
			ce := connect.NewError(connect.CodeAborted, errors.New("conflict"))
			ce.Meta().Set("X-Conflict", "row-7")
			return ce
		}(),
		"no metadata": svcerr.NotFound("plan"),
		"plain":       errors.New("db down"),
	} {
		t.Run(name, func(t *testing.T) {
			srv := serveV2(t, func() error { return local })
			for proto, opts := range protocols {
				received := svcerr.MetaOf(clientV2(srv, opts, svcerr.ClientInterceptorV2()).
					CallUnary(context.Background(), unarySpecV2, &emptypb.Empty{}, &emptypb.Empty{}))
				want := svcerr.MetaOf(local)
				for key, values := range want {
					if got := received.Values(key); !slices.Equal(got, values) {
						t.Errorf("%s: %s received %q, MetaOf(local) %q", proto, key, got, values)
					}
				}
				for _, key := range []string{svcerr.ReasonHeader, "X-Forge-Error-Scopes", "X-Retry", "X-Conflict"} {
					if received.Get(key) != "" && want.Get(key) == "" {
						t.Errorf("%s: %s received %q, absent from MetaOf(local)", proto, key, received.Get(key))
					}
				}
			}
		})
	}
}

// TestWrapReadInProcessV2 pins what section 5 step 7 of the connect-v2
// migration doc tells a v2 project: Wrap's result is a v1 *connect.Error
// until v1 is removed, so connect-go v2's own readers miss it in process,
// and svcerr's v2 readers do not.
func TestWrapReadInProcessV2(t *testing.T) {
	for name, err := range map[string]error{
		"Wrap":   svcerr.Wrap(refusal()),
		"WrapOr": svcerr.WrapOr(errors.New("db down"), refusal()),
	} {
		t.Run(name, func(t *testing.T) {
			if got := svcerr.CodeV2(err); got != connectv2.CodePermissionDenied {
				t.Errorf("CodeV2 = %v, want permission_denied", got)
			}
			if v2 := svcerr.ToConnectV2(err); v2.Code() != connectv2.CodePermissionDenied || v2.Message() != "not yours" {
				t.Errorf("ToConnectV2 = %v %q", v2.Code(), v2.Message())
			}
			if got := svcerr.MetaOf(err).Get(svcerr.ReasonHeader); got != "not_owner" {
				t.Errorf("MetaOf reason = %q, want not_owner", got)
			}
			// The documented gap, until Wrap returns v2 errors (4(j)).
			if got := connectv2.CodeOf(err); got != connectv2.CodeUnknown {
				t.Errorf("connectv2.CodeOf(Wrap result) = %v: Wrap now returns v2 errors, update the migration doc", got)
			}
		})
	}
}
