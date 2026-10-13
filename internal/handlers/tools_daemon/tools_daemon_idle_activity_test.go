// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonactivity"
	"github.com/reliant-labs/reliant/internal/daemonstate"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/pkg/daemonwire"
)

// The activity events below are what the control plane turns into
// Workspace.Status.LastActivity, the clock a cloud machine's idle suspend
// runs on. These tests pin which traffic may restart that clock.

// activityRecorder subscribes to one daemon's activity subject on an embedded
// NATS server and records every event published there.
type activityRecorder struct {
	nc *nats.Conn

	mu     sync.Mutex
	events []daemonwire.Event
}

func newActivityRecorder(t *testing.T, daemonID string) *activityRecorder {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	srv := natstest.RunServer(&opts)
	t.Cleanup(srv.Shutdown)
	require.True(t, srv.ReadyForConnections(2*time.Second))
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	r := &activityRecorder{nc: nc}
	sub, err := nc.Subscribe(daemonwire.Subject(daemonID, daemonwire.EventActivity), func(msg *nats.Msg) {
		evt, err := daemonwire.UnmarshalEvent(msg.Data)
		if err != nil {
			t.Errorf("unmarshal activity event: %v", err)
			return
		}
		r.mu.Lock()
		r.events = append(r.events, evt)
		r.mu.Unlock()
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	require.NoError(t, nc.Flush())
	return r
}

// recorded returns the events seen once the publish side has had time to
// deliver; quiet is how long to wait for anything at all.
func (r *activityRecorder) recorded(t *testing.T, quiet time.Duration) []daemonwire.Event {
	t.Helper()
	require.NoError(t, r.nc.Flush())
	time.Sleep(quiet)
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]daemonwire.Event(nil), r.events...)
}

// waitFor returns the first recorded event, failing if none arrives.
func (r *activityRecorder) waitFor(t *testing.T) daemonwire.Event {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		if len(r.events) > 0 {
			evt := r.events[0]
			r.mu.Unlock()
			return evt
		}
		r.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no activity event was published; this work must keep the machine awake")
	return daemonwire.Event{}
}

func newActivityFixture(t *testing.T) (*Service, *daemonConnection, *parkedStream, *activityRecorder) {
	t.Helper()
	daemonID := uuid.NewString()
	rec := newActivityRecorder(t, daemonID)
	svc := newTestService(nil)
	svc.deps.StatePublisher = daemonstate.NewPublisher(rec.nc)
	stream := newParkedStream()
	conn := newTestConn("user-1", daemonID, stream)
	conn.daemonType = "managed"
	return svc, conn, stream, rec
}

// The daemon's own traffic is not activity. On 2026-10-11 a prod machine
// stayed up for 3h44m with nobody on it because the server's reconciler
// polled it every 30s (exec.bg_status) and the gateway counted every reply as
// activity — as it did PTY output from an open tab's terminal reconnects and
// file-change notices. None of these may restart the idle clock.
func TestDaemonRepliesAndOutputAreNotActivity(t *testing.T) {
	svc, conn, stream, rec := newActivityFixture(t)

	// The reconciler is waiting on its bg_status reply.
	respCh := make(chan *reliantv1.DaemonCommandResponse, 1)
	conn.pendingCommands["bg-status-poll"] = respCh

	ctx, cancel := context.WithCancel(context.Background())
	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		_ = svc.handleIncoming(ctx, conn)
	}()
	t.Cleanup(func() {
		cancel()
		close(stream.recv)
		<-handlerDone
	})

	stream.recv <- &reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_DaemonCommandResponse{
		DaemonCommandResponse: &reliantv1.DaemonCommandResponse{
			RequestId: "bg-status-poll", CommandType: "exec.bg_status", Success: true,
		},
	}}
	select {
	case <-respCh:
	case <-time.After(2 * time.Second):
		t.Fatal("the bg_status reply never reached its waiter")
	}
	stream.recv <- &reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_TerminalOutput{
		TerminalOutput: &reliantv1.TerminalOutputMessage{SessionId: "s", Data: []byte("$ ")},
	}}
	stream.recv <- &reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_ProcessOutputChunk{
		ProcessOutputChunk: &reliantv1.ProcessOutputChunkMessage{ProcessId: "p", Data: "tick\n"},
	}}

	if events := rec.recorded(t, 300*time.Millisecond); len(events) != 0 {
		t.Fatalf("daemon replies and output published %d activity event(s) (first reason %q); "+
			"they would keep an unused machine awake forever", len(events), events[0].Reason)
	}
}

// Forwarding work a person or an agent asked for IS activity, and the event
// names it, so the control plane can log what kept the machine awake.
func TestForwardedWorkIsActivityWithReason(t *testing.T) {
	cases := []struct {
		name string
		msg  *reliantv1.ServerMessage
		want string
	}{
		{
			name: "agent tool call",
			msg: &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_ToolRequest{
				ToolRequest: &reliantv1.ToolRequest{RequestId: "r", ToolName: "shell"},
			}},
			want: "tool call shell",
		},
		{
			name: "terminal keystroke",
			msg: &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_TerminalInput{
				TerminalInput: &reliantv1.TerminalInputMessage{SessionId: "s", Data: []byte("ls\r")},
			}},
			want: "terminal input",
		},
		{
			name: "saving a file",
			msg: &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_DaemonCommand{
				DaemonCommand: &reliantv1.DaemonCommandRequest{RequestId: "r", CommandType: "fs.write_file"},
			}},
			want: "command fs.write_file",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, conn, stream, rec := newActivityFixture(t)
			go svc.runSender(conn)
			t.Cleanup(conn.closeDone)

			require.NoError(t, svc.sendToConn(conn, tc.msg))
			<-stream.sent

			evt := rec.waitFor(t)
			require.Equal(t, tc.want, evt.Reason)
			require.Equal(t, conn.userID, evt.UserID)
			require.Equal(t, "managed", evt.DaemonType)
		})
	}
}

// Server polls and session plumbing forwarded TO the daemon are not activity
// either: the reconciler's poll, an open tab re-creating its terminals and
// refreshing the file tree, and the gateway's own heartbeat.
func TestForwardedPollsAreNotActivity(t *testing.T) {
	svc, conn, stream, rec := newActivityFixture(t)
	go svc.runSender(conn)
	t.Cleanup(conn.closeDone)

	for _, msg := range []*reliantv1.ServerMessage{
		{Message: &reliantv1.ServerMessage_DaemonCommand{
			DaemonCommand: &reliantv1.DaemonCommandRequest{RequestId: "a", CommandType: "exec.bg_status"},
		}},
		{Message: &reliantv1.ServerMessage_DaemonCommand{
			DaemonCommand: &reliantv1.DaemonCommandRequest{RequestId: "b", CommandType: "terminal.create"},
		}},
		{Message: &reliantv1.ServerMessage_DaemonCommand{
			DaemonCommand: &reliantv1.DaemonCommandRequest{RequestId: "c", CommandType: "fs.get_tree"},
		}},
		{Message: &reliantv1.ServerMessage_Heartbeat{Heartbeat: &reliantv1.ServerHeartbeat{Timestamp: 1}}},
	} {
		require.NoError(t, svc.sendToConn(conn, msg))
		<-stream.sent
	}

	if events := rec.recorded(t, 300*time.Millisecond); len(events) != 0 {
		t.Fatalf("polls published %d activity event(s) (first reason %q)", len(events), events[0].Reason)
	}
}

// A tool call that runs longer than the idle timeout — a long test suite, a
// build — must keep the machine awake for its whole duration, not just at the
// moment it was sent. The gateway re-asserts on its heartbeat while the call
// is in flight, and stops once it returns.
func TestInFlightToolCallKeepsMachineAwake(t *testing.T) {
	svc, conn, _, rec := newActivityFixture(t)

	svc.reassertInFlightActivity(conn)
	require.Empty(t, rec.recorded(t, 200*time.Millisecond), "nothing in flight: nothing to keep awake")

	conn.pendingToolRequestsMu.Lock()
	conn.pendingToolRequests["long-test-run"] = make(chan *toolexec.ToolExecutionResponse, 1)
	conn.pendingToolRequestsMu.Unlock()

	svc.reassertInFlightActivity(conn)
	require.Equal(t, daemonactivity.ReasonToolCallInFlight, rec.waitFor(t).Reason)
}
