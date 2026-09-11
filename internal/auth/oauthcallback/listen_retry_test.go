package oauthcallback

import (
	"context"
	"net"
	"testing"
	"time"
)

// A port held briefly by a previous flow must be waited out, not failed on.
// This is the guarantee that makes "cancel, click Connect again" work even
// when the two overlap by milliseconds.
func TestListenWithRetry_WaitsOutBriefHold(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	addr := ln.Addr().String()

	// Release it shortly, as a dying flow would.
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = ln.Close()
	}()

	start := time.Now()
	got, err := listenWithRetry(context.Background(), addr)
	if err != nil {
		t.Fatalf("listenWithRetry should have waited out the hold, got: %v", err)
	}
	defer got.Close()
	t.Logf("bound after %v", time.Since(start))
}

// A port held by something that is NOT going away must still fail — and fail
// within the budget, not hang.
func TestListenWithRetry_FailsOnPermanentHold(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	defer ln.Close()

	start := time.Now()
	if _, err := listenWithRetry(context.Background(), ln.Addr().String()); err == nil {
		t.Fatal("expected failure on a permanently held port")
	}
	elapsed := time.Since(start)
	if elapsed > listenRetryBudget+time.Second {
		t.Errorf("took %v, should give up near %v", elapsed, listenRetryBudget)
	}
	t.Logf("gave up after %v", elapsed)
}
