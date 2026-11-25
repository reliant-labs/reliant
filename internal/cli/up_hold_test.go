package cli

import (
	"context"
	"testing"
	"time"
)

// TestAwaitHoldEnd pins TL-71: a supervising `forge env up` ends its hold
// when everything it started has exited — what `forge env down` from another
// terminal does to it — and not while anything still runs.
func TestAwaitHoldEnd(t *testing.T) {
	old := holdPoll
	holdPoll = 5 * time.Millisecond
	t.Cleanup(func() { holdPoll = old })

	api, web := make(chan struct{}), make(chan struct{})
	procs := &procRegistry{processes: []*managedProcess{{name: "api", done: api}, {name: "web", done: web}}}

	ended := make(chan bool, 1)
	go func() { ended <- awaitHoldEnd(t.Context(), procs) }()

	close(api)
	select {
	case <-ended:
		t.Fatal("the hold ended while web still runs")
	case <-time.After(50 * time.Millisecond):
	}
	close(web)
	select {
	case gone := <-ended:
		if !gone {
			t.Error("awaitHoldEnd = false; want true: the stack is gone")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the hold did not end after every process exited")
	}

	// A signal still ends it, reported as such.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if awaitHoldEnd(ctx, &procRegistry{processes: []*managedProcess{{name: "api", done: make(chan struct{})}}}) {
		t.Error("a cancelled hold reported the stack gone")
	}
}
