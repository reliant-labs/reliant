package oauthcallback

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

const codexAuthorizeTemplate = "https://auth.openai.com/oauth/authorize?redirect_uri={redirect_uri}"

// THE REPORTED BUG, as it appears in `reliant auth serve`'s own output:
//
//	ERROR OAuth callback failed error="OAuth callback cancelled: context canceled"
//	ERROR OAuth callback failed error="failed to start callback listener: listen tcp 127.0.0.1:1455: bind: address already in use"
//
// Both lines carry the SAME timestamp. They are one user action: the user
// closed the provider window (cancelling flow A), then clicked "Connect
// Codex" again (flow B). B cannot bind because A's listener has not finished
// releasing port 1455 — Run() tears the listener down in a goroutine and
// returns without waiting for it.
//
// Codex's port is FIXED at 1455 by OpenAI, so "just use another port" is not
// available: a cancelled flow that lingers blocks every subsequent attempt.
func TestCancelledFlowReleasesFixedPortImmediately(t *testing.T) {
	originalOpenBrowser := openBrowser
	openBrowser = func(string) error { return nil }
	defer func() { openBrowser = originalOpenBrowser }()

	// Flow A: start, then cancel while it waits for the callback.
	ctxA, cancelA := context.WithCancel(context.Background())
	errA := make(chan error, 1)
	go func() {
		_, err := Run(ctxA, codexAuthorizeTemplate)
		errA <- err
	}()

	// Let A bind 1455 before cancelling it.
	if !waitForPortBound(t, 2*time.Second) {
		t.Fatal("flow A never bound the fixed port")
	}

	// Hold a KEEP-ALIVE connection open against A, exactly as the user's
	// browser does after following the authorize redirect. This is the part
	// that makes the race real: http.Server.Close() stops new connections but
	// does not drain established ones, so the socket stays bound in the kernel
	// after Run() has already returned.
	keepAlive := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: false},
		Timeout:   time.Second,
	}
	if resp, err := keepAlive.Get("http://127.0.0.1:1455" + probePath); err == nil {
		// Body deliberately NOT closed: that is what keeps the connection
		// alive in the pool, reproducing the browser's behavior.
		defer func() { _ = resp.Body.Close() }()
	}

	cancelA()

	if err := <-errA; err == nil {
		t.Fatal("cancelled flow should return an error")
	}

	// Flow B: the retry. This is the click that currently fails.
	ctxB, cancelB := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelB()

	started := make(chan error, 1)
	go func() {
		_, err := Run(ctxB, codexAuthorizeTemplate)
		started <- err
	}()

	// B should get far enough to bind and wait for a callback — meaning its
	// failure, when it comes, is the context deadline rather than a bind
	// error. A bind failure returns almost immediately.
	select {
	case err := <-started:
		if err != nil && strings.Contains(err.Error(), "address already in use") {
			t.Fatalf("retry hit the port-bind race: %v", err)
		}
		if err != nil && strings.Contains(err.Error(), "failed to start callback listener") {
			t.Fatalf("retry could not start its listener: %v", err)
		}
	case <-time.After(1500 * time.Millisecond):
		// Still waiting for a callback: it bound successfully. That is the
		// behavior we want.
	}
}

// waitForPortBound reports whether something is serving the probe endpoint on
// the fixed Codex port, which is how we know a flow has finished binding.
func waitForPortBound(t *testing.T, budget time.Duration) bool {
	t.Helper()
	client := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://127.0.0.1:1455" + probePath)
		if err == nil {
			_ = resp.Body.Close()
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// The seamless-queue behavior: a second flow started while the first is still
// waiting must QUEUE on the incumbent listener, not fail with a bind error.
// Both flows then receive the same callback — which is correct, because there
// is only one sign-in happening and the user should not be able to tell that
// two requests were in flight.
func TestOverlappingFlowQueuesInsteadOfFailing(t *testing.T) {
	originalOpenBrowser := openBrowser
	openBrowser = func(string) error { return nil }
	defer func() { openBrowser = originalOpenBrowser }()

	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	resA := make(chan *Result, 1)
	go func() {
		r, err := Run(ctxA, codexAuthorizeTemplate)
		if err == nil {
			resA <- r
		}
	}()
	if !waitForPortBound(t, 2*time.Second) {
		t.Fatal("flow A never bound the fixed port")
	}

	// B starts while A still holds 1455. This used to fail instantly with
	// "address already in use"; now it should queue.
	ctxB, cancelB := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelB()
	resB := make(chan *Result, 1)
	errB := make(chan error, 1)
	go func() {
		r, err := Run(ctxB, codexAuthorizeTemplate)
		if err != nil {
			errB <- err
			return
		}
		resB <- r
	}()

	// Give B time to attempt its bind and fall through to the reuse handshake.
	select {
	case err := <-errB:
		t.Fatalf("second flow failed instead of queueing: %v", err)
	case <-time.After(500 * time.Millisecond):
	}

	// The user completes sign-in. One callback, delivered to both waiters.
	deliverCallback(t)

	for _, want := range []struct {
		name string
		ch   chan *Result
	}{{"A", resA}, {"B", resB}} {
		select {
		case got := <-want.ch:
			if got.Code != "queued-code" {
				t.Errorf("flow %s got code %q, want %q", want.name, got.Code, "queued-code")
			}
		case err := <-errB:
			t.Fatalf("flow %s errored: %v", want.name, err)
		case <-time.After(3 * time.Second):
			t.Fatalf("flow %s never received the callback", want.name)
		}
	}
}

// deliverCallback drives the real redirect against whatever is listening.
func deliverCallback(t *testing.T) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(
		"http://127.0.0.1:1455/auth/callback?code=queued-code&state=queued-state")
	if err != nil {
		t.Fatalf("delivering callback: %v", err)
	}
	_ = resp.Body.Close()
}
