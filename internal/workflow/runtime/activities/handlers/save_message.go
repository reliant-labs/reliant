// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"go.temporal.io/sdk/activity"
)

// parseMessageRole converts a string role to its int32 proto enum value.
func parseMessageRole(s string) int32 {
	switch s {
	case "user":
		return int32(reliantv1.MessageRole_MESSAGE_ROLE_USER)
	case "assistant":
		return int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT)
	case "system":
		return int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM)
	case "tool":
		return int32(reliantv1.MessageRole_MESSAGE_ROLE_TOOL)
	default:
		return 0
	}
}

// parseDisplayStyle converts a string display style to its int32 proto enum value.
func parseDisplayStyle(s string) int32 {
	switch s {
	case "info":
		return int32(reliantv1.DisplayStyle_DISPLAY_STYLE_INFO)
	case "warning":
		return int32(reliantv1.DisplayStyle_DISPLAY_STYLE_WARNING)
	case "success":
		return int32(reliantv1.DisplayStyle_DISPLAY_STYLE_SUCCESS)
	case "hidden":
		return int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN)
	default:
		return 0
	}
}

// DefaultCompactionThreshold is the default token count at which context compaction triggers.
// This is 80% of 200k context window = 160k tokens.
// This should match the compaction_threshold in agent.yaml configurations.
const DefaultCompactionThreshold = 185000

// SaveMessageActivity atomically saves user, assistant, tool, or system messages.
// This is a thin wrapper around threads.Service.SaveMessage that handles
// Temporal-specific concerns (activity info, idempotency key construction).
type SaveMessageActivity struct {
	repo    db.Repository
	threads *threads.Service
}

// NewSaveMessageActivity creates a new SaveMessageActivity
func NewSaveMessageActivity(repo db.Repository) *SaveMessageActivity {
	return &SaveMessageActivity{
		repo:    repo,
		threads: threads.NewService(repo),
	}
}

// Name returns the activity name for registration
func (a *SaveMessageActivity) Name() string {
	return "SaveMessage"
}

// DisplayName returns human-readable name for UI
func (a *SaveMessageActivity) DisplayName() string {
	return "Save Message"
}

// Description returns what the activity does
func (a *SaveMessageActivity) Description() string {
	return "Save a message to the conversation thread (user, assistant, or tool result)"
}

// Category returns the activity category for UI grouping
func (a *SaveMessageActivity) Category() schema.ActivityCategory {
	return schema.CategoryMessageProcessing
}

// Execute saves a message using the threads.Service.
// It extracts Temporal activity info for idempotency and delegates to the service.
func (a *SaveMessageActivity) Execute(ctx context.Context, input ActivityInput) (reliantv1.SaveMessageOutput, error) {
	rtx := input.Runtime
	protoArgs := model.GetSaveMessageNodeArgs(input.Node)
	if protoArgs == nil {
		return reliantv1.SaveMessageOutput{}, fmt.Errorf("expected save_message node, got %s", model.NodeType(input.Node))
	}

	info := activity.GetInfo(ctx)

	// Build scoped activity ID for idempotency.
	activityID := info.ActivityID
	if rtx.WorkflowID != "" {
		runID := info.WorkflowExecution.RunID
		activityID = rtx.WorkflowID + "-" + runID + "-" + activityID
	}

	// Convert proto types to Go types
	resolvedToolCalls := protoToolCallsToMessage(protoArgs.GetResolvedToolCalls())
	resolvedToolResults := protoToolResultsToMessage(protoArgs.GetResolvedToolResults())

	// Convert input to service options
	var thinking *threads.ThinkingContent
	if rt := protoArgs.GetResolvedThinking(); rt != nil && (rt.GetContent() != "" || rt.GetSignature() != "") {
		thinking = &threads.ThinkingContent{
			Content:   rt.GetContent(),
			Signature: rt.GetSignature(),
		}
	}

	var workflowID *string
	if rtx.WorkflowID != "" {
		workflowID = &rtx.WorkflowID
	}

	result, err := a.threads.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID:        rtx.ChatID,
		Thread:        rtx.Thread,
		Role:          parseMessageRole(protoArgs.GetResolvedRole()),
		Content:       protoArgs.GetResolvedContent(),
		Attachments:   protoArgs.GetResolvedAttachments(),
		ToolCalls:     convertToolCalls(resolvedToolCalls),
		ToolResults:   convertToolResults(resolvedToolResults),
		Thinking:      thinking,
		TokenCount:    int(protoArgs.GetTokenCount()),
		DisplayStyle:  parseDisplayStyle(protoArgs.GetResolvedDisplayStyle()),
		WorkflowID:    workflowID,
		StepID:        rtx.StepID,
		ActivityID:    &activityID,
		AttemptNumber: info.Attempt,
	})
	if err != nil {
		return reliantv1.SaveMessageOutput{}, err
	}

	return reliantv1.SaveMessageOutput{
		MessageId:        result.MessageID,
		Thread:           rtx.Thread,
		ToolCalls:        messageToolCallsToProto(resolvedToolCalls),
		ToolResults:      messageToolResultsToProto(resolvedToolResults),
		ThreadTokenCount: int32(result.ThreadTokenCount),
		MessageCount:     int32(result.MessageCount),
		Message:          &reliantv1.MessageOutput{Role: protoArgs.GetResolvedRole(), Text: protoArgs.GetResolvedContent()},
	}, nil
}

// convertToolCalls converts from message.ToolCall to threads.ToolCall.
// Since both are aliases for the same underlying type, this is a simple type cast.
func convertToolCalls(calls []message.ToolCall) []threads.ToolCall {
	if calls == nil {
		return nil
	}
	result := make([]threads.ToolCall, len(calls))
	for i, c := range calls {
		result[i] = threads.ToolCall(c)
	}
	return result
}

// convertToolResults converts from message.ToolResult to threads.ToolResult.
// Since both are aliases for the same underlying type, this is a simple type cast.
func convertToolResults(results []message.ToolResult) []threads.ToolResult {
	if results == nil {
		return nil
	}
	result := make([]threads.ToolResult, len(results))
	for i, r := range results {
		result[i] = threads.ToolResult(r)
	}
	return result
}
