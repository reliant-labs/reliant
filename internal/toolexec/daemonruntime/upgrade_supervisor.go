// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"encoding/json"
	"io"
	"os"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

// Telling the desktop app (phase E1 of the self-upgrade proposal, forge
// docs/proposals/self-upgrade.md §4, "Who acts, by runtime").
//
// A daemon the desktop app spawned runs the binary inside the app's signed
// bundle. It is a person's daemon, so on a RestartRequested it reports
// AWAITING_OWNER and keeps serving (upgrade_drain.go), and its owner is the
// app's updater. The app hears of the request on the daemon's stdout, the
// only channel it reads besides the daemon-state record: one
// `@@RELIANT_UPGRADE {json}` line (daemonstate.UpgradeNoticePrefix), on which
// it checks for an update, downloads it and asks the user to restart. A
// version floor reaches the daemon the same way: the gateway turns it into a
// RestartRequested (internal/handlers/tools_daemon/version_floor.go).

// supervisedByDesktop reports whether the desktop app spawned this daemon and
// reads its stdout for upgrade notices.
func (d *daemonClient) supervisedByDesktop() bool {
	return strings.TrimSpace(d.bootCfg.Supervisor) == daemonstate.SupervisorElectron
}

// upgradeNoticeOf is the notice the desktop app reads for req.
func upgradeNoticeOf(req *reliantv1.RestartRequested) daemonstate.UpgradeNotice {
	return daemonstate.UpgradeNotice{
		TargetVersion:  req.GetTargetVersion(),
		TargetCommit:   req.GetTargetCommit(),
		CommitTimeUnix: req.GetCommitTimeUnix(),
		Urgency:        restartUrgencyName(req.GetUrgency()),
		DeadlineUnixMs: req.GetDeadlineUnixMs(),
		RolloutID:      req.GetRolloutId(),
		Reason:         req.GetReason(),
		UpgradeCommand: req.GetUpgradeCommand(),
		UpgradeURL:     req.GetUpgradeUrl(),
	}
}

// restartUrgencyName is the notice's name for u: the enum name, lowercased,
// without its prefix ("security"). An urgency newer than this build reads as
// its number's name, which the desktop app treats as normal.
func restartUrgencyName(u reliantv1.RestartUrgency) string {
	return strings.ToLower(strings.TrimPrefix(u.String(), "RESTART_URGENCY_"))
}

// tellSupervisor prints req's upgrade notice for the desktop app that spawned
// this daemon, once per distinct request: the gateway asks again on every new
// stream, and a repeat is not news. A request that differs in anything (a
// sooner deadline, a higher urgency, another target) is printed again, so the
// app can act on the change. Nothing is printed when the desktop app did not
// spawn this daemon.
func (d *daemonClient) tellSupervisor(req *reliantv1.RestartRequested) {
	if !d.supervisedByDesktop() {
		return
	}
	line, err := json.Marshal(upgradeNoticeOf(req))
	if err != nil {
		// A struct of strings and integers always marshals.
		logging.Warn(logPrefix+" Could not encode the upgrade notice for the desktop app", "error", err)
		return
	}
	u := &d.upgrade
	u.mu.Lock()
	if u.supervisorNotice == string(line) {
		u.mu.Unlock()
		return
	}
	u.supervisorNotice = string(line)
	out := u.supervisorOut
	u.mu.Unlock()
	if out == nil {
		out = os.Stdout
	}
	// Written straight to stdout, as the stream notice is (runtime.go), so
	// no logger change can reformat, filter or reroute it. One write per
	// line, so it never interleaves with another.
	if _, err := io.WriteString(out, daemonstate.UpgradeNoticePrefix+" "+string(line)+"\n"); err != nil {
		logging.Warn(logPrefix+" Could not tell the desktop app about the requested upgrade", "error", err)
	}
}
