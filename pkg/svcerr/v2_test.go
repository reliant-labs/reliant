package svcerr_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	connectv2proto "connectrpc.com/connect/v2/connectproto"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

const (
	unaryProcedure  = "/svcerr.v1.Svc/Do"
	streamProcedure = "/svcerr.v1.Svc/Stream"
)

var (
	unarySpecV2  = connectv2.Spec{StreamType: connectv2.StreamTypeUnary, Procedure: unaryProcedure}
	streamSpecV2 = connectv2.Spec{StreamType: connectv2.StreamTypeServer, Procedure: streamProcedure}
)

// muxV2 mounts a unary and a server-streaming procedure on one connect-go v2
// server built with interceptors. Both fail with handlerErr() when it is
// non-nil; the stream sends one message first, so its error rides the
// end-of-stream message rather than a trailers-only response.
func muxV2(handlerErr func() error, interceptors ...connectv2.ServerInterceptor) *http.ServeMux {
	server := connectv2.NewServer(interceptors...)
	server.Register(
		connectv2.Method{Spec: unarySpecV2, Handler: func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
			var req emptypb.Empty
			if err := stream.Receive(&req); err != nil {
				return err
			}
			if err := handlerErr(); err != nil {
				return err
			}
			return stream.Send(&emptypb.Empty{})
		}},
		connectv2.Method{Spec: streamSpecV2, Handler: func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
			var req emptypb.Empty
			if err := stream.Receive(&req); err != nil {
				return err
			}
			if err := stream.Send(&emptypb.Empty{}); err != nil {
				return err
			}
			return handlerErr()
		}},
	)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)
	return mux
}

// muxV1 is muxV2 on connect-go v1, behind svcerr.Interceptor.
func muxV1(handlerErr func() error) *http.ServeMux {
	mux := http.NewServeMux()
	opts := connect.WithInterceptors(svcerr.Interceptor())
	mux.Handle(unaryProcedure, connect.NewUnaryHandler(unaryProcedure,
		func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			if err := handlerErr(); err != nil {
				return nil, err
			}
			return connect.NewResponse(&emptypb.Empty{}), nil
		}, opts))
	mux.Handle(streamProcedure, connect.NewServerStreamHandler(streamProcedure,
		func(_ context.Context, _ *connect.Request[emptypb.Empty], stream *connect.ServerStream[emptypb.Empty]) error {
			if err := stream.Send(&emptypb.Empty{}); err != nil {
				return err
			}
			return handlerErr()
		}, opts))
	return mux
}

// callV2 serves handlerErr behind interceptors and calls the unary
// procedure with a v2 client, returning the error and the call's info.
func callV2(t *testing.T, handlerErr error, interceptors ...connectv2.ServerInterceptor) (error, *connectv2.CallInfo) {
	t.Helper()
	srv := httptest.NewServer(muxV2(func() error { return handlerErr }, interceptors...))
	t.Cleanup(srv.Close)
	client := connectv2.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL))
	ctx, info := connectv2.NewClientContext(context.Background())
	err := client.CallUnary(ctx, unarySpecV2, &emptypb.Empty{}, &emptypb.Empty{})
	return err, info
}

func remoteCode(t *testing.T, err error) (connectv2.Code, string) {
	t.Helper()
	var ce *connectv2.Error
	if !errors.As(err, &ce) {
		t.Fatalf("client got %T (%v), want a *connectv2.Error", err, err)
	}
	return ce.Code(), ce.Message()
}

func TestInterceptorV2_UnwrappedConstructorKeepsItsCode(t *testing.T) {
	// The defect: a v2 server serializes no plain error, so the kind's code
	// AND message are lost without the interceptor.
	err, _ := callV2(t, svcerr.PermissionDenied("nope"))
	if code, msg := remoteCode(t, err); code != connectv2.CodeUnknown || msg != "" {
		t.Fatalf("precondition: without the interceptor the client reads %v %q, want unknown and no message", code, msg)
	}
	err, _ = callV2(t, svcerr.PermissionDenied("nope"), svcerr.InterceptorV2())
	if code, msg := remoteCode(t, err); code != connectv2.CodePermissionDenied || msg != "nope" {
		t.Fatalf("client reads %v %q, want permission_denied \"nope\"", code, msg)
	}
	err, _ = callV2(t, svcerr.NotFound("note"), svcerr.InterceptorV2())
	if code, msg := remoteCode(t, err); code != connectv2.CodeNotFound || msg != "note not found" {
		t.Fatalf("client reads %v %q, want not_found \"note not found\"", code, msg)
	}
}

func TestInterceptorV2_IdempotentAndPassThrough(t *testing.T) {
	if err, _ := callV2(t, nil, svcerr.InterceptorV2()); err != nil {
		t.Fatalf("success path: %v", err)
	}
	err, _ := callV2(t, svcerr.Wrap(svcerr.FailedPrecondition("later")), svcerr.InterceptorV2())
	if code, _ := remoteCode(t, err); code != connectv2.CodeFailedPrecondition {
		t.Fatalf("already-wrapped code = %v", code)
	}
	err, _ = callV2(t, svcerr.ToConnectV2(svcerr.FailedPrecondition("later")), svcerr.InterceptorV2())
	if code, _ := remoteCode(t, err); code != connectv2.CodeFailedPrecondition {
		t.Fatalf("ToConnectV2 result code = %v", code)
	}

	reasoned := svcerr.WithReason(svcerr.FailedPrecondition("later"), "TOO_EARLY")
	for name, ints := range map[string][]connectv2.ServerInterceptor{
		"once":  {svcerr.InterceptorV2()},
		"twice": svcerr.WithInterceptorV2([]connectv2.ServerInterceptor{svcerr.InterceptorV2()}),
	} {
		t.Run(name, func(t *testing.T) {
			err, info := callV2(t, reasoned, ints...)
			if code, _ := remoteCode(t, err); code != connectv2.CodeFailedPrecondition {
				t.Fatalf("code = %v", code)
			}
			if got := info.ResponseHeader().Values(svcerr.ReasonHeader); !slices.Equal(got, []string{"TOO_EARLY"}) {
				t.Fatalf("reason header = %q, want exactly [TOO_EARLY]", got)
			}
		})
	}
}

func TestInterceptorV2_HandlerBuiltConnectErrorIsNotRewritten(t *testing.T) {
	builtV2 := connectv2.NewError(connectv2.CodeFailedPrecondition, "failed to push: ! [rejected] main -> main (non-fast-forward)")
	builtV1 := connect.NewError(connect.CodeFailedPrecondition, errors.New(builtV2.Message()))
	for name, built := range map[string]error{"v2": builtV2, "v1": builtV1} {
		t.Run(name, func(t *testing.T) {
			err, _ := callV2(t, built, svcerr.InterceptorV2())
			if code, msg := remoteCode(t, err); code != connectv2.CodeFailedPrecondition || msg != builtV2.Message() {
				t.Fatalf("client reads %v %q, want the handler's %v %q", code, msg, builtV2.Code(), builtV2.Message())
			}
		})
	}
}

func TestInterceptorV2_RawErrorIsRedactedInternal(t *testing.T) {
	err, _ := callV2(t, errors.New(`ERROR: relation "accounts" does not exist dsn=postgres://app:s3cr3t@db/prod`), svcerr.InterceptorV2())
	if code, msg := remoteCode(t, err); code != connectv2.CodeInternal || msg != svcerr.InternalMessage {
		t.Fatalf("client reads %v %q, want internal %q", code, msg, svcerr.InternalMessage)
	}
}

// TestInterceptorV2_RemoteErrorIsReauthored is the forwarding case: a
// handler returns the error a client call to another Connect service gave
// it. v1 passed that through; v2 scrubs a remote error to internal with no
// message unless svcerr re-creates it locally.
func TestInterceptorV2_RemoteErrorIsReauthored(t *testing.T) {
	detail, err := connectv2proto.NewErrorDetail(wrapperspb.String("sku"))
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(muxV2(func() error {
		return connectv2.NewError(connectv2.CodeNotFound, "upstream says no").WithDetail(detail)
	}))
	t.Cleanup(upstream.Close)
	forward := func() error {
		c := connectv2.NewClient(connecthttp.NewTransport(upstream.Client(), upstream.URL))
		return c.CallUnary(context.Background(), unarySpecV2, &emptypb.Empty{}, &emptypb.Empty{})
	}
	if code, _ := remoteCode(t, forward()); code != connectv2.CodeNotFound {
		t.Fatalf("precondition: upstream code = %v", code)
	}

	call := func(interceptors ...connectv2.ServerInterceptor) error {
		srv := httptest.NewServer(muxV2(forward, interceptors...))
		t.Cleanup(srv.Close)
		c := connectv2.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL))
		return c.CallUnary(context.Background(), unarySpecV2, &emptypb.Empty{}, &emptypb.Empty{})
	}
	if code, msg := remoteCode(t, call()); code != connectv2.CodeInternal || msg != "" {
		t.Fatalf("precondition: v2 without svcerr sends %v %q, want internal and no message", code, msg)
	}
	got := call(svcerr.InterceptorV2())
	code, msg := remoteCode(t, got)
	if code != connectv2.CodeNotFound || msg != "upstream says no" {
		t.Fatalf("client reads %v %q, want not_found \"upstream says no\"", code, msg)
	}
	var ce *connectv2.Error
	errors.As(got, &ce)
	if len(ce.Details()) != 1 {
		t.Fatalf("details = %d, want 1", len(ce.Details()))
	}
	m, err := connectv2proto.UnmarshalErrorDetail(ce.Details()[0])
	if err != nil || !proto.Equal(m, wrapperspb.String("sku")) {
		t.Fatalf("detail = %v (%v), want StringValue sku", m, err)
	}
}

func TestInterceptorV2_StreamingKeepsCodeAndReason(t *testing.T) {
	srv := httptest.NewServer(muxV2(func() error {
		return svcerr.WithReason(svcerr.Unavailable("try later"), "busy")
	}, svcerr.InterceptorV2()))
	t.Cleanup(srv.Close)
	client := connectv2.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL))
	ctx, info := connectv2.NewClientContext(context.Background())
	stream, err := client.CallServerStream(ctx, streamSpecV2, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var msg emptypb.Empty
	if err := stream.Receive(&msg); err != nil {
		t.Fatalf("first message: %v", err)
	}
	err = stream.Receive(&msg)
	if code, text := remoteCode(t, err); code != connectv2.CodeUnavailable || text != "try later" {
		t.Fatalf("stream ends with %v %q, want unavailable \"try later\"", code, text)
	}
	if got := info.ResponseTrailer().Get(svcerr.ReasonHeader); got != "busy" {
		t.Fatalf("reason trailer = %q, want busy", got)
	}
}

func TestWithInterceptorV2_AppendsLastAndKeepsInput(t *testing.T) {
	noop := func(next connectv2.ServerFunc) connectv2.ServerFunc { return next }
	in := make([]connectv2.ServerInterceptor, 2, 8) // spare capacity: an in-place append would show
	in[0], in[1] = noop, noop
	out := svcerr.WithInterceptorV2(in)
	if len(in) != 2 || len(out) != 3 || in[:3][2] != nil {
		t.Fatalf("len(in)=%d len(out)=%d (input's spare slot written: %v), want 2 and 3", len(in), len(out), in[:3][2] != nil)
	}
	// The appended interceptor must be the innermost: it is the one that
	// sees the handler's raw error, so a marker outside it sees a v2 error.
	var seen error
	spy := func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, s connectv2.ServerStream) error {
			seen = next(ctx, spec, s)
			return seen
		}
	}
	if err, _ := callV2(t, svcerr.NotFound("x"), svcerr.WithInterceptorV2([]connectv2.ServerInterceptor{spy})...); err == nil {
		t.Fatal("want an error")
	}
	var ce *connectv2.Error
	if !errors.As(seen, &ce) || ce.Code() != connectv2.CodeNotFound {
		t.Fatalf("outer interceptor saw %T %v, want the converted not_found *connectv2.Error", seen, seen)
	}
}

func TestToConnectV2(t *testing.T) {
	if svcerr.ToConnectV2(nil) != nil {
		t.Fatal("ToConnectV2(nil) != nil")
	}

	local := connectv2.NewError(connectv2.CodeAborted, "conflict")
	if got := svcerr.ToConnectV2(local); got != local {
		t.Fatalf("a local v2 error must pass through unchanged, got %v", got)
	}

	remote := connectv2.NewError(connectv2.CodeNotFound, "gone").WithRemote()
	got := svcerr.ToConnectV2(remote)
	if got.IsRemote() || got.Code() != connectv2.CodeNotFound || got.Message() != "gone" {
		t.Fatalf("remote error re-created as %v remote=%v, want a local not_found \"gone\"", got, got.IsRemote())
	}

	kind := svcerr.WithReason(svcerr.NotFound("user"), "user_missing")
	got = svcerr.ToConnectV2(kind)
	if got.Code() != connectv2.CodeNotFound || got.Message() != "user not found" {
		t.Fatalf("kind converted to %v %q", got.Code(), got.Message())
	}
	if !errors.Is(got, svcerr.ErrNotFound) {
		t.Fatal("errors.Is(ToConnectV2(NotFound), ErrNotFound) = false")
	}
	if r, ok := svcerr.ReasonOf(got); !ok || r != "user_missing" {
		t.Fatalf("ReasonOf = %q %v, want user_missing", r, ok)
	}

	raw := errors.New("dial tcp 10.0.0.7:5432: connection refused")
	got = svcerr.ToConnectV2(raw)
	if got.Code() != connectv2.CodeInternal || got.Message() != svcerr.InternalMessage {
		t.Fatalf("raw error converted to %v %q", got.Code(), got.Message())
	}
	if cause := svcerr.Cause(got); cause != raw {
		t.Fatalf("Cause = %v, want the raw error", cause)
	}
	if _, ok := svcerr.StackOf(got); !ok {
		t.Fatal("StackOf found no stack for a converted server fault")
	}

	// The nearest reason wins, as in v1: an outer reason over an already
	// converted error replaces the reason the conversion carried.
	outer := svcerr.WithReason(svcerr.ToConnectV2(svcerr.WithReason(svcerr.Aborted("x"), "inner")), "outer")
	if r, _ := svcerr.ReasonOf(outer); r != "outer" {
		t.Fatalf("ReasonOf(outer over converted) = %q, want outer", r)
	}
	if got := svcerr.ToConnect(outer).Meta().Get(svcerr.ReasonHeader); got != "outer" {
		t.Fatalf("v1 encoding of outer over converted carries reason %q, want outer", got)
	}
	// ...and with no outer reason, the converted error keeps the one its
	// conversion resolved, not an inner one it overrode.
	resolved := svcerr.ToConnectV2(svcerr.WithReason(svcerr.Wrap(svcerr.WithReason(svcerr.Aborted("x"), "inner")), "outer"))
	if r, _ := svcerr.ReasonOf(resolved); r != "outer" {
		t.Fatalf("ReasonOf(converted) = %q, want outer", r)
	}
}

func TestCodeV2AndNewV2(t *testing.T) {
	if got := svcerr.CodeV2(svcerr.PermissionDenied("x")); got != connectv2.CodePermissionDenied {
		t.Fatalf("CodeV2(kind) = %v", got)
	}
	if got := svcerr.CodeV2(connect.NewError(connect.CodeAborted, nil)); got != connectv2.CodeAborted {
		t.Fatalf("CodeV2(v1 error) = %v", got)
	}
	if got := svcerr.CodeV2(errors.New("raw")); got != connectv2.CodeInternal {
		t.Fatalf("CodeV2(raw) = %v", got)
	}
	err := svcerr.NewV2(connectv2.CodeAlreadyExists, "sku taken")
	if !svcerr.IsAlreadyExists(err) || err.Error() != "sku taken" || svcerr.CodeV2(err) != connectv2.CodeAlreadyExists {
		t.Fatalf("NewV2 = %v (code %v)", err, svcerr.CodeV2(err))
	}
}

// TestV2Errors_ClassifiedLikeV1 pins that every helper that recognises a v1
// *connect.Error recognises a v2 one the same way.
func TestV2Errors_ClassifiedLikeV1(t *testing.T) {
	v2 := connectv2.NewError(connectv2.CodeOutOfRange, "page 9 of 3")
	if got := svcerr.Code(v2); got != connect.CodeOutOfRange {
		t.Fatalf("Code(v2) = %v", got)
	}
	if !svcerr.IsClassified(v2) {
		t.Fatal("IsClassified(v2) = false")
	}
	if got := svcerr.Classify(v2); got != svcerr.ClassUser {
		t.Fatalf("Classify(v2 out_of_range) = %v, want user", got)
	}
	ce := svcerr.ToConnect(v2)
	if ce.Code() != connect.CodeOutOfRange || ce.Message() != "page 9 of 3" || connect.IsWireError(ce) {
		t.Fatalf("ToConnect(v2) = %v wire=%v", ce, connect.IsWireError(ce))
	}
	if !errors.Is(ce, v2) {
		t.Fatal("ToConnect(v2) lost the v2 error from its chain")
	}
	if got := svcerr.WrapOr(v2, svcerr.Internal("fallback")); svcerr.Code(got) != connect.CodeOutOfRange {
		t.Fatalf("WrapOr(v2) code = %v, want the v2 verdict", svcerr.Code(got))
	}
	if !connect.IsWireError(svcerr.ToConnect(v2.WithRemote())) {
		t.Fatal("ToConnect(remote v2) is not a wire error")
	}

	// A v2 error behind a WithCause cause decides nothing the client reads,
	// and errors.As does not find it there.
	hidden := svcerr.WithCause(svcerr.Internal("charge failed"), v2)
	if got := svcerr.Code(hidden); got != connect.CodeInternal {
		t.Fatalf("Code(WithCause(Internal, v2)) = %v, want internal", got)
	}
	var found *connectv2.Error
	if errors.As(hidden, &found) {
		t.Fatal("errors.As found a v2 *connect.Error behind a WithCause cause")
	}
	if got := svcerr.ToConnectV2(hidden); got.Code() != connectv2.CodeInternal || got.Message() != "charge failed" {
		t.Fatalf("ToConnectV2(WithCause(Internal, v2)) = %v %q", got.Code(), got.Message())
	}
}
