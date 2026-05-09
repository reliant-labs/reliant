// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
)

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

	activity := handlers.NewCallLLMActivity(s.database, s.streamingHub, nil, nil, nil, nil)
	reservation, err := activity.ReserveManagedReliantUsageForChat(ctx, chat, driver, history, prompts, s.controlPlaneClient)
	if err != nil {
		return nil, err
	}

	eventCh := driver.StreamResponse(ctx, prompts, history, []tools.Tool{})

	var fullContent string
	blockIndex := 0
	blockStarted := false
	var streamErr error
	var usage llm.TokenUsage

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
			if event.Response != nil {
				usage = event.Response.Usage
				if event.Response.Content != "" && fullContent == "" {
					fullContent = event.Response.Content
				}
			}

		case llm.EventError:
			if event.Error != nil {
				logging.Error("Discuss mode LLM error", "error", event.Error, "chatID", req.Msg.ChatId)
				streamErr = event.Error
			}
		}
	}

	activity.CompleteManagedReliantReservationForChat(ctx, chat, driver, usage, streamErr, reservation, s.controlPlaneClient)

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
