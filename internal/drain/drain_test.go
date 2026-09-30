// Copyright (c) 2025 Reliant Labs
package drain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// The defect this pins: on SIGTERM the mains shut the health server down
// FIRST, so /ready went to connection-refused while the serving listener was
// closed in the same breath. The pod stayed in EndpointSlices for seconds with
// nothing listening.
//
// The fix is that /ready must answer 503 "draining" — a real HTTP response, on
// a health server that is still up — for the whole pre-stop window, while
// in-flight requests on the serving listener continue to complete.
func TestReadyReports503DrainingWhileInFlightRequestCompletes(t *testing.T) {
	d := NewWithBudget(150*time.Millisecond, time.Second)

	// The health server: still up throughout the drain.
	health := httptest.NewServer(d.ReadinessHandler("test-service"))
	defer health.Close()

	// The serving listener, with one request parked in flight.
	inFlight := make(chan struct{})
	releaseInFlight := make(chan struct{})
	serving := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(inFlight)
		<-releaseInFlight
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("served"))
	}))
	defer serving.Close()

	servedStatus := make(chan int, 1)
	go func() {
		resp, err := http.Get(serving.URL)
		if err != nil {
			servedStatus <- -1
			return
		}
		defer resp.Body.Close()
		servedStatus <- resp.StatusCode
	}()
	<-inFlight

	// Before the drain starts, readiness passes.
	if status, body := getReady(t, health.URL); status != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("before drain: got status=%d body=%v, want 200 ready", status, body)
	}

	// Begin blocks for PreStopDelay. Probe during that window.
	drainReturned := make(chan struct{})
	go func() {
		d.Begin()
		close(drainReturned)
	}()

	// Give Begin a moment to set the flag, then probe mid-pause.
	deadline := time.After(2 * time.Second)
	for !d.Draining() {
		select {
		case <-deadline:
			t.Fatal("draining flag never flipped")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	select {
	case <-drainReturned:
		t.Fatal("Begin returned before the pre-stop delay elapsed — there is no window for the LB to observe the failing probe")
	default:
	}

	status, body := getReady(t, health.URL)
	if status != http.StatusServiceUnavailable {
		t.Errorf("/ready during drain: got status=%d, want 503", status)
	}
	if body["status"] != "draining" {
		t.Errorf(`/ready during drain: got status=%q, want "draining"`, body["status"])
	}

	// The in-flight request must still complete normally: the serving
	// listener is deliberately NOT closed during the pre-stop window.
	close(releaseInFlight)
	select {
	case got := <-servedStatus:
		if got != http.StatusOK {
			t.Errorf("in-flight request got %d, want 200 — the drain cut a request it was supposed to let finish", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request never completed")
	}

	select {
	case <-drainReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("Begin never returned")
	}
}

// Draining short-circuits BEFORE dependency checks. A probe during shutdown
// must not pay for a DB round-trip whose answer cannot change the outcome —
// and must not flip back to ready if the dependency happens to be healthy.
func TestDrainingShortCircuitsDependencyChecks(t *testing.T) {
	d := NewWithBudget(0, time.Second)

	var checked bool
	var mu sync.Mutex
	handler := d.ReadinessHandler("test-service", ReadinessCheck{
		Name: "db",
		Check: func(context.Context) error {
			mu.Lock()
			checked = true
			mu.Unlock()
			return nil
		},
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	if status, _ := getReady(t, srv.URL); status != http.StatusOK {
		t.Fatalf("pre-drain: got %d, want 200", status)
	}
	mu.Lock()
	if !checked {
		t.Error("dependency check should run when not draining")
	}
	checked = false
	mu.Unlock()

	d.Begin()

	status, body := getReady(t, srv.URL)
	if status != http.StatusServiceUnavailable || body["status"] != "draining" {
		t.Errorf("draining: got status=%d body=%v, want 503 draining", status, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if checked {
		t.Error("dependency checks ran while draining — the answer was already decided")
	}
}

// A failing dependency is still reported as not_ready (not draining), so the
// existing DB/NATS readiness semantics survive the change.
func TestFailingDependencyStillReportsNotReady(t *testing.T) {
	d := NewWithBudget(0, time.Second)
	srv := httptest.NewServer(d.ReadinessHandler("test-service", ReadinessCheck{
		Name:  "db",
		Check: func(context.Context) error { return errors.New("connection refused") },
	}))
	defer srv.Close()

	status, body := getReady(t, srv.URL)
	if status != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", status)
	}
	if body["status"] != "not_ready" {
		t.Errorf(`got status=%q, want "not_ready"`, body["status"])
	}
}

// The budget comes from the env vars control-plane already sets and forge
// already derives terminationGracePeriodSeconds from. Reading them is the
// whole point: a hardcoded 20s cannot track a grace period it does not know.
func TestBudgetReadFromEnvironment(t *testing.T) {
	t.Setenv(EnvPreStopDelay, "7s")
	t.Setenv(EnvShutdownTimeout, "45s")

	d := New()
	if got := d.PreStopDelay(); got != 7*time.Second {
		t.Errorf("PreStopDelay = %s, want 7s", got)
	}
	if got := d.ShutdownTimeout(); got != 45*time.Second {
		t.Errorf("ShutdownTimeout = %s, want 45s", got)
	}
}

func TestBudgetDefaultsMatchServerkit(t *testing.T) {
	t.Setenv(EnvPreStopDelay, "")
	t.Setenv(EnvShutdownTimeout, "")

	d := New()
	if got := d.PreStopDelay(); got != DefaultPreStopDelay {
		t.Errorf("PreStopDelay = %s, want %s", got, DefaultPreStopDelay)
	}
	if got := d.ShutdownTimeout(); got != DefaultShutdownTimeout {
		t.Errorf("ShutdownTimeout = %s, want %s", got, DefaultShutdownTimeout)
	}
}

// A malformed duration must not stop the process from booting, and must not
// produce a zero budget (which would make shutdown instantaneous).
func TestMalformedBudgetFallsBackToDefault(t *testing.T) {
	t.Setenv(EnvPreStopDelay, "not-a-duration")
	t.Setenv(EnvShutdownTimeout, "-1s")

	d := New()
	if got := d.PreStopDelay(); got != DefaultPreStopDelay {
		t.Errorf("PreStopDelay = %s, want %s", got, DefaultPreStopDelay)
	}
	if got := d.ShutdownTimeout(); got != DefaultShutdownTimeout {
		t.Errorf("ShutdownTimeout = %s, want %s", got, DefaultShutdownTimeout)
	}
}

// Teardown is bounded by the budget, not by a hardcoded constant. A step that
// never returns must not hold the process past the grace period — the platform
// is about to SIGKILL it anyway, and a clean-ish exit beats being shot
// mid-flush.
func TestShutdownContextBoundedByBudget(t *testing.T) {
	d := NewWithBudget(0, 120*time.Millisecond)

	start := time.Now()
	ctx, cancel := d.ShutdownContext()
	defer cancel()

	// A teardown step that ignores cancellation entirely.
	wedged := make(chan struct{})
	select {
	case <-wedged:
		t.Fatal("unreachable")
	case <-ctx.Done():
	}

	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Errorf("shutdown context lived %s with a %s budget", elapsed, d.ShutdownTimeout())
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Errorf("ctx.Err() = %v, want DeadlineExceeded", ctx.Err())
	}
}

func getReady(t *testing.T, baseURL string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(baseURL)
	if err != nil {
		t.Fatalf("GET %s: %v", baseURL, err)
	}
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /ready body: %v", err)
	}
	return resp.StatusCode, body
}
