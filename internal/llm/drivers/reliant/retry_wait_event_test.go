package reliant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// The retry ladder used to be completely silent: it runs inside a single
// Temporal activity attempt, so a rate-limited call produced no message, no step
// execution and no status change while it slept. Measured on forge-one-shot run
// b7aa4056, eight of ten fan-out units spent ~113s of their ~129s life in 429
// backoff and every supervision surface reported them as working.
//
// The fix is an event announced BEFORE the wait. Announcing it afterwards would
// be worthless — by then the thing a supervisor needed to see is over — so this
// pins the ordering against the only observable that cannot lie: the retry
// request itself must not have been sent yet when the event arrives.
func TestStreamResponse_AnnouncesProviderBackoffBeforeWaiting(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"Rate limited","type":"rate_limit_error","code":"429"}}`))
	}))
	defer srv.Close()

	client := NewClient(llm.DriverOptions{ApiKey: "test", BaseURL: srv.URL, Model: models.Model{ID: "test-model"}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := client.StreamResponse(ctx, nil, nil, nil)

	var got llm.DriverEvent
	select {
	case got = <-events:
	case <-time.After(30 * time.Second):
		t.Fatal("no event before the backoff wait: a rate-limited stream is invisible")
	}

	if got.Type != llm.EventRetryWait {
		t.Fatalf("first event = %q, want %q — a 429 must announce itself, not go quiet", got.Type, llm.EventRetryWait)
	}
	if got.Retry == nil {
		t.Fatal("EventRetryWait carries no RetryWait: nothing to report")
	}
	// Attempt count, ceiling, provider status and reason: the four facts a
	// supervisor needs to tell "rate limited" from "thinking".
	if got.Retry.Attempt != 1 {
		t.Errorf("attempt = %d, want 1", got.Retry.Attempt)
	}
	if got.Retry.MaxAttempts != models.MaxRetries {
		t.Errorf("max attempts = %d, want %d", got.Retry.MaxAttempts, models.MaxRetries)
	}
	if got.Retry.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status code = %d, want 429", got.Retry.StatusCode)
	}
	if got.Retry.Reason != "http_429" {
		t.Errorf("reason = %q, want http_429", got.Retry.Reason)
	}
	// First rung of 2000ms * 2^(n-1) plus 20% jitter.
	if got.Retry.Delay != 2400*time.Millisecond {
		t.Errorf("delay = %s, want 2.4s", got.Retry.Delay)
	}

	// The ordering proof: the driver is still asleep, so the retry request has
	// not been sent. Cancelling now must leave the request count where it was.
	before := requests.Load()
	cancel()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case _, open := <-events:
			if !open {
				if after := requests.Load(); after != before {
					t.Fatalf("server saw %d more request(s) after the event: the wait was announced AFTER it was taken, not before", after-before)
				}
				return
			}
		case <-deadline:
			t.Fatal("stream did not close after cancellation")
		}
	}
}
