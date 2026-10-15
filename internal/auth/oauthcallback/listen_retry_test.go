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

// setListenRetryBudget shrinks the bind-retry budget for one test that has to
// wait all of it out. What those tests check — the retry gives up, and gives up
// on time — does not depend on the budget's size. Not for parallel tests: it
// swaps package state.
func setListenRetryBudget(t *testing.T, budget time.Duration) {
	t.Helper()
	prev := listenRetryBudget
	listenRetryBudget = budget
	t.Cleanup(func() { listenRetryBudget = prev })
}

// A port held by something that is NOT going away must still fail — and fail
// within the budget, not hang.
func TestListenWithRetry_FailsOnPermanentHold(t *testing.T) {
	setListenRetryBudget(t, 400*time.Millisecond)
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
	if elapsed < listenRetryBudget {
		t.Errorf("gave up after %v, before the %v budget: a held port must be waited out first", elapsed, listenRetryBudget)
	}
	if elapsed > listenRetryBudget+time.Second {
		t.Errorf("took %v, should give up near %v", elapsed, listenRetryBudget)
	}
	t.Logf("gave up after %v", elapsed)
}
