package daemonruntime

import (
	"context"
	"testing"
	"time"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

func TestRestartCommandIgnoringCancellationCannotDeclareDrainedOrLateSuccess(t *testing.T) {
	d, s, fire := restartTestClient(t)
	entered, release := make(chan struct{}), make(chan struct{})
	RegisterCommand("test.restart_uncooperative", func(context.Context, []byte) ([]byte, error) {
		close(entered)
		<-release
		return []byte(`{"success":true}`), nil
	})
	require.NoError(t, d.handleServerMessage(context.Background(), s, &pb.ServerMessage{Message: &pb.ServerMessage_DaemonCommand{DaemonCommand: &pb.DaemonCommandRequest{RequestId: "running", CommandType: "test.restart_uncooperative"}}}))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("command never started")
	}
	d.beginRestart(context.Background(), &pb.RestartRequested{Reason: "test"})
	<-s.sendCh
	fire()
	require.False(t, d.restartSnapshot().Drained, "cancellation is not proof an uncooperative command stopped")
	close(release)
	deadline := time.After(time.Second)
	var failed, drained bool
	for !failed || !drained {
		select {
		case msg := <-s.sendCh:
			if response := msg.GetDaemonCommandResponse(); response != nil {
				require.False(t, response.Success)
				require.Empty(t, response.Payload)
				require.Contains(t, response.ErrorMessage, "outcome unknown")
				failed = true
			}
			if status := msg.GetRestartStatus(); status != nil && status.Drained {
				drained = true
			}
		case <-deadline:
			t.Fatal("command termination did not unblock interrupted response and drain")
		}
	}
}

func TestRestartDeadlineFencesScheduledCommandBeforeRegistration(t *testing.T) {
	d, s, fire := restartTestClient(t)
	// Scheduler barrier: admission already counted the command, but its
	// goroutine has not entered handleDaemonCommand or registered cancellation.
	d.restartCommands = 1
	s.inFlight.Add(1)
	d.beginRestart(context.Background(), &pb.RestartRequested{Reason: "test"})
	<-s.sendCh
	fire()
	require.False(t, d.restartSnapshot().Drained, "queued/running commands are not stopped by cancelling an empty map")
	d.handleDaemonCommand(s, &pb.DaemonCommandRequest{RequestId: "delayed", CommandType: "fs.write_file", Payload: []byte(`{}`)})
	select {
	case msg := <-s.sendCh:
		if msg.GetRestartStatus() != nil {
			msg = <-s.sendCh
		}
		resp := msg.GetDaemonCommandResponse()
		require.NotNil(t, resp)
		require.False(t, resp.Success)
		require.Contains(t, resp.ErrorMessage, "deadline expired")
	case <-time.After(time.Second):
		t.Fatal("delayed command did not return interruption")
	}
	require.Empty(t, d.cancelByReq, "expired command cannot install a fresh cancellation context")
	d.restartMu.Lock()
	d.restartCommands--
	d.restartProgressLocked()
	d.restartMu.Unlock()
	require.True(t, d.restartSnapshot().Drained)
}
