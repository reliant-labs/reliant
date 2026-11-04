// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/rctx"
)

type ListTriggerEventsParams struct {
	TriggerID string `json:"trigger_id" jsonschema:"required,description=ID of an existing trigger from list_triggers or fire_trigger."`
	Limit     int    `json:"limit,omitempty" jsonschema:"description=Page size (default 50, maximum 500)."`
	PageToken string `json:"page_token,omitempty" jsonschema:"description=Opaque next_page_token from an earlier page."`
}

const ListTriggerEventsToolName = "list_trigger_events"
const listTriggerEventsDescription = `Inspect an existing trigger's firing history, newest first: event id, time, outcome, detail, launched chat_id, current run state, and the workflow definition each firing resolved (file, machine, branch and commit, content hash, and the error when a broken definition refused it). Read-only; never fires or changes a trigger. Raw event payloads are not returned.

Use after fire_trigger to see whether a firing launched, skipped or failed; acceptance of a manual fire is not a successful run. Recording is asynchronous, so an empty page may mean the firing has not been recorded yet. Use get_run with a launched chat_id for details. Concurrent fires can appear: do not assume the newest event necessarily belongs to your call. Match fire_workflow_id from fire_trigger to the event with that exact ID, then use its chat_id; do not guess from ordering. Continue with next_page_token when present.`

type listTriggerEventsTool struct {
	repo   runCallerStore
	lister TriggerEventLister
}

func NewListTriggerEventsTool(repo runCallerStore, lister TriggerEventLister) Tool {
	return NewToolWrapper[ListTriggerEventsParams, ToolResponse](&listTriggerEventsTool{repo: repo, lister: lister})
}
func (l *listTriggerEventsTool) Name() string        { return ListTriggerEventsToolName }
func (l *listTriggerEventsTool) Description() string { return listTriggerEventsDescription }
func (l *listTriggerEventsTool) RequiresPermission(ListTriggerEventsParams) (bool, error) {
	return false, nil
}
func (l *listTriggerEventsTool) Execute(ctx *rctx.ToolContext, params ListTriggerEventsParams) (ToolResponse, error) {
	caller, errResp := resolveRunCaller(ctx, l.repo)
	if errResp != nil {
		return *errResp, nil
	}
	id := strings.TrimSpace(params.TriggerID)
	if id == "" {
		return NewTextErrorResponse("trigger_id is required"), nil
	}
	if l.lister == nil {
		return NewTextErrorResponse("Trigger events are not available in this runtime."), nil
	}
	page, err := l.lister.ListTriggerEvents(ctx.Context, caller.userID, id, params.Limit, params.PageToken)
	if err != nil {
		return NewTextErrorResponse("Could not list trigger events: " + err.Error()), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d firing(s) for trigger %s (newest first):\n", len(page.Events), id)
	for _, e := range page.Events {
		fmt.Fprintf(&b, "- %s at %s: %s", e.ID, e.OccurredAt, e.Outcome)
		if e.FireWorkflowID != "" {
			fmt.Fprintf(&b, "; fire_workflow_id %s", e.FireWorkflowID)
		}
		if e.Detail != "" {
			fmt.Fprintf(&b, " — %s", e.Detail)
		}
		if e.ChatID != "" {
			fmt.Fprintf(&b, "; chat_id %s", e.ChatID)
		}
		if e.RunState != "" {
			fmt.Fprintf(&b, "; run %s", e.RunState)
		}
		if e.Definition != nil {
			fmt.Fprintf(&b, "; definition %s", e.Definition.Summary)
		}
		b.WriteByte('\n')
	}
	if len(page.Events) == 0 {
		b.WriteString("No firings recorded yet; an accepted fire may still be pending.\n")
	}
	if page.NextPageToken != "" {
		fmt.Fprintf(&b, "next_page_token: %s\n", page.NextPageToken)
	}
	return WithResponseMetadata(NewTextResponse(strings.TrimSpace(b.String())), page), nil
}
