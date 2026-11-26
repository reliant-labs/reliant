package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
)

// BranchChat creates a new chat branched from a specific message ordinal
func (s *Service) BranchChat(
	ctx context.Context,
	req *connect.Request[reliantv1.BranchChatRequest],
) (*connect.Response[reliantv1.BranchChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// message_id is the primary way to identify the branch point
	if req.Msg.MessageId == "" {
		return nil, svcerr.InvalidArgument("message_id is required")
	}
	// A worktree is a checkout on one machine, so a branch with no machine
	// cannot name one, nor the workspace switch that describes copying into it.
	requestedWorktree := req.Msg.WorktreeId != nil && *req.Msg.WorktreeId != ""
	if req.Msg.GetNoMachine() && (requestedWorktree || req.Msg.WorkspaceContext != nil) {
		return nil, svcerr.InvalidArgument("no_machine and worktree_id/workspace_context are mutually exclusive: a worktree lives on a machine")
	}

	// Get the message directly by ID - simple and unambiguous
	branchPointMsg, err := s.deps.Repo.GetMessage(ctx, req.Msg.MessageId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get message"))
	}

	// The message knows its own chat - use that as the source
	sourceChatID := branchPointMsg.ChatID

	// Derive thread and context_sequence from context_window
	cw, err := s.deps.Repo.GetContextWindow(ctx, branchPointMsg.ContextWindowID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get context window"))
	}
	if cw == nil {
		return nil, svcerr.NotFound("context window")
	}
	branchThread := branchPointMsg.ThreadID
	// Note: cw.Sequence was previously used for branch_snapshot but is no longer needed
	// since inherited messages are resolved on-demand via CW chain

	// Get the source chat to verify ownership and get metadata
	sourceChat, err := s.chatViews().GetChat(ctx, sourceChatID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get source chat"))
	}
	if !authz.CanOwnChat(ctx, userID, sourceChat) {
		return nil, svcerr.NotFound("message")
	}

	// Get messages for context (for tool call adjustment and snapshot building)
	// Use threads.Service for proper fork chain resolution
	messages, err := s.threads.LoadCurrentMessages(ctx, branchThread)
	if err != nil {
		logging.WarnContext(ctx, "Failed to get messages for context", "error", err)
		messages = []*db.Message{branchPointMsg}
	}

	// If the branch point is an assistant message with tool calls, automatically adjust
	// to include the tool response message that follows
	if branchPointMsg.Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
		blocks, err := s.deps.Repo.ListContentBlocks(ctx, branchPointMsg.ID)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to validate branch point"))
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
					logging.WarnContext(ctx, "[BranchChat] Found orphaned tool calls at branch point, auto-repairing",
						"chatID", sourceChatID,
						"messageID", branchPointMsg.ID,
						"orphanCount", len(orphanedToolCalls))

					// Create repair tool message in source chat
					repairMsg, err := s.createBranchRepairToolMessage(ctx, sourceChatID, branchPointMsg, orphanedToolCalls)
					if err != nil {
						logging.ErrorContext(ctx, "[BranchChat] Failed to create repair tool message", "error", err)
						// Don't fail the branch - the in-memory repair in CallLLM will handle it
					} else {
						// Update branch point to the repair message
						branchPointMsg = repairMsg
						logging.InfoContext(ctx, "[BranchChat] Created repair tool message for branch",
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
		requestingChat, err := s.chatViews().GetChat(ctx, req.Msg.ChatId)
		if err == nil && authz.CanOwnChat(ctx, userID, requestingChat) {
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
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to resolve the project's main worktree"))
		}
		worktreeID = mainWorktreeID
	}
	var targetWorktree *db.Worktree // Store for system message creation
	if req.Msg.WorktreeId != nil && *req.Msg.WorktreeId != "" {
		// Verify the worktree exists and belongs to the same project
		worktree, err := s.deps.Repo.GetWorktree(ctx, *req.Msg.WorktreeId)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get worktree"))
		}
		if worktree.ProjectID != sourceChat.ProjectID {
			return nil, svcerr.InvalidArgument("worktree must belong to the same project")
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
		if wt, err := s.deps.Repo.GetWorktree(ctx, *worktreeID); err == nil && wt != nil && wt.DaemonID != nil && *wt.DaemonID != "" {
			activeDaemonID = wt.DaemonID
		}
	}
	// A worktree no machine owns (the main checkout, which every machine with
	// the project has) does not decide where the branch runs, so it stays on
	// the machine its source chat runs on. Left unpinned it fell to the user's
	// default machine: a chat on machine B branched onto machine A.
	if activeDaemonID == nil && !noMachine {
		sourceDaemonID, err := daemonplacement.ChatDaemonID(ctx, s.deps.Repo, fromChat)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to resolve the source chat's machine"))
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
		ID:     branchChatID,
		UserID: sourceChat.UserID,
		// A branch is the source chat's conversation continued: same org.
		OrgID: sourceChat.OrgID,
		// ... and about the same app.
		AppName:      sourceChat.AppName,
		Title:        title,
		ProjectID:    sourceChat.ProjectID,
		WorktreeID:   worktreeID,
		WorkflowName: sourceChat.WorkflowName,
		State:        db.ChatStateIdle,
		// The person who branched owns the branch: it is a conversation
		// whatever started the chat it came from.
		Origin:         db.ChatOriginUser,
		WorkflowID:     &branchWorkflowID, // Root workflow ID = chat ID for UI identification
		ActiveDaemonID: activeDaemonID,    // Pin to the worktree's owning daemon
		NoMachine:      noMachine,
		// The branch starts on what the chat it was branched from was set to:
		// its presets here, its selection (model, mode, ...) in the
		// transaction below. Without them the branch's first message ran on
		// the preset's default tier, whatever the user had picked.
		SelectedPresets: copyPresets(fromChat.SelectedPresets),
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
		LastActive:      time.Now().UTC(),
		LastMessageAt:   sourceChat.LastMessageAt, // Inherit from source chat
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
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to check the workflow can run without a machine"))
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
		logging.ErrorContext(ctx, "Failed to marshal chat_created data for branch", "error", marshalErr, "chatID", branchChat.ID)
	}

	// The branch chat row, its forked workflow+thread, the inherited plan,
	// the hidden branch note, and the chat_created
	// announcement must not be observed apart. All of it is plain DB writes
	// (no Temporal/network calls), so it can all join one transaction: any
	// failure leaves no orphan chat, no thread-less chat, no half-copied
	// plan, and no announcement for a chat that doesn't exist.
	//
	// READ COMMITTED, because nothing here is a read-check-write that
	// SERIALIZABLE's proof protects. The chat, workflow, thread, plan and
	// note rows are new ids nobody else can write. What is read is history
	// before the fork point (the forked messages, the inherited sub-agents)
	// and the source chat's selection, projects and plans, each copied by one
	// statement. The note's context window is the new thread's own. The
	// shared writes are counter upserts on one row each (the message
	// ordinals, the chat's and the user's update streams) and the workflow
	// admission's chat and worktree row locks, which a lock keeps correct, not
	// a snapshot (CreateWorkflowWithThread, store.AllocateMessageOrder and
	// CreateUserUpdate document each).
	// SERIALIZABLE turned any concurrent write to the user's update stream (a
	// running chat writes it all the time) into 40001 "could not serialize
	// access due to concurrent update" on the user counter, and the whole
	// branch was retried: prod trace 82a3f948 ran six attempts in 10.75s, and
	// the client gave up at 10s (obs issue 4bd143b9).
	branchTx := orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts)
	if err := s.deps.Repo.RunTx(ctx, branchTx, func(txCtx context.Context) error {
		if err := s.deps.Repo.CreateChat(txCtx, branchChat); err != nil {
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

		if err := s.selections().Copy(txCtx, fromChat.ID, branchChatID, userID); err != nil {
			return fmt.Errorf("failed to copy the chat's selection to the branch: %w", err)
		}
		// ... and works in the same projects. A branch with no machine has
		// no checkout of any of them to work in.
		if !noMachine {
			if err := s.deps.Repo.CopyChatProjects(txCtx, fromChat.ID, branchChatID); err != nil {
				return err
			}
		}

		// Copy plans and tasks from source chat to branched thread, reset
		// to pending, in one statement.
		if err := s.deps.Repo.CopyChatPlans(txCtx, sourceChatID, branchWorkflowID); err != nil {
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
			if err := s.deps.Repo.CreateUserUpdate(txCtx, &db.UserUpdate{
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
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to create branch"))
	}

	// Answer from the committed row, not the in-memory struct: the root
	// workflow state (PENDING, which tells the client its first send is a
	// StartChat) is derived by the chat view and cannot be set by hand
	// without drifting from it.
	committed, err := s.chatViews().GetChat(ctx, branchChatID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to load branch"))
	}
	// "Continue without machine" ends the chat it leaves waiting for its
	// machine: the conversation now continues in the branch.
	if req.Msg.GetNoMachine() && !fromChat.NoMachine {
		s.abandonMachineWait(ctx, fromChat)
	}
	return connect.NewResponse(&reliantv1.BranchChatResponse{
		Chat: s.chatToProtoWithSelection(ctx, committed),
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
func (s *Service) createBranchNoteMessage(
	ctx context.Context,
	branchChat *db.Chat,
	branchWorkflowID string,
	sourceChat *db.Chat,
	forkPoint *db.Message,
	targetWorktree *db.Worktree,
	workspaceContext *reliantv1.WorkspaceBranchContext,
) error {
	inherited, err := s.deps.Spawns.ListInheritedSpawnChildren(ctx, branchWorkflowID)
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
	if _, err := s.deps.Repo.SaveMessageToThread(ctx, branchChat.ID, branchWorkflowID, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), messageContent, &branchWorkflowID, nil, &hiddenStyle); err != nil {
		return fmt.Errorf("failed to save branch note: %w", err)
	}
	return nil
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
func (s *Service) findOrphanedToolCalls(ctx context.Context, toolCallIDs []string, toolCallBlocks []*db.MessageContentBlock) []orphanedToolCallInfo {
	var orphaned []orphanedToolCallInfo

	for i, toolCallID := range toolCallIDs {
		// Check if there's a matching tool_result in the database
		resultBlock, err := s.deps.ToolBlocks.FindToolBlock(ctx, toolCallID, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT)
		if err != nil {
			logging.WarnContext(ctx, "[BranchChat] Failed to check for tool result", "toolCallID", toolCallID, "error", err)
			continue
		}

		if resultBlock == nil {
			// A call whose durable row is still non-terminal is running, not
			// orphaned. Only a row that exists says this; a call with no row at
			// all predates the tool_calls table or died before recording
			// anything, and is treated as orphaned exactly as before.
			if call, callErr := s.deps.Repo.GetToolCall(ctx, toolCallID); callErr == nil && call != nil && !call.Status.IsTerminal() {
				logging.InfoContext(ctx, "[BranchChat] Skipping in-flight tool call at branch point",
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
func (s *Service) createBranchRepairToolMessage(
	ctx context.Context,
	chatID string,
	assistantMsg *db.Message,
	orphanedToolCalls []orphanedToolCallInfo,
) (*db.Message, error) {
	now := time.Now()
	msgID := uuid.New().String()

	// Create the tool message using the same context_window_id as the assistant message
	repairMsg := &db.Message{
		ID:              msgID,
		ChatID:          chatID,
		ThreadID:        assistantMsg.ThreadID,
		ContextWindowID: assistantMsg.ContextWindowID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_TOOL,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	// The next ordinal and seq are allocated with the insert.
	if err := s.deps.Repo.CreateNumberedMessage(ctx, repairMsg); err != nil {
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

		if err := s.deps.Repo.CreateContentBlock(ctx, block); err != nil {
			logging.ErrorContext(ctx, "[BranchChat] Failed to create repair content block",
				"error", err,
				"blockID", blockID,
				"toolCallID", orphan.ToolCallID)
			// Continue with other blocks
			continue
		}
	}

	return repairMsg, nil
}

// copyPresets is a preset selection the branch can own; nil for none.
func copyPresets(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, name := range in {
		out[key] = name
	}
	return out
}
