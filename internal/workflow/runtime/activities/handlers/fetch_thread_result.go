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

	// An agent whose last assistant message carried no text has not reported.
	//
	// This is the shape a truncated run leaves behind: the thread's final
	// assistant message is tool-call-only (block_type=tool_use, content NULL),
	// so joinTextBlocks returns "" — the agent was still working when it
	// stopped. Saying "completed but produced no text response" with
	// IsError:false told the parent the child had finished cleanly and simply
	// had nothing to say, which is how a spawn killed mid-edit was reported as
	// a success whose result was that sentence (chat
	// 7da3935c-97ec-4843-af78-c3807fe336cb, thread 5e3fe370).
	//
	// The `!result.Found` branch above already treats "no assistant message"
	// as an error; "an assistant message with nothing in it" is the same
	// answer to the same question — the parent cannot act on either — so it
	// reports the same way. The wording says what the parent can actually do
	// about it, since this text IS the tool result the parent reads.
	if result.Content == "" {
		return FetchThreadResultOutput{
			Content: "Agent stopped without reporting a result. Its last action was a tool " +
				"call, so it was still working — this usually means the run was cut short " +
				"rather than finished. Check the thread for the work it completed, and " +
				"re-spawn or resume it if the task is unfinished.",
			IsError: true,
		}, nil
	}

	content := result.Content

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
