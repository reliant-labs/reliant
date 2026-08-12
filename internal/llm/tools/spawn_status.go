// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/threads"
)

type SpawnAgentInfo struct {
	AgentID      string `json:"agent_id"`
	Title        string `json:"title,omitempty"`
	Preset       string `json:"preset,omitempty"`
	Status       string `json:"status"`
	ElapsedMs    int64  `json:"elapsed_ms"`
	LastActivity string `json:"last_activity,omitempty"`
	TurnCount    int    `json:"turn_count"`
	GatedOnAgent bool   `json:"gated,omitempty"`
	GateReason   string `json:"gate_reason,omitempty"`
	GateUnknown  bool   `json:"gate_unknown,omitempty"`
}

type SpawnStatusParams struct {
	// AgentID selects a single agent to inspect and (optionally) wait on. If
	// omitted, this lists ALL of the caller's own sub-agents.
	AgentID string `json:"agent_id,omitempty" jsonschema:"description=The agent_id (thread id) of a sub-agent you spawned. Omit to list all of your sub-agents."`
	// Wait blocks until agent_id reaches a terminal state, or the timeout
	// budget elapses — mirrors bash_wait. Requires agent_id.
	Wait bool `json:"wait,omitempty" jsonschema:"description=Block until the agent reaches a terminal state (completed/failed/cancelled/expired). Requires agent_id. Ignored when listing all agents."`
	// TimeoutSeconds bounds THIS call, not the agent's total runtime.
	// Timing out is not an error — see bash_wait's TimeoutSeconds doc.
	TimeoutSeconds int `json:"timeout_seconds,omitempty" jsonschema:"description=Maximum seconds to block when wait is true (default: 1200, maximum: 1200). Timing out does NOT stop the agent — call again to keep waiting."`
}

type SpawnStatusResponseMetadata struct {
	// Populated in listing mode (no agent_id).
	TotalAgents   int `json:"total_agents,omitempty"`
	RunningAgents int `json:"running_agents,omitempty"`

	// Populated in single-agent mode (agent_id set).
	AgentID   string `json:"agent_id,omitempty"`
	Status    string `json:"status,omitempty"`
	HasExited bool   `json:"has_exited,omitempty"`
	// WaitedMs is how long this call blocked when wait was requested.
	WaitedMs int64 `json:"waited_ms,omitempty"`
	// TimedOut reports that a wait's budget elapsed with the agent still
	// running. Distinct from an error: the agent is fine and the call can be
	// repeated.
	TimedOut bool `json:"timed_out,omitempty"`
}

type spawnStatusTool struct {
	repo    db.Repository
	threads *threads.Service
}

const (
	SpawnStatusToolName = "spawn_status"

	// spawnStatusDefaultTimeout/MaxTimeout are bash_wait's budget, shared via
	// MaxBlockingToolWait so the two blocking waiters cannot drift apart:
	// toolexec.DefaultToolTimeout is derived from that constant with headroom,
	// so both tools return "still running, call again" before the executor
	// cancels them mid-flight.
	//
	// Waiting on an AGENT is the case that made 4 minutes wrong. A background
	// agent routinely works for tens of minutes, so a short budget turned a
	// single "wait for my child" into a string of polls that reported nothing
	// new each time.
	spawnStatusDefaultTimeout = MaxBlockingToolWait
	spawnStatusMaxTimeout     = MaxBlockingToolWait

	// spawnStatusPollInterval trades responsiveness against DB chatter. The
	// wait is server-side, so this costs no model round-trips: it is a loop
	// in one tool call, not one call per poll.
	spawnStatusPollInterval = 500 * time.Millisecond

	spawnStatusDescription = `Check on the sub-agents you (the calling thread) have spawned — list them all, or inspect and optionally wait on one.

WORKSPACE SCOPING:
- Only shows/waits on agents YOU spawned (your direct children), never a
  sibling's or another thread's sub-agents.

TWO MODES:
1. LISTING (omit agent_id): returns every sub-agent you spawned — agent_id,
   title, preset, status, elapsed time, last activity time, turn count, and
   whether it appears gated on a question or approval (best-effort — this
   signal can be unavailable and is never a hard guarantee).
2. SINGLE AGENT (agent_id set): returns that agent's status and its LAST
   ASSISTANT MESSAGE — "is it done, and what did it say?" without cancelling
   it.

WAITING:
Set wait: true with agent_id to block server-side until that agent reaches a
terminal state (completed/failed/cancelled/expired), instead of polling this
tool yourself. One call, no round-trips, no lost work — the same shape as
bash_wait.

TIMEOUTS ARE NOT FAILURES:
If the agent is still running when the budget elapses, this returns normally
with timed_out: true and the agent untouched. Call spawn_status again with
wait: true to keep waiting.

Use spawn_send to message an agent that is still running.`
)

func NewSpawnStatusTool(repo db.Repository) Tool {
	return NewToolWrapper[SpawnStatusParams, ToolResponse](&spawnStatusTool{repo: repo, threads: threads.NewService(repo)})
}

func (s *spawnStatusTool) Name() string {
	return SpawnStatusToolName
}

func (s *spawnStatusTool) Description() string {
	return spawnStatusDescription
}

func (s *spawnStatusTool) RequiresPermission(params SpawnStatusParams) (bool, error) {
	return false, nil
}

func (s *spawnStatusTool) Execute(rctx *rctx.ToolContext, params SpawnStatusParams) (ToolResponse, error) {
	if s.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}
	threadID := rctx.Thread
	if threadID == "" {
		return NewTextErrorResponse("Thread context required"), nil
	}

	if params.AgentID == "" {
		return s.listChildren(rctx, threadID)
	}
	return s.singleAgent(rctx, threadID, params)
}

// listChildren returns every sub-agent the caller has spawned, with
// best-effort gating signals.
func (s *spawnStatusTool) listChildren(rctx *rctx.ToolContext, threadID string) (ToolResponse, error) {
	children, err := s.repo.ListSpawnChildren(rctx.Context, threadID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to list spawned agents: %v", err)), nil
	}
	if len(children) == 0 {
		return NewTextResponse("No sub-agents spawned from this thread."), nil
	}

	// Best-effort gating signal: pending questions and approvals, keyed by
	// thread. Neither call failing (nor approvals.thread_id being NULL on a
	// row) should block the listing — gating is advisory, not load-bearing.
	pendingQuestions, qErr := s.repo.PendingQuestionsByThread(rctx.Context, rctx.ChatID)
	if qErr != nil {
		pendingQuestions = nil
	}
	pendingApprovalThreads := map[string]bool{}
	approvalSignalAvailable := true
	if approvals, aErr := s.repo.ListPendingApprovalsByChat(rctx.Context, rctx.ChatID); aErr == nil {
		for _, a := range approvals {
			if a.ThreadID != nil && *a.ThreadID != "" {
				pendingApprovalThreads[*a.ThreadID] = true
			}
		}
	} else {
		approvalSignalAvailable = false
	}

	lastActivity, laErr := s.repo.LastThreadActivityByChat(rctx.Context, rctx.ChatID)
	if laErr != nil {
		lastActivity = nil
	}

	now := time.Now()
	infos := make([]SpawnAgentInfo, 0, len(children))
	runningCount := 0

	for _, child := range children {
		info := SpawnAgentInfo{
			Status: toolCallStatusLabel(child.ToolCallStatus),
		}

		preset, title := spawnInputPresetAndTitle(child.ToolInput)
		info.Preset = preset
		info.Title = title

		if child.ChildThreadID != nil {
			info.AgentID = *child.ChildThreadID
		}
		if child.ThreadTitle != nil && *child.ThreadTitle != "" {
			info.Title = *child.ThreadTitle
		}
		if info.AgentID == "" {
			// Child rows have not landed yet (dispatch race) — the tool
			// call itself is the only identifier available so far.
			info.AgentID = child.ToolCallID
			info.Status = "starting"
		}

		elapsedEnd := now
		if child.CompletedAt != nil {
			elapsedEnd = *child.CompletedAt
		} else if child.WorkflowCompleted != nil {
			elapsedEnd = *child.WorkflowCompleted
		}
		info.ElapsedMs = elapsedEnd.Sub(child.RequestedAt).Milliseconds()
		if info.ElapsedMs < 0 {
			info.ElapsedMs = 0
		}

		isRunning := child.WorkflowStatus != nil && workflowStatusIsLive(*child.WorkflowStatus)
		if isRunning {
			runningCount++
			if info.Status != "starting" {
				info.Status = "running"
			}
		} else if child.WorkflowStatus != nil {
			info.Status = workflowStatusLabel(*child.WorkflowStatus)
		}

		if info.AgentID != "" {
			if last, ok := lastActivity[info.AgentID]; ok {
				info.LastActivity = last.Format(time.RFC3339)
				info.TurnCount = 0 // populated below via CountMessagesInThread
			}
			if count, cErr := s.repo.CountMessagesInThread(rctx.Context, info.AgentID); cErr == nil {
				info.TurnCount = count
			}
			if q, ok := pendingQuestions[info.AgentID]; ok && q != nil {
				info.GatedOnAgent = true
				info.GateReason = "pending question"
			} else if pendingApprovalThreads[info.AgentID] {
				info.GatedOnAgent = true
				info.GateReason = "pending approval"
			}
		}
		if !approvalSignalAvailable {
			info.GateUnknown = true
		}

		infos = append(infos, info)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== Sub-Agents (%d) ===\n\n", len(infos)))
	for i, info := range infos {
		if i > 0 {
			sb.WriteString("\n---\n\n")
		}
		fmt.Fprintf(&sb, "agent_id: %s\n", info.AgentID)
		if info.Title != "" {
			fmt.Fprintf(&sb, "Title: %s\n", info.Title)
		}
		if info.Preset != "" {
			fmt.Fprintf(&sb, "Preset: %s\n", info.Preset)
		}
		fmt.Fprintf(&sb, "Status: %s\n", info.Status)
		fmt.Fprintf(&sb, "Elapsed: %s\n", formatDuration(time.Duration(info.ElapsedMs)*time.Millisecond))
		if info.LastActivity != "" {
			fmt.Fprintf(&sb, "Last activity: %s\n", info.LastActivity)
		}
		fmt.Fprintf(&sb, "Turns: %d\n", info.TurnCount)
		if info.GatedOnAgent {
			fmt.Fprintf(&sb, "Gated: %s\n", info.GateReason)
		} else if info.GateUnknown {
			sb.WriteString("Gated: unknown (gating signal unavailable)\n")
		}
	}

	metadata := SpawnStatusResponseMetadata{
		TotalAgents:   len(infos),
		RunningAgents: runningCount,
	}
	return WithResponseMetadata(NewTextResponse(sb.String()), metadata), nil
}

// singleAgent reports one agent's status and last assistant message, with an
// optional server-side wait for its thread to reach a terminal status.
func (s *spawnStatusTool) singleAgent(rctx *rctx.ToolContext, threadID string, params SpawnStatusParams) (ToolResponse, error) {
	// Verify ownership BEFORE committing to any wait — a wait on a
	// nonexistent or not-owned agent must fail fast, not park for the full
	// budget only to report "not found".
	if err := verifyIsOwnChild(rctx, s.repo, threadID, params.AgentID); err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	if params.Wait {
		return s.waitForTerminal(rctx, params)
	}
	return s.lastMessageSnapshot(rctx, params, 0)
}

// waitForTerminal polls the agent's thread status server-side until it goes
// terminal or the budget elapses. Modeled directly on bash_wait's loop.
func (s *spawnStatusTool) waitForTerminal(rctx *rctx.ToolContext, params SpawnStatusParams) (ToolResponse, error) {
	budget := spawnStatusDefaultTimeout
	if params.TimeoutSeconds > 0 {
		budget = time.Duration(params.TimeoutSeconds) * time.Second
		if budget > spawnStatusMaxTimeout {
			// Clamp rather than reject: the caller wants to wait longer, and
			// the right answer is to wait as long as one call allows and
			// tell them to call again — not to fail and make them guess a
			// number.
			budget = spawnStatusMaxTimeout
		}
	}

	start := time.Now()
	deadline := start.Add(budget)

	thread, err := s.repo.GetThread(rctx.Context, params.AgentID)
	if err != nil || thread == nil {
		return NewTextErrorResponse(fmt.Sprintf("Agent %q could not be found.", params.AgentID)), nil
	}

	for {
		if core.ThreadStatusIsTerminal(thread.Status) {
			return s.terminalResponse(rctx, thread, params, time.Since(start))
		}

		if time.Now().After(deadline) {
			return s.stillRunningResponse(thread, time.Since(start))
		}

		// Honour cancellation of the surrounding tool call. Without this the
		// loop would keep polling the DB for a call nobody is waiting on.
		select {
		case <-rctx.Done():
			return s.stillRunningResponse(thread, time.Since(start))
		case <-time.After(spawnStatusPollInterval):
		}

		refreshed, refreshErr := s.repo.GetThread(rctx.Context, params.AgentID)
		if refreshErr != nil || refreshed == nil {
			// A transient read failure is not a reason to abandon a wait
			// that may be nearly done; keep the last known state and retry.
			continue
		}
		thread = refreshed
	}
}

func (s *spawnStatusTool) terminalResponse(rctx *rctx.ToolContext, thread *db.Thread, params SpawnStatusParams, waited time.Duration) (ToolResponse, error) {
	resp, err := s.lastMessageSnapshot(rctx, params, waited.Milliseconds())
	if err != nil {
		return resp, err
	}
	prefix := fmt.Sprintf("Agent %s reached %s after %s.\n\n", thread.ID, core.ThreadStatusLabel(thread.Status), formatWaitDuration(waited))
	resp.Content = prefix + resp.Content
	return resp, nil
}

// stillRunningResponse is a SUCCESSFUL result, not an error: the agent is
// healthy and the only news is that it is not done yet. Returning an error
// here would train the model to treat a slow sub-agent as a failure.
func (s *spawnStatusTool) stillRunningResponse(thread *db.Thread, waited time.Duration) (ToolResponse, error) {
	output := fmt.Sprintf(
		"Agent %s is STILL RUNNING after %s — not an error, and it has not been stopped.\n\n"+
			"Call spawn_status(agent_id=%q, wait=true) again to keep waiting, or omit wait to peek at its progress so far.",
		thread.ID, formatWaitDuration(waited), thread.ID)

	metadata := SpawnStatusResponseMetadata{
		AgentID:   thread.ID,
		Status:    core.ThreadStatusLabel(thread.Status),
		HasExited: false,
		WaitedMs:  waited.Milliseconds(),
		TimedOut:  true,
	}
	return WithResponseMetadata(NewTextResponse(output), metadata), nil
}

// lastMessageSnapshot returns an agent's status and its last assistant
// message — "is it done, and what did it say?" rather than a windowed
// transcript. Reuses threads.Service.LastAssistantMessage, the same
// extraction the FetchThreadResult workflow activity uses to hand a spawn's
// result back to its parent. waitedMs is folded into the metadata when this
// is called as part of a completed wait.
func (s *spawnStatusTool) lastMessageSnapshot(rctx *rctx.ToolContext, params SpawnStatusParams, waitedMs int64) (ToolResponse, error) {
	thread, err := s.repo.GetThread(rctx.Context, params.AgentID)
	if err != nil || thread == nil {
		return NewTextErrorResponse(fmt.Sprintf("Agent %q could not be found.", params.AgentID)), nil
	}
	metadata := SpawnStatusResponseMetadata{
		AgentID:   params.AgentID,
		Status:    core.ThreadStatusLabel(thread.Status),
		HasExited: core.ThreadStatusIsTerminal(thread.Status),
		WaitedMs:  waitedMs,
	}

	result, err := s.threads.LastAssistantMessage(rctx.Context, params.AgentID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to read agent activity: %v", err)), nil
	}
	if !result.Found {
		return WithResponseMetadata(NewTextResponse(fmt.Sprintf("Agent %s has no activity yet.", params.AgentID)), metadata), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "=== %s: %s ===\n\n", params.AgentID, metadata.Status)
	if result.Warning != "" {
		fmt.Fprintf(&sb, "[WORKFLOW_WARNING] %s\n\nLast response before warning:\n", result.Warning)
	}
	content := result.Content
	if content == "" {
		content = "(no text response)"
	}
	sb.WriteString(content)

	output := sb.String()
	if truncated := TruncateOutput(SpawnStatusToolName, output, true); truncated != output {
		output = truncated
	}
	return WithResponseMetadata(NewTextResponse(output), metadata), nil
}

// verifyIsOwnChild confirms agentID names a spawn child of callerThreadID —
// tool_calls.thread_id is always the parent's, so a match here is proof of
// ownership, not just a plausible id.
func verifyIsOwnChild(rctx *rctx.ToolContext, repo db.Repository, callerThreadID, agentID string) error {
	if agentID == "" {
		return fmt.Errorf("agent_id is required")
	}
	children, err := repo.ListSpawnChildren(rctx.Context, callerThreadID)
	if err != nil {
		return fmt.Errorf("failed to verify agent ownership: %w", err)
	}
	for _, child := range children {
		if child.ChildThreadID != nil && *child.ChildThreadID == agentID {
			return nil
		}
	}
	return fmt.Errorf("agent_id %q is not a sub-agent spawned from this thread. Use spawn_status (omit agent_id) to see your sub-agents.", agentID)
}

// spawnInputPresetAndTitle extracts the preset and title the LLM originally
// requested from the spawn tool call's raw JSON input. Best-effort: malformed
// or absent input yields empty strings rather than an error, since a listing
// tool should degrade rather than fail on one bad row.
func spawnInputPresetAndTitle(raw []byte) (preset, title string) {
	if len(raw) == 0 {
		return "", ""
	}
	var input struct {
		Preset string `json:"preset"`
		Title  string `json:"title"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", ""
	}
	return input.Preset, input.Title
}

// toolCallStatusLabel renders core.ToolCallStatus for display.
func toolCallStatusLabel(status int32) string {
	switch status {
	case 1:
		return "pending"
	case 2:
		return "executing"
	case 3:
		return "completed"
	case 4:
		return "failed"
	case 5:
		return "cancelled"
	case 6:
		return "backgrounded"
	default:
		return "unknown"
	}
}

// workflowStatusIsLive mirrors workflowStatusIsTerminal's complement: a
// workflow status that has not reached a terminal state is still doing work
// (or paused, which will resume).
func workflowStatusIsLive(status int32) bool {
	switch status {
	case int32(db.WorkflowStatusPending), int32(db.WorkflowStatusRunning), int32(db.WorkflowStatusPaused):
		return true
	default:
		return false
	}
}

func workflowStatusLabel(status int32) string {
	switch status {
	case int32(db.WorkflowStatusPending):
		return "pending"
	case int32(db.WorkflowStatusRunning):
		return "running"
	case int32(db.WorkflowStatusCompleted):
		return "completed"
	case int32(db.WorkflowStatusFailed):
		return "failed"
	case int32(db.WorkflowStatusCancelled):
		return "cancelled"
	case int32(db.WorkflowStatusPaused):
		return "paused"
	case int32(db.WorkflowStatusExpired):
		return "expired"
	default:
		return "unknown"
	}
}
