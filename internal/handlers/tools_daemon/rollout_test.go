// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// A daemon-gateway rollout, end to end, with everything real but the daemon
// and the transport under it: an embedded NATS server, two or three gateway
// replicas (Service + NATSToolBridge + ToolResultPublisher, each on its own
// NATS connection, as separate pods are), and the worker's NATSDaemonRouter.
// The daemon is played by the test, speaking the stream protocol the real one
// does (see daemonruntime/gateway_session.go).
//
// Before this change every one of these scenarios ended the tool call with
// "daemon connection interrupted while waiting for tool request ...".

// rolloutCluster is the NATS server every process in a test shares.
type rolloutCluster struct {
	t   *testing.T
	url string
}

func newRolloutCluster(t *testing.T) *rolloutCluster {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	srv := natstest.RunServer(&opts)
	t.Cleanup(srv.Shutdown)
	require.True(t, srv.ReadyForConnections(2*time.Second))
	return &rolloutCluster{t: t, url: srv.ClientURL()}
}

func (c *rolloutCluster) connect() *nats.Conn {
	c.t.Helper()
	nc, err := nats.Connect(c.url)
	require.NoError(c.t, err)
	c.t.Cleanup(nc.Close)
	return nc
}

// replica starts one daemon-gateway replica as the gateway root (internal/app)
// wires it.
func (c *rolloutCluster) replica() *Service {
	c.t.Helper()
	nc := c.connect()
	svc := newTestService(nil)
	svc.deps.ResultPublisher = toolexec.NewToolResultPublisher(nc)
	bridge := toolexec.NewNATSToolBridge(nc, nil, svc)
	svc.AddConnectionListener(bridge)
	c.t.Cleanup(func() { _ = bridge.Close() })
	return svc
}

// worker is the worker's router, resolving every call to daemonID.
func (c *rolloutCluster) worker(daemonID string) *toolexec.NATSDaemonRouter {
	return toolexec.NewNATSDaemonRouter(c.connect(), toolexec.WithResolver(fixedDaemon(daemonID)))
}

type fixedDaemon string

func (d fixedDaemon) ResolveDaemons(context.Context, string, *toolexec.DaemonSelector) ([]toolexec.DaemonInfo, error) {
	return []toolexec.DaemonInfo{{DaemonID: string(d), Type: "local", Status: "connected"}}, nil
}

// pipeStream is the daemon's end of one stream to a replica: what the replica
// sends arrives on toDaemon, and what the test sends as the daemon goes in
// through fromDaemon.
type pipeStream struct {
	toDaemon   chan *reliantv1.ServerMessage
	fromDaemon chan *reliantv1.DaemonMessage
	hangUpOnce sync.Once
	abortOnce  sync.Once
	aborted    chan struct{}
}

func (p *pipeStream) Send(msg *reliantv1.ServerMessage) error {
	p.toDaemon <- msg
	return nil
}

func (p *pipeStream) Receive() (*reliantv1.DaemonMessage, error) {
	select {
	case msg, ok := <-p.fromDaemon:
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	case <-p.aborted:
		return nil, io.ErrClosedPipe
	}
}

// abort is the replica ending its read of the stream.
func (p *pipeStream) abort() { p.abortOnce.Do(func() { close(p.aborted) }) }

// hangUp is the daemon closing the stream (retiring it).
func (p *pipeStream) hangUp() { p.hangUpOnce.Do(func() { close(p.fromDaemon) }) }

// attach connects the daemon to replica svc the way ConnectDaemon does once
// the registration is through: register, announce (the bridge subscribes),
// serve. It reports false when the replica refuses, as a draining one does.
func attach(t *testing.T, svc *Service, userID, daemonID string) (*pipeStream, bool) {
	t.Helper()
	if svc.draining.Load() {
		return nil, false // ConnectDaemon's first check; pinned on its own above
	}
	stream := &pipeStream{
		toDaemon:   make(chan *reliantv1.ServerMessage, 64),
		fromDaemon: make(chan *reliantv1.DaemonMessage, 64),
		aborted:    make(chan struct{}),
	}
	conn := newTestConn(userID, daemonID, stream)
	conn.movesOnDrain = true // registered as a current daemon does
	svc.mu.Lock()
	svc.connections[daemonID] = conn
	svc.userDaemons[userID] = []string{daemonID}
	svc.mu.Unlock()
	svc.notifyConnected(userID, daemonID, "", "", "")
	go svc.runSender(conn)
	go func() {
		_ = svc.handleIncoming(context.Background(), conn)
		svc.teardownConnection(conn, "test stream ended")
	}()
	t.Cleanup(stream.hangUp)
	return stream, true
}

// expect returns the next message the replica sent on stream that match
// accepts, skipping anything else (heartbeats, acks for other requests).
func expect(t *testing.T, stream *pipeStream, what string, match func(*reliantv1.ServerMessage) bool) *reliantv1.ServerMessage {
	t.Helper()
	timeout := time.After(15 * time.Second)
	for {
		select {
		case msg := <-stream.toDaemon:
			if match(msg) {
				return msg
			}
		case <-timeout:
			t.Fatalf("the replica never sent %s", what)
			return nil
		}
	}
}

func toolRequestOn(t *testing.T, stream *pipeStream) *reliantv1.ToolRequest {
	t.Helper()
	return expect(t, stream, "a tool request", func(m *reliantv1.ServerMessage) bool { return m.GetToolRequest() != nil }).GetToolRequest()
}

func drainNoticeOn(t *testing.T, stream *pipeStream) *reliantv1.GatewayDrain {
	t.Helper()
	return expect(t, stream, "a drain notice", func(m *reliantv1.ServerMessage) bool { return m.GetDrain() != nil }).GetDrain()
}

func ackOn(t *testing.T, stream *pipeStream, requestID string) {
	t.Helper()
	expect(t, stream, "the result's ack", func(m *reliantv1.ServerMessage) bool {
		return m.GetToolResultAck().GetRequestId() == requestID
	})
}

// answerStatus plays the daemon answering the worker's tool.status question
// about requestID on stream.
func answerStatus(t *testing.T, stream *pipeStream, requestID string, state toolexec.ToolRequestState) {
	t.Helper()
	cmd := expect(t, stream, "a tool.status question", isToolStatusQuestion).GetDaemonCommand()
	replyStatus(t, stream, cmd, requestID, state)
}

func isToolStatusQuestion(m *reliantv1.ServerMessage) bool {
	return m.GetDaemonCommand().GetCommandType() == toolexec.ToolStatusCommand
}

// answerEveryStatusUntil plays the daemon answering EVERY tool.status question
// it is asked about requestID on stream, as a real daemon does, until the call
// returns, and returns its outcome. The worker asks again whenever an answer
// does not reach it — a replica still tearing down the old stream answered
// first, or the round trip outlasted its timeout on a loaded host — so a
// daemon that answers only the first question leaves the call waiting out its
// own deadline.
func answerEveryStatusUntil(t *testing.T, stream *pipeStream, requestID string, state toolexec.ToolRequestState, results <-chan callResult) callResult {
	t.Helper()
	timeout := time.After(60 * time.Second)
	for {
		select {
		case r := <-results:
			return r
		case msg := <-stream.toDaemon:
			if isToolStatusQuestion(msg) {
				replyStatus(t, stream, msg.GetDaemonCommand(), requestID, state)
			}
		case <-timeout:
			t.Fatal("the tool call never returned")
			return callResult{}
		}
	}
}

// replyStatus answers the tool.status question cmd about requestID with state.
func replyStatus(t *testing.T, stream *pipeStream, cmd *reliantv1.DaemonCommandRequest, requestID string, state toolexec.ToolRequestState) {
	t.Helper()
	var q toolexec.ToolStatusRequest
	require.NoError(t, json.Unmarshal(cmd.Payload, &q))
	require.Equal(t, requestID, q.RequestID)
	payload, err := json.Marshal(toolexec.ToolStatusResponse{State: state})
	require.NoError(t, err)
	stream.fromDaemon <- &reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_DaemonCommandResponse{
		DaemonCommandResponse: &reliantv1.DaemonCommandResponse{
			RequestId: cmd.RequestId, CommandType: cmd.CommandType, Success: true, Payload: payload,
		},
	}}
}

func sendResult(stream *pipeStream, requestID, content string) {
	stream.fromDaemon <- &reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_ToolResponse{
		ToolResponse: &reliantv1.ToolResponse{RequestId: requestID, Success: true, Content: content},
	}}
}

type callResult struct {
	resp *toolexec.ToolExecutionResponse
	err  error
}

// call starts a long foreground shell call from the worker.
func call(router *toolexec.NATSDaemonRouter, userID string) <-chan callResult {
	out := make(chan callResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		resp, err := router.SendToolRequestSync(ctx, userID, &toolexec.ToolExecutionRequest{
			RequestID: uuid.NewString(),
			ToolName:  "shell",
			ToolInput: `{"command":"go test ./..."}`,
			TimeoutMs: 600_000,
		})
		out <- callResult{resp, err}
	}()
	return out
}

func outcome(t *testing.T, results <-chan callResult) callResult {
	t.Helper()
	select {
	case r := <-results:
		return r
	case <-time.After(60 * time.Second):
		t.Fatal("the tool call never returned")
		return callResult{}
	}
}

// The case the change exists for: a ten-minute call is running when its
// replica rolls. The replica's drain deadline passes long before the call
// ends, so the stream it came in on is gone; the daemon, moved to another
// replica, finishes the call there, and the worker gets the result.
func TestRolloutDeliversTheResultOfACallThatOutlivesItsStream(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: an embedded NATS server and two gateway replicas; runs in task test")
	}
	cluster := newRolloutCluster(t)
	const userID, daemonID = "user-1", "daemon-1"
	podA, podB := cluster.replica(), cluster.replica()
	router := cluster.worker(daemonID)

	streamA, _ := attach(t, podA, userID, daemonID)
	results := call(router, userID)
	req := toolRequestOn(t, streamA)

	// SIGTERM on A: it stops taking the daemon's requests and tells it to
	// move. The daemon opens a stream to B, which is serving.
	drainReplica(podA, time.Now().Add(time.Second))
	drainNoticeOn(t, streamA)
	streamB, ok := attach(t, podB, userID, daemonID)
	require.True(t, ok)

	// New work goes to B, never to A: A unsubscribed before the notice.
	second := call(router, userID)
	secondReq := toolRequestOn(t, streamB)
	sendResult(streamB, secondReq.RequestId, "second")
	require.Equal(t, "second", outcome(t, second).resp.Content)
	for drained := false; !drained; {
		select {
		case msg := <-streamA.toDaemon:
			require.Nil(t, msg.GetToolRequest(), "a draining replica must not be handed new requests")
		default:
			drained = true
		}
	}

	// A's deadline: it ends the stream. The call is still running.
	podA.Close()

	// The worker asks the daemon, through B, what became of the call; the
	// daemon says it is running, then finishes it and sends the result on
	// the stream it has.
	answerStatus(t, streamB, req.RequestId, toolexec.ToolRequestRunning)
	sendResult(streamB, req.RequestId, "ok   github.com/acme/app 412.3s")

	got := outcome(t, results)
	require.NoError(t, got.err)
	require.Empty(t, got.resp.ErrorCode, "the rollout must not fail the call: %s", got.resp.ErrorMessage)
	require.Equal(t, "ok   github.com/acme/app 412.3s", got.resp.Content)
	ackOn(t, streamB, req.RequestId)
}

// A call that finishes during the drain window finishes on the stream it came
// in on, where its waiter is — the make-before-break half of the design.
func TestRolloutLetsACallFinishOnItsOwnStreamDuringTheDrain(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: an embedded NATS server and two gateway replicas; runs in task test")
	}
	cluster := newRolloutCluster(t)
	const userID, daemonID = "user-1", "daemon-1"
	podA, podB := cluster.replica(), cluster.replica()
	router := cluster.worker(daemonID)

	streamA, _ := attach(t, podA, userID, daemonID)
	results := call(router, userID)
	req := toolRequestOn(t, streamA)

	drainReplica(podA, time.Now().Add(20*time.Second))
	drainNoticeOn(t, streamA)
	_, ok := attach(t, podB, userID, daemonID)
	require.True(t, ok)

	sendResult(streamA, req.RequestId, "finished during the drain")
	got := outcome(t, results)
	require.NoError(t, got.err)
	require.Equal(t, "finished during the drain", got.resp.Content)
	ackOn(t, streamA, req.RequestId)

	// With nothing left in flight the daemon retires the old stream, and
	// the replica's drain completes without waiting out its deadline: what
	// the gateway's Listener drain waits for is this replica
	// holding no stream.
	streamA.hangUp()
	require.Eventually(t, func() bool { return podA.GetActiveConnections() == 0 },
		5*time.Second, 10*time.Millisecond, "the daemon retired its stream, so the replica holds none")
}

// A daemon that restarted mid-call has nothing to deliver: everything it was
// running died with it. The call must fail promptly and say so — not wait out
// its ten-minute timeout.
func TestRolloutFailsCleanlyWhenTheDaemonRestartedMidCall(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: waits out the lost-result settle; runs in task test")
	}
	cluster := newRolloutCluster(t)
	const userID, daemonID = "user-1", "daemon-1"
	podA, podB := cluster.replica(), cluster.replica()
	router := cluster.worker(daemonID)

	streamA, _ := attach(t, podA, userID, daemonID)
	results := call(router, userID)
	req := toolRequestOn(t, streamA)

	podA.Close() // the stream drops; the daemon process restarts
	streamB, ok := attach(t, podB, userID, daemonID)
	require.True(t, ok)

	requireLostAndSaysWhy(t, answerEveryStatusUntil(t, streamB, req.RequestId, toolexec.ToolRequestUnknown, results))
}

// The same restart, with the old replica slow to let go: until its teardown
// unsubscribes it, a replica whose stream just dropped still takes the
// daemon's commands, and answers the worker's first tool.status — "not
// connected" — before the replica holding the new stream can. On a loaded
// host that lag is real (the release gate, load 65-140); here it is pinned by
// a stand-in for the old replica that answers the first question it sees.
// The worker asks again, the daemon answers, and the call still fails
// promptly with the right reason.
func TestRolloutFailsCleanlyWhenTheOldReplicaAnswersFirst(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: an embedded NATS server and two gateway replicas; runs in task test")
	}
	cluster := newRolloutCluster(t)
	const userID, daemonID = "user-1", "daemon-1"
	podA, podB := cluster.replica(), cluster.replica()
	router := cluster.worker(daemonID)

	streamA, _ := attach(t, podA, userID, daemonID)
	results := call(router, userID)
	req := toolRequestOn(t, streamA)

	// The old replica, not yet unsubscribed: it answers the first command
	// the way a replica that no longer holds the stream does.
	lagging := cluster.connect()
	answered := make(chan struct{})
	var once sync.Once
	sub, err := lagging.Subscribe("daemon.command."+userID+"."+daemonID, func(msg *nats.Msg) {
		once.Do(func() {
			close(answered)
			_ = msg.Respond([]byte(`{"success":false,"error_message":"daemon ` + daemonID + ` is not connected for user ` + userID + `","error_code":"` + toolexec.ErrorCodeDaemonRoundTrip + `"}`))
		})
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	require.NoError(t, lagging.Flush())

	podA.Close()
	streamB, ok := attach(t, podB, userID, daemonID)
	require.True(t, ok)

	requireLostAndSaysWhy(t, answerEveryStatusUntil(t, streamB, req.RequestId, toolexec.ToolRequestUnknown, results))
	// Every command on the daemon's subject is delivered to the stand-in too,
	// but its callback runs on its own goroutine: on a loaded host (main's
	// release gate saw ~1 run in 9) the new replica's answer settles the call
	// before that callback has run. So wait for it rather than sampling it.
	select {
	case <-answered:
	case <-time.After(10 * time.Second):
		t.Fatal("the lagging replica was never asked: this test did not exercise the race it pins")
	}
}

// requireLostAndSaysWhy asserts the call failed as a request its restarted
// daemon no longer knows.
func requireLostAndSaysWhy(t *testing.T, got callResult) {
	t.Helper()
	require.NoError(t, got.err)
	require.Equal(t, toolexec.ErrorCodeDaemonInterrupted, got.resp.ErrorCode)
	require.Contains(t, got.resp.ErrorMessage, "no longer knows the request")
	require.Contains(t, got.resp.ErrorMessage, "restarted")
}

// Two replicas draining at once, as a rollout with surge can leave them: the
// daemon is refused by the second draining replica, keeps its old stream
// meanwhile, and lands on the third. The call's result still arrives.
func TestRolloutWithTwoReplicasDrainingAtOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: an embedded NATS server and three gateway replicas; runs in task test")
	}
	cluster := newRolloutCluster(t)
	const userID, daemonID = "user-1", "daemon-1"
	podA, podB, podC := cluster.replica(), cluster.replica(), cluster.replica()
	router := cluster.worker(daemonID)

	streamA, _ := attach(t, podA, userID, daemonID)
	results := call(router, userID)
	req := toolRequestOn(t, streamA)

	drainReplica(podA, time.Now().Add(time.Second))
	drainReplica(podB, time.Now().Add(time.Second))
	drainNoticeOn(t, streamA)
	_, ok := attach(t, podB, userID, daemonID)
	require.False(t, ok, "a draining replica must refuse the daemon")
	streamC, ok := attach(t, podC, userID, daemonID)
	require.True(t, ok)

	podA.Close()
	answerStatus(t, streamC, req.RequestId, toolexec.ToolRequestCompleted)
	sendResult(streamC, req.RequestId, "survived two drains")

	got := outcome(t, results)
	require.NoError(t, got.err)
	require.Equal(t, "survived two drains", got.resp.Content)
	ackOn(t, streamC, req.RequestId)
}

// The result may be delivered more than once — the daemon resends until it
// is acknowledged — and the worker takes the first copy.
func TestRolloutToleratesDuplicateDelivery(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: an embedded NATS server and two gateway replicas; runs in task test")
	}
	cluster := newRolloutCluster(t)
	const userID, daemonID = "user-1", "daemon-1"
	podA, podB := cluster.replica(), cluster.replica()
	router := cluster.worker(daemonID)

	streamA, _ := attach(t, podA, userID, daemonID)
	results := call(router, userID)
	req := toolRequestOn(t, streamA)
	drainReplica(podA, time.Now().Add(20*time.Second))
	streamB, _ := attach(t, podB, userID, daemonID)

	// The daemon resends a held result on registration, and again when
	// asked about it, before either copy is acknowledged. B has no waiter
	// for it; it publishes both.
	sendResult(streamB, req.RequestId, "once")
	sendResult(streamB, req.RequestId, "once")

	got := outcome(t, results)
	require.NoError(t, got.err)
	require.Equal(t, "once", got.resp.Content)
	ackOn(t, streamB, req.RequestId)
	ackOn(t, streamB, req.RequestId) // every copy is acknowledged; the daemon ignores the second
}
