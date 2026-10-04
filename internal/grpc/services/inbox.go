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

// Item id prefixes for the dismissable kinds. The id embeds the failing event's
// id, so a newer failure is a new item and is not hidden by an old dismissal.
const (
	inboxFailingPrefix      = "automation_failing:"
	inboxLaunchFailedPrefix = "automation_launch_failed:"
)

// InboxService implements the InboxService RPC handlers.
type InboxService struct {
	reliantv1connect.UnimplementedInboxServiceHandler
	database db.Repository
}

// NewInboxService creates a new InboxService.
func NewInboxService(database db.Repository) *InboxService {
	return &InboxService{database: database}
}

// ListInbox returns everything waiting on the caller. Pending rows come from
// one UNION ALL query; automation health reuses triggers.ComputeHealth over one
// batched firings query, so the cost does not grow with the number of triggers.
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

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		if items[i].WaitingSince != items[j].WaitingSince {
			return items[i].WaitingSince < items[j].WaitingSince
		}
		return items[i].ItemId < items[j].ItemId
	})

	resp := &reliantv1.ListInboxResponse{}
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

// DismissInboxItem hides a failure item for the caller.
func (s *InboxService) DismissInboxItem(
	ctx context.Context,
	req *connect.Request[reliantv1.DismissInboxItemRequest],
) (*connect.Response[reliantv1.DismissInboxItemResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user ID not found in context"))
	}
	itemID := req.Msg.ItemId
	if !isDismissableInboxItem(itemID) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("only automation failure items can be dismissed; approvals and questions clear when resolved"))
	}
	if err := s.database.DismissInboxItem(ctx, userID, itemID, time.Now().UTC()); err != nil {
		logging.Error("Failed to dismiss inbox item", "error", err, "itemID", itemID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to dismiss inbox item"))
	}
	return connect.NewResponse(&reliantv1.DismissInboxItemResponse{}), nil
}

func isDismissableInboxItem(itemID string) bool {
	for _, prefix := range []string{inboxFailingPrefix, inboxLaunchFailedPrefix} {
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
		newest := triggers.NewestFailure(firings)
		if newest == nil {
			continue
		}
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
			ItemId:       inboxFailingPrefix + newest.Event.ID,
			TriggerId:    t.ID,
			TriggerName:  t.Name,
			ProjectId:    t.ProjectID,
			ProjectName:  t.ProjectName,
			WorkflowName: t.Workflow,
			WaitingSince: newest.Event.OccurredAt.UTC().Format(time.RFC3339Nano),
			Payload:      &reliantv1.InboxItem_AutomationFailing{AutomationFailing: payload},
		})
	}
	return out, nil
}

func (s *InboxService) dropDismissed(ctx context.Context, userID string, items []*reliantv1.InboxItem) ([]*reliantv1.InboxItem, error) {
	var candidates []string
	for _, it := range items {
		if isDismissableInboxItem(it.ItemId) {
			candidates = append(candidates, it.ItemId)
		}
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
		item.Payload = &reliantv1.InboxItem_Approval{Approval: inboxApproval(p)}
	case core.InboxPendingQuestion:
		item.Kind = reliantv1.InboxItemKind_INBOX_ITEM_KIND_QUESTION
		q := &reliantv1.InboxQuestion{QuestionId: p.ItemKey, ThreadId: p.Text1, Prompt: firstQuestionPrompt(p.Text2)}
		if p.Text2 != "" {
			meta := p.Text2
			q.Metadata = &meta
		}
		item.Payload = &reliantv1.InboxItem_Question{Question: q}
	case core.InboxPendingWaitingForMachine:
		item.Kind = reliantv1.InboxItemKind_INBOX_ITEM_KIND_WAITING_FOR_MACHINE
		item.Payload = &reliantv1.InboxItem_WaitingForMachine{WaitingForMachine: &reliantv1.InboxWaitingForMachine{
			DaemonId: p.Text1, DaemonName: p.Text2,
		}}
	case core.InboxPendingLaunchFailed:
		item.Kind = reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_LAUNCH_FAILED
		item.ItemId = inboxLaunchFailedPrefix + p.ItemKey
		item.Payload = &reliantv1.InboxItem_AutomationLaunchFailed{AutomationLaunchFailed: &reliantv1.InboxAutomationLaunchFailed{
			Reason: p.Text1, EventId: p.ItemKey,
		}}
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

// summarizeArguments renders tool arguments as one short line.
func summarizeArguments(v any) string {
	var text string
	switch t := v.(type) {
	case string:
		text = t
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		text = string(b)
	}
	text = strings.Join(strings.Fields(text), " ")
	const max = 200
	if r := []rune(text); len(r) > max {
		text = string(r[:max-1]) + "…"
	}
	return text
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
