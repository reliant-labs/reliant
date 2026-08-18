// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"go.temporal.io/sdk/activity"
)

// ============================================================================
// TYPES (strongly typed inputs/outputs)
// ============================================================================

// QuestionCreateInput is the input for the QuestionCreate activity.
type QuestionCreateInput struct {
	ChatID             string  `json:"chat_id" reliant:"-"`
	WorkflowID         string  `json:"workflow_id" reliant:"-"`
	TemporalWorkflowID string  `json:"temporal_workflow_id" reliant:"-"`
	ThreadID           string  `json:"thread_id" reliant:"-"`
	StepID             string  `json:"step_id" reliant:"-"`
	LoopNodeID         string  `json:"loop_node_id,omitempty" reliant:"-"`
	LoopIteration      int     `json:"loop_iteration" reliant:"-"`
	Metadata           *string `json:"metadata,omitempty" reliant:"-"`
	ToolCallID         string  `json:"tool_call_id,omitempty" reliant:"-"`
}

// QuestionCreateOutput is the output from the QuestionCreate activity.
type QuestionCreateOutput struct {
	QuestionID      string `json:"question_id"`
	AlreadyResolved bool   `json:"already_resolved"`
	ResponseData    string `json:"response_data"`
}

// QuestionResolveInput is the input for the QuestionResolve activity.
type QuestionResolveInput struct {
	QuestionID   string `json:"question_id" reliant:"-"`
	ResponseData string `json:"response_data" reliant:"-"`
}

// QuestionResolveOutput is the output from the QuestionResolve activity.
type QuestionResolveOutput struct {
	Success bool `json:"success"`
}

// ============================================================================
// ACTIVITY: QuestionCreateActivity
// ============================================================================

// QuestionCreateActivity creates a question record in the DB and returns immediately.
// The workflow then waits on a signal channel for resolution — no polling needed.
type QuestionCreateActivity struct {
	repo db.Repository
}

// NewQuestionCreateActivity creates a new QuestionCreateActivity
func NewQuestionCreateActivity(repo db.Repository) *QuestionCreateActivity {
	return &QuestionCreateActivity{repo: repo}
}

// Name returns the activity name for registration
func (a *QuestionCreateActivity) Name() string {
	return "QuestionCreate"
}

// DisplayName returns human-readable name for UI
func (a *QuestionCreateActivity) DisplayName() string {
	return "QuestionCreate"
}

// Description returns what the activity does
func (a *QuestionCreateActivity) Description() string {
	return "Create question record for user interaction"
}

// Category returns the activity category for UI grouping
func (a *QuestionCreateActivity) Category() schema.ActivityCategory {
	return schema.CategoryAgentic
}

// Execute creates the question record. If a question already exists for this
// workflow+step+iteration (idempotency), it returns the existing one. If the
// existing question is already resolved, AlreadyResolved=true is returned so
// the workflow can skip signal waiting.
func (a *QuestionCreateActivity) Execute(ctx context.Context, input QuestionCreateInput) (QuestionCreateOutput, error) {
	logger := activity.GetLogger(ctx)

	logger.Info("[QuestionCreate] Creating question record",
		"chatID", input.ChatID,
		"workflowID", input.WorkflowID,
		"stepID", input.StepID,
		"loopIteration", input.LoopIteration)

	// IDEMPOTENCY: Check if we already created a question for this workflow+step+iteration.
	existingQuestions, err := a.repo.GetQuestionsByWorkflowStepIteration(ctx, input.WorkflowID, input.StepID, input.LoopIteration)
	if err == nil && len(existingQuestions) > 0 {
		// If tool_call_id is set, find the matching question
		var match *db.Question
		if input.ToolCallID != "" {
			for _, q := range existingQuestions {
				if containsToolCallID(q.Metadata, input.ToolCallID) {
					match = q
					break
				}
			}
		} else {
			match = existingQuestions[0]
		}

		if match != nil {
			logger.Info("[QuestionCreate] Found existing question",
				"questionID", match.ID,
				"status", match.Status)

			if match.Status == db.QuestionStatusResolved {
				responseData := ""
				if match.ResponseData != nil {
					responseData = *match.ResponseData
				}
				return QuestionCreateOutput{
					QuestionID:      match.ID,
					AlreadyResolved: true,
					ResponseData:    responseData,
				}, nil
			}

			// Still pending — return question ID so workflow waits on signal
			return QuestionCreateOutput{
				QuestionID:      match.ID,
				AlreadyResolved: false,
			}, nil
		}
	}

	// Generate question ID and create record
	questionID := uuid.New().String()

	// Resolve temporal workflow ID: use provided value, fall back to WorkflowID
	temporalWorkflowID := input.TemporalWorkflowID
	if temporalWorkflowID == "" {
		temporalWorkflowID = input.WorkflowID
	}

	// Build the question record
	var loopNodeID *string
	if input.LoopNodeID != "" {
		loopNodeID = &input.LoopNodeID
	}
	loopIteration := input.LoopIteration

	var toolCallID *string
	if input.ToolCallID != "" {
		toolCallID = &input.ToolCallID
	}

	question := &db.Question{
		ID:                 questionID,
		ChatID:             input.ChatID,
		WorkflowID:         input.WorkflowID,
		TemporalWorkflowID: temporalWorkflowID,
		ThreadID:           input.ThreadID,
		StepID:             input.StepID,
		LoopNodeID:         loopNodeID,
		LoopIteration:      &loopIteration,
		Status:             db.QuestionStatusPending,
		Metadata:           input.Metadata,
		ToolCallID:         toolCallID,
		CreatedAt:          time.Now().UTC(),
	}

	// Dual-write: create question record + mark chat unread + emit update atomically
	err = a.repo.RunTx(ctx, func(txCtx context.Context) error {
		if err := a.repo.CreateQuestion(txCtx, question); err != nil {
			return fmt.Errorf("failed to create question: %w", err)
		}

		// Mark chat as unread so UI shows notification badge
		if err := a.repo.UpdateChatUnread(txCtx, input.ChatID, true, "question_pending"); err != nil {
			logger.Warn("[QuestionCreate] Failed to mark chat as unread",
				"error", err,
				"chatID", input.ChatID)
		}

		// Emit question update (include metadata so frontend can render the question)
		metadata := ""
		if input.Metadata != nil {
			metadata = *input.Metadata
		}
		logger.Info("[QuestionCreate] Emitting question update",
			"questionID", questionID,
			"chatID", input.ChatID,
			"metadataLen", len(metadata),
			"metadataPreview", truncateForLog(metadata, 200),
		)
		if err := a.repo.EmitQuestionUpdate(txCtx, input.ChatID, db.QuestionUpdate{
			QuestionID: questionID,
			ChatID:     input.ChatID,
			WorkflowID: input.WorkflowID,
			ThreadID:   input.ThreadID,
			StepID:     input.StepID,
			Status:     "pending",
			Metadata:   metadata,
		}); err != nil {
			return fmt.Errorf("failed to emit question update: %w", err)
		}

		return nil
	})
	if err != nil {
		return QuestionCreateOutput{}, err
	}

	logger.Info("[QuestionCreate] Question created successfully",
		"questionID", questionID,
		"chatID", input.ChatID)

	return QuestionCreateOutput{
		QuestionID:      questionID,
		AlreadyResolved: false,
	}, nil
}

// ============================================================================
// ACTIVITY: QuestionResolveActivity
// ============================================================================

// QuestionResolveActivity resolves a question record in the DB. Used by the workflow
// when the signal timer expires (timeout case).
type QuestionResolveActivity struct {
	repo db.Repository
}

// NewQuestionResolveActivity creates a new QuestionResolveActivity
func NewQuestionResolveActivity(repo db.Repository) *QuestionResolveActivity {
	return &QuestionResolveActivity{repo: repo}
}

// Name returns the activity name for registration
func (a *QuestionResolveActivity) Name() string {
	return "QuestionResolve"
}

// DisplayName returns human-readable name for UI
func (a *QuestionResolveActivity) DisplayName() string {
	return "QuestionResolve"
}

// Description returns what the activity does
func (a *QuestionResolveActivity) Description() string {
	return "Resolve a question record in the database (timeout cleanup)"
}

// Category returns the activity category for UI grouping
func (a *QuestionResolveActivity) Category() schema.ActivityCategory {
	return schema.CategoryAgentic
}

// Execute resolves the question record with the given response data.
func (a *QuestionResolveActivity) Execute(ctx context.Context, input QuestionResolveInput) (QuestionResolveOutput, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("[QuestionResolve] Resolving question",
		"questionID", input.QuestionID)

	var responseData *string
	if input.ResponseData != "" {
		responseData = &input.ResponseData
	}

	// Read the row before resolving: the emit below needs its chat/thread/step
	// scoping, and after ResolveQuestion the lookup would still work but the
	// ordering keeps the emit honest about what was actually closed.
	question, err := a.repo.GetQuestionByID(ctx, input.QuestionID)
	if err != nil {
		logger.Warn("[QuestionResolve] Failed to load question for update emission",
			"questionID", input.QuestionID, "error", err)
	}

	if err := a.repo.ResolveQuestion(ctx, input.QuestionID, responseData); err != nil {
		return QuestionResolveOutput{}, fmt.Errorf("failed to resolve question: %w", err)
	}

	// A timed-out question is closed as far as the user is concerned, so the
	// feed must say so. Without this the last question update stays "pending"
	// forever and every later open of the chat replays that gate — the question
	// reappears even though nothing is waiting on it. Best-effort: the DB row
	// is already resolved, which is what actually unblocks the run.
	if question != nil {
		if err := a.repo.EmitQuestionUpdate(ctx, question.ChatID, db.QuestionUpdate{
			QuestionID: question.ID,
			ChatID:     question.ChatID,
			WorkflowID: question.WorkflowID,
			ThreadID:   question.ThreadID,
			StepID:     question.StepID,
			Status:     "resolved",
		}); err != nil {
			logger.Warn("[QuestionResolve] Failed to emit question resolved update",
				"questionID", input.QuestionID, "error", err)
		}
	}

	return QuestionResolveOutput{Success: true}, nil
}

// ============================================================================
// HELPERS
// ============================================================================

// containsToolCallID checks if the metadata JSON contains a matching tool_call_id.
func containsToolCallID(metadata *string, toolCallID string) bool {
	if metadata == nil || toolCallID == "" {
		return false
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(*metadata), &m); err != nil {
		return false
	}
	if id, ok := m["tool_call_id"].(string); ok {
		return id == toolCallID
	}
	return false
}

func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
