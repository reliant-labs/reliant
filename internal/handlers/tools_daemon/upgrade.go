// Copyright (c) 2026 Reliant Labs
package tools_daemon

import (
	"fmt"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// The drain protocol, from the gateway's side (RestartRequested; the daemon's
// half is daemonruntime/upgrade_drain.go, the senders' toolexec's
// daemon_upgrade.go).
//
//   - RequestDaemonRestart sends a held daemon RestartRequested, only when its
//     registration declared drains_on_restart_request: a daemon that predates
//     the protocol would ignore it, so the caller is told instead.
//   - The daemon reports its state on every heartbeat. While it is UPDATING or
//     DRAINED this gateway sends it no new tool request: each is answered
//     toolexec.ErrDaemonUpdating unsent, which a router treats as undelivered
//     and re-sends once the daemon is back. Everything else — daemon commands,
//     cancels, terminals — still flows: the drain is for tool calls.
//   - tools.online answers with the drain's deadline (DaemonUpdatingUntil), so
//     a waiting call waits for the drain and the restart, not just the 90s a
//     reconnect takes.

// recordUpgradeStatus takes the upgrade status a heartbeat carried. Called
// from the receive loop, so routing stops the moment the heartbeat is read.
func (c *daemonConnection) recordUpgradeStatus(st *reliantv1.DaemonUpgradeStatus) {
	state := st.GetState()
	if state == reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_UNSPECIFIED {
		state = reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_CURRENT
	}
	c.upgradeDeadlineMs.Store(st.GetDeadlineUnixMs())
	prev := reliantv1.DaemonUpgradeState(c.upgradeState.Swap(int32(state)))
	if prev == state {
		return
	}
	if prev == reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_UNSPECIFIED &&
		state == reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_CURRENT {
		return // the first heartbeat of an ordinary stream
	}
	logging.Info(LOG_PREFIX_TOOLS_DAEMON+" Daemon restart state changed",
		"daemonID", c.daemonID, "userID", c.userID,
		"from", prev.String(), "to", state.String(),
		"deadline_unix_ms", st.GetDeadlineUnixMs(), "rollout_id", st.GetRolloutId(),
		"target_version", st.GetTargetVersion(), "tool_calls_running", st.GetToolCallsRunning(),
		"detail", st.GetDetail())
}

// refusesToolRequests reports whether the daemon is draining for a restart,
// so no new tool request may be sent to it.
func (c *daemonConnection) refusesToolRequests() bool {
	switch reliantv1.DaemonUpgradeState(c.upgradeState.Load()) {
	case reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_UPDATING,
		reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_DRAINED:
		return true
	default:
		return false
	}
}

// updatingUntil is when the daemon's drain ends, while it refuses tool
// requests. A drain that named no deadline is given one from now.
func (c *daemonConnection) updatingUntil() (time.Time, bool) {
	if !c.refusesToolRequests() {
		return time.Time{}, false
	}
	if ms := c.upgradeDeadlineMs.Load(); ms > 0 {
		return time.UnixMilli(ms), true
	}
	return time.Now(), true
}

// DaemonUpdatingUntil implements toolexec.DaemonUpgradeReporter.
func (s *Service) DaemonUpdatingUntil(userID, daemonID string) (time.Time, bool) {
	conn, err := s.connForUser(userID, daemonID)
	if err != nil {
		return time.Time{}, false
	}
	return conn.updatingUntil()
}

// RequestDaemonRestart implements toolexec.DaemonRestartRequester: it sends
// req to the daemon, which reports its progress on its heartbeat.
func (s *Service) RequestDaemonRestart(userID, daemonID string, req *reliantv1.RestartRequested) error {
	conn, err := s.connForUser(userID, daemonID)
	if err != nil {
		return err
	}
	if !conn.drainsOnRestartRequest {
		return toolexec.ErrRestartNotSupported
	}
	msg := &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_RestartRequested{RestartRequested: req}}
	if err := s.sendToConn(conn, msg); err != nil {
		return fmt.Errorf("sending the restart request: %w", err)
	}
	logging.Info(LOG_PREFIX_TOOLS_DAEMON+" Asked a daemon to restart",
		"daemonID", daemonID, "userID", userID, "rollout_id", req.GetRolloutId(),
		"target_version", req.GetTargetVersion(), "urgency", req.GetUrgency().String(),
		"deadline_unix_ms", req.GetDeadlineUnixMs())
	return nil
}

// errUpdating is the refusal of a tool request for a draining daemon.
func errUpdating(conn *daemonConnection, requestID string) error {
	return fmt.Errorf("daemon %s is restarting for an update; tool request %s was not sent: %w",
		conn.daemonID, requestID, toolexec.ErrDaemonUpdating)
}
