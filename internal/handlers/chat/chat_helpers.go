// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/runstateproto"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
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

// requireChatOwner is the ownership check for an RPC that reads nothing of the
// chat past who owns it: the chats row alone, not the activity view
// getChatForUser reads (whose activity, workflow-state and working-thread
// columns are subqueries over the chat's messages, workflows and threads).
// A missing chat and another user's are the same core.ErrChatNotFound.
func (s *Service) requireChatOwner(ctx context.Context, chatID, userID string) error {
	_, err := s.deps.Repo.GetChatRowWithUserCheck(ctx, chatID, userID)
	return err
}

// getChatForUser fetches a chat, with its activity-derived fields, and
// verifies ownership in a single query. Where only ownership matters, use
// requireChatOwner, which skips the activity view.
func (s *Service) getChatForUser(ctx context.Context, chatID, userID string) (*db.Chat, error) {
	chat, err := s.deps.Repo.GetChatWithUserCheck(ctx, chatID, userID)
	if err != nil {
		return nil, err
	}
	return chat, nil
}

// paramsChangeQueryTimeout bounds checkParamsActuallyChanged's workflow query.
//
// A query on a running workflow is answered by the WORKER, after any workflow
// task it is processing for that run — and after replaying the run's history
// if it is not cached. A busy worker made this one advisory question cost a
// send 5+ seconds in prod (SendMessage 22:15:36, 9.2s, sampled in this query
// for 5s straight), and since SendMessage holds the chat's run-control lock
// through it, a second send to the same chat queued behind it. An idle,
// cached workflow answers in milliseconds.
const paramsChangeQueryTimeout = 500 * time.Millisecond

// checkParamsActuallyChanged queries the workflow for its current inputs and compares
// with the incoming params. Returns true only if at least one param value actually changed.
// This prevents the "params changed" message from being sent when params haven't changed.
//
// The answer only decides whether a hidden "params changed" note is saved; the
// params reach the run through update_workflow_state either way. So a query
// that cannot answer in paramsChangeQueryTimeout is treated like one that
// failed: assume changed, and never hold the user's send for it.
func (s *Service) checkParamsActuallyChanged(ctx context.Context, workflowID string, incomingParams map[string]interface{}) bool {
	queryCtx, cancel := context.WithTimeout(ctx, paramsChangeQueryTimeout)
	defer cancel()
	currentInputs, err := s.queryRunInputs(queryCtx, workflowID)
	if err != nil {
		// If query fails (e.g., workflow not running yet) or the worker is too
		// busy to answer in time, assume params changed to be safe
		logging.WarnContext(ctx, "Failed to query workflow inputs, assuming params changed", "error", err, "workflowID", workflowID,
			"timed_out", errors.Is(queryCtx.Err(), context.DeadlineExceeded))
		return true
	}
	return inputsDiffer(currentInputs, incomingParams)
}

// queryRunInputs reads the current inputs of workflowID's current run with its
// get_workflow_inputs query. A closed run is refused (rtemporal.ErrRunNotOpen),
// never replayed: see rtemporal.QueryOpenRun. The run the chat row names is
// not consulted — it goes stale at every continue-as-new, and the current run
// is the one update_workflow_state reaches anyway.
func (s *Service) queryRunInputs(ctx context.Context, workflowID string) (map[string]interface{}, error) {
	queryResp, err := rtemporal.QueryOpenRun(ctx, s.deps.Temporal, workflowID, "get_workflow_inputs")
	if err != nil {
		return nil, err
	}
	var currentInputs map[string]interface{}
	if err := queryResp.Get(&currentInputs); err != nil {
		return nil, fmt.Errorf("decode workflow inputs: %w", err)
	}
	return currentInputs, nil
}

// inputsDiffer reports whether any incoming input differs from the run's
// current value, or is one the run does not have.
//
// Both sides go through JSON serialization (Temporal stores as JSON, protobuf
// values are JSON-compatible), so normalizing to JSON bytes eliminates type
// mismatches (e.g., float64 vs int from JSON round-trip, structpb types vs
// native Go types).
func inputsDiffer(current, incoming map[string]interface{}) bool {
	for key, newValue := range incoming {
		currentValue, exists := current[key]
		if !exists {
			logging.Debug("Param change detected: new param", "key", key)
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
			logging.Debug("Param change detected: value changed", "key", key)
			return true
		}
	}
	logging.Debug("No actual param changes detected", "paramCount", len(incoming))
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
	proto.NoMachine = c.NoMachine
	proto.WorkingThreads = int32(c.WorkingThreads)
	if c.UsageLimitResumesAt != nil {
		resumes := c.UsageLimitResumesAt.UTC().Format(time.RFC3339)
		proto.UsageLimitResumesAt = &resumes
		proto.UsageLimitProbing = c.UsageLimitProbing
		if c.UsageLimitProvider != "" {
			provider := c.UsageLimitProvider
			proto.UsageLimitProvider = &provider
		}
	}
	// The root run's lifecycle: the web decides paused/pending from these, and
	// a pending state is what tells it to StartChat rather than SendMessage.
	proto.WorkflowState = runstateproto.State(c.RootStatus.State)
	proto.WorkflowStopReason = runstateproto.StopReason(c.RootStatus.StopReason)
	if c.LaunchKind != "" {
		proto.LaunchKind = &c.LaunchKind
	}
	if c.TriggerID != nil {
		proto.TriggerId = c.TriggerID
	}
	if c.AdoptedAt != nil {
		adopted := c.AdoptedAt.Format(time.RFC3339)
		proto.AdoptedAt = &adopted
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

// trackStartedChatMessage records the user message a chat start carries as a
// message_sent: StartChat is the only way a chat's first message reaches the
// server, and the web no longer reports it. The seed carries no stored message
// id, so the client's named id (or, failing that, the chat's) identifies it.
func (s *Service) trackStartedChatMessage(ctx context.Context, userID string, chat *db.Chat, clientMessageID string, msg *reliantv1.StartChatRequest) {
	if chat == nil {
		return
	}
	var content string
	for _, m := range msg.GetMessages() {
		if m.GetRole() == reliantv1.MessageRole_MESSAGE_ROLE_USER {
			content = m.GetContent()
			break
		}
	}
	if content == "" {
		return
	}
	messageID := clientMessageID
	if messageID == "" {
		messageID = chat.ID
	}
	go s.trackMessageSent(context.WithoutCancel(ctx), userID, chat, messageID, chat.MainThreadID(), content, len(msg.GetAttachments()))
}

func (s *Service) trackMessageSent(ctx context.Context, userID string, chat *db.Chat, messageID, threadID, userContent string, attachmentCount int) {
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
		count, err := s.deps.Repo.CountMessagesInThread(ctx, threadID)
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

	analyticsClient := analytics.ForUser(ctx, userID)
	analyticsClient.TrackMessageSent(metrics)

	// Check if this is the user's first-ever message across all chats
	if isFirstInChat {
		// List all chats for this user to see if they have any other chats with messages
		chats, err := s.deps.Repo.SearchChats(ctx, db.ChatSearchFilters{
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
