// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow"
)

// YieldService implements the YieldService RPC handlers
type YieldService struct {
	reliantv1connect.UnimplementedYieldServiceHandler
	database     db.Repository
	pauseService *workflow.PauseService
}

// NewYieldService creates a new YieldService
func NewYieldService(database db.Repository, pauseService *workflow.PauseService) *YieldService {
	return &YieldService{
		database:     database,
		pauseService: pauseService,
	}
}

// ResolveYield resolves a pending yield (e.g., user clicks "Continue")
func (s *YieldService) ResolveYield(
	ctx context.Context,
	req *connect.Request[reliantv1.ResolveYieldRequest],
) (*connect.Response[reliantv1.ResolveYieldResponse], error) {
	if req.Msg.YieldId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("yield_id is required"))
	}
	if req.Msg.Action == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("action is required"))
	}

	// Look up yield by ID
	yield, err := s.database.GetYieldByID(ctx, req.Msg.YieldId)
	if err != nil {
		logging.Error("Failed to get yield", "error", err, "yieldID", req.Msg.YieldId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get yield"))
	}
	if yield == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("yield not found"))
	}

	// Verify status is pending
	if yield.Status != db.YieldStatusPending {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("yield already resolved"))
	}

	// Resolve yield in DB and emit resolved event for frontend
	if err := s.database.ResolveYield(ctx, req.Msg.YieldId, req.Msg.Action); err != nil {
		logging.Error("Failed to resolve yield", "error", err, "yieldID", req.Msg.YieldId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resolve yield"))
	}
	yieldResolvedUpdate := db.YieldUpdate{
		YieldID:    yield.ID,
		ChatID:     yield.ChatID,
		WorkflowID: yield.WorkflowID,
		StepID:     yield.StepID,
		Status:     "resolved",
	}
	if yield.Metadata != nil {
		yieldResolvedUpdate.Metadata = *yield.Metadata
	}
	if err := s.database.EmitYieldUpdate(ctx, yield.ChatID, yieldResolvedUpdate); err != nil {
		logging.Warn("Failed to emit yield resolved update", "error", err, "yieldID", yield.ID)
	}

	// Save a user message to the chat timeline for ask_user yields
	if req.Msg.Action == "reply" && req.Msg.ResponseData != nil && *req.Msg.ResponseData != "" && yield.Metadata != nil {
		var meta struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(*yield.Metadata), &meta); err == nil && meta.Type == "ask_user" {
			var respData struct {
				Answers []struct {
					Question string   `json:"question"`
					Selected []string `json:"selected"`
					Freetext string   `json:"freetext"`
				} `json:"answers"`
			}
			if err := json.Unmarshal([]byte(*req.Msg.ResponseData), &respData); err == nil && len(respData.Answers) > 0 {
				var parts []string
				for _, a := range respData.Answers {
					line := fmt.Sprintf("**Q: %s**\nA: %s", a.Question, strings.Join(a.Selected, ", "))
					if a.Freetext != "" {
						line += fmt.Sprintf(" (note: %s)", a.Freetext)
					}
					parts = append(parts, line)
				}
				formattedMessage := strings.Join(parts, "\n\n")

				targetThread := yield.WorkflowID
				if yield.ThreadID != "" {
					targetThread = yield.ThreadID
				}

				if _, err := s.database.SaveMessageToThread(ctx, yield.ChatID, targetThread, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), formattedMessage, &yield.WorkflowID, nil, nil); err != nil {
					logging.Warn("Failed to save ask_user reply message", "error", err, "yieldID", yield.ID, "chatID", yield.ChatID)
				}
			}
		}
	}

	// Signal the workflow to unblock from its yield wait.
	// Uses SignalWithRecovery to handle expired workflows (reset + re-signal).
	// Use TemporalWorkflowID for signal routing — for inline spawns, WorkflowID is a
	// logical child ID that doesn't correspond to any Temporal execution.
	if s.pauseService != nil {
		signalData := map[string]interface{}{"action": req.Msg.Action}
		if req.Msg.ResponseData != nil {
			signalData["response_data"] = *req.Msg.ResponseData
		}
		signalName := "signal.yield." + yield.ID
		signalTarget := yield.TemporalWorkflowID
		if signalTarget == "" {
			signalTarget = yield.WorkflowID // fallback for old yield records without temporal_workflow_id
		}
		if err := s.pauseService.SignalWithRecovery(ctx, signalTarget, signalName, signalData); err != nil {
			logging.Warn("Failed to signal yield resolution",
				"error", err,
				"yieldID", yield.ID,
				"workflowID", yield.WorkflowID,
				"temporalWorkflowID", signalTarget,
			)
			// Don't fail the RPC — yield is resolved in DB, worst case workflow hits timer
		}
	}

	return connect.NewResponse(&reliantv1.ResolveYieldResponse{
		Success: true,
	}), nil
}

// GetPendingYield gets the current pending yield for a chat
func (s *YieldService) GetPendingYield(
	ctx context.Context,
	req *connect.Request[reliantv1.GetPendingYieldRequest],
) (*connect.Response[reliantv1.GetPendingYieldResponse], error) {
	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}

	yield, err := s.database.GetPendingYieldByChatID(ctx, req.Msg.ChatId)
	if err != nil {
		logging.Error("Failed to get pending yield", "error", err, "chatID", req.Msg.ChatId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get pending yield"))
	}

	resp := &reliantv1.GetPendingYieldResponse{}
	if yield != nil {
		resp.Yield = &reliantv1.YieldInfo{
			YieldId:    yield.ID,
			ChatId:     yield.ChatID,
			WorkflowId: yield.WorkflowID,
			StepId:     yield.StepID,
			Status:     yield.Status,
			CreatedAt:  yield.CreatedAt.Format(time.RFC3339),
			Metadata:   yield.Metadata,
		}
	}

	return connect.NewResponse(resp), nil
}
