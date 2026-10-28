package daemonruntime

import (
	"context"
	"testing"
	"time"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/require"
)

// No wall-clock timers or sleeps: the test owns the deadline callback. Queue
// reads are synchronous except for a bounded failure guard on asynchronous work.
func restartTestClient(t *testing.T) (*daemonClient, *gatewaySession, func()) {
	t.Helper()
	d := &daemonClient{cancelByReq: make(map[string]context.CancelFunc)}
	s := useTestSession(d, 64)
	var fire func()
	d.restartAfter = func(wait time.Duration, fn func()) func() bool {
		require.LessOrEqual(t, wait, toolexec.DaemonDrainTimeout)
		fire = fn
		return func() bool { return true }
	}
	return d, s, func() { require.NotNil(t, fire); fire() }
}

func TestDaemonRestartDrainWithoutCalls(t *testing.T) {
	d, s, fire := restartTestClient(t)
	d.beginRestart(context.Background(), &pb.RestartRequested{Reason: "test"})
	require.False(t, (<-s.sendCh).GetRestartStatus().Drained)
	status := (<-s.sendCh).GetRestartStatus()
	require.True(t, status.Drained)
	require.False(t, status.TimedOut)
	fire() // a timer already firing when stopped is harmless
	require.False(t, d.restartSnapshot().TimedOut)
	require.Empty(t, s.sendCh)
}

func TestDaemonRestartWaitsForCallAndItsAcknowledgement(t *testing.T) {
	d, s, _ := restartTestClient(t)
	d.results.start("running")
	s.inFlight.Add(1)
	d.beginRestart(context.Background(), &pb.RestartRequested{Reason: "test"})
	require.False(t, (<-s.sendCh).GetRestartStatus().Drained)
	require.False(t, d.restartSnapshot().Drained)
	d.sendToolResult(s, "running", finished("done"))
	require.Equal(t, "done", sentToolResponse(t, s).Content)
	require.False(t, d.restartSnapshot().Drained, "finishing is not delivery")
	d.handleToolResultAck("running")
	require.True(t, (<-s.sendCh).GetRestartStatus().Drained)
	require.Zero(t, s.inFlight.Load())
}

func TestDaemonRestartTimeoutPersistsB1ResultAndRejectsLateCompletion(t *testing.T) {
	d, s, fire := restartTestClient(t)
	store, err := openOutboxStore(t.TempDir())
	require.NoError(t, err)
	d.results.restore(store)
	d.results.start("running")
	d.results.persistRunning("running")
	cancelled := make(chan struct{})
	d.registerCancel("running", func() { close(cancelled) })
	d.beginRestart(context.Background(), &pb.RestartRequested{Reason: "deadline"})
	<-s.sendCh
	fire()
	<-cancelled
	status := d.restartSnapshot()
	require.True(t, status.Drained)
	require.True(t, status.TimedOut)
	// A tool ignoring cancellation and returning late cannot replace the verdict.
	d.sendToolResult(s, "running", finished("late success"))
	held := d.results.matching(func(*heldResult) bool { return true })
	require.Len(t, held, 1)
	require.Equal(t, toolexec.ErrorCodeDaemonRestarted, held[0].msg.GetToolResponse().ErrorCode)
	var replacement resultOutbox
	replacement.restore(store)
	require.Equal(t, toolexec.ToolRequestCompleted, replacement.state("running"))
	require.Equal(t, toolexec.ErrorCodeDaemonRestarted, replacement.matching(func(*heldResult) bool { return true })[0].msg.GetToolResponse().ErrorCode)
}

func TestDaemonRestartRejectsNewWorkAndReadvertisesOnGatewayReconnect(t *testing.T) {
	d, s, _ := restartTestClient(t)
	d.beginRestart(context.Background(), &pb.RestartRequested{Reason: "test"})
	<-s.sendCh
	<-s.sendCh
	require.NoError(t, d.handleServerMessage(context.Background(), s, &pb.ServerMessage{Message: &pb.ServerMessage_ToolRequest{ToolRequest: &pb.ToolRequest{RequestId: "new", ToolName: "shell"}}}))
	require.Equal(t, toolexec.ErrorCodeDaemonUndelivered, sentToolResponse(t, s).ErrorCode)
	require.Equal(t, toolexec.ToolRequestUnknown, d.results.state("new"), "rejected calls never acquire a running marker")
	second := newTestSession(16)
	d.activateSession(second)
	require.True(t, (<-second.sendCh).GetRestartStatus().Drained)
	require.Equal(t, "test", d.restartSnapshot().Reason)
	replacement := &daemonClient{}
	require.Nil(t, replacement.restartSnapshot(), "only a new daemon process becomes routable")
}

func TestDaemonRestartWaitsForHeldResultsRestoredByB1(t *testing.T) {
	d, s, _ := restartTestClient(t)
	d.results.hold("previous", interruptedByRestartResponse("previous"), nil)
	d.beginRestart(context.Background(), &pb.RestartRequested{Reason: "test"})
	require.False(t, (<-s.sendCh).GetRestartStatus().Drained)
	d.handleToolResultAck("previous")
	require.True(t, (<-s.sendCh).GetRestartStatus().Drained)
}
