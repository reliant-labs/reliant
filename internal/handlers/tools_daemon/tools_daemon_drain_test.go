// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonactivity"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Close must ACTIVELY end every open daemon stream with Unavailable.
//
// The defect it pins: Close was a no-op, and the gateway's stop's only other
// step was http.Server.Shutdown, which waits for active requests to finish.
// A daemon bidi stream never finishes on its own, so Shutdown always burned
// the full shutdown budget and the process then exited with every daemon's TCP
// connection cut — no status, and dead time for the whole budget on every
// gateway rollout.
//
// With Close ending streams, the handler returns promptly with Unavailable and
// the daemon redials immediately (see isFatalError in
// internal/toolexec/daemonruntime/runtime.go: Unavailable is recoverable).
func TestCloseEndsOpenDaemonStreamsWithUnavailable(t *testing.T) {
	svc := newTestService(nil)

	stream := newParkedStream()
	conn := newTestConn("user-1", "daemon-1", stream)

	svc.mu.Lock()
	svc.connections[conn.daemonID] = conn
	svc.userDaemons[conn.userID] = []string{conn.daemonID}
	svc.mu.Unlock()

	// handleIncoming is what the Connect handler blocks in for the life of
	// the stream. Its return value is the status the daemon receives.
	handlerErr := make(chan error, 1)
	go func() { handlerErr <- svc.handleIncoming(context.Background(), conn) }()

	// The stream is parked in Receive, exactly like a healthy idle daemon
	// between heartbeats.
	select {
	case err := <-handlerErr:
		t.Fatalf("handler returned before shutdown: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	svc.Close()

	select {
	case err := <-handlerErr:
		require.Error(t, err, "Close must end the stream with a status, not a nil return")
		require.Equal(t, connect.CodeUnavailable, svcerr.Code(err),
			"the daemon must see Unavailable so it redials to a live replica; "+
				"got %v", err)
		require.Contains(t, err.Error(), "draining")
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not end the open daemon stream — the handler is still parked in Receive, " +
			"which is what made gateway shutdown burn its entire budget")
	}
}

// Unavailable must stay on the daemon's reconnect path. If it were ever
// classified terminal, a gateway rollout would permanently disconnect every
// daemon instead of moving them to the surge pod.
func TestDrainingErrorIsRecoverableForDaemons(t *testing.T) {
	require.Equal(t, connect.CodeUnavailable, svcerr.Code(errGatewayDraining))
	require.NotEqual(t, connect.CodeAborted, svcerr.Code(errGatewayDraining),
		"Aborted is the supersede signal and is terminal — a draining gateway must not use it")
}

// The gateway's drain status is the shared drain sentinel, which forge's
// logging interceptor treats as expected (svcerr.ErrServerDraining). Every
// held daemon stream ends with it on every rollout; matched by anything else,
// each one was a "stream failed" record at ERROR and a Sentry event.
func TestDrainingErrorIsTheServerDrainingSentinel(t *testing.T) {
	require.True(t, svcerr.IsServerDraining(errGatewayDraining),
		"errGatewayDraining must match svcerr.ErrServerDraining, or every rollout pages once per connected daemon")
	require.Contains(t, errGatewayDraining.Error(), "gateway draining",
		"the daemon prints this message; it must still say what happened")
}

// Close with no connections must not panic or block; the gateway shuts down
// this way whenever no daemon happens to be attached.
func TestCloseWithNoConnectionsIsANoOp(t *testing.T) {
	svc := newTestService(nil)
	done := make(chan struct{})
	go func() { svc.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close blocked with no connections")
	}
}

// Close is idempotent: it runs from gateway shutdown while individual streams
// may already be ending on their own.
func TestCloseIsIdempotent(t *testing.T) {
	svc := newTestService(nil)
	conn := newTestConn("user-1", "daemon-1", newParkedStream())

	svc.mu.Lock()
	svc.connections[conn.daemonID] = conn
	svc.userDaemons[conn.userID] = []string{conn.daemonID}
	svc.mu.Unlock()

	svc.Close()
	svc.Close()

	require.Equal(t, connect.CodeUnavailable, svcerr.Code(conn.closedReason()),
		"the first Close's reason must survive the second")
}

// drainRecorder is a connection listener that hears the drain, and notes
// whether the daemon had already been told to move when it did.
type drainRecorder struct {
	conn       *daemonConnection
	drained    []string
	noticeSent []bool
}

func (r *drainRecorder) OnDaemonConnected(string, string)    {}
func (r *drainRecorder) OnDaemonDisconnected(string, string) {}
func (r *drainRecorder) OnDaemonDraining(userID, daemonID string) {
	r.drained = append(r.drained, userID+"/"+daemonID)
	r.noticeSent = append(r.noticeSent, len(r.conn.sendCh) > 0)
}

// drainReplica is the listener's drain reaching every stream the replica
// holds at once: what each ConnectDaemon stream's handOverOnDrain does when
// the soft phase begins, for a test that holds its connections without a
// listener. It reports how many daemons were told to move.
func drainReplica(svc *Service, deadline time.Time) int {
	svc.draining.Store(true)
	svc.mu.RLock()
	conns := make([]*daemonConnection, 0, len(svc.connections))
	for _, conn := range svc.connections {
		conns = append(conns, conn)
	}
	svc.mu.RUnlock()
	moved := 0
	for _, conn := range conns {
		if svc.handOver(conn, deadline) {
			moved++
		}
	}
	return moved
}

// The hand-over stops this replica being chosen for the daemon's new requests
// BEFORE it tells the daemon to move: told first, the daemon could register
// on another replica while this one still took its requests, and one request
// would reach it twice.
func TestHandOverStopsRoutingBeforeTellingTheDaemonToMove(t *testing.T) {
	svc := newTestService(nil)
	conn := newTestConn("user-1", "daemon-1", newParkedStream())
	conn.movesOnDrain = true
	rec := &drainRecorder{conn: conn}
	svc.AddConnectionListener(rec)
	svc.mu.Lock()
	registerTestConn(svc, conn)
	svc.mu.Unlock()

	deadline := time.Now().Add(20 * time.Second)
	require.Equal(t, 1, drainReplica(svc, deadline))

	require.Equal(t, []string{"user-1/daemon-1"}, rec.drained)
	require.Equal(t, []bool{false}, rec.noticeSent, "listeners must hear the drain before the daemon is told to move")

	msg := <-conn.sendCh
	require.NotNil(t, msg.GetDrain(), "the daemon must be told this replica is draining")
	require.Equal(t, deadline.UnixMilli(), msg.GetDrain().GetDeadlineUnixMs())
	require.True(t, conn.drainNoticeSent.Load())

	select {
	case <-conn.done:
		t.Fatal("the stream must stay open: replies to requests in flight on it still come here")
	default:
	}
}

// A daemon that cannot move — an older build, or one the gateway dialed — is
// ended at once, as before: kept open it would only be unreachable until the
// deadline, because this replica no longer routes its requests.
func TestHandOverEndsAtOnceTheStreamsOfDaemonsThatCannotMove(t *testing.T) {
	svc := newTestService(nil)
	old := newTestConn("user-1", "daemon-old", newParkedStream())
	current := newTestConn("user-1", "daemon-new", newParkedStream())
	current.movesOnDrain = true
	svc.mu.Lock()
	registerTestConn(svc, old)
	registerTestConn(svc, current)
	svc.mu.Unlock()

	require.Equal(t, 1, drainReplica(svc, time.Now().Add(20*time.Second)), "only the daemon that can move is told to")
	select {
	case <-old.done:
		require.Equal(t, connect.CodeUnavailable, svcerr.Code(old.closedReason()))
	default:
		t.Fatal("a daemon that cannot move must be ended at once, so it redials straight away")
	}
	require.False(t, old.drainNoticeSent.Load())
	require.NotNil(t, (<-current.sendCh).GetDrain())
}

// The listener's drain is what hands a daemon over: the stream's soft phase
// fires handOverOnDrain, and the notice names the moment that drain will end
// the stream — the registry's own hard deadline (serverkit.DrainDeadline),
// not a re-derivation of it.
func TestListenerDrainHandsTheDaemonOverWithTheDrainsDeadline(t *testing.T) {
	svc := newTestService(nil)
	conn := newTestConn("user-1", "daemon-1", newParkedStream())
	conn.movesOnDrain = true

	streams := serverkit.NewStreams()
	ctx, release := streams.Bind(context.Background())
	defer release()
	_ = serverkit.Draining(ctx) // ConnectDaemon claims the soft phase first
	stop := svc.handOverOnDrain(ctx, conn)
	defer stop()

	budget, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	drained := make(chan error, 1)
	go func() { drained <- streams.Drain(budget) }()

	var notice *reliantv1.GatewayDrain
	select {
	case msg := <-conn.sendCh:
		notice = msg.GetDrain()
	case <-time.After(2 * time.Second):
		t.Fatal("the listener's drain never told the daemon to move")
	}
	require.NotNil(t, notice)
	hard, ok := serverkit.DrainDeadline(ctx)
	require.True(t, ok)
	require.Equal(t, hard.UnixMilli(), notice.GetDeadlineUnixMs(), "the notice names the drain's own hard deadline")
	require.True(t, svc.draining.Load(), "a replica that has begun handing daemons over refuses new ones")

	// The daemon moved and retired its stream: the drain is done.
	release()
	require.NoError(t, <-drained)
}

// A stream that arrives once the listener has begun draining is refused at
// once, before it is registered.
func TestStreamArrivingDuringTheListenerDrainIsRefused(t *testing.T) {
	svc := newTestService(nil)
	streams := serverkit.NewStreams()
	streams.BeginDrain()
	ctx, release := streams.Bind(context.Background())
	defer release()

	err := svc.ConnectDaemon(ctx, nil)
	require.Equal(t, connect.CodeUnavailable, svcerr.Code(err))
	require.True(t, svcerr.IsServerDraining(err))
}

// A draining replica takes no new daemon, in either direction: the daemon
// must land on a replica that will still be serving it a minute from now.
func TestDrainingReplicaRefusesNewDaemonStreams(t *testing.T) {
	svc := newTestService(nil)
	drainReplica(svc, time.Now().Add(time.Second))

	err := svc.ConnectDaemon(context.Background(), nil)
	require.Equal(t, connect.CodeUnavailable, svcerr.Code(err), "Unavailable keeps the daemon on its reconnect path")
	require.True(t, svcerr.IsServerDraining(err))

	_, err = svc.RegisterOutboundConnection(context.Background(), "user-1", "daemon-1", "10.0.0.1", 9190, &reliantv1.DaemonRegister{}, nil, nil)
	require.Equal(t, connect.CodeUnavailable, svcerr.Code(err))
}

// Once told to move, the daemon closes its old stream itself when it is done
// with it. That is the drain working, and it must end like one.
func TestStreamTheDaemonClosesAfterTheDrainNoticeEndsAsADrain(t *testing.T) {
	svc := newTestService(nil)
	stream := newParkedStream()
	conn := newTestConn("user-1", "daemon-1", stream)
	conn.drainNoticeSent.Store(true)

	errCh := make(chan error, 1)
	go func() { errCh <- svc.handleIncoming(context.Background(), conn) }()
	close(stream.recv)

	select {
	case err := <-errCh:
		require.True(t, svcerr.IsServerDraining(err), "got %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("handleIncoming did not return when the daemon closed its stream")
	}
}

// fakeResultPublisher records the results a gateway publishes for waiters
// elsewhere.
type fakeResultPublisher struct {
	mu        sync.Mutex
	published []*toolexec.ToolExecutionResponse
	err       error
}

func (p *fakeResultPublisher) PublishToolResult(_ context.Context, userID, daemonID string, resp *toolexec.ToolExecutionResponse) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.published = append(p.published, resp)
	return nil
}

func ackFor(t *testing.T, conn *daemonConnection, within time.Duration) string {
	t.Helper()
	timeout := time.After(within)
	for {
		select {
		case msg := <-conn.sendCh:
			if ack := msg.GetToolResultAck(); ack != nil {
				return ack.RequestId
			}
		case <-timeout:
			return ""
		}
	}
}

// Even the local waiter must acquire confirmed publication ownership before
// the daemon can delete its durable copy.
func TestToolResultIsAcknowledgedAfterLocalWaiterPublication(t *testing.T) {
	svc := newTestService(nil)
	pub := &fakeResultPublisher{}
	svc.deps.ResultPublisher = pub
	conn := newTestConn("user-1", "daemon-1", newParkedStream())
	waiter := make(chan *toolexec.ToolExecutionResponse, 1)
	conn.pendingToolRequests["req-1"] = waiter

	svc.routeToolResponse(conn, &reliantv1.ToolResponse{RequestId: "req-1", Success: true, Content: "ok"})

	got := <-waiter
	require.Equal(t, "ok", got.Content)
	require.Equal(t, "daemon-1", got.DaemonID, "which machine ran it is the gateway's to say")
	require.Equal(t, "req-1", ackFor(t, conn, time.Second))
	pub.mu.Lock()
	require.Len(t, pub.published, 1, "local buffering alone never authorizes deletion")
	pub.mu.Unlock()
}

// Redelivery: a result whose request came in over another stream has no
// waiter here. It is published to the request's result subject, and
// acknowledged only after that — a result that could not be published stays
// with the daemon, which sends it again.
func TestToolResultWithNoWaiterIsPublishedThenAcknowledged(t *testing.T) {
	svc := newTestService(nil)
	pub := &fakeResultPublisher{}
	svc.deps.ResultPublisher = pub
	conn := newTestConn("user-1", "daemon-1", newParkedStream())

	svc.routeToolResponse(conn, &reliantv1.ToolResponse{RequestId: "req-2", Success: true, Content: "late"})
	require.Equal(t, "req-2", ackFor(t, conn, 5*time.Second))
	pub.mu.Lock()
	require.Len(t, pub.published, 1)
	require.Equal(t, "late", pub.published[0].Content)
	require.Equal(t, "daemon-1", pub.published[0].DaemonID)
	pub.mu.Unlock()

	pub.err = errors.New("nats unavailable")
	svc.routeToolResponse(conn, &reliantv1.ToolResponse{RequestId: "req-3", Success: true})
	require.Empty(t, ackFor(t, conn, 200*time.Millisecond), "an unpublished result must not be acknowledged")
}

// A call that outlived a drained replica's stream is still running, and this
// replica never sent it; the daemon's own count keeps the machine awake.
func TestDaemonReportedToolCallsKeepTheMachineAwake(t *testing.T) {
	svc, conn, _, rec := newActivityFixture(t)
	conn.daemonToolCallsInFlight.Store(1)
	svc.reassertInFlightActivity(conn)
	require.Equal(t, daemonactivity.ReasonToolCallInFlight, rec.waitFor(t).Reason)
}
