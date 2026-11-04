// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// newDaemonlessService builds the service without a database. Every path
// exercised here is pure connection bookkeeping — no message is dispatched, so
// nothing reaches the repository.
func newDaemonlessService(t *testing.T) *Service {
	t.Helper()
	svc := newTestService(nil)
	t.Cleanup(svc.Close)
	return svc
}

// parkedStream blocks in Receive until a message is pushed or recv is closed —
// exactly what a healthy idle daemon stream does between heartbeats.
// Like a real one, it is woken out of Receive only by a message, the daemon
// hanging up (recv closed), or abort.
type parkedStream struct {
	recv chan *reliantv1.DaemonMessage
	sent chan *reliantv1.ServerMessage

	abortOnce sync.Once
	aborted   chan struct{}
	// receiving counts the goroutines inside Receive right now.
	receiving atomic.Int32
}

func newParkedStream() *parkedStream {
	return &parkedStream{
		recv:    make(chan *reliantv1.DaemonMessage),
		sent:    make(chan *reliantv1.ServerMessage, 8),
		aborted: make(chan struct{}),
	}
}

func (s *parkedStream) Send(msg *reliantv1.ServerMessage) error {
	s.sent <- msg
	return nil
}

func (s *parkedStream) Receive() (*reliantv1.DaemonMessage, error) {
	s.receiving.Add(1)
	defer s.receiving.Add(-1)
	select {
	case msg, ok := <-s.recv:
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	case <-s.aborted:
		return nil, errors.New("stream aborted")
	}
}

func (s *parkedStream) abort() { s.abortOnce.Do(func() { close(s.aborted) }) }

func newTestConn(userID, daemonID string, stream daemonStream) *daemonConnection {
	return &daemonConnection{
		userID:              userID,
		daemonID:            daemonID,
		connectedAt:         time.Now().UTC(),
		lastActivity:        time.Now().UTC(),
		attachedAt:          leaseTime(time.Now()),
		attachSource:        db.DaemonAttachmentSourceInbound,
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
		require.Equal(t, connect.CodeAborted, svcerr.Code(err),
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

	require.Equal(t, connect.CodeAborted, svcerr.Code(conn.closedReason()))
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

// A tool response must carry the identity of the daemon that ran it.
//
// A backgrounded call's process lives in that one daemon's memory, and a user
// can have several daemons. The server records this id on the tool call so it
// can later ask THAT daemon whether the process ended; without it, every
// backgrounded call stayed "running" forever. The id comes from the
// gateway-assigned connection, never from anything the daemon asserts.
func TestToolResponseIsStampedWithTheAnsweringDaemon(t *testing.T) {
	svc := newDaemonlessService(t)
	daemonID := uuid.NewString()
	stream := newParkedStream()
	conn := newTestConn("user-1", daemonID, stream)
	svc.mu.Lock()
	registerTestConn(svc, conn)
	svc.mu.Unlock()

	go func() { _ = svc.handleIncoming(context.Background(), conn) }()
	defer close(stream.recv)

	respCh := make(chan *toolexec.ToolExecutionResponse, 1)
	go func() {
		resp, err := svc.sendToolRequestToConn(context.Background(), conn, &toolexec.ToolExecutionRequest{
			RequestID: "req-bg",
			ToolName:  "shell",
			ToolInput: `{"command":"npm run dev","run_in_background":true}`,
			TimeoutMs: 5000,
		})
		if err == nil {
			respCh <- resp
		}
	}()

	select {
	case <-conn.sendCh:
	case <-time.After(3 * time.Second):
		t.Fatal("tool request was never sent")
	}
	stream.recv <- &reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_ToolResponse{
		ToolResponse: &reliantv1.ToolResponse{RequestId: "req-bg", Success: true, Backgrounded: true},
	}}

	select {
	case resp := <-respCh:
		require.True(t, resp.Backgrounded)
		require.Equal(t, daemonID, resp.DaemonID, "the response must name the daemon whose connection answered")
	case <-time.After(3 * time.Second):
		t.Fatal("tool response never arrived")
	}
}

// The daemon enforces a request's timeout itself and answers with the verdict
// (a killed shell command: exit 124, timed out) a moment after it. That answer
// must reach the caller, not lose a race against the gateway's own timer and
// be replaced by a transport failure — which a run step can only read as
// "outcome unknown" and refuse to re-run.
func TestToolResponseJustAfterTheRequestTimeoutIsDelivered(t *testing.T) {
	svc := newDaemonlessService(t)
	stream := newParkedStream()
	conn := newTestConn("user-1", uuid.NewString(), stream)
	svc.mu.Lock()
	registerTestConn(svc, conn)
	svc.mu.Unlock()

	go func() { _ = svc.handleIncoming(context.Background(), conn) }()
	defer close(stream.recv)

	type result struct {
		resp *toolexec.ToolExecutionResponse
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		resp, err := svc.sendToolRequestToConn(context.Background(), conn, &toolexec.ToolExecutionRequest{
			RequestID: "req-timeout",
			ToolName:  "shell",
			ToolInput: `{"command":"sleep 600","timeout":50}`,
			TimeoutMs: 50,
		})
		resCh <- result{resp, err}
	}()

	select {
	case <-conn.sendCh:
	case <-time.After(3 * time.Second):
		t.Fatal("tool request was never sent")
	}
	time.Sleep(300 * time.Millisecond) // well past the request's 50ms timeout
	stream.recv <- &reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_ToolResponse{
		ToolResponse: &reliantv1.ToolResponse{RequestId: "req-timeout", Success: true,
			Content: `{"stdout":"","stderr":"command timed out","exit_code":124}`},
	}}

	select {
	case res := <-resCh:
		require.NoError(t, res.err, "the daemon's verdict arrived within the grace; it is the answer")
		require.Contains(t, res.resp.Content, `"exit_code":124`)
	case <-time.After(5 * time.Second):
		t.Fatal("tool response never arrived")
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
		require.Contains(t, err.Error(), "connection interrupted")
		require.Contains(t, err.Error(), "may still complete")
		// Typed, so the bridge can tell the requester the outcome is unknown
		// (and the requester can ask again when that is safe) without
		// parsing this message.
		require.ErrorIs(t, err, toolexec.ErrDaemonConnectionInterrupted)
	case <-time.After(3 * time.Second):
		t.Fatal("the caller was still waiting after its daemon connection ended")
	}
}
