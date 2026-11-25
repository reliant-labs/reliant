package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// UpdateChat updates chat metadata
func (s *Service) UpdateChat(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateChatRequest],
) (*connect.Response[reliantv1.UpdateChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	chat, err := s.deps.Repo.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	if !authz.CanOwnChat(ctx, userID, chat) {
		return nil, svcerr.NotFound("chat")
	}

	// Update fields
	// NOTE: model/temperature/max_tokens are workflow input params, not stored on chat
	if req.Msg.Title != nil {
		chat.Title = *req.Msg.Title
	}
	if req.Msg.WorktreeId != nil {
		// Same invariant as creation: a chat may be moved between the project's
		// worktrees, but never to a foreign one and never cleared to null.
		resolved, err := s.launcher().ResolveChatWorktreeID(ctx, chat.ProjectID, req.Msg.WorktreeId)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to resolve worktree"))
		}
		// A chat pinned to one machine cannot move into a workspace whose
		// checkout is on another.
		if chat.ActiveDaemonID != nil && resolved != nil {
			if err := s.checkWorkspaceMachine(ctx, userID, *resolved, *chat.ActiveDaemonID); err != nil {
				return nil, err
			}
		}
		chat.WorktreeID = resolved
	}

	// Handle workflow name change - only allowed when root workflow is pending
	if req.Msg.WorkflowName != nil && *req.Msg.WorkflowName != "" {
		newWorkflowName := *req.Msg.WorkflowName

		// Validate workflow exists
		// A failure to read it is classified; anything else is a verdict.
		if _, err := s.launcher().LoadWorkflowForValidation(ctx, newWorkflowName, chat.ProjectID); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("workflow not found: %s", newWorkflowName)))
		}

		// Check if root workflow is pending (chat hasn't started)
		if chat.WorkflowID == nil || *chat.WorkflowID == "" {
			return nil, svcerr.FailedPrecondition("cannot change workflow: chat has no workflow assigned")
		}

		rootWorkflow, err := s.deps.Repo.GetWorkflow(ctx, *chat.WorkflowID)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get root workflow"))
		}

		if rootWorkflow.Status != db.Pending() {
			return nil, svcerr.FailedPrecondition("cannot change workflow after chat has started - use Branch instead")
		}

		// Update workflow name on the workflow record
		if err := s.deps.Repo.UpdateWorkflowName(ctx, *chat.WorkflowID, newWorkflowName); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update workflow"))
		}

		// Update chat's workflow name
		chat.WorkflowName = &newWorkflowName
	}

	chat.UpdatedAt = time.Now().UTC()
	chat.LastActive = time.Now().UTC()

	if err := s.deps.Repo.UpdateChat(ctx, chat); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update chat"))
	}

	// Emit chat_title_changed if title was updated
	if req.Msg.Title != nil {
		titleData, _ := json.Marshal(map[string]string{
			"chat_id": chat.ID,
			"title":   chat.Title,
		})
		if err := s.deps.Repo.CreateUserUpdate(ctx, &db.UserUpdate{
			UserID:     userID,
			ProjectID:  &chat.ProjectID,
			WorktreeID: chat.WorktreeID,
			ChatID:     &chat.ID,
			UpdateType: db.UserUpdateChatTitleChanged,
			EntityType: db.EntityTypeChat,
			EntityID:   chat.ID,
			Data:       titleData,
		}); err != nil {
			logging.WarnContext(ctx, "Failed to emit chat_title_changed", "error", err)
		}
	}

	// Emit chat_config_changed to user_updates when workflow_name changes.
	// (Replaces the dropped user_updates_chat_config_update SQL trigger.)
	if req.Msg.WorkflowName != nil && *req.Msg.WorkflowName != "" {
		configData, _ := json.Marshal(map[string]interface{}{
			"chat_id":       chat.ID,
			"workflow_name": *chat.WorkflowName,
		})
		if err := s.deps.Repo.CreateUserUpdate(ctx, &db.UserUpdate{
			UserID:     userID,
			ProjectID:  &chat.ProjectID,
			WorktreeID: chat.WorktreeID,
			ChatID:     &chat.ID,
			UpdateType: db.UserUpdateChatConfigChanged,
			EntityType: db.EntityTypeChat,
			EntityID:   chat.ID,
			Data:       configData,
		}); err != nil {
			logging.WarnContext(ctx, "Failed to emit chat_config_changed", "error", err)
		}
	}

	// Emit a chat-level update when any of workflow_name/state/title change.
	// (Replaces the dropped chat_updates_chat_update SQL trigger.)
	// State and title changes already produce their own specific chat_updates
	// via UpdateChatState and the message paths, but workflow_name changes
	// previously only had the trigger. Emit a general CHAT_UPDATE for it.
	if req.Msg.WorkflowName != nil || req.Msg.Title != nil {
		var wfName string
		if chat.WorkflowName != nil {
			wfName = *chat.WorkflowName
		}
		chatUpdateData, _ := json.Marshal(map[string]interface{}{
			"update_type":   int(db.UpdateTypeChat),
			"chat_id":       chat.ID,
			"workflow_name": wfName,
			"state":         int(chat.State),
			"title":         chat.Title,
		})
		if err := s.deps.Repo.CreateChatUpdate(ctx, chat.ID, db.UpdateTypeChat, chat.ID, string(chatUpdateData)); err != nil {
			logging.WarnContext(ctx, "Failed to emit chat_update for config change", "error", err)
		}
	}

	updateChatProto := chatToProto(chat)
	return connect.NewResponse(&reliantv1.UpdateChatResponse{
		Chat: updateChatProto,
	}), nil
}
