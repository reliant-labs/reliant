package observe

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
)

func TestNewClientStack_Defaults(t *testing.T) {
	stack, err := NewClientStack(ClientStackDeps{})
	if err != nil {
		t.Fatalf("NewClientStack: %v", err)
	}
	if stack.HTTPClient == nil {
		t.Fatal("HTTPClient is nil")
	}
	if got := stack.HTTPClient.Timeout; got != defaultClientTimeout {
		t.Fatalf("Timeout = %v, want %v", got, defaultClientTimeout)
	}
	if stack.HTTPClient.Transport != nil {
		t.Fatalf("Transport = %v, want nil (http.DefaultTransport fallback)", stack.HTTPClient.Transport)
	}
	if len(stack.ClientOptions) != 1 {
		t.Fatalf("ClientOptions len = %d, want 1 (the interceptor bundle)", len(stack.ClientOptions))
	}
}

func TestNewClientStack_TimeoutOverrides(t *testing.T) {
	// Explicit positive timeout is honored.
	stack, err := NewClientStack(ClientStackDeps{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewClientStack: %v", err)
	}
	if got := stack.HTTPClient.Timeout; got != 5*time.Second {
		t.Fatalf("Timeout = %v, want 5s", got)
	}

	// Negative disables the timeout (streaming RPCs).
	stack, err = NewClientStack(ClientStackDeps{Timeout: -1})
	if err != nil {
		t.Fatalf("NewClientStack: %v", err)
	}
	if got := stack.HTTPClient.Timeout; got != 0 {
		t.Fatalf("Timeout = %v, want 0 (disabled)", got)
	}
}

func TestNewClientStack_TransportPassthrough(t *testing.T) {
	rt := http.DefaultTransport.(*http.Transport).Clone()
	stack, err := NewClientStack(ClientStackDeps{Transport: rt})
	if err != nil {
		t.Fatalf("NewClientStack: %v", err)
	}
	if stack.HTTPClient.Transport != http.RoundTripper(rt) {
		t.Fatal("Transport not passed through")
	}
}

// TestClientRequestIDInterceptor_ForwardsFromContext exercises the unary
// wrap directly: an ID on ctx lands on the outgoing header; an existing
// header wins; an empty ctx forwards nothing.
func TestClientRequestIDInterceptor_ForwardsFromContext(t *testing.T) {
	interceptor := ClientRequestIDInterceptor()
	var seen string
	next := connect.UnaryFunc(func(_ context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		seen = req.Header().Get(RequestIDHeader)
		return nil, nil
	})
	wrapped := interceptor.WrapUnary(next)

	// ID on ctx is forwarded.
	req := connect.NewRequest(&struct{}{})
	if _, err := wrapped(ContextWithRequestID(context.Background(), "rid-123"), req); err != nil {
		t.Fatalf("wrapped: %v", err)
	}
	if seen != "rid-123" {
		t.Fatalf("forwarded request id = %q, want %q", seen, "rid-123")
	}

	// Pre-set header is left untouched.
	req = connect.NewRequest(&struct{}{})
	req.Header().Set(RequestIDHeader, "preset")
	if _, err := wrapped(ContextWithRequestID(context.Background(), "rid-123"), req); err != nil {
		t.Fatalf("wrapped: %v", err)
	}
	if seen != "preset" {
		t.Fatalf("request id = %q, want preset header to win", seen)
	}

	// No ID on ctx forwards nothing.
	req = connect.NewRequest(&struct{}{})
	if _, err := wrapped(context.Background(), req); err != nil {
		t.Fatalf("wrapped: %v", err)
	}
	if seen != "" {
		t.Fatalf("request id = %q, want empty", seen)
	}
}
