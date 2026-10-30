// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rundiagnostics"
)

// ensureAutomationFailureMessage covers both runtime failures and graphs that
// completed at outcome: failure. A commentary message before the failure is
// not an explanation; only text written after the failed attempt suppresses
// the fallback. The preallocated id makes retries converge to one message.
func (a *WorkflowStatusActivity) ensureAutomationFailureMessage(ctx context.Context, input WorkflowStatusInput) {
	chat, err := a.repo.GetChat(ctx, input.ChatID)
	if err != nil || chat == nil {
		return
	}
	rows, err := a.repo.ListRunNodeExecutions(ctx, input.WorkflowID, rundiagnostics.RecordLimit)
	if err != nil {
		logging.WarnContext(ctx, "Could not read failed automation nodes", "chat_id", input.ChatID, "error", err)
	}
	detail := rundiagnostics.Failure(rows)
	if detail == "" {
		detail = rundiagnostics.ErrorText(input.Error)
	}
	if detail == "" && input.OutcomeNode != "" {
		// The declaring node runs no activity (compute, a sub-workflow), so
		// there was no attempt to mark.
		detail = fmt.Sprintf("Node %s declared the run's outcome: failure.", logging.RedactText(input.OutcomeNode))
	}
	if detail == "" {
		detail = "Workflow declared failure without a recorded node error."
	}
	if err := a.repo.SetLaunchEventRunFailureDetail(ctx, input.ChatID, detail); err != nil {
		logging.WarnContext(ctx, "Could not record automation failure detail", "chat_id", input.ChatID, "error", err)
	}
	if automationTrigger(chat) == "" {
		return
	}
	thread := input.Thread
	if thread == "" {
		thread = input.WorkflowID
	}
	last, err := a.repo.GetLatestMessageInThread(ctx, thread)
	if err == nil && last != nil && last.Role == pb.MessageRole_MESSAGE_ROLE_ASSISTANT {
		// A human-readable final answer after the failing node is the author's
		// failure message. Do not replace it, but don't mistake pre-failure progress
		// text or a tool-call-only assistant message for that answer.
		afterFailure := false
		for _, node := range rundiagnostics.Nodes(rows) {
			if node.Status == "failed" {
				for _, row := range rows {
					if row.ID == node.Id {
						// A failure-outcome save_message IS the author's
						// message; its row is written just after it.
						afterFailure = !last.CreatedAt.Before(row.CreatedAt) || row.ActivityName == "SaveMessage"
					}
				}
				break
			}
		}
		if afterFailure {
			if blocks, e := a.repo.ListContentBlocks(ctx, last.ID); e == nil {
				for _, b := range blocks {
					if b.BlockType == pb.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT && b.Content != nil && strings.TrimSpace(*b.Content) != "" {
						return
					}
				}
			}
		}
	}
	key := fmt.Sprintf("automation-failure:%s:%s", input.WorkflowID, temporalRunID(ctx))
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(key)).String()
	style := int32(pb.DisplayStyle_DISPLAY_STYLE_WARNING)
	_, _, err = a.repo.SaveMessageToThreadOnce(ctx, input.ChatID, thread,
		int32(pb.MessageRole_MESSAGE_ROLE_ASSISTANT), detail, &input.WorkflowID, nil, &style, id)
	if err != nil {
		logging.WarnContext(ctx, "Could not save automation failure message", "chat_id", input.ChatID, "error", err)
	}
}
