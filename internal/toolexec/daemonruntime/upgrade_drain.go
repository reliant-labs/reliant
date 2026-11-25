// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

// The drain protocol, the daemon's half (RestartRequested; phase B2 of the
// self-upgrade proposal, forge docs/proposals/self-upgrade.md §4). It mirrors
// the gateway drain (gateway_session.go), for this process instead of a
// gateway replica.
//
// On a managed machine (a cloud workspace pod, whose image is its version):
//
//  1. The daemon reports UPDATING on its heartbeat, at once and on every
//     heartbeat after, with the drain's deadline: the request's, capped at
//     upgradeMaxDrain (owner decision D5). The gateway stops routing new tool
//     requests here, so their senders find no responder and re-send after
//     the restart (toolexec's dispatchUndelivered).
//  2. A tool request that still arrives — before the gateway has heard, or on
//     a stream to another replica — is refused, unrun, with
//     toolexec.ErrorCodeDaemonUpdating, which a worker treats the same way.
//  3. Running calls finish, until none is left or the deadline passes. Their
//     results need no flush: each is fsynced to the outbox before it is sent,
//     and one cut off by the restart becomes "interrupted by daemon restart"
//     at the next start (result_outbox_store.go).
//  4. The daemon reports DRAINED and waits for the platform to roll its pod.
//     If nothing has after upgradeRestartWait, it reports FAILED and exits
//     with an upgrade exit (ErrUpgradeExit) so it cannot stay drained forever:
//     the container restarts it, under RestartPolicy Always.
//
// The drain is not a stop and does not run under one: daemon.go's 10s
// shutdown watchdog arms only on a signal, so a drain of minutes is never cut
// short by it. A signal during a drain is a stop, and wins. The upgrade exit's
// own teardown has its own bound (upgradeExitBackstop).
//
// On a person's machine the daemon never drains (owner decision D6: never
// block a user-owned machine): it reports AWAITING_OWNER and keeps serving.
// Its owner's updater acts — Electron's, a package manager, or later the
// self-update primitive — and stops it the way it always does.
//
// Background processes still die with a managed daemon at a restart, as at
// any stop: KillAllRunning (daemon.go) is unchanged, and a pod roll ends
// them anyway. Only a self-update's exec in place hands them to the next
// image (R5: upgrade_self.go, shell/background_survive.go).

const (
	// upgradeMaxDrain caps how long a drain lets running calls go on (D5).
	upgradeMaxDrain = 10 * time.Minute
	// upgradeRestartWait is how long a drained managed daemon waits for its
	// pod to be rolled before it restarts in place.
	upgradeRestartWait = 5 * time.Minute
	// upgradeDrainPoll is how often a drain checks for running calls.
	upgradeDrainPoll = time.Second
	// upgradeExitBackstop bounds the teardown after an upgrade exit: a
	// daemon wedged in shutdown is not restarted.
	upgradeExitBackstop = 20 * time.Second
)

// ErrUpgradeExit is what Start returns when the daemon exited so it can be
// restarted onto a newer build. It is the remedy, not a crash. It carries
// UpgradeExitCode as its ExitCode, which the CLI exits with.
var ErrUpgradeExit error = upgradeExitError{}

// UpgradeExitCode is the process exit status after ErrUpgradeExit: EX_TEMPFAIL,
// non-zero so every supervisor (Kubernetes, Electron, systemd's on-failure)
// restarts the daemon.
const UpgradeExitCode = 75

type upgradeExitError struct{}

func (upgradeExitError) Error() string { return "the daemon exited to be restarted for an update" }

// ExitCode is the process exit status the CLI exits with.
func (upgradeExitError) ExitCode() int { return UpgradeExitCode }

// upgradeDrain is one daemon's drain state. The zero value is ready: a
// daemonClient built as a literal (as tests do) needs no setup.
type upgradeDrain struct {
	mu sync.Mutex
	// status is what the heartbeat reports; nil is CURRENT.
	status *reliantv1.DaemonUpgradeStatus
	// draining is set once a managed daemon starts its drain; from then on
	// it refuses new tool requests until it exits.
	draining bool
	deadline time.Time
	exited   bool
	changed  chan struct{}
	// commands counts admitted daemon commands, including synchronous exec.run.
	commands int
	// teardownDone disarms the exit backstop once Start has finished teardown.
	teardownDone atomic.Bool

	// stopRuntime stops the runtime for the upgrade exit (armUpgrade).
	stopRuntime func()
	// record mirrors the status into the daemon-state record; nil skips it.
	record func(*daemonstate.Upgrade)
	// exit force-exits the process when the upgrade exit's teardown does not
	// finish (upgradeExitBackstop); nil never does.
	exit func(int)

	// Test seams; zero means the constants above.
	maxDrain    time.Duration
	restartWait time.Duration
	poll        time.Duration
	backstop    time.Duration
	// onPoll runs at each pass of the drain's wait loop, and onReturn when
	// runUpgradeDrain returns, so a test can wait on the drain having
	// looked rather than on elapsed time. Set before the drain starts.
	onPoll   func()
	onReturn func()
	// afterFunc arms the upgrade exit's backstop: time.AfterFunc when nil.
	// A test replaces it to see the backstop armed, and to fire it at once.
	afterFunc func(time.Duration, func())

	// Self-update (upgrade_self.go). self is the RestartRequested an
	// auto-updating daemon was sent, nil until one arrives; selfWake wakes
	// its loop when one does. lastActivity is the last tool or terminal
	// activity, unix nanoseconds on now's clock.
	self         *selfRequest
	selfWake     chan struct{}
	lastActivity atomic.Int64

	// Self-update test seams: the clock every self-update wait reads, the
	// background-process count, and the waits; zero means the real clock,
	// the shell manager's processes and the constants.
	now             func() time.Time
	backgroundCount func() int
	stageEvery      time.Duration
	selfRetry       time.Duration
	quietIdle       time.Duration
	quietDrain      time.Duration
}

// armUpgrade gives the drain what its upgrade exit needs: how to stop the
// runtime, where the record is, and the force-exit for a teardown that hangs.
func (d *daemonClient) armUpgrade(stopRuntime func(), dataDir string, exit func(int)) {
	u := &d.upgrade
	u.mu.Lock()
	defer u.mu.Unlock()
	u.stopRuntime = stopRuntime
	u.exit = exit
	if dataDir != "" {
		u.record = func(up *daemonstate.Upgrade) {
			if err := daemonstate.SetUpgrade(dataDir, up); err != nil {
				logging.Warn(logPrefix+" Could not record the upgrade state", "error", err)
			}
		}
	}
}

func (u *upgradeDrain) durations() (maxDrain, restartWait, poll, backstop time.Duration) {
	maxDrain, restartWait, poll, backstop = u.maxDrain, u.restartWait, u.poll, u.backstop
	if maxDrain <= 0 {
		maxDrain = upgradeMaxDrain
	}
	if restartWait <= 0 {
		restartWait = upgradeRestartWait
	}
	if poll <= 0 {
		poll = upgradeDrainPoll
	}
	if backstop <= 0 {
		backstop = upgradeExitBackstop
	}
	return maxDrain, restartWait, poll, backstop
}

// changedCh wakes the heartbeat loop when the status changes. Buffered 1:
// a wake already pending covers a newer change.
func (u *upgradeDrain) changedCh() chan struct{} {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.changed == nil {
		u.changed = make(chan struct{}, 1)
	}
	return u.changed
}

// refusing reports whether new tool requests are refused: a drain began.
func (u *upgradeDrain) refusing() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.draining
}

// exitRequested reports whether the drain ended in an upgrade exit.
func (u *upgradeDrain) exitRequested() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.exited
}

// active reports whether there is a status to report.
func (u *upgradeDrain) active() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.status != nil
}

// heartbeatStatus is the status a heartbeat carries, with the running count
// as of now; nil while CURRENT.
func (u *upgradeDrain) heartbeatStatus(running int) *reliantv1.DaemonUpgradeStatus {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.status == nil {
		return nil
	}
	st := &reliantv1.DaemonUpgradeStatus{
		State:            u.status.GetState(),
		DeadlineUnixMs:   u.status.GetDeadlineUnixMs(),
		RolloutId:        u.status.GetRolloutId(),
		TargetVersion:    u.status.GetTargetVersion(),
		Detail:           u.status.GetDetail(),
		ToolCallsRunning: uint32(max(running, 0)),
	}
	return st
}

// publish replaces the status and wakes the heartbeat loop.
func (u *upgradeDrain) publish(st *reliantv1.DaemonUpgradeStatus) {
	u.mu.Lock()
	u.status = st
	record := u.record
	if u.changed == nil {
		u.changed = make(chan struct{}, 1)
	}
	changed := u.changed
	u.mu.Unlock()

	select {
	case changed <- struct{}{}:
	default:
	}
	if record != nil {
		var deadline time.Time
		if ms := st.GetDeadlineUnixMs(); ms > 0 {
			deadline = time.UnixMilli(ms).UTC()
		}
		record(&daemonstate.Upgrade{
			State:         upgradeStateName(st.GetState()),
			Deadline:      deadline,
			TargetVersion: st.GetTargetVersion(),
			RolloutID:     st.GetRolloutId(),
			Detail:        st.GetDetail(),
		})
	}
}

// upgradeStateName is the record's name for s: the enum name, lowercased,
// without its prefix ("updating").
func upgradeStateName(s reliantv1.DaemonUpgradeState) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "DAEMON_UPGRADE_STATE_"))
}

// awaitingOwner reports whether the daemon already told its owner to
// upgrade: a repeat (the gateway asks again on every new stream) is only
// logged, not printed again.
func (u *upgradeDrain) awaitingOwner() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.status.GetState() == reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_AWAITING_OWNER
}

// defaultUpgradeAdvice is what a person's daemon tells its owner when the
// server names no upgrade command or URL: the desktop app updates the daemon
// it bundles; a CLI install is upgraded the way it was installed.
const defaultUpgradeAdvice = "update the Reliant desktop app, or upgrade the reliant CLI (brew upgrade --cask reliant-labs/reliant/reliant)"

// upgradeAdvice is how the owner of a person's machine upgrades: the
// server's command, else its URL, else defaultUpgradeAdvice.
func upgradeAdvice(req *reliantv1.RestartRequested) string {
	if c := strings.TrimSpace(req.GetUpgradeCommand()); c != "" {
		return c
	}
	if u := strings.TrimSpace(req.GetUpgradeUrl()); u != "" {
		return u
	}
	return defaultUpgradeAdvice
}

// selfUpdateHint is what a person's daemon adds to its upgrade advice when
// its install can replace itself (cliupdate: a self-owned install).
const selfUpdateHint = "This reliant can update itself: run `reliant update`, or start the daemon with --auto-update to have it install new releases at quiet moments."

// awaitingOwnerNotice is the console notice a person's daemon prints when the
// platform asks it to upgrade.
func awaitingOwnerNotice(reason, advice string) string {
	if reason = strings.TrimSpace(reason); reason == "" {
		reason = "a newer version is due"
	}
	return "\n  ! Upgrade needed: " + reason + "\n    Upgrade: " + advice + "\n    This daemon keeps serving until then.\n\n"
}

// drainDeadline is when a drain asked to end at deadlineMs ends: never later
// than maxDrain from now, never earlier than now. 0 means maxDrain from now.
func drainDeadline(deadlineMs int64, now time.Time, maxDrain time.Duration) time.Time {
	limit := now.Add(maxDrain)
	if deadlineMs <= 0 {
		return limit
	}
	d := time.UnixMilli(deadlineMs)
	switch {
	case d.After(limit):
		return limit
	case d.Before(now):
		return now
	default:
		return d
	}
}

// handleRestartRequested acts on the platform's RestartRequested. ctx is the
// runtime's: the drain ends with it.
func (d *daemonClient) handleRestartRequested(ctx context.Context, req *reliantv1.RestartRequested) {
	u := &d.upgrade
	maxDrain, _, _, _ := u.durations()
	now := time.Now()
	logging.InfoContext(ctx, logPrefix+" The platform asked this daemon to restart",
		"target_version", req.GetTargetVersion(), "target_commit", req.GetTargetCommit(),
		"urgency", req.GetUrgency().String(), "rollout_id", req.GetRolloutId(),
		"deadline_unix_ms", req.GetDeadlineUnixMs(), "reason", req.GetReason(), "managed", d.host.Managed())

	if !d.host.Managed() {
		if d.autoUpdating() {
			// Its owner opted in to auto-update: the daemon installs the
			// newest release itself, at a quiet moment (upgrade_self.go).
			d.selfRestartRequested(ctx, req)
			return
		}
		// Never block a person's machine (D6). Nothing here updates it
		// unasked: its owner does — `reliant update`, the desktop app or a
		// package manager — and stops it as they always do. So the owner is
		// told why and how, in the log, on the console once, and in
		// `reliant daemon status` (the detail).
		advice := upgradeAdvice(req)
		logging.WarnContext(ctx, logPrefix+" The platform asked this daemon to upgrade; it keeps serving until its owner does",
			"reason", req.GetReason(), "upgrade", advice)
		selfOwned := d.selfUpdate != nil && d.selfUpdate.SelfOwned()
		if selfOwned {
			logging.WarnContext(ctx, logPrefix+" "+selfUpdateHint)
		}
		if !u.awaitingOwner() {
			notice := awaitingOwnerNotice(req.GetReason(), advice)
			if selfOwned {
				notice = strings.TrimSuffix(notice, "\n") + "    " + selfUpdateHint + "\n\n"
			}
			fmt.Fprint(logging.Notices(), notice)
		}
		u.publish(&reliantv1.DaemonUpgradeStatus{
			State:          reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_AWAITING_OWNER,
			DeadlineUnixMs: req.GetDeadlineUnixMs(),
			RolloutId:      req.GetRolloutId(),
			TargetVersion:  req.GetTargetVersion(),
			Detail:         "this machine is updated by its owner's app or package manager; the daemon keeps serving until then. Upgrade: " + advice,
		})
		return
	}

	deadline := drainDeadline(req.GetDeadlineUnixMs(), now, maxDrain)
	u.mu.Lock()
	if u.draining {
		// A repeat can only bring the deadline forward.
		if deadline.Before(u.deadline) {
			u.deadline = deadline
			if u.status != nil {
				u.status.DeadlineUnixMs = deadline.UnixMilli()
			}
		}
		u.mu.Unlock()
		return
	}
	u.draining = true
	u.deadline = deadline
	u.mu.Unlock()

	u.publish(&reliantv1.DaemonUpgradeStatus{
		State:          reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_UPDATING,
		DeadlineUnixMs: deadline.UnixMilli(),
		RolloutId:      req.GetRolloutId(),
		TargetVersion:  req.GetTargetVersion(),
		Detail:         "draining: new tool calls are refused and sent again after the restart; running ones may finish",
	})
	go d.runUpgradeDrain(ctx, req)
}

// runUpgradeDrain waits for the running calls, reports DRAINED, then waits to
// be restarted (see the top of this file).
func (d *daemonClient) runUpgradeDrain(ctx context.Context, req *reliantv1.RestartRequested) {
	u := &d.upgrade
	if u.onReturn != nil {
		defer u.onReturn()
	}
	_, restartWait, poll, _ := u.durations()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		if u.onPoll != nil {
			u.onPoll()
		}
		running := d.results.runningCount()
		u.mu.Lock()
		deadline := u.deadline
		u.mu.Unlock()
		if running == 0 || !time.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}

	running := d.results.runningCount()
	detail := "drained: no tool call is running; waiting to be restarted"
	if running > 0 {
		detail = fmt.Sprintf("drain deadline passed with %d tool call(s) still running; a restart interrupts them", running)
	}
	logging.InfoContext(ctx, logPrefix+" Drained for the requested restart", "running", running, "rollout_id", req.GetRolloutId())
	u.mu.Lock()
	deadlineMs := u.deadline.UnixMilli()
	u.mu.Unlock()
	u.publish(&reliantv1.DaemonUpgradeStatus{
		State:          reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_DRAINED,
		DeadlineUnixMs: deadlineMs,
		RolloutId:      req.GetRolloutId(),
		TargetVersion:  req.GetTargetVersion(),
		Detail:         detail,
	})

	timer := time.NewTimer(restartWait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return // restarted the expected way: the pod was rolled
	case <-timer.C:
	}
	why := fmt.Sprintf("drained, but the platform did not restart this machine within %s; restarting the daemon in place", restartWait)
	logging.WarnContext(ctx, logPrefix+" "+why, "rollout_id", req.GetRolloutId())
	u.publish(&reliantv1.DaemonUpgradeStatus{
		State:          reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_FAILED,
		DeadlineUnixMs: deadlineMs,
		RolloutId:      req.GetRolloutId(),
		TargetVersion:  req.GetTargetVersion(),
		Detail:         why,
	})
	u.upgradeExit()
}

// upgradeExit stops the runtime so Start returns ErrUpgradeExit, and bounds
// the teardown that follows.
func (u *upgradeDrain) upgradeExit() {
	_, _, _, backstop := u.durations()
	u.mu.Lock()
	if u.exited {
		u.mu.Unlock()
		return
	}
	u.exited = true
	stop, exit, afterFunc := u.stopRuntime, u.exit, u.afterFunc
	u.mu.Unlock()
	if afterFunc == nil {
		afterFunc = func(d time.Duration, f func()) { time.AfterFunc(d, f) }
	}
	if stop != nil {
		stop()
	}
	if exit != nil {
		afterFunc(backstop, func() {
			if u.teardownDone.Load() {
				return
			}
			logging.Error(logPrefix+" Shutdown for an upgrade exit did not finish — force-exiting", "backstop", backstop)
			exit(UpgradeExitCode)
		})
	}
}

// refuseForUpgrade answers a tool request that arrived while the daemon is
// draining, without running it. The answer is a transport error
// (toolexec.ErrorCodeDaemonUpdating): nothing ran, so the sender may send it
// again once the daemon is back.
func (d *daemonClient) refuseForUpgrade(s *gatewaySession, req *reliantv1.ToolRequest) {
	msg := "this machine is restarting for an update and did not run this tool call; it is sent again once the machine is back"
	resp := &reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_ToolResponse{ToolResponse: &reliantv1.ToolResponse{
		RequestId:    req.GetRequestId(),
		Success:      false,
		IsError:      true,
		Content:      msg,
		ErrorMessage: msg,
		ErrorCode:    toolexec.ErrorCodeDaemonUpdating,
	}}}
	if err := d.reply(s, resp); err != nil {
		logSendFailure(logPrefix+" Failed to refuse a tool request during an upgrade drain", err, "request_id", req.GetRequestId())
		return
	}
	logging.Info(logPrefix+" Refused a tool request: draining for a restart",
		"request_id", req.GetRequestId(), "tool", req.GetToolName())
}
