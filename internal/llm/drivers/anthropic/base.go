// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/anthropicwire"
	toolsPkg "github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// MalformedJSONError indicates that the API returned malformed JSON in the tool input.
// This is typically caused by network issues or API glitches during streaming,
// where partial JSON chunks are appended incorrectly.
// This error is transient and should trigger a retry.
type MalformedJSONError struct {
	ToolName string
	ToolID   string
	Input    string // Truncated for logging
}

func (e *MalformedJSONError) Error() string {
	return fmt.Sprintf("malformed JSON in tool input (transient): tool=%s, id=%s, input=%q", e.ToolName, e.ToolID, e.Input)
}

// IsMalformedJSONError returns true if the error is a MalformedJSONError.
// This can be used by error classification systems to identify transient streaming errors.
func IsMalformedJSONError(err error) bool {
	var mjErr *MalformedJSONError
	return errors.As(err, &mjErr)
}

// baseClient contains shared functionality for Anthropic-based drivers
type baseClient struct {
	options llm.DriverOptions
	client  anthropic.Client
}

func newBase(opts llm.DriverOptions, clientOptions []option.RequestOption) *baseClient {
	// Support validate (short timeout for validation requests)
	if opts.MaxTokens < 10 {
		clientOptions = append(clientOptions, option.WithRequestTimeout(10*time.Second))
	}
	// NOTE: We deliberately do NOT force temperature=1.0 here. Real Claude Code never
	// sends temperature (it is null in every capture); the API defaults to 1.0, which
	// is what extended thinking requires. Adaptive models (opus-4.8/sonnet-5/fable-5)
	// 400 if temperature is sent at all, so omitting it is mandatory.
	return &baseClient{
		options: opts,
		client:  llm.NewAnthropicSDKClient(clientOptions...),
	}
}

func (b *baseClient) addCacheControl(block *anthropic.ContentBlockParamUnion) {
	if b.options.DisableCache {
		return
	}

	switch {
	case block.OfText != nil:
		block.OfText.CacheControl = anthropic.CacheControlEphemeralParam{Type: "ephemeral"}
	case block.OfToolResult != nil:
		block.OfToolResult.CacheControl = anthropic.CacheControlEphemeralParam{Type: "ephemeral"}
	}
}

// msgBlocks pairs a converted Anthropic message with a pointer to its content blocks,
// allowing post-conversion mutation (e.g. cache control, validation).
type msgBlocks struct {
	msg    anthropic.MessageParam
	blocks *[]anthropic.ContentBlockParamUnion
}

func (b *baseClient) convertMessages(messages []message.Message) (anthropicMessages []anthropic.MessageParam) {
	// When thinking is enabled, we include thinking blocks from history in assistant messages.
	// When thinking is disabled (e.g. non-reasoning model), we strip them to avoid API errors.

	var allMessages []msgBlocks

	for _, msg := range messages {
		switch msg.Role {
		case message.System:
			textContent := fmt.Sprintf("<system>\n%s\n</system>", msg.Content().String())
			content := anthropic.NewTextBlock(textContent)
			blocks := []anthropic.ContentBlockParamUnion{content}
			allMessages = append(allMessages, msgBlocks{
				msg:    anthropic.NewUserMessage(content),
				blocks: &blocks,
			})

		case message.User:
			var contentBlocks []anthropic.ContentBlockParamUnion

			// Include ALL text contents (user text + file reference contents)
			for _, textContent := range msg.TextContents() {
				if textContent.Text != "" {
					contentBlocks = append(contentBlocks, anthropic.NewTextBlock(textContent.Text))
				}
			}

			for _, binaryContent := range msg.BinaryContent() {
				if binaryContent.MIMEType == "application/pdf" {
					base64Data := binaryContent.String(Family)
					contentBlocks = append(contentBlocks, anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{
						Data: base64Data,
					}))
				} else {
					base64Image := binaryContent.String(Family)
					contentBlocks = append(contentBlocks, anthropic.NewImageBlockBase64(binaryContent.MIMEType, base64Image))
				}
			}

			allMessages = append(allMessages, msgBlocks{
				msg:    anthropic.NewUserMessage(contentBlocks...),
				blocks: &contentBlocks,
			})

		case message.Assistant:
			blocks := []anthropic.ContentBlockParamUnion{}

			// Add thinking block FIRST (required by Claude API when thinking is enabled)
			// The thinking block must come before text and tool_use blocks.
			// Only include thinking blocks if thinking is enabled for this request;
			// sending them to a non-reasoning model will cause an API error.
			//
			// This predicate must match the one that decides whether the REQUEST
			// asks for thinking (getThinkingConfig / applyClaudeCodeExtras), which
			// is adaptive OR budget. Testing only isThinkingEnabled left adaptive
			// models (opus-5 / sonnet-5 / fable-5 — thinking:{type:"adaptive"},
			// which deliberately carries no ReasoningEffort budget) asking for
			// thinking on every turn while never replaying a single thinking block,
			// so each assistant turn was rebuilt as text+tool_calls only and the
			// cached prefix stopped matching from the message history onward.
			if b.isAdaptiveThinking() || b.isThinkingEnabled() {
				reasoning := msg.ReasoningContent()
				if reasoning.Thinking != "" && reasoning.Signature != "" {
					blocks = append(blocks, anthropic.NewThinkingBlock(reasoning.Signature, reasoning.Thinking))
					logging.Info("[convertMessages] Added thinking block",
						"msg_id", msg.ID,
						"thinking_len", len(reasoning.Thinking),
						"signature_len", len(reasoning.Signature))
				} else if reasoning.Thinking != "" {
					logging.Warn("[convertMessages] Skipping thinking block - missing signature",
						"msg_id", msg.ID,
						"thinking_len", len(reasoning.Thinking))
				}
			}

			if msg.Content().String() != "" {
				blocks = append(blocks, anthropic.NewTextBlock(msg.Content().String()))
			}

			for _, toolCall := range msg.ToolCalls() {
				// Ensure tool input is valid JSON - use empty object if input is empty or invalid
				inputJSON := toolCall.Input
				if inputJSON == "" || !json.Valid([]byte(inputJSON)) {
					logging.Warn("Invalid or empty tool input, using empty JSON object",
						"tool_id", toolCall.ID,
						"tool_name", toolCall.Name,
						"input", inputJSON)
					inputJSON = "{}"
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(toolCall.ID, json.RawMessage(inputJSON), toolCall.Name))
			}

			if len(blocks) == 0 {
				continue
			}

			allMessages = append(allMessages, msgBlocks{
				msg:    anthropic.NewAssistantMessage(blocks...),
				blocks: &blocks,
			})

		case message.Tool:
			results := make([]anthropic.ContentBlockParamUnion, len(msg.ToolResults()))
			for i, toolResult := range msg.ToolResults() {
				if len(toolResult.BinaryParts) == 0 {
					results[i] = anthropic.NewToolResultBlock(toolResult.ToolCallID, toolResult.Content, toolResult.IsError)
				} else {
					var blocks []anthropic.ToolResultBlockParamContentUnion
					if toolResult.Content != "" {
						var textBlock anthropic.TextBlockParam
						textBlock.Text = toolResult.Content
						blocks = append(blocks, anthropic.ToolResultBlockParamContentUnion{OfText: &textBlock})
					}
					for _, bp := range toolResult.BinaryParts {
						base64Data := base64.StdEncoding.EncodeToString(bp.Data)
						if bp.MIMEType == "application/pdf" {
							var docBlock anthropic.DocumentBlockParam
							docBlock.Source.OfBase64 = &anthropic.Base64PDFSourceParam{Data: base64Data}
							blocks = append(blocks, anthropic.ToolResultBlockParamContentUnion{OfDocument: &docBlock})
						} else {
							imgBlock := anthropic.ImageBlockParam{
								Source: anthropic.ImageBlockParamSourceUnion{
									OfBase64: &anthropic.Base64ImageSourceParam{
										Data:      base64Data,
										MediaType: anthropic.Base64ImageSourceMediaType(bp.MIMEType),
									},
								},
							}
							blocks = append(blocks, anthropic.ToolResultBlockParamContentUnion{OfImage: &imgBlock})
						}
					}
					results[i] = anthropic.ContentBlockParamUnion{
						OfToolResult: &anthropic.ToolResultBlockParam{
							ToolUseID: toolResult.ToolCallID,
							IsError:   anthropic.Bool(toolResult.IsError),
							Content:   blocks,
						},
					}
				}
			}

			allMessages = append(allMessages, msgBlocks{
				msg:    anthropic.NewUserMessage(results...),
				blocks: &results,
			})
		}
	}

	// Validate tool_result references — remove orphaned tool_results that would cause API errors.
	// This is a defensive safety net; the primary fix is in message persistence.
	allMessages = validateToolResultReferences(allMessages)

	// Add cache control to the last block of the last message
	if len(allMessages) > 0 {
		lastMsg := allMessages[len(allMessages)-1]
		if lastMsg.blocks != nil && len(*lastMsg.blocks) > 0 {
			b.addCacheControl(&(*lastMsg.blocks)[len(*lastMsg.blocks)-1])
		}
	}

	// Extract just the messages
	for _, m := range allMessages {
		anthropicMessages = append(anthropicMessages, m.msg)
	}

	return
}

// validateToolResultReferences removes tool_result blocks that reference tool_use IDs
// not present in the immediately preceding assistant message. This prevents the Anthropic
// API error "unexpected tool_use_id found in tool_result blocks" caused by orphaned
// tool results from message persistence bugs.
func validateToolResultReferences(allMessages []msgBlocks) []msgBlocks {
	var result []msgBlocks

	// Collect tool_use IDs from the last assistant message seen so far
	var lastAssistantToolUseIDs map[string]struct{}

	for _, m := range allMessages {
		if m.msg.Role == anthropic.MessageParamRoleAssistant {
			// Build set of tool_use IDs from this assistant message
			lastAssistantToolUseIDs = make(map[string]struct{})
			for _, block := range m.msg.Content {
				if block.OfToolUse != nil {
					lastAssistantToolUseIDs[block.OfToolUse.ID] = struct{}{}
				}
			}
			result = append(result, m)
			continue
		}

		// For user messages, check if any blocks are tool_results
		hasToolResults := false
		for _, block := range m.msg.Content {
			if block.OfToolResult != nil {
				hasToolResults = true
				break
			}
		}

		if !hasToolResults {
			// Not a tool result message, pass through unchanged
			result = append(result, m)
			continue
		}

		// Filter tool_result blocks: keep only those with matching tool_use IDs
		var validBlocks []anthropic.ContentBlockParamUnion
		for _, block := range m.msg.Content {
			if block.OfToolResult == nil {
				// Non-tool-result block in a user message, keep it
				validBlocks = append(validBlocks, block)
				continue
			}

			toolUseID := block.OfToolResult.ToolUseID
			if lastAssistantToolUseIDs == nil {
				logging.Warn("[convertMessages] Removing orphaned tool_result: no preceding assistant message",
					"tool_use_id", toolUseID)
				continue
			}

			if _, ok := lastAssistantToolUseIDs[toolUseID]; !ok {
				logging.Warn("[convertMessages] Removing orphaned tool_result: tool_use_id not found in preceding assistant message",
					"tool_use_id", toolUseID)
				continue
			}

			validBlocks = append(validBlocks, block)
		}

		// If all tool_result blocks were removed, drop the entire message
		if len(validBlocks) == 0 {
			logging.Warn("[convertMessages] Removing empty message after orphaned tool_result cleanup")
			continue
		}

		// Rebuild the message with only valid blocks
		if len(validBlocks) != len(m.msg.Content) {
			logging.Warn("[convertMessages] Removed orphaned tool_result blocks from message",
				"original_count", len(m.msg.Content),
				"valid_count", len(validBlocks))
			result = append(result, msgBlocks{
				msg:    anthropic.NewUserMessage(validBlocks...),
				blocks: &validBlocks,
			})
		} else {
			result = append(result, m)
		}
	}

	return result
}

func (b *baseClient) convertTools(tools []toolsPkg.Tool) []anthropic.ToolUnionParam {
	anthropicTools := make([]anthropic.ToolUnionParam, len(tools))

	for i, tool := range tools {
		schema := tool.ParamSchema()
		inputSchema := anthropic.ToolInputSchemaParam{
			Properties:  schema.Properties,
			ExtraFields: map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema"},
		}

		// Add required fields if present in schema
		if len(schema.Required) > 0 {
			inputSchema.Required = schema.Required
		}

		toolParam := anthropic.ToolParam{
			Name:        tool.Name(),
			Description: anthropic.String(tool.Description()),
			InputSchema: inputSchema,
		}

		// Cache only the last tool
		if !b.options.DisableCache && i == len(tools)-1 {
			logging.Debug("Anthropic: Caching last tool", "name", tool.Name(), "index", i)
			toolParam.CacheControl = anthropic.CacheControlEphemeralParam{
				Type: "ephemeral",
			}
		}

		anthropicTools[i] = anthropic.ToolUnionParam{OfTool: &toolParam}
	}

	return anthropicTools
}

// toolChoice returns the tool_choice param for a pinned tool, or the zero
// value (provider default "auto") when no pin is configured.
//
// toolName is the name as it appears in the request's tools array, which is
// not always options.ForceToolChoice: the Claude Code driver rewrites tool
// names with an mcp__ prefix, and tool_choice must match that rewritten name.
// DisableParallelToolUse forces exactly one tool_use block.
func (b *baseClient) toolChoice(toolName string) anthropic.ToolChoiceUnionParam {
	var choice anthropic.ToolChoiceUnionParam
	if toolName == "" {
		return choice
	}
	choice.OfTool = &anthropic.ToolChoiceToolParam{
		Name:                   toolName,
		DisableParallelToolUse: anthropic.Bool(true),
	}
	return choice
}

func (b *baseClient) finishReason(reason string) message.FinishReason {
	// The table lives in anthropicwire because Claude-on-Vertex reads the same
	// stop_reason vocabulary from a different package, and the two hand-kept
	// copies drifted. Delegating is what keeps them from drifting again;
	// anthropicwire also logs anything it does not recognise.
	return anthropicwire.FinishReason(anthropicwire.SourceAnthropic, reason)
}

// toolCalls extracts tool calls from the message and returns them along with any validation error.
// The error is returned separately to allow the caller to decide how to handle it.
func (b *baseClient) toolCalls(msg anthropic.Message) ([]message.ToolCall, error) {
	var toolCalls []message.ToolCall

	for _, block := range msg.Content {
		switch variant := block.AsAny().(type) {
		case anthropic.ToolUseBlock:
			// Validate that the accumulated JSON input is valid
			// The SDK's Accumulate() appends partial JSON chunks directly to Input,
			// which can result in malformed JSON if the stream is interrupted or
			// the API sends corrupted chunks (e.g., network issues, API glitches).
			inputStr := string(variant.Input)
			if !json.Valid(variant.Input) {
				// Return a transient error that will trigger a retry
				// Include the tool name and truncated input for debugging
				truncatedInput := inputStr
				if len(truncatedInput) > 100 {
					truncatedInput = truncatedInput[:100] + "..."
				}
				return nil, &MalformedJSONError{
					ToolName: variant.Name,
					ToolID:   variant.ID,
					Input:    truncatedInput,
				}
			}

			toolCall := message.ToolCall{
				ID:       variant.ID,
				Name:     variant.Name,
				Input:    inputStr,
				Type:     string(variant.Type),
				Finished: true,
			}
			toolCalls = append(toolCalls, toolCall)
		}
	}

	return toolCalls, nil
}

// buildCompleteEvent creates a complete event from accumulated message.
// Returns an error event if tool call JSON validation fails.
// manualThinkingSignature is used as a fallback if SDK accumulation doesn't capture it.
func (b *baseClient) buildCompleteEvent(accumulatedMessage anthropic.Message, manualThinkingSignature string) llm.DriverEvent {
	content := ""
	thinking := ""
	thinkingSignature := ""
	logging.Info("called build complete event with", "content_blocks", len(accumulatedMessage.Content))
	for _, block := range accumulatedMessage.Content {
		switch v := block.AsAny().(type) {
		case anthropic.TextBlock:
			content += v.Text
		case anthropic.ThinkingBlock:
			thinking += v.Thinking
			// The signature is returned with the thinking block
			logging.Info("Found ThinkingBlock in accumulated message",
				"thinking_len", len(v.Thinking),
				"sdk_signature_len", len(v.Signature),
				"has_sdk_signature", v.Signature != "")
			if v.Signature != "" {
				thinkingSignature = v.Signature
			}
		}
	}

	// Use manual signature as fallback if SDK didn't capture it
	if thinkingSignature == "" && manualThinkingSignature != "" {
		logging.Info("Using manually accumulated signature (SDK fallback)",
			"manual_signature_len", len(manualThinkingSignature))
		thinkingSignature = manualThinkingSignature
	}

	finishReason := message.FinishReasonUnknown
	if accumulatedMessage.StopReason != "" {
		finishReason = b.finishReason(string(accumulatedMessage.StopReason))
	}

	toolCalls, err := b.toolCalls(accumulatedMessage)
	if err != nil {
		// MalformedJSONError indicates transient streaming corruption
		// Return as an error event so the caller can retry
		logging.Warn("Stream buildCompleteEvent: Tool call JSON validation failed (will retry)",
			"error", err,
			"content_blocks", len(accumulatedMessage.Content))
		return llm.DriverEvent{
			Type:  llm.EventError,
			Error: err,
		}
	}

	return llm.DriverEvent{
		Type: llm.EventComplete,
		Response: &llm.DriverResponse{
			Content:           content,
			Thinking:          thinking,
			ThinkingSignature: thinkingSignature,
			ToolCalls:         toolCalls,
			Usage:             b.usage(accumulatedMessage),
			FinishReason:      finishReason,
		},
	}
}

func (b *baseClient) usage(msg anthropic.Message) llm.TokenUsage {
	// TokenCount = sum of all token usage (input + output + cache)
	total := msg.Usage.InputTokens + msg.Usage.OutputTokens +
		msg.Usage.CacheCreationInputTokens + msg.Usage.CacheReadInputTokens
	return llm.TokenUsage{
		TokenCount:   total,
		InputTokens:  msg.Usage.InputTokens,
		OutputTokens: msg.Usage.OutputTokens,
		// Carry the cache split through instead of only folding it into the
		// total. Both numbers were already being read here to compute `total`
		// and then dropped, which is why a stalled turn could not be
		// attributed to a cache miss from the logs alone.
		CacheReadInputTokens:     msg.Usage.CacheReadInputTokens,
		CacheCreationInputTokens: msg.Usage.CacheCreationInputTokens,
	}
}

// streamResponseInternal handles the common streaming logic for both drivers
func (b *baseClient) streamResponseInternal(ctx context.Context, params anthropic.MessageNewParams) <-chan llm.DriverEvent {
	eventChan := make(chan llm.DriverEvent)

	go func() {
		defer close(eventChan)

		stream := b.client.Messages.NewStreaming(ctx, params)
		accumulated := anthropic.Message{}
		gotMessageStop := false
		currentToolID := ""

		// Manual accumulation of thinking signature
		// The SDK's Accumulate may not properly handle signature_delta in all cases
		var manualThinkingSignature string

		// Process stream events
		for stream.Next() {
			event := stream.Current()

			// Debug: log all event types
			logging.Info("Stream event received",
				"event_type", fmt.Sprintf("%T", event.AsAny()))

			if err := accumulated.Accumulate(event); err != nil {
				logging.Warn("Stream accumulation error, will send accumulated message",
					"error", err, "blocks", len(accumulated.Content))
				eventChan <- llm.DriverEvent{Type: llm.EventError, Error: err}
				break
			}

			switch e := event.AsAny().(type) {
			case anthropic.ContentBlockStartEvent:
				if e.ContentBlock.Type == "text" {
					eventChan <- llm.DriverEvent{Type: llm.EventContentStart}
					continue
				}
				if e.ContentBlock.Type == "tool_use" {
					currentToolID = e.ContentBlock.ID
					eventChan <- llm.DriverEvent{
						Type: llm.EventToolUseStart,
						ToolCall: &message.ToolCall{
							ID:       e.ContentBlock.ID,
							Name:     e.ContentBlock.Name,
							Finished: false,
						},
					}
				}

			case anthropic.ContentBlockDeltaEvent:
				if e.Delta.Type == "thinking_delta" && e.Delta.Thinking != "" {
					eventChan <- llm.DriverEvent{Type: llm.EventThinkingDelta, Thinking: e.Delta.Thinking}
					continue
				}
				if e.Delta.Type == "signature_delta" && e.Delta.Signature != "" {
					// Manually accumulate signature - SDK's Accumulate may not handle this properly
					manualThinkingSignature += e.Delta.Signature
					logging.Info("Received signature_delta",
						"delta_len", len(e.Delta.Signature),
						"total_signature_len", len(manualThinkingSignature))
					continue
				}
				if e.Delta.Type == "text_delta" && e.Delta.Text != "" {
					eventChan <- llm.DriverEvent{Type: llm.EventContentDelta, Content: e.Delta.Text}
					continue
				}
				if e.Delta.Type == "input_json_delta" && currentToolID != "" {
					// The PartialJSON.Raw() method returns the raw JSON string bytes
					// We need to store this as-is without additional encoding
					rawInput := e.Delta.PartialJSON
					eventChan <- llm.DriverEvent{
						Type: llm.EventToolUseDelta,
						ToolCall: &message.ToolCall{
							ID:       currentToolID,
							Finished: false,
							Input:    rawInput,
						},
					}
				}

			case anthropic.ContentBlockStopEvent:
				if currentToolID != "" {
					eventChan <- llm.DriverEvent{
						Type:     llm.EventToolUseStop,
						ToolCall: &message.ToolCall{ID: currentToolID},
					}
					currentToolID = ""
				} else {
					eventChan <- llm.DriverEvent{Type: llm.EventContentStop}
				}

			case anthropic.MessageStopEvent:
				gotMessageStop = true
				logging.Info("Message stop event",
					"reason", accumulated.StopReason,
					"blocks", len(accumulated.Content))
				eventChan <- b.buildCompleteEvent(accumulated, manualThinkingSignature)
			}
		}

		logging.Info("Stream loop exited",
			"gotMessageStop", gotMessageStop,
			"accumulated_blocks", len(accumulated.Content),
			"manual_signature_len", len(manualThinkingSignature))

		// Check for stream errors
		if err := stream.Err(); err != nil && !errors.Is(err, io.EOF) {
			logging.Warn("Stream error", "error", err)
			eventChan <- llm.DriverEvent{Type: llm.EventError, Error: err}
		}

		// Always send complete event if not already sent
		if !gotMessageStop {
			logging.Warn("Stream ended without MessageStopEvent, using accumulated data",
				"blocks", len(accumulated.Content),
				"manual_signature_len", len(manualThinkingSignature))
			eventChan <- b.buildCompleteEvent(accumulated, manualThinkingSignature)
		}
	}()

	return eventChan
}

// prepareSystemPrompts prepares system prompts with cache control
func (b *baseClient) prepareSystemPrompts(prompts []string) []anthropic.TextBlockParam {
	systemPrompts := []anthropic.TextBlockParam{}
	for i, prompt := range prompts {
		if len(strings.TrimSpace(prompt)) == 0 {
			// CRITICAL: Skip empty prompts - they cause "cache_control cannot be set for empty text blocks" errors
			logging.Warn("Anthropic: Skipping empty system prompt", "index", i, "totalPrompts", len(prompts))
			continue
		}

		block := anthropic.TextBlockParam{
			Text: prompt,
		}
		// Cache the last 2 system prompts
		totalPrompts := len(prompts)
		if !b.options.DisableCache && totalPrompts > 0 && i >= totalPrompts-2 {
			block.CacheControl = anthropic.CacheControlEphemeralParam{
				Type: "ephemeral",
			}
		}
		systemPrompts = append(systemPrompts, block)
	}
	return systemPrompts
}

// isThinkingEnabled returns whether extended thinking should be active for this request.
func (b *baseClient) isThinkingEnabled() bool {
	return b.options.Model.CanReason &&
		b.options.ReasoningEffort != "" &&
		b.options.ReasoningEffort != "disabled" &&
		b.options.MaxTokens > 1024
}

// isAdaptiveThinking reports whether the model uses adaptive thinking
// (thinking:{type:"adaptive"} + output_config:{effort}) rather than the classic
// budget tier (thinking:{type:"enabled",budget_tokens:N}).
func (b *baseClient) isAdaptiveThinking() bool {
	return b.options.Model.ThinkingMode == "adaptive"
}

// getThinkingConfig returns the thinking configuration for extended thinking.
//
// Two tiers:
//   - adaptive (opus-4.8/sonnet-5/fable-5): thinking:{type:"adaptive"}. These
//     models reject budget_tokens and temperature. Effort is carried separately in
//     output_config (see getOutputConfig). Adaptive thinking is always on for these
//     models, matching real Claude Code traffic.
//   - budget (haiku-4.5 and all older models, ThinkingMode "budget"/unset):
//     thinking:{type:"enabled",budget_tokens:N}, gated by isThinkingEnabled.
func (b *baseClient) getThinkingConfig() anthropic.ThinkingConfigParamUnion {
	var thinkingParam anthropic.ThinkingConfigParamUnion

	if b.isAdaptiveThinking() {
		thinkingParam.OfAdaptive = &anthropic.ThinkingConfigAdaptiveParam{Type: "adaptive"}
		return thinkingParam
	}

	if !b.isThinkingEnabled() {
		return thinkingParam // Empty/disabled
	}

	// Map reasoning effort levels to fixed thinking budgets
	var thinkingBudget int64
	switch b.options.ReasoningEffort {
	case "low":
		thinkingBudget = 1024 // Minimal thinking for faster responses
	case "medium":
		thinkingBudget = 16000 // Moderate thinking
	case "high":
		thinkingBudget = 31999 // Maximum supported thinking
	default:
		thinkingBudget = 16000 // Default to medium
	}

	// Clamp thinking budget to be less than MaxTokens (API requires max_tokens > budget_tokens)
	if thinkingBudget >= b.options.MaxTokens {
		thinkingBudget = b.options.MaxTokens - 1
	}

	thinkingParam.OfEnabled = &anthropic.ThinkingConfigEnabledParam{
		BudgetTokens: thinkingBudget,
	}

	logging.Debug("Anthropic: Enabled thinking",
		"level", b.options.ReasoningEffort,
		"budget_tokens", thinkingBudget,
		"max_tokens", b.options.MaxTokens)

	return thinkingParam
}

// getOutputConfig returns output_config:{effort:<level>} for adaptive-thinking
// models and (false) for budget-tier models, which must NOT send output_config.
// The effort level uses the caller's ReasoningEffort when set, else "high" (the
// value observed in every adaptive capture).
func (b *baseClient) getOutputConfig() (anthropic.OutputConfigParam, bool) {
	if !b.isAdaptiveThinking() {
		return anthropic.OutputConfigParam{}, false
	}

	var effort anthropic.OutputConfigEffort
	switch b.options.ReasoningEffort {
	case "low":
		effort = anthropic.OutputConfigEffortLow
	case "medium":
		effort = anthropic.OutputConfigEffortMedium
	case "high":
		effort = anthropic.OutputConfigEffortHigh
	default:
		effort = anthropic.OutputConfigEffortHigh // captures always show effort:"high"
	}

	return anthropic.OutputConfigParam{Effort: effort}, true
}
