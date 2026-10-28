package tools_daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/require"
)

type blockingRestartPublisher struct {
	entered chan struct{}
	release chan error
}

func (p *blockingRestartPublisher) PublishToolResult(context.Context, string, string, *toolexec.ToolExecutionResponse) error {
	close(p.entered)
	return <-p.release
}

func TestRestartLocalWaiterIsNotDurableDelivery(t *testing.T) {
	for _, success := range []bool{false, true} {
		t.Run(map[bool]string{false: "publish failure", true: "confirmed publication"}[success], func(t *testing.T) {
			svc := newTestService(nil)
			p := &blockingRestartPublisher{make(chan struct{}), make(chan error)}
			svc.deps.ResultPublisher = p
			conn := newTestConn("user", "daemon", newParkedStream())
			waiter := make(chan *toolexec.ToolExecutionResponse, 1)
			conn.pendingToolRequests = map[string]chan *toolexec.ToolExecutionResponse{"running": waiter}
			svc.routeToolResponse(conn, &pb.ToolResponse{RequestId: "running", Success: true, Content: "durable outcome"})
			require.Equal(t, "durable outcome", (<-waiter).Content)
			select {
			case <-p.entered:
			case <-time.After(time.Second):
				t.Fatal("local waiter bypassed durable publication")
			}
			require.Empty(t, conn.sendCh, "buffered waiter cannot authorize durable deletion or drain readiness")
			if success {
				p.release <- nil
				select {
				case msg := <-conn.sendCh:
					require.Equal(t, "running", msg.GetToolResultAck().GetRequestId())
				case <-time.After(time.Second):
					t.Fatal("confirmed publication not acknowledged")
				}
			} else {
				p.release <- errors.New("gateway publication failed")
			}
		})
	}
}

func TestRestartNATSRetainsStatusRecoveryNotNewWork(t *testing.T) {
	cluster := newRolloutCluster(t)
	svc := cluster.replica()
	stream, ok := attach(t, svc, "user", "daemon")
	require.True(t, ok)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := svc.RequestDaemonRestart(ctx, "user", "daemon", &pb.RestartRequested{Reason: "test"})
	require.NoError(t, err)
	require.NotNil(t, (<-stream.toDaemon).GetRestartRequested())
	nc := cluster.connect()
	_, err = nc.RequestWithContext(ctx, "tools.online.user.daemon", nil)
	require.ErrorIs(t, err, nats.ErrNoResponders, "updating must not advertise new-work availability")
	recovery, err := nc.RequestWithContext(ctx, "tools.recovery.user.daemon", nil)
	require.NoError(t, err)
	require.Equal(t, "true", string(recovery.Data))
	done := make(chan error, 1)
	go func() {
		_, err := cluster.worker("daemon").SendDaemonCommand(ctx, "user", toolexec.ToolStatusCommand, []byte(`{"request_id":"held"}`), 1000)
		done <- err
	}()
	select {
	case msg := <-stream.toDaemon:
		command := msg.GetDaemonCommand()
		require.NotNil(t, command)
		require.Equal(t, toolexec.ToolStatusCommand, command.CommandType)
		stream.fromDaemon <- &pb.DaemonMessage{Message: &pb.DaemonMessage_DaemonCommandResponse{DaemonCommandResponse: &pb.DaemonCommandResponse{RequestId: command.RequestId, Success: true, Payload: []byte(`{"state":"completed"}`)}}}
	case <-ctx.Done():
		t.Fatal("updating daemon lost tool.status route")
	}
	require.NoError(t, <-done)
	// The status-triggered resend uses the real result subject while ordinary
	// tool admission remains withdrawn. Its ack follows publication.
	inbox, err := nc.SubscribeSync("tools.result.user.daemon.held")
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	stream.fromDaemon <- &pb.DaemonMessage{Message: &pb.DaemonMessage_ToolResponse{ToolResponse: &pb.ToolResponse{RequestId: "held", Success: true, Content: "recovered while updating"}}}
	result, err := inbox.NextMsgWithContext(ctx)
	require.NoError(t, err)
	require.Contains(t, string(result.Data), "recovered while updating")
	select {
	case msg := <-stream.toDaemon:
		require.Equal(t, "held", msg.GetToolResultAck().GetRequestId())
	case <-ctx.Done():
		t.Fatal("recovered result was not acknowledged")
	}
}

func TestRestartRequiresDurableLeaseAndRetriesRepeatedObservation(t *testing.T) {
	missing := true
	writes := 0
	svc := newTestService(&MockRepository{SetDaemonRestartStatusFunc: func(_ context.Context, _, _ string, _ time.Time, data string) error {
		if missing {
			return sql.ErrNoRows
		}
		writes++
		var s pb.DaemonRestartStatus
		return json.Unmarshal([]byte(data), &s)
	}})
	conn := newTestConn("user", "daemon", newParkedStream())
	svc.connections["daemon"] = conn
	_, err := svc.RequestDaemonRestart(context.Background(), "user", "daemon", &pb.RestartRequested{Reason: "test"})
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.Nil(t, conn.restart.Load(), "nondurable intent must not withdraw routing")
	require.Empty(t, conn.sendCh)
	missing = false
	status, err := svc.RequestDaemonRestart(context.Background(), "user", "daemon", &pb.RestartRequested{Reason: "test"})
	require.NoError(t, err)
	require.Equal(t, 1, writes)
	missing = true
	require.ErrorIs(t, svc.recordRestart(context.Background(), conn, status), sql.ErrNoRows)
	missing = false
	require.NoError(t, svc.recordRestart(context.Background(), conn, status))
	require.Equal(t, 2, writes, "same observation must repair recreated lease")
}
