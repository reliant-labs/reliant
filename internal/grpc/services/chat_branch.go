// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/threads"
)

// BranchChat creates a new chat branched from a specific message ordinal
func (s *ChatService) BranchChat(
	ctx context.Context,
	req *connect.Request[reliantv1.BranchChatRequest],
) (*connect.Response[reliantv1.BranchChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// message_id is the primary way to identify the branch point
	if req.Msg.MessageId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("message_id is required"))
	}

	// Get the message directly by ID - simple and unambiguous
	branchPointMsg, err := s.database.GetMessage(ctx, req.Msg.MessageId)
	if err != nil {
		logging.Error("Failed to get branch point message", "error", err, "messageID", req.Msg.MessageId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("message not found"))
	}

	// The message knows its own chat - use that as the source
	sourceChatID := branchPointMsg.ChatID

	// Derive thread and context_sequence from context_window
	cw, err := s.database.GetContextWindow(ctx, branchPointMsg.ContextWindowID)
	if err != nil || cw == nil {
		logging.Error("Failed to get context window for branch point message", "error", err, "messageID", req.Msg.MessageId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("context window not found"))
	}
	branchThread := branchPointMsg.ThreadID
	// Note: cw.Sequence was previously used for branch_snapshot but is no longer needed
	// since inherited messages are resolved on-demand via CW chain

	// Get the source chat to verify ownership and get metadata
	sourceChat, err := s.database.GetChat(ctx, sourceChatID)
	if err != nil {
		logging.Error("Failed to get source chat", "error", err, "chatID", sourceChatID)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("source chat not found"))
	}
	if sourceChat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("message not found"))
	}

	// Get messages for context (for tool call adjustment and snapshot building)
	// Use threads.Service for proper fork chain resolution
	messages, err := s.threads.LoadCurrentMessages(ctx, branchThread)
	if err != nil {
		logging.Warn("Failed to get messages for context", "error", err)
		messages = []*db.Message{branchPointMsg}
	}

	// If the branch point is an assistant message with tool calls, automatically adjust
	// to include the tool response message that follows
	if branchPointMsg.Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
		blocks, err := s.database.ListContentBlocks(ctx, branchPointMsg.ID)
		if err != nil {
			logging.Error("Failed to check for tool calls", "error", err, "messageID", branchPointMsg.ID)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to validate branch point"))
		}

		// Collect tool_call IDs from this assistant message
		var toolCallIDs []string
		var toolCallBlocks []*db.MessageContentBlock
		for _, block := range blocks {
			if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL && block.ToolCallID != nil {
				toolCallIDs = append(toolCallIDs, *block.ToolCallID)
				toolCallBlocks = append(toolCallBlocks, block)
			}
		}

		// Find next tool response message if there are tool calls
		if len(toolCallIDs) > 0 {
			foundToolMsg := false
			for _, msg := range messages {
				// Tool message should be in the same context window as the assistant message
				if msg.Ordinal > branchPointMsg.Ordinal &&
					msg.Role == reliantv1.MessageRole_MESSAGE_ROLE_TOOL &&
					msg.ContextWindowID == branchPointMsg.ContextWindowID {
					branchPointMsg = msg
					foundToolMsg = true
					break
				}
			}

			// Safety net: If assistant has tool calls but no tool message follows,
			// check for orphaned tool calls and auto-repair before branching.
			// This handles cases where cleanup hasn't run on the source conversation.
			if !foundToolMsg {
				orphanedToolCalls := s.findOrphanedToolCalls(ctx, toolCallIDs, toolCallBlocks)
				if len(orphanedToolCalls) > 0 {
					logging.Warn("[BranchChat] Found orphaned tool calls at branch point, auto-repairing",
						"chatID", sourceChatID,
						"messageID", branchPointMsg.ID,
						"orphanCount", len(orphanedToolCalls))

					// Create repair tool message in source chat
					repairMsg, err := s.createBranchRepairToolMessage(ctx, sourceChatID, branchPointMsg, orphanedToolCalls)
					if err != nil {
						logging.Error("[BranchChat] Failed to create repair tool message", "error", err)
						// Don't fail the branch - the in-memory repair in CallLLM will handle it
					} else {
						// Update branch point to the repair message
						branchPointMsg = repairMsg
						logging.Info("[BranchChat] Created repair tool message for branch",
							"repairMessageID", repairMsg.ID,
							"repairOrdinal", repairMsg.Ordinal)
					}
				}
			}
		}
	}

	// Prepare title
	title := ""
	if req.Msg.Title != nil {
		title = *req.Msg.Title
	}
	if title == "" {
		title = fmt.Sprintf("%s (branch)", sourceChat.Title)
	}

	// Determine worktree ID - use provided one or inherit from the requesting chat
	worktreeID := sourceChat.WorktreeID
	// If the requesting chat differs from the source chat (branching from an inherited message),
	// use the requesting chat's worktree as the default instead of the message's original chat's worktree.
	if req.Msg.ChatId != "" && req.Msg.ChatId != sourceChatID {
		requestingChat, err := s.database.GetChat(ctx, req.Msg.ChatId)
		if err == nil && requestingChat.UserID == userID {
			worktreeID = requestingChat.WorktreeID
		}
	}
	var targetWorktree *db.Worktree // Store for system message creation
	if req.Msg.WorktreeId != nil && *req.Msg.WorktreeId != "" {
		// Verify the worktree exists and belongs to the same project
		worktree, err := s.database.GetWorktree(ctx, *req.Msg.WorktreeId)
		if err != nil {
			logging.Error("Failed to get worktree for branch", "error", err, "worktreeID", *req.Msg.WorktreeId)
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found"))
		}
		if worktree.ProjectID != sourceChat.ProjectID {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worktree must belong to the same project"))
		}
		worktreeID = req.Msg.WorktreeId
		targetWorktree = worktree
	}

	// Create new branched chat with pointer to parent (NO message copying)
	// IMPORTANT: Set workflow_id = chat_id for root workflow identification
	// This is consistent with CreateChat behavior and ensures UI can detect root workflows
	branchChatID := uuid.New().String()
	branchWorkflowID := branchChatID // Root workflow ID = chat ID (same pattern as CreateChat)

	// NOTE: Context inheritance is now handled via workflow fork (see below)
	// The Chat struct no longer has BranchedFromChatID, BranchedAtOrdinal, ParentContextSequence
	branchChat := &db.Chat{
		ID:            branchChatID,
		UserID:        sourceChat.UserID,
		Title:         title,
		ProjectID:     sourceChat.ProjectID,
		WorktreeID:    worktreeID,
		WorkflowName:  sourceChat.WorkflowName,
		State:         db.ChatStateIdle,
		WorkflowID:    &branchWorkflowID, // Root workflow ID = chat ID for UI identification
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
		LastActive:    time.Now().UTC(),
		LastMessageAt: sourceChat.LastMessageAt, // Inherit from source chat
	}

	// Create the branched chat
	if err := s.database.CreateChat(ctx, branchChat); err != nil {
		logging.Error("Failed to create branched chat", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create branch"))
	}

	// Determine workflow name - inherit from source chat or use user's default
	var workflowName string
	if sourceChat.WorkflowName != nil && *sourceChat.WorkflowName != "" {
		workflowName = *sourceChat.WorkflowName
	} else {
		// Source chat has no workflow, use user's default preference
		workflowName = s.resolveDefaultWorkflow(ctx, userID, "")
	}

	// Create root workflow - fork metadata lives in the Thread record, not here
	rootWorkflow := &db.Workflow{
		ID:           branchWorkflowID,
		ChatID:       branchChatID,
		WorkflowName: workflowName,
		Thread:       branchWorkflowID,         // Root workflow: thread = workflow ID
		Status:       db.WorkflowStatusPending, // Pending until first message (allows workflow switching)
		CreatedAt:    time.Now().UTC(),
	}

	// Create workflow and thread atomically using CreateWorkflowWithThread
	// This is a cross-conversation fork (parent thread in different conversation)
	// The service extracts thread, CW, and ordinal from the branch point message
	_, _, _, err = s.threads.CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
		Workflow:        rootWorkflow,
		ThreadID:        branchWorkflowID,
		ChatID:          branchChatID,
		ForkFromMessage: &branchPointMsg.ID,
	})
	if err != nil {
		logging.Error("Failed to create workflow with thread for branched chat", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create workflow fork"))
	}

	// Copy plan and tasks from source chat to branched thread
	if err := s.copyPlanAndTasks(ctx, sourceChatID, branchWorkflowID); err != nil {
		// Non-fatal - log but don't fail the branch operation
		logging.Error("Failed to copy plan to branched chat", "error", err, "sourceChatID", sourceChatID, "targetThreadID", branchWorkflowID)
	}

	// NOTE: branch_snapshot is no longer created here.
	// Inherited messages are now resolved on-demand via the context window chain
	// when ListMessages is called. This eliminates stale snapshot data and
	// ensures consistent message resolution between LLM context and UI display.
	// See ListMessages() for the CW chain resolution implementation.

	// Create a system message when branching to a different worktree
	// This helps the user understand the context of the new workspace
	if targetWorktree != nil {
		if err := s.createWorkspaceBranchSystemMessage(ctx, branchChat, targetWorktree, branchWorkflowID, req.Msg.WorkspaceContext); err != nil {
			logging.Error("Failed to create workspace branch system message", "error", err)
			// Non-fatal - the chat is created, just won't have the system message
		}
	}

	branchChatProto := chatToProto(branchChat)
	return connect.NewResponse(&reliantv1.BranchChatResponse{
		Chat: branchChatProto,
	}), nil
}

// createWorkspaceBranchSystemMessage creates a system message when branching to a different workspace
// This helps the user understand the context and what files were copied
func (s *ChatService) createWorkspaceBranchSystemMessage(
	ctx context.Context,
	branchChat *db.Chat,
	targetWorktree *db.Worktree,
	branchWorkflowID string,
	workspaceContext *reliantv1.WorkspaceBranchContext,
) error {
	// Build the system message content
	var messageContent string

	// Base message about workspace branching
	messageContent = fmt.Sprintf("This conversation has been branched to workspace **%s** (branch: `%s`).\n\n",
		targetWorktree.Name, targetWorktree.Branch)
	messageContent += "All code changes should be made in this workspace."

	// Add information about copied files if workspace context is provided
	if workspaceContext != nil {
		if workspaceContext.CopyFilesEnabled {
			if len(workspaceContext.FilesCopied) > 0 {
				messageContent += fmt.Sprintf("\n\n**Uncommitted files copied from source workspace (%d files):**\n",
					len(workspaceContext.FilesCopied))
				// Limit the list to avoid very long messages
				maxFiles := 10
				for i, file := range workspaceContext.FilesCopied {
					if i >= maxFiles {
						messageContent += fmt.Sprintf("- ... and %d more files\n", len(workspaceContext.FilesCopied)-maxFiles)
						break
					}
					messageContent += fmt.Sprintf("- `%s`\n", file)
				}
			} else {
				messageContent += "\n\nUncommitted files were set to be copied, but no files needed copying."
			}
		} else {
			messageContent += "\n\n**Note:** Uncommitted files were not copied. Only committed changes are shared between workspaces."
		}
	}

	// Use SaveMessageToThread to properly handle ordinal and context_sequence.
	// This automatically inherits the correct context_sequence from the forked workflow.
	// Use system role with display_style=hidden so it's sent to LLM but not shown in UI.
	hiddenStyle := int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN)
	_, err := s.database.SaveMessageToThread(ctx, branchChat.ID, branchWorkflowID, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), messageContent, &branchWorkflowID, nil, &hiddenStyle)
	if err != nil {
		return fmt.Errorf("failed to create system message: %w", err)
	}

	return nil
}

// copyPlanAndTasks copies the plan and tasks from a source chat to a target thread.
// This preserves the task hierarchy by mapping old task IDs to new ones.
// Task statuses are reset to "pending" in the new thread.
func (s *ChatService) copyPlanAndTasks(ctx context.Context, sourceChatID, targetThreadID string) error {
	// Get plans from the source chat
	plans, err := s.database.ListPlansByChatID(ctx, sourceChatID)
	if err != nil {
		return fmt.Errorf("failed to list plans: %w", err)
	}

	if len(plans) == 0 {
		// No plan to copy
		return nil
	}

	// Copy each plan (typically there's just one)
	for _, sourcePlan := range plans {
		newPlanID := uuid.New().String()

		// Create the new plan with reset status
		newPlan := &db.Plan{
			ID:          newPlanID,
			ThreadID:    targetThreadID,
			Title:       sourcePlan.Title,
			Description: sourcePlan.Description,
			Status:      int32(db.PlanStatusPending), // Reset to pending
			Complexity:  sourcePlan.Complexity,
		}

		if err := s.database.CreatePlan(ctx, newPlan); err != nil {
			return fmt.Errorf("failed to create plan: %w", err)
		}

		// Get all tasks for this plan
		tasks, err := s.database.ListTasksByPlan(ctx, sourcePlan.ID)
		if err != nil {
			return fmt.Errorf("failed to list tasks: %w", err)
		}

		// Map old task IDs to new task IDs for parent reference resolution
		taskIDMap := make(map[string]string)

		// First pass: create all tasks with new IDs (without parent references)
		for _, sourceTask := range tasks {
			newTaskID := uuid.New().String()
			taskIDMap[sourceTask.ID] = newTaskID
		}

		// Second pass: create tasks with proper parent references
		for _, sourceTask := range tasks {
			newTaskID := taskIDMap[sourceTask.ID]

			// Map parent task ID if present
			var newParentTaskID *string
			if sourceTask.ParentTaskID != nil {
				if mappedID, ok := taskIDMap[*sourceTask.ParentTaskID]; ok {
					newParentTaskID = &mappedID
				}
			}

			newTask := &db.Task{
				ID:           newTaskID,
				PlanID:       newPlanID,
				ParentTaskID: newParentTaskID,
				Title:        sourceTask.Title,
				Description:  sourceTask.Description,
				Status:       int32(db.TaskStatusPending), // Reset to pending
				Position:     sourceTask.Position,
			}

			if err := s.database.CreateTask(ctx, newTask); err != nil {
				return fmt.Errorf("failed to create task: %w", err)
			}
		}

		logging.Debug("Copied plan and tasks to branched chat",
			"sourcePlanID", sourcePlan.ID,
			"newPlanID", newPlanID,
			"taskCount", len(tasks),
			"targetThreadID", targetThreadID,
		)
	}

	return nil
}

// orphanedToolCallInfo holds information about an orphaned tool call for branch repair
type orphanedToolCallInfo struct {
	ToolCallID string
	ToolName   string
}

// findOrphanedToolCalls checks which tool calls don't have matching tool results
func (s *ChatService) findOrphanedToolCalls(ctx context.Context, toolCallIDs []string, toolCallBlocks []*db.MessageContentBlock) []orphanedToolCallInfo {
	var orphaned []orphanedToolCallInfo

	for i, toolCallID := range toolCallIDs {
		// Check if there's a matching tool_result in the database
		resultBlock, err := s.database.GetToolResultBlock(ctx, toolCallID)
		if err != nil {
			logging.Warn("[BranchChat] Failed to check for tool result", "toolCallID", toolCallID, "error", err)
			continue
		}

		if resultBlock == nil {
			toolName := "unknown"
			if i < len(toolCallBlocks) && toolCallBlocks[i].ToolName != nil {
				toolName = *toolCallBlocks[i].ToolName
			}
			orphaned = append(orphaned, orphanedToolCallInfo{
				ToolCallID: toolCallID,
				ToolName:   toolName,
			})
		}
	}

	return orphaned
}

// createBranchRepairToolMessage creates a tool message with synthetic tool_results
// for orphaned tool calls. This repairs the conversation state so branching can proceed
// with a valid message history.
func (s *ChatService) createBranchRepairToolMessage(
	ctx context.Context,
	chatID string,
	assistantMsg *db.Message,
	orphanedToolCalls []orphanedToolCallInfo,
) (*db.Message, error) {
	now := time.Now()
	msgID := uuid.New().String()

	// Get next ordinal for this thread
	nextOrdinal, err := s.database.GetNextOrdinal(ctx, assistantMsg.ThreadID)
	if err != nil {
		return nil, fmt.Errorf("failed to get next ordinal: %w", err)
	}

	// Create the tool message using the same context_window_id as the assistant message
	repairMsg := &db.Message{
		ID:              msgID,
		ChatID:          chatID,
		Ordinal:         nextOrdinal,
		ThreadID:        assistantMsg.ThreadID,
		ContextWindowID: assistantMsg.ContextWindowID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if err := s.database.CreateMessage(ctx, repairMsg); err != nil {
		return nil, fmt.Errorf("failed to create repair message: %w", err)
	}

	// Create tool_result content blocks for each orphaned tool call
	for i, orphan := range orphanedToolCalls {
		blockID := uuid.New().String()
		isError := true
		content := "Tool execution was cancelled before completion. The previous request was interrupted."

		block := &db.MessageContentBlock{
			ID:         blockID,
			MessageID:  msgID,
			Position:   i,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
			Content:    &content,
			ToolName:   &orphan.ToolName,
			ToolCallID: &orphan.ToolCallID,
			IsError:    &isError,
			IsComplete: true,
			CreatedAt:  now,
			UpdatedAt:  now,
		}

		if err := s.database.CreateContentBlock(ctx, block); err != nil {
			logging.Error("[BranchChat] Failed to create repair content block",
				"error", err,
				"blockID", blockID,
				"toolCallID", orphan.ToolCallID)
			// Continue with other blocks
			continue
		}
	}

	return repairMsg, nil
}

// ListBranches lists all chats branched from a parent chat
// Branches are identified via Thread records - a chat is a branch if its root thread
// has a parent thread in the requested chat
func (s *ChatService) ListBranches(
	ctx context.Context,
	req *connect.Request[reliantv1.ListBranchesRequest],
) (*connect.Response[reliantv1.ListBranchesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	// Verify user owns the parent chat
	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// List all chats for the user
	allChats, err := s.database.ListChats(ctx, db.ChatFilters{
		UserID: userID,
		Limit:  1000,
	})
	if err != nil {
		logging.Error("Failed to list chats", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list branches"))
	}

	// Find branches by checking each chat's root thread for fork metadata
	// A chat is a branch if its root thread's parent is in the requested chat
	var branches []*reliantv1.BranchInfo
	for _, chat := range allChats {
		if chat.WorkflowID == nil || *chat.WorkflowID == "" {
			continue
		}

		// Get the root thread to check fork metadata
		threadRecord, parentConversationID, err := s.database.GetThreadWithParent(ctx, *chat.WorkflowID)
		if err != nil {
			// Skip chats without valid threads
			continue
		}

		// Check if this thread was forked from the requested chat
		if parentConversationID != nil && *parentConversationID == req.Msg.ChatId {
			branch := &reliantv1.BranchInfo{
				Id:         chat.ID,
				Title:      chat.Title,
				CreatedAt:  chat.CreatedAt.Format(time.RFC3339),
				LastActive: chat.LastActive.Format(time.RFC3339),
			}
			if threadRecord.ForkAtOrdinal != nil {
				branch.BranchedAtOrdinal = threadRecord.ForkAtOrdinal
			}
			branches = append(branches, branch)
		}
	}

	return connect.NewResponse(&reliantv1.ListBranchesResponse{
		Branches: branches,
		Total:    int32(len(branches)),
	}), nil
}
