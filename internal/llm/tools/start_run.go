// Copyright (c) 2025 Reliant Labs
package tools

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/ctxkeys"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type StartRunParams struct {
	Workflow  string            `json:"workflow" jsonschema:"required,description=Workflow to run, e.g. builtin://agent or a project workflow slug. list_workflows shows what is available."`
	Message   string            `json:"message" jsonschema:"required,description=The task for the new run. It is the first user message the run sees."`
	ProjectID string            `json:"project_id,omitempty" jsonschema:"description=Project to run in. Defaults to the project of the run you are executing in."`
	Inputs    map[string]any    `json:"inputs,omitempty" jsonschema:"description=Workflow input parameters, keyed by input name."`
	Presets   map[string]string `json:"presets,omitempty" jsonschema:"description=Preset selections, keyed by preset slot."`
	Title     string            `json:"title,omitempty" jsonschema:"description=Title for the new run. Generated from the message when omitted."`
}

const (
	StartRunToolName    = "start_run"
	startRunDescription = `Start a NEW top-level run — a separate chat the user can open and watch — and return immediately with its ids.

This is not a sub-agent. A sub-agent (the agent tool) runs inside YOUR execution and reports back to you; a run started here is detached: it has its own chat, its own workflow and its own lifetime, and it keeps going whether or not you do. Use it for standing or parallel work that should outlive this conversation. Use the agent tool for work you need the answer to.

The run belongs to the user, who can see it in the sidebar and stop it. It is NOT unattended: if it asks a question, it waits for the human.

RETURNS the new chat id (also its run id for list_runs, get_run, control_run and send_to_run). The run is started, not finished — check on it with get_run.

Calling this again with the same tool call (a retry) attaches to the run already started instead of starting a second one.

LIMITS: an agent-started run may start further runs, but only 3 levels deep, and the user may have at most 10 agent-started runs live at once. Past either limit this fails; finish or cancel something first.`
)

type startRunTool struct {
	repo    db.Repository
	starter RunStarter
}

func NewStartRunTool(repo db.Repository, starter RunStarter) Tool {
	return NewToolWrapper[StartRunParams, ToolResponse](&startRunTool{repo: repo, starter: starter})
}

func (s *startRunTool) Name() string        { return StartRunToolName }
func (s *startRunTool) Description() string { return startRunDescription }

func (s *startRunTool) RequiresPermission(params StartRunParams) (bool, error) {
	return false, nil
}

// StartRunResponseMetadata is the machine-readable half of start_run's result.
type StartRunResponseMetadata struct {
	RunID          string `json:"run_id"`
	ChatID         string `json:"chat_id"`
	TemporalRunID  string `json:"temporal_run_id,omitempty"`
	AlreadyStarted bool   `json:"already_started,omitempty"`
}

func (s *startRunTool) Execute(rctx *rctx.ToolContext, params StartRunParams) (ToolResponse, error) {
	caller, errResp := resolveRunCaller(rctx, s.repo)
	if errResp != nil {
		return *errResp, nil
	}
	if s.starter == nil {
		return NewTextErrorResponse("Starting a run is not available in this runtime."), nil
	}
	if strings.TrimSpace(params.Workflow) == "" {
		return NewTextErrorResponse("workflow is required"), nil
	}
	if strings.TrimSpace(params.Message) == "" {
		return NewTextErrorResponse("message is required"), nil
	}

	projectID := params.ProjectID
	if projectID == "" {
		projectID = caller.chat.ProjectID
	}

	// The tool call id is the dedupe key's identity, so a retried
	// ExecuteTools activity re-issuing the SAME call lands on the same
	// trigger_events row. The calling chat is part of the key because tool
	// call ids are minted by the model and only unique within a conversation.
	toolCallID := currentToolCallID(rctx)
	if toolCallID == "" {
		// No stable identity to dedupe on; the launch still records lineage.
		toolCallID = uuid.NewString()
	}
	dedupeKey := rctx.ChatID + ":" + toolCallID

	// A retry of a start that already happened is answered from the event
	// row, BEFORE the caps: a retry must not be refused for the very run it
	// is trying to report.
	if existing, err := s.repo.GetTriggerEventByDedupe(rctx.Context, core.TriggerEventKindAgentStartRun, dedupeKey); err == nil {
		if existing.ChatID != nil && *existing.ChatID != "" {
			return startedRunResponse(StartedRun{ChatID: *existing.ChatID, AlreadyStarted: true}), nil
		}
	} else if !errors.Is(err, core.ErrTriggerEventNotFound) {
		return NewTextErrorResponse("Could not check for an earlier start: " + err.Error()), nil
	}

	depth, err := agentStartRunDepth(rctx, s.repo, rctx.ChatID)
	if err != nil {
		return NewTextErrorResponse("Could not determine how deeply this run was started: " + err.Error()), nil
	}
	if depth >= MaxAgentStartRunDepth {
		return NewTextErrorResponse(fmt.Sprintf(
			"Refusing to start a run: this run was itself started by an agent %d level(s) deep, and the limit is %d. "+
				"Agents starting agents is capped so a mistake cannot fan out without bound. "+
				"Do the work in this run, or ask the user to start the next one.", depth, MaxAgentStartRunDepth)), nil
	}

	// The count and the launch are not one transaction, so concurrent starts
	// can overshoot the cap by the number racing. That is acceptable for a
	// cost guard; it is not a security boundary.
	live, err := s.repo.CountLiveLaunchedRuns(rctx.Context, caller.userID, core.TriggerEventKindAgentStartRun)
	if err != nil {
		return NewTextErrorResponse("Could not count live agent-started runs: " + err.Error()), nil
	}
	if live >= MaxConcurrentAgentStartedRuns {
		return NewTextErrorResponse(fmt.Sprintf(
			"Refusing to start a run: %d agent-started runs are already live (pending, running or paused) and the limit is %d. "+
				"Use list_runs to see them, then wait for some to finish or cancel one with control_run.",
			live, MaxConcurrentAgentStartedRuns)), nil
	}

	started, err := s.starter.StartRun(rctx.Context, StartRunRequest{
		OwnerUserID:  caller.userID,
		ProjectID:    projectID,
		Workflow:     params.Workflow,
		Message:      params.Message,
		Inputs:       params.Inputs,
		Presets:      params.Presets,
		Title:        params.Title,
		DaemonID:     callerDaemonID(caller.chat),
		NoMachine:    caller.chat.NoMachine,
		ParentChatID: rctx.ChatID,
		DedupeKey:    dedupeKey,
	})
	if err != nil {
		return NewTextErrorResponse("Could not start the run: " + err.Error()), nil
	}
	return startedRunResponse(started), nil
}

func startedRunResponse(run StartedRun) ToolResponse {
	var text string
	if run.AlreadyStarted {
		text = fmt.Sprintf("This start was already done: the run is %s (this call attached to it; nothing new was started). "+
			"Check it with get_run(run_id=%q).", run.ChatID, run.ChatID)
	} else {
		text = fmt.Sprintf("Started run %s. It is running now, detached from you — it has not finished. "+
			"Check on it with get_run(run_id=%q); list_runs shows all your runs.", run.ChatID, run.ChatID)
	}
	return WithResponseMetadata(NewTextResponse(text), StartRunResponseMetadata{
		RunID:          run.ChatID,
		ChatID:         run.ChatID,
		TemporalRunID:  run.RunID,
		AlreadyStarted: run.AlreadyStarted,
	})
}

// currentToolCallID is the id of the tool call being executed, which the tool
// wrapper records on the context before invoking Execute.
func currentToolCallID(rctx *rctx.ToolContext) string {
	if tc, ok := rctx.Value(ctxkeys.ToolCallContextKey).(*ctxkeys.ToolCallContext); ok && tc != nil {
		return tc.CurrentToolCallID
	}
	return ""
}

// agentStartRunDepth counts how many agent.start_run links sit above chatID:
// 0 for a run a human started, 1 for a run a human's run started, and so on.
// It walks parent_chat_id through trigger_events.
//
// The walk is bounded by MaxAgentStartRunDepth+1 steps rather than trusting the
// data to be acyclic, so a corrupt lineage reports "too deep" instead of
// looping.
func agentStartRunDepth(rctx *rctx.ToolContext, repo db.Repository, chatID string) (int, error) {
	depth := 0
	current := chatID
	for depth <= MaxAgentStartRunDepth {
		event, err := repo.GetTriggerEventByChat(rctx.Context, core.TriggerEventKindAgentStartRun, current)
		if errors.Is(err, core.ErrTriggerEventNotFound) {
			return depth, nil
		}
		if err != nil {
			return 0, err
		}
		parent, _ := event.Payload["parent_chat_id"].(string)
		depth++
		if parent == "" {
			return depth, nil
		}
		current = parent
	}
	return depth, nil
}

// callerDaemonID is the daemon the calling chat is bound to, or "" when it has
// none (daemon selection is then left to the runtime, as for any chat).
func callerDaemonID(chat *db.Chat) string {
	if chat.ActiveDaemonID == nil {
		return ""
	}
	return *chat.ActiveDaemonID
}
