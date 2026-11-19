// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

// managedTestDaemon is a managed daemon whose drain runs on test time: it
// polls every millisecond, and drains and waits to be restarted for as long
// as the test says.
func managedTestDaemon(maxDrain, restartWait time.Duration) *daemonClient {
	d := newTestDaemonClient("d-upgrade", "u-upgrade")
	d.host = Host{DaemonType: daemonTypeManaged}
	d.upgrade.maxDrain = maxDrain
	d.upgrade.restartWait = restartWait
	d.upgrade.poll = time.Millisecond
	return d
}

func restartRequest(deadline time.Time) *reliantv1.ServerMessage {
	var ms int64
	if !deadline.IsZero() {
		ms = deadline.UnixMilli()
	}
	return &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_RestartRequested{RestartRequested: &reliantv1.RestartRequested{
		TargetVersion: "v1.2.3", RolloutId: "roll-1", DeadlineUnixMs: ms,
		Urgency: reliantv1.RestartUrgency_RESTART_URGENCY_NORMAL, Reason: "test",
	}}}
}

func upgradeState(d *daemonClient) reliantv1.DaemonUpgradeState {
	return d.upgrade.heartbeatStatus(d.results.runningCount()).GetState()
}

// drainProbe records the drain loop's passes and its return, so a test that
// asserts something does NOT happen waits on the drain having looked N times
// (the loop re-reads the state on every pass) instead of on elapsed time.
// Install it before the drain starts.
type drainProbe struct {
	polls    atomic.Int64
	returned chan struct{}
}

func probeDrain(d *daemonClient) *drainProbe {
	p := &drainProbe{returned: make(chan struct{})}
	d.upgrade.onPoll = func() { p.polls.Add(1) }
	d.upgrade.onReturn = func() { close(p.returned) }
	return p
}

// waitForPolls returns once the drain loop has made n more passes than it had
// when called.
func (p *drainProbe) waitForPolls(t *testing.T, n int64) {
	t.Helper()
	target := p.polls.Load() + n
	require.Eventually(t, func() bool { return p.polls.Load() >= target }, 5*time.Second, time.Millisecond,
		"the drain loop never made %d passes", n)
}

func (p *drainProbe) waitForReturn(t *testing.T) {
	t.Helper()
	select {
	case <-p.returned:
	case <-time.After(5 * time.Second):
		t.Fatal("the drain never returned")
	}
}

func waitForUpgradeState(t *testing.T, d *daemonClient, want reliantv1.DaemonUpgradeState) *reliantv1.DaemonUpgradeStatus {
	t.Helper()
	require.Eventually(t, func() bool { return upgradeState(d) == want }, 5*time.Second, time.Millisecond,
		"the daemon never reported %s", want)
	return d.upgrade.heartbeatStatus(d.results.runningCount())
}

// While a managed daemon drains, a new tool request is answered unrun with
// the code a sender re-sends on, and is not recorded, so the same request id
// runs once the daemon is back.
func TestDrainingDaemonRefusesNewToolRequestsUnrun(t *testing.T) {
	d := managedTestDaemon(time.Hour, time.Hour)
	s := useTestSession(d, 16)
	d.results.start("running-1") // keeps the drain in UPDATING
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, d.handleServerMessage(ctx, s, restartRequest(time.Time{})))
	require.Equal(t, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_UPDATING, upgradeState(d))

	require.NoError(t, d.handleServerMessage(ctx, s, toolRequestMessage("new-1")))
	got := sentToolResponse(t, s)
	assert.Equal(t, "new-1", got.GetRequestId())
	assert.False(t, got.GetSuccess())
	assert.Equal(t, toolexec.ErrorCodeDaemonUpdating, got.GetErrorCode())
	assert.True(t, toolexec.IsTransportErrorCode(got.GetErrorCode()), "a refusal is not the tool's own failure")
	assert.Zero(t, s.inFlight.Load(), "a refused request holds no stream open")
	_, fresh := d.results.begin("new-1")
	assert.True(t, fresh, "a refused request id was never recorded as run")
}

// The drain lets running calls finish, then reports DRAINED, and keeps
// refusing new work while it waits to be restarted.
func TestDrainWaitsForRunningCallsThenReportsDrained(t *testing.T) {
	d := managedTestDaemon(time.Hour, time.Hour)
	useTestSession(d, 16)
	d.results.start("running-1")
	probe := probeDrain(d)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, d.handleServerMessage(ctx, nil, restartRequest(time.Now().Add(time.Minute))))
	st := d.upgrade.heartbeatStatus(d.results.runningCount())
	require.Equal(t, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_UPDATING, st.GetState())
	assert.EqualValues(t, 1, st.GetToolCallsRunning())
	assert.Equal(t, "roll-1", st.GetRolloutId())
	assert.Equal(t, "v1.2.3", st.GetTargetVersion())

	// Each pass re-checks the running count: after several passes with the
	// call still running, a drain that wrongly ended would have published
	// DRAINED.
	probe.waitForPolls(t, 5)
	require.Equal(t, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_UPDATING, upgradeState(d),
		"a running call keeps the drain going until its deadline")

	d.results.hold("running-1", d.toolResponse("running-1", finished("done")), nil)
	drained := waitForUpgradeState(t, d, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_DRAINED)
	assert.Zero(t, drained.GetToolCallsRunning())
	assert.True(t, d.upgrade.refusing(), "a drained daemon still refuses new work")
	assert.False(t, d.upgrade.exitRequested(), "a drained managed daemon waits for its pod to be rolled")
}

// The drain ends at its deadline, capped at the daemon's own limit, even with
// a call still running.
func TestDrainEndsAtTheCappedDeadlineWithCallsStillRunning(t *testing.T) {
	d := managedTestDaemon(30*time.Millisecond, time.Hour)
	d.results.start("forever-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	// The platform's deadline is an hour away; the daemon's limit wins.
	d.handleRestartRequested(ctx, restartRequest(time.Now().Add(time.Hour)).GetRestartRequested())
	st := d.upgrade.heartbeatStatus(1)
	assert.LessOrEqual(t, st.GetDeadlineUnixMs(), start.Add(30*time.Millisecond).UnixMilli()+1)

	drained := waitForUpgradeState(t, d, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_DRAINED)
	assert.EqualValues(t, 1, drained.GetToolCallsRunning())
	assert.Contains(t, drained.GetDetail(), "1 tool call(s) still running")
}

// A drained daemon the platform never restarts does not stay drained: it
// reports FAILED and makes an upgrade exit, stopping the runtime, with its
// teardown bounded by the backstop.
func TestDrainedDaemonNotRestartedMakesAnUpgradeExit(t *testing.T) {
	d := managedTestDaemon(time.Hour, 20*time.Millisecond)
	d.upgrade.backstop = 10 * time.Millisecond
	var stopped atomic.Bool
	exitCode := make(chan int, 1)
	d.armUpgrade(func() { stopped.Store(true) }, "", func(code int) { exitCode <- code })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d.handleRestartRequested(ctx, restartRequest(time.Time{}).GetRestartRequested())
	failed := waitForUpgradeState(t, d, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_FAILED)
	assert.Contains(t, failed.GetDetail(), "did not restart this machine")
	require.Eventually(t, d.upgrade.exitRequested, 5*time.Second, time.Millisecond)
	assert.True(t, stopped.Load(), "the upgrade exit stops the runtime")
	select {
	case code := <-exitCode:
		assert.Equal(t, UpgradeExitCode, code, "a teardown that does not finish is force-exited, still as an upgrade exit")
	case <-time.After(5 * time.Second):
		t.Fatal("the upgrade exit's teardown was not bounded")
	}
}

// The drain does not run under a shutdown bound: draining for longer than the
// upgrade exit's backstop (and daemon.go's 10s signal grace, which never arms
// for a drain) neither stops nor exits the daemon. A stop of the runtime
// mid-drain ends the drain quietly: a stop wins.
func TestDrainIsNotBoundedByTheShutdownWatchdog(t *testing.T) {
	d := managedTestDaemon(time.Hour, time.Hour)
	d.upgrade.backstop = time.Millisecond
	var stopped atomic.Bool
	var exited atomic.Bool
	d.armUpgrade(func() { stopped.Store(true) }, "", func(int) { exited.Store(true) })
	d.results.start("running-1")
	probe := probeDrain(d)
	ctx, cancel := context.WithCancel(context.Background())

	d.handleRestartRequested(ctx, restartRequest(time.Time{}).GetRestartRequested())
	// 50 passes at a 1ms poll put the drain well past the 1ms backstop with
	// the drain goroutine demonstrably running: a drain wrongly bounded by
	// it would have stopped or exited by now.
	probe.waitForPolls(t, 50)
	assert.Equal(t, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_UPDATING, upgradeState(d))
	assert.False(t, stopped.Load())
	assert.False(t, exited.Load())

	cancel() // a signal's stop
	// The drain has returned, so nothing later can turn the stop into an
	// upgrade exit.
	probe.waitForReturn(t)
	assert.False(t, d.upgrade.exitRequested(), "a stop during a drain is a stop, not an upgrade exit")
	assert.False(t, exited.Load())
}

// A person's machine is never blocked (D6): the daemon reports AWAITING_OWNER
// and keeps running tool calls.
func TestDaemonOnAPersonsMachineNeverDrains(t *testing.T) {
	d := newTestDaemonClient("d-laptop", "u-laptop")
	d.upgrade.poll = time.Millisecond
	s := useTestSession(d, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, d.handleServerMessage(ctx, s, restartRequest(time.Now().Add(time.Minute))))
	st := d.upgrade.heartbeatStatus(0)
	assert.Equal(t, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_AWAITING_OWNER, st.GetState())
	assert.Equal(t, "roll-1", st.GetRolloutId())
	assert.False(t, d.upgrade.refusing())

	d.results.begin("run-1") // stands in for running it
	require.NoError(t, d.handleServerMessage(ctx, s, toolRequestMessage("run-1")))
	noToolResponse(t, s, "a request on a person's machine is not refused")
}

// A repeated request can bring a drain's deadline forward, never push it
// back, and does not start a second drain.
func TestRepeatedRestartRequestOnlyBringsTheDeadlineForward(t *testing.T) {
	d := managedTestDaemon(time.Hour, time.Hour)
	d.results.start("running-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := time.Now().Add(10 * time.Minute)
	d.handleRestartRequested(ctx, restartRequest(first).GetRestartRequested())
	d.handleRestartRequested(ctx, restartRequest(first.Add(5*time.Minute)).GetRestartRequested())
	assert.Equal(t, first.UnixMilli(), d.upgrade.heartbeatStatus(1).GetDeadlineUnixMs(), "a later deadline is ignored")

	sooner := time.Now().Add(time.Minute)
	d.handleRestartRequested(ctx, restartRequest(sooner).GetRestartRequested())
	assert.Equal(t, sooner.UnixMilli(), d.upgrade.heartbeatStatus(1).GetDeadlineUnixMs())
}

func TestDrainDeadline(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	limit := 10 * time.Minute
	cases := map[string]struct {
		deadlineMs int64
		want       time.Time
	}{
		"none: the daemon's limit":   {0, now.Add(limit)},
		"within the limit":           {now.Add(time.Minute).UnixMilli(), now.Add(time.Minute)},
		"beyond the limit is capped": {now.Add(time.Hour).UnixMilli(), now.Add(limit)},
		"past: drain now":            {now.Add(-time.Minute).UnixMilli(), now},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.True(t, tc.want.Equal(drainDeadline(tc.deadlineMs, now, limit)))
		})
	}
}

// The heartbeat carries the upgrade status, and a change goes out at once
// rather than a heartbeat interval later.
func TestHeartbeatCarriesTheUpgradeStatusAtOnce(t *testing.T) {
	d := managedTestDaemon(time.Hour, time.Hour)
	d.timing = sessionTiming{heartbeatInterval: time.Hour}
	d.results.start("running-1")
	s := newTestSession(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.runHeartbeats(ctx, s, nil)

	d.handleRestartRequested(ctx, restartRequest(time.Time{}).GetRestartRequested())
	select {
	case msg := <-s.sendCh:
		require.Equal(t, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_UPDATING, msg.GetHeartbeat().GetUpgrade().GetState())
		assert.EqualValues(t, 1, msg.GetHeartbeat().GetUpgrade().GetToolCallsRunning())
	case <-time.After(5 * time.Second):
		t.Fatal("the change was not sent at once")
	}

	// A stream that registers mid-drain says so on its first heartbeat.
	next := newTestSession(8)
	go d.runHeartbeats(ctx, next, nil)
	select {
	case msg := <-next.sendCh:
		require.Equal(t, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_UPDATING, msg.GetHeartbeat().GetUpgrade().GetState())
	case <-time.After(5 * time.Second):
		t.Fatal("a stream opened mid-drain did not report it at once")
	}
}

// A daemon with no restart in progress reports nothing, which a gateway
// reads as CURRENT, exactly as from a daemon that predates the protocol.
func TestHeartbeatOmitsTheUpgradeStatusWhenCurrent(t *testing.T) {
	d := &daemonClient{timing: sessionTiming{heartbeatInterval: 10 * time.Millisecond}}
	s := newTestSession(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.runHeartbeats(ctx, s, nil)
	select {
	case msg := <-s.sendCh:
		require.NotNil(t, msg.GetHeartbeat())
		assert.Nil(t, msg.GetHeartbeat().GetUpgrade())
	case <-time.After(5 * time.Second):
		t.Fatal("no heartbeat")
	}
}

// The status is mirrored into the daemon-state record, where `reliant daemon
// status` and a supervisor read it.
func TestUpgradeStatusIsRecordedInTheDaemonState(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, daemonstate.Init(dir, daemonstate.Startup{Managed: true}))
	d := managedTestDaemon(time.Hour, time.Hour)
	d.armUpgrade(nil, dir, nil)
	d.results.start("running-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d.handleRestartRequested(ctx, restartRequest(time.Time{}).GetRestartRequested())
	st, err := daemonstate.Read(dir)
	require.NoError(t, err)
	require.NotNil(t, st.Upgrade)
	assert.Equal(t, "updating", st.Upgrade.State)
	assert.Equal(t, "v1.2.3", st.Upgrade.TargetVersion)
	assert.False(t, st.Upgrade.Deadline.IsZero())
}
