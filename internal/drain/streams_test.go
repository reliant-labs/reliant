// Copyright (c) 2025 Reliant Labs
package drain

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStreamsEndCancelsBoundContextsWithTheDrainCause(t *testing.T) {
	streams := NewStreams()
	ctx, release := streams.Bind(context.Background())
	defer release()

	if ctx.Err() != nil || Ended(ctx) {
		t.Fatal("a bound context must stay live until End")
	}
	streams.End()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("End did not cancel the bound context")
	}
	if !Ended(ctx) {
		t.Fatalf("Ended must report a drain; cause = %v", context.Cause(ctx))
	}

	// A context derived inside the handler reports it too: that is how a
	// handler several calls deep learns why it was cancelled.
	derived, cancel := context.WithCancel(ctx)
	defer cancel()
	if !Ended(derived) {
		t.Fatal("a context derived from a drained one must report the drain")
	}
}

func TestStreamsBoundAfterEndAreAlreadyEnded(t *testing.T) {
	streams := NewStreams()
	streams.End()
	streams.End() // idempotent

	ctx, release := streams.Bind(context.Background())
	defer release()
	if !Ended(ctx) {
		t.Fatal("a stream that arrives while the server drains must end at once")
	}
}

func TestEndedIsFalseForOtherCancellations(t *testing.T) {
	streams := NewStreams()

	parent, cancelParent := context.WithCancel(context.Background())
	clientGone, release := streams.Bind(parent)
	defer release()
	cancelParent()
	<-clientGone.Done()
	if Ended(clientGone) {
		t.Fatal("the client going away is not a drain")
	}

	released, releaseIt := streams.Bind(context.Background())
	releaseIt()
	<-released.Done()
	if Ended(released) {
		t.Fatal("a handler finishing on its own is not a drain")
	}
	streams.End()
	if Ended(released) {
		t.Fatal("End must not rewrite the cause of a stream that had already finished")
	}
}

func TestHandlerBindsEveryRequest(t *testing.T) {
	streams := NewStreams()
	ended := make(chan bool, 1)
	srv := httptest.NewServer(streams.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		ended <- Ended(r.Context())
	})))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	streams.End()
	select {
	case drained := <-ended:
		if !drained {
			t.Fatal("the handler's request context must end with the drain cause")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("End did not reach the handler's request context")
	}
}

func TestDrainingErrorMatchesTheSentinelAndKeepsItsMessage(t *testing.T) {
	err := fmt.Errorf("stream ended: %w", DrainingError("gateway draining: reconnect"))
	if !IsServerDraining(err) || !errors.Is(err, ErrServerDraining) {
		t.Fatal("a transport's own drain error must match ErrServerDraining")
	}
	if err.Error() != "stream ended: gateway draining: reconnect" {
		t.Fatalf("message = %q", err.Error())
	}
	if IsServerDraining(errors.New("server draining: this replica is shutting down")) {
		t.Fatal("matching is by identity, never by message")
	}
}
