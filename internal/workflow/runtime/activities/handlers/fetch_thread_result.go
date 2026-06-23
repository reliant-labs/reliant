// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"go.temporal.io/sdk/activity"
)

// FetchThreadResultInput is the input for fetching the final result from a child thread
type FetchThreadResultInput struct {
	ChatID string `json:"chat_id" reliant:"-"`
	Thread string `json:"thread"`
}

// FetchThreadResultOutput contains the final assistant response from a child thread
type FetchThreadResultOutput struct {
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}

// FetchThreadResultActivity fetches the final assistant message from a child thread
type FetchThreadResultActivity struct {
	repo    db.Repository
	threads *threads.Service
}

// NewFetchThreadResultActivity creates a new FetchThreadResultActivity
func NewFetchThreadResultActivity(repo db.Repository, threads *threads.Service) *FetchThreadResultActivity {
	return &FetchThreadResultActivity{repo: repo, threads: threads}
}

// Name returns the activity name for registration
func (a *FetchThreadResultActivity) Name() string {
	return "FetchThreadResult"
}

// DisplayName returns human-readable name for UI
func (a *FetchThreadResultActivity) DisplayName() string {
	return "Fetch Thread Result"
}

// Description returns what the activity does
func (a *FetchThreadResultActivity) Description() string {
	return "Fetch the final assistant response from a child thread"
}

// Category returns the activity category for UI grouping
func (a *FetchThreadResultActivity) Category() schema.ActivityCategory {
	return schema.CategoryMessageProcessing
}

// Execute fetches the final assistant response from a child thread
func (a *FetchThreadResultActivity) Execute(ctx context.Context, input FetchThreadResultInput) (FetchThreadResultOutput, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("[FetchThreadResult] Fetching result from child thread",
		"chatID", input.ChatID,
		"thread", input.Thread)

	// Get all messages visible in this thread (including inherited from parent forks)
	// Uses threads.Service for proper fork chain resolution
	messages, err := a.threads.LoadCurrentMessages(ctx, input.Thread)
	if err != nil {
		return FetchThreadResultOutput{
			Content: fmt.Sprintf("Failed to fetch thread messages: %v", err),
			IsError: true,
		}, nil
	}

	if len(messages) == 0 {
		return FetchThreadResultOutput{
			Content: "No messages found in child thread",
			IsError: true,
		}, nil
	}

	// Find the last assistant message and check for warning messages after it
	var lastAssistantMessage *db.Message
	var lastAssistantIndex = -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
			lastAssistantMessage = messages[i]
			lastAssistantIndex = i
			break
		}
	}

	if lastAssistantMessage == nil {
		return FetchThreadResultOutput{
			Content: "No assistant response found in child thread",
			IsError: true,
		}, nil
	}

	// Check if there's a warning message after the last assistant message
	// This indicates the workflow ended abnormally (e.g., max turns reached)
	// Warning messages now use display_style="warning" instead of role="warning"
	var warningMessage *db.Message
	for i := lastAssistantIndex + 1; i < len(messages); i++ {
		if messages[i].DisplayStyle != nil && *messages[i].DisplayStyle == reliantv1.DisplayStyle_DISPLAY_STYLE_WARNING {
			warningMessage = messages[i]
			break
		}
	}

	// Get the content blocks for the assistant message
	blocks, err := a.repo.ListContentBlocks(ctx, lastAssistantMessage.ID)
	if err != nil {
		return FetchThreadResultOutput{
			Content: fmt.Sprintf("Failed to fetch message content: %v", err),
			IsError: true,
		}, nil
	}

	// Concatenate all text blocks
	var textParts []string
	for _, block := range blocks {
		if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT && block.Content != nil {
			textParts = append(textParts, *block.Content)
		}
	}

	content := strings.Join(textParts, "\n")
	if content == "" {
		content = "Agent completed but produced no text response"
	}

	// If there's a warning message, prepend it and mark as error
	if warningMessage != nil {
		warningBlocks, err := a.repo.ListContentBlocks(ctx, warningMessage.ID)
		if err == nil {
			var warningParts []string
			for _, block := range warningBlocks {
				if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT && block.Content != nil {
					warningParts = append(warningParts, *block.Content)
				}
			}
			if len(warningParts) > 0 {
				warningContent := strings.Join(warningParts, "\n")
				content = fmt.Sprintf("[WORKFLOW_WARNING] %s\n\nLast response before warning:\n%s", warningContent, content)
				logger.Info("[FetchThreadResult] Found warning message in thread",
					"chatID", input.ChatID,
					"thread", input.Thread,
					"warning", warningContent)
				return FetchThreadResultOutput{
					Content: content,
					IsError: true,
				}, nil
			}
		}
	}

	logger.Info("[FetchThreadResult] Successfully fetched result",
		"chatID", input.ChatID,
		"thread", input.Thread,
		"contentLength", len(content))

	return FetchThreadResultOutput{
		Content: content,
		IsError: false,
	}, nil
}
