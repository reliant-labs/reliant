// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
)

// activeWorkflowNameForResume returns the workflow name to (re)start for an
// EXISTING conversation whose prior run is being resumed or resurrected.
//
// The db.Workflow ROW name can go stale after a transition_to handoff: on
// completion, graduate.go (TransitionChatOnCompletion) permanently switches
// chat.WorkflowName to the transition target, but it cannot rewrite the
// finished ROW's name because UpdateWorkflowName is only permitted while the
// workflow is pending. The chat is therefore the source of truth for the
// conversation's CURRENT workflow.
//
// Preferring chat.WorkflowName ensures a resume/ghost-recovery restarts the
// workflow the conversation actually moved to (e.g. builtin://agent), NOT the
// stale one-shot pipeline recorded on the row (e.g. builtin://forge-one-shot),
// which would re-run the whole build against an already-built project. The
// normal fresh-start path already reads *chat.WorkflowName, so this keeps the
// resume path consistent with it. Falls back to the row name only when the
// chat carries none (defensive; WorkflowName is required at chat creation).
func activeWorkflowNameForResume(chat *db.Chat, existingWorkflow *db.Workflow) string {
	if chat != nil && chat.WorkflowName != nil && *chat.WorkflowName != "" {
		return *chat.WorkflowName
	}
	if existingWorkflow != nil {
		return existingWorkflow.WorkflowName
	}
	return ""
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
	// The root run's lifecycle: the web decides paused/pending from these, and
	// a pending state is what tells it to StartChat rather than SendMessage.
	proto.WorkflowState = workflowStateToProto(c.RootStatus.State)
	proto.WorkflowStopReason = workflowStopReasonToProto(c.RootStatus.StopReason)
	if c.LaunchKind != "" {
		proto.LaunchKind = &c.LaunchKind
	}
	if c.TriggerID != nil {
		proto.TriggerId = c.TriggerID
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

// launchErrorToConnect maps a launch error onto the wire code this service has
// always returned for that failure.
//
// internal/launch deliberately does not import connect — it runs on the worker
// too, where there is no request to answer — so the handler owns the mapping.
// The codes here are the ones chat creation returned before the start path moved,
// and several are asserted by tests.
func launchErrorToConnect(err error) error {
	if err == nil {
		return nil
	}

	var validationErr *launch.ValidationError
	if errors.As(err, &validationErr) {
		if validationErr.Kind == launch.ValidationFailedPrecondition {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New(validationErr.Reason))
		}
		return connect.NewError(connect.CodeInvalidArgument, errors.New(validationErr.Reason))
	}

	var notFoundErr *launch.NotFoundError
	if errors.As(err, &notFoundErr) {
		return connect.NewError(connect.CodeNotFound, errors.New(notFoundErr.Reason))
	}

	var alreadyErr *launch.AlreadyLaunchedError
	if errors.As(err, &alreadyErr) {
		return connect.NewError(connect.CodeAlreadyExists, errors.New(alreadyErr.Error()))
	}

	if errors.Is(err, launch.ErrNotPending) {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}

	// Internal failures keep the terse message on the wire and the detail in
	// the logs, exactly as the inline code did.
	var internalErr *launch.InternalError
	if errors.As(err, &internalErr) {
		return connect.NewError(connect.CodeInternal, errors.New(internalErr.Reason))
	}

	return connect.NewError(connect.CodeInternal, err)
}

// inputMessageFromSeed converts a launch seed message back into the wire type.
// SendMessage's own save path still speaks InputMessage, so a seed produced by
// launch (the greenfield guidance) has to come back across the boundary.
func inputMessageFromSeed(seed launch.SeedMessage) *reliantv1.InputMessage {
	return &reliantv1.InputMessage{
		Role:         seed.Role,
		Content:      seed.Content,
		DisplayStyle: seed.DisplayStyle,
	}
}

// seedMessagesFromInput converts wire InputMessages into launch seed messages.
// launch holds no proto request types, so the translation is the handler's.
func seedMessagesFromInput(messages []*reliantv1.InputMessage) []launch.SeedMessage {
	seeds := make([]launch.SeedMessage, 0, len(messages))
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		seeds = append(seeds, launch.SeedMessage{
			Role:         msg.Role,
			Content:      msg.Content,
			DisplayStyle: msg.DisplayStyle,
		})
	}
	return seeds
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
