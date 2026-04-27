// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	cfg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// ChatService implements the ChatService RPC handlers
type ChatService struct {
	reliantv1connect.UnimplementedChatServiceHandler
	database     db.Repository
	tempClient   client.Client
	pauseService *workflow.PauseService
	threads      *threads.Service
	taskQueue    string
	streamingHub streaming.StreamingHub
	discussLocks sync.Map // per-chat lock to prevent concurrent discuss calls
}

// NewChatService creates a new ChatService
func NewChatService(database db.Repository, tempClient client.Client, pauseService *workflow.PauseService, taskQueue string, hub streaming.StreamingHub) *ChatService {
	if strings.TrimSpace(taskQueue) == "" {
		taskQueue = workflow.SharedTaskQueue
	}

	return &ChatService{
		database:     database,
		tempClient:   tempClient,
		pauseService: pauseService,
		threads:      threads.NewService(database),
		taskQueue:    taskQueue,
		streamingHub: hub,
	}
}

// getChatForUser fetches a chat and verifies ownership in a single query.
// This is the preferred pattern for defense-in-depth ownership checks on chat access.
// New code should use this instead of separate GetChat + UserID comparison.
func (s *ChatService) getChatForUser(ctx context.Context, chatID, userID string) (*db.Chat, error) {
	chat, err := s.database.GetChatWithUserCheck(ctx, chatID, userID)
	if err != nil {
		return nil, err
	}
	return chat, nil
}

// checkParamsActuallyChanged queries the workflow for its current inputs and compares
// with the incoming params. Returns true only if at least one param value actually changed.
// This prevents the "params changed" message from being sent when params haven't changed.
func (s *ChatService) checkParamsActuallyChanged(ctx context.Context, workflowID, runID string, incomingParams map[string]interface{}) bool {
	// Query the workflow for its current inputs
	queryResp, err := s.tempClient.QueryWorkflow(ctx, workflowID, runID, "get_workflow_inputs")
	if err != nil {
		// If query fails (e.g., workflow not running yet), assume params changed to be safe
		logging.Warn("Failed to query workflow inputs, assuming params changed", "error", err, "workflowID", workflowID)
		return true
	}

	var currentInputs map[string]interface{}
	if err := queryResp.Get(&currentInputs); err != nil {
		logging.Warn("Failed to decode workflow inputs, assuming params changed", "error", err, "workflowID", workflowID)
		return true
	}

	// Compare each incoming param with current value using JSON normalization.
	// Both sides go through JSON serialization (Temporal stores as JSON, protobuf
	// values are JSON-compatible), so normalizing to JSON bytes eliminates type
	// mismatches (e.g., float64 vs int from JSON round-trip, structpb types vs
	// native Go types).
	for key, newValue := range incomingParams {
		currentValue, exists := currentInputs[key]
		if !exists {
			logging.Debug("Param change detected: new param", "key", key, "value", newValue)
			return true
		}
		newJSON, errNew := json.Marshal(newValue)
		curJSON, errCur := json.Marshal(currentValue)
		if errNew != nil || errCur != nil {
			// If marshaling fails, fall back to assuming changed
			logging.Warn("Failed to marshal param for comparison, assuming changed", "key", key, "marshalNewErr", errNew, "marshalCurErr", errCur)
			return true
		}
		if string(newJSON) != string(curJSON) {
			logging.Debug("Param change detected: value changed", "key", key, "old", string(curJSON), "new", string(newJSON))
			return true
		}
	}

	logging.Debug("No actual param changes detected", "workflowID", workflowID, "paramCount", len(incomingParams))
	return false
}

// chatToProto converts a db.Chat to proto Chat
// chatToProto converts a db.Chat to proto Chat
// NOTE: model/temperature/max_tokens are no longer stored on chat - they are workflow input params
// NOTE: WorkflowStatus comes from JOIN with workflows table (see GetChat query)
func chatToProto(c *db.Chat) *reliantv1.Chat {
	proto := &reliantv1.Chat{
		Id:              c.ID,
		UserId:          c.UserID,
		Title:           c.Title,
		ProjectId:       c.ProjectID,
		State:           c.State,
		CreatedAt:       c.CreatedAt.Format(time.RFC3339),
		UpdatedAt:       c.UpdatedAt.Format(time.RFC3339),
		LastActive:      c.LastActive.Format(time.RFC3339),
		SelectedPresets: c.SelectedPresets,
	}
	if c.WorktreeID != nil {
		proto.WorktreeId = c.WorktreeID
	}
	if c.WorkflowName != nil {
		proto.WorkflowName = c.WorkflowName
	}
	if c.WorkflowID != nil {
		proto.WorkflowId = c.WorkflowID
	}
	if c.RunID != nil {
		proto.RunId = c.RunID
	}
	if c.LastMessageAt != nil {
		formatted := c.LastMessageAt.Format(time.RFC3339)
		proto.LastMessageAt = &formatted
	}
	// Activity is the computed activity from chats_with_activity view
	if c.Activity != nil {
		proto.Activity = reliantv1.ChatActivity(*c.Activity)
	}
	proto.Unread = c.Unread
	if c.ActiveDaemonID != nil {
		proto.ActiveDaemonId = c.ActiveDaemonID
	}
	return proto
}

// displayStyleProtoToInt32Ptr converts a proto DisplayStyle pointer to an *int32 for the database.
func displayStyleProtoToInt32Ptr(ds *reliantv1.DisplayStyle) *int32 {
	if ds == nil {
		return nil
	}
	v := int32(*ds)
	return &v
}

// getEffectiveWorkingPath returns the working directory path for a chat.
// If the chat has a worktree, it returns the worktree path.
// Otherwise, it returns the project path.
// This ensures tools execute in the correct directory based on the chat's context.
func (s *ChatService) getEffectiveWorkingPath(ctx context.Context, chat *db.Chat) string {
	// First try to get worktree path if the chat has one
	if chat.WorktreeID != nil && *chat.WorktreeID != "" {
		if worktree, err := s.database.GetWorktree(ctx, *chat.WorktreeID); err == nil && worktree != nil {
			return worktree.Path
		}
	}

	// Fall back to project path
	if chat.ProjectID != "" {
		if project, err := s.database.GetProject(ctx, chat.ProjectID); err == nil && project != nil {
			return project.Path
		}
	}

	return ""
}

// ============================================================================
// TEMPORAL WORKFLOW STATUS
// ============================================================================

// TemporalWorkflowState represents the result of querying Temporal for workflow status
type TemporalWorkflowState struct {
	Exists    bool              // Whether the workflow exists in Temporal
	Status    db.WorkflowStatus // Mapped status (only valid if Exists is true)
	RunID     string            // Current run ID (only valid if Exists is true)
	IsRunning bool              // True if Temporal says workflow is running (may still be stuck)
}

// getTemporalWorkflowState queries Temporal for the actual workflow state.
// This is the source of truth - DB status is just a cache.
// Returns state with Exists=false if workflow not found (expired or never existed).
func (s *ChatService) getTemporalWorkflowState(ctx context.Context, workflowID string) (*TemporalWorkflowState, error) {
	descResp, err := s.tempClient.DescribeWorkflowExecution(ctx, workflowID, "")
	if err != nil {
		// Check if it's a "not found" error
		errStr := err.Error()
		if strings.Contains(errStr, "not found") || strings.Contains(errStr, "NotFound") {
			return &TemporalWorkflowState{Exists: false}, nil
		}
		// Unexpected error - return it
		return nil, fmt.Errorf("failed to query Temporal: %w", err)
	}

	if descResp == nil || descResp.WorkflowExecutionInfo == nil {
		return &TemporalWorkflowState{Exists: false}, nil
	}

	execStatus := descResp.WorkflowExecutionInfo.Status
	runID := ""
	if descResp.WorkflowExecutionInfo.Execution != nil {
		runID = descResp.WorkflowExecutionInfo.Execution.RunId
	}

	// Map Temporal status to our status
	var mappedStatus db.WorkflowStatus
	isRunning := false
	switch execStatus {
	case enums.WORKFLOW_EXECUTION_STATUS_RUNNING:
		// Temporal says running - could be actively running or waiting for worker
		mappedStatus = db.WorkflowStatusRunning
		isRunning = true
	case enums.WORKFLOW_EXECUTION_STATUS_COMPLETED:
		mappedStatus = db.WorkflowStatusCompleted
	case enums.WORKFLOW_EXECUTION_STATUS_FAILED, enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		mappedStatus = db.WorkflowStatusFailed
	case enums.WORKFLOW_EXECUTION_STATUS_CANCELED, enums.WORKFLOW_EXECUTION_STATUS_TERMINATED:
		mappedStatus = db.WorkflowStatusCancelled
	case enums.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW:
		// Workflow continued - treat as running since there's a new run
		mappedStatus = db.WorkflowStatusRunning
		isRunning = true
	default:
		// Unknown status - treat as completed to allow starting fresh
		logging.Warn("Unknown Temporal workflow status",
			"workflowID", workflowID,
			"status", execStatus.String(),
		)
		mappedStatus = db.WorkflowStatusCompleted
	}

	return &TemporalWorkflowState{
		Exists:    true,
		Status:    mappedStatus,
		RunID:     runID,
		IsRunning: isRunning,
	}, nil
}

// reconcileWorkflowStatus updates DB status to match Temporal if they differ.
// This keeps the UI consistent without relying on DB as source of truth.
func (s *ChatService) reconcileWorkflowStatus(ctx context.Context, workflowID string, dbStatus, temporalStatus db.WorkflowStatus) {
	if dbStatus == temporalStatus {
		return
	}

	logging.Debug("Reconciling workflow status: DB differs from Temporal",
		"workflowID", workflowID,
		"dbStatus", dbStatus,
		"temporalStatus", temporalStatus,
	)

	if err := s.database.UpdateWorkflowStatus(ctx, workflowID, temporalStatus); err != nil {
		logging.Warn("Failed to reconcile workflow status",
			"error", err,
			"workflowID", workflowID,
		)
	}
}

// resurrectGhostWorkflow handles the case where a workflow exists in DB as running/paused
// but is missing from Temporal (ghost workflow). Instead of failing, we restart the workflow
// with a fresh Temporal execution, allowing the conversation to continue seamlessly.
//
// This is a defensive recovery mechanism that ensures the system always strives toward
// a working state, even after Temporal data loss or server restarts.
func (s *ChatService) resurrectGhostWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.SendMessageRequest],
	chat *db.Chat,
	existingWorkflow *db.Workflow,
	workflowID string,
	userID string,
) (*connect.Response[reliantv1.SendMessageResponse], error) {
	workflowName := existingWorkflow.WorkflowName

	// Extract user and system messages from input
	userContent, systemMessages, hasUserContent := extractMessagesFromInput(req.Msg.Messages)

	// Update selected presets if provided
	if len(req.Msg.SelectedPresets) > 0 {
		chat.SelectedPresets = req.Msg.SelectedPresets
		chat.UpdatedAt = time.Now().UTC()
		if err := s.database.UpdateChat(ctx, chat); err != nil {
			logging.Error("[Ghost Recovery] Failed to update chat presets", "error", err, "chatID", req.Msg.ChatId)
			// Non-fatal, continue
		}
	}

	// Determine target thread - default to root workflow thread
	targetThread := existingWorkflow.Thread
	if req.Msg.TargetThread != nil && *req.Msg.TargetThread != "" {
		targetThread = *req.Msg.TargetThread
	}

	// Step 1: Save messages BEFORE starting workflow
	// System messages first, then user message
	for _, sysMsg := range systemMessages {
		_, err := s.database.SaveMessageToThread(ctx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), sysMsg.Content, &workflowID, nil, displayStyleProtoToInt32Ptr(sysMsg.DisplayStyle))
		if err != nil {
			logging.Error("[Ghost Recovery] Failed to save system message", "error", err, "chatID", req.Msg.ChatId)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save system message: %w", err))
		}
	}

	var savedMessageID string
	if hasUserContent || len(req.Msg.Attachments) > 0 {
		savedMsg, err := s.database.SaveMessageToThread(ctx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), userContent, &workflowID, req.Msg.Attachments, nil)
		if err != nil {
			logging.Error("[Ghost Recovery] Failed to save message", "error", err, "chatID", req.Msg.ChatId)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save message: %w", err))
		}
		savedMessageID = savedMsg.ID
		logging.Info("[Ghost Recovery] Saved user message",
			"chatID", req.Msg.ChatId,
			"messageID", savedMessageID,
			"thread", targetThread,
		)
	}

	// Step 2: Build workflow options - same ID, fresh execution
	workflowOptions := client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                s.taskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_TERMINATE_EXISTING,
		WorkflowExecutionTimeout: workflow.WorkflowExecutionTimeout,
	}

	// Step 4: Build workflow inputs (with presets and model defaults)
	// Use worktree path if available, otherwise project path
	projectPath := s.getEffectiveWorkingPath(ctx, chat)

	// Merge presets: existing chat presets + any new ones from the request
	effectivePresets := make(map[string]string)
	for k, v := range chat.SelectedPresets {
		if v != "" {
			effectivePresets[k] = v
		}
	}
	for k, v := range req.Msg.SelectedPresets {
		if v != "" {
			effectivePresets[k] = v
		}
	}

	initialData := s.buildWorkflowInputs(ctx, userID, projectPath, chat.ProjectID, workflowName, effectivePresets, req.Msg.WorkflowParams)

	// Validate workflow inputs before starting
	if validationErrors := s.validateWorkflowInputs(ctx, workflowName, chat.ProjectID, initialData); len(validationErrors) > 0 {
		errMsgs := make([]string, len(validationErrors))
		for i, e := range validationErrors {
			errMsgs[i] = e.Error()
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow input validation failed: %s", strings.Join(errMsgs, "; ")))
	}

	// Step 5: Build execution context - inheriting existing thread
	// We use ThreadModeInherit because we're continuing an existing conversation
	execContext := &v2.ExecutionContext{
		WorkflowID:   workflowID,
		ChatID:       req.Msg.ChatId,
		WorkflowName: workflowName,
		Thread:       targetThread,
		ThreadMode:   model.ThreadModeInherit, // Inherit existing thread context
	}

	// Note: Message was already saved above before ghost recovery

	// Inject session daemon if set on chat
	injectSessionDaemonID(initialData, chat)

	workflowInput := v2.WorkflowInput{
		ChatID:       req.Msg.ChatId,
		WorkflowName: workflowName,
		Inputs:       initialData,
		ExecContext:  execContext,
	}

	// Step 6: Start fresh Temporal execution
	workflowRun, err := s.tempClient.ExecuteWorkflow(ctx, workflowOptions, v2.DynamicWorkflow, workflowInput)
	if err != nil {
		logging.Error("[Ghost Recovery] Failed to start workflow", "error", err, "chatID", req.Msg.ChatId, "workflowID", workflowID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resurrect workflow"))
	}

	runID := workflowRun.GetRunID()
	logging.Info("[Ghost Recovery] Successfully resurrected workflow",
		"chatID", req.Msg.ChatId,
		"workflowID", workflowID,
		"newRunID", runID,
		"workflowName", workflowName,
		"thread", targetThread,
	)

	// Step 7: Update run IDs and workflow status
	s.updateWorkflowRunIDs(ctx, req.Msg.ChatId, workflowID, runID)
	if err := s.database.UpdateWorkflowStatus(ctx, workflowID, db.WorkflowStatusRunning); err != nil {
		logging.Warn("[Ghost Recovery] Failed to update workflow status", "error", err, "workflowID", workflowID)
		// Non-fatal - workflow is running in Temporal
	}

	workflowStatus := fmt.Sprintf("%d", db.WorkflowStatusRunning)
	go s.trackMessageSent(ctx, userID, chat, savedMessageID, targetThread, userContent, len(req.Msg.Attachments))
	return connect.NewResponse(&reliantv1.SendMessageResponse{
		ChatId:         req.Msg.ChatId,
		WorkflowId:     workflowID,
		RunId:          runID,
		Status:         "processing",
		WorkflowStatus: &workflowStatus,
		MessageId:      savedMessageID,
	}), nil
}

// ============================================================================
// WORKFLOW PARAM RESOLUTION
// ============================================================================

// defaultNewWorkflowTemplate returns the starter template for new workflow drafts.
// Uses the embedded builtin://agent workflow so new workflows start with a working agent pattern.
// Any changes to internal/workflow/builtin/agent.yaml automatically become the new default.
// The caller should replace "name: agent" with the desired workflow name.
func defaultNewWorkflowTemplate() string {
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("agent.yaml")
	if err != nil {
		// Fallback to minimal workflow if embedded file fails (shouldn't happen)
		logging.Error("Failed to read embedded agent.yaml", "error", err)
		return `name: agent
description: ""
nodes: []`
	}
	return string(data)
}

// isWorkflowBuilderChat checks if the chat is using the workflow_builder preset
func isWorkflowBuilderChat(selectedPresets map[string]string) bool {
	for _, presetName := range selectedPresets {
		if presetName == "workflow_builder" {
			return true
		}
	}
	return false
}

// extractMessagesFromInput separates user and system messages from the input messages array.
// Returns: userContent (concatenated user messages), systemMessages slice, and whether any user content exists.
func extractMessagesFromInput(messages []*reliantv1.InputMessage) (userContent string, systemMessages []*reliantv1.InputMessage, hasUserContent bool) {
	var userParts []string
	for _, msg := range messages {
		switch msg.Role {
		case reliantv1.MessageRole_MESSAGE_ROLE_USER:
			if msg.Content != "" {
				userParts = append(userParts, msg.Content)
			}
		case reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM:
			if msg.Content != "" {
				systemMessages = append(systemMessages, msg)
			}
		}
	}
	userContent = strings.Join(userParts, "\n")
	hasUserContent = len(userParts) > 0
	return
}

func validateWorkflowParamStructure(params map[string]*structpb.Value) error {
	for key, value := range params {
		if strings.Contains(key, ".") {
			return fmt.Errorf("workflow_params contains dotted key %q; use nested objects instead (for example {\"agent\": {\"model\": ...}})", key)
		}
		if err := validateWorkflowParamKeyPath(key, value); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkflowParamKeyPath(keyPath string, value *structpb.Value) error {
	if value == nil {
		return nil
	}
	structValue, ok := value.AsInterface().(map[string]interface{})
	if !ok {
		return nil
	}
	for nestedKey, nestedValue := range structValue {
		nestedPath := nestedKey
		if keyPath != "" {
			nestedPath = keyPath + "." + nestedKey
		}
		if strings.Contains(nestedKey, ".") {
			return fmt.Errorf("workflow_params contains dotted key %q; use nested objects instead (for example {\"agent\": {\"model\": ...}})", nestedPath)
		}
		nestedProtoValue, err := structpb.NewValue(nestedValue)
		if err != nil {
			continue
		}
		if err := validateWorkflowParamKeyPath(nestedPath, nestedProtoValue); err != nil {
			return err
		}
	}
	return nil
}

// resolveDefaultWorkflow resolves the workflow to use when not explicitly provided.
// Priority: request > user setting > system default (builtin://agent)
func (s *ChatService) resolveDefaultWorkflow(ctx context.Context, userID string, reqWorkflow string) string {
	// Priority 1: Explicit request
	if reqWorkflow != "" {
		return reqWorkflow
	}

	// Priority 2: User default setting
	if setting, err := s.database.GetSetting(ctx, userID, nil, "config.default_workflow"); err == nil && setting.Value != "" {
		return setting.Value
	}

	// Priority 3: System default
	return workflow.DefaultWorkflow
}

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

// updateWorkflowRunIDs updates the chat with workflow_id and run_id
func (s *ChatService) updateWorkflowRunIDs(ctx context.Context, chatID, workflowID, runID string) {
	chat, err := s.database.GetChat(ctx, chatID)
	if err != nil {
		logging.Error("Failed to get chat for workflow update", "error", err, "chatID", chatID)
		return
	}

	chat.WorkflowID = &workflowID
	chat.RunID = &runID
	if err := s.database.UpdateChat(ctx, chat); err != nil {
		logging.Error("Failed to update chat with workflow info", "error", err, "chatID", chatID)
	}
}

func (s *ChatService) trackMessageSent(ctx context.Context, userID string, chat *db.Chat, messageID, threadID, userContent string, attachmentCount int) {
	if chat == nil || messageID == "" {
		return
	}

	// Determine workflow name and type
	var workflowName, workflowType string
	if chat.WorkflowName != nil {
		workflowName = *chat.WorkflowName
		if strings.HasPrefix(workflowName, "builtin://") {
			workflowType = "builtin"
		} else {
			workflowType = "custom"
		}
	}

	// Check if this is the first message in the chat by counting existing user messages
	isFirstInChat := false
	if threadID != "" {
		count, err := s.database.CountMessagesInThread(ctx, threadID)
		if err == nil && count <= 1 {
			isFirstInChat = true
		}
	}

	metrics := analytics.MessageSentMetrics{
		MessageID:      messageID,
		ChatID:         chat.ID,
		ProjectID:      chat.ProjectID,
		ThreadID:       threadID,
		HasAttachments: attachmentCount > 0,
		ContentLength:  len(userContent),
		WorkflowName:   workflowName,
		WorkflowType:   workflowType,
		IsFirstInChat:  isFirstInChat,
	}
	if chat.WorkflowID != nil {
		metrics.WorkflowID = *chat.WorkflowID
	}

	analyticsClient := analytics.GetClientForUser(ctx, userID)
	analyticsClient.TrackMessageSent(metrics)

	// Check if this is the user's first-ever message across all chats
	if isFirstInChat {
		// List all chats for this user to see if they have any other chats with messages
		chats, err := s.database.SearchChats(ctx, db.ChatSearchFilters{
			UserID: userID,
			Limit:  2, // We only need to know if there's more than 1
		})
		if err == nil && len(chats) <= 1 {
			analyticsClient.TrackFirstMessageSent(analytics.FirstMessageSentMetrics{
				ChatID:       chat.ID,
				ProjectID:    chat.ProjectID,
				WorkflowName: workflowName,
				WorkflowType: workflowType,
			})
		}
	}
}

// SendMessage sends messages to a chat and continues workflow
func (s *ChatService) SendMessage(
	ctx context.Context,
	req *connect.Request[reliantv1.SendMessageRequest],
) (*connect.Response[reliantv1.SendMessageResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	// Extract user and system messages from input
	userContent, systemMessages, hasUserContent := extractMessagesFromInput(req.Msg.Messages)

	// Require at least one user message or attachments
	if !hasUserContent && len(req.Msg.Attachments) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at least one user message or attachment is required"))
	}

	// Get chat with ownership check (defense-in-depth via single query)
	chat, err := s.getChatForUser(ctx, req.Msg.ChatId, userID)
	if err != nil {
		logging.Error("Failed to get chat", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// Note: Previously checked if workflow completed and thread is closed.
	// Removed to allow restarting workflows - SendMessage will start a new workflow
	// for completed/failed/cancelled workflows (see status switch below).

	if err := validateWorkflowParamStructure(req.Msg.WorkflowParams); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Check workflow status to decide: resume paused, send to running, or start new
	// This must happen atomically to avoid race conditions
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		workflowID := *chat.WorkflowID

		// Use transaction to atomically check status and update
		var existingWorkflow *db.Workflow

		err := s.database.RunTx(ctx, func(txCtx context.Context) error {
			wf, err := s.database.GetWorkflow(txCtx, workflowID)
			if err != nil {
				return err
			}
			existingWorkflow = wf
			return nil
		})

		if err == nil && existingWorkflow != nil {
			// Use Temporal as source of truth for workflow status
			// For running workflows, verify Temporal agrees
			if existingWorkflow.Status == db.WorkflowStatusRunning {
				temporalState, temporalErr := s.getTemporalWorkflowState(ctx, workflowID)
				if temporalErr != nil {
					// Temporal query failed - this is an error we should surface
					logging.Error("Failed to query Temporal for workflow status",
						"error", temporalErr,
						"chatID", req.Msg.ChatId,
						"workflowID", workflowID,
					)
					return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("failed to check workflow status: %w", temporalErr))
				}

				if !temporalState.Exists {
					// Ghost workflow detected - Temporal lost the workflow but DB says running
					// RESURRECT: Start a fresh Temporal execution with the same workflow ID
					// This allows the conversation to continue seamlessly
					logging.Info("[Ghost Recovery] Resurrecting ghost workflow",
						"chatID", req.Msg.ChatId,
						"workflowID", workflowID,
						"dbStatus", existingWorkflow.Status,
						"workflowName", existingWorkflow.WorkflowName,
					)

					return s.resurrectGhostWorkflow(ctx, req, chat, existingWorkflow, workflowID, userID)
				} else if !temporalState.IsRunning {
					if existingWorkflow.Status == db.WorkflowStatusPaused {
						// Paused workflow whose Temporal execution timed out or completed.
						// Keep status as paused — the paused handler uses PauseService.ResumeWorkflow
						// which handles reset-based resume for expired executions.
						logging.Info("Paused workflow Temporal execution not running - will attempt reset-based resume",
							"workflowID", workflowID,
							"chatID", req.Msg.ChatId,
							"temporalStatus", temporalState.Status,
						)
					} else {
						// Running workflow that stopped — reconcile status
						s.reconcileWorkflowStatus(ctx, workflowID, existingWorkflow.Status, temporalState.Status)
						existingWorkflow.Status = temporalState.Status
					}
				}
			}

			switch existingWorkflow.Status {
			case db.WorkflowStatusPaused:

				// Discuss mode: lightweight LLM chat without resuming the workflow
				if req.Msg.Discuss {
					return s.handleDiscussMode(ctx, req, chat, existingWorkflow, workflowID, userID, userContent, hasUserContent, systemMessages)
				}

				// Update selected presets on chat if provided (before starting workflow)
				if len(req.Msg.SelectedPresets) > 0 {
					chat.SelectedPresets = req.Msg.SelectedPresets
					chat.UpdatedAt = time.Now().UTC()
					if err := s.database.UpdateChat(ctx, chat); err != nil {
						logging.Error("Failed to update chat presets", "error", err, "chatID", req.Msg.ChatId)
						// Don't fail - non-critical
					}
				}

				// Determine target thread - use workflow's thread from DB (not root workflow ID)
				// to handle forked/child threads correctly.
				targetThread := existingWorkflow.Thread
				if req.Msg.TargetThread != nil && *req.Msg.TargetThread != "" {
					targetThread = *req.Msg.TargetThread
				}

				// Atomically: save messages + update status to running
				var savedMessageID string
				err := s.database.RunTx(ctx, func(txCtx context.Context) error {
					// Save system messages first
					for _, sysMsg := range systemMessages {
						_, err := s.database.SaveMessageToThread(txCtx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), sysMsg.Content, &workflowID, nil, displayStyleProtoToInt32Ptr(sysMsg.DisplayStyle))
						if err != nil {
							return fmt.Errorf("failed to save system message: %w", err)
						}
					}

					// Save the user message
					if hasUserContent || len(req.Msg.Attachments) > 0 {
						savedMsg, err := s.database.SaveMessageToThread(txCtx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), userContent, &workflowID, req.Msg.Attachments, nil)
						if err != nil {
							return fmt.Errorf("failed to save message: %w", err)
						}
						savedMessageID = savedMsg.ID
					}

					// Update workflow status to running
					if err := s.database.UpdateWorkflowStatus(txCtx, workflowID, db.WorkflowStatusRunning); err != nil {
						return fmt.Errorf("failed to update workflow status: %w", err)
					}

					return nil
				})
				if err != nil {
					logging.Error("Failed to resume paused workflow", "error", err, "workflowID", workflowID)
					return nil, connect.NewError(connect.CodeInternal, err)
				}

				// Get RunID from chat (workflow struct doesn't have it)
				runID := ""
				if chat.RunID != nil {
					runID = *chat.RunID
				}

				// Signal workflow with param/preset input updates when provided.
				if len(req.Msg.WorkflowParams) > 0 || len(req.Msg.SelectedPresets) > 0 {
					stateUpdate := s.buildStateUpdateForActiveWorkflow(ctx, userID, chat, existingWorkflow.WorkflowName, req.Msg.SelectedPresets, req.Msg.WorkflowParams)

					// Validate model selectors in updated params
					if validationErrors := s.validateWorkflowInputs(ctx, existingWorkflow.WorkflowName, chat.ProjectID, stateUpdate); len(validationErrors) > 0 {
						errMsgs := make([]string, len(validationErrors))
						for i, e := range validationErrors {
							errMsgs[i] = e.Error()
						}
						return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow input validation failed: %s", strings.Join(errMsgs, "; ")))
					}

					// Only add "params changed" message if params actually changed from current workflow state
					if len(stateUpdate) > 0 && s.checkParamsActuallyChanged(ctx, workflowID, runID, stateUpdate) {
						hiddenStyle := int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN)
						_, err := s.database.SaveMessageToThread(ctx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), "Some of your params have changed, which may include mode, tools, temperature, or something else. Please continue as planned.", &workflowID, nil, &hiddenStyle)
						if err != nil {
							logging.Warn("Failed to save system message about param changes", "error", err, "chatID", req.Msg.ChatId)
						}
					}

					if len(stateUpdate) > 0 {
						if err := s.tempClient.SignalWorkflow(ctx, workflowID, runID, "update_workflow_state", stateUpdate); err != nil {
							logging.Warn("Failed to signal workflow with param updates", "error", err, "workflowID", workflowID)
						}
					}
				}

				// Resume the workflow via PauseService which handles all cases:
				// - Live Temporal execution: sends signal.resume
				// - Expired/timed-out execution: resets to pause point and resumes
				// - Lost execution: returns ErrWorkflowNotFound
				if err := s.pauseService.ResumeWorkflow(ctx, workflowID, req.Msg.ChatId); err != nil {
					if errors.Is(err, workflow.ErrWorkflowNotFound) {
						logging.Warn("Paused workflow not found in Temporal during SendMessage - starting fresh",
							"workflowID", workflowID, "chatID", req.Msg.ChatId)
						// Fall through to start a new workflow below
						break
					}
					logging.Error("Failed to resume paused workflow",
						"error", err, "workflowID", workflowID, "chatID", req.Msg.ChatId)
				}

				// If reset-based resume happened, update the chat's run ID
				if newState, stateErr := s.getTemporalWorkflowState(ctx, workflowID); stateErr == nil && newState.Exists {
					if chat.RunID == nil || *chat.RunID != newState.RunID {
						s.updateWorkflowRunIDs(ctx, req.Msg.ChatId, workflowID, newState.RunID)
						runID = newState.RunID
					}
				}

				// Return with updated workflow_status so frontend knows we resumed
				workflowStatus := fmt.Sprintf("%d", db.WorkflowStatusRunning)
				go s.trackMessageSent(ctx, userID, chat, savedMessageID, targetThread, userContent, len(req.Msg.Attachments))
				return connect.NewResponse(&reliantv1.SendMessageResponse{
					ChatId:         req.Msg.ChatId,
					WorkflowId:     workflowID,
					RunId:          runID,
					Status:         "processing",
					WorkflowStatus: &workflowStatus,
					MessageId:      savedMessageID,
				}), nil

			case db.WorkflowStatusRunning:

				// Update selected presets on chat if provided
				if len(req.Msg.SelectedPresets) > 0 {
					chat.SelectedPresets = req.Msg.SelectedPresets
					chat.UpdatedAt = time.Now().UTC()
					if err := s.database.UpdateChat(ctx, chat); err != nil {
						logging.Error("Failed to update chat presets", "error", err, "chatID", req.Msg.ChatId)
						// Don't fail - non-critical
					}
				}

				// Use workflow's thread from DB (not root workflow ID) to handle
				// forked/child threads correctly.
				targetThread := existingWorkflow.Thread
				if req.Msg.TargetThread != nil && *req.Msg.TargetThread != "" {
					targetThread = *req.Msg.TargetThread
				}

				// Save system messages first
				for _, sysMsg := range systemMessages {
					_, err := s.database.SaveMessageToThread(ctx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), sysMsg.Content, &workflowID, nil, displayStyleProtoToInt32Ptr(sysMsg.DisplayStyle))
					if err != nil {
						logging.Error("Failed to save system message to running workflow", "error", err)
						return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save system message: %w", err))
					}
				}

				// Save the user message
				var savedMsg *db.Message
				if hasUserContent || len(req.Msg.Attachments) > 0 {
					var err error
					savedMsg, err = s.database.SaveMessageToThread(ctx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), userContent, &workflowID, req.Msg.Attachments, nil)
					if err != nil {
						logging.Error("Failed to save message to running workflow", "error", err)
						return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save message: %w", err))
					}
				}

				// Signal workflow with param/preset input updates when provided.
				if len(req.Msg.WorkflowParams) > 0 || len(req.Msg.SelectedPresets) > 0 {
					stateUpdate := s.buildStateUpdateForActiveWorkflow(ctx, userID, chat, existingWorkflow.WorkflowName, req.Msg.SelectedPresets, req.Msg.WorkflowParams)

					// Validate model selectors in updated params
					if validationErrors := s.validateWorkflowInputs(ctx, existingWorkflow.WorkflowName, chat.ProjectID, stateUpdate); len(validationErrors) > 0 {
						errMsgs := make([]string, len(validationErrors))
						for i, e := range validationErrors {
							errMsgs[i] = e.Error()
						}
						return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow input validation failed: %s", strings.Join(errMsgs, "; ")))
					}

					runID := ""
					if chat.RunID != nil {
						runID = *chat.RunID
					}

					// Only add "params changed" message if params actually changed from current workflow state
					if len(stateUpdate) > 0 && s.checkParamsActuallyChanged(ctx, workflowID, runID, stateUpdate) {
						hiddenStyle := int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN)
						_, err := s.database.SaveMessageToThread(ctx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), "Some of your params have changed, which may include mode, tools, temperature, or something else. Please continue as planned.", &workflowID, nil, &hiddenStyle)
						if err != nil {
							logging.Error("Failed to save system message about param changes", "error", err, "chatID", req.Msg.ChatId)
							// Non-fatal, continue to signal
						}
					}

					if len(stateUpdate) > 0 {
						if err := s.tempClient.SignalWorkflow(ctx, workflowID, runID, "update_workflow_state", stateUpdate); err != nil {
							logging.Warn("Failed to signal workflow with param updates", "error", err, "workflowID", workflowID)
						}
					}
				}

				// Get RunID from chat
				runID := ""
				if chat.RunID != nil {
					runID = *chat.RunID
				}

				workflowStatus := fmt.Sprintf("%d", db.WorkflowStatusRunning)
				var messageID string
				if savedMsg != nil {
					messageID = savedMsg.ID
				}
				go s.trackMessageSent(ctx, userID, chat, messageID, targetThread, userContent, len(req.Msg.Attachments))
				return connect.NewResponse(&reliantv1.SendMessageResponse{
					ChatId:         req.Msg.ChatId,
					WorkflowId:     workflowID,
					RunId:          runID,
					Status:         "processing",
					WorkflowStatus: &workflowStatus,
					MessageId:      messageID,
				}), nil

			case db.WorkflowStatusExpired:
				// Expired workflow — reset to pause point and resume
				logging.Info("Resuming expired workflow via reset",
					"chatID", req.Msg.ChatId,
					"workflowID", workflowID,
				)

				// Update selected presets on chat if provided
				if len(req.Msg.SelectedPresets) > 0 {
					chat.SelectedPresets = req.Msg.SelectedPresets
					chat.UpdatedAt = time.Now().UTC()
					if err := s.database.UpdateChat(ctx, chat); err != nil {
						logging.Error("Failed to update chat presets", "error", err, "chatID", req.Msg.ChatId)
					}
				}

				// Use workflow's thread from DB (not root workflow ID) to handle
				// forked/child threads correctly.
				targetThread := existingWorkflow.Thread
				if req.Msg.TargetThread != nil && *req.Msg.TargetThread != "" {
					targetThread = *req.Msg.TargetThread
				}

				// Save messages before reset
				var savedMessageID string
				for _, sysMsg := range systemMessages {
					_, err := s.database.SaveMessageToThread(ctx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), sysMsg.Content, &workflowID, nil, displayStyleProtoToInt32Ptr(sysMsg.DisplayStyle))
					if err != nil {
						logging.Error("Failed to save system message", "error", err)
						return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save system message: %w", err))
					}
				}
				if hasUserContent || len(req.Msg.Attachments) > 0 {
					savedMsg, err := s.database.SaveMessageToThread(ctx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), userContent, &workflowID, req.Msg.Attachments, nil)
					if err != nil {
						logging.Error("Failed to save message", "error", err)
						return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save message: %w", err))
					}
					savedMessageID = savedMsg.ID
				}

				// Reset expired workflow (handles orphan repair, reset, resume signal)
				newRunID, err := s.pauseService.ResumeExpiredWorkflow(ctx, workflowID, req.Msg.ChatId)
				if err != nil {
					logging.Error("Failed to reset expired workflow", "error", err, "workflowID", workflowID)
					return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resume expired workflow: %w", err))
				}

				// Update chat's run ID with the new run
				s.updateWorkflowRunIDs(ctx, req.Msg.ChatId, workflowID, newRunID)

				workflowStatus := fmt.Sprintf("%d", db.WorkflowStatusRunning)
				return connect.NewResponse(&reliantv1.SendMessageResponse{
					ChatId:         req.Msg.ChatId,
					WorkflowId:     workflowID,
					RunId:          newRunID,
					Status:         "processing",
					WorkflowStatus: &workflowStatus,
					MessageId:      savedMessageID,
				}), nil

			case db.WorkflowStatusFailed:
				// Check if this is a stuck workflow (DB says failed but Temporal says running)
				// Stuck workflows cannot be restarted - user must branch to continue
				temporalState, temporalErr := s.getTemporalWorkflowState(ctx, workflowID)
				if temporalErr == nil && temporalState.Exists && temporalState.IsRunning {
					logging.Warn("Attempted to resume stuck workflow - blocking",
						"chatID", req.Msg.ChatId,
						"workflowID", workflowID,
					)
					return nil, connect.NewError(connect.CodeFailedPrecondition,
						fmt.Errorf("this conversation experienced a workflow error and cannot be resumed - use the branch feature to start a new conversation from any previous message"))
				}
				// Normal failed workflow - fall through to start new

			case db.WorkflowStatusCancelled, db.WorkflowStatusCompleted:
				// Need to start a new workflow - fall through to normal flow
			}
		}
	}

	// Workflow name is always set on chat (required at creation)
	workflowName := *chat.WorkflowName

	needsChatUpdate := false

	// Use existing workflow ID from chat, or use chat ID as root workflow ID
	var workflowID string
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		workflowID = *chat.WorkflowID
	} else {
		workflowID = req.Msg.ChatId // Root workflow ID = chat ID
		chat.WorkflowID = &workflowID
		needsChatUpdate = true
	}

	// Handle workflow switching - only allowed when root workflow is pending (chat hasn't started)
	if req.Msg.Workflow != nil && *req.Msg.Workflow != "" && *req.Msg.Workflow != workflowName {
		// Check if root workflow is pending
		rootWorkflow, err := s.database.GetWorkflow(ctx, workflowID)
		if err != nil {
			logging.Error("Failed to get root workflow", "error", err, "workflowID", workflowID)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check workflow status"))
		}

		// Allow switching if workflow doesn't exist yet (new chat) or is pending (branched chat)
		if rootWorkflow != nil && rootWorkflow.Status != db.WorkflowStatusPending {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("cannot change workflow after chat has started - use Branch to create a new chat with a different workflow"))
		}

		// Validate workflow exists
		if _, err := s.loadWorkflowForValidation(ctx, *req.Msg.Workflow, chat.ProjectID); err != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workflow not found: %s", *req.Msg.Workflow))
		}

		// Update workflow name on both chat and workflow record
		workflowName = *req.Msg.Workflow
		chat.WorkflowName = req.Msg.Workflow
		needsChatUpdate = true

		if rootWorkflow != nil {
			if err := s.database.UpdateWorkflowName(ctx, workflowID, workflowName); err != nil {
				logging.Error("Failed to update workflow name", "error", err, "workflowID", workflowID)
				// Continue - chat update is more important
			}
		}
	}

	// Update selected presets if provided
	if len(req.Msg.SelectedPresets) > 0 {
		chat.SelectedPresets = req.Msg.SelectedPresets
		needsChatUpdate = true
	}

	// Update chat if needed
	if needsChatUpdate {
		chat.UpdatedAt = time.Now().UTC()
		if err := s.database.UpdateChat(ctx, chat); err != nil {
			logging.Error("Failed to update chat", "error", err, "chatID", req.Msg.ChatId)
			// Don't fail the request for non-critical updates
		}
	}

	workflowOptions := client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                s.taskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_TERMINATE_EXISTING,
		WorkflowExecutionTimeout: workflow.WorkflowExecutionTimeout,
	}

	// Merge presets: chat presets are base, request presets override
	effectivePresets := make(map[string]string)
	for k, v := range chat.SelectedPresets {
		if v != "" {
			effectivePresets[k] = v
		}
	}
	for k, v := range req.Msg.SelectedPresets {
		if v != "" {
			effectivePresets[k] = v
		}
	}

	// Resolve path for preset loading and workflow validation - use worktree if available
	projectPath := s.getEffectiveWorkingPath(ctx, chat)

	// Build workflow inputs from merged presets and user params
	initialData := s.buildWorkflowInputs(ctx, userID, projectPath, chat.ProjectID, workflowName, effectivePresets, req.Msg.WorkflowParams)

	// Determine target thread
	targetThread := workflowID
	if req.Msg.TargetThread != nil && *req.Msg.TargetThread != "" {
		targetThread = *req.Msg.TargetThread
	}

	// Build execution context for the workflow
	execContext := &v2.ExecutionContext{
		WorkflowID:   workflowID,
		ChatID:       req.Msg.ChatId,
		WorkflowName: workflowName,
		Thread:       targetThread,
		ThreadMode:   model.ThreadModeNew,
	}

	// Save messages BEFORE starting workflow for consistency
	// System messages first, then user message
	for _, sysMsg := range systemMessages {
		_, err := s.database.SaveMessageToThread(ctx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), sysMsg.Content, &workflowID, nil, displayStyleProtoToInt32Ptr(sysMsg.DisplayStyle))
		if err != nil {
			logging.Error("Failed to save system message for new workflow", "error", err, "chatID", req.Msg.ChatId)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save system message"))
		}
	}

	// Save user message to thread (message is saved BEFORE workflow starts)
	var savedMessageID string
	if hasUserContent || len(req.Msg.Attachments) > 0 {
		savedMsg, err := s.database.SaveMessageToThread(ctx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), userContent, &workflowID, req.Msg.Attachments, nil)
		if err != nil {
			logging.Error("Failed to save message for new workflow", "error", err, "chatID", req.Msg.ChatId)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save message"))
		}
		savedMessageID = savedMsg.ID
	}

	// Validate workflow inputs before starting
	// This catches missing required inputs early (400) instead of at runtime
	if validationErrors := s.validateWorkflowInputs(ctx, workflowName, chat.ProjectID, initialData); len(validationErrors) > 0 {
		errMsgs := make([]string, len(validationErrors))
		for i, e := range validationErrors {
			errMsgs[i] = e.Error()
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow input validation failed: %s", strings.Join(errMsgs, "; ")))
	}

	// Inject session daemon if set on chat
	injectSessionDaemonID(initialData, chat)

	workflowInput := v2.WorkflowInput{
		ChatID:       req.Msg.ChatId,
		WorkflowName: workflowName,
		Inputs:       initialData,
		ExecContext:  execContext,
	}

	workflowRun, err := s.tempClient.ExecuteWorkflow(ctx, workflowOptions, v2.DynamicWorkflow, workflowInput)
	if err != nil {
		logging.Error("Failed to start workflow", "error", err, "chatID", req.Msg.ChatId, "workflowID", workflowID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to start workflow"))
	}

	runID := workflowRun.GetRunID()
	s.updateWorkflowRunIDs(ctx, req.Msg.ChatId, workflowID, runID)

	workflowStatus := fmt.Sprintf("%d", db.WorkflowStatusRunning)
	go s.trackMessageSent(ctx, userID, chat, savedMessageID, targetThread, userContent, len(req.Msg.Attachments))
	return connect.NewResponse(&reliantv1.SendMessageResponse{
		ChatId:         req.Msg.ChatId,
		WorkflowId:     workflowID,
		RunId:          runID,
		Status:         "processing",
		WorkflowStatus: &workflowStatus,
		MessageId:      savedMessageID,
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

	// Cancel the workflow via Temporal
	if err := s.tempClient.CancelWorkflow(ctx, workflowID, ""); err != nil {
		logging.Error("Failed to cancel workflow", "error", err, "workflowID", workflowID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to cancel workflow"))
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

// UpdateWorkflowParams updates workflow parameters for a running chat
// This signals the running workflow to update its inputs (e.g., mode, temperature)
func (s *ChatService) UpdateWorkflowParams(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateWorkflowParamsRequest],
) (*connect.Response[reliantv1.UpdateWorkflowParamsResponse], error) {
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

	// Always signal the root workflow. Child workflows are inline goroutines
	// with synthetic DB IDs that Temporal doesn't know about.
	// Thread-scoped updates use the __thread key in the signal payload.
	workflowID := ""
	runID := ""
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		workflowID = *chat.WorkflowID
		if chat.RunID != nil {
			runID = *chat.RunID
		}
	} else {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no active workflow for chat"))
	}

	if err := validateWorkflowParamStructure(req.Msg.Params); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// If targeting a specific thread, verify it exists and is running
	if req.Msg.ThreadId != nil && *req.Msg.ThreadId != "" {
		wf, wfErr := s.database.GetWorkflowByThread(ctx, req.Msg.ChatId, *req.Msg.ThreadId)
		if wfErr != nil || wf == nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no workflow found for thread"))
		}
		if wf.Status != db.WorkflowStatusRunning {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("thread workflow is not running"))
		}
	}

	// Build state update through the same path as SendMessage —
	// this ensures schema defaults and validation are applied.
	workflowName := ""
	if chat.WorkflowName != nil {
		workflowName = *chat.WorkflowName
	}
	stateUpdate := s.buildStateUpdateForActiveWorkflow(ctx, userID, chat, workflowName, nil, req.Msg.Params)

	// Validate model selectors in the updated params before signaling the workflow
	if validationErrors := s.validateWorkflowInputs(ctx, workflowName, chat.ProjectID, stateUpdate); len(validationErrors) > 0 {
		errMsgs := make([]string, len(validationErrors))
		for i, e := range validationErrors {
			errMsgs[i] = e.Error()
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow input validation failed: %s", strings.Join(errMsgs, "; ")))
	}

	// Add thread scope to signal payload so the handler updates the correct thread's inputs
	if req.Msg.ThreadId != nil && *req.Msg.ThreadId != "" {
		stateUpdate["__thread"] = *req.Msg.ThreadId
	}

	// Signal the root workflow with the parameter updates
	err = s.tempClient.SignalWorkflow(
		ctx,
		workflowID,
		runID,
		"update_workflow_state",
		stateUpdate,
	)
	if err != nil {
		logging.Error("Failed to signal workflow for param update",
			"chatID", req.Msg.ChatId,
			"workflowID", workflowID,
			"threadID", req.Msg.ThreadId,
			"error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update workflow params"))
	}

	logging.Debug("Updated workflow params",
		"chatID", req.Msg.ChatId,
		"workflowID", workflowID,
		"threadID", req.Msg.ThreadId,
		"params", stateUpdate)

	return connect.NewResponse(&reliantv1.UpdateWorkflowParamsResponse{
		Success: true,
		Message: "Workflow parameters updated",
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

// GetWorkflowExecutions returns the workflow execution tree for a chat
func (s *ChatService) GetWorkflowExecutions(
	ctx context.Context,
	req *connect.Request[reliantv1.GetWorkflowExecutionsRequest],
) (*connect.Response[reliantv1.GetWorkflowExecutionsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	// Verify ownership
	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// Get all workflows for this chat
	workflows, err := s.database.ListWorkflowsByChat(ctx, req.Msg.ChatId)
	if err != nil {
		logging.Error("Failed to list workflows", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list workflows"))
	}

	if len(workflows) == 0 {
		return connect.NewResponse(&reliantv1.GetWorkflowExecutionsResponse{}), nil
	}

	// Get step executions for all workflows
	stepsByWorkflow := make(map[string][]*db.StepExecution)
	for _, wf := range workflows {
		steps, err := s.database.GetStepExecutionsByWorkflow(ctx, wf.ID)
		if err != nil {
			logging.Warn("Failed to get step executions", "error", err, "workflowID", wf.ID)
			continue
		}
		stepsByWorkflow[wf.ID] = steps
	}

	// Build workflow map for tree construction
	workflowMap := make(map[string]*db.Workflow)
	for _, wf := range workflows {
		workflowMap[wf.ID] = wf
	}

	// Find root workflows (no parent or parent is chat itself)
	var roots []*db.Workflow
	for _, wf := range workflows {
		if wf.ParentID == nil {
			roots = append(roots, wf)
		}
	}

	if len(roots) == 0 {
		return connect.NewResponse(&reliantv1.GetWorkflowExecutionsResponse{}), nil
	}

	// Sort roots by created_at descending (newest first)
	sort.Slice(roots, func(i, j int) bool {
		return roots[i].CreatedAt.After(roots[j].CreatedAt)
	})

	// Build tree for each root workflow
	allRootProtos := make([]*reliantv1.WorkflowExecution, 0, len(roots))
	for _, root := range roots {
		rootProto := s.buildWorkflowExecutionTree(root, workflows, stepsByWorkflow)
		allRootProtos = append(allRootProtos, rootProto)
	}

	// The most recent root is first (for backwards compat)
	var latestRootProto *reliantv1.WorkflowExecution
	if len(allRootProtos) > 0 {
		latestRootProto = allRootProtos[0]
	}

	return connect.NewResponse(&reliantv1.GetWorkflowExecutionsResponse{
		RootWorkflow:     latestRootProto,
		AllRootWorkflows: allRootProtos,
	}), nil
}

// GetThreadWorkflowInputs returns the workflow inputs for a specific thread.
// It looks up the workflow record for the thread, queries Temporal for current inputs,
// and falls back to empty inputs for completed workflows.
func (s *ChatService) GetThreadWorkflowInputs(
	ctx context.Context,
	req *connect.Request[reliantv1.GetThreadWorkflowInputsRequest],
) (*connect.Response[reliantv1.GetThreadWorkflowInputsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" || req.Msg.ThreadId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id and thread_id are required"))
	}

	// Verify chat ownership
	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	// Look up the workflow record for this thread
	wf, err := s.database.GetWorkflowByThread(ctx, req.Msg.ChatId, req.Msg.ThreadId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no workflow found for thread"))
	}
	if wf == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no workflow found for thread"))
	}

	isRunning := wf.Status == db.WorkflowStatusRunning

	// Resolve to root workflow ID for Temporal queries.
	// Child workflows are inline goroutines with synthetic DB IDs that Temporal doesn't know.
	// All queries must target the root workflow (the only real Temporal workflow).
	rootWorkflowID := ""
	rootRunID := ""
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		rootWorkflowID = *chat.WorkflowID
		if chat.RunID != nil {
			rootRunID = *chat.RunID
		}
	}

	// Try to query Temporal for thread-specific inputs (only works for running workflows)
	inputsMap := make(map[string]*structpb.Value)
	if isRunning && rootWorkflowID != "" {
		// Use get_thread_inputs query which returns the thread's subInputs map
		queryResp, err := s.tempClient.QueryWorkflow(ctx, rootWorkflowID, rootRunID, "get_thread_inputs", req.Msg.ThreadId)
		if err == nil {
			var currentInputs map[string]interface{}
			if err := queryResp.Get(&currentInputs); err == nil {
				// Convert to protobuf Values, filtering out runtime-injected inputs
				for key, value := range currentInputs {
					if workflow.RuntimeInjectedInputs[key] {
						continue
					}
					pbVal, err := structpb.NewValue(value)
					if err == nil {
						inputsMap[key] = pbVal
					}
				}
			}
		} else {
			logging.Debug("Failed to query thread inputs", "error", err, "rootWorkflowID", rootWorkflowID, "thread", req.Msg.ThreadId)
		}
	}

	return connect.NewResponse(&reliantv1.GetThreadWorkflowInputsResponse{
		WorkflowName: wf.WorkflowName,
		Inputs:       inputsMap,
		IsRunning:    isRunning,
	}), nil
}

// buildWorkflowExecutionTree recursively builds the workflow execution tree
func (s *ChatService) buildWorkflowExecutionTree(
	wf *db.Workflow,
	allWorkflows []*db.Workflow,
	stepsByWorkflow map[string][]*db.StepExecution,
) *reliantv1.WorkflowExecution {
	proto := &reliantv1.WorkflowExecution{
		Id:           wf.ID,
		WorkflowName: wf.WorkflowName,
		Thread:       wf.Thread,
		Status:       wf.Status,
		CreatedAt:    wf.CreatedAt.Format(time.RFC3339),
		MessageCount: 0, // TODO: count messages by thread
	}

	// Add thread statuses from static analysis
	proto.ThreadStatuses = s.getThreadStatusesForWorkflow(wf, stepsByWorkflow[wf.ID])

	if wf.ParentID != nil {
		proto.ParentId = wf.ParentID
	}
	if wf.SpawnedByNodeID != nil {
		proto.SpawnedByNodeId = wf.SpawnedByNodeID
	}
	// Populate ForkedFromThread, ParentThread, and ThreadTitle from Thread table (single source of truth)
	if thread, err := s.database.GetThread(context.Background(), wf.Thread); err == nil && thread != nil {
		if thread.ParentThreadID != nil {
			// ForkedFromThread: only for actual forks (have fork metadata)
			if thread.ForkAtOrdinal != nil {
				proto.ForkedFromThread = thread.ParentThreadID
			}
			// ParentThread: always set when parent exists (both fork and new)
			proto.ParentThread = thread.ParentThreadID
		}
		if thread.Title != nil {
			proto.ThreadTitle = thread.Title
		}
	}
	if wf.LoopIteration != nil {
		iteration := int32(*wf.LoopIteration)
		proto.Iteration = &iteration
	}
	if wf.CompletedAt != nil {
		completedAt := wf.CompletedAt.Format(time.RFC3339)
		proto.CompletedAt = &completedAt
	}

	// Add step executions (omit output_json to reduce response size — can be 8-14MB with full outputs)
	if steps, ok := stepsByWorkflow[wf.ID]; ok {
		proto.Steps = make([]*reliantv1.StepExecution, len(steps))
		for i, step := range steps {
			proto.Steps[i] = &reliantv1.StepExecution{
				Id:           step.ID,
				WorkflowId:   step.WorkflowID,
				StepId:       step.StepID,
				ActivityName: step.ActivityName,
				CreatedAt:    step.CreatedAt.Format(time.RFC3339),
			}
			// Only include a minimal output_json for save steps (need message_id for timeline)
			// Skip full output_json to avoid sending megabytes of step execution output
			if step.OutputJSON.Valid && strings.HasSuffix(step.StepID, "-save") {
				proto.Steps[i].OutputJson = step.OutputJSON.String
			}
			if step.ExitCode.Valid {
				exitCode := int32(step.ExitCode.Int64)
				proto.Steps[i].ExitCode = &exitCode
			}
			if step.Success.Valid {
				proto.Steps[i].Success = &step.Success.Bool
			}
			if step.DurationMs.Valid {
				proto.Steps[i].DurationMs = &step.DurationMs.Int64
			}
			if step.LoopNodeID.Valid {
				proto.Steps[i].LoopNodeId = &step.LoopNodeID.String
			}
			if step.LoopIteration.Valid {
				loopIter := int32(step.LoopIteration.Int64)
				proto.Steps[i].LoopIteration = &loopIter
			}
		}
	}

	// Find and add children
	for _, child := range allWorkflows {
		if child.ParentID != nil && *child.ParentID == wf.ID {
			childProto := s.buildWorkflowExecutionTree(child, allWorkflows, stepsByWorkflow)
			proto.Children = append(proto.Children, childProto)
		}
	}

	return proto
}

// getThreadStatusesForWorkflow returns simple thread status based on workflow running state.
// With the simplified thread model, all threads are active while workflow is running.
func (s *ChatService) getThreadStatusesForWorkflow(wf *db.Workflow, _ []*db.StepExecution) []*reliantv1.ThreadStatus {
	// Return simple root thread status
	// IsActive is true because this is only called for active workflows
	actualUUID := wf.Thread
	return []*reliantv1.ThreadStatus{
		{
			LogicalName: string(v2.ThreadRoot),
			IsActive:    true,
			ActualUuid:  &actualUUID,
		},
	}
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

// buildWorkflowInputs constructs the initial workflow input data by applying presets,
// user params, and workflow schema defaults. Order: presets (base), then user params (override),
// then apply workflow defaults for any missing inputs (e.g. model default from YAML).
func (s *ChatService) buildWorkflowInputs(
	ctx context.Context,
	userID string,
	projectPath string,
	projectID string,
	workflowName string,
	selectedPresets map[string]string,
	userParams map[string]*structpb.Value,
) map[string]interface{} {
	initialData := make(map[string]interface{})

	// Apply selected presets first (preset params are the base, user params override)
	userParamKeys := make([]string, 0, len(userParams))
	for k := range userParams {
		userParamKeys = append(userParamKeys, k)
	}
	logging.Info("[buildWorkflowInputs] Starting", "workflow", workflowName, "selectedPresets", selectedPresets, "userParamKeys", userParamKeys)
	if len(selectedPresets) > 0 {
		loadPreset := s.createDBPresetLoaderFull(ctx, userID, projectID)
		for groupName, presetName := range selectedPresets {
			if presetName == "" {
				continue
			}
			p, err := loadPreset(presetName)
			if err != nil {
				logging.Warn("Failed to load preset", "preset", presetName, "group", groupName, "error", err)
				continue
			}
			initialData = preset.ApplyToInputs(p, initialData, groupName)
			logging.Info("[buildWorkflowInputs] Applied preset", "preset", presetName, "group", groupName, "tools_after_preset", initialData["tools"])
		}
	} else {
		logging.Info("[buildWorkflowInputs] No presets selected")
	}

	// User-provided params override preset values.
	// Params must use nested structure (e.g., {"agent": {"model": "..."}}).
	// Flat keys like "agent.model" are rejected by validation.
	for key, value := range userParams {
		v := value.AsInterface()

		if shouldSkipEmptyPresetOverride(key, v) {
			logging.Info("[buildWorkflowInputs] Skipping empty override", "key", key)
			continue
		}

		if key == "tools" {
			logging.Info("[buildWorkflowInputs] User param tools override", "value", v, "type", fmt.Sprintf("%T", v))
		}

		// If value is a map, merge it with existing group map.
		if mapVal, ok := v.(map[string]interface{}); ok {
			if existing, ok := initialData[key].(map[string]interface{}); ok {
				for nestedKey, nestedValue := range mapVal {
					if shouldSkipEmptyPresetOverride(nestedKey, nestedValue) {
						continue
					}
					existing[nestedKey] = nestedValue
				}
			} else {
				filteredMap := make(map[string]interface{}, len(mapVal))
				for nestedKey, nestedValue := range mapVal {
					if shouldSkipEmptyPresetOverride(nestedKey, nestedValue) {
						continue
					}
					filteredMap[nestedKey] = nestedValue
				}
				initialData[key] = filteredMap
			}
		} else {
			initialData[key] = v
		}
	}

	logging.Info("[buildWorkflowInputs] After user params", "tools", initialData["tools"])

	// Apply workflow schema defaults (e.g. model: { id: gpt-4o }) so validation and execution
	// see the same inputs the workflow defines. Required inputs without defaults remain absent
	// so validateWorkflowInputs can reject missing required params before a chat starts.
	protoInputs := s.loadWorkflowInputsForBuild(ctx, userID, workflowName, projectID)
	if len(protoInputs) > 0 {
		initialData = v2.ApplyDefaults(initialData, protoInputs)
	}

	// Normalize model inputs: convert any remaining string model values to {id: string} objects.
	// This is the ONE place where string-to-object conversion happens — at the gRPC ingestion boundary.
	// Everything downstream rejects strings.
	if len(protoInputs) > 0 {
		normalizeModelInputs(initialData, protoInputs)
	}

	logging.Info("[buildWorkflowInputs] Final resolved", "tools", initialData["tools"])

	// Add project_path to workflow inputs so spawned workflows can load presets
	// This flows through: workflow.go -> StepExecutor -> executeSpawnInline -> InlineWorkflowExecutor
	if projectPath != "" {
		initialData["project_path"] = projectPath
	}

	return initialData
}

// loadWorkflowForBuild loads the workflow definition for buildWorkflowInputs (defaults application).
// Uses same order as chat validation: DB by slug, then project files. Returns nil if not found.

func shouldSkipEmptyPresetOverride(paramKey string, value interface{}) bool {
	switch paramKey {
	case "tools", "spawn_presets":
		listValue, ok := value.([]interface{})
		return ok && len(listValue) == 0
	default:
		return false
	}
}

func (s *ChatService) buildStateUpdateForActiveWorkflow(
	ctx context.Context,
	userID string,
	chat *db.Chat,
	workflowName string,
	requestPresets map[string]string,
	requestParams map[string]*structpb.Value,
) map[string]interface{} {
	if chat == nil {
		return map[string]interface{}{}
	}

	effectivePresets := make(map[string]string)
	for key, value := range chat.SelectedPresets {
		if value != "" {
			effectivePresets[key] = value
		}
	}
	for key, value := range requestPresets {
		if value != "" {
			effectivePresets[key] = value
		}
	}

	projectPath := s.getEffectiveWorkingPath(ctx, chat)
	return s.buildWorkflowInputs(ctx, userID, projectPath, chat.ProjectID, workflowName, effectivePresets, requestParams)
}

// loadWorkflowInputsForBuild loads workflow input schemas as proto types for ApplyDefaults.
// Uses the same resolution order as validation so builtin workflows also get defaults
// and boundary normalization for nested model selectors.
func (s *ChatService) loadWorkflowInputsForBuild(ctx context.Context, userID, workflowName, projectID string) map[string]*reliantv1.Input {
	if strings.HasPrefix(workflowName, "builtin://") {
		name := strings.TrimPrefix(workflowName, "builtin://")
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(name + ".yaml")
		if err != nil {
			logging.Warn("Could not load builtin workflow inputs for build", "workflow", workflowName, "error", err)
			return nil
		}
		wf, parseErr := wfyaml.ParseWorkflow(data)
		if parseErr != nil {
			logging.Warn("Could not parse builtin workflow inputs for build", "workflow", workflowName, "error", parseErr)
			return nil
		}
		return wf.GetInputs()
	}

	// Try DB draft first
	slug := strings.ToLower(strings.ReplaceAll(workflowName, " ", "-"))
	draft, err := s.database.GetWorkflowDraftBySlug(ctx, userID, slug)
	if err == nil && draft != nil {
		wf, parseErr := wfyaml.ParseWorkflow([]byte(draft.Definition))
		if parseErr == nil {
			return wf.GetInputs()
		}
	}

	// Try stored project config (synced by daemon)
	projectWf, _, lookupErr := loadProjectWorkflowBySlugFromDB(s.database, ctx, projectID, slug)
	if lookupErr == nil && projectWf != nil {
		return projectWf.GetInputs()
	}
	return nil
}

// validateWorkflowInputs loads a workflow and validates the provided inputs against its schema.
// Returns validation errors if inputs are missing or invalid, nil if valid.
// This enables early validation (400 error) before starting the workflow.
func (s *ChatService) validateWorkflowInputs(ctx context.Context, workflowName, projectID string, inputs map[string]interface{}) []error {
	userID := auth.MustGetUserID(ctx)

	// Load workflow definition to get input schemas (builtin/project config first, then user draft from DB)
	wf, err := s.loadWorkflowForValidation(ctx, workflowName, projectID)
	if err != nil {
		// User workflows often exist only in DB; load by slug so we validate the same definition that will run
		slug := strings.ToLower(strings.ReplaceAll(workflowName, " ", "-"))
		draft, dbErr := s.database.GetWorkflowDraftBySlug(ctx, userID, slug)
		if dbErr != nil || draft == nil {
			logging.Warn("Could not load workflow for input validation", "workflow", workflowName, "error", err)
			return nil
		}
		wf, err = v2.ParseWorkflowProtoBytesNoValidation([]byte(draft.Definition))
		if err != nil {
			logging.Warn("Could not parse draft for input validation", "workflow", workflowName, "error", err)
			return nil
		}
	}

	// Filter out runtime-injected inputs before validation
	// These are internal values that shouldn't be validated against the workflow schema
	filteredInputs := make(map[string]interface{})
	for key, value := range inputs {
		if !workflow.RuntimeInjectedInputs[key] {
			filteredInputs[key] = value
		}
	}

	// Apply explicit defaults before validation so optional schema defaults are included.
	// Required inputs without defaults intentionally remain absent and are rejected below.
	protoInputs := s.loadWorkflowInputsForBuild(ctx, userID, workflowName, projectID)
	inputsWithDefaults := v2.ApplyDefaults(filteredInputs, protoInputs)

	// Filter again after ApplyDefaults to ensure runtime-injected inputs aren't reintroduced
	// (ApplyDefaults should not add them, but this is a safety measure)
	finalInputs := make(map[string]interface{})
	for key, value := range inputsWithDefaults {
		if !workflow.RuntimeInjectedInputs[key] {
			finalInputs[key] = value
		}
	}

	// Normalize model inputs: convert any remaining string model values to {id: string} objects
	// before validation. This mirrors the normalization in buildWorkflowInputs.
	if protoInputs != nil {
		normalizeModelInputs(filteredInputs, protoInputs)
		normalizeModelInputs(finalInputs, protoInputs)
	}

	// Validate inputs against schema
	var errs []error
	if result := validation.ValidateInputs(wf, finalInputs); result.HasErrors() {
		errs = append(errs, result.AsError())
	}

	// Validate model availability - check that any model inputs can be resolved
	// with the user's configured API keys. If the selected model isn't available,
	// reject it with a clear error instead of silently substituting.
	modelSelectors := extractModelSelectors(finalInputs, wf.GetInputs(), "")
	for inputPath, selector := range modelSelectors {
		if err := drivers.ValidateModelSelector(ctx, userID, selector); err != nil {
			errs = append(errs, fmt.Errorf("input '%s': %w", inputPath, err))
		}
	}

	return errs
}

// normalizeModelInputs walks all model-type inputs using the schema and converts
// any remaining string values to model selector objects. Legacy "model@provider"
// strings are normalized to {id, providers} at this boundary.
// This is the single boundary conversion point — everything downstream expects objects.
func normalizeModelInputs(inputs map[string]interface{}, schemas map[string]*reliantv1.Input) {
	for name, schema := range schemas {
		if schema == nil {
			continue
		}

		switch model.GetInputType(schema) {
		case "model":
			if value, ok := inputs[name]; ok && value != nil {
				if s, ok := value.(string); ok {
					selector, normalized := normalizeLegacyModelSelectorString(s)
					if normalized != nil {
						inputs[name] = normalized
						logging.Info("[normalizeModelInputs] Converted string model to object", "input", name, "model", selector, "providers", normalized["providers"])
					}
				}
			}
		case "group":
			groupInputs := model.GetGroupInputs(schema)
			if groupInputs != nil {
				if groupValue, ok := inputs[name].(map[string]interface{}); ok {
					normalizeModelInputs(groupValue, groupInputs)
				}
			}
		}
	}
}

func normalizeLegacyModelSelectorString(raw string) (string, map[string]interface{}) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}

	selector := map[string]interface{}{}
	modelID := raw

	if at := strings.LastIndex(raw, "@"); at > 0 && at < len(raw)-1 {
		provider := strings.TrimSpace(raw[at+1:])
		candidateID := strings.TrimSpace(raw[:at])
		if provider != "" && candidateID != "" {
			modelID = candidateID
			selector["providers"] = []interface{}{provider}
		}
	}

	selector["id"] = modelID
	return modelID, selector
}

// extractModelSelectors recursively extracts model selector values from workflow inputs
// using proto V2Input schemas. Returns a map of input path -> selector value.
func extractModelSelectors(inputs map[string]interface{}, schemas map[string]*reliantv1.Input, prefix string) map[string]interface{} {
	result := make(map[string]interface{})

	for name, schema := range schemas {
		if schema == nil {
			continue
		}

		path := name
		if prefix != "" {
			path = prefix + "." + name
		}

		switch model.GetInputType(schema) {
		case "model":
			if value, ok := inputs[name]; ok && value != nil {
				result[path] = value
			}
		case "group":
			groupInputs := model.GetGroupInputs(schema)
			if groupInputs != nil {
				if groupValue, ok := inputs[name].(map[string]interface{}); ok {
					nestedSelectors := extractModelSelectors(groupValue, groupInputs, path)
					for k, v := range nestedSelectors {
						result[k] = v
					}
				}
			}
		}
	}

	return result
}

// loadWorkflowForValidation loads a workflow by name for input validation.
// Searches builtin workflows first, then stored project workflows from DB.
func (s *ChatService) loadWorkflowForValidation(ctx context.Context, workflowName, projectID string) (*reliantv1.Workflow, error) {
	// Handle builtin:// protocol
	if strings.HasPrefix(workflowName, "builtin://") {
		name := strings.TrimPrefix(workflowName, "builtin://")
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(name + ".yaml")
		if err != nil {
			return nil, fmt.Errorf("builtin workflow not found: %s", workflowName)
		}
		return v2.ParseWorkflowProtoBytesNoValidation(data)
	}

	// Load from stored project config (synced by daemon)
	// Normalize slug the same way as generateWorkflowSlug in load_workflow.go.
	slug := normalizeWorkflowSlug(workflowName)
	projectWf, _, err := loadProjectWorkflowBySlugFromDB(s.database, ctx, projectID, slug)
	if err == nil && projectWf != nil {
		return projectWf, nil
	}

	return nil, fmt.Errorf("workflow not found: %s", workflowName)
}

func (s *ChatService) loadCreateChatWorkflowForValidation(ctx context.Context, userID, workflowName, projectID string) (*reliantv1.Workflow, error) {
	if strings.HasPrefix(workflowName, "builtin://") {
		return s.loadWorkflowForValidation(ctx, workflowName, projectID)
	}

	slug := normalizeWorkflowSlug(workflowName)
	draft, err := s.database.GetUsableWorkflowBySlug(ctx, userID, slug)
	if err != nil {
		return nil, fmt.Errorf("failed to look up workflow '%s': %w", workflowName, err)
	}
	if draft != nil {
		wf, parseErr := wfyaml.ParseWorkflow([]byte(draft.Definition))
		if parseErr != nil {
			return nil, fmt.Errorf("failed to parse workflow '%s': %w", workflowName, parseErr)
		}
		return wf, nil
	}

	wf, err := s.loadWorkflowForValidation(ctx, workflowName, projectID)
	if err != nil {
		return nil, fmt.Errorf("workflow '%s' not found", workflowName)
	}
	return wf, nil
}

func (s *ChatService) createChatWorkflowLoader(ctx context.Context, userID, projectID string) validation.WorkflowLoader {
	return func(workflowName string) (*reliantv1.Workflow, error) {
		return s.loadCreateChatWorkflowForValidation(ctx, userID, workflowName, projectID)
	}
}

func (s *ChatService) validateCreateChatWorkflowTree(ctx context.Context, userID, workflowName, projectID string) error {
	wf, err := s.loadCreateChatWorkflowForValidation(ctx, userID, workflowName, projectID)
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}

	validationOpts := &validation.ValidationOptions{
		WorkflowLoader:       s.createChatWorkflowLoader(ctx, userID, projectID),
		CanonicalWorkflowRef: workflowName,
	}
	if projectID != "" {
		validationOpts.PresetLoader = s.createDBPresetLoader(ctx, projectID)
	}

	result := validation.StaticAnalysisWithOptions(wf, validationOpts)
	if result != nil && result.HasErrors() {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("workflow tree validation failed for '%s': %s", workflowName, result.Error()))
	}

	return nil
}

// createDBPresetLoader returns a PresetLoader that reads presets from project/builtin sources.
// Validation does not currently have user context, so DB-backed user presets are resolved at runtime only.
func (s *ChatService) createDBPresetLoader(ctx context.Context, projectID string) validation.PresetLoader {
	return func(presetName string) (map[string]interface{}, error) {
		// Pass empty userID so validation behavior stays scoped to project/builtin presets.
		p, err := s.loadPresetFromDB(ctx, "", projectID, presetName)
		if err != nil {
			return nil, err
		}
		return p.Params, nil
	}
}

// createDBPresetLoaderFull returns a function that loads full preset objects from all runtime sources.
// Used by buildWorkflowInputs where the full preset is needed for ApplyToInputs.
func (s *ChatService) createDBPresetLoaderFull(ctx context.Context, userID, projectID string) func(name string) (*preset.Preset, error) {
	return func(name string) (*preset.Preset, error) {
		return s.loadPresetFromDB(ctx, userID, projectID, name)
	}
}

func dbPresetToRuntimePreset(p *db.Preset) *preset.Preset {
	description := ""
	if p.Description != nil {
		description = *p.Description
	}

	result := &preset.Preset{
		Name:        p.Name,
		Description: description,
		Tag:         p.Tag,
		Params:      p.Params,
		Source:      "user",
	}
	// Normalize model params: convert any legacy string model values to {id: string} objects.
	preset.NormalizeModelParams(result)
	return result
}

// loadPresetFromDB loads a preset by name. Priority: user presets > stored project presets > builtins.
func (s *ChatService) loadPresetFromDB(ctx context.Context, userID, projectID, name string) (*preset.Preset, error) {
	slug := strings.ToLower(strings.ReplaceAll(name, " ", "-"))

	if s.database != nil {
		// Try project-scoped user preset first.
		if projectID != "" {
			dbPreset, err := s.database.GetPresetBySlugAndProject(ctx, userID, slug, projectID)
			if err == nil && dbPreset != nil {
				return dbPresetToRuntimePreset(dbPreset), nil
			}
		}

		// Fall back to global user preset.
		dbPreset, err := s.database.GetPresetBySlug(ctx, userID, slug)
		if err == nil && dbPreset != nil {
			return dbPresetToRuntimePreset(dbPreset), nil
		}

		// Try stored project presets from daemon config sync.
		if projectID != "" {
			record, err := s.database.GetProjectConfigRecord(ctx, projectID)
			if err == nil {
				presets, err := cfg.ParseStoredPresets(record.ProjectPresetsJSON)
				if err == nil {
					sp := cfg.FindStoredPresetByName(presets, name)
					if sp != nil {
						p, err := preset.ParsePreset([]byte(sp.YAMLContent), name)
						if err == nil {
							p.Source = "project"
							return p, nil
						}
					}
				}
			}
		}
	}

	// Fall back to builtin presets.
	builtinPath := "presets/" + name + ".yaml"
	data, err := builtin.BuiltinPresetsFS.ReadFile(builtinPath)
	if err == nil {
		p, err := preset.ParsePreset(data, name)
		if err == nil {
			p.Source = "builtin"
			return p, nil
		}
	}

	return nil, fmt.Errorf("preset not found: %s", name)
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

// injectSessionDaemonID adds the session's active daemon to workflow inputs.
// This is a runtime-injected input that flows through to daemon resolution.
func injectSessionDaemonID(inputs map[string]interface{}, chat *db.Chat) {
	if chat != nil && chat.ActiveDaemonID != nil && *chat.ActiveDaemonID != "" {
		inputs["session_daemon_id"] = *chat.ActiveDaemonID
	}
}

// normalizeWorkflowSlug produces a URL-safe slug from a workflow name.
// This MUST stay in sync with generateWorkflowSlug in
// internal/workflow/runtime/activities/handlers/load_workflow.go.
func normalizeWorkflowSlug(name string) string {
	slug := strings.ToLower(strings.TrimSpace(name))
	slug = strings.ReplaceAll(slug, " ", "-")
	slug = strings.ReplaceAll(slug, "_", "-")
	return slug
}

// handleDiscussMode handles the discuss mode: a lightweight LLM call while the workflow stays paused.
// It saves the user message, streams an LLM response, saves the assistant response, and returns
// with the workflow status still Paused.
func (s *ChatService) handleDiscussMode(
	ctx context.Context,
	req *connect.Request[reliantv1.SendMessageRequest],
	chat *db.Chat,
	existingWorkflow *db.Workflow,
	workflowID string,
	userID string,
	userContent string,
	hasUserContent bool,
	systemMessages []*reliantv1.InputMessage,
) (*connect.Response[reliantv1.SendMessageResponse], error) {
	// Guard against concurrent discuss calls for the same chat
	if _, loaded := s.discussLocks.LoadOrStore(req.Msg.ChatId, true); loaded {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("a discussion is already in progress for this chat"))
	}
	defer s.discussLocks.Delete(req.Msg.ChatId)

	targetThread := existingWorkflow.Thread
	if req.Msg.TargetThread != nil && *req.Msg.TargetThread != "" {
		targetThread = *req.Msg.TargetThread
	}

	// 1. Save user message to the workflow's thread
	var savedMessageID string
	err := s.database.RunTx(ctx, func(txCtx context.Context) error {
		for _, sysMsg := range systemMessages {
			_, err := s.database.SaveMessageToThread(txCtx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), sysMsg.Content, &workflowID, nil, displayStyleProtoToInt32Ptr(sysMsg.DisplayStyle))
			if err != nil {
				return fmt.Errorf("failed to save system message: %w", err)
			}
		}
		if hasUserContent || len(req.Msg.Attachments) > 0 {
			savedMsg, err := s.database.SaveMessageToThread(txCtx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), userContent, &workflowID, req.Msg.Attachments, nil)
			if err != nil {
				return fmt.Errorf("failed to save user message: %w", err)
			}
			savedMessageID = savedMsg.ID
		}
		return nil
	})
	if err != nil {
		logging.Error("Failed to save discuss mode messages", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// 2. Load conversation history from the thread
	history, err := handlers.LoadMessagesForLLM(ctx, s.database, req.Msg.ChatId, targetThread, nil)
	if err != nil {
		logging.Error("Failed to load conversation history for discuss mode", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load conversation history: %w", err))
	}

	// 3. Resolve model and create LLM driver
	driver, err := s.resolveDiscussDriver(ctx, userID, chat)
	if err != nil {
		logging.Error("Failed to resolve LLM driver for discuss mode", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resolve LLM driver: %w", err))
	}

	// 4. Stream LLM response (no tools)
	prompts := []string{
		"You are a helpful assistant. The user has paused their workflow and wants to discuss something. " +
			"Help them think through their question. You do not have access to any tools. " +
			"Keep your responses concise and helpful.",
	}

	eventCh := driver.StreamResponse(ctx, prompts, history, []tools.Tool{})

	var fullContent string
	blockIndex := 0
	blockStarted := false
	var streamErr error

	for event := range eventCh {
		switch event.Type {
		case llm.EventContentStart:
			blockStarted = true
			if s.streamingHub != nil {
				s.streamingHub.Publish(ctx, req.Msg.ChatId, streaming.StreamingDelta{
					DeltaType:  streaming.DeltaTypeContentBlockStart,
					BlockIndex: blockIndex,
					BlockType:  "text",
					Thread:     targetThread,
				})
			}

		case llm.EventContentDelta:
			if !blockStarted {
				blockStarted = true
				if s.streamingHub != nil {
					s.streamingHub.Publish(ctx, req.Msg.ChatId, streaming.StreamingDelta{
						DeltaType:  streaming.DeltaTypeContentBlockStart,
						BlockIndex: blockIndex,
						BlockType:  "text",
						Thread:     targetThread,
					})
				}
			}
			fullContent += event.Content
			if s.streamingHub != nil {
				s.streamingHub.Publish(ctx, req.Msg.ChatId, streaming.StreamingDelta{
					DeltaType:  streaming.DeltaTypeContentBlockDelta,
					BlockIndex: blockIndex,
					Delta:      event.Content,
					Thread:     targetThread,
				})
			}

		case llm.EventContentStop:
			blockIndex++
			blockStarted = false

		case llm.EventComplete:
			if event.Response != nil && event.Response.Content != "" && fullContent == "" {
				fullContent = event.Response.Content
			}

		case llm.EventError:
			if event.Error != nil {
				logging.Error("Discuss mode LLM error", "error", event.Error, "chatID", req.Msg.ChatId)
				streamErr = event.Error
			}
		}
	}

	// Emit stream_cancelled delta on error so the frontend knows streaming ended
	if streamErr != nil && s.streamingHub != nil {
		s.streamingHub.Publish(ctx, req.Msg.ChatId, streaming.StreamingDelta{
			DeltaType: streaming.DeltaTypeStreamCancelled,
			Thread:    targetThread,
		})
	}

	// 5. Save assistant response message
	if fullContent != "" {
		_, err := s.database.SaveMessageToThread(ctx, req.Msg.ChatId, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT), fullContent, &workflowID, nil, nil)
		if err != nil {
			logging.Error("Failed to save discuss mode assistant response", "error", err, "chatID", req.Msg.ChatId)
			// Non-fatal: the user already saw the streamed response
		}
	}

	// 6. Return with workflow_status still Paused
	workflowStatus := fmt.Sprintf("%d", db.WorkflowStatusPaused)
	return connect.NewResponse(&reliantv1.SendMessageResponse{
		ChatId:         req.Msg.ChatId,
		WorkflowId:     workflowID,
		Status:         "discuss",
		WorkflowStatus: &workflowStatus,
		MessageId:      savedMessageID,
	}), nil
}

// resolveDiscussDriver resolves an LLM driver for discuss mode by extracting the model
// from the chat's selected presets, falling back to a sensible default.
func (s *ChatService) resolveDiscussDriver(ctx context.Context, userID string, chat *db.Chat) (llm.Driver, error) {
	var modelID string

	// Try to get model from the chat's selected presets
	for _, presetName := range chat.SelectedPresets {
		if presetName == "" {
			continue
		}
		p, err := s.loadPresetFromDB(ctx, userID, chat.ProjectID, presetName)
		if err != nil {
			continue
		}
		if modelRaw, ok := p.Params["model"]; ok {
			if modelMap, ok := modelRaw.(map[string]interface{}); ok {
				if id, ok := modelMap["id"].(string); ok && id != "" {
					modelID = id
					break
				}
			}
		}
	}

	// Fall back to a sensible default model
	if modelID == "" {
		modelID = string(models.Claude45Sonnet)
	}

	preferences := models.Preferences{
		{ModelID: models.ModelID(modelID)},
	}

	return drivers.GetDriver(ctx, userID, preferences)
}
