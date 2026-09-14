// Copyright (c) 2025 Reliant Labs
package oauthhelper

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// An abandoned OAuth flow must not hold the port hostage.
//
// POST /oauth/start blocks for as long as the user takes in the browser — that
// is the whole shape of the flow, and `track` deliberately keeps the idle timer
// from firing underneath it. So a user who closes the tab mid-login leaves a
// request blocked on a browser that is never coming back.
//
// http.Server.Shutdown WAITS for in-flight requests, which means a
// graceful-only shutdown waits on exactly that. The user's next "Connect" click
// then fails: the close blocks, the re-open finds the port still bound, and the
// UI reports a broken helper on a machine whose daemon is perfectly healthy.
//
// This is the regression guard for that: Shutdown must return within its
// context budget even with a request in flight, and the port must be free
// immediately afterwards.
func TestShutdownForcesPortReleaseWithRequestInFlight(t *testing.T) {
	t.Parallel()

	// A REAL in-flight HTTP request, not a direct track() call: http.Shutdown
	// waits on connections it is serving, and only an actual request creates
	// one. (track alone bumps a counter the idle timer reads — it is invisible
	// to Shutdown, so a test built on it proves nothing about this bug.)
	blocking := make(chan struct{})
	defer close(blocking)

	srv, err := Start(Options{
		Source:      "test",
		Port:        freePort(t),
		IdleTimeout: time.Hour, // never the thing that saves us here
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Stand in for /oauth/start, which blocks until the browser comes back.
	started := make(chan struct{})
	srv.srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		srv.track(func() {
			close(started)
			<-blocking
		})
		w.WriteHeader(http.StatusOK)
	})

	addr := srv.Addr()
	go func() {
		resp, err := http.Get(fmt.Sprintf("http://%s/oauth/start", addr))
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request never reached the handler")
	}

	// Budget is deliberately short: the point is that Shutdown RETURNS rather
	// than waiting out a request nobody will finish.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- srv.Shutdown(ctx) }()

	select {
	case <-done:
		// An error is expected and fine — graceful shutdown times out, the
		// forced Close follows. What matters is that it returned at all.
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown blocked on an abandoned in-flight request — " +
			"the next Connect click would fail with the port still bound")
	}

	// The port must be genuinely free, not merely reported closed: the next
	// open binds it for real.
	if err := assertPortFree(addr); err != nil {
		t.Fatalf("port still held after Shutdown: %v", err)
	}
}

// The helper must be re-openable immediately after an abandoned flow — the
// user clicking Connect a second time.
func TestReopenAfterAbandonedFlow(t *testing.T) {
	t.Parallel()

	port := freePort(t)
	blocking := make(chan struct{})
	defer close(blocking)

	first, err := Start(Options{Source: "test", Port: port, IdleTimeout: time.Hour})
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}

	started := make(chan struct{})
	first.srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		first.track(func() {
			close(started)
			<-blocking
		})
		w.WriteHeader(http.StatusOK)
	})
	go func() {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/oauth/start", port))
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request never reached the handler")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_ = first.Shutdown(ctx)

	second, err := Start(Options{Source: "test", Port: port, IdleTimeout: time.Hour})
	if err != nil {
		t.Fatalf("re-open after an abandoned flow must succeed, got: %v", err)
	}
	t.Cleanup(func() {
		c, cancel2 := context.WithTimeout(context.Background(), time.Second)
		defer cancel2()
		_ = second.Shutdown(c)
	})

	resp, err := http.Get(fmt.Sprintf("http://%s/health", second.Addr()))
	if err != nil {
		t.Fatalf("health on the re-opened helper: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", resp.StatusCode)
	}
}

// assertPortFree proves the socket is bindable, which is the property the next
// Start actually needs — a closed listener that has not released the socket
// would still fail it.
func assertPortFree(addr string) error {
	var lastErr error
	for range 20 {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			_ = ln.Close()
			return nil
		}
		lastErr = err
		if !strings.Contains(strings.ToLower(err.Error()), "address already in use") {
			return err
		}
		time.Sleep(25 * time.Millisecond)
	}
	return lastErr
}
