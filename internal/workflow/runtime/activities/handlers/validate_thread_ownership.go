// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"go.temporal.io/sdk/activity"
)

// ValidateThreadOwnershipInput is the input for validating thread ownership
type ValidateThreadOwnershipInput struct {
	ThreadID       string `json:"thread_id"`
	ExpectedChatID string `json:"expected_chat_id"`
}

// ValidateThreadOwnershipOutput contains the validation result
type ValidateThreadOwnershipOutput struct {
	Valid        bool   `json:"valid"`
	ErrorMessage string `json:"error_message,omitempty"`
}

// ValidateThreadOwnershipActivity validates that a thread belongs to the expected chat
type ValidateThreadOwnershipActivity struct {
	repo db.Repository
}

// NewValidateThreadOwnershipActivity creates a new ValidateThreadOwnershipActivity
func NewValidateThreadOwnershipActivity(repo db.Repository) *ValidateThreadOwnershipActivity {
	return &ValidateThreadOwnershipActivity{repo: repo}
}

// Name returns the activity name for registration
func (a *ValidateThreadOwnershipActivity) Name() string {
	return "ValidateThreadOwnership"
}

// DisplayName returns human-readable name for UI
func (a *ValidateThreadOwnershipActivity) DisplayName() string {
	return "Validate Thread Ownership"
}

// Description returns what the activity does
func (a *ValidateThreadOwnershipActivity) Description() string {
	return "Validates that a thread belongs to the expected chat (prevents cross-chat thread resumption)"
}

// Category returns the activity category for UI grouping
func (a *ValidateThreadOwnershipActivity) Category() schema.ActivityCategory {
	return schema.CategoryMessageProcessing
}

// Execute validates that a thread belongs to the expected chat
func (a *ValidateThreadOwnershipActivity) Execute(ctx context.Context, input ValidateThreadOwnershipInput) (ValidateThreadOwnershipOutput, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("[ValidateThreadOwnership] Validating thread ownership",
		"threadID", input.ThreadID,
		"expectedChatID", input.ExpectedChatID)

	// Look up the thread
	thread, err := a.repo.GetThread(ctx, input.ThreadID)
	if err != nil {
		logger.Error("[ValidateThreadOwnership] Failed to get thread",
			"threadID", input.ThreadID,
			"error", err)
		return ValidateThreadOwnershipOutput{
			Valid:        false,
			ErrorMessage: fmt.Sprintf("Thread not found: %s. The agent_id may be invalid or the thread may have been deleted.", input.ThreadID),
		}, nil
	}

	// Check if the thread belongs to the expected chat
	if thread.ChatID != input.ExpectedChatID {
		logger.Warn("[ValidateThreadOwnership] Thread belongs to different chat",
			"threadID", input.ThreadID,
			"threadChatID", thread.ChatID,
			"expectedChatID", input.ExpectedChatID)
		return ValidateThreadOwnershipOutput{
			Valid:        false,
			ErrorMessage: fmt.Sprintf("Cannot resume thread %s: this thread belongs to a different conversation. Threads cannot be resumed across chat branches. Please start a new spawn without agent_id to create a fresh thread in this conversation.", input.ThreadID),
		}, nil
	}

	logger.Info("[ValidateThreadOwnership] Thread ownership validated successfully",
		"threadID", input.ThreadID,
		"chatID", thread.ChatID)

	return ValidateThreadOwnershipOutput{
		Valid: true,
	}, nil
}
