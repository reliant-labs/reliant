// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/model"

	"go.temporal.io/api/enums/v1"
)

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
