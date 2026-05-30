// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/model"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

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
	if jwt, ok := auth.GetUserJWT(userID); ok {
		execContext.UserJWT = jwt
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
				logging.Info("Expired workflow detected, attempting reset-based resume",
					"workflowID", workflowID,
					"chatID", req.Msg.ChatId,
				)

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
	if jwt, ok := auth.GetUserJWT(userID); ok {
		execContext.UserJWT = jwt
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
