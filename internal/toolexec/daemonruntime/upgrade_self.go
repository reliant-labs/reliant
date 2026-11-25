// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/selfupdate"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/cliupdate"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Self-update: a daemon on a person's machine whose owner opted in
// (`reliant daemon start --auto-update`, owner decision D7) and whose install
// nothing else owns (internal/cliupdate) installs new releases by itself.
// Phase E2 of forge docs/proposals/self-upgrade.md. It never blocks the
// machine (D6): it installs only when nothing is going on.
//
// Without the opt-in nothing here runs: a person's daemon reports
// AWAITING_OWNER on a RestartRequested and keeps serving (upgrade_drain.go),
// and its notice names `reliant update` when the install could take it.
//
// With it, from the moment the daemon starts:
//
//  1. Stage: download the channel's newest release, verify its signature and
//     probe it, while serving as usual; at start and every stageEvery after.
//     STAGED once one is ready. Nothing newer: nothing to report.
//  2. Wait for a quiet moment: no tool call running, no background process
//     alive that the swap would stop, and no tool or terminal activity for
//     quietIdle (10 minutes). A background process this daemon started with
//     file-backed output survives the swap: it is handed to the new image,
//     which adopts it (R5, shell/background_survive.go), so it never holds
//     an update back. One that cannot survive it — on Windows, where there
//     is no exec in place, every one; a credentials-none command; a
//     foreground command moved to the background — is never killed by an
//     update, with one exception: a RestartRequested of security or
//     emergency urgency whose deadline has passed installs even while such
//     processes run (they are stopped, as at any restart) and without
//     waiting out the idle time. A RestartRequested of any other urgency
//     changes nothing: the daemon was going to install anyway.
//  3. Drain (UPDATING), briefly: a request that arrived as the quiet moment
//     was seen may finish, until selfQuietDrain. If work is still running
//     then, the update is declined, nothing is swapped, and the daemon goes
//     back to STAGED for the next quiet moment; a forced one interrupts it.
//  4. DRAINED, then the runtime stops (an upgrade exit) and `daemon start`
//     performs the swap and execs the new binary in this same process
//     (cliupdate.Daemon.ExecPending). The new binary confirms itself once the
//     gateway accepts it, or forge puts the previous one back.
//
// Every wait reads the clock through upgradeDrain.now, so tests drive the
// idle time without waiting it out.

// SelfUpdate is a person's daemon's own updater (cmd wires *cliupdate.Daemon).
type SelfUpdate interface {
	// AutoUpdate reports whether the daemon installs releases itself.
	AutoUpdate() bool
	// SelfOwned reports whether `reliant update` could replace this install.
	SelfOwned() bool
	// Advice is how the owner updates this install by hand.
	Advice() string
	// Stage downloads, verifies and probes the newest release, unless it is
	// staged already, and returns its version (cliupdate.ErrNoUpdate: none).
	Stage(ctx context.Context) (string, error)
	// Apply drains through req.Drain, then calls req.Shutdown.
	Apply(ctx context.Context, req cliupdate.ApplyRequest) error
	// Confirm marks a just-swapped binary healthy.
	Confirm()
}

const (
	// selfQuietIdle is how long the machine must see no tool or terminal
	// activity before a staged release is installed.
	selfQuietIdle = 10 * time.Minute
	// selfQuietDrain bounds the drain of an update: only a request that
	// arrived as the quiet moment was seen can be running, or, when forced,
	// what is cut off after it.
	selfQuietDrain = time.Minute
	// selfRetryWait is how long a daemon whose release could not be staged
	// or installed waits before it tries again.
	selfRetryWait = 30 * time.Minute
)

// errSelfDrainDeclined is the drain's refusal to cut work off.
var errSelfDrainDeclined = errors.New("work was still running at the drain deadline")

// selfRequest is the RestartRequested the self-update has been sent: a
// repeat can raise its urgency and bring its deadline forward, never the
// reverse.
type selfRequest struct {
	urgency    reliantv1.RestartUrgency
	deadlineMs int64
	rolloutID  string
}

func (r *selfRequest) raise(req *reliantv1.RestartRequested) {
	if req.GetUrgency() > r.urgency {
		r.urgency = req.GetUrgency()
	}
	if d := req.GetDeadlineUnixMs(); d > 0 && (r.deadlineMs == 0 || d < r.deadlineMs) {
		r.deadlineMs = d
	}
	if id := req.GetRolloutId(); id != "" {
		r.rolloutID = id
	}
}

// forced reports whether the request may install without a quiet moment:
// security or emergency urgency, past its deadline.
func (r selfRequest) forced(now time.Time) bool {
	return r.urgency >= reliantv1.RestartUrgency_RESTART_URGENCY_SECURITY &&
		r.deadlineMs > 0 && !now.Before(time.UnixMilli(r.deadlineMs))
}

func selfUrgency(u reliantv1.RestartUrgency) selfupdate.Urgency {
	switch u {
	case reliantv1.RestartUrgency_RESTART_URGENCY_NORMAL:
		return selfupdate.Normal
	case reliantv1.RestartUrgency_RESTART_URGENCY_SECURITY:
		return selfupdate.Security
	case reliantv1.RestartUrgency_RESTART_URGENCY_EMERGENCY:
		return selfupdate.Emergency
	}
	return selfupdate.Routine
}

// clock is the time every self-update wait reads.
func (u *upgradeDrain) clock() time.Time {
	if u.now != nil {
		return u.now()
	}
	return time.Now()
}

// markActivity records tool or terminal activity: a quiet moment is
// selfQuietIdle after the last one.
func (u *upgradeDrain) markActivity() {
	u.lastActivity.Store(u.clock().UnixNano())
}

// idleFor is how long it has been since the last activity.
func (u *upgradeDrain) idleFor() time.Duration {
	last := u.lastActivity.Load()
	if last == 0 {
		return time.Duration(1<<63 - 1)
	}
	return u.clock().Sub(time.Unix(0, last))
}

// isActivity reports whether msg is a tool call or terminal use: what a
// person or an agent is doing on the machine right now.
func isActivity(msg *reliantv1.ServerMessage) bool {
	switch m := msg.GetMessage().(type) {
	case *reliantv1.ServerMessage_ToolRequest, *reliantv1.ServerMessage_TerminalInput,
		*reliantv1.ServerMessage_TerminalResize, *reliantv1.ServerMessage_TerminalOutputSubscribe:
		return true
	case *reliantv1.ServerMessage_DaemonCommand:
		name := m.DaemonCommand.GetCommandType()
		return strings.HasPrefix(name, "terminal.") || strings.HasPrefix(name, "exec.") || name == "mcp.call_tool"
	}
	return false
}

// beginCommand atomically admits work against an upgrade drain.
func (u *upgradeDrain) beginCommand() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.draining {
		return false
	}
	u.commands++
	return true
}

func (u *upgradeDrain) endCommand() {
	u.mu.Lock()
	u.commands--
	u.markActivity()
	u.mu.Unlock()
}

func (d *daemonClient) runningWork() int {
	d.upgrade.mu.Lock()
	commands := d.upgrade.commands
	d.upgrade.mu.Unlock()
	return commands + d.results.runningCount()
}

// backgroundRunning counts the background processes still alive that an
// install would stop: one that survives the exec (SurvivesSelfUpdate) does
// not count.
func (d *daemonClient) backgroundRunning() int {
	if d.upgrade.backgroundCount != nil {
		return d.upgrade.backgroundCount()
	}
	n := 0
	for _, p := range shell.GetBackgroundManager().GetAllProcesses() {
		if p.StatusSafe() == "running" && !p.SurvivesSelfUpdate() {
			n++
		}
	}
	return n
}

// autoUpdating reports whether this daemon installs releases itself.
func (d *daemonClient) autoUpdating() bool {
	return !d.host.Managed() && d.selfUpdate != nil && d.selfUpdate.AutoUpdate()
}

// startSelfUpdate runs the self-update for the runtime's lifetime, when this
// daemon installs releases itself.
func (d *daemonClient) startSelfUpdate(ctx context.Context) {
	if !d.autoUpdating() {
		return
	}
	d.upgrade.markActivity() // a start is the first activity: never install right after one
	go d.runSelfUpdate(ctx)
}

// selfRestartRequested takes a RestartRequested on a daemon that installs
// releases itself: it can only make the install sooner.
func (d *daemonClient) selfRestartRequested(ctx context.Context, req *reliantv1.RestartRequested) {
	u := &d.upgrade
	u.mu.Lock()
	if u.self == nil {
		u.self = &selfRequest{}
	}
	u.self.raise(req)
	if u.selfWake == nil {
		u.selfWake = make(chan struct{}, 1)
	}
	wake := u.selfWake
	u.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
	logging.InfoContext(ctx, logPrefix+" Auto-update: the platform asked this daemon to restart; it installs the newest release at its next quiet moment",
		"target_version", req.GetTargetVersion(), "urgency", req.GetUrgency().String())
}

func (d *daemonClient) selfReq() selfRequest {
	u := &d.upgrade
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.self == nil {
		return selfRequest{}
	}
	return *u.self
}

func (u *upgradeDrain) wakeCh() chan struct{} {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.selfWake == nil {
		u.selfWake = make(chan struct{}, 1)
	}
	return u.selfWake
}

func (d *daemonClient) publishSelf(state reliantv1.DaemonUpgradeState, version, detail string, deadlineMs int64) {
	d.upgrade.publish(&reliantv1.DaemonUpgradeStatus{
		State:          state,
		DeadlineUnixMs: deadlineMs,
		RolloutId:      d.selfReq().rolloutID,
		TargetVersion:  version,
		Detail:         detail,
	})
}

func (u *upgradeDrain) selfDurations() (stageEvery, retry, idle, drain time.Duration) {
	stageEvery, retry, idle, drain = u.stageEvery, u.selfRetry, u.quietIdle, u.quietDrain
	if stageEvery <= 0 {
		stageEvery = cliupdate.DefaultStageEvery
	}
	if retry <= 0 {
		retry = selfRetryWait
	}
	if idle <= 0 {
		idle = selfQuietIdle
	}
	if drain <= 0 {
		drain = selfQuietDrain
	}
	return stageEvery, retry, idle, drain
}

// runSelfUpdate stages, waits for a quiet moment and installs, until the
// runtime stops (see the top of this file).
func (d *daemonClient) runSelfUpdate(ctx context.Context) {
	u := &d.upgrade
	if u.onReturn != nil {
		defer u.onReturn()
	}
	stageEvery, retry, _, _ := u.selfDurations()
	for ctx.Err() == nil {
		version, err := d.selfUpdate.Stage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			wait := stageEvery
			if errors.Is(err, cliupdate.ErrNoUpdate) {
				logging.DebugContext(ctx, logPrefix+" Auto-update: nothing to install", "reason", err.Error())
			} else {
				wait = retry
				why := fmt.Sprintf("auto-update could not download and verify the newest release (%v); this daemon keeps serving and tries again in %s", err, retry)
				logging.WarnContext(ctx, logPrefix+" "+why)
				d.publishSelf(reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_FAILED, "", why, 0)
			}
			if !d.selfSleep(ctx, wait) {
				return
			}
			continue
		}
		if done := d.installWhenQuiet(ctx, version); done {
			return
		}
	}
}

// selfSleep waits for wait, a RestartRequested, or the runtime to stop
// (false).
func (d *daemonClient) selfSleep(ctx context.Context, wait time.Duration) bool {
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
	case <-d.upgrade.wakeCh():
	}
	return true
}

// installWhenQuiet waits for each quiet moment and installs version there,
// until it is installed (true) or fails for a reason another quiet moment
// will not fix (false: the caller stages again later).
func (d *daemonClient) installWhenQuiet(ctx context.Context, version string) bool {
	u := &d.upgrade
	stageEvery, retry, idle, drainFor := u.selfDurations()
	refresh := time.NewTimer(stageEvery)
	defer refresh.Stop()
	for {
		d.publishSelf(reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_STAGED, version,
			fmt.Sprintf("%s is downloaded and verified; this daemon keeps serving and installs it at its next quiet moment (no tool call running, no background process the restart would stop, and no activity for %s)", version, idle), 0)
		forced, ok := d.waitForQuiet(ctx, idle, refresh.C)
		if !ok {
			return ctx.Err() != nil // otherwise refresh before considering installation
		}
		deadline := u.clock().Add(drainFor)
		u.mu.Lock()
		u.draining, u.deadline = true, deadline
		u.mu.Unlock()
		d.publishSelf(reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_UPDATING, version,
			"installing "+version+": new tool calls are refused and sent again after the restart", deadline.UnixMilli())

		err := d.selfUpdate.Apply(ctx, cliupdate.ApplyRequest{
			Urgency:  selfUrgency(d.selfReq().urgency),
			Deadline: deadline,
			Drain: func(ctx context.Context, deadline time.Time) (int, error) {
				return d.selfDrain(ctx, deadline, forced, version)
			},
			Shutdown: u.upgradeExit,
		})
		if err == nil {
			return true // the runtime is stopping for the swap
		}
		u.mu.Lock()
		u.draining = false
		u.mu.Unlock()
		if ctx.Err() != nil {
			return true
		}
		if !errors.Is(err, errSelfDrainDeclined) {
			why := fmt.Sprintf("auto-update could not install %s (%v); this daemon keeps serving and tries again later", version, err)
			logging.WarnContext(ctx, logPrefix+" "+why)
			d.publishSelf(reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_FAILED, version, why, 0)
			return !d.selfSleep(ctx, retry)
		}
		logging.InfoContext(ctx, logPrefix+" Auto-update waits for the next quiet moment", "version", version, "reason", err.Error())
	}
}

// quiet reports whether nothing is going on: no tool call, no background
// process, and no activity for idle.
func (d *daemonClient) quiet(idle time.Duration) bool {
	return d.runningWork() == 0 && d.backgroundRunning() == 0 && d.upgrade.idleFor() >= idle
}

// waitForQuiet returns at a quiet moment, or forced when a security request's
// deadline passed first; ok is false on stop or when staging must refresh.
func (d *daemonClient) waitForQuiet(ctx context.Context, idle time.Duration, refresh <-chan time.Time) (forced, ok bool) {
	_, _, poll, _ := d.upgrade.durations()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		if d.upgrade.onPoll != nil {
			d.upgrade.onPoll()
		}
		// Refresh wins over quiet/forced: a restart must not install an old
		// staged candidate when a newer security release is available.
		select {
		case <-refresh:
			return false, false
		case <-d.upgrade.wakeCh():
			return false, false
		default:
		}
		if d.quiet(idle) {
			return false, true
		}
		if d.selfReq().forced(d.upgrade.clock()) {
			return true, true
		}
		select {
		case <-ctx.Done():
			return false, false
		case <-refresh:
			return false, false
		case <-d.upgrade.wakeCh():
			return false, false
		case <-ticker.C:
		}
	}
}

// selfDrain is the Drainer of an update: it lets running work finish until
// deadline, then reports DRAINED, or declines when work is left and the
// update may not cut it off. A forced update does not wait for background
// processes.
func (d *daemonClient) selfDrain(ctx context.Context, deadline time.Time, forced bool, version string) (int, error) {
	_, _, poll, _ := d.upgrade.durations()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		running, bg := d.runningWork(), d.backgroundRunning()
		done := running == 0 && (bg == 0 || forced)
		if done || !d.upgrade.clock().Before(deadline) {
			if !done && !forced {
				return running, fmt.Errorf("%w (%d tool call(s), %d background process(es))", errSelfDrainDeclined, running, bg)
			}
			detail := "drained: installing " + version + " and restarting in place"
			if running > 0 || bg > 0 {
				detail = fmt.Sprintf("installing %s past the platform's deadline: %d tool call(s) and %d background process(es) are interrupted", version, running, bg)
			}
			d.publishSelf(reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_DRAINED, version, detail, deadline.UnixMilli())
			return running, nil
		}
		select {
		case <-ctx.Done():
			return running, ctx.Err()
		case <-ticker.C:
		}
	}
}

// selfUpdateConfirm is called whenever the gateway accepts this daemon: a
// binary that was just swapped in is healthy.
func (d *daemonClient) selfUpdateConfirm() {
	if d.selfUpdate != nil {
		d.selfUpdate.Confirm()
	}
}
