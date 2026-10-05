// Copyright (c) 2025 Reliant Labs. All rights reserved.
package reconciliation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// BackgroundProcessDaemons is how the reconciler reaches the daemons that run
// backgrounded tool calls. Declared here, at the consumer, and kept to the two
// questions the sweep asks: "what happened to these processes" and "which
// daemons could be running them".
type BackgroundProcessDaemons interface {
	// SendDaemonCommandToDaemon runs a command on ONE named daemon of the user.
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	// ConnectedDaemonIDs lists the user's daemons that are reachable now.
	ConnectedDaemonIDs(ctx context.Context, userID string) ([]string, error)
}

// backgroundProcessDaemonsHolder boxes the interface so it can sit behind an
// atomic.Pointer.
type backgroundProcessDaemonsHolder struct {
	daemons BackgroundProcessDaemons
}

// SetBackgroundProcessDaemons enables the backgrounded-process sweep. Without
// it the sweep is skipped: the reconciler then has no way to learn that a
// process ended, and must not guess. Safe to call while the poll loop runs.
func (r *Reconciler) SetBackgroundProcessDaemons(d BackgroundProcessDaemons) {
	if d == nil {
		r.bgDaemons.Store(nil)
		return
	}
	r.bgDaemons.Store(&backgroundProcessDaemonsHolder{daemons: d})
}

// backgroundProcessDaemons returns the configured daemons, or nil.
func (r *Reconciler) backgroundProcessDaemons() BackgroundProcessDaemons {
	if h := r.bgDaemons.Load(); h != nil {
		return h.daemons
	}
	return nil
}

const (
	// bgStatusTimeoutMs bounds one exec.bg_status round trip. The command
	// reads an in-memory table and does no OS work, so anything slower is a
	// daemon that is not answering; the next pass asks again.
	bgStatusTimeoutMs int32 = 5_000

	// bgStatusBatchSize bounds the ids in one exec.bg_status payload, keeping
	// the request far below the transport's chunking threshold however many
	// calls one daemon owns.
	bgStatusBatchSize = 500

	// processGoneMessage is recorded on a call whose daemon no longer tracks
	// its process. Every way to get there ends the process: a daemon restart
	// kills its children on the way down (KillAllRunning) and starts with an
	// empty registry, and a deleted daemon's machine is gone.
	processGoneMessage = "background process is no longer running on its machine; it was stopped when the daemon restarted or was removed"
)

// reconcileBackgroundedProcesses closes backgrounded tool calls whose process
// has ended.
//
// A backgrounded call (status 6) hands its command to a process that lives in
// ONE daemon's memory. Nothing told the server when that process exited: the
// daemon has no database, and no code path ever moved these rows. Every
// backgrounded shell call therefore stayed at status 6 forever — 4,213 of them
// on one dev database, the oldest two months old — and every chat snapshot
// shipped them as live work, 647 on a single chat open.
//
// This is the one place that settles them, and it is durable by construction:
// it re-derives each call's fate from the daemon that owns it on every pass,
// so a daemon that was offline when its process exited is caught on the pass
// after it reconnects, and a missed message cannot strand a call.
//
// Per call, keyed on (daemon_id, background_process_id):
//
//   - the daemon reports the process ended → close with its real outcome
//     (exit 0 → Completed; anything else → Failed);
//   - the daemon does not know the process → Cancelled. The registry is
//     in-memory, so a daemon that restarted has forgotten it, and the restart
//     killed it;
//   - the daemon row is gone → Cancelled. The owner went away;
//   - the daemon is unreachable → left alone. Unknown is not ended.
//
// Rows from before daemon_id was recorded name no daemon. Those close only
// when EVERY connected daemon of the user affirmatively does not know the
// process (or the user has no daemon at all), and never on an error: closing a
// live dev server would be a lie the user cannot undo.
func (r *Reconciler) reconcileBackgroundedProcesses(ctx context.Context, stats *passStats) (int, error) {
	daemons := r.backgroundProcessDaemons()
	if daemons == nil {
		return 0, nil
	}
	calls, err := r.repo.ListBackgroundedProcessToolCalls(ctx)
	if err != nil {
		logging.Error("[Reconciler] Failed to list backgrounded process tool calls", "error", err)
		return 0, fmt.Errorf("failed to list backgrounded process tool calls: %w", err)
	}
	if len(calls) == 0 {
		return 0, nil
	}

	// Group by owner. A call with a recorded daemon is asked of that daemon;
	// one without is asked of every connected daemon of its user.
	type daemonKey struct{ userID, daemonID string }
	byDaemon := map[daemonKey][]*db.BackgroundedProcessCall{}
	unattributedByUser := map[string][]*db.BackgroundedProcessCall{}
	for _, call := range calls {
		if call.DaemonID != nil && *call.DaemonID != "" {
			k := daemonKey{call.UserID, *call.DaemonID}
			byDaemon[k] = append(byDaemon[k], call)
			continue
		}
		unattributedByUser[call.UserID] = append(unattributedByUser[call.UserID], call)
	}

	closed := 0
	for k, owned := range byDaemon {
		closed += r.settleOwnedProcesses(ctx, stats, daemons, k.userID, k.daemonID, owned)
	}
	for userID, unattributed := range unattributedByUser {
		closed += r.settleUnattributedProcesses(ctx, stats, daemons, userID, unattributed)
	}

	if closed > 0 {
		logging.Info("[Reconciler] Closed backgrounded tool calls whose process had ended", "rows", closed)
	}
	return closed, nil
}

// settleOwnedProcesses closes the calls whose process ran on daemonID.
func (r *Reconciler) settleOwnedProcesses(ctx context.Context, stats *passStats, daemons BackgroundProcessDaemons, userID, daemonID string, calls []*db.BackgroundedProcessCall) int {
	if _, err := r.repo.GetDaemon(ctx, daemonID); errors.Is(err, sql.ErrNoRows) {
		// The daemon was deleted, and its processes with it.
		closed := 0
		for _, call := range calls {
			if r.closeBackgroundedCall(ctx, stats, call, core.ToolCallStatusCancelled, processGoneMessage) {
				closed++
			}
		}
		return closed
	}

	withoutProcess, byProcess := splitByProcessID(calls)
	statuses, unknown, ok := askDaemon(ctx, daemons, userID, daemonID, keys(byProcess))
	if !ok {
		return 0 // unreachable: ask again next pass
	}

	closed := 0
	for processID, info := range statuses {
		status, message, ended := outcomeOfProcess(info)
		if !ended {
			continue
		}
		for _, call := range byProcess[processID] {
			if r.closeBackgroundedCall(ctx, stats, call, status, message) {
				closed++
			}
		}
	}
	for _, processID := range unknown {
		for _, call := range byProcess[processID] {
			if r.closeBackgroundedCall(ctx, stats, call, core.ToolCallStatusCancelled, processGoneMessage) {
				closed++
			}
		}
	}
	// A recorded daemon but no process id: the backgrounding never reported
	// one. The daemon is reachable, so it is up — but nothing addressable is
	// left to ask about, and nothing will ever report for this call.
	for _, call := range withoutProcess {
		if r.closeBackgroundedCall(ctx, stats, call, core.ToolCallStatusCancelled, processGoneMessage) {
			closed++
		}
	}
	return closed
}

// settleUnattributedProcesses closes calls that name no daemon, on proof only.
func (r *Reconciler) settleUnattributedProcesses(ctx context.Context, stats *passStats, daemons BackgroundProcessDaemons, userID string, calls []*db.BackgroundedProcessCall) int {
	daemonIDs, err := daemons.ConnectedDaemonIDs(ctx, userID)
	if err != nil {
		logging.Warn("[Reconciler] Could not list connected daemons; leaving unattributed backgrounded calls alone",
			"userID", userID, "error", err)
		return 0
	}
	if len(daemonIDs) == 0 {
		// Nothing is connected right now. Absence of a connection is not
		// proof the process ended (the machine may be asleep), so wait.
		return 0
	}

	withoutProcess, byProcess := splitByProcessID(calls)
	processIDs := keys(byProcess)

	// A process is provably gone only if EVERY connected daemon answered and
	// none of them is tracking it. One silent daemon could be the owner.
	running := map[string]*daemon.ProcessStatusInfo{}
	for _, daemonID := range daemonIDs {
		statuses, _, ok := askDaemon(ctx, daemons, userID, daemonID, processIDs)
		if !ok {
			return 0
		}
		for id, info := range statuses {
			running[id] = info
		}
	}

	closed := 0
	for processID, owned := range byProcess {
		status, message := core.ToolCallStatusCancelled, processGoneMessage
		if info, tracked := running[processID]; tracked {
			var ended bool
			if status, message, ended = outcomeOfProcess(info); !ended {
				continue
			}
		}
		for _, call := range owned {
			if r.closeBackgroundedCall(ctx, stats, call, status, message) {
				closed++
			}
		}
	}
	// No process id and no daemon: nothing names the process at all, and
	// every daemon that could own it is reachable — nothing will ever report.
	for _, call := range withoutProcess {
		if r.closeBackgroundedCall(ctx, stats, call, core.ToolCallStatusCancelled, processGoneMessage) {
			closed++
		}
	}
	return closed
}

// askDaemon runs exec.bg_status on one daemon. ok is false when the daemon
// could not answer; the caller must then treat every process as unknown-fate,
// NOT as gone.
func askDaemon(ctx context.Context, daemons BackgroundProcessDaemons, userID, daemonID string, processIDs []string) (map[string]*daemon.ProcessStatusInfo, []string, bool) {
	statuses := map[string]*daemon.ProcessStatusInfo{}
	var unknown []string
	for start := 0; start < len(processIDs); start += bgStatusBatchSize {
		end := min(start+bgStatusBatchSize, len(processIDs))
		payload, err := json.Marshal(daemon.ProcessStatusRequest{ProcessIDs: processIDs[start:end]})
		if err != nil {
			return nil, nil, false
		}
		raw, err := daemons.SendDaemonCommandToDaemon(ctx, userID, daemonID, "exec.bg_status", payload, bgStatusTimeoutMs)
		if err != nil {
			logging.Debug("[Reconciler] Daemon did not answer exec.bg_status; will retry next pass",
				"daemonID", daemonID, "error", err)
			return nil, nil, false
		}
		var resp daemon.ProcessStatusResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			logging.Warn("[Reconciler] Unreadable exec.bg_status response", "daemonID", daemonID, "error", err)
			return nil, nil, false
		}
		for i := range resp.Processes {
			statuses[resp.Processes[i].ID] = &resp.Processes[i]
		}
		unknown = append(unknown, resp.Unknown...)
	}
	return statuses, unknown, true
}

// outcomeOfProcess maps a daemon-reported process state to the terminal status
// of the tool call that started it. ended is false while it still runs.
func outcomeOfProcess(info *daemon.ProcessStatusInfo) (status core.ToolCallStatus, message string, ended bool) {
	switch info.Status {
	case "running", "":
		return 0, "", false
	case "completed":
		if info.ExitCode == nil || *info.ExitCode == 0 {
			return core.ToolCallStatusCompleted, "", true
		}
		return core.ToolCallStatusFailed, fmt.Sprintf("background process exited with code %d", *info.ExitCode), true
	case "failed":
		if info.ExitCode != nil {
			return core.ToolCallStatusFailed, fmt.Sprintf("background process exited with code %d", *info.ExitCode), true
		}
		return core.ToolCallStatusFailed, "background process failed", true
	case "killed", "killed_externally":
		// A kill is something someone did to the process, not an error it
		// produced: the same reading CancelToolCall gives a user's stop.
		return core.ToolCallStatusCancelled, "background process was killed", true
	default:
		return core.ToolCallStatusFailed, fmt.Sprintf("background process ended (%s)", info.Status), true
	}
}

// closeBackgroundedCall moves one call off BACKGROUNDED and tells any open
// client. Returns whether the row was written.
//
// Only a row still BACKGROUNDED is closed. UpsertToolCallStatus deliberately
// allows terminal-to-terminal corrections, so without this a user's cancel
// landing between the list and this write would be overwritten with the
// process's exit status. The read/write pair is not atomic, by the same
// accepted trade-off UpsertToolCallStatus documents.
//
// Through UpsertToolCallStatus, so the write inherits what earlier transitions
// recorded (started_at, the process and daemon ids). A status event follows
// the write: without it an open chat keeps showing the process as running
// until reload.
func (r *Reconciler) closeBackgroundedCall(ctx context.Context, stats *passStats, call *db.BackgroundedProcessCall, status core.ToolCallStatus, message string) bool {
	if existing, err := r.repo.GetToolCall(ctx, call.ToolCallID); err == nil && existing != nil &&
		existing.Status != core.ToolCallStatusBackgrounded {
		return false
	}

	now := time.Now().UTC()
	row := &db.ToolCall{
		ID:          call.ToolCallID,
		ChatID:      call.ChatID,
		ToolName:    call.ToolName,
		Status:      status,
		CompletedAt: &now,
		UpdatedAt:   now,
	}
	if message != "" {
		row.ErrorMessage = &message
	}
	if err := db.UpsertToolCallStatus(ctx, r.repo, row); err != nil {
		logging.Error("[Reconciler] Failed to close backgrounded tool call",
			"toolCallID", call.ToolCallID, "status", status, "error", err)
		return false
	}

	if err := r.repo.EmitToolCallUpdate(ctx, call.ChatID, db.ToolCallUpdate{
		ToolCallID:  call.ToolCallID,
		ToolName:    call.ToolName,
		Status:      db.ToolCallStatus(toolCallStatusEventName(status)),
		Timestamp:   now.Format(time.RFC3339),
		CompletedAt: now.Format(time.RFC3339Nano),
	}); err != nil {
		// The durable row is what a reload and the snapshot read; the event
		// only spares an open client the wait.
		logging.Warn("[Reconciler] Closed backgrounded tool call but could not emit its status",
			"toolCallID", call.ToolCallID, "error", err)
	}
	r.recordAnomaly(stats, anomalyBackgroundedProcessClosed)
	return true
}

// toolCallStatusEventName is the chat_updates status string for a terminal
// durable status — the vocabulary the live stream uses.
func toolCallStatusEventName(status core.ToolCallStatus) string {
	switch status {
	case core.ToolCallStatusCompleted:
		return string(db.ToolCallStatusCompleted)
	case core.ToolCallStatusCancelled:
		return string(db.ToolCallStatusCancelled)
	default:
		return string(db.ToolCallStatusFailed)
	}
}

// splitByProcessID separates calls that name a process from those that do not.
func splitByProcessID(calls []*db.BackgroundedProcessCall) (without []*db.BackgroundedProcessCall, byProcess map[string][]*db.BackgroundedProcessCall) {
	byProcess = map[string][]*db.BackgroundedProcessCall{}
	for _, call := range calls {
		if call.BackgroundProcessID == nil || *call.BackgroundProcessID == "" {
			without = append(without, call)
			continue
		}
		id := *call.BackgroundProcessID
		byProcess[id] = append(byProcess[id], call)
	}
	return without, byProcess
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
