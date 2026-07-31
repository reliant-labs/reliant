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
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
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
		WorktreeID:      req.Msg.WorktreeId,
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

	// All validations passed - now create chat in transaction
	if err := s.database.RunTx(ctx, func(txCtx context.Context) error {
		if err := s.database.CreateChat(txCtx, chat); err != nil {
			return fmt.Errorf("failed to create chat: %w", err)
		}
		return nil
	}); err != nil {
		logging.Error("Failed to create chat", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create chat"))
	}

	// Associate chat with workflow draft for workflow builder chats editing EXISTING workflows.
	// For existing workflows: frontend passes builder_workflow_slug, backend looks up and associates.
	// For new workflows: frontend creates draft via CreateWorkflowDraft, then calls
	// AssociateChatWithWorkflowDraft after chat creation.
	var createdDraftID string
	if isWorkflowBuilderChat(req.Msg.SelectedPresets) {
		if builderSlug, ok := req.Msg.WorkflowParams["builder_workflow_slug"]; ok {
			if slugValue, isString := builderSlug.AsInterface().(string); isString && slugValue != "" {
				draft, err := s.database.GetWorkflowDraftBySlug(ctx, userID, slugValue)
				if err != nil {
					logging.Warn("Failed to look up workflow draft for builder", "error", err, "slug", slugValue)
				} else if draft != nil {
					_, err := s.database.AssociateChatWithDraft(ctx, draft.ID, chatID)
					if err != nil {
						logging.Warn("Failed to associate chat with workflow draft", "error", err, "draftID", draft.ID, "chatID", chatID)
					}
					createdDraftID = draft.ID
				}
			}
		}
	}

	// Emit chat_created user update for global websocket
	// This allows other browser windows to see the new chat
	chatCreatedData := map[string]interface{}{
		"chat_id":     chatID,
		"title":       chat.Title,
		"project_id":  chat.ProjectID,
		"worktree_id": chat.WorktreeID,
		"workflow":    chat.WorkflowName,
		"state":       string(chat.State),
		"created_at":  chat.CreatedAt.Format(time.RFC3339),
	}
	chatCreatedJSON, err := json.Marshal(chatCreatedData)
	if err != nil {
		logging.Error("Failed to marshal chat_created data", "error", err, "chatID", chatID)
	} else {
		chatCreatedUpdate := &db.UserUpdate{
			UserID:     userID,
			ProjectID:  &chat.ProjectID,
			WorktreeID: chat.WorktreeID,
			ChatID:     &chatID,
			UpdateType: db.UserUpdateChatCreated,
			EntityType: db.EntityTypeChat,
			EntityID:   chatID,
			Data:       chatCreatedJSON,
		}
		if err := s.database.CreateUserUpdate(ctx, chatCreatedUpdate); err != nil {
			// Log but don't fail - the chat was created successfully
			logging.Error("Failed to create chat_created user update", "error", err, "chatID", chatID)
		}
	}

	// Start workflow on shared task queue
	workflowOptions := client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                s.taskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_TERMINATE_EXISTING,
		WorkflowExecutionTimeout: workflow.WorkflowExecutionTimeout,
	}

	// initialData was already built and validated before chat creation

	// Build execution context for the workflow
	// This is the source of truth for thread, message, and execution state
	execContext := &v2.ExecutionContext{
		WorkflowID:   workflowID,
		ChatID:       chatID,
		WorkflowName: workflowName,
		Thread:       workflowID,
		ThreadMode:   model.ThreadModeNew,
	}

	// Create workflow and thread atomically BEFORE saving messages
	// This ensures proper FK relationships and allows WorkflowStatus to update status to running
	rootWorkflow := &db.Workflow{
		ID:           workflowID,
		ChatID:       chatID,
		WorkflowName: workflowName,
		Thread:       workflowID, // Root workflow: thread = workflow ID
		Status:       db.WorkflowStatusPending,
		CreatedAt:    now,
	}
	_, _, _, err = s.threads.CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
		Workflow: rootWorkflow,
		ThreadID: workflowID,
		ChatID:   chatID,
	})
	if err != nil {
		logging.Error("Failed to create workflow with thread for chat", "error", err, "chatID", chatID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create workflow and thread"))
	}

	// Save messages BEFORE starting workflow for consistency
	// System messages are saved first, then user message
	for _, sysMsg := range systemMessages {
		_, err := s.database.SaveMessageToThread(ctx, chatID, workflowID, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), sysMsg.Content, &workflowID, nil, displayStyleProtoToInt32Ptr(sysMsg.DisplayStyle))
		if err != nil {
			logging.Error("Failed to save system message", "error", err, "chatID", chatID)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save system message"))
		}
	}

	// Save first user message to thread (message is saved BEFORE workflow starts)
	if hasUserContent || len(req.Msg.Attachments) > 0 {
		_, err := s.database.SaveMessageToThread(ctx, chatID, workflowID, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), userContent, &workflowID, req.Msg.Attachments, nil)
		if err != nil {
			logging.Error("Failed to save first message", "error", err, "chatID", chatID)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save first message"))
		}
	}

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
	s.updateWorkflowRunIDs(ctx, chatID, workflowID, runID)

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
	if createdDraftID != "" {
		response.DraftId = &createdDraftID
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
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		workflowID := *chat.WorkflowID

		// Check if the root workflow needs ghost recovery
		wf, wfErr := s.database.GetWorkflow(ctx, workflowID)
		if wfErr == nil && wf != nil && wf.Status == db.WorkflowStatusRunning {
			// DB says running — verify Temporal agrees
			temporalState, err := s.getTemporalWorkflowState(ctx, workflowID)
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
		chat.WorktreeID = req.Msg.WorktreeId
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

		if rootWorkflow.Status != db.WorkflowStatusPending {
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
		if chat.WorkflowID != nil && *chat.WorkflowID != "" {
			_ = s.tempClient.CancelWorkflow(ctx, *chat.WorkflowID, "")
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
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		runID := ""
		if chat.RunID != nil {
			runID = *chat.RunID
		}
		if err := s.tempClient.CancelWorkflow(ctx, *chat.WorkflowID, runID); err != nil {
			logging.Error("Failed to cancel workflow for archived chat", "error", err, "chatID", req.Msg.ChatId, "workflowID", *chat.WorkflowID)
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

	var beforeOrdinal int64
	if req.Msg.BeforeOrdinal != nil && *req.Msg.BeforeOrdinal > 0 {
		beforeOrdinal = *req.Msg.BeforeOrdinal
	}

	// Determine the main thread ID for this chat
	mainThreadID := req.Msg.ChatId
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		mainThreadID = *chat.WorkflowID
	}

	// Use CW chain resolution to get messages for the main thread.
	// This resolves inherited messages from parent chats for branched conversations.
	threadsSvc := threads.NewService(s.database)
	mainThreadMessages, err := threadsSvc.LoadCurrentMessages(ctx, mainThreadID)
	if err != nil {
		logging.Error("Failed to resolve main thread messages", "error", err, "threadID", mainThreadID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load messages"))
	}

	// Normalize thread IDs: inherited messages should appear as part of the current chat's thread
	// This gives the UI a single unified thread view instead of showing messages from multiple threads
	for _, msg := range mainThreadMessages {
		if msg.ThreadID != mainThreadID {
			msg.ThreadID = mainThreadID // Normalize inherited message to current thread
		}
	}

	// Get all threads for this conversation to find child workflow threads (sub-agents)
	allThreads, err := s.database.ListThreadsByConversation(ctx, req.Msg.ChatId)
	if err != nil {
		logging.Warn("Failed to list threads for conversation", "error", err, "chatID", req.Msg.ChatId)
		allThreads = []*db.Thread{} // Continue with just main thread messages
	}

	// Collect messages from child threads (sub-agents, taskforces)
	// These are threads that belong to this chat but aren't the main thread
	var childThreadMessages []*db.Message
	for _, thread := range allThreads {
		if thread.ID == mainThreadID {
			continue // Skip main thread, already resolved via CW chain
		}
		// Get messages for this child thread directly (they don't inherit from elsewhere)
		childMsgs, err := threadsSvc.LoadCurrentMessages(ctx, thread.ID)
		if err != nil {
			logging.Warn("Failed to load child thread messages", "error", err, "threadID", thread.ID)
			continue
		}
		childThreadMessages = append(childThreadMessages, childMsgs...)
	}

	// Combine main thread messages (with inheritance) and child thread messages
	// Deduplicate by message ID: main thread messages take priority because they
	// have the correct primary thread assignment. Child threads inherit parent
	// messages via CW chain, and LoadCurrentMessages normalizes their ThreadID
	// to the child thread — without dedup, the same message appears once per
	// thread that inherits it.
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

	// Sort by ordinal to maintain conversation order
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Ordinal < messages[j].Ordinal
	})

	// Total count is the number of messages we loaded
	totalCount := len(messages)

	// Apply pagination: filter by before_ordinal first, then take recent N from end
	if beforeOrdinal > 0 {
		filtered := make([]*db.Message, 0, len(messages))
		for _, msg := range messages {
			if msg.Ordinal < beforeOrdinal {
				filtered = append(filtered, msg)
			}
		}
		messages = filtered
	}

	// Take only the most recent N messages (from the end)
	// Note: messages are ordered by ordinal ASC (oldest first), so we take from the end
	if recentLimit > 0 && len(messages) > recentLimit {
		messages = messages[len(messages)-recentLimit:]
	}

	// Calculate if there are more older messages available
	var oldestOrdinal int64
	if len(messages) > 0 {
		oldestOrdinal = messages[0].Ordinal
	}
	hasMore := len(messages) < totalCount || beforeOrdinal > 0

	// Collect all attachment IDs from image and file_reference blocks
	attachmentIDSet := make(map[string]bool)
	for _, msg := range messages {
		if msg.Role == reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM {
			continue
		}
		blocks, err := s.database.ListContentBlocks(ctx, msg.ID)
		if err != nil {
			continue
		}
		for _, block := range blocks {
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

	// Build a map of tool_results by tool_call_id for matching
	toolResultsByCallID := make(map[string]*reliantv1.MatchedToolResult)
	for _, msg := range messages {
		if msg.Role != reliantv1.MessageRole_MESSAGE_ROLE_TOOL {
			continue
		}
		blocks, err := s.database.ListContentBlocks(ctx, msg.ID)
		if err != nil {
			continue
		}
		for _, block := range blocks {
			if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT && block.ToolCallID != nil {
				result := &reliantv1.MatchedToolResult{
					ToolCallId: *block.ToolCallID,
					Type:       "tool_result",
				}
				if block.Content != nil {
					result.Content = block.Content
				}
				if block.IsError != nil {
					result.IsError = block.IsError
				}
				toolResultsByCallID[*block.ToolCallID] = result
			}
		}
	}

	// Build response messages
	var protoMessages []*reliantv1.Message
	for _, msg := range messages {
		// Get content blocks for this message
		blocks, err := s.database.ListContentBlocks(ctx, msg.ID)
		if err != nil {
			logging.Error("Failed to list content blocks", "error", err, "messageID", msg.ID)
			continue
		}

		// Skip hidden messages from UI (they are still sent to LLM)
		// This includes compaction summaries which are saved with display_style=hidden
		if msg.DisplayStyle != nil && *msg.DisplayStyle == reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN {
			continue
		}

		// Collect attachments for this message
		var msgAttachments []*db.Attachment
		for _, block := range blocks {
			if (block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE || block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE) && block.Content != nil {
				if att, found := attachmentMap[*block.Content]; found {
					msgAttachments = append(msgAttachments, att)
				}
			}
		}

		protoMessages = append(protoMessages, messageToProto(msg, blocks, msgAttachments, &MessageToProtoOptions{
			ToolResultsByCallID: toolResultsByCallID,
		}))
	}

	return connect.NewResponse(&reliantv1.ListMessagesResponse{
		Messages:      protoMessages,
		Total:         int32(totalCount),
		Count:         int32(len(protoMessages)),
		HasMore:       hasMore,
		OldestOrdinal: oldestOrdinal,
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
	if newState == db.ChatStateArchived && chat.WorkflowID != nil && *chat.WorkflowID != "" {
		runID := ""
		if chat.RunID != nil {
			runID = *chat.RunID
		}
		if err := s.tempClient.CancelWorkflow(ctx, *chat.WorkflowID, runID); err != nil {
			logging.Error("Failed to cancel workflow for archived chat",
				"error", err,
				"chatID", req.Msg.ChatId,
				"workflowID", *chat.WorkflowID)
		}
	}

	return connect.NewResponse(&reliantv1.UpdateChatStateResponse{
		State:         newState,
		PreviousState: previousState,
		Message:       fmt.Sprintf("Chat state updated to %v", newState),
	}), nil
}

// CancelChat cancels the running workflow for a chat
func (s *ChatService) CancelChat(
	ctx context.Context,
	req *connect.Request[reliantv1.CancelChatRequest],
) (*connect.Response[reliantv1.CancelChatResponse], error) {
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

	workflowID := *chat.WorkflowID

	// User cancel is the explicit "start fresh next time" marker: drop the
	// position checkpoint so the next message never resumes this run at
	// position, regardless of how the run's terminal status settles (the
	// workflow's own cancelled-path clears it too, but a wedged run never
	// reaches that path). Best-effort.
	if err := s.database.DeleteWorkflowCheckpoint(ctx, workflowID); err != nil {
		logging.Warn("CancelChat: failed to clear workflow checkpoint",
			"workflowID", workflowID, "error", err)
	}

	// Check if the Temporal workflow is still running before sending cancel signal.
	// If it's already completed/terminated, reconcile the DB status directly
	// so the frontend receives an activity=IDLE event.
	temporalState, temporalErr := s.getTemporalWorkflowState(ctx, workflowID)
	if temporalErr == nil && temporalState != nil {
		if !temporalState.Exists || !temporalState.IsRunning {
			// Workflow already finished in Temporal — reconcile DB status
			// so the activity event fires and unblocks the frontend
			logging.Info("CancelChat: workflow not running in Temporal, reconciling DB status",
				"workflowID", workflowID,
				"temporalExists", temporalState.Exists,
				"temporalStatus", temporalState.Status,
			)
			status := db.WorkflowStatusCancelled
			if temporalState.Exists {
				status = temporalState.Status
			}
			s.reconcileWorkflowStatus(ctx, workflowID, db.WorkflowStatusRunning, status)
			return connect.NewResponse(&reliantv1.CancelChatResponse{
				Success: true,
				Message: "Chat cancelled successfully",
			}), nil
		}
	}

	// The run is live in Temporal. A user cancel is authoritative and terminal:
	// unlike the reconciler — which leaves a question-parked run alone because a
	// human still owns it — an explicit cancel must kill the run even when it is
	// parked on a gate.

	// 1. Void any pending question so the run is no longer awaiting input and the
	//    chat's computed activity drops the AWAITING_INPUT state. Best-effort;
	//    the terminate + status swap below are what actually stop the run.
	if q, qErr := s.database.GetPendingQuestionByChatID(ctx, req.Msg.ChatId); qErr != nil {
		logging.Warn("CancelChat: failed to look up pending question",
			"chatID", req.Msg.ChatId, "error", qErr)
	} else if q != nil {
		cancelledResponse := `{"action":"cancelled","reason":"chat cancelled by user"}`
		if err := s.database.ResolveQuestion(ctx, q.ID, &cancelledResponse); err != nil {
			logging.Warn("CancelChat: failed to void pending question",
				"chatID", req.Msg.ChatId, "questionID", q.ID, "error", err)
		}
	}

	// 2. Forcefully terminate the workflow. A graceful CancelWorkflow only
	//    requests cancellation cooperatively; a run parked on a signal Await
	//    (e.g. an ask_question gate) never observes it and stays RUNNING forever
	//    (status=2). Terminate is the same forceful stop the reconciler uses for
	//    wedged runs. Best-effort — we still reconcile the DB status below.
	if err := s.tempClient.TerminateWorkflow(ctx, workflowID, "", "Chat cancelled by user"); err != nil {
		logging.Warn("CancelChat: failed to terminate workflow in Temporal (continuing to reconcile DB)",
			"error", err, "workflowID", workflowID)
	}

	// 3. Move the workflow row to CANCELLED. CAS (not a blind write) so a run
	//    that settled terminally underneath us is never clobbered — the swap that
	//    succeeds also emits the chat_activity_changed (IDLE) event that unblocks
	//    the frontend, the same transition the not-running branch performs. Cover
	//    a paused run too, since a user cancel overrides a pause. If neither swap
	//    lands the run was already terminal, and its own transition already fired.
	swapped, err := s.database.CompareAndSwapWorkflowStatus(ctx, workflowID, db.WorkflowStatusCancelled, db.WorkflowStatusRunning)
	if err != nil {
		logging.Error("CancelChat: failed to mark workflow cancelled", "error", err, "workflowID", workflowID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to cancel workflow"))
	}
	if !swapped {
		if _, err := s.database.CompareAndSwapWorkflowStatus(ctx, workflowID, db.WorkflowStatusCancelled, db.WorkflowStatusPaused); err != nil {
			logging.Warn("CancelChat: failed to cancel paused workflow", "error", err, "workflowID", workflowID)
		}
	}

	// 4. Cascade to the subtree. The terminate above is a HARD kill, so the
	//    workflow's own completion handler never runs — and that handler is
	//    what normally drives the status activity's cancelled arm, the only
	//    thing that cascades on a cancel. Step 3 covers the root row the
	//    handler would have written; this covers the spawn and thread rows it
	//    would have drained. Without it every descendant stays at running/paused
	//    forever (nothing revisits a row with a parent_id), so `workflow ps`
	//    reports a cancelled run as live and the chat stays "active" in
	//    chats_with_activity — which makes the IDLE event step 3 is meant to
	//    emit compute to RUNNING instead.
	//
	//    CANCELLED, not completed: these rows were terminated mid-flight and
	//    never finished their work. Recording them as completed made a cancelled
	//    run indistinguishable from a successful one in every later count.
	if err := s.database.CascadeTerminalStatusToDescendants(ctx, workflowID, db.WorkflowStatusCancelled); err != nil {
		logging.Error("CancelChat: failed to cascade cancellation to child workflows",
			"error", err, "workflowID", workflowID)
	}

	return connect.NewResponse(&reliantv1.CancelChatResponse{
		Success: true,
		Message: "Chat cancelled successfully",
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

	// Use PauseService for unified pause logic (stop worker, update DB, emit event).
	// PauseWorkflow returns nil even when the workflow already completed (it reconciles
	// the DB status in that case), so success here means the workflow is stopped.
	if err := s.pauseService.PauseWorkflow(ctx, *chat.WorkflowID, req.Msg.ChatId, "manual"); err != nil {
		logging.Error("Failed to pause workflow", "error", err, "workflowID", *chat.WorkflowID)
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

	workflowID := *chat.WorkflowID

	// Check if this is a stuck workflow (DB says failed but Temporal says running)
	// Stuck workflows cannot be resumed - user must branch to continue
	wf, err := s.database.GetWorkflow(ctx, workflowID)
	if err == nil && wf != nil && wf.Status == db.WorkflowStatusFailed {
		temporalState, temporalErr := s.getTemporalWorkflowState(ctx, workflowID)
		if temporalErr == nil && temporalState.Exists && temporalState.IsRunning {
			logging.Warn("Attempted to resume stuck workflow - blocking",
				"chatID", req.Msg.ChatId,
				"workflowID", workflowID,
			)
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("this conversation experienced a workflow error and cannot be resumed - use the branch feature to start a new conversation from any previous message"))
		}
	}

	// Check if workflow is expired — needs reset-based resume
	isExpired := wf != nil && wf.Status == db.WorkflowStatusExpired

	// Use PauseService for unified resume logic
	// PauseService.ResumeWorkflow handles both paused (signal) and expired (reset) workflows
	if err := s.pauseService.ResumeWorkflow(ctx, workflowID, req.Msg.ChatId); err != nil {
		// Check if the workflow was lost and needs recovery
		if errors.Is(err, workflow.ErrWorkflowNotFound) {
			logging.Info("Workflow not found in Temporal - signaling recovery needed",
				"workflowID", workflowID,
				"chatID", req.Msg.ChatId,
			)
			// Return success=false with needs_recovery and recovery_type
			// The frontend should prompt the user to confirm starting a new workflow
			return connect.NewResponse(&reliantv1.ResumeChatResponse{
				Success:       false,
				Message:       "The workflow session was interrupted and cannot be resumed. Your message history is preserved. Would you like to start a new conversation?",
				WorkflowId:    workflowID,
				NeedsRecovery: true,
				RecoveryType:  reliantv1.RecoveryType_RECOVERY_TYPE_WORKFLOW_LOST,
			}), nil
		}
		logging.Error("Failed to resume workflow", "error", err, "workflowID", workflowID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resume workflow"))
	}

	// If workflow was expired and reset, update the chat's run ID from the new Temporal run
	runID := ""
	if isExpired {
		// Fetch the new run ID from Temporal (reset created a new run)
		temporalState, temporalErr := s.getTemporalWorkflowState(ctx, workflowID)
		if temporalErr == nil && temporalState.Exists {
			runID = temporalState.RunID
			s.updateWorkflowRunIDs(ctx, req.Msg.ChatId, workflowID, runID)
		}
	} else if chat.RunID != nil {
		runID = *chat.RunID
	}

	return connect.NewResponse(&reliantv1.ResumeChatResponse{
		Success:      true,
		Message:      "Chat resumed successfully",
		WorkflowId:   workflowID,
		RunId:        runID,
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
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		temporalState, err := s.getTemporalWorkflowState(ctx, *chat.WorkflowID)
		if err != nil {
			logging.Error("Failed to check workflow status for compaction",
				"error", err,
				"chatID", req.Msg.ChatId,
				"workflowID", *chat.WorkflowID,
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
	s.updateWorkflowRunIDs(ctx, req.Msg.ChatId, workflowID, runID)

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
