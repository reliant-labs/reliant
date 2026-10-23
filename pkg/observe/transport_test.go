package observe

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// roundTripFunc adapts a function to http.RoundTripper so tests can observe
// exactly what reached the wire.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func okResponse(r *http.Request) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     http.Header{},
		Request:    r,
	}
}

func TestRequestIDTransport_ForwardsFromContext(t *testing.T) {
	var seen string
	rt := NewRequestIDTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = r.Header.Get(RequestIDHeader)
		return okResponse(r), nil
	}))

	req := httptest.NewRequest(http.MethodGet, "https://example.com/v1/things", nil)
	req = req.WithContext(ContextWithRequestID(context.Background(), "rid-123"))
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if seen != "rid-123" {
		t.Fatalf("outbound %s = %q, want rid-123", RequestIDHeader, seen)
	}
}

// TestRequestIDTransport_DoesNotMutateRequest — a RoundTripper must leave the
// caller's request alone; the header lands on a clone.
func TestRequestIDTransport_DoesNotMutateRequest(t *testing.T) {
	rt := NewRequestIDTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return okResponse(r), nil
	}))

	req := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
	req = req.WithContext(ContextWithRequestID(context.Background(), "rid-123"))
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if got := req.Header.Get(RequestIDHeader); got != "" {
		t.Fatalf("caller's request was mutated: %s = %q", RequestIDHeader, got)
	}
}

func TestRequestIDTransport_NoIDOnContext(t *testing.T) {
	var seen string
	var present bool
	rt := NewRequestIDTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = r.Header.Get(RequestIDHeader)
		_, present = r.Header[http.CanonicalHeaderKey(RequestIDHeader)]
		return okResponse(r), nil
	}))

	req := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if present || seen != "" {
		t.Fatalf("forwarded %s = %q with no ID on ctx; want the header absent", RequestIDHeader, seen)
	}
}

func TestRequestIDTransport_PresetHeaderWins(t *testing.T) {
	var seen string
	rt := NewRequestIDTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = r.Header.Get(RequestIDHeader)
		return okResponse(r), nil
	}))

	req := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
	req.Header.Set(RequestIDHeader, "preset")
	req = req.WithContext(ContextWithRequestID(context.Background(), "rid-123"))
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if seen != "preset" {
		t.Fatalf("outbound %s = %q, want preset (an explicit header is never overwritten)", RequestIDHeader, seen)
	}
}

func TestRequestIDTransport_NilBaseUsesDefaultTransport(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get(RequestIDHeader)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewRequestIDTransport(nil)}
	req, err := http.NewRequestWithContext(ContextWithRequestID(context.Background(), "rid-nil-base"), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if seen != "rid-nil-base" {
		t.Fatalf("server saw %s = %q, want rid-nil-base", RequestIDHeader, seen)
	}
}

// TestRequestIDTransport_EndToEnd walks the whole hop the scaffold relies on:
// an inbound request gets an ID, a handler makes an outbound call over the
// transport, and the far side sees the same ID.
func TestRequestIDTransport_EndToEnd(t *testing.T) {
	var downstreamSaw string
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downstreamSaw = r.Header.Get(RequestIDHeader)
		w.WriteHeader(http.StatusOK)
	}))
	defer downstream.Close()

	client := &http.Client{Transport: NewRequestIDTransport(http.DefaultTransport)}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Stands in for the inbound RequestIDMiddleware.
		ctx := ContextWithRequestID(r.Context(), "rid-e2e")
		out, err := http.NewRequestWithContext(ctx, http.MethodGet, downstream.URL, nil)
		if err != nil {
			t.Errorf("NewRequestWithContext: %v", err)
			return
		}
		resp, err := client.Do(out)
		if err != nil {
			t.Errorf("client.Do: %v", err)
			return
		}
		_ = resp.Body.Close()
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	resp, err := http.Get(upstream.URL)
	if err != nil {
		t.Fatalf("GET upstream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if downstreamSaw != "rid-e2e" {
		t.Fatalf("downstream saw %s = %q, want rid-e2e", RequestIDHeader, downstreamSaw)
	}
}
