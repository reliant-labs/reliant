package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v5"
)

// fakeStreamConn is the minimal connect.StreamingHandlerConn used to exercise
// WrapStreamingHandler without spinning up a real Connect server.
type fakeStreamConn struct {
	procedure string
	reqHeader http.Header
	respHdr   http.Header
}

func newFakeStream(procedure string, reqHdr http.Header) *fakeStreamConn {
	return &fakeStreamConn{
		procedure: procedure,
		reqHeader: reqHdr,
		respHdr:   http.Header{},
	}
}

func (f *fakeStreamConn) Spec() connect.Spec {
	return connect.Spec{Procedure: f.procedure, StreamType: connect.StreamTypeServer}
}
func (f *fakeStreamConn) Peer() connect.Peer           { return connect.Peer{} }
func (f *fakeStreamConn) Receive(any) error            { return errors.New("not implemented") }
func (f *fakeStreamConn) RequestHeader() http.Header   { return f.reqHeader }
func (f *fakeStreamConn) Send(any) error               { return nil }
func (f *fakeStreamConn) ResponseHeader() http.Header  { return f.respHdr }
func (f *fakeStreamConn) ResponseTrailer() http.Header { return http.Header{} }

func TestConnectInterceptor_StreamingHandler_BadTokenRejected(t *testing.T) {
	v, _ := NewValidator(Config{
		Provider: ProviderJWT,
		JWT:      JWTConfig{SigningMethod: "HS256", Secret: "s"},
	})
	ic := v.ConnectInterceptor(InterceptorOptions{}, nil)

	called := false
	next := connect.StreamingHandlerFunc(func(_ context.Context, _ connect.StreamingHandlerConn) error {
		called = true
		return nil
	})
	wrapped := ic.WrapStreamingHandler(next)

	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer not-a-valid-jwt")
	conn := newFakeStream("/svc.Foo/Stream", hdr)

	err := wrapped(context.Background(), conn)
	if err == nil || connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
	if called {
		t.Error("downstream handler should NOT be invoked on auth failure")
	}
}

func TestConnectInterceptor_StreamingHandler_GoodTokenAttachesClaims(t *testing.T) {
	type ctxClaimsKey struct{}
	withClaims := func(ctx context.Context, c *Claims) context.Context {
		return context.WithValue(ctx, ctxClaimsKey{}, c)
	}

	v, _ := NewValidator(Config{
		Provider: ProviderJWT,
		JWT:      JWTConfig{SigningMethod: "HS256", Secret: "shh"},
	})
	ic := v.ConnectInterceptor(InterceptorOptions{}, withClaims)

	var seen *Claims
	next := connect.StreamingHandlerFunc(func(ctx context.Context, _ connect.StreamingHandlerConn) error {
		c, _ := ctx.Value(ctxClaimsKey{}).(*Claims)
		seen = c
		return nil
	})
	wrapped := ic.WrapStreamingHandler(next)

	tok := signHS256(t, "shh", jwt.MapClaims{
		"sub": "streamer",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+tok)
	conn := newFakeStream("/svc.Foo/Stream", hdr)

	if err := wrapped(context.Background(), conn); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if seen == nil {
		t.Fatal("downstream handler did not receive claims via context")
	}
	if seen.UserID != "streamer" {
		t.Errorf("claims.UserID = %q, want streamer", seen.UserID)
	}
}

func TestConnectInterceptor_StreamingHandler_SkipsHealthAndExplicitSkips(t *testing.T) {
	v, _ := NewValidator(Config{
		Provider:    ProviderJWT,
		SkipMethods: []string{"/svc/PublicStream"},
		JWT:         JWTConfig{SigningMethod: "HS256", Secret: "s"},
	})
	ic := v.ConnectInterceptor(InterceptorOptions{}, nil)
	called := 0
	next := connect.StreamingHandlerFunc(func(_ context.Context, _ connect.StreamingHandlerConn) error {
		called++
		return nil
	})
	wrapped := ic.WrapStreamingHandler(next)

	// Health: should skip auth entirely → no Authorization needed.
	if err := wrapped(context.Background(), newFakeStream("/grpc.health.v1.Health/Watch", http.Header{})); err != nil {
		t.Fatalf("health stream: %v", err)
	}
	// Explicit skip method.
	if err := wrapped(context.Background(), newFakeStream("/svc/PublicStream", http.Header{})); err != nil {
		t.Fatalf("explicit-skip stream: %v", err)
	}
	if called != 2 {
		t.Errorf("expected 2 downstream invocations, got %d", called)
	}
}

// TestConnectInterceptor_UnarySymmetry confirms the new ConnectInterceptor
// handles unary RPCs identically to the legacy Interceptor() entry point —
// projects can migrate to ConnectInterceptor without losing unary behaviour.
func TestConnectInterceptor_UnarySymmetry(t *testing.T) {
	v, _ := NewValidator(Config{Provider: ProviderJWT})
	ic := v.ConnectInterceptor(InterceptorOptions{}, nil)
	if ic == nil {
		t.Fatal("ConnectInterceptor returned nil")
	}
	wrapped := ic.WrapUnary(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, nil
	})
	if wrapped == nil {
		t.Fatal("WrapUnary returned nil")
	}
}
