// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/cliupdate"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

// fakeSelfUpdate stands in for cliupdate.Daemon: Stage answers version (or
// stageErr), and Apply drains through the runtime's drain exactly as forge's
// Request does, then calls the runtime's shutdown.
type fakeSelfUpdate struct {
	auto, selfOwned bool
	version         string
	stageErr        error
	// beforeDrain runs inside Apply, before the drain: a test makes work
	// arrive "as the quiet moment was seen" there.
	beforeDrain func()

	stages   atomic.Int64
	applies  atomic.Int64
	confirms atomic.Int64
	mu       sync.Mutex
	last     cliupdate.ApplyRequest
}

func (f *fakeSelfUpdate) AutoUpdate() bool { return f.auto }
func (f *fakeSelfUpdate) SelfOwned() bool  { return f.selfOwned }
func (f *fakeSelfUpdate) Advice() string   { return "reliant update" }
func (f *fakeSelfUpdate) Confirm()         { f.confirms.Add(1) }

func (f *fakeSelfUpdate) Stage(context.Context) (string, error) {
	f.stages.Add(1)
	return f.version, f.stageErr
}

func (f *fakeSelfUpdate) Apply(ctx context.Context, req cliupdate.ApplyRequest) error {
	f.applies.Add(1)
	f.mu.Lock()
	f.last = req
	f.mu.Unlock()
	if f.beforeDrain != nil {
		f.beforeDrain()
	}
	if _, err := req.Drain(ctx, req.Deadline); err != nil {
		return fmt.Errorf("selfupdate: the drain declined the upgrade: %w", err)
	}
	req.Shutdown()
	return nil
}

// selfClock is the self-update's clock: it moves only when a test says.
type selfClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *selfClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *selfClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// selfUpdatingDaemon is a person's daemon with auto-update on, on test time:
// a fake clock, a background-process count the test sets, polls every
// millisecond, and a runtime stop it can observe.
type selfUpdatingDaemon struct {
	d       *daemonClient
	su      *fakeSelfUpdate
	clock   *selfClock
	bg      atomic.Int64
	stopped atomic.Bool
	probe   *drainProbe
}

func newSelfUpdatingDaemon(t *testing.T, su *fakeSelfUpdate) *selfUpdatingDaemon {
	t.Helper()
	s := &selfUpdatingDaemon{su: su, clock: &selfClock{t: time.Unix(1_800_000_000, 0)}}
	d := newTestDaemonClient("d-self", "u-self")
	d.selfUpdate = su
	d.upgrade.poll = time.Millisecond
	d.upgrade.now = s.clock.now
	d.upgrade.backgroundCount = func() int { return int(s.bg.Load()) }
	d.upgrade.stageEvery, d.upgrade.selfRetry = time.Hour, time.Hour
	d.upgrade.backstop = time.Hour
	d.armUpgrade(func() { s.stopped.Store(true) }, "", nil)
	s.probe = probeDrain(d)
	s.d = d
	return s
}

func (s *selfUpdatingDaemon) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.d.startSelfUpdate(ctx)
}

func (s *selfUpdatingDaemon) waitInstalled(t *testing.T) {
	t.Helper()
	require.Eventually(t, s.d.upgrade.exitRequested, 5*time.Second, time.Millisecond, "the daemon never stopped for the install")
	assert.True(t, s.stopped.Load(), "the install stops the runtime")
}

// A staged release is installed by itself once nothing has happened for the
// idle time: no RestartRequested is needed.
func TestAutoUpdateInstallsAtAQuietMoment(t *testing.T) {
	s := newSelfUpdatingDaemon(t, &fakeSelfUpdate{auto: true, selfOwned: true, version: "1.27.0"})
	s.start(t)

	staged := waitForUpgradeState(t, s.d, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_STAGED)
	assert.Equal(t, "1.27.0", staged.GetTargetVersion())
	assert.False(t, s.d.upgrade.refusing(), "a staged daemon keeps taking tool calls")

	// Just started: a start counts as activity, so nothing is installed
	// until the idle time has passed on the clock.
	s.clock.advance(selfQuietIdle - time.Second)
	s.probe.waitForPolls(t, 5)
	assert.Zero(t, s.su.applies.Load(), "installed before the machine was idle for %s", selfQuietIdle)

	s.clock.advance(time.Second)
	s.waitInstalled(t)
	drained := waitForUpgradeState(t, s.d, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_DRAINED)
	assert.Contains(t, drained.GetDetail(), "drained: installing 1.27.0")
	assert.EqualValues(t, 1, s.su.applies.Load())
}

// Tool calls, background processes and fresh activity each keep the daemon
// from installing.
func TestAutoUpdateWaitsForWorkAndActivity(t *testing.T) {
	s := newSelfUpdatingDaemon(t, &fakeSelfUpdate{auto: true, selfOwned: true, version: "1.27.0"})
	s.d.results.start("running-1")
	s.bg.Store(1)
	s.start(t)
	waitForUpgradeState(t, s.d, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_STAGED)
	s.clock.advance(time.Hour)

	s.probe.waitForPolls(t, 5)
	assert.Zero(t, s.su.applies.Load(), "a running tool call blocks the install")

	s.d.results.hold("running-1", s.d.toolResponse("running-1", finished("done")), nil)
	s.probe.waitForPolls(t, 5)
	assert.Zero(t, s.su.applies.Load(), "a running background process blocks the install")

	s.bg.Store(0)
	s.d.upgrade.markActivity() // a terminal keystroke, a tool call finishing
	s.probe.waitForPolls(t, 5)
	assert.Zero(t, s.su.applies.Load(), "recent activity blocks the install")

	s.clock.advance(selfQuietIdle)
	s.waitInstalled(t)
}

// What counts as activity: tool calls and terminal use, not the platform's
// own housekeeping.
func TestActivityIsToolAndTerminalUse(t *testing.T) {
	cmd := func(name string) *reliantv1.ServerMessage {
		return &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_DaemonCommand{DaemonCommand: &reliantv1.DaemonCommandRequest{CommandType: name}}}
	}
	for msg, want := range map[*reliantv1.ServerMessage]bool{
		toolRequestMessage("r-1"): true,
		{Message: &reliantv1.ServerMessage_TerminalInput{TerminalInput: &reliantv1.TerminalInputMessage{}}}: true,
		cmd("terminal.create"): true,
		cmd("exec.run"):        true,
		cmd("mcp.call_tool"):   true,
		cmd("git.status"):      false,
		{Message: &reliantv1.ServerMessage_Heartbeat{}}: false,
		restartRequest(time.Time{}):                     false,
	} {
		assert.Equal(t, want, isActivity(msg), "%v", msg)
	}
}

// A security request past its deadline installs even with background
// processes running and no idle time; they are interrupted, as at any
// restart.
func TestAutoUpdateSecurityDeadlineForcesTheInstall(t *testing.T) {
	s := newSelfUpdatingDaemon(t, &fakeSelfUpdate{auto: true, selfOwned: true, version: "1.27.0"})
	s.bg.Store(2)
	s.start(t)
	waitForUpgradeState(t, s.d, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_STAGED)

	req := restartRequest(s.clock.now().Add(time.Minute)).GetRestartRequested()
	req.Urgency = reliantv1.RestartUrgency_RESTART_URGENCY_SECURITY
	s.d.handleRestartRequested(context.Background(), req)
	s.probe.waitForPolls(t, 5)
	assert.Zero(t, s.su.applies.Load(), "before its deadline a security request waits for a quiet moment too")

	s.clock.advance(time.Minute)
	s.waitInstalled(t)
	drained := waitForUpgradeState(t, s.d, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_DRAINED)
	assert.Contains(t, drained.GetDetail(), "2 background process(es) are interrupted")
	assert.Equal(t, "roll-1", drained.GetRolloutId())
}

// A request of normal urgency past its deadline forces nothing: the daemon
// was going to install at a quiet moment anyway.
func TestAutoUpdateNormalRequestDoesNotForce(t *testing.T) {
	s := newSelfUpdatingDaemon(t, &fakeSelfUpdate{auto: true, selfOwned: true, version: "1.27.0"})
	s.bg.Store(1)
	s.start(t)
	waitForUpgradeState(t, s.d, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_STAGED)
	s.d.handleRestartRequested(context.Background(), restartRequest(s.clock.now()).GetRestartRequested())
	s.clock.advance(time.Hour)
	s.probe.waitForPolls(t, 5)
	assert.Zero(t, s.su.applies.Load())
	assert.Equal(t, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_STAGED, upgradeState(s.d), "a person's daemon is never blocked")
}

// Work that arrives as the quiet moment is seen and outlives the drain
// declines the install: nothing is swapped, the daemon serves again, and it
// installs at the next quiet moment.
func TestAutoUpdateDeclinedDrainWaitsForTheNextQuietMoment(t *testing.T) {
	su := &fakeSelfUpdate{auto: true, selfOwned: true, version: "1.27.0"}
	s := newSelfUpdatingDaemon(t, su)
	first := true
	su.beforeDrain = func() {
		if first {
			first = false
			s.d.results.start("late-1")
			s.clock.advance(selfQuietDrain) // the drain's deadline passes with it running
		}
	}
	s.start(t)
	s.clock.advance(selfQuietIdle)

	require.Eventually(t, func() bool { return su.applies.Load() == 1 }, 5*time.Second, time.Millisecond)
	waitForUpgradeState(t, s.d, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_STAGED)
	assert.False(t, s.d.upgrade.refusing(), "a declined install takes tool calls again")
	assert.False(t, s.d.upgrade.exitRequested())

	s.d.results.hold("late-1", s.d.toolResponse("late-1", finished("done")), nil)
	s.waitInstalled(t)
	assert.EqualValues(t, 2, su.applies.Load())
}

// A release that cannot be staged is reported, and the daemon keeps serving;
// nothing newer is not news.
func TestAutoUpdateStageFailures(t *testing.T) {
	s := newSelfUpdatingDaemon(t, &fakeSelfUpdate{auto: true, selfOwned: true, stageErr: errors.New("signature does not verify")})
	s.start(t)
	failed := waitForUpgradeState(t, s.d, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_FAILED)
	assert.Contains(t, failed.GetDetail(), "signature does not verify")
	assert.Contains(t, failed.GetDetail(), "keeps serving")
	assert.False(t, s.d.upgrade.refusing())

	s = newSelfUpdatingDaemon(t, &fakeSelfUpdate{auto: true, selfOwned: true, stageErr: fmt.Errorf("%w on latest", cliupdate.ErrNoUpdate)})
	s.start(t)
	require.Eventually(t, func() bool { return s.su.stages.Load() == 1 }, 5*time.Second, time.Millisecond)
	assert.Nil(t, s.d.upgrade.heartbeatStatus(0), "nothing to install reports CURRENT")
}

// Without the opt-in a person's daemon never stages or installs: a
// RestartRequested leaves it AWAITING_OWNER, as before.
func TestWithoutAutoUpdateNothingIsInstalled(t *testing.T) {
	s := newSelfUpdatingDaemon(t, &fakeSelfUpdate{auto: false, selfOwned: true, version: "1.27.0"})
	s.start(t)
	s.d.handleRestartRequested(context.Background(), restartRequest(time.Time{}).GetRestartRequested())
	st := waitForUpgradeState(t, s.d, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_AWAITING_OWNER)
	assert.NotContains(t, st.GetDetail(), "installs")
	assert.Zero(t, s.su.stages.Load())
	assert.Zero(t, s.su.applies.Load())
	assert.False(t, s.d.upgrade.refusing())
}

// A managed machine's image is its version: its daemon drains for the pod
// roll whatever updater it was handed.
func TestManagedDaemonNeverSelfUpdates(t *testing.T) {
	s := newSelfUpdatingDaemon(t, &fakeSelfUpdate{auto: true, selfOwned: true, version: "1.27.0"})
	s.d.host = Host{DaemonType: daemonTypeManaged}
	s.start(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.d.handleRestartRequested(ctx, restartRequest(time.Time{}).GetRestartRequested())
	waitForUpgradeState(t, s.d, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_DRAINED)
	assert.Zero(t, s.su.stages.Load())
}

// The gateway accepting the daemon confirms a just-swapped binary.
func TestConnectedStreamConfirmsTheSwap(t *testing.T) {
	s := newSelfUpdatingDaemon(t, &fakeSelfUpdate{})
	s.d.recordStream(daemonstate.StreamConnected, "")
	assert.EqualValues(t, 1, s.su.confirms.Load())
}
