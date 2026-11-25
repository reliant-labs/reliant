// Copyright (c) 2026 Reliant Labs
package tools_daemon

import (
	"context"
	"encoding/json"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonstate"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/pkg/daemonwire"
)

func upgradeHeartbeat(state reliantv1.DaemonUpgradeState, deadline time.Time) *reliantv1.DaemonMessage {
	hb := &reliantv1.DaemonHeartbeat{Timestamp: time.Now().UnixMilli()}
	if state != reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_UNSPECIFIED {
		hb.Upgrade = &reliantv1.DaemonUpgradeStatus{State: state, DeadlineUnixMs: deadline.UnixMilli(), RolloutId: "roll-1"}
	}
	return &reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_Heartbeat{Heartbeat: hb}}
}

func registeredConn(t *testing.T, svc *Service) *daemonConnection {
	t.Helper()
	conn := newTestConn("user-1", uuid.NewString(), newParkedStream())
	svc.mu.Lock()
	registerTestConn(svc, conn)
	svc.mu.Unlock()
	return conn
}

// The receive loop takes the daemon's drain state off its heartbeat, so a
// draining daemon stops getting tool requests the moment it says so.
func TestHeartbeatUpgradeStateStopsToolRouting(t *testing.T) {
	svc := newTestService(&MockRepository{})
	t.Cleanup(svc.Close)
	stream := newParkedStream()
	conn := newTestConn("user-1", uuid.NewString(), stream)
	svc.mu.Lock()
	registerTestConn(svc, conn)
	svc.mu.Unlock()
	go func() { _ = svc.handleIncoming(context.Background(), conn) }()

	deadline := time.Now().Add(5 * time.Minute).Truncate(time.Millisecond)
	stream.recv <- upgradeHeartbeat(reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_UPDATING, deadline)
	require.Eventually(t, conn.refusesToolRequests, 5*time.Second, time.Millisecond)
	until, updating := svc.DaemonUpdatingUntil("user-1", conn.daemonID)
	require.True(t, updating)
	assert.True(t, deadline.Equal(until))

	// Not routed: answered unsent, as undelivered, and nothing was queued.
	_, err := svc.SendToolRequestSync(context.Background(), "user-1", conn.daemonID,
		&toolexec.ToolExecutionRequest{RequestID: "req-1", ToolName: "bash"})
	require.ErrorIs(t, err, toolexec.ErrDaemonUpdating)
	require.ErrorIs(t, svc.SendToolRequest(context.Background(), "user-1", conn.daemonID,
		&toolexec.ToolExecutionRequest{RequestID: "req-2", ToolName: "bash"}), toolexec.ErrDaemonUpdating)
	assert.Empty(t, conn.sendCh, "no tool request reached the draining daemon")
}

// The control plane hears of each change of the daemon's upgrade state, once,
// as an upgrade event: not of every heartbeat that repeats it, not of the
// first heartbeat of an ordinary stream, and never as a connected or activity
// event, which its consumers would answer by re-mirroring a build or keeping
// the machine awake. Through the real publisher, onto NATS.
func TestHeartbeatPublishesUpgradeEventOnTransitionOnly(t *testing.T) {
	daemonID := uuid.NewString()
	rec := newActivityRecorder(t, daemonID) // for its NATS connection
	sub, err := rec.nc.SubscribeSync(daemonwire.SubjectPrefix + daemonID + ".*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	require.NoError(t, rec.nc.Flush())

	svc := newTestService(&MockRepository{})
	t.Cleanup(svc.Close)
	svc.deps.StatePublisher = daemonstate.NewPublisher(rec.nc)
	stream := newParkedStream()
	conn := newTestConn("user-1", daemonID, stream)
	conn.daemonType = "cloud"
	conn.build = db.DaemonBuild{Version: "1.8.3", Commit: "abc123"}
	svc.mu.Lock()
	registerTestConn(svc, conn)
	svc.mu.Unlock()
	go func() { _ = svc.handleIncoming(context.Background(), conn) }()

	deadline := time.UnixMilli(1792800123456)
	for _, state := range []reliantv1.DaemonUpgradeState{
		reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_UNSPECIFIED, // an ordinary stream's first heartbeat
		reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_CURRENT,
		reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_UPDATING,
		reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_UPDATING,
		reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_UPDATING,
		reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_DRAINED,
		reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_DRAINED,
		reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_FAILED,
		// A sentinel: the receive loop reads in order and one connection
		// publishes in order, so anything the heartbeats before it caused
		// arrives before its event.
		reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_CURRENT,
	} {
		stream.recv <- upgradeHeartbeat(state, deadline)
	}

	var got []daemonwire.UpgradeState
	for len(got) < 4 {
		msg, err := sub.NextMsg(30 * time.Second) // a failure bound, not a wait
		require.NoError(t, err, "published only %v", got)
		require.Equal(t, daemonwire.Subject(daemonID, daemonwire.EventUpgrade), msg.Subject,
			"an upgrade transition published something other than an upgrade event")
		evt, err := daemonwire.UnmarshalEvent(msg.Data)
		require.NoError(t, err)
		require.Equal(t, daemonwire.EventUpgrade, evt.Type)
		assert.Nil(t, evt.Build, "an upgrade event carries no build")
		assert.Empty(t, evt.Reason)
		assert.Equal(t, "user-1", evt.UserID)
		assert.Equal(t, "cloud", evt.DaemonType)
		require.NotNil(t, evt.Upgrade)
		assert.Equal(t, "roll-1", evt.Upgrade.RolloutID)
		require.NotNil(t, evt.Upgrade.Deadline)
		assert.True(t, deadline.Equal(*evt.Upgrade.Deadline))
		got = append(got, evt.Upgrade.State)
	}
	assert.Equal(t, []daemonwire.UpgradeState{
		daemonwire.UpgradeStateUpdating, daemonwire.UpgradeStateDrained,
		daemonwire.UpgradeStateFailed, daemonwire.UpgradeStateCurrent,
	}, got, "one event per change of state, none for a repeat")
}

// A daemon that attaches mid-drain (it moved gateways, or restarted in place)
// reports its state on its new stream's first heartbeat, and that is published:
// the new stream's connected event carried none.
func TestFirstHeartbeatMidDrainIsATransition(t *testing.T) {
	conn := newTestConn("u", "d", nil)
	assert.True(t, conn.recordUpgradeStatus(&reliantv1.DaemonUpgradeStatus{State: reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_DRAINED}))
	assert.False(t, conn.recordUpgradeStatus(&reliantv1.DaemonUpgradeStatus{State: reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_DRAINED}))
	assert.True(t, conn.recordUpgradeStatus(nil), "a drain called off (back to CURRENT) is a change")
}

func TestWireUpgrade(t *testing.T) {
	up, ok := wireUpgrade(&reliantv1.DaemonUpgradeStatus{
		State: reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_DRAINED, DeadlineUnixMs: 1792800123456,
		RolloutId: "roll-1", TargetVersion: "1.9.0", ToolCallsRunning: 2, Detail: "drain deadline passed",
	})
	require.True(t, ok)
	deadline := time.UnixMilli(1792800123456).UTC()
	assert.Equal(t, daemonwire.Upgrade{
		State: daemonwire.UpgradeStateDrained, RolloutID: "roll-1", TargetVersion: "1.9.0",
		Deadline: &deadline, ToolCallsRunning: 2, Detail: "drain deadline passed",
	}, up)

	up, ok = wireUpgrade(nil)
	require.True(t, ok)
	assert.Equal(t, daemonwire.Upgrade{State: daemonwire.UpgradeStateCurrent}, up, "no report is CURRENT, with no deadline")

	_, ok = wireUpgrade(&reliantv1.DaemonUpgradeStatus{State: reliantv1.DaemonUpgradeState(99)})
	assert.False(t, ok, "a state this gateway cannot name is not published")

	for name, value := range reliantv1.DaemonUpgradeState_value {
		if _, ok := wireUpgrade(&reliantv1.DaemonUpgradeStatus{State: reliantv1.DaemonUpgradeState(value)}); !ok {
			t.Errorf("%s has no wire name", name)
		}
	}
}

func TestRefusesToolRequestsOnlyWhileDraining(t *testing.T) {
	cases := map[reliantv1.DaemonUpgradeState]bool{
		reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_UNSPECIFIED:    false,
		reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_CURRENT:        false,
		reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_UPDATING:       true,
		reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_DRAINED:        true,
		reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_AWAITING_OWNER: false, // never blocks a person's machine
		reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_FAILED:         false, // the daemon itself still refuses while it exits
	}
	for state, refuses := range cases {
		t.Run(state.String(), func(t *testing.T) {
			conn := newTestConn("u", "d", nil)
			conn.recordUpgradeStatus(upgradeHeartbeat(state, time.Now().Add(time.Minute)).GetHeartbeat().GetUpgrade())
			assert.Equal(t, refuses, conn.refusesToolRequests())
		})
	}
}

// A heartbeat without the field — a daemon that predates the protocol, or one
// back to CURRENT — routes as before; a daemon that came back routes again.
func TestHeartbeatWithoutUpgradeStatusRoutesNormally(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn := newTestConn("u", "d", nil)
		conn.recordUpgradeStatus(nil)
		assert.False(t, conn.refusesToolRequests())
		conn.recordUpgradeStatus(&reliantv1.DaemonUpgradeStatus{State: reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_DRAINED})
		require.True(t, conn.refusesToolRequests())
		until, ok := conn.updatingUntil()
		require.True(t, ok)
		assert.Equal(t, time.Now(), until, "a drain without a deadline uses the current virtual time")
		conn.recordUpgradeStatus(nil)
		assert.False(t, conn.refusesToolRequests())
	})
}

// RestartRequested goes only to a daemon that declared it acts on it. An old
// daemon would ignore the message, so its caller is told instead, and nothing
// is sent.
func TestRequestDaemonRestartOnlyReachesCapableDaemons(t *testing.T) {
	svc := newDaemonlessService(t)
	req := &reliantv1.RestartRequested{TargetVersion: "v2", RolloutId: "roll-1",
		Urgency: reliantv1.RestartUrgency_RESTART_URGENCY_NORMAL, DeadlineUnixMs: time.Now().Add(time.Minute).UnixMilli()}

	old := registeredConn(t, svc)
	require.ErrorIs(t, svc.RequestDaemonRestart("user-1", old.daemonID, req), toolexec.ErrRestartNotSupported)
	assert.Empty(t, old.sendCh, "a daemon without the capability never gets the message")

	capable := registeredConn(t, svc)
	capable.drainsOnRestartRequest = true
	require.NoError(t, svc.RequestDaemonRestart("user-1", capable.daemonID, req))
	sent := <-capable.sendCh
	assert.Equal(t, "roll-1", sent.GetRestartRequested().GetRolloutId())

	require.Error(t, svc.RequestDaemonRestart("user-2", capable.daemonID, req), "another user's daemon is not reachable")
	require.Error(t, svc.RequestDaemonRestart("user-1", "no-such-daemon", req))
}

func TestPublishDaemonHeartbeat_CarriesUpgradeState(t *testing.T) {
	svc := listChangedService(nil)
	t.Cleanup(svc.Close)
	hub := &recordingUserHub{}
	svc.deps.UserUpdateHub = hub

	deadline := time.Now().Add(time.Minute)
	svc.publishDaemonHeartbeat(context.Background(), "user-1", "daemon-1", time.Now(),
		upgradeHeartbeat(reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_UPDATING, deadline).GetHeartbeat())
	svc.publishDaemonHeartbeat(context.Background(), "user-1", "daemon-1", time.Now(), &reliantv1.DaemonHeartbeat{})

	hub.mu.Lock()
	defer hub.mu.Unlock()
	require.Len(t, hub.events, 2)
	var updating, current map[string]any
	require.NoError(t, json.Unmarshal(hub.events[0].Payload.Data, &updating))
	require.NoError(t, json.Unmarshal(hub.events[1].Payload.Data, &current))
	assert.Equal(t, "updating", updating["upgrade_state"])
	assert.EqualValues(t, deadline.UnixMilli(), updating["upgrade_deadline_unix_ms"])
	assert.NotContains(t, current, "upgrade_state")
}
