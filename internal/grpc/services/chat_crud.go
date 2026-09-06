// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// CreateChat creates a new chat and starts its workflow
func (s *ChatService) CreateChat(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateChatRequest],
) (*connect.Response[reliantv1.CreateChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	// Extract user and system messages from input
	userContent, systemMessages, hasUserContent := extractMessagesFromInput(req.Msg.Messages)

	// Require at least one user message or attachments
	if !hasUserContent && len(req.Msg.Attachments) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at least one user message or attachment is required"))
	}

	// Verify user owns the project and get project details
	project, err := s.database.GetProjectWithUserCheck(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
	}

	// Resolve workflow - use user's default if not specified
	workflowName := s.resolveDefaultWorkflow(ctx, userID, req.Msg.Workflow)

	// Resolve execution mode from request
	// Create chat ID - root workflow ID equals chat ID for simple identification
	// workflow_name is stored separately in the chat record for querying/debugging
	chatID := uuid.New().String()
	workflowID := chatID // Root workflow ID = chat ID
	now := time.Now().UTC()

	// Prepare title
	title := ""
	if req.Msg.Title != nil {
		title = *req.Msg.Title
	}

	// Create chat object (not yet persisted) - model/temperature/max_tokens are workflow input params, not stored on chat
	chat := &db.Chat{
		ID:              chatID,
		UserID:          userID,
		Title:           title,
		ProjectID:       req.Msg.ProjectId,
		WorkflowName:    &workflowName,
		State:           db.ChatStateIdle,
		WorkflowID:      &workflowID,
		SelectedPresets: req.Msg.SelectedPresets,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActive:      now,
	}

	// Validate workflow tree BEFORE creating chat to avoid runtime graph failures and orphaned chats.
	// Uses runtime-equivalent loader semantics: builtin:// and usable workflow drafts only.
	if err := s.validateCreateChatWorkflowTree(ctx, userID, workflowName, project.ID); err != nil {
		return nil, err
	}

	if err := validateWorkflowParamStructure(req.Msg.WorkflowParams); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Every chat must name a resolvable worktree, or the UI's worktree-grouped
	// chat list silently hides it. Callers that omit one (e.g. the CLI) bind to
	// the project's main worktree. Resolved after the request validators so a
	// malformed request still reports its own error rather than a project-state
	// precondition, but before getEffectiveWorkingPath below, which reads it.
	worktreeID, err := s.resolveChatWorktreeID(ctx, req.Msg.ProjectId, req.Msg.WorktreeId)
	if err != nil {
		return nil, err
	}
	chat.WorktreeID = worktreeID

	// DEBUG: Log raw proto tools value before any processing
	if toolsProto, ok := req.Msg.WorkflowParams["tools"]; ok {
		logging.Info("[CreateChat] Raw proto tools param",
			"chatID", chatID,
			"asInterface", toolsProto.AsInterface(),
			"protoString", toolsProto.String(),
		)
	} else {
		logging.Info("[CreateChat] No tools param in workflowParams", "chatID", chatID, "paramKeys", func() []string {
			keys := make([]string, 0, len(req.Msg.WorkflowParams))
			for k := range req.Msg.WorkflowParams {
				keys = append(keys, k)
			}
			return keys
		}())
	}

	// Build and validate workflow inputs BEFORE creating chat
	// Use worktree path if chat is in a worktree, otherwise project path
	workingPath := s.getEffectiveWorkingPath(ctx, chat)
	initialData := s.buildWorkflowInputs(ctx, userID, workingPath, project.ID, workflowName, req.Msg.SelectedPresets, req.Msg.WorkflowParams)

	// Validate resolved inputs (catches empty model after defaults resolution)
	if validationErrors := s.validateWorkflowInputs(ctx, workflowName, project.ID, initialData); len(validationErrors) > 0 {
		errMsgs := make([]string, len(validationErrors))
		for i, e := range validationErrors {
			errMsgs[i] = e.Error()
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow input validation failed: %s", strings.Join(errMsgs, "; ")))
	}

	// Build execution context for the workflow
	// This is the source of truth for thread, message, and execution state
	execContext := &v2.ExecutionContext{
		WorkflowID:   workflowID,
		ChatID:       chatID,
		WorkflowName: workflowName,
		Thread:       workflowID,
		ThreadMode:   model.ThreadModeNew,
	}

	// A brand-new chat is a first turn by construction, so no message count is
	// needed here. When the project directory holds no code the stack is still
	// open, and the model gets that observation plus the criteria for
	// proposing forge ahead of the user's first message. No-op when the
	// project already holds code or the daemon is unreachable.
	if guidance := s.greenfieldGuidanceForChat(ctx, userID, chat); guidance != nil {
		systemMessages = append([]*reliantv1.InputMessage{guidance}, systemMessages...)
	}

	// Root workflow + thread, created atomically with the chat below.
	rootWorkflow := &db.Workflow{
		ID:           workflowID,
		ChatID:       chatID,
		WorkflowName: workflowName,
		Thread:       workflowID, // Root workflow: thread = workflow ID
		Status:       db.Pending(),
		CreatedAt:    now,
	}

	// chat_created payload for the global websocket, computed from data we
	// already have (no DB round trip) so it can be emitted inside the same
	// transaction as the row it announces.
	chatCreatedData := map[string]interface{}{
		"chat_id":     chatID,
		"title":       chat.Title,
		"project_id":  chat.ProjectID,
		"worktree_id": chat.WorktreeID,
		"workflow":    chat.WorkflowName,
		"state":       string(chat.State),
		"created_at":  chat.CreatedAt.Format(time.RFC3339),
	}
	chatCreatedJSON, marshalErr := json.Marshal(chatCreatedData)
	if marshalErr != nil {
		logging.Error("Failed to marshal chat_created data", "error", marshalErr, "chatID", chatID)
	}

	// The chat row, its root workflow+thread, the initial messages, and the
	// chat_created announcement must not be observed apart: a client that
	// sees chat_created must be able to load a chat that already has a
	// thread. Group them in one transaction so any failure leaves nothing
	// behind (no orphan chat, no thread-less chat, no announcement for a
	// chat that doesn't exist).
	if err := s.database.RunTx(ctx, func(txCtx context.Context) error {
		if err := s.database.CreateChat(txCtx, chat); err != nil {
			return fmt.Errorf("failed to create chat: %w", err)
		}

		if _, _, _, err := s.threads.CreateWorkflowWithThread(txCtx, threads.CreateWorkflowWithThreadOpts{
			Workflow: rootWorkflow,
			ThreadID: workflowID,
			ChatID:   chatID,
		}); err != nil {
			return fmt.Errorf("failed to create workflow and thread: %w", err)
		}

		// Save messages BEFORE starting workflow for consistency.
		// System messages are saved first, then the user message.
		for _, sysMsg := range systemMessages {
			if _, err := s.database.SaveMessageToThread(txCtx, chatID, workflowID, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), sysMsg.Content, &workflowID, nil, displayStyleProtoToInt32Ptr(sysMsg.DisplayStyle)); err != nil {
				return fmt.Errorf("failed to save system message: %w", err)
			}
		}
		if hasUserContent || len(req.Msg.Attachments) > 0 {
			if _, err := s.database.SaveMessageToThread(txCtx, chatID, workflowID, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), userContent, &workflowID, req.Msg.Attachments, nil); err != nil {
				return fmt.Errorf("failed to save first message: %w", err)
			}
		}

		if chatCreatedJSON != nil {
			if err := s.database.CreateUserUpdate(txCtx, &db.UserUpdate{
				UserID:     userID,
				ProjectID:  &chat.ProjectID,
				WorktreeID: chat.WorktreeID,
				ChatID:     &chatID,
				UpdateType: db.UserUpdateChatCreated,
				EntityType: db.EntityTypeChat,
				EntityID:   chatID,
				Data:       chatCreatedJSON,
			}); err != nil {
				return fmt.Errorf("failed to create chat_created user update: %w", err)
			}
		}

		return nil
	}); err != nil {
		logging.Error("Failed to create chat", "error", err, "chatID", chatID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create chat"))
	}

	// Start workflow on shared task queue
	workflowOptions := client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                s.taskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_TERMINATE_EXISTING,
		WorkflowExecutionTimeout: workflow.WorkflowExecutionTimeout,
	}

	// initialData was already built and validated before chat creation

	// Inject session daemon if set on chat
	injectSessionDaemonID(initialData, chat)

	workflowInput := v2.WorkflowInput{
		ChatID:       chatID,
		WorkflowName: workflowName,
		Inputs:       initialData,
		ExecContext:  execContext,
	}

	workflowRun, err := s.tempClient.ExecuteWorkflow(ctx, workflowOptions, v2.DynamicWorkflow, workflowInput)
	if err != nil {
		logging.Error("Failed to start workflow", "error", err, "chatID", chatID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to start workflow"))
	}

	runID := workflowRun.GetRunID()
	s.runs.RecordRun(ctx, chatID, workflowID, runID)

	// Workflow record was already created above with CreateWorkflowWithThread (status=pending)
	// WorkflowStatus activity will update it to 'running' when the workflow starts

	// Start GenerateTitle workflow
	generateTitleOptions := client.StartWorkflowOptions{
		ID:                       fmt.Sprintf("generate-title-%s", chatID),
		TaskQueue:                s.taskQueue,
		WorkflowExecutionTimeout: workflow.WorkflowExecutionTimeout,
	}
	generateTitleInput := map[string]interface{}{
		"chat_id":       chatID,
		"first_message": userContent,
	}
	_, titleErr := s.tempClient.ExecuteWorkflow(ctx, generateTitleOptions, "GenerateTitleWorkflow", generateTitleInput)
	if titleErr != nil {
		logging.Error("Failed to start title generation workflow", "error", titleErr, "chatID", chatID)
		// Don't fail the request for title generation failure
	}

	// Fetch created chat
	createdChat, err := s.database.GetChat(ctx, chatID)
	if err != nil {
		logging.Error("Failed to fetch created chat", "error", err, "chatID", chatID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to fetch created chat"))
	}

	createChatProto := chatToProto(createdChat)
	response := &reliantv1.CreateChatResponse{
		Chat:       createChatProto,
		WorkflowId: workflowID,
		RunId:      runID,
	}
	return connect.NewResponse(response), nil
}

// ListChats lists all non-archived chats for a project
func (s *ChatService) ListChats(
	ctx context.Context,
	req *connect.Request[reliantv1.ListChatsRequest],
) (*connect.Response[reliantv1.ListChatsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	limit := int(100)
	if req.Msg.Limit != nil && *req.Msg.Limit > 0 {
		limit = int(*req.Msg.Limit)
	}

	filters := db.ChatFilters{
		UserID:          userID,
		ProjectID:       &req.Msg.ProjectId,
		Limit:           limit,
		ExcludeArchived: true,
	}

	chats, err := s.database.ListChats(ctx, filters)
	if err != nil {
		logging.Error("Failed to list chats", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list chats"))
	}

	// The background reconciler repairs stale DB status against Temporal.
	protoChats := make([]*reliantv1.Chat, len(chats))
	for i, c := range chats {
		protoChats[i] = chatToProto(c)
	}

	// Get latest user update sequence for stream sync —
	// the frontend uses this to connect the streaming RPC without redundant event replay.
	latestSeq, err := s.database.GetLatestUserUpdateSequence(ctx, userID)
	if err != nil {
		// Non-fatal: log and return 0 so the frontend falls back to full replay
		logging.Warn("Failed to get latest user update sequence", "error", err, "userID", userID)
	}

	return connect.NewResponse(&reliantv1.ListChatsResponse{
		Chats:                  protoChats,
		Total:                  int32(len(protoChats)),
		LastUserUpdateSequence: latestSeq,
	}), nil
}

// GetChat retrieves a specific chat by ID
func (s *ChatService) GetChat(
	ctx context.Context,
	req *connect.Request[reliantv1.GetChatRequest],
) (*connect.Response[reliantv1.GetChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// Verify user owns the chat
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// Check for ghost workflows (DB says running, but Temporal lost it).
	// GetChat is a READ operation — it does NOT reconcile workflow status.
	// Status transitions are handled by WorkflowStatus("completed") activity
	// callbacks and EnsureWorkflowRunning belt-and-suspenders in ActivityWrapper.
	needsRecovery := false
	if workflowID := chat.MainThreadID(); workflowID != "" {
		// Check if the root workflow needs ghost recovery
		wf, wfErr := s.database.GetWorkflow(ctx, workflowID)
		if wfErr == nil && wf != nil && wf.Status == db.Active() {
			// DB says running — verify Temporal agrees
			temporalState, err := s.runs.State(ctx, workflowID)
			if err != nil {
				logging.Warn("Failed to query Temporal for workflow status",
					"error", err, "chatID", req.Msg.ChatId, "workflowID", workflowID)
			} else if !temporalState.Exists {
				// Ghost workflow — Temporal lost it but DB says running
				needsRecovery = true
			}
			// If Temporal says completed but DB says running, DON'T reconcile here.
			// The WorkflowStatus("completed") activity callback is the authoritative
			// path for transitioning DB status. The EnsureWorkflowRunning belt-and-
			// suspenders in ActivityWrapper handles the reverse case.
		}
	}

	protoChat := chatToProto(chat)
	protoChat.NeedsRecovery = needsRecovery
	return connect.NewResponse(&reliantv1.GetChatResponse{
		Chat: protoChat,
	}), nil
}

// UpdateChat updates chat metadata
func (s *ChatService) UpdateChat(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateChatRequest],
) (*connect.Response[reliantv1.UpdateChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// Update fields
	// NOTE: model/temperature/max_tokens are workflow input params, not stored on chat
	if req.Msg.Title != nil {
		chat.Title = *req.Msg.Title
	}
	if req.Msg.WorktreeId != nil {
		// Same invariant as creation: a chat may be moved between the project's
		// worktrees, but never to a foreign one and never cleared to null.
		resolved, err := s.resolveChatWorktreeID(ctx, chat.ProjectID, req.Msg.WorktreeId)
		if err != nil {
			return nil, err
		}
		chat.WorktreeID = resolved
	}

	// Handle workflow name change - only allowed when root workflow is pending
	if req.Msg.WorkflowName != nil && *req.Msg.WorkflowName != "" {
		newWorkflowName := *req.Msg.WorkflowName

		// Validate workflow exists
		if _, err := s.loadWorkflowForValidation(ctx, newWorkflowName, chat.ProjectID); err != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workflow not found: %s", newWorkflowName))
		}

		// Check if root workflow is pending (chat hasn't started)
		if chat.WorkflowID == nil || *chat.WorkflowID == "" {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("cannot change workflow: chat has no workflow assigned"))
		}

		rootWorkflow, err := s.database.GetWorkflow(ctx, *chat.WorkflowID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal,
				fmt.Errorf("failed to get root workflow: %w", err))
		}

		if rootWorkflow.Status != db.Pending() {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("cannot change workflow after chat has started - use Branch instead"))
		}

		// Update workflow name on the workflow record
		if err := s.database.UpdateWorkflowName(ctx, *chat.WorkflowID, newWorkflowName); err != nil {
			logging.Error("Failed to update workflow name", "error", err, "workflowID", *chat.WorkflowID)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update workflow"))
		}

		// Update chat's workflow name
		chat.WorkflowName = &newWorkflowName
	}

	chat.UpdatedAt = time.Now().UTC()
	chat.LastActive = time.Now().UTC()

	if err := s.database.UpdateChat(ctx, chat); err != nil {
		logging.Error("Failed to update chat", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update chat"))
	}

	// Emit chat_title_changed if title was updated
	if req.Msg.Title != nil {
		titleData, _ := json.Marshal(map[string]string{
			"chat_id": chat.ID,
			"title":   chat.Title,
		})
		if err := s.database.CreateUserUpdate(ctx, &db.UserUpdate{
			UserID:     userID,
			ProjectID:  &chat.ProjectID,
			WorktreeID: chat.WorktreeID,
			ChatID:     &chat.ID,
			UpdateType: db.UserUpdateChatTitleChanged,
			EntityType: db.EntityTypeChat,
			EntityID:   chat.ID,
			Data:       titleData,
		}); err != nil {
			logging.Warn("Failed to emit chat_title_changed", "error", err)
		}
	}

	// Emit chat_config_changed to user_updates when workflow_name changes.
	// (Replaces the dropped user_updates_chat_config_update SQL trigger.)
	if req.Msg.WorkflowName != nil && *req.Msg.WorkflowName != "" {
		configData, _ := json.Marshal(map[string]interface{}{
			"chat_id":       chat.ID,
			"workflow_name": *chat.WorkflowName,
		})
		if err := s.database.CreateUserUpdate(ctx, &db.UserUpdate{
			UserID:     userID,
			ProjectID:  &chat.ProjectID,
			WorktreeID: chat.WorktreeID,
			ChatID:     &chat.ID,
			UpdateType: db.UserUpdateChatConfigChanged,
			EntityType: db.EntityTypeChat,
			EntityID:   chat.ID,
			Data:       configData,
		}); err != nil {
			logging.Warn("Failed to emit chat_config_changed", "error", err)
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
		if err := s.database.CreateChatUpdate(ctx, chat.ID, db.UpdateTypeChat, chat.ID, string(chatUpdateData)); err != nil {
			logging.Warn("Failed to emit chat_update for config change", "error", err)
		}
	}

	updateChatProto := chatToProto(chat)
	return connect.NewResponse(&reliantv1.UpdateChatResponse{
		Chat: updateChatProto,
	}), nil
}

// DeleteChat archives or permanently deletes a chat
func (s *ChatService) DeleteChat(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteChatRequest],
) (*connect.Response[reliantv1.DeleteChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	chat, err := s.getChatForUser(ctx, req.Msg.ChatId, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// If already archived, hard delete permanently
	if chat.State == db.ChatStateArchived {
		// Cancel workflow if still running (belt-and-suspenders for hard delete)
		if workflowID := chat.MainThreadID(); workflowID != "" {
			_ = s.tempClient.CancelWorkflow(ctx, workflowID, "")
		}

		if err := s.database.DeleteChat(ctx, req.Msg.ChatId); err != nil {
			logging.Error("Failed to permanently delete chat", "error", err, "chatID", req.Msg.ChatId)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to permanently delete chat"))
		}

		return connect.NewResponse(&reliantv1.DeleteChatResponse{
			Success:            true,
			Message:            "Chat permanently deleted",
			PermanentlyDeleted: true,
		}), nil
	}

	// Otherwise, archive the chat
	if err := s.database.UpdateChatState(ctx, req.Msg.ChatId, db.ChatStateArchived, "user_archived"); err != nil {
		logging.Error("Failed to archive chat", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to archive chat"))
	}

	// Cancel the workflow if running
	if workflowID := chat.MainThreadID(); workflowID != "" {
		runID := ""
		if chat.RunID != nil {
			runID = *chat.RunID
		}
		if err := s.tempClient.CancelWorkflow(ctx, workflowID, runID); err != nil {
			logging.Error("Failed to cancel workflow for archived chat", "error", err, "chatID", req.Msg.ChatId, "workflowID", workflowID)
		}
	}

	return connect.NewResponse(&reliantv1.DeleteChatResponse{
		Success:            true,
		Message:            "Chat archived successfully",
		PermanentlyDeleted: false,
	}), nil
}

// SearchChats searches chats by title and message content
func (s *ChatService) SearchChats(
	ctx context.Context,
	req *connect.Request[reliantv1.SearchChatsRequest],
) (*connect.Response[reliantv1.SearchChatsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	// Return empty results if no query
	if req.Msg.Query == "" {
		return connect.NewResponse(&reliantv1.SearchChatsResponse{
			Chats: []*reliantv1.Chat{},
			Total: 0,
		}), nil
	}

	limit := 100
	if req.Msg.Limit != nil && *req.Msg.Limit > 0 {
		limit = int(*req.Msg.Limit)
	}

	filters := db.ChatSearchFilters{
		UserID:      userID,
		ProjectID:   req.Msg.ProjectId,
		SearchQuery: req.Msg.Query,
		Limit:       limit,
	}

	chats, err := s.database.SearchChats(ctx, filters)
	if err != nil {
		logging.Error("Failed to search chats", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to search chats"))
	}

	protoChats := make([]*reliantv1.Chat, len(chats))
	for i, c := range chats {
		protoChats[i] = chatToProto(c)
	}

	return connect.NewResponse(&reliantv1.SearchChatsResponse{
		Chats: protoChats,
		Total: int32(len(protoChats)),
	}), nil
}

// ListArchivedChats lists all archived chats with worktree info
func (s *ChatService) ListArchivedChats(
	ctx context.Context,
	req *connect.Request[reliantv1.ListArchivedChatsRequest],
) (*connect.Response[reliantv1.ListArchivedChatsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	archivedChats, err := s.database.ListArchivedChats(ctx, userID)
	if err != nil {
		logging.Error("Failed to list archived chats", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list archived chats"))
	}

	protoChats := make([]*reliantv1.ArchivedChat, len(archivedChats))
	for i, ac := range archivedChats {
		acProto := chatToProto(&ac.Chat)
		protoChats[i] = &reliantv1.ArchivedChat{
			Chat: acProto,
		}
		if ac.WorktreeName != nil {
			protoChats[i].WorktreeName = ac.WorktreeName
		}
		if ac.WorktreeDeletedAt != nil {
			deletedAt := ac.WorktreeDeletedAt.Format(time.RFC3339)
			protoChats[i].WorktreeDeletedAt = &deletedAt
		}
	}

	return connect.NewResponse(&reliantv1.ListArchivedChatsResponse{
		Chats: protoChats,
		Total: int32(len(protoChats)),
	}), nil
}

// ListMessages lists messages for a chat with content blocks
// For branched chats, this resolves inherited messages via the context window chain.
func (s *ChatService) ListMessages(
	ctx context.Context,
	req *connect.Request[reliantv1.ListMessagesRequest],
) (*connect.Response[reliantv1.ListMessagesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	// Verify user owns the chat
	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// Parse pagination params
	recentLimit := 0
	if req.Msg.Recent != nil && *req.Msg.Recent > 0 {
		recentLimit = int(*req.Msg.Recent)
	}

	var beforeSeq int64
	if req.Msg.BeforeSeq != nil && *req.Msg.BeforeSeq > 0 {
		beforeSeq = *req.Msg.BeforeSeq
	}

	// Determine the main thread ID for this chat
	mainThreadID := req.Msg.ChatId
	if id := chat.MainThreadID(); id != "" {
		mainThreadID = id
	}

	threadsSvc := threads.NewService(s.database)

	var messages []*db.Message
	var totalCount int
	var oldestSeq int64
	var hasMore bool

	// The thread whose point of view these messages are read from; governs
	// tool-call status resolution (see MessageToProtoOptions.ViewingThreadID).
	viewingThreadID := mainThreadID

	switch {
	case req.Msg.ThreadId != nil && *req.Msg.ThreadId != "":
		// Single-thread read: return exactly this thread's visual thread. No
		// merge with siblings and no main-thread window — the caller asked for
		// one thread, so the answer is that thread or an error, never a
		// best-effort slice of something else.
		threadID := *req.Msg.ThreadId
		viewingThreadID = threadID

		thread, err := s.database.GetThread(ctx, threadID)
		if err != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("thread not found"))
		}
		// Ownership is transitive through the chat, which is already
		// authorized above. A thread from another chat is NotFound rather
		// than a silent fall back to the chat-wide list: answering a
		// different question than the one asked is how the spawn preview
		// came to render the wrong thing in the first place.
		if thread.ChatID != req.Msg.ChatId {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("thread not found"))
		}

		messages, hasMore, err = s.listThreadMessages(ctx, threadsSvc, threadID, beforeSeq, recentLimit)
		if err != nil {
			logging.Error("Failed to load thread messages", "error", err, "chatID", req.Msg.ChatId, "threadID", threadID)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load messages"))
		}
		// The display count, not the LLM one: listThreadMessages reads the
		// transcript (which crosses compaction boundaries), so a total that
		// stopped at the newest summary would describe a different list than
		// the one being returned.
		totalCount, err = threadsSvc.CountDisplayMessages(ctx, threadID)
		if err != nil {
			logging.Error("Failed to count thread messages", "error", err, "chatID", req.Msg.ChatId, "threadID", threadID)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load messages"))
		}
		if len(messages) > 0 {
			oldestSeq = messages[0].Seq
		}

	case recentLimit > 0:
		messages, totalCount, oldestSeq, hasMore, err = s.listMessagesBounded(
			ctx, threadsSvc, req.Msg.ChatId, mainThreadID, beforeSeq, recentLimit)
		if err != nil {
			logging.Error("Failed to load bounded messages", "error", err, "chatID", req.Msg.ChatId)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load messages"))
		}
	default:
		// No page size given: the caller wants the whole chat (e.g. the cold
		// initial load before any pagination has happened). Resolve every
		// thread's full history and hand it to paginateBySeq unbounded.
		messages, totalCount, err = s.listMessagesUnbounded(ctx, threadsSvc, req.Msg.ChatId, mainThreadID)
		if err != nil {
			logging.Error("Failed to resolve messages", "error", err, "chatID", req.Msg.ChatId)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load messages"))
		}

		var page messagePage
		messages, page = paginateBySeq(messages, beforeSeq, recentLimit, mainThreadID)
		oldestSeq, hasMore = page.OldestSeq, page.HasMore
	}

	// Batch-fetch content blocks for the paginated page of messages in one
	// query, then group by message id. This replaces three separate
	// ListContentBlocks (single-message) round trips per message with a
	// single ListContentBlocksForMessages call.
	messageIDs := make([]string, len(messages))
	for i, msg := range messages {
		messageIDs[i] = msg.ID
	}
	allBlocks, err := s.database.ListContentBlocksForMessages(ctx, messageIDs)
	if err != nil {
		logging.Error("Failed to batch list content blocks", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load messages"))
	}
	blocksByMessageID := make(map[string][]*db.MessageContentBlock, len(messages))
	for _, block := range allBlocks {
		blocksByMessageID[block.MessageID] = append(blocksByMessageID[block.MessageID], block)
	}

	// Collect all attachment IDs from image and file_reference blocks
	attachmentIDSet := make(map[string]bool)
	for _, msg := range messages {
		if msg.Role == reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM {
			continue
		}
		for _, block := range blocksByMessageID[msg.ID] {
			if (block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE || block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE) && block.Content != nil {
				attachmentIDSet[*block.Content] = true
			}
		}
	}

	// Fetch all attachments in bulk
	attachmentIDs := make([]string, 0, len(attachmentIDSet))
	for id := range attachmentIDSet {
		attachmentIDs = append(attachmentIDs, id)
	}
	attachmentMap := make(map[string]*db.Attachment)
	if len(attachmentIDs) > 0 {
		attachmentsData, err := s.database.GetAttachmentsByIDs(ctx, attachmentIDs)
		if err == nil {
			for _, att := range attachmentsData {
				attachmentMap[att.ID] = att
			}
		}
	}

	protoMessages := assembleMessagesForDisplay(
		ctx, s.database, messages, messageIDs, blocksByMessageID, attachmentMap, viewingThreadID, 0)

	return connect.NewResponse(&reliantv1.ListMessagesResponse{
		Messages:  protoMessages,
		Total:     int32(totalCount),
		Count:     int32(len(protoMessages)),
		HasMore:   hasMore,
		OldestSeq: oldestSeq,
	}), nil
}

// UpdateChatState changes chat state (idle/archived)
func (s *ChatService) UpdateChatState(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateChatStateRequest],
) (*connect.Response[reliantv1.UpdateChatStateResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	// Validate state value
	newState := db.ChatState(req.Msg.State)
	switch newState {
	case db.ChatStateIdle, db.ChatStateArchived:
		// Valid user-initiated transitions
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid state, must be 'idle' or 'archived'"))
	}

	// Get chat to verify ownership
	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	previousState := chat.State

	// Determine reason for state change
	var reason string
	switch newState {
	case db.ChatStateIdle:
		reason = "user_dismissed"
	case db.ChatStateArchived:
		reason = "user_archived"
	}

	// Update the state
	if err := s.database.UpdateChatState(ctx, req.Msg.ChatId, newState, reason); err != nil {
		logging.Error("Failed to update chat state", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update chat state"))
	}

	// If unarchiving a chat, also unarchive the associated workspace
	if newState == db.ChatStateIdle && previousState == db.ChatStateArchived && chat.WorktreeID != nil {
		worktree, err := s.database.GetWorktree(ctx, *chat.WorktreeID)
		if err == nil && worktree.DeletedAt != nil {
			if err := s.database.UnarchiveWorktree(ctx, *chat.WorktreeID); err != nil {
				logging.Error("Failed to unarchive workspace when unarchiving chat",
					"error", err,
					"chatID", req.Msg.ChatId,
					"worktreeID", *chat.WorktreeID)
			}
		}
	}

	// If archiving, also cancel any running workflow
	if workflowID := chat.MainThreadID(); newState == db.ChatStateArchived && workflowID != "" {
		runID := ""
		if chat.RunID != nil {
			runID = *chat.RunID
		}
		if err := s.tempClient.CancelWorkflow(ctx, workflowID, runID); err != nil {
			logging.Error("Failed to cancel workflow for archived chat",
				"error", err,
				"chatID", req.Msg.ChatId,
				"workflowID", workflowID)
		}
	}

	return connect.NewResponse(&reliantv1.UpdateChatStateResponse{
		State:         newState,
		PreviousState: previousState,
		Message:       fmt.Sprintf("Chat state updated to %v", newState),
	}), nil
}

// TerminateChat forcefully terminates the running workflow for a chat.
func (s *ChatService) TerminateChat(
	ctx context.Context,
	req *connect.Request[reliantv1.TerminateChatRequest],
) (*connect.Response[reliantv1.TerminateChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	if err := s.runs.Terminate(ctx, req.Msg.ChatId); err != nil {
		switch {
		case errors.Is(err, runs.ErrNoWorkflow):
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		case errors.Is(err, runs.ErrChatNotFound):
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
		default:
			logging.Error("Failed to terminate workflow", "error", err, "chatID", req.Msg.ChatId)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to terminate workflow"))
		}
	}

	return connect.NewResponse(&reliantv1.TerminateChatResponse{
		Success: true,
		Message: "Chat terminated successfully",
	}), nil
}

// PauseChat pauses the running workflow by sending a pause signal and cancelling in-flight activities.
// The workflow stays alive in Temporal (blocked at the next step boundary) and is resumable.
// In-flight activities are cancelled immediately via the shared cancellable activity context.
func (s *ChatService) PauseChat(
	ctx context.Context,
	req *connect.Request[reliantv1.PauseChatRequest],
) (*connect.Response[reliantv1.PauseChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	// Get chat to verify ownership
	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	if chat.WorkflowID == nil || *chat.WorkflowID == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("chat has no workflow"))
	}

	// Kill the tools BEFORE freeing the workflow to re-dispatch. The pause
	// signal below cancels the ExecuteTools activity at its next step
	// boundary, which frees the workflow to move on -- if that happened
	// first, the successor step could start while a tool from the current
	// step is still provably running on the daemon, and tools are not
	// idempotent. Push the same immediate daemon cancel InterruptThread uses,
	// scoped to every thread of the chat instead of one, and do it first.
	threadsSvc := s.threads
	if threadsSvc == nil {
		threadsSvc = threads.NewService(s.database,
			threads.WithTemporalSignaler(s.tempClient),
			threads.WithToolCanceler(s.daemonRouter),
		)
	}
	if _, err := threadsSvc.CancelChatToolCalls(ctx, threads.CancelChatToolCallsOpts{
		UserID: userID,
		ChatID: req.Msg.ChatId,
	}); err != nil {
		logging.Warn("Failed to push tool cancel while pausing; the workflow-level pause still applies",
			"error", err, "chatID", req.Msg.ChatId)
	}

	// A run that had already finished is not an error — the service reconciles
	// the stale row and reports success, because the user's intent is met.
	// A failed cancel push above must not strand the workflow un-paused, so
	// this always runs regardless of that outcome.
	if err := s.runs.Pause(ctx, req.Msg.ChatId); err != nil {
		logging.Error("Failed to pause workflow", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to pause workflow"))
	}

	return connect.NewResponse(&reliantv1.PauseChatResponse{
		Success: true,
		Message: "Chat paused successfully",
	}), nil
}

// ResumeChat resumes a paused workflow by restarting its dedicated worker
// The workflow continues exactly where it left off - all local state is preserved.
func (s *ChatService) ResumeChat(
	ctx context.Context,
	req *connect.Request[reliantv1.ResumeChatRequest],
) (*connect.Response[reliantv1.ResumeChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	// Get chat to verify ownership
	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	if chat.WorkflowID == nil || *chat.WorkflowID == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("chat has no workflow"))
	}

	outcome, err := s.runs.Resume(ctx, req.Msg.ChatId)
	if err != nil {
		logging.Error("Failed to resume workflow", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resume workflow"))
	}

	switch outcome.Kind {
	case runs.OutcomeUnresumable:
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("this conversation experienced a workflow error and cannot be resumed - use the branch feature to start a new conversation from any previous message"))

	case runs.OutcomeNeedsRecovery, runs.OutcomeNeedsRestart:
		// The frontend prompts the user to confirm starting a new conversation.
		return connect.NewResponse(&reliantv1.ResumeChatResponse{
			Success:       false,
			Message:       "The workflow session was interrupted and cannot be resumed. Your message history is preserved. Would you like to start a new conversation?",
			WorkflowId:    outcome.WorkflowID,
			NeedsRecovery: true,
			RecoveryType:  reliantv1.RecoveryType_RECOVERY_TYPE_WORKFLOW_LOST,
		}), nil
	}

	return connect.NewResponse(&reliantv1.ResumeChatResponse{
		Success:      true,
		Message:      "Chat resumed successfully",
		WorkflowId:   outcome.WorkflowID,
		RunId:        outcome.RunID,
		RecoveryType: reliantv1.RecoveryType_RECOVERY_TYPE_RESUMED,
	}), nil
}

// REMOVED: UpdateChatAgent - agent is now a workflow param, use UpdateWorkflowParams instead

// ============================================
// Phase 4: Extended Chat Operations
// ============================================

// DismissChat clears needs_attention state when user views a chat
func (s *ChatService) DismissChat(
	ctx context.Context,
	req *connect.Request[reliantv1.DismissChatRequest],
) (*connect.Response[reliantv1.DismissChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	// Get chat to verify ownership
	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// Only dismiss if currently unread
	if !chat.Unread {
		return connect.NewResponse(&reliantv1.DismissChatResponse{
			State:   chat.State,
			Changed: false,
			Message: "Chat does not need dismissal",
		}), nil
	}

	// Clear unread flag
	if err := s.database.UpdateChatUnread(ctx, req.Msg.ChatId, false, "user_dismissed"); err != nil {
		logging.Error("Failed to dismiss chat", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to dismiss chat"))
	}

	return connect.NewResponse(&reliantv1.DismissChatResponse{
		State:   chat.State,
		Changed: true,
		Message: "Chat dismissed",
	}), nil
}

// MarkUnreadChat marks a chat as needing attention (mark as unread)
func (s *ChatService) MarkUnreadChat(
	ctx context.Context,
	req *connect.Request[reliantv1.MarkUnreadChatRequest],
) (*connect.Response[reliantv1.MarkUnreadChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	// Get chat to verify ownership
	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// Don't allow marking archived chats as unread
	if chat.State == db.ChatStateArchived {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot mark archived chat as unread"))
	}

	// If already unread, no change needed
	if chat.Unread {
		return connect.NewResponse(&reliantv1.MarkUnreadChatResponse{
			State:   chat.State,
			Changed: false,
			Message: "Chat already marked as unread",
		}), nil
	}

	// Set unread flag
	if err := s.database.UpdateChatUnread(ctx, req.Msg.ChatId, true, "user_marked_unread"); err != nil {
		logging.Error("Failed to mark chat as unread", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to mark chat as unread"))
	}

	return connect.NewResponse(&reliantv1.MarkUnreadChatResponse{
		State:   chat.State,
		Changed: true,
		Message: "Chat marked as unread",
	}), nil
}

// CompactChat triggers manual context compaction for a chat
func (s *ChatService) CompactChat(
	ctx context.Context,
	req *connect.Request[reliantv1.CompactChatRequest],
) (*connect.Response[reliantv1.CompactChatResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}
	if req.Msg.ThreadId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("thread_id is required"))
	}

	// Get chat to verify ownership
	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// Check if there are any messages to compact on the specified thread
	threadToCompact := req.Msg.ThreadId
	messages, err := s.database.ListMessages(ctx, req.Msg.ChatId, db.MessageListOptions{
		Thread: &threadToCompact,
	})
	if err != nil {
		logging.Error("Failed to list messages", "error", err, "chatID", req.Msg.ChatId, "threadID", threadToCompact)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check messages"))
	}

	if len(messages) == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot compact: thread has no messages"))
	}

	// Check if a workflow is already running - use Temporal as source of truth
	if workflowID := chat.MainThreadID(); workflowID != "" {
		temporalState, err := s.runs.State(ctx, workflowID)
		if err != nil {
			logging.Error("Failed to check workflow status for compaction",
				"error", err,
				"chatID", req.Msg.ChatId,
				"workflowID", workflowID,
			)
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("failed to check workflow status: %w", err))
		}
		if temporalState.Exists && temporalState.IsRunning {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot compact while a workflow is running"))
		}
	}

	// Start the compact workflow with a new UUID
	workflowID := workflow.NewWorkflowID()

	workflowOptions := client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                s.taskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_TERMINATE_EXISTING,
		WorkflowExecutionTimeout: workflow.WorkflowExecutionTimeout,
	}

	// Compact workflow uses the specified thread
	workflowInput := v2.WorkflowInput{
		ChatID:       req.Msg.ChatId,
		WorkflowName: "builtin://compact",
		Inputs: map[string]interface{}{
			"thread": threadToCompact,
		},
		ExecContext: &v2.ExecutionContext{
			WorkflowID:   workflowID,
			ChatID:       req.Msg.ChatId,
			WorkflowName: "builtin://compact",
			Thread:       threadToCompact,
			ThreadMode:   model.ThreadModeInherit,
		},
	}

	workflowRun, err := s.tempClient.ExecuteWorkflow(ctx, workflowOptions, v2.DynamicWorkflow, workflowInput)
	if err != nil {
		logging.Error("Failed to start compact workflow", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to start compaction"))
	}

	runID := workflowRun.GetRunID()
	s.runs.RecordRun(ctx, req.Msg.ChatId, workflowID, runID)

	return connect.NewResponse(&reliantv1.CompactChatResponse{
		ChatId:     req.Msg.ChatId,
		WorkflowId: workflowID,
		RunId:      runID,
		Status:     "compacting",
		Message:    "Context compaction started",
	}), nil
}

// GetChatUpdates fetches chat updates since a sequence number
func (s *ChatService) GetChatUpdates(
	ctx context.Context,
	req *connect.Request[reliantv1.GetChatUpdatesRequest],
) (*connect.Response[reliantv1.GetChatUpdatesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	// Get chat with ownership check (defense-in-depth via single query)
	_, err := s.getChatForUser(ctx, req.Msg.ChatId, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// Get updates since sequence
	updates, err := s.database.GetUpdatesSince(ctx, req.Msg.ChatId, req.Msg.SinceSeq, 0)
	if err != nil {
		logging.Error("Failed to fetch updates", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to fetch updates"))
	}

	// Get latest sequence for client
	latestSeq, err := s.database.GetLatestUpdateSequence(ctx, req.Msg.ChatId)
	if err != nil {
		logging.Error("Failed to get latest sequence", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get latest sequence"))
	}

	// Convert to proto messages
	protoUpdates := make([]*reliantv1.ChatUpdate, len(updates))
	for i, update := range updates {
		protoUpdates[i] = &reliantv1.ChatUpdate{
			SequenceNumber: update.SequenceNumber,
			UpdateType:     update.UpdateType.String(),
			EntityId:       update.EntityID,
			Data:           string(update.Data),
			CreatedAt:      update.CreatedAt.Format(time.RFC3339),
		}
	}

	return connect.NewResponse(&reliantv1.GetChatUpdatesResponse{
		Updates:        protoUpdates,
		Total:          int32(len(updates)),
		LatestSequence: latestSeq,
	}), nil
}

// SetChatDaemon sets the active daemon for a chat session.
func (s *ChatService) SetChatDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.SetChatDaemonRequest],
) (*connect.Response[reliantv1.SetChatDaemonResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	// Get chat and verify ownership
	chat, err := s.getChatForUser(ctx, req.Msg.ChatId, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// Set or clear the active daemon
	var daemonID *string
	if req.Msg.DaemonId != "" {
		daemonID = &req.Msg.DaemonId
	}

	if err := s.database.UpdateChatActiveDaemon(ctx, chat.ID, daemonID); err != nil {
		logging.Error("Failed to set chat daemon", "error", err, "chatID", chat.ID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to set chat daemon"))
	}

	// Re-fetch chat with updated daemon
	updatedChat, err := s.database.GetChat(ctx, chat.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to reload chat"))
	}

	return connect.NewResponse(&reliantv1.SetChatDaemonResponse{
		Chat: chatToProto(updatedChat),
	}), nil
}

// ListChatPlans lists plans associated with a chat
func (s *ChatService) ListChatPlans(
	ctx context.Context,
	req *connect.Request[reliantv1.ListChatPlansRequest],
) (*connect.Response[reliantv1.ListChatPlansResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	// Get chat to verify ownership
	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// List plans for the chat (across all threads)
	plans, err := s.database.ListPlansByChatID(ctx, req.Msg.ChatId)
	if err != nil {
		logging.Error("Failed to list plans", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list plans"))
	}

	// Convert to proto messages
	protoPlans := make([]*reliantv1.ChatPlan, len(plans))
	for i, p := range plans {
		protoPlan := &reliantv1.ChatPlan{
			Id:        p.ID,
			ChatId:    req.Msg.ChatId,
			Title:     p.Title,
			Status:    planStatusFromInt32(p.Status),
			CreatedAt: p.CreatedAt.Format(time.RFC3339),
			UpdatedAt: p.UpdatedAt.Format(time.RFC3339),
		}
		if p.Description != nil {
			protoPlan.Description = p.Description
		}
		if p.CompletedAt != nil {
			completedAt := p.CompletedAt.Format(time.RFC3339)
			protoPlan.CompletedAt = &completedAt
		}
		protoPlans[i] = protoPlan
	}

	return connect.NewResponse(&reliantv1.ListChatPlansResponse{
		Plans: protoPlans,
		Total: int32(len(plans)),
	}), nil
}

// listMessagesUnbounded resolves EVERY message this chat has -- the main
// thread's full CW-chain-resolved history plus every child thread's full
// history -- for the legacy "recent not specified" path. This is the
// pre-existing (unbounded) ListMessages behavior, extracted unchanged so
// paginateBySeq keeps operating over a truly complete slice for that one
// caller shape.
func (s *ChatService) listMessagesUnbounded(
	ctx context.Context, threadsSvc *threads.Service, chatID, mainThreadID string,
) ([]*db.Message, int, error) {
	mainThreadMessages, err := threadsSvc.LoadDisplayMessages(ctx, mainThreadID)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to resolve main thread messages: %w", err)
	}
	for _, msg := range mainThreadMessages {
		if msg.ThreadID != mainThreadID {
			msg.ThreadID = mainThreadID
		}
	}

	allThreads, err := s.database.ListThreadsByConversation(ctx, chatID)
	if err != nil {
		logging.Warn("Failed to list threads for conversation", "error", err, "chatID", chatID)
		allThreads = []*db.Thread{}
	}

	var childThreadMessages []*db.Message
	for _, thread := range allThreads {
		if thread.ID == mainThreadID {
			continue
		}
		childMsgs, err := threadsSvc.LoadDisplayMessages(ctx, thread.ID)
		if err != nil {
			logging.Warn("Failed to load child thread messages", "error", err, "threadID", thread.ID)
			continue
		}
		childThreadMessages = append(childThreadMessages, childMsgs...)
	}

	messageMap := make(map[string]*db.Message, len(mainThreadMessages)+len(childThreadMessages))
	for _, msg := range mainThreadMessages {
		messageMap[msg.ID] = msg
	}
	for _, msg := range childThreadMessages {
		if _, exists := messageMap[msg.ID]; !exists {
			messageMap[msg.ID] = msg
		}
	}
	messages := make([]*db.Message, 0, len(messageMap))
	for _, msg := range messageMap {
		messages = append(messages, msg)
	}

	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Seq < messages[j].Seq
	})

	return messages, len(messages), nil
}

// listThreadMessages reads exactly one thread's visual thread — the read
// behind ListMessagesRequest.thread_id.
//
// It is the whole implementation of a single-thread read, and it is this
// short on purpose: one thread's history is a thing the threads service
// already knows how to resolve (fork points, compaction chains and visual-
// thread normalization included). The chat-wide paths above are long because
// they merge N threads and then have to decide what to keep; there is nothing
// to merge here, so there is nothing to decide.
//
// recentLimit <= 0 means "the whole thread", which is what a spawn preview
// wants: the child is bounded by its own length, not by a page size borrowed
// from the main transcript.
func (s *ChatService) listThreadMessages(
	ctx context.Context, threadsSvc *threads.Service, threadID string, beforeSeq int64, recentLimit int,
) ([]*db.Message, bool, error) {
	if recentLimit > 0 {
		return threadsSvc.LoadRecentMessagesBefore(ctx, threadID, beforeSeq, recentLimit)
	}

	messages, err := threadsSvc.LoadDisplayMessages(ctx, threadID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to load thread messages: %w", err)
	}
	if beforeSeq > 0 {
		filtered := messages[:0]
		for _, msg := range messages {
			if msg.Seq < beforeSeq {
				filtered = append(filtered, msg)
			}
		}
		messages = filtered
	}
	// The whole thread was read, so nothing precedes it.
	return messages, false, nil
}

// listMessagesBounded is the SQL-bounded counterpart to
// listMessagesUnbounded, for the normal (recentLimit > 0) path. It reads
// only the window a page actually needs instead of the chat's entire
// history:
//
//   - Main thread: LoadRecentMessagesBefore bounds the read in SQL for the
//     common (unforked) case, falling back to the full resolution only for a
//     forked/compacted main thread.
//   - Sibling threads: bounded to the seq span the main-thread window
//     resolved to (LoadMessagesInSeqRange), rather than each sibling's whole
//     history -- exactly mirroring what windowByMainThread would have kept
//     from an unbounded read, at a fraction of the read cost.
//   - Total: CountDisplayMessages(main) covers the main thread's own
//     CW-chain-resolved count -- the DISPLAY resolution, matching the
//     transcript this page is a window into; the LLM count stops at a
//     compaction summary and would undercount a compacted chat by its whole
//     summarized history. CountMessagesInChat(chat) - CountMessagesInThread(main)
//     covers every sibling thread's messages (siblings don't inherit from
//     elsewhere, so their own chat_id-scoped rows are their whole count).
//   - HasMore: taken directly from the main thread's own hasMoreOlder. This
//     is sufficient because seq is a chat-global, densely-allocated order
//     (GetNextSeqByChat walks the full CW chain when allocating) and every
//     message a branch chat displays -- inherited or local -- carries a seq
//     from that same allocator, so no sibling thread's oldest message can
//     precede the resolved main thread's oldest message.
func (s *ChatService) listMessagesBounded(
	ctx context.Context, threadsSvc *threads.Service, chatID, mainThreadID string, beforeSeq int64, recentLimit int,
) ([]*db.Message, int, int64, bool, error) {
	mainThreadMessages, mainHasMoreOlder, err := threadsSvc.LoadRecentMessagesBefore(ctx, mainThreadID, beforeSeq, recentLimit)
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("failed to load recent main thread messages: %w", err)
	}
	for _, msg := range mainThreadMessages {
		if msg.ThreadID != mainThreadID {
			msg.ThreadID = mainThreadID
		}
	}

	// The seq span this page's main-thread window actually covers. Sibling
	// threads are bounded to this range rather than their whole history --
	// same effect as windowByMainThread's "every sibling message inside that
	// seq range". The upper bound mirrors ListRecentChatWindow's own
	// snapshot window (messages.sql), which is intentionally open above: a
	// spawn thread can out-write and out-live the main thread, finishing
	// after the main thread's newest message in this page, and the
	// tool-call preview those spawn messages render from must still appear.
	// The cursor itself is still the right upper bound on a page OTHER than
	// the newest -- everything at or after beforeSeq belongs to a page
	// already served.
	var fromSeq int64
	var toSeq *int64
	if len(mainThreadMessages) > 0 {
		fromSeq = mainThreadMessages[0].Seq
	} else if beforeSeq > 0 {
		fromSeq = beforeSeq
	}
	if beforeSeq > 0 {
		toSeq = &beforeSeq
	}

	allThreads, err := s.database.ListThreadsByConversation(ctx, chatID)
	if err != nil {
		logging.Warn("Failed to list threads for conversation", "error", err, "chatID", chatID)
		allThreads = []*db.Thread{}
	}

	messageMap := make(map[string]*db.Message, len(mainThreadMessages))
	for _, msg := range mainThreadMessages {
		messageMap[msg.ID] = msg
	}
	for _, thread := range allThreads {
		if thread.ID == mainThreadID {
			continue
		}
		childMsgs, err := threadsSvc.LoadMessagesInSeqRange(ctx, thread.ID, fromSeq, toSeq)
		if err != nil {
			logging.Warn("Failed to load child thread messages in range", "error", err, "threadID", thread.ID)
			continue
		}
		for _, msg := range childMsgs {
			if _, exists := messageMap[msg.ID]; !exists {
				messageMap[msg.ID] = msg
			}
		}
	}

	messages := make([]*db.Message, 0, len(messageMap))
	for _, msg := range messageMap {
		messages = append(messages, msg)
	}
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Seq < messages[j].Seq
	})

	// The window above is already the correctly-sized page (the same shape
	// windowByMainThread would produce), but re-apply it for safety: a
	// sibling thread with messages exactly AT the boundary could otherwise
	// push the reported page past recentLimit main-thread messages.
	messages = windowByMainThread(messages, mainThreadID, recentLimit)

	mainCount, err := threadsSvc.CountDisplayMessages(ctx, mainThreadID)
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("failed to count main thread messages: %w", err)
	}
	chatCount, err := s.database.CountMessagesInChat(ctx, chatID)
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("failed to count chat messages: %w", err)
	}
	mainOwnCount, err := s.database.CountMessagesInThread(ctx, mainThreadID)
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("failed to count main thread's own messages: %w", err)
	}
	totalCount := mainCount + chatCount - mainOwnCount

	var oldestSeq int64
	if len(messages) > 0 {
		oldestSeq = messages[0].Seq
	}

	return messages, totalCount, oldestSeq, mainHasMoreOlder, nil
}

// messagePage carries the cursor facts a paginated message read has to report
// back to the client.
type messagePage struct {
	// OldestSeq is the lowest seq in the returned page, i.e. the cursor the
	// client passes as before_seq to ask for the page before this one.
	OldestSeq int64
	// HasMore reports whether any message older than this page exists.
	HasMore bool
}

// windowByMainThread returns the tail of messages containing the newest `limit`
// main-thread messages, plus every sibling-thread message inside that range.
//
// Counting the window across every thread makes a page of a spawn-heavy chat
// almost entirely spawn messages, which render collapsed inside their tool
// call rather than in the transcript — so a full page loads and the visible
// conversation barely moves. Below one cursor in a real chat there were 5,675
// messages chat-wide but only 781 on the main thread, so a 200-message page
// advanced the transcript by ~27 rows. Counting the MAIN thread and carrying
// siblings along keeps both the transcript's N rows and the spawn messages
// the tool-call previews render from.
//
// messages must be sorted by seq ascending. mainThreadID == "" falls back to
// a plain newest-N cut. If limit <= 0 or there are already at most `limit`
// messages, messages is returned unchanged.
func windowByMainThread(messages []*db.Message, mainThreadID string, limit int) []*db.Message {
	if limit <= 0 || len(messages) <= limit {
		return messages
	}
	if mainThreadID == "" {
		return messages[len(messages)-limit:]
	}
	// Walk back until we have `limit` main-thread messages, then cut there —
	// sibling messages above that point come along for free.
	mainSeen, cutoff := 0, -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].ThreadID == mainThreadID {
			mainSeen++
			if mainSeen == limit {
				cutoff = i
				break
			}
		}
	}
	if cutoff > 0 {
		return messages[cutoff:]
	}
	// cutoff < 0 means the remaining history holds fewer than `limit`
	// main-thread messages; return all of it.
	return messages
}

// paginateBySeq narrows a seq-ascending message slice to one page and reports
// the cursor facts for it.
//
// hasMore is derived by comparing the page's oldest seq against the oldest seq
// in the whole chat, which is the only formulation that answers the question the
// client is actually asking ("is there anything before what I'm holding?").
//
// The previous implementation was `len(messages) < totalCount || beforeSeq > 0`
// with totalCount taken AFTER the before_seq filter. Both halves were wrong.
// totalCount measured the already-filtered set, so it shrank in step with the
// slice it was compared against; and `beforeSeq > 0` forced hasMore true for
// every paginated request, so scrollback could never terminate. Together they
// made the client unable to distinguish "more history exists" from "you have
// reached the beginning" — the user-visible symptom being a chat that refuses
// to load older messages no matter how far you scroll.
//
// The page size is counted in MAIN-THREAD messages via windowByMainThread; see
// its doc comment for why.
//
// messages must already be sorted by seq ascending.
func paginateBySeq(messages []*db.Message, beforeSeq int64, recentLimit int, mainThreadID string) ([]*db.Message, messagePage) {
	if len(messages) == 0 {
		return messages, messagePage{}
	}

	// The oldest seq the chat has, captured before either filter runs.
	chatOldestSeq := messages[0].Seq

	if beforeSeq > 0 {
		filtered := make([]*db.Message, 0, len(messages))
		for _, msg := range messages {
			if msg.Seq < beforeSeq {
				filtered = append(filtered, msg)
			}
		}
		messages = filtered
	}

	// Messages are oldest-first, so the most recent N are at the end.
	messages = windowByMainThread(messages, mainThreadID, recentLimit)

	if len(messages) == 0 {
		// Paginated past the beginning: nothing to return and nothing older.
		return messages, messagePage{}
	}

	oldest := messages[0].Seq
	return messages, messagePage{
		OldestSeq: oldest,
		HasMore:   oldest > chatOldestSeq,
	}
}
