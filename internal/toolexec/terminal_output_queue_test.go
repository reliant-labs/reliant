package toolexec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/observability"
)

// terminalChunks returns n distinct, numbered chunks and their concatenation,
// so a consumer can prove it got every byte in order.
func terminalChunks(n int) ([][]byte, []byte) {
	chunks := make([][]byte, n)
	var all bytes.Buffer
	for i := range chunks {
		chunks[i] = []byte(fmt.Sprintf("<chunk %05d>", i))
		all.Write(chunks[i])
	}
	return chunks, all.Bytes()
}

// readTerminalOutput drains ch until it closes, slowly when pause is set, and
// returns the PTY bytes and the last lifecycle event it saw.
func readTerminalOutput(t *testing.T, ch <-chan *TerminalOutputEvent, pause time.Duration) ([]byte, *TerminalOutputEvent) {
	t.Helper()
	var got bytes.Buffer
	var last *TerminalOutputEvent
	deadline := time.After(10 * time.Second)
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return got.Bytes(), last
			}
			if evt.Error != "" || evt.Closed {
				last = evt
				continue
			}
			got.Write(evt.Data)
			if pause > 0 {
				time.Sleep(pause)
			}
		case <-deadline:
			t.Fatal("the output channel never closed")
		}
	}
}

// A consumer slower than its producer gets every byte, in order: the producer
// waits for room instead of dropping (the 64-slot channel used to drop).
func TestTerminalOutputQueue_SlowConsumerGetsEveryByteInOrder(t *testing.T) {
	q := NewTerminalOutputQueue("s1", 4, 5*time.Second)
	chunks, want := terminalChunks(200)
	go func() {
		for _, c := range chunks {
			if err := q.Send(&TerminalOutputEvent{SessionID: "s1", Data: c}); err != nil {
				t.Errorf("send: %v", err)
				return
			}
		}
		q.Close()
	}()

	got, last := readTerminalOutput(t, q.C(), 200*time.Microsecond)
	require.Nil(t, last, "no error: nothing overflowed")
	require.Equal(t, string(want), string(got))
}

// Close releases a producer waiting on a full queue, and is safe to repeat.
func TestTerminalOutputQueue_CloseReleasesWaitingSend(t *testing.T) {
	q := NewTerminalOutputQueue("s1", 1, time.Minute)
	require.NoError(t, q.Send(&TerminalOutputEvent{Data: []byte("a")}))
	done := make(chan error, 1)
	go func() { done <- q.Send(&TerminalOutputEvent{Data: []byte("b")}) }()
	time.Sleep(10 * time.Millisecond)
	q.Close()
	q.Close()
	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrTerminalOutputClosed)
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not release the waiting Send")
	}
}

// A consumer that takes nothing past the timeout ends the stream loudly: the
// producer is told, and the consumer gets what was queued, then the overflow
// error, then the end of the channel. A Send after the overflow returns at
// once, before the consumer has read anything: the overflow's delivery waits
// for the consumer without holding the queue's lock (reliant #2228, #2230).
func TestTerminalOutputQueue_OverflowEndsWithError(t *testing.T) {
	q := NewTerminalOutputQueue("s1", 2, 20*time.Millisecond)
	require.NoError(t, q.Send(&TerminalOutputEvent{Data: []byte("a")}))
	require.NoError(t, q.Send(&TerminalOutputEvent{Data: []byte("b")}))

	start := time.Now()
	require.ErrorIs(t, q.Send(&TerminalOutputEvent{Data: []byte("c")}), ErrTerminalOutputOverflow)
	require.GreaterOrEqual(t, time.Since(start), 20*time.Millisecond, "the producer waited before giving up")
	require.ErrorIs(t, q.Send(&TerminalOutputEvent{Data: []byte("d")}), ErrTerminalOutputClosed,
		"a failed queue takes nothing more")

	got, last := readTerminalOutput(t, q.C(), 0)
	require.Equal(t, "ab", string(got))
	require.NotNil(t, last)
	require.True(t, last.Overflow)
	require.Equal(t, TerminalOutputOverflowMessage, last.Error)
	require.Equal(t, "s1", last.SessionID)
}

// startTerminalOutputRouter returns a router whose user-1 resolves to
// daemon-1, and a publisher of that session's output as the gateway's
// forwarder publishes it.
func startTerminalOutputRouter(t *testing.T) (*NATSDaemonRouter, func(*TerminalOutputEvent)) {
	t.Helper()
	nc := startBridgeTestNATS(t)
	router := NewNATSDaemonRouter(nc, WithResolver(staticResolver{daemons: []DaemonInfo{{DaemonID: "daemon-1"}}}))
	subject := daemonSubject(terminalOutputSubject, "user-1", "daemon-1") + ".s1"
	publish := func(evt *TerminalOutputEvent) {
		data, err := json.Marshal(evt)
		require.NoError(t, err)
		require.NoError(t, nc.Publish(subject, data))
	}
	return router, publish
}

// The api-server's hop: a websocket pump slower than the gateway gets every
// byte, in order.
func TestNATSRouterTerminalOutput_SlowConsumerGetsEveryByteInOrder(t *testing.T) {
	router, publish := startTerminalOutputRouter(t)
	ch, unsub, err := router.SubscribeTerminalOutput(context.Background(), "user-1", "s1")
	require.NoError(t, err)

	chunks, want := terminalChunks(3 * TerminalOutputQueueCapacity) // past the queue, so the callback waits
	for _, c := range chunks {
		publish(&TerminalOutputEvent{SessionID: "s1", Data: c})
	}
	publish(&TerminalOutputEvent{SessionID: "s1", Closed: true})

	var got bytes.Buffer
	deadline := time.After(10 * time.Second)
	for got.Len() < len(want) {
		select {
		case evt := <-ch:
			require.False(t, evt.Overflow, "a slow consumer within the timeout is not an overflow")
			got.Write(evt.Data)
			time.Sleep(20 * time.Microsecond)
		case <-deadline:
			t.Fatalf("got %d of %d bytes", got.Len(), len(want))
		}
	}
	require.Equal(t, string(want), got.String())
	select {
	case evt := <-ch:
		require.True(t, evt.Closed, "the exit follows the output")
	case <-time.After(5 * time.Second):
		t.Fatal("no exit event")
	}
	unsub()
}

// A websocket pump that takes nothing past the timeout ends the session with
// the overflow error, and the overflow is counted.
func TestNATSRouterTerminalOutput_OverflowEndsSessionAndCounts(t *testing.T) {
	prev := terminalOutputSendTimeout
	terminalOutputSendTimeout = 50 * time.Millisecond
	t.Cleanup(func() { terminalOutputSendTimeout = prev })
	before := testutil.ToFloat64(observability.TerminalOutputOverflowTotal.WithLabelValues("api"))

	router, publish := startTerminalOutputRouter(t)
	ch, unsub, err := router.SubscribeTerminalOutput(context.Background(), "user-1", "s1")
	require.NoError(t, err)
	defer unsub()

	chunks, _ := terminalChunks(TerminalOutputQueueCapacity + 10)
	for _, c := range chunks {
		publish(&TerminalOutputEvent{SessionID: "s1", Data: c})
	}
	// Nobody reads until the callback has given up.
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(observability.TerminalOutputOverflowTotal.WithLabelValues("api")) == before+1
	}, 5*time.Second, 10*time.Millisecond)

	got, last := readTerminalOutput(t, ch, 0)
	require.NotNil(t, last, "the session ends with an error, not in silence")
	require.True(t, last.Overflow)
	require.Equal(t, TerminalOutputOverflowMessage, last.Error)
	_, wantPrefix := terminalChunks(TerminalOutputQueueCapacity)
	require.Equal(t, string(wantPrefix), string(got), "what was delivered is an unbroken prefix")
}
