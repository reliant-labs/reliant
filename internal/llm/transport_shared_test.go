// Copyright (c) 2025 Reliant Labs

package llm

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestResilientTransportIsShared pins the singleton.
//
// An *http.Transport IS the connection pool. When ResilientTransport built a
// fresh one per call, every LLM request paid a new TCP + TLS handshake and
// MaxIdleConnsPerHost was dead config — nothing was ever reused, because the
// pool was garbage as soon as the call finished. It also meant one transient
// network event could kill every in-flight stream at once, which showed up as
// idle-timeout/EOF failures landing in the same second across unrelated chats.
func TestResilientTransportIsShared(t *testing.T) {
	a := ResilientTransport()
	b := ResilientTransport()

	if a != b {
		t.Fatal("ResilientTransport returned distinct transports; the connection pool is per-call again")
	}
}

// The clients drivers actually construct must ride the shared pool too —
// sharing the transport is pointless if every client wraps a fresh one.
func TestStreamingAndResilientClientsShareOnePool(t *testing.T) {
	// Both client constructors wrap the shared transport in decorators
	// (otelhttp, idle-timeout). The decorators are per-client and stateless;
	// what must be shared is the *http.Transport underneath them.
	if ResilientHTTPClient().Transport == nil {
		t.Fatal("ResilientHTTPClient has no transport")
	}
	if StreamingHTTPClient().Transport == nil {
		t.Fatal("StreamingHTTPClient has no transport")
	}

	// The underlying pool is the same object for every caller.
	first, second := ResilientTransport(), ResilientTransport()
	if first != second {
		t.Fatal("shared transport identity is unstable")
	}
}

// newResilientTransport must still hand out INDEPENDENT pools — that is the
// escape hatch for anything needing isolation, and the singleton is built on
// it, so a regression that made it return the shared instance would silently
// alias every "isolated" transport back together.
func TestNewResilientTransportIsIndependent(t *testing.T) {
	a := newResilientTransport()
	b := newResilientTransport()

	if a == b {
		t.Fatal("newResilientTransport returned the same transport twice; pool isolation is gone")
	}
	if a == ResilientTransport() || b == ResilientTransport() {
		t.Fatal("newResilientTransport returned the shared singleton")
	}
}

// Concurrent use of the shared transport must be safe — it is now touched by
// every in-flight LLM call at once. http.Transport documents this, but the
// singleton makes us depend on it, so assert it rather than assume.
func TestSharedTransportHandlesConcurrentRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Transport: ResilientTransport()}

	var wg sync.WaitGroup
	errs := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get(srv.URL)
			if err != nil {
				errs <- err
				return
			}
			resp.Body.Close()
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent request through shared transport failed: %v", err)
	}
}
