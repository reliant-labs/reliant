// Copyright (c) 2025 Reliant Labs

package daemonactivity

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// What keeps a machine awake.
//
// A cloud machine suspends when it has been idle for its idle timeout (30
// minutes by default). "Idle" is measured from Workspace.Status.LastActivity,
// and the daemon gateway is what refreshes it: it publishes an activity event
// on daemon.v1.state.<daemonID>.activity, the control plane republishes that
// onto the Workspace CR, and the idle detector reads it. So the rules in this
// file ARE the machine's idle policy. Every activity event a daemon's traffic
// produces is an hour a customer pays for, so the policy has to be right in
// both directions.
//
// Activity is something a person or an agent DID to the machine: an agent's
// tool call, a keystroke in a terminal, a command a user or an agent asked
// for. It is attributed to the REQUEST the gateway forwards to the daemon,
// never to the daemon's own traffic back. The daemon talks all the time
// without anyone being there — replies to a poll, PTY output, file-change
// notices, inventories — and none of it says a person or an agent is present.
//
// That rule replaced one that counted every inbound message except the
// heartbeat, and it is why a machine with no user and no agent stayed up for
// 3h44m on 2026-10-11 until a human came back: the server's background
// reconciler asks each machine for the status of its backgrounded processes
// every 30 seconds (exec.bg_status), the daemon's reply counted as activity,
// and so LastActivity never aged. One agent-started `while true; ...; sleep
// 300` loop was enough to keep a machine awake forever. A browser tab left
// open did the same through its terminal reconnects and file-tree refreshes.
//
// Only what the caller explicitly marks as work counts, and anything not
// listed does not. Erring toward "not activity" costs, at worst, an idle
// warning while someone reads without typing; erring the other way costs an
// always-on machine nobody is using, which is the failure this file exists to
// prevent.

// ReasonHeldRunBackgroundWork is the reason the server re-asserts while a
// process an agent backgrounded is still running and the agent's run is held
// on a provider usage limit. The run is not abandoned, only parked off-machine
// until the limit resets, and the process is its work in progress: a deploy or
// a build that a suspend would kill mid-apply (2026-10-28, three times in one
// night). Bounded on both sides, so an agent-started `while true` loop cannot
// pin a machine (see the 2026-10-11 rule above): it counts only while the
// hold is younger than reconciliation.heldRunAwakeFor and the process younger
// than reconciliation.heldRunProcessMaxAge. A process of a run that is not held
// (finished, or waiting on the user) never counts. When nothing is running the
// machine sleeps through the hold and the run wakes it on resume
// (toolexec.WakeForRun). See Reconciler.reassertHeldRunWork.
const ReasonHeldRunBackgroundWork = "background process of a run held on a provider usage limit"

// ReasonToolCallInFlight is the reason the gateway re-asserts while an agent's
// tool call is still running on the machine, so a long test run or build is
// never mistaken for idleness. See ToolsDaemonService.reassertInFlightActivity.
const ReasonToolCallInFlight = "tool call in flight"

// ReasonForServerMessage reports whether forwarding msg to a daemon is
// activity — work a person or an agent asked the machine to do — and the
// human-readable reason that is logged when it keeps the machine awake.
func ReasonForServerMessage(msg *reliantv1.ServerMessage) (string, bool) {
	switch m := msg.GetMessage().(type) {
	case *reliantv1.ServerMessage_ToolRequest:
		// An agent's tool call: the turn is running on this machine.
		return "tool call " + m.ToolRequest.GetToolName(), true
	case *reliantv1.ServerMessage_TerminalInput:
		// A keystroke in an interactive terminal. The terminal's own
		// answers to the shell's queries ride the same channel and are
		// not keystrokes; see IsTerminalReport.
		if IsTerminalReport(m.TerminalInput.GetData()) {
			return "", false
		}
		return "terminal input", true
	case *reliantv1.ServerMessage_DaemonCommand:
		return CommandReason(m.DaemonCommand.GetCommandType())
	case *reliantv1.ServerMessage_KillProcess:
		// A person or an agent stopping a process they started.
		return "process kill", true
	case *reliantv1.ServerMessage_LocalModelHttpRequest:
		// An agent's LLM call relayed to a model server on this machine.
		return "local model request", true
	default:
		// Heartbeats, registration, config watches, terminal resize and
		// output subscriptions, cancels: session plumbing, not work.
		return "", false
	}
}

// CommandReason reports whether a daemon command is activity, with its
// reason. Only the commands in userActionCommands count.
func CommandReason(commandType string) (string, bool) {
	if userActionCommands[commandType] {
		return "command " + commandType, true
	}
	return "", false
}

// IsClassifiedCommand reports whether commandType has a deliberate place in
// the idle policy, as activity or as background traffic. Every command the
// daemon registers must be classified; the daemonruntime test that enforces
// it is what makes adding a command a decision about the idle policy rather
// than an accident of it.
func IsClassifiedCommand(commandType string) bool {
	return userActionCommands[commandType] || backgroundCommands[commandType]
}

// userActionCommands are daemon commands that do work a person or an agent
// explicitly asked for — they create, change or remove something — and that
// no background loop sends on a schedule. Forwarding one keeps the machine
// awake.
var userActionCommands = map[string]bool{
	"auth.open_oauth_helper": true,
	"auth.start_oauth":       true,

	// The user confirmed clearing caches in the "Free up space" dialog.
	"disk.clear_caches": true,

	"exec.bg_kill":  true,
	"exec.bg_start": true,
	"exec.run":      true,

	"forge.deploy_plan_start": true,
	"forge.deploy_start":      true,
	"forge.promote_apply":     true,

	"fs.copy":              true,
	"fs.delete":            true,
	"fs.find_replace":      true,
	"fs.mkdir":             true,
	"fs.patch_file":        true,
	"fs.write_binary_file": true,
	"fs.write_file":        true,

	"git.clone":   true,
	"git.pull":    true,
	"git.reclone": true,

	// A workflow publishing a branch: a commit, then a push.
	"git.publish":         true,
	"git.publish_prepare": true,
	"git.remove":          true,
	// Explicit retained-recovery deletion and separate workspace restoration.
	"git.recovery_delete":       true,
	"worktree.recovery_restore": true,

	"localmodels.set_endpoints": true,

	"mcp.call_tool":      true,
	"mcp.install_bundle": true,
	"mcp.manage_server":  true,
	"mcp.write_config":   true,

	"project.init_files":    true,
	"project.init_git_repo": true,

	"skills.delete_global": true,

	// adopt_move / adopt_restore move a worktree the user chose to import;
	// prune removes the stale records the user asked to clear.
	"worktree.adopt_move":           true,
	"worktree.adopt_restore":        true,
	"worktree.commit":               true,
	"worktree.copy_paths":           true,
	"worktree.create":               true,
	"worktree.create_pr":            true,
	"worktree.delete_directory":     true,
	"worktree.force_cleanup":        true,
	"worktree.prune":                true,
	"worktree.pull":                 true,
	"worktree.push":                 true,
	"worktree.recreate":             true,
	"worktree.remove_workspace_dir": true,
	"worktree.revert":               true,
	"worktree.stage":                true,
	"worktree.unstage":              true,
}

// backgroundCommands are daemon commands that never keep a machine awake:
// reads and status queries (a UI refreshes them on a timer, the server's
// reconciler polls them), session plumbing, and housekeeping that sweepers
// send on their own schedule.
var backgroundCommands = map[string]bool{
	"auth.close_oauth_helper": true,

	// Automatic preservation/verification/reclamation is housekeeping, not
	// evidence of a human or agent actively using the machine.
	"git.recovery_publish":       true,
	"git.recovery_verify":        true,
	"worktree.recovery_observe":  true,
	"worktree.recovery_capture":  true,
	"worktree.recovery_finalize": true,

	// exec.bg_status is the reconciler's 30s poll that kept a machine awake
	// for hours with nobody on it.
	"exec.bg_list":   true,
	"exec.bg_output": true,
	"exec.bg_status": true,

	"forge.deploy_plan":       true,
	"forge.deploy_status":     true,
	"forge.env_shape":         true,
	"forge.env_status":        true,
	"forge.env_verify":        true,
	"forge.project_name":      true,
	"forge.promote_plan":      true,
	"forge.render_capability": true,
	"forge.secret_list":       true,
	"forge.topology":          true,

	"fs.get_tree":         true,
	"fs.glob":             true,
	"fs.list_dir":         true,
	"fs.pdf_page_count":   true,
	"fs.preview_info":     true,
	"fs.read_binary_file": true,
	"fs.read_file":        true,
	"fs.read_pdf_pages":   true,
	"fs.search":           true,
	"fs.stat":             true,

	"mcp.check_resource":   true,
	"mcp.ensure_gitignore": true,
	"mcp.ensure_loaded":    true,
	"mcp.list_tools":       true,
	"mcp.server_status":    true,

	"pkg.list_commands": true,

	"project.check_git":         true,
	"project.check_init_status": true,
	"project.code_presence":     true,
	"project.git_branches":      true,
	"project.git_changes":       true,
	"project.git_info":          true,

	"repo.discover": true,

	"skills.get_home_dir":      true,
	"skills.read_file":         true,
	"skills.read_skill_assets": true,

	// The worker asking what became of a tool call whose stream dropped
	// (toolexec.ToolStatusCommand); the call itself keeps the machine awake.
	"tool.status": true,

	// A terminal is created and re-created by an open tab reconnecting;
	// only typing into it is activity.
	"terminal.close":  true,
	"terminal.create": true,
	"terminal.list":   true,
	"terminal.resize": true,

	// worktree.reconcile, snapshot_remove and delete_branch are sent by the
	// worktree sweeper on its own schedule. discover_repos backs the
	// sidebar's background discovery of worktrees made outside Reliant,
	// which must never wake a machine.
	"worktree.delete_branch":      true,
	"worktree.discover":           true,
	"worktree.discover_repos":     true,
	"worktree.ensure":             true,
	"worktree.generate_repo_id":   true,
	"worktree.get_default_branch": true,
	"worktree.get_pr":             true,
	"worktree.git_changes":        true,
	"worktree.git_commits":        true,
	"worktree.git_status":         true,
	"worktree.import_validate":    true,
	"worktree.reconcile":          true,
	"worktree.snapshot_remove":    true,
	"worktree.validate_path":      true,
}

// IsTerminalReport reports whether terminal input consists only of the
// terminal's automatic replies to escape-sequence queries, rather than
// anything a person typed.
//
// xterm answers a program's queries — device attributes (DA), cursor
// position (CPR), device status, colour queries, focus in/out — by writing
// the answer to its input, exactly where keystrokes go. A shell prompt that
// queries the terminal therefore produces "input" every time a session
// starts, and an open tab that reconnects its terminals every few minutes
// would otherwise keep the machine awake with nobody at the keyboard.
//
// Conservative by construction: data counts as a report only when EVERY byte
// belongs to a recognised reply sequence. A keystroke anywhere in it — and
// arrow keys, which are CSI sequences with final bytes A–D — makes it input.
func IsTerminalReport(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	for i := 0; i < len(data); {
		n := reportLen(data[i:])
		if n == 0 {
			return false
		}
		i += n
	}
	return true
}

// reportLen returns the length of the terminal reply sequence at the start of
// b, or 0 when b does not start with one.
func reportLen(b []byte) int {
	const esc = 0x1b
	if len(b) < 2 || b[0] != esc {
		return 0
	}
	switch b[1] {
	case '[':
		// CSI: parameters, then a final byte. Only finals that are replies
		// qualify: c (DA1/DA2), R (CPR), n (DSR), I/O (focus), t (window
		// report), y (DECRPM, after '$').
		for i := 2; i < len(b); i++ {
			c := b[i]
			switch {
			case c >= '0' && c <= '9', c == ';', c == '?', c == '>', c == '$':
				continue
			case c == 'c', c == 'R', c == 'n', c == 'I', c == 'O', c == 't', c == 'y':
				return i + 1
			default:
				return 0
			}
		}
		return 0
	case ']', 'P':
		// OSC (colour query replies) and DCS (XTGETTCAP replies): run to
		// BEL or to ST (ESC \).
		for i := 2; i < len(b); i++ {
			if b[i] == 0x07 {
				return i + 1
			}
			if b[i] == esc && i+1 < len(b) && b[i+1] == '\\' {
				return i + 2
			}
		}
		return 0
	default:
		return 0
	}
}
