// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"io"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// newDaemonlessService builds the service without a database. Every path
// exercised here is pure connection bookkeeping — no message is dispatched, so
// nothing reaches the repository.
func newDaemonlessService(t *testing.T) *ToolsDaemonService {
	t.Helper()
	svc := NewToolsDaemonService(nil)
	t.Cleanup(svc.Close)
	return svc
}

// parkedStream blocks in Receive until a message is pushed or recv is closed —
// exactly what a healthy idle daemon stream does between heartbeats.
type parkedStream struct {
	recv chan *reliantv1.DaemonMessage
	sent chan *reliantv1.ServerMessage
}

func newParkedStream() *parkedStream {
	return &parkedStream{
		recv: make(chan *reliantv1.DaemonMessage),
		sent: make(chan *reliantv1.ServerMessage, 8),
	}
}

func (s *parkedStream) Send(msg *reliantv1.ServerMessage) error {
	s.sent <- msg
	return nil
}

func (s *parkedStream) Receive() (*reliantv1.DaemonMessage, error) {
	msg, ok := <-s.recv
	if !ok {
		return nil, io.EOF
	}
	return msg, nil
}

func newTestConn(userID, daemonID string, stream daemonStream) *daemonConnection {
	return &daemonConnection{
		userID:              userID,
		daemonID:            daemonID,
		connectedAt:         time.Now().UTC(),
		lastActivity:        time.Now().UTC(),
		stream:              stream,
		sendCh:              make(chan *reliantv1.ServerMessage, 8),
		done:                make(chan struct{}),
		pendingCommands:     make(map[string]chan *reliantv1.DaemonCommandResponse),
		pendingToolRequests: make(map[string]chan *toolexec.ToolExecutionResponse),
		terminalSubs:        make(map[string][]chan *toolexec.TerminalOutputEvent),
		processOutputSubs:   make(map[string][]chan *toolexec.ProcessOutputEvent),
	}
}

// A superseded connection must end its stream AT ONCE, not at the daemon's next
// heartbeat. Checking done only between messages left the handler parked in
// Receive for up to a full heartbeat interval, during which the gateway had
// already handed the daemon id to someone else — and the daemon, seeing only a
// bare EOF whenever the handler eventually did return, redialed and evicted the
// new holder right back.
func TestHandleIncomingEndsImmediatelyWhenSuperseded(t *testing.T) {
	svc := newDaemonlessService(t)

	conn := newTestConn("user-1", uuid.NewString(), newParkedStream())

	errCh := make(chan error, 1)
	go func() { errCh <- svc.handleIncoming(context.Background(), conn) }()

	// Let the reader goroutine park inside Receive, which is where a healthy
	// idle daemon stream spends nearly all of its time.
	time.Sleep(100 * time.Millisecond)

	svc.supersedeIncumbent(conn)

	select {
	case err := <-errCh:
		require.Error(t, err, "a superseded stream must not end with a bare nil/EOF")
		require.Equal(t, connect.CodeAborted, connect.CodeOf(err),
			"the daemon distinguishes supersession from a network drop by this code")
	case <-time.After(3 * time.Second):
		t.Fatal("handleIncoming stayed parked in Receive after the connection was superseded")
	}
}

// An ordinary teardown (sweeper, shutdown) is not a supersession and must not
// tell the daemon to stop reconnecting.
func TestHandleIncomingEndsWithoutErrorOnOrdinaryTeardown(t *testing.T) {
	svc := newDaemonlessService(t)

	conn := newTestConn("user-1", uuid.NewString(), newParkedStream())

	errCh := make(chan error, 1)
	go func() { errCh <- svc.handleIncoming(context.Background(), conn) }()
	time.Sleep(100 * time.Millisecond)

	conn.closeDone()

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("handleIncoming did not return after an ordinary teardown")
	}
}

// The status code IS the contract with the daemon: internal/toolexec/
// daemonruntime stops reconnecting on CodeAborted and on nothing else.
func TestSupersedeIncumbentRecordsAbortedReason(t *testing.T) {
	svc := newDaemonlessService(t)

	conn := newTestConn("user-1", uuid.NewString(), newParkedStream())
	svc.supersedeIncumbent(conn)

	require.Equal(t, connect.CodeAborted, connect.CodeOf(conn.closedReason()))
}

// The connection that holds the pending-response entry must be the connection
// the request is SENT on. Resolving the user's default daemon a second time at
// send-time meant a slot replaced in between delivered the request to
// connection B while the waiter listened on connection A: B's answer found no
// pending entry and was dropped, A never closed, and the caller waited out the
// entire tool timeout (330s at the NATS hop) for a reply that could not arrive.
func TestSendToolRequestGoesToTheConnectionThatIsWaiting(t *testing.T) {
	svc := newDaemonlessService(t)

	const userID = "user-1"
	daemonID := uuid.NewString()

	waiting := newTestConn(userID, daemonID, newParkedStream())
	// The slot now points at a DIFFERENT connection, as it does after a
	// registration replaces the incumbent.
	usurper := newTestConn(userID, daemonID, newParkedStream())
	svc.mu.Lock()
	registerTestConn(svc, usurper)
	svc.mu.Unlock()

	go func() {
		_, _ = svc.sendToolRequestToConn(context.Background(), waiting, &toolexec.ToolExecutionRequest{
			RequestID: "req-1",
			ToolName:  "bash",
			ToolInput: `{"command":"true"}`,
			TimeoutMs: 1000,
		})
	}()

	select {
	case msg := <-waiting.sendCh:
		require.Equal(t, "req-1", msg.GetToolRequest().GetRequestId())
	case msg := <-usurper.sendCh:
		t.Fatalf("tool request was sent to a connection with no pending waiter (%q) — its answer would be dropped", msg.GetToolRequest().GetRequestId())
	case <-time.After(3 * time.Second):
		t.Fatal("tool request was never sent")
	}
}

// A tool request whose connection dies must fail as soon as the connection
// does, so the caller is not left waiting out the full tool timeout.
func TestSendToolRequestFailsAsSoonAsItsConnectionEnds(t *testing.T) {
	svc := newDaemonlessService(t)

	conn := newTestConn("user-1", uuid.NewString(), newParkedStream())

	errCh := make(chan error, 1)
	go func() {
		_, err := svc.sendToolRequestToConn(context.Background(), conn, &toolexec.ToolExecutionRequest{
			RequestID: "req-1",
			ToolName:  "bash",
			ToolInput: `{"command":"sleep 600"}`,
			// The lane timeout that produced the 330s NATS waits.
			TimeoutMs: 300000,
		})
		errCh <- err
	}()

	select {
	case <-conn.sendCh:
	case <-time.After(3 * time.Second):
		t.Fatal("tool request was never sent to the connection that is waiting for its answer")
	}
	svc.supersedeIncumbent(conn)

	select {
	case err := <-errCh:
		require.Error(t, err)
		require.Contains(t, err.Error(), "daemon disconnected")
	case <-time.After(3 * time.Second):
		t.Fatal("the caller was still waiting after its daemon connection ended")
	}
}
