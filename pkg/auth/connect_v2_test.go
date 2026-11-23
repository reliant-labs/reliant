package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	v2EchoProcedure  = "/test.v1.EchoService/Echo"
	v2WatchProcedure = "/test.v1.EchoService/Watch"
)

// serveEchoV2 mounts a v2 server with a unary Echo and a server-streaming
// Watch behind ints, and returns a v2 client for it. Each handler answers
// with what onCall returns for its context.
func serveEchoV2(t *testing.T, onCall func(ctx context.Context) string, ints ...connectv2.ServerInterceptor) *connectv2.Client {
	t.Helper()
	srv := connectv2.NewServer(ints...)
	srv.Register(
		connectv2.Method{
			Spec: connectv2.Spec{StreamType: connectv2.StreamTypeUnary, Procedure: v2EchoProcedure},
			Handler: func(ctx context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
				var req wrapperspb.StringValue
				if err := stream.Receive(&req); err != nil {
					return err
				}
				return stream.Send(wrapperspb.String(onCall(ctx)))
			},
		},
		connectv2.Method{
			Spec: connectv2.Spec{StreamType: connectv2.StreamTypeServer, Procedure: v2WatchProcedure},
			Handler: func(ctx context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
				var req wrapperspb.StringValue
				if err := stream.Receive(&req); err != nil {
					return err
				}
				return stream.Send(wrapperspb.String(onCall(ctx)))
			},
		},
	)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	return connectv2.NewClient(connecthttp.NewTransport(hs.Client(), hs.URL))
}

// callEchoV2 calls Echo (or, with stream, Watch) with headers set.
func callEchoV2(t *testing.T, c *connectv2.Client, stream bool, headers map[string]string) (string, error) {
	t.Helper()
	ctx, info := connectv2.NewClientContext(context.Background())
	for k, v := range headers {
		info.RequestHeader().Set(k, v)
	}
	if !stream {
		var res wrapperspb.StringValue
		err := c.CallUnary(ctx, connectv2.Spec{StreamType: connectv2.StreamTypeUnary, Procedure: v2EchoProcedure}, wrapperspb.String("hi"), &res)
		return res.GetValue(), err
	}
	cs, err := c.CallServerStream(ctx, connectv2.Spec{StreamType: connectv2.StreamTypeServer, Procedure: v2WatchProcedure}, wrapperspb.String("hi"))
	if err != nil {
		return "", err
	}
	defer cs.Close()
	var res wrapperspb.StringValue
	if err := cs.Receive(&res); err != nil {
		return "", err
	}
	if err := cs.Receive(&wrapperspb.StringValue{}); !errors.Is(err, io.EOF) {
		return "", err
	}
	return res.GetValue(), nil
}

// callEchoV1 serves the same unary Echo on connect-go v1 behind ic and calls
// it with headers set.
func callEchoV1(t *testing.T, ic connect.Interceptor, headers map[string]string) error {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(v2EchoProcedure, connect.NewUnaryHandler(v2EchoProcedure,
		func(context.Context, *connect.Request[wrapperspb.StringValue]) (*connect.Response[wrapperspb.StringValue], error) {
			return connect.NewResponse(wrapperspb.String("ok")), nil
		}, connect.WithInterceptors(ic)))
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	req := connect.NewRequest(wrapperspb.String("hi"))
	for k, v := range headers {
		req.Header().Set(k, v)
	}
	_, err := connect.NewClient[wrapperspb.StringValue, wrapperspb.StringValue](hs.Client(), hs.URL+v2EchoProcedure).CallUnary(context.Background(), req)
	return err
}

func jwtValidator(t *testing.T, secret string, skip ...string) *Validator {
	t.Helper()
	v, err := NewValidator(Config{Provider: ProviderJWT, SkipMethods: skip, JWT: JWTConfig{SigningMethod: "HS256", Secret: secret}})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// A denied call reaches the wire with the same code and message from both
// majors' interceptors: v1 sent the message of AuthenticateHeaders' error,
// and a v2 transport would send that foreign error as a bare Unknown.
func TestConnectInterceptorV2_WireParityWithV1(t *testing.T) {
	v := jwtValidator(t, "s")
	v2c := serveEchoV2(t, func(context.Context) string { return "ok" }, v.ConnectInterceptorV2(InterceptorOptions{}, nil))
	for name, headers := range map[string]map[string]string{
		"missing header": nil,
		"not bearer":     {"Authorization": "Basic abc"},
		"bad token":      {"Authorization": "Bearer not-a-valid-jwt"},
	} {
		t.Run(name, func(t *testing.T) {
			errV1 := callEchoV1(t, v.ConnectInterceptor(InterceptorOptions{}, nil), headers)
			e1, ok := errors.AsType[*connect.Error](errV1)
			if !ok {
				t.Fatalf("v1 call: %v, want a connect error", errV1)
			}
			_, errV2 := callEchoV2(t, v2c, false, headers)
			e2, ok := errors.AsType[*connectv2.Error](errV2)
			if !ok {
				t.Fatalf("v2 call: %v, want a connect error", errV2)
			}
			if e2.Code() != connectv2.CodeUnauthenticated || uint32(e2.Code()) != uint32(e1.Code()) {
				t.Errorf("v2 code %v, v1 code %v; want both unauthenticated", e2.Code(), e1.Code())
			}
			if e2.Message() != e1.Message() || e1.Message() == "" {
				t.Errorf("v2 message %q, v1 message %q; want the same non-empty text", e2.Message(), e1.Message())
			}
		})
	}
}

func TestConnectInterceptorV2_GoodTokenAttachesClaims(t *testing.T) {
	type ctxClaimsKey struct{}
	withClaims := func(ctx context.Context, c *Claims) context.Context {
		return context.WithValue(ctx, ctxClaimsKey{}, c)
	}
	v := jwtValidator(t, "shh")
	c := serveEchoV2(t, func(ctx context.Context) string {
		claims, _ := ctx.Value(ctxClaimsKey{}).(*Claims)
		if claims == nil {
			return "<none>"
		}
		return claims.UserID
	}, v.ConnectInterceptorV2(InterceptorOptions{}, withClaims))
	// A fixed expiry far in the future: the token is valid whenever the test
	// runs, with no reading of the clock near a boundary.
	tok := signHS256(t, "shh", jwt.MapClaims{"sub": "streamer", "exp": time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC).Unix()})
	for _, stream := range []bool{false, true} {
		got, err := callEchoV2(t, c, stream, map[string]string{"Authorization": "Bearer " + tok})
		if err != nil {
			t.Fatalf("stream=%v: %v", stream, err)
		}
		if got != "streamer" {
			t.Errorf("stream=%v: handler saw claims for %q, want streamer", stream, got)
		}
	}
}

// Streaming calls are authenticated too: one v2 interceptor covers every
// stream type, so there is no unary-only gap to fall into.
func TestConnectInterceptorV2_StreamingBadTokenRejected(t *testing.T) {
	v := jwtValidator(t, "s")
	var called atomic.Bool
	c := serveEchoV2(t, func(context.Context) string { called.Store(true); return "ok" }, v.ConnectInterceptorV2(InterceptorOptions{}, nil))
	_, err := callEchoV2(t, c, true, map[string]string{"Authorization": "Bearer not-a-valid-jwt"})
	if connectv2.CodeOf(err) != connectv2.CodeUnauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
	if called.Load() {
		t.Error("handler ran for a rejected stream")
	}
}

func TestConnectInterceptorV2_SkipsHealthAndExplicitSkips(t *testing.T) {
	v := jwtValidator(t, "s", v2EchoProcedure)
	c := serveEchoV2(t, func(context.Context) string { return "ok" }, v.ConnectInterceptorV2(InterceptorOptions{}, nil))
	if _, err := callEchoV2(t, c, false, nil); err != nil {
		t.Fatalf("explicitly skipped procedure: %v", err)
	}
	if _, err := callEchoV2(t, c, true, nil); connectv2.CodeOf(err) != connectv2.CodeUnauthenticated {
		t.Fatalf("a procedure not on the skip list must still authenticate, got %v", err)
	}
	// The health service skips by prefix, on the Spec's procedure.
	ic := v.ConnectInterceptorV2(InterceptorOptions{SkipMethods: []string{"/other/Method"}}, nil)
	called := false
	err := ic(func(context.Context, connectv2.Spec, connectv2.ServerStream) error { called = true; return nil })(
		context.Background(), connectv2.Spec{Procedure: "/grpc.health.v1.Health/Watch"}, nil)
	if err != nil || !called {
		t.Fatalf("health procedure: err %v called %v, want it to pass unauthenticated", err, called)
	}
}

func TestErrorToV2(t *testing.T) {
	detail, err := connect.NewErrorDetail(wrapperspb.String("why"))
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("bad signature")
	e1 := connect.NewError(connect.CodePermissionDenied, cause)
	e1.AddDetail(detail)

	e2, ok := errorToV2(e1).(*connectv2.Error)
	if !ok {
		t.Fatalf("errorToV2(v1) = %T, want *connectv2.Error", errorToV2(e1))
	}
	if e2.Code() != connectv2.CodePermissionDenied || e2.Message() != "bad signature" || e2.Error() != e1.Error() {
		t.Errorf("converted %q (%v), want %q", e2.Error(), e2.Code(), e1.Error())
	}
	if ds := e2.Details(); len(ds) != 1 || ds[0].Type != "google.protobuf.StringValue" {
		t.Errorf("details %+v, want the StringValue detail", ds)
	} else {
		var got wrapperspb.StringValue
		if err := (&anypb.Any{TypeUrl: "type.googleapis.com/" + ds[0].Type, Value: ds[0].Value}).UnmarshalTo(&got); err != nil || got.GetValue() != "why" {
			t.Errorf("detail value %q (%v), want why", got.GetValue(), err)
		}
	}
	if !errors.Is(e2, cause) {
		t.Error("the converted error lost its cause")
	}

	native := connectv2.NewError(connectv2.CodeNotFound, "gone")
	if got := errorToV2(native); got != error(native) {
		t.Errorf("a v2 error must pass through unchanged, got %v", got)
	}
	plain := errors.New("boom")
	if got := errorToV2(plain); connectv2.CodeOf(got) != connectv2.CodeUnknown || got.(*connectv2.Error).Message() != "boom" {
		t.Errorf("a plain error must become Unknown with its text (v1 wire behaviour), got %v", got)
	}
	if errorToV2(nil) != nil {
		t.Error("errorToV2(nil) must be nil")
	}
}
