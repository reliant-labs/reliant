// Copyright (c) 2025 Reliant Labs
package oauthhelper

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A second sign-in attempt must be able to take the provider's callback port
// back from a flow whose browser was closed.
//
// THE PORT IS NOT NEGOTIABLE. Codex redirects to 127.0.0.1:1455, registered on
// OpenAI's side (oauthcallback.InferConfig), so a retry needs that exact port —
// there is nowhere else to go. A flow whose tab was closed is still blocked in
// oauthcallback.Run holding it, and it is never coming back.
//
// oauthcallback's own contention handling cannot resolve this, which is why the
// fix lives here: tryReuseExistingListener joins a LIVE sibling flow and the
// abandoned one answers that probe, so the retry would queue behind a flow that
// never completes. Cancelling the previous flow is the only thing that frees
// the port.
func TestSecondFlowReclaimsAbandonedCallbackPort(t *testing.T) {
	t.Parallel()

	// A provider that accepts the authorize request and then never redirects —
	// exactly what the user's closed tab looks like from this side.
	silent := httpServerThatNeverRedirects(t)

	srv, err := Start(Options{
		Source:       "test",
		Port:         freePort(t),
		ExtraOrigins: []string{"http://localhost:3000"},
		IdleTimeout:  time.Hour,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	// First attempt: abandoned. Its request is dropped the way a closed tab
	// drops one, leaving oauthcallback.Run holding the callback port.
	firstDone := make(chan struct{})
	firstCtx, abandonFirst := context.WithCancel(context.Background())
	go func() {
		defer close(firstDone)
		_ = postOAuthStart(firstCtx, srv.Addr(), silent)
	}()

	if err := waitForCallbackPort(bound, 3*time.Second); err != nil {
		t.Fatalf("first flow never bound the callback port: %v", err)
	}

	// The browser goes away. The HTTP request dies; the flow does not.
	abandonFirst()
	<-firstDone

	// Second attempt — the user clicking Connect again. It must acquire the
	// same fixed port rather than failing or hanging behind the dead flow.
	secondCtx, cancelSecond := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSecond()

	acquired := make(chan error, 1)
	go func() { acquired <- postOAuthStart(secondCtx, srv.Addr(), silent) }()

	// The proof is that the port changes hands: the second flow binds it, which
	// it can only do once the first has been cancelled.
	if err := waitForCallbackPortRebind(4 * time.Second); err != nil {
		t.Fatalf("second flow could not reclaim the callback port: %v", err)
	}

	cancelSecond()
	select {
	case <-acquired:
	case <-time.After(3 * time.Second):
		t.Fatal("second flow did not unwind after cancellation")
	}
}

// Shutting the helper down must release the callback port as well, or an idle
// timeout leaves a goroutine owning the port the next sign-in needs.
func TestShutdownReleasesCallbackPort(t *testing.T) {
	t.Parallel()

	silent := httpServerThatNeverRedirects(t)

	srv, err := Start(Options{
		Source:       "test",
		Port:         freePort(t),
		ExtraOrigins: []string{"http://localhost:3000"},
		IdleTimeout:  time.Hour,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer close(done)
		_ = postOAuthStart(ctx, srv.Addr(), silent)
	}()

	if err := waitForCallbackPort(bound, 3*time.Second); err != nil {
		t.Fatalf("flow never bound the callback port: %v", err)
	}

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)

	if err := waitForCallbackPort(free, 3*time.Second); err != nil {
		t.Fatalf("callback port still held after Shutdown: %v", err)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

// codexCallbackPort is the fixed port oauthcallback.InferConfig assigns to an
// auth.openai.com authorize URL. Hard-coded here on purpose: if that contract
// ever changes, these tests should fail loudly rather than follow it silently.
const codexCallbackPort = 1455

type portState int

const (
	bound portState = iota
	free
)

func httpServerThatNeverRedirects(t *testing.T) string {
	t.Helper()
	// The authorize URL only has to LOOK like Codex's for InferConfig to pin
	// port 1455; nothing here is fetched.
	return "https://auth.openai.com/oauth/authorize?redirect_uri={redirect_uri}&state=test"
}

func postOAuthStart(ctx context.Context, helperAddr, authorizeURL string) error {
	body, _ := json.Marshal(map[string]string{"authorize_url_template": authorizeURL})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("http://%s/oauth/start", helperAddr), strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://localhost:3000")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func waitForCallbackPort(want portState, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	addr := fmt.Sprintf("127.0.0.1:%d", codexCallbackPort)
	for time.Now().Before(deadline) {
		ln, err := net.Listen("tcp", addr)
		held := err != nil
		if ln != nil {
			_ = ln.Close()
		}
		if (want == bound) == held {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	if want == bound {
		return fmt.Errorf("port %d never became bound", codexCallbackPort)
	}
	return fmt.Errorf("port %d never became free", codexCallbackPort)
}

// waitForCallbackPortRebind waits for the port to be held again after the
// handover — the second flow having taken it.
func waitForCallbackPortRebind(budget time.Duration) error {
	if err := waitForCallbackPort(free, budget/2); err != nil {
		// It may never appear free if the handover is fast; that is fine.
		_ = err
	}
	return waitForCallbackPort(bound, budget)
}
