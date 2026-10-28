package toolexec

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/require"
)

func TestUpdatingDaemonExtendsGraceAndResendsOnlyUndeliveredRequest(t *testing.T) {
	// Last seen is outside the ordinary 90s grace. The persisted drain, not a
	// conveniently fresh heartbeat, is what makes this request wait.
	records := reconnectRecords(time.Now().Add(-5*time.Minute), nil)
	status := &pb.DaemonRestartStatus{DeadlineUnixMs: time.Now().Add(DaemonDrainTimeout).UnixMilli(), Reason: "test"}
	data, err := json.Marshal(status)
	require.NoError(t, err)
	records.daemons[0].RestartStatus = string(data)
	router, daemon := reconnectTestRouter(t, records)
	require.WithinDuration(t, time.UnixMilli(status.DeadlineUnixMs).Add(DaemonRestartBudget), router.reconnectDeadline(context.Background(), reconnectUser, reconnectDaemon, time.Now()), time.Millisecond)
	waiting := make(chan struct{}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = WithDaemonReconnectWait(ctx, func(string) func(bool) { waiting <- struct{}{}; return nil })
	type answer struct {
		resp *ToolExecutionResponse
		err  error
	}
	done := make(chan answer, 1)
	go func() {
		resp, err := router.SendToolRequestSync(ctx, reconnectUser, &ToolExecutionRequest{RequestID: "drain-request", ToolName: "shell", ToolInput: `{"command":"side effect"}`})
		done <- answer{resp, err}
	}()
	select {
	case <-waiting:
	case <-ctx.Done():
		t.Fatal("draining daemon did not get extended grace")
	}
	require.Empty(t, daemon.toolRequests(), "nothing executes while quiesced")
	// Reconnect is coordinated by the observer, not a guessed delay.
	daemon.connect()
	select {
	case out := <-done:
		require.NoError(t, out.err)
		require.NotNil(t, out.resp)
		require.True(t, out.resp.Success)
	case <-ctx.Done():
		t.Fatal("request not re-sent after replacement reconnect")
	}
	require.Len(t, daemon.toolRequests(), 1, "a side-effectful request executes exactly once")
}

func TestDaemonRestartGraceIsBoundedAndOwnerScoped(t *testing.T) {
	now := time.Now()
	records := &fakeDaemonRecords{daemons: []*db.Daemon{{ID: "machine", UserID: "owner", RestartStatus: `{"deadline_unix_ms":1,"drained":true}`}}}
	r := NewNATSDaemonRouter(nil, WithDatabase(records))
	require.Equal(t, now, r.reconnectDeadline(context.Background(), "owner", "machine", now), "a stale observation cannot extend grace")
	require.Zero(t, r.restartDeadline(context.Background(), "owner", "different"))
	require.Equal(t, dispatchUndelivered, classifyDispatchErr(ErrDaemonUpdating))
	require.Equal(t, ErrorCodeDaemonUndelivered, transportErrorCode(ErrDaemonUpdating))
}
