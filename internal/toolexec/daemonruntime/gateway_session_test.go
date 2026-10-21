// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// useTestSession makes a fresh session d's current one, with a queue of buf
// messages and no writer: a test reads what the daemon sends straight off the
// queue (testSendCh).
func useTestSession(d *daemonClient, buf int) *gatewaySession {
	s := newTestSession(buf)
	d.session.Store(s)
	return s
}

func newTestSession(buf int) *gatewaySession {
	return &gatewaySession{
		sendCh:   make(chan *reliantv1.DaemonMessage, buf),
		done:     make(chan struct{}),
		sendDone: make(chan struct{}),
	}
}

// testSendCh is the current session's queue.
func (d *daemonClient) testSendCh() chan *reliantv1.DaemonMessage {
	return d.session.Load().sendCh
}

// sentToolResponse returns the next tool response queued on s, failing the
// test when none is.
func sentToolResponse(t *testing.T, s *gatewaySession) *reliantv1.ToolResponse {
	t.Helper()
	for {
		select {
		case msg := <-s.sendCh:
			if r := msg.GetToolResponse(); r != nil {
				return r
			}
		default:
			t.Fatal("expected a tool response on this session")
			return nil
		}
	}
}

func noToolResponse(t *testing.T, s *gatewaySession, why string) {
	t.Helper()
	for {
		select {
		case msg := <-s.sendCh:
			require.Nil(t, msg.GetToolResponse(), why)
		default:
			return
		}
	}
}

func finished(content string) *toolexec.ExecutionResult {
	return &toolexec.ExecutionResult{Success: true, Content: content}
}

// The outbox answers "what became of this request" through the whole life of
// a call, and an acknowledgement is consumed exactly once.
func TestResultOutboxTracksACallFromStartToAcknowledgement(t *testing.T) {
	var o resultOutbox
	require.Equal(t, toolexec.ToolRequestUnknown, o.state("req-1"), "a request never seen is unknown")

	o.start("req-1")
	require.Equal(t, toolexec.ToolRequestRunning, o.state("req-1"))
	require.Equal(t, 1, o.runningCount())

	o.hold("req-1", (&daemonClient{}).toolResponse("req-1", finished("ok")), nil)
	require.Equal(t, toolexec.ToolRequestCompleted, o.state("req-1"))
	require.Equal(t, 0, o.runningCount())

	require.NotNil(t, o.ack("req-1"))
	require.Equal(t, toolexec.ToolRequestDelivered, o.state("req-1"))
	require.Nil(t, o.ack("req-1"), "a second ack for the same result is a no-op")
	require.Empty(t, o.matching(func(*heldResult) bool { return true }))
}

// A result finished while the daemon has no stream is held, then sent on the
// next stream the gateway registers — and only until a gateway acknowledges
// it.
func TestToolResultIsHeldUntilAcknowledgedAndResentOnTheNextStream(t *testing.T) {
	d := &daemonClient{}
	first := useTestSession(d, 8)
	first.end() // the stream dropped before the call finished

	d.results.start("req-1")
	d.sendToolResult(first, "req-1", finished("built"))
	require.Equal(t, toolexec.ToolRequestCompleted, d.results.state("req-1"))

	second := newTestSession(8)
	d.activateSession(second)
	resp := sentToolResponse(t, second)
	require.Equal(t, "req-1", resp.RequestId)
	require.Equal(t, "built", resp.Content)

	d.handleToolResultAck("req-1")
	require.Equal(t, toolexec.ToolRequestDelivered, d.results.state("req-1"))

	third := newTestSession(8)
	d.activateSession(third)
	noToolResponse(t, third, "an acknowledged result must not be sent again")
}

// A result whose stream ended before any gateway acknowledged it goes again
// on the current stream when that stream ends.
func TestResultSentOnAStreamThatEndsUnacknowledgedIsResent(t *testing.T) {
	d := &daemonClient{}
	old := useTestSession(d, 8)
	d.results.start("req-1")
	d.sendToolResult(old, "req-1", finished("out"))
	require.Equal(t, "req-1", sentToolResponse(t, old).RequestId)

	current := newTestSession(8)
	d.session.Store(current)
	old.end()
	d.sessionEnded(old)

	require.Equal(t, "req-1", sentToolResponse(t, current).RequestId,
		"a result last sent on a stream that ended unacknowledged must go out again")
}

// While the stream a request came in on is open, its reply goes there — the
// draining replica is the one holding the waiter. Once it has ended, the
// reply goes on the current stream.
func TestReplyGoesBackOnTheOriginatingStreamWhileItIsOpen(t *testing.T) {
	d := &daemonClient{}
	origin := newTestSession(8)
	current := useTestSession(d, 8)

	d.results.start("req-1")
	d.sendToolResult(origin, "req-1", finished("a"))
	require.Equal(t, "req-1", sentToolResponse(t, origin).RequestId)
	noToolResponse(t, current, "the reply belongs on the stream its request came in on")

	origin.end()
	d.results.start("req-2")
	d.sendToolResult(origin, "req-2", finished("b"))
	require.Equal(t, "req-2", sentToolResponse(t, current).RequestId)
}

// Make before break: a draining stream is retired only once a successor is
// registered AND nothing that came in on it is still unanswered.
func TestDrainingStreamRetiresOnlyWhenIdleAndSucceeded(t *testing.T) {
	d := &daemonClient{}
	old := useTestSession(d, 8)
	var retired atomic.Bool
	old.retire = func() { retired.Store(true) }

	// A tool call arrives on the old stream, then the replica drains.
	old.inFlight.Add(1)
	d.results.start("req-1")
	require.True(t, d.beginSessionDrain(old, &reliantv1.GatewayDrain{}))
	require.False(t, d.beginSessionDrain(old, &reliantv1.GatewayDrain{}), "only the first notice moves the daemon")

	d.maybeRetire(old)
	require.False(t, retired.Load(), "never retire the only stream")

	successor := newTestSession(8)
	d.activateSession(successor)
	require.False(t, retired.Load(), "a call is still in flight on the old stream")

	d.sendToolResult(old, "req-1", finished("done"))
	require.Equal(t, "req-1", sentToolResponse(t, old).RequestId, "the reply goes where the waiter is")
	require.False(t, retired.Load(), "not until the gateway acknowledges the result")

	d.handleToolResultAck("req-1")
	require.True(t, retired.Load(), "acknowledged, nothing left in flight, successor registered: retire")
}

// A daemon command holds a draining stream open until its response is queued.
func TestDrainingStreamWaitsForItsDaemonCommands(t *testing.T) {
	d := newTestDaemonClient("d-drain", "u-drain")
	old := useTestSession(d, 8)
	var retired atomic.Bool
	old.retire = func() { retired.Store(true) }
	old.inFlight.Add(1) // a command arrived on the old stream before the drain
	require.True(t, d.beginSessionDrain(old, &reliantv1.GatewayDrain{}))
	d.activateSession(newTestSession(8))
	require.False(t, retired.Load(), "the command is still running")

	d.handleDaemonCommand(old, statusCommand(t, "cmd-1", "nothing"))
	require.NotNil(t, (<-old.sendCh).GetDaemonCommandResponse(), "the response goes back on the old stream")
	require.True(t, retired.Load(), "the command was the last thing in flight")
}

func statusCommand(t *testing.T, commandID, requestID string) *reliantv1.DaemonCommandRequest {
	t.Helper()
	payload, err := json.Marshal(toolexec.ToolStatusRequest{RequestID: requestID})
	require.NoError(t, err)
	return &reliantv1.DaemonCommandRequest{RequestId: commandID, CommandType: toolexec.ToolStatusCommand, Payload: payload}
}

// toolState asks d about requestID on s, and returns the answer with any tool
// results d sent along the way.
func toolState(t *testing.T, d *daemonClient, s *gatewaySession, requestID string) (toolexec.ToolRequestState, []*reliantv1.ToolResponse) {
	t.Helper()
	d.handleDaemonCommand(s, statusCommand(t, "status-"+requestID, requestID))
	var results []*reliantv1.ToolResponse
	for {
		msg := <-s.sendCh
		if r := msg.GetToolResponse(); r != nil {
			results = append(results, r)
		}
		if resp := msg.GetDaemonCommandResponse(); resp != nil {
			require.True(t, resp.Success, resp.ErrorMessage)
			var status toolexec.ToolStatusResponse
			require.NoError(t, json.Unmarshal(resp.Payload, &status))
			return status.State, results
		}
	}
}

// tool.status answers from the outbox, and asking about a finished call is
// enough to get its result sent again.
func TestToolStatusCommandAnswersAndResendsAHeldResult(t *testing.T) {
	d := newTestDaemonClient("d-status", "u-status")
	s := useTestSession(d, 16)

	state, _ := toolState(t, d, s, "req-never")
	require.Equal(t, toolexec.ToolRequestUnknown, state,
		"a daemon that restarted knows nothing of the request — that is the clean failure")

	d.results.start("req-1")
	state, _ = toolState(t, d, s, "req-1")
	require.Equal(t, toolexec.ToolRequestRunning, state)

	d.results.hold("req-1", d.toolResponse("req-1", finished("result")), nil)
	state, resent := toolState(t, d, s, "req-1")
	require.Equal(t, toolexec.ToolRequestCompleted, state)
	require.Len(t, resent, 1, "asking resends the held result")
	require.Equal(t, "req-1", resent[0].RequestId)

	d.handleToolResultAck("req-1")
	state, resent = toolState(t, d, s, "req-1")
	require.Equal(t, toolexec.ToolRequestDelivered, state)
	require.Empty(t, resent)
}

// The heartbeat carries the daemon's count of executing tool calls, so the
// replica a call outlived its stream onto still keeps the machine awake.
func TestHeartbeatCarriesToolCallsInFlight(t *testing.T) {
	d := &daemonClient{timing: sessionTiming{heartbeatInterval: 10 * time.Millisecond}}
	s := newTestSession(8)
	d.results.start("req-1")
	d.results.start("req-2")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.runHeartbeats(ctx, s, nil)
	select {
	case msg := <-s.sendCh:
		require.EqualValues(t, 2, msg.GetHeartbeat().GetToolCallsInFlight())
	case <-time.After(5 * time.Second):
		t.Fatal("no heartbeat")
	}
}

// drainingGateway plays two replicas behind one address. The first stream
// (replica A) gets a tool request and then a drain notice; every later stream
// (replica B) is served normally. It records what arrived where.
type drainingGateway struct {
	reliantv1connect.UnimplementedToolsDaemonServiceHandler
	request  *reliantv1.ToolRequest
	deadline time.Duration
	// cutAtDeadline makes replica A end its stream at the deadline, as a
	// real replica does, instead of waiting for the daemon to retire it.
	cutAtDeadline bool

	sessions atomic.Int32
	mu       sync.Mutex
	events   []string
	// secondOpenedWhileFirstOpen records make-before-break.
	firstOpen                  atomic.Bool
	secondOpenedWhileFirstOpen atomic.Bool
	responses                  chan string // "<replica>:<request id>"
	firstEnded                 chan struct{}
}

func (g *drainingGateway) record(event string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.events = append(g.events, event)
}

func (g *drainingGateway) ConnectDaemon(ctx context.Context, stream *connect.BidiStream[reliantv1.DaemonMessage, reliantv1.ServerMessage]) error {
	n := g.sessions.Add(1)
	replica := "A"
	if n > 1 {
		replica = "B"
		if g.firstOpen.Load() {
			g.secondOpenedWhileFirstOpen.Store(true)
		}
	}
	if _, err := stream.Receive(); err != nil {
		return err
	}
	if err := stream.Send(&reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_RegistrationAck{
		RegistrationAck: &reliantv1.RegistrationAck{Accepted: true, DaemonId: "daemon-1", UserId: "user-1"},
	}}); err != nil {
		return err
	}
	if replica == "A" {
		g.firstOpen.Store(true)
		defer func() { g.firstOpen.Store(false); close(g.firstEnded) }()
		if err := stream.Send(&reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_ToolRequest{ToolRequest: g.request}}); err != nil {
			return err
		}
		if err := stream.Send(&reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_Drain{
			Drain: &reliantv1.GatewayDrain{DeadlineUnixMs: time.Now().Add(g.deadline).UnixMilli()},
		}}); err != nil {
			return err
		}
	}

	received := make(chan *reliantv1.DaemonMessage)
	errs := make(chan error, 1)
	go func() {
		for {
			msg, err := stream.Receive()
			if err != nil {
				errs <- err
				return
			}
			received <- msg
		}
	}()
	var cut <-chan time.Time
	if replica == "A" && g.cutAtDeadline {
		cut = time.After(g.deadline)
	}
	for {
		select {
		case <-ctx.Done():
			g.record(replica + ": daemon ended the stream")
			return ctx.Err()
		case err := <-errs:
			g.record(replica + ": daemon ended the stream")
			return err
		case <-cut:
			return connect.NewError(connect.CodeUnavailable, nil)
		case msg := <-received:
			if r := msg.GetToolResponse(); r != nil {
				g.responses <- replica + ":" + r.RequestId
				if err := stream.Send(&reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_ToolResultAck{
					ToolResultAck: &reliantv1.ToolResultAck{RequestId: r.RequestId},
				}}); err != nil {
					return err
				}
			}
		}
	}
}

func slowShellRequest(t *testing.T, requestID, dir, command string) *reliantv1.ToolRequest {
	t.Helper()
	input, err := json.Marshal(map[string]any{"command": command})
	require.NoError(t, err)
	ctxJSON, err := json.Marshal(map[string]any{
		"chat_id":  "chat-drain",
		"worktree": map[string]any{"id": "wt-drain", "path": dir},
	})
	require.NoError(t, err)
	return &reliantv1.ToolRequest{
		RequestId: requestID, ToolCallId: requestID, ToolName: tools.ShellToolName,
		ToolInput: string(input), ContextJson: string(ctxJSON), TimeoutMs: 30_000,
	}
}

func startDrainingGateway(t *testing.T, g *drainingGateway) *daemonClient {
	t.Helper()
	g.responses = make(chan string, 8)
	g.firstEnded = make(chan struct{})
	client := newDaemonClientForTest(t, t.TempDir(), startGateway(t, g))
	startDaemon(t, client)
	return client
}

// The whole daemon side of a rollout, against real streams: the replica
// serving a running tool call drains; the daemon opens a stream to another
// replica while the first is still open; the call's result goes back on the
// first stream, where its waiter is; and the daemon retires that stream once
// the result is acknowledged — well before the replica's deadline.
func TestDaemonMovesOffADrainingGatewayMakeBeforeBreak(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: runs a real shell command over real streams; runs in task test")
	}
	g := &drainingGateway{deadline: 20 * time.Second}
	g.request = slowShellRequest(t, "req-drain", t.TempDir(), "sleep 1; echo finished")
	startDrainingGateway(t, g)

	select {
	case got := <-g.responses:
		require.Equal(t, "A:req-drain", got, "the result goes back on the stream its request came in on")
	case <-time.After(20 * time.Second):
		t.Fatal("the tool call's result never arrived")
	}
	require.True(t, g.secondOpenedWhileFirstOpen.Load(),
		"make before break: the daemon must open its next stream while the draining one is still open")

	select {
	case <-g.firstEnded:
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon did not retire the drained stream once its work was acknowledged")
	}
	assert.Less(t, g.sessions.Load(), int32(3), "one move, not a reconnect loop")
}

// A call that outlives the draining replica's deadline is not lost: the
// replica ends the stream, and the daemon sends the result on its new stream
// once the call finishes.
func TestResultOfACallThatOutlivesTheDrainArrivesOnTheNewStream(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: runs a real shell command over real streams; runs in task test")
	}
	g := &drainingGateway{deadline: 300 * time.Millisecond, cutAtDeadline: true}
	g.request = slowShellRequest(t, "req-long", t.TempDir(), "sleep 2; echo finished")
	client := startDrainingGateway(t, g)

	select {
	case got := <-g.responses:
		require.Equal(t, "B:req-long", got, "the old stream is gone; the result must come over the new one")
	case <-time.After(20 * time.Second):
		t.Fatal("the result of a call that outlived its stream never arrived")
	}
	require.Eventually(t, func() bool {
		return client.results.state("req-long") == toolexec.ToolRequestDelivered
	}, 5*time.Second, 20*time.Millisecond, "the daemon must forget the result once acknowledged")
}
