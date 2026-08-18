// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/attachment"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/models"
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

// DefaultCompactionThreshold is the FALLBACK token count at which context
// compaction triggers when a per-node arg is unset and the model's real context
// window is unknown. Known models derive their threshold from the real window
// (models.CompactionThresholdFraction × max_context_window); this constant is the
// single-source floor, shared with models.UnknownModelCompactionFloor.
const DefaultCompactionThreshold = models.UnknownModelCompactionFloor

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

	// Build the idempotency key this message dedupes on.
	//
	// The caller may supply one, and when it does it wins outright: that is a
	// message whose identity is its position in the workflow graph, and the
	// caller has already derived a key that survives a resume (the inject
	// seed — see runtime.injectIdempotencyKey).
	//
	// Otherwise the key is scoped to the RunID, which is right for every other
	// message: an assistant reply or tool result belongs to the run that
	// produced it, and a resumed run genuinely should write its own.
	activityID := rtx.MessageIdempotencyKey
	if activityID == "" {
		activityID = info.ActivityID
		if rtx.WorkflowID != "" {
			runID := info.WorkflowExecution.RunID
			activityID = rtx.WorkflowID + "-" + runID + "-" + activityID
		}
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

	// Create DB attachments for inject files and merge their IDs into the attachments list
	attachments := protoArgs.GetResolvedAttachments()
	for _, f := range protoArgs.GetResolvedInjectFiles() {
		attID, err := a.createInjectFileAttachment(ctx, f)
		if err != nil {
			slog.Warn("Failed to create inject file attachment", "filename", f.GetFilename(), "error", err)
			continue
		}
		attachments = append(attachments, attID)
	}

	// Delta identity: persist the assistant message under its pre-allocated
	// streaming id. Assistant-only — other roles never streamed deltas, and a
	// misconfigured id on them would collide with the assistant row.
	fixedMessageID := ""
	if protoArgs.GetResolvedRole() == "assistant" {
		fixedMessageID = rtx.AssistantMessageID
	}

	result, err := a.threads.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID:        rtx.ChatID,
		Thread:        rtx.Thread,
		Role:          parseMessageRole(protoArgs.GetResolvedRole()),
		Content:       protoArgs.GetResolvedContent(),
		Attachments:   attachments,
		ToolCalls:     convertToolCalls(resolvedToolCalls),
		ToolResults:   convertToolResults(resolvedToolResults),
		Thinking:      thinking,
		TokenCount:    int(protoArgs.GetTokenCount()),
		Cost:          protoArgs.GetCost(),
		Model:         protoArgs.GetResolvedModel(),
		Agent:         protoArgs.GetResolvedAgent(),
		DisplayStyle:  parseDisplayStyle(protoArgs.GetResolvedDisplayStyle()),
		WorkflowID:    workflowID,
		StepID:        rtx.StepID,
		ActivityID:    &activityID,
		AttemptNumber: info.Attempt,
		MessageID:     fixedMessageID,
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

// createInjectFileAttachment creates a DB attachment from inject file data and returns the attachment ID.
func (a *SaveMessageActivity) createInjectFileAttachment(ctx context.Context, f *reliantv1.InjectFileMsg) (string, error) {
	attID := uuid.New().String()
	now := time.Now().UTC()

	attType := attachment.GetAttachmentType(f.GetFilename())
	attTypeStr := string(attType)
	if attType != attachment.TypeImage {
		attTypeStr = string(attachment.TypeDocument)
	}

	att := &db.Attachment{
		ID:             attID,
		Filename:       f.GetFilename(),
		Size:           int64(len(f.GetData())),
		MimeType:       f.GetMimeType(),
		AttachmentType: attTypeStr,
		Content:        f.GetData(),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := a.repo.CreateAttachment(ctx, att); err != nil {
		return "", fmt.Errorf("failed to create attachment for %s: %w", f.GetFilename(), err)
	}
	return attID, nil
}
