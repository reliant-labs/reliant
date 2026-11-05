package tools_daemon

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// newTerminalTestService is a Service with one connected daemon whose sends
// land on a buffered channel, enough for SubscribeTerminalOutput.
func newTerminalTestService(t *testing.T) (*Service, *daemonConnection) {
	t.Helper()
	conn := &daemonConnection{
		userID:            "user-1",
		daemonID:          "daemon-1",
		sendCh:            make(chan *reliantv1.ServerMessage, 16),
		done:              make(chan struct{}),
		terminalSubs:      make(map[string][]*toolexec.TerminalOutputQueue),
		processOutputSubs: make(map[string][]chan *toolexec.ProcessOutputEvent),
	}
	svc := &Service{
		connections: map[string]*daemonConnection{},
		userDaemons: map[string][]string{},
	}
	registerTestConn(svc, conn)
	return svc, conn
}

// The gateway's hop: the daemon's receive loop dispatches faster than the NATS
// forwarder drains. Every byte arrives, in order — dispatch used to drop a
// chunk whenever the forwarder's 64-slot channel was full.
func TestDispatchTerminalEvent_SlowSubscriberGetsEveryByteInOrder(t *testing.T) {
	svc, conn := newTerminalTestService(t)
	ch, unsub, err := svc.SubscribeTerminalOutput("user-1", "daemon-1", "s1")
	require.NoError(t, err)
	defer unsub()

	var want bytes.Buffer
	n := 3 * toolexec.TerminalOutputQueueCapacity // past the queue, so dispatch waits
	go func() {
		for i := 0; i < n; i++ {
			chunk := []byte(fmt.Sprintf("<chunk %05d>", i))
			conn.dispatchTerminalEvent(&toolexec.TerminalOutputEvent{SessionID: "s1", Data: chunk})
		}
	}()
	for i := 0; i < n; i++ {
		want.WriteString(fmt.Sprintf("<chunk %05d>", i))
	}

	var got bytes.Buffer
	deadline := time.After(10 * time.Second)
	for got.Len() < want.Len() {
		select {
		case evt := <-ch:
			require.False(t, evt.Overflow)
			got.Write(evt.Data)
			time.Sleep(20 * time.Microsecond)
		case <-deadline:
			t.Fatalf("got %d of %d bytes", got.Len(), want.Len())
		}
	}
	require.Equal(t, want.String(), got.String())
}

// A forwarder that takes nothing past the timeout: dispatch stops waiting on
// it, unregisters it, counts the overflow, and the forwarder receives what
// was queued and then the overflow error, which it publishes to the
// api-server like any other event.
func TestDispatchTerminalEvent_OverflowEndsSessionAndCounts(t *testing.T) {
	prev := terminalOutputSendTimeout
	terminalOutputSendTimeout = 20 * time.Millisecond
	t.Cleanup(func() { terminalOutputSendTimeout = prev })
	before := testutil.ToFloat64(observability.TerminalOutputOverflowTotal.WithLabelValues("gateway"))

	svc, conn := newTerminalTestService(t)
	ch, unsub, err := svc.SubscribeTerminalOutput("user-1", "daemon-1", "s1")
	require.NoError(t, err)
	defer unsub()

	for i := 0; i < toolexec.TerminalOutputQueueCapacity+5; i++ {
		conn.dispatchTerminalEvent(&toolexec.TerminalOutputEvent{SessionID: "s1", Data: []byte("x")})
	}
	require.Equal(t, before+1, testutil.ToFloat64(observability.TerminalOutputOverflowTotal.WithLabelValues("gateway")),
		"one overflow, counted once: the unregistered subscriber is not waited on again")
	conn.terminalSubsMu.Lock()
	require.Empty(t, conn.terminalSubs["s1"], "the overflowed subscriber is unregistered")
	conn.terminalSubsMu.Unlock()

	var data int
	var last *toolexec.TerminalOutputEvent
	for evt := range ch {
		if evt.Overflow {
			last = evt
			continue
		}
		data += len(evt.Data)
	}
	require.Equal(t, toolexec.TerminalOutputQueueCapacity, data, "what was queued is delivered")
	require.NotNil(t, last)
	require.Equal(t, toolexec.TerminalOutputOverflowMessage, last.Error)
}

// A daemon disconnecting closes its subscribers, including one dispatch is
// waiting on, without a send on a closed channel.
func TestCloseAllSubscribers_ReleasesWaitingDispatch(t *testing.T) {
	prev := terminalOutputSendTimeout
	terminalOutputSendTimeout = time.Minute
	t.Cleanup(func() { terminalOutputSendTimeout = prev })

	svc, conn := newTerminalTestService(t)
	_, unsub, err := svc.SubscribeTerminalOutput("user-1", "daemon-1", "s1")
	require.NoError(t, err)
	defer unsub()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < toolexec.TerminalOutputQueueCapacity+1; i++ {
			conn.dispatchTerminalEvent(&toolexec.TerminalOutputEvent{SessionID: "s1", Data: []byte("x")})
		}
	}()
	time.Sleep(20 * time.Millisecond)
	conn.closeAllSubscribers()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the subscribers did not release the waiting dispatch")
	}
}
