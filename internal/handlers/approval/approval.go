// Copyright (c) 2025 Reliant Labs
package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// chatBelongsToUser verifies the chat exists and is owned by the caller.
// Approvals gate tool execution, so acting on one is acting on the chat —
// ownership is transitive through Approval.ChatID. A foreign chat is
// NotFound, never PermissionDenied, so the check cannot be used as a
// cross-tenant existence oracle.
func (s *Service) chatBelongsToUser(ctx context.Context, chatID string) error {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return svcerr.Unauthenticated("user ID not found in context")
	}
	chat, err := s.deps.Chats.GetChatRow(ctx, chatID)
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if !authz.CanOwnChat(ctx, userID, chat) {
		return svcerr.NotFound("approval")
	}
	return nil
}

// ReasonApprovalNotPending is the error reason for acting on an approval that
// is no longer pending: someone decided it, its wait expired, or the run
// waiting on it ended. Clients branch on it to drop the approval and show
// the message, which says what happened.
const ReasonApprovalNotPending = "approval_not_pending"

// ReasonGrantApprovedAlone is the error reason for approving a pipeline
// capability-grant card in a batch: it is approved only on its own.
const ReasonGrantApprovedAlone = "grant_approved_alone"

// notActionable is the error for acting on a, which is no longer pending.
func notActionable(a *db.Approval) error {
	return svcerr.WithReason(svcerr.FailedPrecondition(resolvedMessage(a)), ReasonApprovalNotPending)
}

// resolvedMessage says, for the person, what already happened to a.
func resolvedMessage(a *db.Approval) string {
	reason := ""
	if a.DenialReason != nil {
		reason = *a.DenialReason
	}
	withReason := func(head string) string {
		if reason == "" {
			return head + "."
		}
		return head + ": " + reason
	}
	switch reliantv1.ApprovalStatus(a.Status) {
	case reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED:
		return "This request was already approved."
	case reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED:
		// Timed-out approvals were recorded as denied with action "timeout"
		// before EXPIRED existed.
		if a.ActionTaken != nil && *a.ActionTaken == "timeout" {
			return "This request expired: no one answered before its time limit, so nothing was approved."
		}
		return withReason("This request was already denied")
	case reliantv1.ApprovalStatus_APPROVAL_STATUS_EXPIRED:
		return withReason("This request expired")
	default:
		return withReason("This request was already resolved")
	}
}

// answeredByItsRun reports whether denying a reaches the model through its own
// run. A tool-call approval asks about one call, and the run waiting on it
// answers that call in ExecuteTools with the refusal, so a denial message — a
// tool_result for every call in the turn, including calls that still run —
// would contradict and duplicate it.
func answeredByItsRun(a *db.Approval) bool {
	return a.ApprovalType == int32(reliantv1.ApprovalType_APPROVAL_TYPE_TOOL_CALL)
}

// decision is what the person decided about one or more approvals.
type decision struct {
	approved bool
	// denialReason is the reason a denial records (denials only).
	denialReason string
	// actionTaken is the button that was clicked, when the client names one.
	actionTaken *string
	// connectionID is the connection the person made to approve a
	// connection proposal (Approve only).
	connectionID string
}

func denialReasonOr(reason *string, fallback string) string {
	if reason != nil && *reason != "" {
		return *reason
	}
	return fallback
}

// resolveApprovals records d on every listed approval that is still pending,
// all in one transaction, then signals each one's waiting run. It returns the
// chat they belong to, how many it resolved and how many it skipped as
// already resolved.
//
// Only a person, or a token granted approvals:decide explicitly, decides an
// approval (auth.RequireApprovalDecider), or nothing is read or written. This
// is the authority; the interceptor's scope check in front of it is a first
// gate. reliant:api is refused, and so is any token bound to a machine, scope
// or not. Owning the chat is not enough, because an agent's shell holds the
// owner's CLI login, and an approval it could answer itself — a release-gate
// waiver, a promotion — would gate nothing (reliant #2123).
//
// Every id is checked before anything is written: it must exist, and all of
// them must be one chat the caller owns. An approval already decided — on
// another surface, or by its run timing out — is skipped, read again inside
// the transaction so a decision that lands concurrently is never overwritten.
// Only when none of them was still pending is that an error
// (FailedPrecondition, ReasonApprovalNotPending, saying what happened), which
// is what lets a batch approve what is left of itself after one of its
// approvals expired or was decided elsewhere.
func (s *Service) resolveApprovals(ctx context.Context, ids []string, d decision) (string, int, int, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return "", 0, 0, svcerr.Unauthenticated("user ID not found in context")
	}
	if err := auth.RequireApprovalDecider(ctx, "decide an approval"); err != nil {
		logging.WarnContext(ctx, "[Approval] Refused a decision made with a credential that may not decide",
			"credential", auth.CredentialFromContext(ctx).String(), "approvalIDs", ids)
		return "", 0, 0, err
	}
	var chatID string
	for _, id := range ids {
		approval, err := s.deps.Repo.GetApproval(ctx, id)
		if err != nil {
			return "", 0, 0, svcerr.WrapOr(err, svcerr.Internal("failed to get approval"))
		}
		if approval == nil {
			return "", 0, 0, svcerr.NotFound("approval")
		}
		// The same-chat rule makes one ownership check cover every id.
		if chatID == "" {
			chatID = approval.ChatID
			if err := s.chatBelongsToUser(ctx, chatID); err != nil {
				return "", 0, 0, err
			}
		} else if chatID != approval.ChatID {
			return "", 0, 0, svcerr.InvalidArgument("all approvals must belong to the same chat")
		}
	}
	// A connection proposal is approved only by making its connection, one
	// at a time: a batch leaves it pending, and approving it alone needs the
	// connection, checked against the proposal (connection_proposal.go).
	//
	// A capability-grant card gives an automation standing power, so it is
	// approved only on its own too: a batch leaves it pending.
	if d.approved {
		var held, heldGrant *db.Approval
		kept := ids[:0:0]
		for _, id := range ids {
			approval, err := s.deps.Repo.GetApproval(ctx, id)
			if err != nil {
				return "", 0, 0, svcerr.WrapOr(err, svcerr.Internal("failed to get approval"))
			}
			p := approval.ConnectionProposal()
			switch {
			case approval.CapabilityGrantRequest() != nil:
				if len(ids) > 1 {
					heldGrant = approval
					continue
				}
				kept = append(kept, id)
			case p == nil:
				kept = append(kept, id)
			case len(ids) > 1 || d.connectionID == "":
				held = approval
			default:
				if approval.Status == int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING) {
					if err := s.checkProposalConnection(ctx, userID, p, d.connectionID); err != nil {
						return "", 0, 0, err
					}
				}
				kept = append(kept, id)
			}
		}
		if len(kept) == 0 && held != nil {
			if held.Status != int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING) {
				return "", 0, 0, notActionable(held)
			}
			return "", 0, 0, connectionRequired(held.ConnectionProposal())
		}
		if len(kept) == 0 && heldGrant != nil {
			if heldGrant.Status != int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING) {
				return "", 0, 0, notActionable(heldGrant)
			}
			return "", 0, 0, svcerr.WithReason(svcerr.FailedPrecondition(
				"A pipeline capability grant gives an automation standing power: approve it on its own, not with others."), ReasonGrantApprovedAlone)
		}
		ids = kept
	}

	status := reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED
	statusName := "approved"
	var denialReason *string
	if !d.approved {
		status = reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED
		statusName = "denied"
		denialReason = &d.denialReason
	}
	var actionTaken *string
	if d.actionTaken != nil && *d.actionTaken != "" {
		actionTaken = d.actionTaken
	}

	var resolved, skipped []*db.Approval
	err := s.deps.Repo.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		resolved, skipped = nil, nil
		for _, id := range ids {
			approval, err := s.deps.Repo.GetApproval(txCtx, id)
			if err != nil {
				return err
			}
			if approval == nil {
				continue
			}
			if approval.Status != int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING) {
				skipped = append(skipped, approval)
				continue
			}
			var metadata *string
			if d.approved && approval.ConnectionProposal() != nil {
				if metadata, err = withConnectionID(approval, d.connectionID); err != nil {
					return fmt.Errorf("failed to record the approving connection: %w", err)
				}
			}
			if err := s.deps.Repo.UpdateApprovalStatus(txCtx, approval.ID, int32(status), denialReason, actionTaken, metadata); err != nil {
				return fmt.Errorf("failed to update approval status: %w", err)
			}
			// Approving a capability-grant card is what makes the grant,
			// recorded with the approver: the card and its grants commit
			// together, and a grant refused (the approver does not own the
			// activation and the pool) leaves the card pending.
			if d.approved && approval.CapabilityGrantRequest() != nil {
				if s.deps.Grants == nil {
					return svcerr.FailedPrecondition("pipeline capability grants cannot be made on this server; nothing was granted")
				}
				if _, err := s.deps.Grants.GrantApproved(txCtx, userID, approval); err != nil {
					return err
				}
			}
			updateData := map[string]interface{}{
				"update_type":   "approval",
				"id":            approval.ID,
				"approval_type": approval.ApprovalType,
				"entity_id":     approval.EntityID,
				"status":        statusName,
				"title":         approval.Title,
				"resolved_at":   time.Now().Format(time.RFC3339),
			}
			if denialReason != nil {
				updateData["denial_reason"] = *denialReason
			}
			if actionTaken != nil {
				updateData["action_taken"] = *actionTaken
			}
			db.AttributeApprovalUpdate(updateData, approval)
			updateDataJSON, err := json.Marshal(updateData)
			if err != nil {
				return fmt.Errorf("failed to marshal chat_update data: %w", err)
			}
			if err := s.deps.Repo.CreateChatUpdate(txCtx, approval.ChatID, db.UpdateTypeApproval, approval.ID, string(updateDataJSON)); err != nil {
				return fmt.Errorf("failed to create chat_update: %w", err)
			}
			resolved = append(resolved, approval)
		}
		return nil
	})
	if err != nil {
		return "", 0, 0, svcerr.WrapOr(err, svcerr.Internal("failed to resolve approval"))
	}
	if len(resolved) == 0 {
		if len(skipped) == 1 {
			return "", 0, 0, notActionable(skipped[0])
		}
		return "", 0, 0, svcerr.WithReason(svcerr.FailedPrecondition(fmt.Sprintf(
			"These %d requests were already resolved (decided, expired, or their run ended); nothing changed.", len(skipped))),
			ReasonApprovalNotPending)
	}

	// One denial message for the chat, unless every denied approval is
	// answered by its own run.
	if !d.approved {
		for _, approval := range resolved {
			if !answeredByItsRun(approval) {
				if err := s.createDenialMessage(ctx, chatID, d.denialReason); err != nil {
					logging.ErrorContext(ctx, "Failed to create denial message", "error", err, "chatID", chatID)
				}
				break
			}
		}
	}

	signal := map[string]interface{}{"status": statusName, "action_taken": ""}
	if actionTaken != nil {
		signal["action_taken"] = *actionTaken
	}
	if denialReason != nil {
		signal["denial_reason"] = *denialReason
	}
	for _, approval := range resolved {
		if approval.CapabilityGrantRequest() != nil && approval.TemporalWorkflowID == "" {
			continue // no run waits on a capability-grant card
		}
		if d.approved && approval.ConnectionProposal() != nil {
			proposal := make(map[string]interface{}, len(signal)+1)
			for k, v := range signal {
				proposal[k] = v
			}
			proposal["connection_id"] = d.connectionID
			s.signalApproval(ctx, approval, proposal)
			continue
		}
		s.signalApproval(ctx, approval, signal)
	}
	return chatID, len(resolved), len(skipped), nil
}

// batchMessage is a batch response's message: what was done, and how many
// were skipped as already resolved.
func batchMessage(verb string, done, skipped int) string {
	msg := fmt.Sprintf("%s %d requests", verb, done)
	if skipped > 0 {
		msg += fmt.Sprintf("; %d already resolved, skipped", skipped)
	}
	return msg
}

// signalApproval sends a signal to the workflow using PauseService.SignalWithRecovery
func (s *Service) signalApproval(ctx context.Context, approval *db.Approval, signalData map[string]interface{}) {
	if approval.TemporalWorkflowID == "" {
		logging.WarnContext(ctx, "[Approval] No temporal_workflow_id on approval, cannot signal", "approvalID", approval.ID)
		return
	}
	signalName := "signal.approval." + approval.ID
	if err := s.deps.Signals.SignalWithRecovery(ctx, approval.TemporalWorkflowID, signalName, signalData, nil); err != nil {
		logging.WarnContext(ctx, "[Approval] Failed to signal approval resolution",
			"error", err,
			"approvalID", approval.ID,
			"temporalWorkflowID", approval.TemporalWorkflowID,
		)
	}
}

// createDenialMessage creates a tool message with denied tool results for the given chat
func (s *Service) createDenialMessage(ctx context.Context, chatID string, denialReason string) error {

	// Get all messages to find the last assistant message with tool_calls
	msgs, err := s.deps.Repo.ListMessages(ctx, chatID, db.MessageListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list messages: %w", err)
	}

	// Find the last assistant message
	var lastAssistantMsg *db.Message
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
			lastAssistantMsg = msgs[i]
			break
		}
	}

	if lastAssistantMsg == nil {
		logging.DebugContext(ctx, "[Approval] No assistant message found for denial", "chatID", chatID)
		return nil
	}

	// Get content blocks for the assistant message to find tool_calls
	blocks, err := s.deps.Repo.ListContentBlocks(ctx, lastAssistantMsg.ID)
	if err != nil {
		return fmt.Errorf("failed to list content blocks: %w", err)
	}

	// Filter for tool_call blocks
	var toolCallBlocks []*db.MessageContentBlock
	for _, block := range blocks {
		if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL && block.ToolCallID != nil {
			toolCallBlocks = append(toolCallBlocks, block)
		}
	}

	if len(toolCallBlocks) == 0 {
		logging.DebugContext(ctx, "[Approval] No tool_call blocks found in assistant message", "chatID", chatID, "messageID", lastAssistantMsg.ID)
		return nil
	}

	logging.DebugContext(ctx, "[Approval] Found tool_call blocks to deny", "count", len(toolCallBlocks), "chatID", chatID)

	// Create the denial message and content blocks atomically
	now := time.Now().UTC()
	messageID := uuid.New().String()
	isError := true
	denialContent := denialReason

	err = s.deps.Repo.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		// Create the tool message (same context_window as the assistant message)
		msg := &db.Message{
			ID:              messageID,
			ChatID:          chatID,
			ThreadID:        lastAssistantMsg.ThreadID,
			ContextWindowID: lastAssistantMsg.ContextWindowID,
			Role:            reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
			CreatedAt:       now,
			UpdatedAt:       now,
		}

		// The next ordinal and seq are allocated with the insert.
		if err := s.deps.Repo.CreateNumberedMessage(txCtx, msg); err != nil {
			return fmt.Errorf("failed to create denial message: %w", err)
		}

		// Create a text content block with the denial notice
		textBlockID := uuid.New().String()
		denialNotice := "User denied one or more tool calls"
		version := 1
		textBlock := &db.MessageContentBlock{
			ID:        textBlockID,
			MessageID: messageID,
			Position:  0,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   &denialNotice,
			Version:   &version,
			CreatedAt: now,
			UpdatedAt: now,
		}

		if err := s.deps.Repo.CreateContentBlock(txCtx, textBlock); err != nil {
			return fmt.Errorf("failed to create text content block: %w", err)
		}

		// Create tool_result blocks for each denied tool_call
		for i, toolCallBlock := range toolCallBlocks {
			blockID := uuid.New().String()
			block := &db.MessageContentBlock{
				ID:         blockID,
				MessageID:  messageID,
				Position:   i + 1, // Start after the text block
				BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
				ToolCallID: toolCallBlock.ToolCallID,
				Content:    &denialContent,
				IsError:    &isError,
				Version:    &version,
				CreatedAt:  now,
				UpdatedAt:  now,
			}

			if err := s.deps.Repo.CreateContentBlock(txCtx, block); err != nil {
				return fmt.Errorf("failed to create tool_result block %d: %w", i, err)
			}
		}

		// Create chat_update for the new message
		contentBlocksData := make([]map[string]interface{}, 0, len(toolCallBlocks)+1)

		// Add text block
		contentBlocksData = append(contentBlocksData, map[string]interface{}{
			"id":       textBlockID,
			"type":     "text",
			"content":  denialNotice,
			"position": 0,
		})

		// Add tool_result blocks
		for i, toolCallBlock := range toolCallBlocks {
			contentBlocksData = append(contentBlocksData, map[string]interface{}{
				"id":           uuid.New().String(),
				"type":         "tool_result",
				"tool_call_id": *toolCallBlock.ToolCallID,
				"content":      denialContent,
				"is_error":     true,
				"position":     i + 1,
			})
		}

		// seq is what the client sorts by; without it this message
		// deserializes at seq 0 and jumps to the top of the transcript.
		updateData := db.MessageUpdateData{
			UpdateType:    "message",
			ID:            messageID,
			ChatID:        chatID,
			Seq:           msg.Seq,
			Ordinal:       msg.Ordinal,
			Thread:        lastAssistantMsg.ThreadID,
			Role:          "tool",
			ContentBlocks: contentBlocksData,
			CreatedAt:     now.Format(time.RFC3339),
		}

		updateDataJSON, err := json.Marshal(updateData)
		if err != nil {
			return fmt.Errorf("failed to marshal chat_update data: %w", err)
		}

		if err := s.deps.Repo.CreateChatUpdate(txCtx, chatID, db.UpdateTypeMessage, messageID, string(updateDataJSON)); err != nil {
			return fmt.Errorf("failed to create chat_update: %w", err)
		}

		return nil
	})

	if err != nil {
		return err
	}

	logging.DebugContext(ctx, "[Approval] Created denial message",
		"chatID", chatID, "messageID", messageID, "toolCallCount", len(toolCallBlocks))

	return nil
}
