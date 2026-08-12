// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"

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
	threads *threads.Service
}

// NewFetchThreadResultActivity creates a new FetchThreadResultActivity
func NewFetchThreadResultActivity(threads *threads.Service) *FetchThreadResultActivity {
	return &FetchThreadResultActivity{threads: threads}
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

	result, err := a.threads.LastAssistantMessage(ctx, input.Thread)
	if err != nil {
		return FetchThreadResultOutput{
			Content: fmt.Sprintf("Failed to fetch thread messages: %v", err),
			IsError: true,
		}, nil
	}
	if !result.Found {
		return FetchThreadResultOutput{
			Content: "No assistant response found in child thread",
			IsError: true,
		}, nil
	}

	content := result.Content
	if content == "" {
		content = "Agent completed but produced no text response"
	}

	if result.Warning != "" {
		content = fmt.Sprintf("[WORKFLOW_WARNING] %s\n\nLast response before warning:\n%s", result.Warning, content)
		logger.Info("[FetchThreadResult] Found warning message in thread",
			"chatID", input.ChatID,
			"thread", input.Thread,
			"warning", result.Warning)
		return FetchThreadResultOutput{
			Content: content,
			IsError: true,
		}, nil
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
