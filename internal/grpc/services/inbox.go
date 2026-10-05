// Copyright (c) 2025 Reliant Labs
//
//forge:exclude-contract: Connect RPC handler implementation; its contract is the generated proto service interface
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/triggers"
)

const defaultInboxLimit = 100

// Item id prefixes, one per kind, so an id names what it dismisses. A failure
// id embeds the FIRST failing event of the current episode, so more failures
// stay one item (and one dismissal), while a new episode after a success is a
// new item. A waiting-for-machine id embeds the chat and when the block began,
// so a later block is a new item. Approval, question and run-finished ids
// embed their row or chat.
const (
	inboxApprovalPrefix          = "approval:"
	inboxQuestionPrefix          = "question:"
	inboxWaitingForMachinePrefix = "waiting_for_machine:"
	inboxFailingPrefix           = "automation_failing:"
	inboxLaunchFailedPrefix      = "automation_launch_failed:"
	inboxRunFinishedPrefix       = "run_finished:"
)

var inboxItemPrefixes = []string{
	inboxApprovalPrefix, inboxQuestionPrefix, inboxWaitingForMachinePrefix,
	inboxFailingPrefix, inboxLaunchFailedPrefix, inboxRunFinishedPrefix,
}

// maxInboxDismissBatch bounds one Dismiss/Restore call ("Dismiss all" on a
// section). The list itself is capped at defaultInboxLimit.
const maxInboxDismissBatch = 500

// InboxService implements the InboxService RPC handlers.
type InboxService struct {
	reliantv1connect.UnimplementedInboxServiceHandler
	database db.Repository
}

// NewInboxService creates a new InboxService.
func NewInboxService(database db.Repository) *InboxService {
	return &InboxService{database: database}
}

// ListInbox returns everything waiting on the caller, optionally within one
// project. Pending rows come from one UNION ALL query; automation health reuses
// triggers.ComputeHealth over one batched firings query, so the cost does not
// grow with the number of triggers.
//
// Scoping is applied after dismissal so the scoped response can also say how
// many items wait in the caller's other projects (one read, not two).
func (s *InboxService) ListInbox(
	ctx context.Context,
	req *connect.Request[reliantv1.ListInboxRequest],
) (*connect.Response[reliantv1.ListInboxResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user ID not found in context"))
	}
	limit := defaultInboxLimit
	if req.Msg.Limit != nil {
		limit = int(*req.Msg.Limit)
		if limit < 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("limit must not be negative"))
		}
	}

	projectID := strings.TrimSpace(req.Msg.GetProjectId())

	pending, err := s.database.ListInboxPending(ctx, userID)
	if err != nil {
		logging.Error("Failed to list inbox", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list inbox"))
	}
	failing, err := s.failingAutomations(ctx, userID)
	if err != nil {
		logging.Error("Failed to compute failing automations for inbox", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list inbox"))
	}

	items := make([]*reliantv1.InboxItem, 0, len(pending)+len(failing))
	for _, p := range pending {
		items = append(items, inboxItemFromPending(p))
	}
	items = append(items, failing...)

	items, err = s.dropDismissed(ctx, userID, items)
	if err != nil {
		logging.Error("Failed to filter dismissed inbox items", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list inbox"))
	}

	resp := &reliantv1.ListInboxResponse{}
	if projectID != "" {
		scoped := items[:0]
		for _, it := range items {
			if it.ProjectId == projectID {
				scoped = append(scoped, it)
			} else {
				resp.OtherProjectsCount++
			}
		}
		items = scoped
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		if items[i].WaitingSince != items[j].WaitingSince {
			return items[i].WaitingSince < items[j].WaitingSince
		}
		return items[i].ItemId < items[j].ItemId
	})

	for _, it := range items {
		switch it.Kind {
		case reliantv1.InboxItemKind_INBOX_ITEM_KIND_APPROVAL,
			reliantv1.InboxItemKind_INBOX_ITEM_KIND_QUESTION,
			reliantv1.InboxItemKind_INBOX_ITEM_KIND_WAITING_FOR_MACHINE:
			resp.BlockingCount++
		default:
			resp.HasInformational = true
		}
	}
	if len(items) > limit {
		items = items[:limit]
		resp.Truncated = true
	}
	resp.Items = items
	return connect.NewResponse(resp), nil
}

// DismissInboxItem hides items from the caller's inbox. Hiding an approval or
// a question does not resolve it; the run still waits on it.
func (s *InboxService) DismissInboxItem(
	ctx context.Context,
	req *connect.Request[reliantv1.DismissInboxItemRequest],
) (*connect.Response[reliantv1.DismissInboxItemResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user ID not found in context"))
	}
	if err := validateInboxItemIDs(req.Msg.ItemIds); err != nil {
		return nil, err
	}
	if err := s.database.DismissInboxItems(ctx, userID, req.Msg.ItemIds, time.Now().UTC()); err != nil {
		logging.Error("Failed to dismiss inbox items", "error", err, "count", len(req.Msg.ItemIds))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to dismiss inbox item"))
	}
	return connect.NewResponse(&reliantv1.DismissInboxItemResponse{}), nil
}

// RestoreInboxItem undoes DismissInboxItem (the Undo on the dismiss toast).
func (s *InboxService) RestoreInboxItem(
	ctx context.Context,
	req *connect.Request[reliantv1.RestoreInboxItemRequest],
) (*connect.Response[reliantv1.RestoreInboxItemResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user ID not found in context"))
	}
	if err := validateInboxItemIDs(req.Msg.ItemIds); err != nil {
		return nil, err
	}
	if err := s.database.RestoreInboxItems(ctx, userID, req.Msg.ItemIds); err != nil {
		logging.Error("Failed to restore inbox items", "error", err, "count", len(req.Msg.ItemIds))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to restore inbox item"))
	}
	return connect.NewResponse(&reliantv1.RestoreInboxItemResponse{}), nil
}

func validateInboxItemIDs(itemIDs []string) error {
	if len(itemIDs) == 0 {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("item_ids is required"))
	}
	if len(itemIDs) > maxInboxDismissBatch {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at most %d item ids per call", maxInboxDismissBatch))
	}
	for _, id := range itemIDs {
		if !isInboxItemID(id) {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("not an inbox item id: %q", id))
		}
	}
	return nil
}

func isInboxItemID(itemID string) bool {
	for _, prefix := range inboxItemPrefixes {
		if strings.HasPrefix(itemID, prefix) && len(itemID) > len(prefix) {
			return true
		}
	}
	return false
}

// failingAutomations returns one item per enabled trigger whose health is
// FAILING, using the same ComputeHealth as Trigger.health.
func (s *InboxService) failingAutomations(ctx context.Context, userID string) ([]*reliantv1.InboxItem, error) {
	stored, err := s.database.ListTriggers(ctx, core.TriggerFilters{UserID: userID})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(stored))
	for _, t := range stored {
		if t.Enabled {
			ids = append(ids, t.ID)
		}
	}
	recent, err := s.database.RecentTriggerFirings(ctx, userID, ids, triggers.HealthWindow)
	if err != nil {
		return nil, err
	}

	// The episode is read separately from the health window: a streak longer
	// than the window must still be one item with an honest count.
	episodes, err := s.database.FiringsSinceLastSuccess(ctx, userID, ids, triggers.EpisodeFirings)
	if err != nil {
		return nil, err
	}

	var out []*reliantv1.InboxItem
	for _, t := range stored {
		if !t.Enabled {
			continue
		}
		firings := recent[t.ID]
		health := triggers.ComputeHealth(firings)
		if health.Status != reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_FAILING {
			continue
		}
		streak := triggers.FailureStreak(episodes[t.ID])
		if streak.First == nil {
			continue
		}
		health.ConsecutiveFailures = streak.Count
		payload := &reliantv1.InboxAutomationFailing{Health: health}
		for _, f := range firings {
			if f.Run != nil {
				chatID := f.Run.ChatID
				payload.LastRunChatId = &chatID
				break
			}
		}
		out = append(out, &reliantv1.InboxItem{
			Kind:         reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING,
			ItemId:       inboxFailingPrefix + streak.First.Event.ID,
			TriggerId:    t.ID,
			TriggerName:  t.Name,
			ProjectId:    t.ProjectID,
			ProjectName:  t.ProjectName,
			WorkflowName: t.Workflow,
			WaitingSince: streak.First.Event.OccurredAt.UTC().Format(time.RFC3339Nano),
			Payload:      &reliantv1.InboxItem_AutomationFailing{AutomationFailing: payload},
		})
	}
	return out, nil
}

func (s *InboxService) dropDismissed(ctx context.Context, userID string, items []*reliantv1.InboxItem) ([]*reliantv1.InboxItem, error) {
	candidates := make([]string, 0, len(items))
	for _, it := range items {
		candidates = append(candidates, it.ItemId)
	}
	dismissed, err := s.database.ListDismissedInboxItemIDs(ctx, userID, candidates)
	if err != nil {
		return nil, err
	}
	if len(dismissed) == 0 {
		return items, nil
	}
	kept := items[:0]
	for _, it := range items {
		if !dismissed[it.ItemId] {
			kept = append(kept, it)
		}
	}
	return kept, nil
}

func inboxItemFromPending(p *core.InboxPending) *reliantv1.InboxItem {
	item := &reliantv1.InboxItem{
		ItemId:       p.ItemKey,
		ChatId:       p.ChatID,
		RunId:        p.RunID,
		TriggerId:    p.TriggerID,
		TriggerName:  p.TriggerName,
		ProjectId:    p.ProjectID,
		ProjectName:  p.ProjectName,
		WorkflowName: p.WorkflowName,
		ChatTitle:    p.ChatTitle,
		WaitingSince: p.WaitingSince.UTC().Format(time.RFC3339Nano),
	}
	switch p.Kind {
	case core.InboxPendingApproval:
		item.Kind = reliantv1.InboxItemKind_INBOX_ITEM_KIND_APPROVAL
		item.ItemId = inboxApprovalPrefix + p.ItemKey
		item.Payload = &reliantv1.InboxItem_Approval{Approval: inboxApproval(p)}
	case core.InboxPendingQuestion:
		item.Kind = reliantv1.InboxItemKind_INBOX_ITEM_KIND_QUESTION
		item.ItemId = inboxQuestionPrefix + p.ItemKey
		q := &reliantv1.InboxQuestion{QuestionId: p.ItemKey, ThreadId: p.Text1, Prompt: firstQuestionPrompt(p.Text2)}
		if p.Text2 != "" {
			meta := p.Text2
			q.Metadata = &meta
		}
		item.Payload = &reliantv1.InboxItem_Question{Question: q}
	case core.InboxPendingWaitingForMachine:
		item.Kind = reliantv1.InboxItemKind_INBOX_ITEM_KIND_WAITING_FOR_MACHINE
		item.ItemId = inboxWaitingForMachinePrefix + p.ItemKey
		item.Payload = &reliantv1.InboxItem_WaitingForMachine{WaitingForMachine: &reliantv1.InboxWaitingForMachine{
			DaemonId: p.Text1, DaemonName: p.Text2,
		}}
	case core.InboxPendingLaunchFailed:
		item.Kind = reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_LAUNCH_FAILED
		item.ItemId = inboxLaunchFailedPrefix + p.ItemKey
		item.Payload = &reliantv1.InboxItem_AutomationLaunchFailed{AutomationLaunchFailed: &reliantv1.InboxAutomationLaunchFailed{
			Reason: p.Text1, EventId: p.ItemKey, ConsecutiveFailures: p.Int1,
		}}
	case core.InboxPendingRunFinished:
		item.Kind = reliantv1.InboxItemKind_INBOX_ITEM_KIND_RUN_FINISHED
		item.ItemId = inboxRunFinishedPrefix + p.ItemKey
		item.Payload = &reliantv1.InboxItem_RunFinished{RunFinished: &reliantv1.InboxRunFinished{}}
	}
	return item
}

func inboxApproval(p *core.InboxPending) *reliantv1.InboxApproval {
	a := &reliantv1.InboxApproval{
		ApprovalId:   p.ItemKey,
		ApprovalType: reliantv1.ApprovalType(p.Int1),
		Title:        p.Text1,
	}
	var meta map[string]any
	if p.Text2 == "" || json.Unmarshal([]byte(p.Text2), &meta) != nil {
		return a
	}
	if v, ok := meta["tool_name"].(string); ok && v != "" {
		a.ToolName = &v
	}
	if v, ok := meta["tool_call_id"].(string); ok && v != "" {
		a.ToolCallId = &v
	}
	for _, key := range []string{"arguments", "args", "input"} {
		if v, ok := meta[key]; ok {
			a.ArgumentSummary = summarizeArguments(v)
			break
		}
	}
	return a
}

// summarizeArguments renders tool arguments as one short line of text: a lone
// argument is its value ("git push origin main"), several are "key: value"
// pairs in key order. Only nested values fall back to compact JSON.
func summarizeArguments(v any) string {
	var text string
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) == 1 {
			text = argumentValue(t[keys[0]])
			break
		}
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+": "+argumentValue(t[k]))
		}
		text = strings.Join(parts, " · ")
	default:
		text = argumentValue(t)
	}
	text = strings.Join(strings.Fields(text), " ")
	const max = 200
	if r := []rune(text); len(r) > max {
		text = string(r[:max-1]) + "…"
	}
	return text
}

func argumentValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func firstQuestionPrompt(metadata string) string {
	var payload struct {
		Questions []struct {
			Question string `json:"question"`
		} `json:"questions"`
	}
	if metadata == "" || json.Unmarshal([]byte(metadata), &payload) != nil || len(payload.Questions) == 0 {
		return ""
	}
	return payload.Questions[0].Question
}
