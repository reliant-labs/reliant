// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
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
	// A worktree is a checkout on one machine, so a branch with no machine
	// cannot name one, nor the workspace switch that describes copying into it.
	requestedWorktree := req.Msg.WorktreeId != nil && *req.Msg.WorktreeId != ""
	if req.Msg.GetNoMachine() && (requestedWorktree || req.Msg.WorkspaceContext != nil) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("no_machine and worktree_id/workspace_context are mutually exclusive: a worktree lives on a machine"))
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
				if msg.Seq > branchPointMsg.Seq &&
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
	// The chat the user branched from: the source chat, unless the branch
	// point is a message it inherited from an earlier chat.
	fromChat := sourceChat
	// If the requesting chat differs from the source chat (branching from an inherited message),
	// use the requesting chat's worktree as the default instead of the message's original chat's worktree.
	if req.Msg.ChatId != "" && req.Msg.ChatId != sourceChatID {
		requestingChat, err := s.database.GetChat(ctx, req.Msg.ChatId)
		if err == nil && requestingChat.UserID == userID {
			worktreeID = requestingChat.WorktreeID
			fromChat = requestingChat
		}
	}

	// A branch with no machine ("Continue without machine",
	// research/NO_MACHINE_CHATS.md) carries the conversation and nothing
	// machine-bound: it binds to the project's main worktree, as every
	// no-machine chat does (the chat list groups by worktree), and pins no
	// daemon. A branch of a chat that already has no machine stays without one
	// unless it names a worktree, which is a machine's checkout.
	noMachine := req.Msg.GetNoMachine() || (fromChat.NoMachine && !requestedWorktree)
	if noMachine {
		mainWorktreeID, err := s.launcher().ResolveChatWorktreeID(ctx, sourceChat.ProjectID, nil)
		if err != nil {
			return nil, launchErrorToConnect(err)
		}
		worktreeID = mainWorktreeID
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

	// Pin the branch to the daemon that owns its worktree's checkout on disk.
	// A branch chat's worktree exists on only one daemon; without this the
	// chat's tool calls resolve to a default daemon that lacks the path (the
	// "branch chat didn't work" bug). Derived from the resolved worktree so the
	// session daemon is set from the first message, not lazily on interaction.
	var activeDaemonID *string
	if worktreeID != nil && *worktreeID != "" && !noMachine {
		if wt, err := s.database.GetWorktree(ctx, *worktreeID); err == nil && wt != nil && wt.DaemonID != nil && *wt.DaemonID != "" {
			activeDaemonID = wt.DaemonID
		}
	}
	// A worktree no machine owns (the main checkout, which every machine with
	// the project has) does not decide where the branch runs, so it stays on
	// the machine its source chat runs on. Left unpinned it fell to the user's
	// default machine: a chat on machine B branched onto machine A.
	if activeDaemonID == nil && !noMachine {
		sourceDaemonID, err := chatDaemonID(ctx, s.database, fromChat)
		if err != nil {
			logging.Error("Failed to resolve source chat's machine for branch", "error", err, "chatID", fromChat.ID)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resolve the source chat's machine"))
		}
		if sourceDaemonID != "" {
			activeDaemonID = &sourceDaemonID
		}
	}

	// Create new branched chat with pointer to parent (NO message copying)
	// IMPORTANT: Set workflow_id = chat_id for root workflow identification
	// This is consistent with StartChat behavior and ensures UI can detect root workflows
	branchChatID := uuid.New().String()
	branchWorkflowID := branchChatID // Root workflow ID = chat ID (same pattern as StartChat)

	// NOTE: Context inheritance is now handled via workflow fork (see below)
	// The Chat struct no longer has BranchedFromChatID, BranchedAtOrdinal, ParentContextSequence
	branchChat := &db.Chat{
		ID:             branchChatID,
		UserID:         sourceChat.UserID,
		Title:          title,
		ProjectID:      sourceChat.ProjectID,
		WorktreeID:     worktreeID,
		WorkflowName:   sourceChat.WorkflowName,
		State:          db.ChatStateIdle,
		WorkflowID:     &branchWorkflowID, // Root workflow ID = chat ID for UI identification
		ActiveDaemonID: activeDaemonID,    // Pin to the worktree's owning daemon
		NoMachine:      noMachine,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		LastActive:     time.Now().UTC(),
		LastMessageAt:  sourceChat.LastMessageAt, // Inherit from source chat
	}

	// Determine workflow name - inherit from source chat or use user's default
	var workflowName string
	if sourceChat.WorkflowName != nil && *sourceChat.WorkflowName != "" {
		workflowName = *sourceChat.WorkflowName
	} else {
		// Source chat has no workflow, use user's default preference
		workflowName = s.launcher().ResolveDefaultWorkflow(ctx, userID, "")
	}
	// Refuse now a workflow that cannot run without a machine, rather than
	// leave a branch whose first send is bound to fail.
	if noMachine {
		if err := s.launcher().ValidateNoMachine(ctx, userID, workflowName, sourceChat.ProjectID); err != nil {
			return nil, launchErrorToConnect(err)
		}
	}

	// Create root workflow - fork metadata lives in the Thread record, not here
	rootWorkflow := &db.Workflow{
		ID:           branchWorkflowID,
		ChatID:       branchChatID,
		WorkflowName: workflowName,
		Thread:       branchWorkflowID, // Root workflow: thread = workflow ID
		Status:       db.Pending(),     // Pending until first message (allows workflow switching)
		CreatedAt:    time.Now().UTC(),
		OwnerUserID:  &userID,
	}

	// Announce the new chat on the user stream, exactly as StartChat does.
	//
	// Without this the branch exists but no client knows about it: the chat list
	// is patched from user updates, so the new branch only appears after a full
	// page refetch. A branch is a chat creation from the list's point of view,
	// and the fact that it was produced by forking rather than by StartChat is
	// not something the list cares about.
	chatCreatedData := map[string]interface{}{
		"chat_id":     branchChat.ID,
		"title":       branchChat.Title,
		"project_id":  branchChat.ProjectID,
		"worktree_id": branchChat.WorktreeID,
		"workflow":    branchChat.WorkflowName,
		"state":       string(branchChat.State),
		"created_at":  branchChat.CreatedAt.Format(time.RFC3339),
		"no_machine":  branchChat.NoMachine,
	}
	chatCreatedJSON, marshalErr := json.Marshal(chatCreatedData)
	if marshalErr != nil {
		logging.Error("Failed to marshal chat_created data for branch", "error", marshalErr, "chatID", branchChat.ID)
	}

	// The branch chat row, its forked workflow+thread, the inherited plan,
	// the hidden branch note, and the chat_created
	// announcement must not be observed apart. All of it is plain DB writes
	// (no Temporal/network calls), so it can all join one transaction: any
	// failure leaves no orphan chat, no thread-less chat, no half-copied
	// plan, and no announcement for a chat that doesn't exist.
	if err := s.database.RunTx(ctx, func(txCtx context.Context) error {
		if err := s.database.CreateChat(txCtx, branchChat); err != nil {
			return fmt.Errorf("failed to create branch: %w", err)
		}

		// Create workflow and thread atomically using CreateWorkflowWithThread.
		// This is a cross-conversation fork (parent thread in different conversation)
		// The service extracts thread, CW, and ordinal from the branch point message
		if _, _, _, err := s.threads.CreateWorkflowWithThread(txCtx, threads.CreateWorkflowWithThreadOpts{
			Workflow:        rootWorkflow,
			ThreadID:        branchWorkflowID,
			ChatID:          branchChatID,
			ForkFromMessage: &branchPointMsg.ID,
		}); err != nil {
			return fmt.Errorf("failed to create workflow fork: %w", err)
		}

		// Copy plan and tasks from source chat to branched thread
		if err := s.copyPlanAndTasks(txCtx, sourceChatID, branchWorkflowID); err != nil {
			return fmt.Errorf("failed to copy plan to branched chat: %w", err)
		}

		// NOTE: branch_snapshot is no longer created here.
		// Inherited messages are now resolved on-demand via the context window chain
		// when ListMessages is called. This eliminates stale snapshot data and
		// ensures consistent message resolution between LLM context and UI display.
		// See ListMessages() for the CW chain resolution implementation.

		// Every branch starts with a hidden note telling the model what it
		// is. Read after the thread exists, inside this transaction, so the
		// inherited sub-agents it counts are exactly the ones spawn_status
		// will list for this branch.
		if err := s.createBranchNoteMessage(txCtx, branchChat, branchWorkflowID, sourceChat, branchPointMsg, targetWorktree, req.Msg.WorkspaceContext); err != nil {
			return fmt.Errorf("failed to create branch note: %w", err)
		}

		if chatCreatedJSON != nil {
			if err := s.database.CreateUserUpdate(txCtx, &db.UserUpdate{
				UserID:     userID,
				ProjectID:  &branchChat.ProjectID,
				WorktreeID: branchChat.WorktreeID,
				ChatID:     &branchChat.ID,
				UpdateType: db.UserUpdateChatCreated,
				EntityType: db.EntityTypeChat,
				EntityID:   branchChat.ID,
				Data:       chatCreatedJSON,
			}); err != nil {
				return fmt.Errorf("failed to emit chat_created user update for branch: %w", err)
			}
		}

		return nil
	}); err != nil {
		logging.Error("Failed to create branched chat", "error", err, "chatID", branchChatID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create branch"))
	}

	// Answer from the committed row, not the in-memory struct: the root
	// workflow state (PENDING, which tells the client its first send is a
	// StartChat) is derived by the chat view and cannot be set by hand
	// without drifting from it.
	committed, err := s.database.GetChat(ctx, branchChatID)
	if err != nil {
		logging.Error("Failed to re-read branched chat after commit", "error", err, "chatID", branchChatID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load branch"))
	}
	// "Continue without machine" ends the chat it leaves waiting for its
	// machine: the conversation now continues in the branch.
	if req.Msg.GetNoMachine() && !fromChat.NoMachine {
		s.abandonMachineWait(ctx, fromChat)
	}
	return connect.NewResponse(&reliantv1.BranchChatResponse{
		Chat: chatToProto(committed),
	}), nil
}

// createBranchNoteMessage writes the hidden note every branch starts with.
//
// A branch inherits a transcript that reads, to its model, as its own past —
// including spawn handles promising "you will be notified" that will never
// notify it, because the original chat still owns those sub-agents and keeps
// receiving their results. The note states the facts that transcript cannot.
//
// Written once, at branch time, from values fixed at that moment, so it is
// byte-stable for prompt caching. SYSTEM role and HIDDEN style: it reaches the
// model through the ordinary history load (each driver renders a SYSTEM turn
// as a <system> block) and stays out of the user's transcript.
func (s *ChatService) createBranchNoteMessage(
	ctx context.Context,
	branchChat *db.Chat,
	branchWorkflowID string,
	sourceChat *db.Chat,
	forkPoint *db.Message,
	targetWorktree *db.Worktree,
	workspaceContext *reliantv1.WorkspaceBranchContext,
) error {
	inherited, err := s.database.ListInheritedSpawnChildren(ctx, branchWorkflowID)
	if err != nil {
		return fmt.Errorf("failed to list inherited sub-agents: %w", err)
	}
	messageContent := branchContextNote(sourceChat, forkPoint, inherited)
	if targetWorktree != nil {
		messageContent += "\n\n" + workspaceBranchNote(targetWorktree, workspaceContext)
	}

	// SaveMessageToThread handles ordinal and context_sequence, inheriting
	// the forked thread's context sequence.
	hiddenStyle := int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN)
	if _, err := s.database.SaveMessageToThread(ctx, branchChat.ID, branchWorkflowID, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), messageContent, &branchWorkflowID, nil, &hiddenStyle); err != nil {
		return fmt.Errorf("failed to save branch note: %w", err)
	}
	return nil
}

// branchContextNote states what a branch is: of which chat, from which point,
// and — only when there are any — what became of the sub-agents the original
// had already spawned. Facts only, a few lines; the model decides what to do
// with them.
func branchContextNote(sourceChat *db.Chat, forkPoint *db.Message, inherited []*db.InheritedSpawnChild) string {
	source := "chat " + sourceChat.ID
	if sourceChat.Title != "" {
		source = fmt.Sprintf("%q (chat %s)", sourceChat.Title, sourceChat.ID)
	}
	note := fmt.Sprintf(
		"This chat is a branch of %s, forked at the message above (%s). The original chat may still be running.",
		source, forkPoint.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"))

	// One agent per child thread: a resumed spawn has a row per resumption.
	// A call whose child never landed still counts, keyed by its tool call.
	agents := map[string]bool{}
	ownedBySource := true
	for _, child := range inherited {
		key := child.ToolCallID
		if child.ChildThreadID != nil {
			key = *child.ChildThreadID
		}
		agents[key] = true
		if child.SourceChatID != sourceChat.ID {
			// A branch of a branch also inherits what older chats spawned.
			ownedBySource = false
		}
	}
	owner := "the original chat"
	if !ownedBySource {
		owner = "the chats that spawned them"
	}
	switch n := len(agents); {
	case n == 1:
		note += "\nThe sub-agent spawned before the branch belongs to " + owner + ". " +
			"Here it is visible read-only with spawn_status; its results and notifications are not delivered to this chat. " +
			"Re-spawn anything you need from this chat."
	case n > 1:
		note += fmt.Sprintf("\nThe %d sub-agents spawned before the branch belong to %s. ", n, owner) +
			"Here they are visible read-only with spawn_status; their results and notifications are not delivered to this chat. " +
			"Re-spawn anything you need from this chat."
	}
	return note
}

// workspaceBranchNote describes the workspace a branch was moved into, and
// which uncommitted files came with it.
func workspaceBranchNote(targetWorktree *db.Worktree, workspaceContext *reliantv1.WorkspaceBranchContext) string {
	messageContent := fmt.Sprintf("This conversation has been branched to workspace **%s** (branch: `%s`).\n\n",
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
	return messageContent
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
//
// A missing result does not by itself mean the call is dead. A spawn that is
// still running has no result yet and will write a real one to this thread when
// its sub-agent finishes, so the durable tool_calls row is consulted to tell the
// two apart: a non-terminal status is a call still in flight, and synthesizing a
// result for it would both lie about a live call and collide with the real
// result when it lands.
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
			// A call whose durable row is still non-terminal is running, not
			// orphaned. Only a row that exists says this; a call with no row at
			// all predates the tool_calls table or died before recording
			// anything, and is treated as orphaned exactly as before.
			if call, callErr := s.database.GetToolCall(ctx, toolCallID); callErr == nil && call != nil && !call.Status.IsTerminal() {
				logging.Info("[BranchChat] Skipping in-flight tool call at branch point",
					"toolCallID", toolCallID,
					"status", call.Status,
					"childWorkflowID", call.ChildWorkflowID)
				continue
			}

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

	// Get next chat-global seq. See 20260802000000_add_message_seq.sql.
	nextSeq, err := s.database.GetNextSeq(ctx, chatID, assistantMsg.ThreadID)
	if err != nil {
		return nil, fmt.Errorf("failed to get next seq: %w", err)
	}

	// Create the tool message using the same context_window_id as the assistant message
	repairMsg := &db.Message{
		ID:              msgID,
		ChatID:          chatID,
		Ordinal:         nextOrdinal,
		Seq:             nextSeq,
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
		content := handlers.InterruptedToolResultContent

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
		threadRecord, parentChatID, err := s.database.GetThreadWithParent(ctx, *chat.WorkflowID)
		if err != nil {
			// Skip chats without valid threads
			continue
		}

		// A branch is a FORK that crossed into another chat. Origin == fork
		// rules out spawn/node children and roots but doesn't say which chat
		// it came from; the parent chat_id comparison does that part, and
		// also establishes the crossing (this chat differs from the parent's,
		// or it would not be listed as a branch OF that chat).
		if threadRecord.Origin == db.ThreadOriginFork && parentChatID != nil && *parentChatID == req.Msg.ChatId {
			branch := &reliantv1.BranchInfo{
				Id:         chat.ID,
				Title:      chat.Title,
				CreatedAt:  chat.CreatedAt.Format(time.RFC3339),
				LastActive: chat.LastActive.Format(time.RFC3339),
			}
			if threadRecord.ForkAtMessageID != nil {
				if forkMsg, err := s.database.GetMessage(ctx, *threadRecord.ForkAtMessageID); err == nil && forkMsg != nil {
					branch.BranchedAtOrdinal = &forkMsg.Ordinal
				}
			}
			branches = append(branches, branch)
		}
	}

	return connect.NewResponse(&reliantv1.ListBranchesResponse{
		Branches: branches,
		Total:    int32(len(branches)),
	}), nil
}
