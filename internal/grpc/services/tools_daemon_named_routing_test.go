package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// namedRoutingFixture is one user with a local (laptop) and a cloud daemon
// connected to the SAME gateway replica, bridged onto an embedded NATS server.
// The default pick prefers the laptop, which is what used to receive every
// request that was addressed to the cloud daemon.
type namedRoutingFixture struct {
	nc            *nats.Conn
	svc           *ToolsDaemonService
	laptop, cloud *daemonConnection
}

const namedRoutingUser = "user-1"

func newNamedRoutingFixture(t *testing.T) *namedRoutingFixture {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	srv := natstest.RunServer(&opts)
	t.Cleanup(srv.Shutdown)
	require.True(t, srv.ReadyForConnections(2*time.Second))
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	svc := NewToolsDaemonService(nil)
	newConn := func(daemonType string) *daemonConnection {
		return &daemonConnection{
			userID:              namedRoutingUser,
			daemonID:            uuid.New().String(),
			daemonType:          daemonType,
			connectedAt:         time.Now(),
			sendCh:              make(chan *reliantv1.ServerMessage, 8),
			done:                make(chan struct{}),
			pendingCommands:     make(map[string]chan *reliantv1.DaemonCommandResponse),
			pendingToolRequests: make(map[string]chan *toolexec.ToolExecutionResponse),
		}
	}
	f := &namedRoutingFixture{nc: nc, svc: svc, laptop: newConn("local"), cloud: newConn("cloud")}
	svc.mu.Lock()
	registerTestConn(svc, f.cloud)
	registerTestConn(svc, f.laptop)
	svc.mu.Unlock()

	svc.mu.RLock()
	require.Equal(t, f.laptop.daemonID, svc.defaultDaemonForUser(namedRoutingUser).daemonID,
		"precondition: the default pick is the laptop")
	svc.mu.RUnlock()

	bridge := toolexec.NewNATSToolBridge(nc, nil, svc)
	t.Cleanup(func() { _ = bridge.Close() })
	bridge.OnDaemonConnected(namedRoutingUser, f.laptop.daemonID)
	bridge.OnDaemonConnected(namedRoutingUser, f.cloud.daemonID)
	require.NoError(t, nc.Flush())
	return f
}

func (f *namedRoutingFixture) subject(base string, c *daemonConnection) string {
	return base + "." + namedRoutingUser + "." + c.daemonID
}

// expectOnlyOn waits for one message on want and asserts none arrived on other.
func expectOnlyOn(t *testing.T, want, other *daemonConnection) *reliantv1.ServerMessage {
	t.Helper()
	select {
	case msg := <-want.sendCh:
		select {
		case stray := <-other.sendCh:
			t.Fatalf("message for daemon %s was ALSO delivered to daemon %s: %v", want.daemonID, other.daemonID, stray)
		case <-time.After(150 * time.Millisecond):
		}
		return msg
	case stray := <-other.sendCh:
		t.Fatalf("message for daemon %s was delivered to daemon %s instead: %v", want.daemonID, other.daemonID, stray)
	case <-time.After(3 * time.Second):
		t.Fatalf("message for daemon %s was never delivered", want.daemonID)
	}
	return nil
}

func TestBridge_ToolRequestForCloudDaemonIsNotDeliveredToLaptop(t *testing.T) {
	f := newNamedRoutingFixture(t)
	payload, _ := json.Marshal(&toolexec.ToolExecutionRequest{RequestID: "req-1", ToolName: "bash", ToolCallID: "tc-1"})
	require.NoError(t, f.nc.Publish(f.subject("tools.request", f.cloud), payload))

	msg := expectOnlyOn(t, f.cloud, f.laptop)
	assert.Equal(t, "req-1", msg.GetToolRequest().GetRequestId())
}

func TestBridge_CancelForCloudDaemonIsNotDeliveredToLaptop(t *testing.T) {
	f := newNamedRoutingFixture(t)
	payload, _ := json.Marshal(map[string]string{"request_id": "req-1", "reason": "stop"})
	require.NoError(t, f.nc.Publish(f.subject("tools.cancel", f.cloud), payload))

	msg := expectOnlyOn(t, f.cloud, f.laptop)
	assert.Equal(t, "req-1", msg.GetToolCancel().GetRequestId())
}

func TestBridge_OtherFireAndForgetSubjectsStayOnTheNamedDaemon(t *testing.T) {
	f := newNamedRoutingFixture(t)

	bg, _ := json.Marshal(map[string]string{"request_id": "req-1", "tool_call_id": "tc-1"})
	require.NoError(t, f.nc.Publish(f.subject("tools.background", f.cloud), bg))
	assert.Equal(t, "req-1", expectOnlyOn(t, f.cloud, f.laptop).GetToolBackground().GetRequestId())

	cfg, _ := json.Marshal(map[string]string{"project_path": "/work/p", "request_id": "cfg-1"})
	require.NoError(t, f.nc.Publish(f.subject("daemon.config.load", f.cloud), cfg))
	assert.Equal(t, "cfg-1", expectOnlyOn(t, f.cloud, f.laptop).GetLoadProjectConfigs().GetRequestId())

	in, _ := json.Marshal(map[string]any{"data": []byte("ls\n")})
	require.NoError(t, f.nc.Publish(f.subject("daemon.terminal.input", f.cloud)+".sess-1", in))
	assert.Equal(t, "sess-1", expectOnlyOn(t, f.cloud, f.laptop).GetTerminalInput().GetSessionId())

	rs, _ := json.Marshal(map[string]any{"cols": 80, "rows": 24})
	require.NoError(t, f.nc.Publish(f.subject("daemon.terminal.resize", f.cloud)+".sess-1", rs))
	assert.Equal(t, "sess-1", expectOnlyOn(t, f.cloud, f.laptop).GetTerminalResize().GetSessionId())

	kill, _ := json.Marshal(map[string]string{"process_id": "proc-1"})
	reply, err := f.nc.Request(f.subject("daemon.process.kill", f.cloud), kill, 3*time.Second)
	require.NoError(t, err)
	assert.Contains(t, string(reply.Data), `"ok":true`)
	assert.Equal(t, "proc-1", expectOnlyOn(t, f.cloud, f.laptop).GetKillProcess().GetProcessId())
}

// The bridge still holds a subscription for a daemon whose connection is gone
// (a disconnect race, or a replica that never held it). A request that names
// it must fail, never land on the user's other daemon.
func TestBridge_RequestForDisconnectedDaemonIsNotDeliveredToTheOtherOne(t *testing.T) {
	f := newNamedRoutingFixture(t)

	f.svc.mu.Lock()
	delete(f.svc.connections, f.cloud.daemonID)
	f.svc.userDaemons[namedRoutingUser] = []string{f.laptop.daemonID}
	f.svc.mu.Unlock()

	payload, _ := json.Marshal(&toolexec.ToolExecutionRequest{RequestID: "req-2", ToolName: "bash"})
	require.NoError(t, f.nc.Publish(f.subject("tools.request", f.cloud), payload))
	cancel, _ := json.Marshal(map[string]string{"request_id": "req-2"})
	require.NoError(t, f.nc.Publish(f.subject("tools.cancel", f.cloud), cancel))

	select {
	case stray := <-f.laptop.sendCh:
		t.Fatalf("request naming a disconnected daemon was delivered to the laptop: %v", stray)
	case <-time.After(400 * time.Millisecond):
	}

	// The synchronous path reports the failure to its caller.
	sync, _ := json.Marshal(&toolexec.ToolExecutionRequest{RequestID: "req-3", ToolName: "bash", TimeoutMs: 2000})
	reply, err := f.nc.Request(f.subject("tools.request.sync", f.cloud), sync, 5*time.Second)
	require.NoError(t, err)
	var resp toolexec.ToolExecutionResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	assert.False(t, resp.Success)
	assert.True(t, resp.IsError)
	select {
	case stray := <-f.laptop.sendCh:
		t.Fatalf("sync request naming a disconnected daemon reached the laptop: %v", stray)
	case <-time.After(150 * time.Millisecond):
	}
}

// A daemon id is only addressable by its owner.
func TestManager_NamedDaemonOfAnotherUserIsRefused(t *testing.T) {
	f := newNamedRoutingFixture(t)
	err := f.svc.SendToolExecutionCancel(context.Background(), "someone-else", f.cloud.daemonID, "req-1", "x")
	require.Error(t, err)
	select {
	case stray := <-f.cloud.sendCh:
		t.Fatalf("another user's cancel reached the daemon: %v", stray)
	default:
	}
}
