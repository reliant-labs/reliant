// Copyright (c) 2025 Reliant Labs

package toolexec

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

// startBridgeTestNATS runs an in-process NATS server and returns a client
// connection. Mirrors the helper in internal/daemonliveness's tests.
func startBridgeTestNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1 // random port
	srv := natstest.RunServer(&opts)
	t.Cleanup(srv.Shutdown)

	if !srv.ReadyForConnections(2 * time.Second) {
		t.Fatal("test nats server failed to come up")
	}
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect to test nats: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

// blockingDaemonMgr answers tool requests after a per-request delay encoded in
// the tool input, so a test can interleave a slow and a fast call.
type blockingDaemonMgr struct {
	recordingDaemonMgr
	release   chan struct{}
	inFlight  atomic.Int32
	maxSeen   atomic.Int32
	cmdBlocks chan struct{}
}

func (m *blockingDaemonMgr) SendToolRequestSync(_ context.Context, _ string, req *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	cur := m.inFlight.Add(1)
	for {
		prev := m.maxSeen.Load()
		if cur <= prev || m.maxSeen.CompareAndSwap(prev, cur) {
			break
		}
	}
	defer m.inFlight.Add(-1)

	// "slow" parks until the test releases it; "fast" returns immediately.
	if req.ToolName == "slow" {
		<-m.release
	}
	return &ToolExecutionResponse{
		RequestID: req.RequestID,
		Success:   true,
		Content:   req.ToolName,
	}, nil
}

func (m *blockingDaemonMgr) SendDaemonCommand(_ context.Context, _ string, req *reliantv1.DaemonCommandRequest) (*reliantv1.DaemonCommandResponse, error) {
	if req.CommandType == "slow" {
		<-m.cmdBlocks
	}
	return &reliantv1.DaemonCommandResponse{Success: true}, nil
}

// TestToolRequestSync_SlowCallDoesNotBlockFastCall is the regression test for
// the head-of-line stall on tools.request.sync.
//
// nats.go delivers messages for a subscription to its callback one at a time.
// The handler used to wait for the daemon round-trip INLINE, so a long tool
// call blocked every later request on the same {user,daemon} subject for its
// full duration — a 3ms `sed` was observed taking 209s, finishing 12ms after
// the `go test` ahead of it released the callback.
//
// Here the slow call parks indefinitely. If the fast call cannot complete
// while it is parked, the bug is back.
func TestToolRequestSync_SlowCallDoesNotBlockFastCall(t *testing.T) {
	nc := startBridgeTestNATS(t)
	mgr := &blockingDaemonMgr{release: make(chan struct{})}
	bridge := NewNATSToolBridge(nc, nil, mgr)
	t.Cleanup(func() {
		close(mgr.release) // let the parked handler exit so Close() can return
		_ = bridge.Close()
	})

	const userID, daemonID = "u-1", "d-1"
	bridge.OnDaemonConnected(userID, daemonID)

	subject := daemonSubject(toolRequestSyncSubject, userID, daemonID)

	// Fire the slow request. Nothing waits on its reply; it exists to occupy
	// the subscription.
	slowPayload, err := json.Marshal(&ToolExecutionRequest{RequestID: "slow-1", ToolName: "slow"})
	require.NoError(t, err)
	slowInbox := nats.NewInbox()
	slowSub, err := nc.SubscribeSync(slowInbox)
	require.NoError(t, err)
	require.NoError(t, nc.PublishRequest(subject, slowInbox, slowPayload))

	// Wait for the slow call to actually be in the manager, so the ordering
	// under test is real rather than a race we happened to win.
	require.Eventually(t, func() bool { return mgr.inFlight.Load() >= 1 },
		2*time.Second, 5*time.Millisecond, "slow tool request never reached the manager")

	// The fast request must round-trip while the slow one is still parked.
	fastPayload, err := json.Marshal(&ToolExecutionRequest{RequestID: "fast-1", ToolName: "fast"})
	require.NoError(t, err)

	reply, err := nc.Request(subject, fastPayload, 3*time.Second)
	require.NoError(t, err, "fast request timed out behind the slow one — head-of-line blocking has regressed")

	var resp ToolExecutionResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	require.Equal(t, "fast-1", resp.RequestID)
	require.True(t, resp.Success)

	// Both were genuinely in the manager at once, which is the property that
	// makes the reply above meaningful.
	require.GreaterOrEqual(t, mgr.maxSeen.Load(), int32(2),
		"expected slow and fast requests to be in flight concurrently")

	require.NoError(t, slowSub.Unsubscribe())
}

// TestDaemonCommand_SlowCommandDoesNotBlockFastCommand covers the same defect
// on daemon.command, which shared the inline-wait shape.
func TestDaemonCommand_SlowCommandDoesNotBlockFastCommand(t *testing.T) {
	nc := startBridgeTestNATS(t)
	mgr := &blockingDaemonMgr{release: make(chan struct{}), cmdBlocks: make(chan struct{})}
	bridge := NewNATSToolBridge(nc, nil, mgr)
	t.Cleanup(func() {
		close(mgr.cmdBlocks)
		close(mgr.release)
		_ = bridge.Close()
	})

	const userID, daemonID = "u-2", "d-2"
	bridge.OnDaemonConnected(userID, daemonID)

	subject := daemonSubject(daemonCommandSubject, userID, daemonID)

	slowPayload, err := json.Marshal(map[string]interface{}{
		"request_id": "slow-cmd", "command_type": "slow",
	})
	require.NoError(t, err)
	slowInbox := nats.NewInbox()
	slowSub, err := nc.SubscribeSync(slowInbox)
	require.NoError(t, err)
	require.NoError(t, nc.PublishRequest(subject, slowInbox, slowPayload))

	fastPayload, err := json.Marshal(map[string]interface{}{
		"request_id": "fast-cmd", "command_type": "fast",
	})
	require.NoError(t, err)

	reply, err := nc.Request(subject, fastPayload, 3*time.Second)
	require.NoError(t, err, "fast daemon command timed out behind the slow one")

	var resp struct {
		Success bool `json:"success"`
	}
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	require.True(t, resp.Success)

	require.NoError(t, slowSub.Unsubscribe())
}

// TestToolRequestSync_OverloadFailsFast proves the in-flight budget rejects
// rather than blocks. Blocking on a full budget would reintroduce exactly the
// head-of-line stall the async dispatch exists to prevent.
func TestToolRequestSync_OverloadFailsFast(t *testing.T) {
	nc := startBridgeTestNATS(t)
	mgr := &blockingDaemonMgr{release: make(chan struct{})}
	bridge := NewNATSToolBridge(nc, nil, mgr)

	// Saturate the budget so the next request must be rejected.
	for i := 0; i < maxInFlightRequests; i++ {
		bridge.inFlight <- struct{}{}
	}
	t.Cleanup(func() {
		for i := 0; i < maxInFlightRequests; i++ {
			<-bridge.inFlight
		}
		close(mgr.release)
		_ = bridge.Close()
	})

	const userID, daemonID = "u-3", "d-3"
	bridge.OnDaemonConnected(userID, daemonID)
	subject := daemonSubject(toolRequestSyncSubject, userID, daemonID)

	payload, err := json.Marshal(&ToolExecutionRequest{RequestID: "over-1", ToolName: "fast"})
	require.NoError(t, err)

	reply, err := nc.Request(subject, payload, 2*time.Second)
	require.NoError(t, err, "overloaded bridge must answer, not hang")

	var resp ToolExecutionResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	require.False(t, resp.Success)
	require.True(t, resp.IsError)
	require.Equal(t, ErrorCodeDaemonRoundTrip, resp.ErrorCode)
	// Content (not just ErrorMessage) is what the LLM sees on a tool result.
	require.NotEmpty(t, resp.Content)
}
