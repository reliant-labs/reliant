package tools_daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/pkg/daemonwire"
	"github.com/stretchr/testify/require"
)

func TestDaemonRestartStopsNewRoutingButDeliversInFlightResult(t *testing.T) {
	cluster := newRolloutCluster(t)
	svc := cluster.replica()
	stream, ok := attach(t, svc, "user", "daemon")
	require.True(t, ok)
	// A persisted observation is also what a worker joining mid-drain reads.
	records := &toolexec.MockDaemonRecords{ListDaemonsByUserIDFunc: func(context.Context, string) ([]*db.Daemon, error) {
		conn, err := svc.connForUser("user", "daemon")
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(conn.restart.Load())
		return []*db.Daemon{{ID: "daemon", UserID: "user", RestartStatus: string(data)}}, err
	}}
	worker := toolexec.NewNATSDaemonRouter(cluster.connect(), toolexec.WithResolver(fixedDaemon("daemon")), toolexec.WithDatabase(records))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := make(chan *toolexec.ToolExecutionResponse, 1)
	failure := make(chan error, 1)
	go func() {
		resp, err := worker.SendToolRequestSync(ctx, "user", &toolexec.ToolExecutionRequest{RequestID: "running", ToolName: "shell"})
		result <- resp
		failure <- err
	}()
	select {
	case msg := <-stream.toDaemon:
		require.Equal(t, "running", msg.GetToolRequest().GetRequestId())
	case <-ctx.Done():
		t.Fatal("tool did not reach daemon")
	}
	status, err := worker.RequestDaemonRestart(ctx, "user", "daemon", &pb.RestartRequested{Reason: "admin test"})
	require.NoError(t, err)
	require.False(t, status.Drained)
	select {
	case msg := <-stream.toDaemon:
		require.Equal(t, status.DeadlineUnixMs, msg.GetRestartRequested().GetDeadlineUnixMs())
	case <-ctx.Done():
		t.Fatal("restart notice missing")
	}
	// RequestDaemonRestart returns only after OnDaemonDraining's NATS flush.
	// A new request has no responder, not a timeout or an interrupted outcome.
	nc := cluster.connect()
	_, err = nc.RequestWithContext(ctx, "tools.request.sync.user.daemon", []byte(`{"request_id":"new","tool_name":"shell"}`))
	require.ErrorIs(t, err, nats.ErrNoResponders)
	// A callback that was selected BEFORE unsubscribe is refused at the
	// admission seam too, with authoritative non-delivery, not interruption.
	_, err = svc.SendToolRequestSync(ctx, "user", "daemon", &toolexec.ToolExecutionRequest{RequestID: "late", ToolName: "shell"})
	require.ErrorIs(t, err, toolexec.ErrDaemonUpdating)
	stream.fromDaemon <- &pb.DaemonMessage{Message: &pb.DaemonMessage_ToolResponse{ToolResponse: &pb.ToolResponse{RequestId: "running", Success: true, Content: "finished"}}}
	select {
	case resp := <-result:
		require.Equal(t, "finished", resp.Content)
		require.NoError(t, <-failure)
	case <-ctx.Done():
		t.Fatal("in-flight result lost during drain")
	}
	select {
	case msg := <-stream.toDaemon:
		require.Equal(t, "running", msg.GetToolResultAck().GetRequestId())
	case <-ctx.Done():
		t.Fatal("result ack missing during drain")
	}
	repeated, err := worker.RequestDaemonRestart(ctx, "user", "daemon", &pb.RestartRequested{Reason: "duplicate", DeadlineUnixMs: time.Now().Add(time.Hour).UnixMilli()})
	require.NoError(t, err)
	require.Equal(t, status.DeadlineUnixMs, repeated.DeadlineUnixMs)
	require.False(t, svc.draining.Load(), "draining one daemon never drains its gateway")
	// A worker's side-effectful request sent during drain waits, never executes
	// here, and is re-sent once a fresh process is attached on another replica.
	waiting := make(chan struct{}, 1)
	waitCtx := toolexec.WithDaemonReconnectWait(ctx, func(string) func(bool) { waiting <- struct{}{}; return nil })
	go func() {
		resp, err := worker.SendToolRequestSync(waitCtx, "user", &toolexec.ToolExecutionRequest{RequestID: "after-restart", ToolName: "shell"})
		result <- resp
		failure <- err
	}()
	select {
	case <-waiting:
	case <-ctx.Done():
		t.Fatal("new call did not wait through the drain")
	}
	nextReplica := cluster.replica()
	next, ok := attach(t, nextReplica, "user", "daemon")
	require.True(t, ok)
	// Prove subscriptions are installed before announcing reconnect, matching
	// connect's ordered notification. No probe-interval sleep is needed.
	require.NoError(t, nc.Flush())
	event, err := json.Marshal(daemonwire.Event{DaemonID: "daemon", UserID: "user", Type: daemonwire.EventConnected, At: time.Now()})
	require.NoError(t, err)
	require.NoError(t, nc.Publish(daemonwire.Subject("daemon", daemonwire.EventConnected), event))
	require.NoError(t, nc.Flush())
	select {
	case msg := <-next.toDaemon:
		require.Equal(t, "after-restart", msg.GetToolRequest().GetRequestId())
	case <-ctx.Done():
		t.Fatal("undelivered request did not reach replacement")
	}
	next.fromDaemon <- &pb.DaemonMessage{Message: &pb.DaemonMessage_ToolResponse{ToolResponse: &pb.ToolResponse{RequestId: "after-restart", Success: true, Content: "replacement"}}}
	select {
	case resp := <-result:
		require.Equal(t, "replacement", resp.Content)
		require.NoError(t, <-failure)
	case <-ctx.Done():
		t.Fatal("replacement result missing")
	}
}

func TestDaemonRestartRegistrationRetainsOnlySameProcessDrain(t *testing.T) {
	svc := newTestService(nil)
	conn := newTestConn("user", "daemon", newParkedStream())
	status := &pb.DaemonRestartStatus{DeadlineUnixMs: time.Now().Add(time.Minute).UnixMilli(), Reason: "test", Drained: true, IncarnationId: conn.incarnationID}
	require.NoError(t, svc.restoreRestart(context.Background(), conn, &pb.DaemonRegister{RestartStatus: status, IncarnationId: conn.incarnationID}))
	require.NotNil(t, conn.restart.Load())
	require.ErrorIs(t, svc.sendToConn(conn, &pb.ServerMessage{Message: &pb.ServerMessage_ToolRequest{ToolRequest: &pb.ToolRequest{RequestId: "new"}}}), toolexec.ErrDaemonUpdating)
	conn = newTestConn("user", "daemon", newParkedStream()) // a fresh process/connection
	require.NoError(t, svc.restoreRestart(context.Background(), conn, &pb.DaemonRegister{IncarnationId: conn.incarnationID}))
	require.Nil(t, conn.restart.Load())
	require.NoError(t, svc.sendToConn(conn, &pb.ServerMessage{Message: &pb.ServerMessage_ToolRequest{ToolRequest: &pb.ToolRequest{RequestId: "after"}}}))
	require.Equal(t, "after", (<-conn.sendCh).GetToolRequest().RequestId)
}

func TestDaemonRestartDrainedReportIsStoredBeforeReturn(t *testing.T) {
	var stored *pb.DaemonRestartStatus
	svc := newTestService(&MockRepository{SetDaemonRestartStatusFunc: func(_ context.Context, _, _ string, _ time.Time, data string) error {
		stored = &pb.DaemonRestartStatus{}
		return json.Unmarshal([]byte(data), stored)
	}})
	conn := newTestConn("user", "daemon", newParkedStream())
	status := &pb.DaemonRestartStatus{DeadlineUnixMs: 1234, Reason: "test", IncarnationId: conn.incarnationID}
	conn.restart.Store(status)
	require.NoError(t, svc.recordRestart(context.Background(), conn, &pb.DaemonRestartStatus{DeadlineUnixMs: 1234, Reason: "test", Drained: true, TimedOut: true, IncarnationId: conn.incarnationID}))
	require.True(t, stored.Drained)
	require.True(t, stored.TimedOut)
}
