// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// newDispatchTestClient returns a daemon client whose outbound stream is the
// returned channel, so a test can watch for the reply to one command.
func newDispatchTestClient(t *testing.T) (*daemonClient, chan *reliantv1.DaemonMessage) {
	t.Helper()
	d := newTestDaemonClient("d-hol", "u-hol")
	d.bootCfg.DataDir = t.TempDir()
	d.fsWatchersByPr = make(map[string]context.CancelFunc)
	d.sendCh = make(chan *reliantv1.DaemonMessage, 256)
	d.sessionDone = make(chan struct{})
	t.Cleanup(func() { close(d.sessionDone) })
	return d, d.sendCh
}

func codePresenceCommand(t *testing.T, requestID, dir string) *reliantv1.ServerMessage {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"path": dir})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_DaemonCommand{
		DaemonCommand: &reliantv1.DaemonCommandRequest{
			RequestId: requestID, CommandType: "project.code_presence", Payload: payload, TimeoutMs: 5000,
		},
	}}
}

// awaitCommandReply waits for the DaemonCommandResponse to requestID,
// skipping any other traffic the session produces meanwhile.
func awaitCommandReply(t *testing.T, out <-chan *reliantv1.DaemonMessage, requestID string, within time.Duration, why string) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case msg := <-out:
			if resp := msg.GetDaemonCommandResponse(); resp != nil && resp.GetRequestId() == requestID {
				if !resp.GetSuccess() {
					t.Fatalf("command %s failed: %s", requestID, resp.GetErrorMessage())
				}
				return
			}
		case <-deadline:
			t.Fatal(why)
		}
	}
}

// A slow command must not delay a fast one sent after it. Every command runs
// on its own goroutine; this pins that, because a daemon that handled
// commands one at a time would make the greenfield probe on StartChat wait for
// whatever git push or forge deploy happened to be running for another chat.
func TestSlowDaemonCommandDoesNotDelayFastCommand(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	RegisterCommand("test.hol_slow", func(ctx context.Context, _ []byte) ([]byte, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return []byte(`{}`), nil
	})

	d, out := newDispatchTestClient(t)
	ctx := context.Background()

	slow := &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_DaemonCommand{
		DaemonCommand: &reliantv1.DaemonCommandRequest{RequestId: "slow-1", CommandType: "test.hol_slow", TimeoutMs: 30000},
	}}
	if err := d.handleServerMessage(ctx, slow); err != nil {
		t.Fatalf("dispatch slow: %v", err)
	}
	if err := d.handleServerMessage(ctx, codePresenceCommand(t, "fast-1", t.TempDir())); err != nil {
		t.Fatalf("dispatch fast: %v", err)
	}

	awaitCommandReply(t, out, "fast-1", time.Second,
		"the fast command's reply waited behind an unrelated slow command")
}

// The registration handshake must not hold up commands.
//
// On every (re)connect the gateway's RegistrationAck names every project the
// user has (16 on the machine this was measured on), and the daemon answered
// each with a full config snapshot — skill discovery included, 4.9s cold for a
// single multi-repo project — INSIDE the stream's receive loop. Nothing behind
// the ack was even dispatched until all of them finished, so every command
// sent in the seconds after a reconnect (a laptop waking, a gateway rollout)
// queued behind discovery it had nothing to do with.
func TestRegistrationAckDoesNotBlockCommandDispatch(t *testing.T) {
	d, out := newDispatchTestClient(t)

	discoveryStarted := make(chan struct{}, 4)
	releaseDiscovery := make(chan struct{})
	defer close(releaseDiscovery)
	d.buildSnapshot = func(string) (*reliantv1.ProjectConfigSnapshot, error) {
		discoveryStarted <- struct{}{}
		<-releaseDiscovery
		return &reliantv1.ProjectConfigSnapshot{}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ack := &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_RegistrationAck{
		RegistrationAck: &reliantv1.RegistrationAck{
			Accepted:              true,
			RequestedProjectPaths: []string{t.TempDir()},
		},
	}}

	handled := make(chan struct{})
	go func() {
		_ = d.handleServerMessage(ctx, ack)
		close(handled)
	}()
	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("the receive loop is stuck building project snapshots for the RegistrationAck; " +
			"no command behind it can be dispatched until discovery finishes")
	}

	// Discovery is still running (released only at test end). A command
	// arriving now must be answered anyway.
	select {
	case <-discoveryStarted:
	case <-time.After(time.Second):
		t.Fatal("the registration ack never requested a project snapshot")
	}
	if err := d.handleServerMessage(ctx, codePresenceCommand(t, "after-ack", t.TempDir())); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	awaitCommandReply(t, out, "after-ack", time.Second,
		"a command sent right after registration waited for project discovery")
}
