// Copyright (c) 2026 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonoffline"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// updatingDaemonMgr is a reconnecting daemon whose gateway reports it
// draining for a restart: tool requests are refused unsent, as the gateway
// does (ErrDaemonUpdating), or by the daemon itself (an old gateway passing
// on the daemon's DAEMON_UPDATING answer), until it has restarted.
type updatingDaemonMgr struct {
	reconnectingDaemonMgr
	mu            sync.Mutex
	updatingUntil time.Time
	// reportsUpdating is false for a gateway that predates the protocol: it
	// answers tools.online "true" and passes the daemon's refusal on.
	reportsUpdating bool
	refused         int
	restarts        []*reliantv1.RestartRequested
	restartErr      error
}

func (m *updatingDaemonMgr) updating() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.updatingUntil.IsZero()
}

func (m *updatingDaemonMgr) DaemonUpdatingUntil(_, _ string) (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.reportsUpdating || m.updatingUntil.IsZero() {
		return time.Time{}, false
	}
	return m.updatingUntil, true
}

func (m *updatingDaemonMgr) SendToolRequestSync(ctx context.Context, userID, daemonID string, req *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	m.mu.Lock()
	updating, viaGateway := !m.updatingUntil.IsZero(), m.reportsUpdating
	if updating {
		m.refused++
	}
	m.mu.Unlock()
	switch {
	case updating && viaGateway:
		return nil, fmt.Errorf("daemon %s is restarting: %w", daemonID, ErrDaemonUpdating)
	case updating:
		return &ToolExecutionResponse{RequestID: req.RequestID, IsError: true, ErrorCode: ErrorCodeDaemonUpdating,
			Content: "this machine is restarting for an update"}, nil
	}
	return m.reconnectingDaemonMgr.SendToolRequestSync(ctx, userID, daemonID, req)
}

func (m *updatingDaemonMgr) RequestDaemonRestart(_, _ string, req *reliantv1.RestartRequested) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.restartErr != nil {
		return m.restartErr
	}
	m.restarts = append(m.restarts, req)
	return nil
}

// restartAfter ends the drain after delay: the daemon comes back on a new
// stream, which the gateway announces.
func (m *updatingDaemonMgr) restartAfter(d *reconnectingDaemon, delay time.Duration) {
	d.pending.Add(1)
	go func() {
		defer d.pending.Done()
		time.Sleep(delay)
		m.mu.Lock()
		m.updatingUntil = time.Time{}
		m.mu.Unlock()
		d.disconnect()
		d.connect()
	}()
}

func updatingTestRouter(t *testing.T, reportsUpdating bool) (*NATSDaemonRouter, *reconnectingDaemon, *updatingDaemonMgr) {
	t.Helper()
	records := reconnectRecords(time.Now(), nil)
	router, daemon := reconnectTestRouter(t, records)
	mgr := &updatingDaemonMgr{
		reconnectingDaemonMgr: reconnectingDaemonMgr{d: daemon},
		updatingUntil:         time.Now().Add(10 * time.Minute),
		reportsUpdating:       reportsUpdating,
	}
	daemon.bridge.mgr = mgr
	daemon.connect()
	return router, daemon, mgr
}

// A tool call that reaches a daemon draining for a restart did not run. It
// waits for the daemon to come back and is delivered once, after the
// restart, instead of failing into the model as a tool error.
func TestToolRequest_ToADrainingDaemonWaitsForTheRestart(t *testing.T) {
	for name, viaGateway := range map[string]bool{"refused by the gateway": true, "refused by the daemon behind an old gateway": false} {
		t.Run(name, func(t *testing.T) {
			router, daemon, mgr := updatingTestRouter(t, viaGateway)
			mgr.restartAfter(daemon, 300*time.Millisecond)
			waits := &waitRecorder{}
			ctx := WithDaemonReconnectWait(context.Background(), waits.observe)

			resp, err := router.SendToolRequestSync(ctx, reconnectUser, shellRequest(tools.ShellToolName, `{"command":"go test ./..."}`))
			require.NoError(t, err)
			assert.True(t, resp.Success)
			assert.False(t, mgr.updating())
			assert.Len(t, daemon.toolRequests(), 1, "the call ran once, after the restart")
			started, ended := waits.snapshot()
			assert.Equal(t, []string{reconnectDaemon}, started, "the run hears it is waiting for its machine")
			assert.Equal(t, []bool{true}, ended)
		})
	}
}

// While its gateway reports the drain, the waiting call does not re-send into
// it: the wait's first probe, at once, says "not back yet", where "true"
// would have re-sent the call into the drain straight away.
func TestToolRequest_ToADrainingDaemonIsNotResentDuringTheDrain(t *testing.T) {
	router, daemon, mgr := updatingTestRouter(t, true)
	mgr.restartAfter(daemon, 300*time.Millisecond)
	_, err := router.SendToolRequestSync(WithDaemonReconnectWait(context.Background(), nil), reconnectUser,
		shellRequest(tools.ShellToolName, `{"command":"ls"}`))
	require.NoError(t, err)
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	assert.Equal(t, 1, mgr.refused, "refused once, then waited for the restart without re-sending")
}

// A call that cannot wait (no opt-in) fails at once, unrun, with the offline
// marker, so the workflow's daemon-offline pause takes over.
func TestToolRequest_ToADrainingDaemonWithoutWaitingFailsAsOffline(t *testing.T) {
	router, _, _ := updatingTestRouter(t, true)
	_, err := router.SendToolRequestSync(context.Background(), reconnectUser, shellRequest(tools.ShellToolName, `{"command":"ls"}`))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDaemonUpdating)
	assert.ErrorIs(t, err, ErrNotDispatched, "nothing ran: a run step may send it again")
	assert.True(t, daemonoffline.IsError(err), "the offline marker survives: %v", err)
}

func TestReconnectDeadline(t *testing.T) {
	seen := time.Now()
	assert.Equal(t, seen.Add(DaemonReconnectGrace), reconnectDeadline(seen, time.Time{}), "no drain: the reconnect grace")
	drainEnds := seen.Add(10 * time.Minute)
	assert.Equal(t, drainEnds.Add(DaemonRestartBudget), reconnectDeadline(seen, drainEnds), "a drain is waited out, then the restart")
	assert.Equal(t, seen.Add(DaemonReconnectGrace), reconnectDeadline(seen, seen.Add(-time.Hour)), "never shorter than the grace")
}

func TestOnlineReply(t *testing.T) {
	online, until := parseOnlineReply([]byte("true"))
	assert.True(t, online)
	assert.True(t, until.IsZero())

	deadline := time.UnixMilli(time.Now().Add(time.Minute).UnixMilli())
	reply := EncodeOnlineReplyUpdating(deadline)
	assert.NotEqual(t, "true", string(reply), "a router that predates the protocol reads a draining daemon as not answering")
	online, until = parseOnlineReply(reply)
	assert.False(t, online)
	assert.True(t, deadline.Equal(until))

	online, until = parseOnlineReply([]byte("false"))
	assert.False(t, online)
	assert.True(t, until.IsZero())
}

// The internal send path: the router asks the gateway holding the daemon's
// stream, over the daemon's command subject, and the gateway answers it
// itself; the command never reaches the daemon.
func TestRequestDaemonRestart_ReachesTheGatewayHoldingTheDaemon(t *testing.T) {
	router, daemon, mgr := updatingTestRouter(t, true)
	req := &reliantv1.RestartRequested{TargetVersion: "v2", RolloutId: "roll-1",
		Urgency: reliantv1.RestartUrgency_RESTART_URGENCY_SECURITY, DeadlineUnixMs: time.Now().Add(time.Minute).UnixMilli()}
	require.NoError(t, router.RequestDaemonRestart(context.Background(), reconnectUser, reconnectDaemon, req))

	mgr.mu.Lock()
	require.Len(t, mgr.restarts, 1)
	got := mgr.restarts[0]
	mgr.mu.Unlock()
	assert.Equal(t, "roll-1", got.GetRolloutId())
	assert.Equal(t, reliantv1.RestartUrgency_RESTART_URGENCY_SECURITY, got.GetUrgency())
	assert.Empty(t, daemon.commandRequests(), "the gateway answers it; the daemon gets RestartRequested, not a command")

	require.Error(t, router.RequestDaemonRestart(context.Background(), reconnectUser, "", req))
}

func TestRequestDaemonRestart_PreservesUnsupportedDaemonAcrossGateway(t *testing.T) {
	router, daemon, mgr := updatingTestRouter(t, true)
	mgr.mu.Lock()
	mgr.restartErr = fmt.Errorf("gateway refusal: %w", ErrRestartNotSupported)
	mgr.mu.Unlock()

	err := router.RequestDaemonRestart(context.Background(), reconnectUser, reconnectDaemon, &reliantv1.RestartRequested{RolloutId: "r"})
	require.ErrorIs(t, err, ErrRestartNotSupported)
	assert.Empty(t, daemon.commandRequests())

	mgr.mu.Lock()
	mgr.restartErr = fmt.Errorf("restart delivery failed")
	mgr.mu.Unlock()
	err = router.RequestDaemonRestart(context.Background(), reconnectUser, reconnectDaemon, &reliantv1.RestartRequested{RolloutId: "r"})
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrRestartNotSupported)
	assert.ErrorContains(t, err, "restart delivery failed")
}

// A gateway that cannot ask (a manager without the method) says so instead
// of forwarding the command to the daemon, which would not know it.
func TestRequestDaemonRestart_RefusedByAGatewayThatCannotAsk(t *testing.T) {
	router, daemon := reconnectTestRouter(t, reconnectRecords(time.Now(), nil))
	daemon.connect()
	err := router.RequestDaemonRestart(context.Background(), reconnectUser, reconnectDaemon, &reliantv1.RestartRequested{RolloutId: "r"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot ask a daemon to restart")
	assert.Empty(t, daemon.commandRequests())
}

func TestRequestRestartPayloadRoundTrips(t *testing.T) {
	req := &reliantv1.RestartRequested{TargetVersion: "v2", CommitTimeUnix: 42}
	data, err := protojson.Marshal(req)
	require.NoError(t, err)
	require.True(t, json.Valid(data), "the payload rides daemon.command's JSON envelope")
	var back reliantv1.RestartRequested
	require.NoError(t, protojson.Unmarshal(data, &back))
	assert.EqualValues(t, 42, back.GetCommitTimeUnix())
}
