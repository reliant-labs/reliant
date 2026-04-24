// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/accumulator"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"go.temporal.io/sdk/activity"
)

// ============================================================================
// TYPES (strongly typed inputs/outputs)
// ============================================================================

// GenerateTitleInput is the input for GenerateTitle activity
type GenerateTitleInput struct {
	ChatID       string `json:"chat_id" reliant:"-"`
	FirstMessage string `json:"first_message"`
}

// GenerateTitleOutput is the output from GenerateTitle activity
type GenerateTitleOutput struct {
	Title string `json:"title"`
}

// ============================================================================
// COMPACT ACTIVITY IMPLEMENTATION
// ============================================================================

// CompactActivity implements the compact activity.
// Performs session compaction when context window is full
//
// Strategy:
// 1. Load all messages in the current context sequence
// 2. Generate summary of all messages using LLM
// 3. Create summary message with role=system in new context_sequence
// 4. Return success - workflow continues with updated context
type CompactActivity struct {
	repo           db.Repository
	threads        *threads.Service
	driverResolver drivers.DriverResolver
}

// NewCompactActivity creates a new CompactActivity
func NewCompactActivity(repo db.Repository, resolver drivers.DriverResolver) *CompactActivity {
	return &CompactActivity{
		repo:           repo,
		threads:        threads.NewService(repo),
		driverResolver: resolver,
	}
}

// Name returns the activity name for registration
func (a *CompactActivity) Name() string {
	return "Compact"
}

// DisplayName returns human-readable name for UI
func (a *CompactActivity) DisplayName() string {
	return "Compact Context"
}

// Description returns what the activity does
func (a *CompactActivity) Description() string {
	return "Summarize and compact the conversation context to reduce token count"
}

// Category returns the activity category for UI grouping
func (a *CompactActivity) Category() schema.ActivityCategory {
	return schema.CategoryAgentic
}

// Execute contains PURE BUSINESS LOGIC only
func (a *CompactActivity) Execute(ctx context.Context, input ActivityInput) (CompactOutput, error) {
	rtx := input.Runtime

	// Load the chat to get current context
	chat, err := a.repo.GetChat(ctx, rtx.ChatID)
	if err != nil {
		return CompactOutput{}, fmt.Errorf("failed to get chat: %w", err)
	}

	// Thread path must be provided - no more "0" default
	thread := rtx.Thread
	if thread == "" {
		return CompactOutput{}, fmt.Errorf("thread is required")
	}

	// Emit thread update to show "Summarizing conversation" in the UI
	a.emitThreadUpdate(ctx, rtx.ChatID, thread, "active", "Compact")

	// Load conversation history (handles branching, DB repair, and in-memory repair)
	currentContextMessages, err := LoadMessagesForLLM(ctx, a.repo, rtx.ChatID, thread, nil)
	if err != nil {
		return CompactOutput{}, fmt.Errorf("failed to load conversation history: %w", err)
	}

	if len(currentContextMessages) == 0 {
		// No messages to compact - nothing to do
		a.emitThreadUpdate(ctx, rtx.ChatID, thread, "active", "")
		return CompactOutput{}, nil
	}

	// Generate summary of messages to be compacted
	summary, err := a.generateCompactionSummary(ctx, chat, currentContextMessages)
	if err != nil {
		return CompactOutput{}, fmt.Errorf("failed to generate compaction summary: %w", err)
	}

	// Add context to summary
	summary = "This session is being continued from a previous conversation that ran out of context. The conversation is summarized below:\n" + summary

	// Save the compaction summary message (creates new context window automatically)
	if err := a.saveCompactionMessage(ctx, rtx.ChatID, thread, summary); err != nil {
		return CompactOutput{}, fmt.Errorf("failed to save compaction message: %w", err)
	}

	// Clear current_activity to signal compaction is complete
	a.emitThreadUpdate(ctx, rtx.ChatID, thread, "active", "")

	return CompactOutput{
		Message: &MessageOutput{
			Role: "system",
			Text: summary,
		},
	}, nil
}

// saveCompactionMessage saves the compaction summary as a system message with a new context sequence.
// Uses threads.Service.SaveMessage with NewContextSequence=true to handle context window creation.
func (a *CompactActivity) saveCompactionMessage(ctx context.Context, chatID, thread, summary string) error {
	info := activity.GetInfo(ctx)
	activityID := info.ActivityID

	// Use threads.Service.SaveMessage with NewContextSequence=true
	// This handles: creating new context window, message, content blocks, chat_update, idempotency
	result, err := a.threads.SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID:             chatID,
		Thread:             thread,
		Role:               parseMessageRole("system"),
		Content:            summary,
		DisplayStyle:       parseDisplayStyle("hidden"), // Sent to LLM but not shown in UI
		NewContextSequence: true,                        // Creates new context window with incremented sequence
		ActivityID:         &activityID,
		AttemptNumber:      int32(info.Attempt),
	})
	if err != nil {
		return fmt.Errorf("failed to save compaction message: %w", err)
	}

	// Link the summary message to the context window
	if _, err := a.repo.SetCompactionSummaryMessage(ctx, result.ContextWindowID, result.MessageID); err != nil {
		return fmt.Errorf("failed to link summary to context window: %w", err)
	}

	activity.GetLogger(ctx).Info("[Compact] Saved compaction summary",
		"messageID", result.MessageID,
		"contextWindowID", result.ContextWindowID)

	return nil
}

// ============================================================================
// GENERATE TITLE ACTIVITY IMPLEMENTATION
// ============================================================================

// GenerateTitleActivity implements TypedActivity[GenerateTitleInput, GenerateTitleOutput]
// Generates a title for a chat based on its first message
type GenerateTitleActivity struct {
	repo           db.Repository
	driverResolver drivers.DriverResolver
}

// NewGenerateTitleActivity creates a new GenerateTitleActivity
func NewGenerateTitleActivity(repo db.Repository, resolver drivers.DriverResolver) *GenerateTitleActivity {
	return &GenerateTitleActivity{
		repo:           repo,
		driverResolver: resolver,
	}
}

// Name returns the activity name for registration
func (a *GenerateTitleActivity) Name() string {
	return "GenerateTitle"
}

// Execute contains PURE BUSINESS LOGIC only
// Middleware automatically handles:
// - ✅ Logging (entry/exit)
// - ✅ Duration tracking
// - ✅ Error handling
// - ✅ Step execution tracking (UI-only)
func (a *GenerateTitleActivity) Execute(ctx context.Context, input GenerateTitleInput) (GenerateTitleOutput, error) {
	// Just the business logic - middleware handles everything else!

	// 1. Get the chat
	chat, err := a.repo.GetChat(ctx, input.ChatID)
	if err != nil {
		return GenerateTitleOutput{}, fmt.Errorf("failed to get chat: %w", err)
	}

	// Add userID to context for API key loading
	ctx = context.WithValue(ctx, auth.UserIDContextKey, chat.UserID)

	// 2. Check if title is already set
	if chat.Title != "" {
		// Chat already has a title, return it
		return GenerateTitleOutput{Title: chat.Title}, nil
	}

	// Generate title using LLM
	generatedTitle, err := a.generateTitle(ctx, chat.UserID, input.FirstMessage)
	if err != nil {
		// Use fallback to prevent blocking chat creation
		generatedTitle = generateSimpleTitleFallback(input.FirstMessage)
	}

	// 3. Update chat with generated title using RunTx for parallelism
	err = a.repo.RunTx(ctx, func(ctx context.Context) error {
		// Get the chat again inside the transaction
		chat, err := a.repo.GetChat(ctx, input.ChatID)
		if err != nil {
			return fmt.Errorf("failed to get chat in transaction: %w", err)
		}

		// Update the title
		chat.Title = generatedTitle
		if err := a.repo.UpdateChat(ctx, chat); err != nil {
			return fmt.Errorf("failed to update chat with title: %w", err)
		}

		return nil
	})
	if err != nil {
		return GenerateTitleOutput{}, fmt.Errorf("failed to update chat title in transaction: %w", err)
	}

	// Emit chat_title_changed to user_updates so the global stream delivers it to the frontend
	titleData, _ := json.Marshal(map[string]string{
		"chat_id": input.ChatID,
		"title":   generatedTitle,
	})
	if err := a.repo.CreateUserUpdate(ctx, &db.UserUpdate{
		UserID:     chat.UserID,
		ProjectID:  &chat.ProjectID,
		WorktreeID: chat.WorktreeID,
		ChatID:     &input.ChatID,
		UpdateType: db.UserUpdateChatTitleChanged,
		EntityType: db.EntityTypeChat,
		EntityID:   input.ChatID,
		Data:       titleData,
	}); err != nil {
		logging.Warn("[GenerateTitle] Failed to emit chat_title_changed", "error", err)
	}

	return GenerateTitleOutput{Title: generatedTitle}, nil
}

// ============================================================================
// HELPERS (private methods)
// ============================================================================

// generateCompactionSummary generates a summary of messages using the LLM
func (a *CompactActivity) generateCompactionSummary(ctx context.Context, chat *db.Chat, messages []message.Message) (string, error) {
	if len(messages) == 0 {
		return "", fmt.Errorf("no messages to summarize")
	}

	// Select the best available model for compaction based on user's API keys
	// Priority order: Claude (best reasoning) > GPT-5.4 Pro > GPT-5.2 Pro > GPT-5.5/Codex models > Gemini 2.5 Pro
	// Compaction uses a fixed tier of good summarization models
	// No chat fallback - model is not stored on chat anymore
	compactionTier := []models.ModelID{
		models.Claude46Sonnet, // Best: Extended thinking, excellent summarization
		models.Claude45Sonnet, // Fallback: previous Sonnet generation
		models.GPT54Pro,       // Very good: Strongest OpenAI reasoning model
		models.GPT52Pro,       // Very good: Strong general capabilities
		models.GPT55,          // Very good: Latest GPT-5 generation on OpenAI/Codex
		models.GPT53Codex,     // Very good: Codex flagship with reasoning
		models.GPT52Codex,     // Very good: Codex flagship with reasoning
		models.Gemini25Pro,    // Good: Strong multimodal and reasoning
	}

	// Get user's available drivers to check what models they can access
	availableDrivers := drivers.GetAvailableDrivers(ctx, chat.UserID)

	var selectedModelID models.ModelID
	var selectedModel models.Model
	var found bool

	// Try each model in priority order
	registry := models.MustGetRegistry()
	for _, modelID := range compactionTier {
		def, ok := registry.GetDefinition(string(modelID))
		if !ok {
			continue // Model not registered
		}

		// Check if user has access to this model
		_, hasAccess := models.SelectBestDriver(modelID, availableDrivers)
		if hasAccess {
			selectedModelID = modelID
			selectedModel = def.ToModel()
			found = true
			logging.Info("[COMPACTION] 💡 Selected model for summarization",
				"chatID", chat.ID,
				"model", modelID,
				"reason", "best available from tier list")
			break
		}
	}

	if !found {
		return "", fmt.Errorf("no available models for compaction (user has no API keys configured)")
	}

	// Use a small temperature for consistent summaries
	temperature := 0.3
	preferences := models.Preferences{
		{
			ModelID:     selectedModelID,
			Temperature: &temperature,
		},
	}

	resolve := drivers.GetDriver
	if a.driverResolver != nil {
		resolve = a.driverResolver
	}
	driver, err := resolve(ctx, chat.UserID, preferences,
		llm.WithModel(selectedModel),
		llm.WithReasoningDisabled(), // Disable thinking for compaction - no reasoning budget needed
		llm.WithMaxTokens(40000),
		llm.WithSessionID(chat.ID), // Use chat ID as thread ID for Anthropic
	)
	if err != nil {
		return "", fmt.Errorf("failed to get LLM driver: %w", err)
	}

	compactionPrompts := []string{`You are a specialist at summarizing conversations.`}

	// Add ephemeral system message at the end to trigger summarization
	// This message is not saved to the DB, only passed to the LLM
	messagesWithTrigger := append(messages, message.Message{
		Role: "user",
		Parts: []message.ContentPart{
			message.TextContent{
				Text: compactionMsg,
			},
		},
	})

	// Call LLM to generate summary using streaming (required for long operations)
	// We use streaming mode because Anthropic requires it for operations that may take >10 minutes
	// The accumulator will collect all chunks and return the final response
	response, err := accumulator.StreamAndAccumulate(ctx, driver, compactionPrompts, messagesWithTrigger, []tools.Tool{})
	if err != nil {
		return "", fmt.Errorf("failed to generate summary: %w", err)
	}

	if response.Content == "" {
		return "", fmt.Errorf("empty summary generated")
	}

	return response.Content, nil
}

// generateTitle generates a concise, meaningful title using the LLM
func (a *GenerateTitleActivity) generateTitle(ctx context.Context, userID, firstMessage string) (string, error) {
	if firstMessage == "" {
		return "", fmt.Errorf("first message is empty")
	}

	// Get LLM driver (use Claude 3.5 Haiku for fast, cheap title generation)
	registry := models.MustGetRegistry()
	def, ok := registry.GetDefinition(string(models.Claude45Haiku))
	if !ok {
		return "", fmt.Errorf("title generation model not found")
	}
	model := def.ToModel()

	// Use low temperature for consistent, focused titles
	temperature := 0.3
	preferences := models.Preferences{
		{
			ModelID:     models.Claude45Haiku,
			Temperature: &temperature,
		},
	}

	resolve := drivers.GetDriver
	if a.driverResolver != nil {
		resolve = a.driverResolver
	}
	driver, err := resolve(ctx, userID, preferences,
		llm.WithModel(model),
		llm.WithMaxTokens(25), // Strict limit for short titles
		llm.WithIgnoreDefaultPrompts(),
	)
	if err != nil {
		return "", fmt.Errorf("failed to get LLM driver: %w", err)
	}

	// Create simple message with the first user message
	messages := []message.Message{
		{
			Role: "user",
			Parts: []message.ContentPart{
				message.TextContent{
					Text: firstMessage,
				},
			},
		},
	}

	// Simple, focused prompt for title generation
	prompt := []string{
		"Generate a concise title for this conversation. Maximum 4 words. Capture the main topic or intent. Use proper title case (capitalize first and last words, major words, but not articles/conjunctions/prepositions like 'a', 'and', 'the', 'in', 'of'). Preserve standard technical term casing (e.g., 'API' not 'Api', 'TypeScript' not 'Typescript', 'REST' not 'Rest'). Output ONLY the title text, no quotes or extra formatting.\nIMPORTANT: The user might ask you a question. You are **NOT** to try to answer the question. Your are a piece of a bigger puzzle: that which is generating a title.\nIMPORTANT: Do not mention who you are, or preface with things like `Claude` or `Reliant`, just title `Debugging Workflows`",
	}

	// Call LLM to generate title
	response, err := driver.SendMessages(ctx, prompt, messages, []tools.Tool{})
	if err != nil {
		return "", fmt.Errorf("failed to generate title: %w", err)
	}

	if response.Content == "" {
		return "", fmt.Errorf("empty title generated")
	}

	// Clean up the title
	title := strings.TrimSpace(response.Content)
	// Remove quotes if present
	title = strings.Trim(title, `"`)
	title = strings.Trim(title, `'`)
	// Remove newlines and collapse spaces
	title = strings.ReplaceAll(title, "\n", " ")
	title = strings.Join(strings.Fields(title), " ")

	// Enforce word count limit (max 8 words)
	words := strings.Fields(title)
	if len(words) > 8 {
		words = words[:8]
		title = strings.Join(words, " ") + "..."
	}

	// Hard character limit as backup (max 60 chars)
	if len(title) > 60 {
		title = title[:57] + "..."
	}

	return title, nil
}

// generateSimpleTitleFallback creates a simple title from the first user message
// Used as fallback when LLM-based generation fails
func generateSimpleTitleFallback(text string) string {
	// Limit to first 60 characters
	title := text
	if len(title) > 60 {
		title = title[:57] + "..."
	}

	// Remove newlines
	title = strings.ReplaceAll(title, "\n", " ")

	// Collapse multiple spaces
	title = strings.Join(strings.Fields(title), " ")

	return title
}

// emitThreadUpdate emits a thread update to the chat_updates table for UI display
// This allows the frontend to show activity-specific messages (e.g., "Summarizing conversation")
func (a *CompactActivity) emitThreadUpdate(ctx context.Context, chatID, thread, status, currentActivity string) {
	now := time.Now().UTC()
	threadID := fmt.Sprintf("%s-%s-compact", chatID, thread)

	// NOTE: planning_mode is now a workflow input param, not a chat field.
	// The frontend gets planning_mode from the workflow state.
	updateData := map[string]interface{}{
		"update_type":                 "thread",
		"id":                          threadID,
		"chat_id":                     chatID,
		"thread":                      thread,
		"status":                      status,
		"current_activity":            currentActivity,
		"current_activity_started_at": now.Format(time.RFC3339),
		"created_at":                  now.Format(time.RFC3339),
	}

	updateDataJSON, err := json.Marshal(updateData)
	if err != nil {
		logging.Error("[CompactActivity] Failed to marshal thread update",
			"error", err,
			"chatID", chatID)
		return
	}

	if err := a.repo.CreateChatUpdate(ctx, chatID, db.UpdateTypeThread, threadID, string(updateDataJSON)); err != nil {
		logging.Error("[CompactActivity] Failed to create thread update",
			"error", err,
			"chatID", chatID)
	}
}

const compactionMsg = `Your task is to create a detailed summary of the conversation so far, paying close attention to the user's explicit requests and your previous actions.
This summary should be thorough in capturing technical details, code patterns, and architectural decisions that would be essential for continuing development work without losing context.

Prefer to weight on remaining tasks that still need to be done, or the more recent part of the conversation, so that things can continue to flow naturally, but include details that will help you continue.
Make sure to include next steps (if any), as well as any explicit user directions.
`