package observe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/types/known/structpb"
)

// Mirrors TestNewClientStack_Defaults / _TimeoutOverrides /
// _TransportPassthrough.
func TestNewClientStackV2_Defaults(t *testing.T) {
	stack, err := NewClientStackV2(ClientStackDepsV2{})
	if err != nil {
		t.Fatal(err)
	}
	if stack.HTTPClient == nil || stack.HTTPClient.Timeout != defaultClientTimeout || stack.HTTPClient.Transport != nil {
		t.Fatalf("HTTPClient = %+v, want the 30s default over http.DefaultTransport", stack.HTTPClient)
	}
	if len(stack.Interceptors) != 3 {
		t.Errorf("Interceptors = %d, want 3 (svcerr error metadata, otelconnect, request-id)", len(stack.Interceptors))
	}
	if len(stack.HTTPOptions) != 1 {
		t.Errorf("HTTPOptions = %d, want 1 (the unbounded read limit)", len(stack.HTTPOptions))
	}

	stack, err = NewClientStackV2(ClientStackDepsV2{Timeout: 5 * time.Second})
	if err != nil || stack.HTTPClient.Timeout != 5*time.Second {
		t.Fatalf("Timeout = %v (%v), want 5s", stack.HTTPClient.Timeout, err)
	}
	stack, err = NewClientStackV2(ClientStackDepsV2{Timeout: -1})
	if err != nil || stack.HTTPClient.Timeout != 0 {
		t.Fatalf("Timeout = %v (%v), want 0 (disabled)", stack.HTTPClient.Timeout, err)
	}
	rt := http.DefaultTransport.(*http.Transport).Clone()
	stack, err = NewClientStackV2(ClientStackDepsV2{
		Transport: rt,
		Extras:    []connectv2.ClientInterceptor{nil, ClientRequestIDInterceptorV2()},
	})
	if err != nil || stack.HTTPClient.Transport != http.RoundTripper(rt) {
		t.Fatal("Transport not passed through")
	}
	if len(stack.Interceptors) != 4 {
		t.Errorf("Interceptors = %d, want 4: a nil Extra is dropped", len(stack.Interceptors))
	}
}

// Mirrors TestClientRequestIDInterceptor_ForwardsFromContext over a real
// call, unary and streaming: an id on ctx is forwarded, a preset header
// wins, no id forwards nothing.
func TestClientRequestIDInterceptorV2_ForwardsFromContext(t *testing.T) {
	var seen string
	s := serveV2(t, v2Handlers{
		unary: func(ctx context.Context) error {
			seen = callInfoV2(ctx).RequestHeader().Get(RequestIDHeader)
			return nil
		},
		stream: func(ctx context.Context, send func() error) error {
			seen = callInfoV2(ctx).RequestHeader().Get(RequestIDHeader)
			return send()
		},
	})
	stack, err := NewClientStackV2(ClientStackDepsV2{})
	if err != nil {
		t.Fatal(err)
	}
	s.client = stack.NewClient(s.url)

	ctx := ContextWithRequestID(t.Context(), "rid-123")
	if _, err := s.call(ctx, nil); err != nil || seen != "rid-123" {
		t.Fatalf("forwarded = %q (%v), want rid-123", seen, err)
	}
	if _, _, err := s.watch(ctx, nil); err != nil || seen != "rid-123" {
		t.Fatalf("stream forwarded = %q (%v), want rid-123", seen, err)
	}
	if _, err := s.call(ctx, map[string]string{RequestIDHeader: "preset"}); err != nil || seen != "preset" {
		t.Fatalf("request id = %q (%v), want the preset header to win", seen, err)
	}
	if _, err := s.call(t.Context(), nil); err != nil || seen != "" {
		t.Fatalf("request id = %q (%v), want none", seen, err)
	}
}

// connect-go v2 caps a client's response messages at 4 MiB; v1 read any
// size. The stack lifts the cap unless the caller sets one.
func TestNewClientStackV2_ReadLimitMatchesV1(t *testing.T) {
	big := &structpb.Struct{Fields: map[string]*structpb.Value{"blob": structpb.NewStringValue(strings.Repeat("x", 5<<20))}}
	srv := connectv2.NewServer()
	srv.Register(connectv2.Method{Spec: v2UnarySpec, Handler: func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
		if err := stream.Receive(&structpb.Struct{}); err != nil {
			return err
		}
		return stream.Send(big)
	}})
	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	call := func(c *connectv2.Client) error {
		return c.CallUnary(t.Context(), v2UnarySpec, &structpb.Struct{}, &structpb.Struct{})
	}

	if err := call(connectv2.NewClient(connecthttp.NewTransport(hs.Client(), hs.URL))); connectv2.CodeOf(err) != connectv2.CodeResourceExhausted {
		t.Fatalf("a bare v2 client read 5 MiB (err %v): the test no longer exercises the default cap", err)
	}
	stack, err := NewClientStackV2(ClientStackDepsV2{})
	if err != nil {
		t.Fatal(err)
	}
	if err := call(stack.NewClient(hs.URL)); err != nil {
		t.Fatalf("stack client: %v, want the 5 MiB response read as v1 reads it", err)
	}
	stack, err = NewClientStackV2(ClientStackDepsV2{HTTPOptions: []connecthttp.Option{connecthttp.WithReadMaxBytes(1 << 20)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := call(stack.NewClient(hs.URL)); connectv2.CodeOf(err) != connectv2.CodeResourceExhausted {
		t.Fatalf("err = %v, want the caller's 1 MiB limit to apply", err)
	}
}
