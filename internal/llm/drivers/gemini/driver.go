// Copyright (c) 2025 Reliant Labs
package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/invopop/jsonschema"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
	"google.golang.org/genai"
)

type GeminiClient struct {
	options llm.DriverOptions
	client  *genai.Client
}

// Name returns the name of the driver
func (c *GeminiClient) Name() string {
	return "gemini"
}

func NewClient(opts llm.DriverOptions) (*GeminiClient, error) {
	// Use streaming HTTP client with DNS resilience, ResponseHeaderTimeout (2min),
	// and idle stream timeout (5min). Do NOT use Client.Timeout — it applies to
	// the entire request including body reads and would kill long-running streams.
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{APIKey: opts.ApiKey, Backend: genai.BackendGeminiAPI, HTTPClient: llm.StreamingHTTPClient()})
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	return &GeminiClient{
		options: opts,
		client:  client,
	}, nil
}

func (g *GeminiClient) convertMessages(messages []message.Message) []*genai.Content {
	var history []*genai.Content
	for _, msg := range messages {
		switch msg.Role {
		case message.User:
			var parts []*genai.Part
			for _, textContent := range msg.TextContents() {
				if textContent.Text == "" {
					continue
				}
				parts = append(parts, &genai.Part{Text: textContent.Text})
			}

			for _, binaryContent := range msg.BinaryContent() {
				part := &genai.Part{InlineData: &genai.Blob{
					MIMEType: binaryContent.MIMEType,
					Data:     binaryContent.Data,
				}}
				parts = append(parts, part)
			}
			history = append(history, &genai.Content{
				Parts: parts,
				Role:  "user",
			})
		case message.System:
			// System messages (e.g., compaction summaries) are converted to user messages
			// because Gemini handles system instructions separately via SystemInstruction field
			var parts []*genai.Part
			textPart := &genai.Part{Text: msg.Content().String()}
			parts = append(parts, textPart)
			history = append(history, &genai.Content{
				Parts: parts,
				Role:  "user",
			})
		case message.Assistant:
			var assistantParts []*genai.Part

			if msg.Content().String() != "" {
				textPart := &genai.Part{Text: msg.Content().String()}
				assistantParts = append(assistantParts, textPart)
			}

			if len(msg.ToolCalls()) > 0 {
				for _, call := range msg.ToolCalls() {
					args, _ := parseJsonToMap(call.Input)
					part := &genai.Part{
						FunctionCall: &genai.FunctionCall{
							Name: call.Name,
							Args: args,
						},
					}

					// Only include thought signature if it was actually provided by the model
					// Thought signatures are stored as base64 to preserve binary data
					if call.ThoughtSignature != "" {
						if decoded, err := base64.StdEncoding.DecodeString(call.ThoughtSignature); err == nil {
							part.ThoughtSignature = decoded
						} else {
							logging.Warn("[GEMINI] Failed to decode thought signature, using raw bytes",
								"error", err, "toolName", call.Name)
							part.ThoughtSignature = []byte(call.ThoughtSignature)
						}
					}

					assistantParts = append(assistantParts, part)
				}
			}

			if len(assistantParts) > 0 {
				history = append(history, &genai.Content{
					Role:  "model",
					Parts: assistantParts,
				})
			}

		case message.Tool:
			for _, result := range msg.ToolResults() {
				response := map[string]interface{}{"result": result.Content}
				parsed, err := parseJsonToMap(result.Content)
				if err == nil {
					response = parsed
				}

				// Find the corresponding tool call to get the function name AND thought signature
				toolName := result.Name // Use the result's name field if available
				var thoughtSignature string
				if toolName == "" {
					// Fallback: search for the tool call in message history
					for _, m := range messages {
						if m.Role == message.Assistant {
							for _, call := range m.ToolCalls() {
								if call.ID == result.ToolCallID {
									toolName = call.Name
									thoughtSignature = call.ThoughtSignature
									break
								}
							}
						}
						if toolName != "" {
							break
						}
					}
				} else {
					// If we got the name from result, still need to find thought signature
					for _, m := range messages {
						if m.Role == message.Assistant {
							for _, call := range m.ToolCalls() {
								if call.ID == result.ToolCallID {
									thoughtSignature = call.ThoughtSignature
									break
								}
							}
						}
						if thoughtSignature != "" {
							break
						}
					}
				}

				if toolName == "" {
					logging.Warn("[GEMINI] Tool result without tool name", "toolCallID", result.ToolCallID)
					continue // Skip invalid tool results
				}

				// Add thought signature to function response if available
				part := &genai.Part{
					FunctionResponse: &genai.FunctionResponse{
						Name:     toolName,
						Response: response,
					},
				}

				// Only include thought signature if it was actually provided by the model
				// Thought signatures are stored as base64 to preserve binary data
				if thoughtSignature != "" {
					if decoded, err := base64.StdEncoding.DecodeString(thoughtSignature); err == nil {
						part.ThoughtSignature = decoded
					} else {
						logging.Warn("[GEMINI] Failed to decode thought signature for tool response",
							"error", err, "toolName", toolName)
						part.ThoughtSignature = []byte(thoughtSignature)
					}
				}

				// Gemini requires tool results to use "user" role, not "function"
				history = append(history, &genai.Content{
					Parts: []*genai.Part{part},
					Role:  "user",
				})
			}
		}
	}

	return history
}

// === Message validation with fallbacks ===
// convertMessagesWithValidation converts messages with validation and fallback handling
func (g *GeminiClient) convertMessagesWithValidation(messages []message.Message) []*genai.Content {
	history := g.convertMessages(messages)

	// CRITICAL: Never return empty message list - API will fail
	if len(history) == 0 {
		logging.Warn("[GEMINI] Message conversion produced zero messages, using fallback",
			"originalCount", len(messages))

		// Create a fallback user message
		fallback := &genai.Content{
			Role: "user",
			Parts: []*genai.Part{
				{Text: "[System: Message conversion failed. Please respond with a brief acknowledgment.]"},
			},
		}
		history = append(history, fallback)
	}

	// Validate message structure
	for i, content := range history {
		if content == nil {
			logging.Warn("[GEMINI] Null content at index, skipping", "index", i)
			continue
		}

		if len(content.Parts) == 0 {
			logging.Warn("[GEMINI] Empty parts in message", "index", i, "role", content.Role)
			// Add a placeholder part to prevent API errors
			content.Parts = []*genai.Part{{Text: "[Empty message]"}}
		}

		// Validate parts
		hasValidPart := false
		for _, part := range content.Parts {
			if part != nil && (part.Text != "" || part.FunctionCall != nil || part.FunctionResponse != nil || part.InlineData != nil) {
				hasValidPart = true
				break
			}
		}

		if !hasValidPart {
			logging.Warn("[GEMINI] No valid parts in message, adding placeholder", "index", i, "role", content.Role)
			content.Parts = append(content.Parts, &genai.Part{Text: "[Empty message]"})
		}
	}

	return history
}

func (g *GeminiClient) convertTools(tools []tools.Tool) []*genai.Tool {
	geminiTool := &genai.Tool{}
	geminiTool.FunctionDeclarations = make([]*genai.FunctionDeclaration, 0, len(tools))

	for _, tool := range tools {
		schema := tool.ParamSchema()
		declaration := &genai.FunctionDeclaration{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  convertToSchema(schema),
		}

		geminiTool.FunctionDeclarations = append(geminiTool.FunctionDeclarations, declaration)
	}

	return []*genai.Tool{geminiTool}
}
func (g *GeminiClient) finishReason(reason genai.FinishReason) message.FinishReason {
	switch reason {
	case genai.FinishReasonStop:
		return message.FinishReasonEndTurn
	case genai.FinishReasonMaxTokens:
		return message.FinishReasonMaxTokens
	default:
		return message.FinishReasonUnknown
	}
}

func (g *GeminiClient) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) (*llm.DriverResponse, error) {
	var remaining time.Duration
	deadline, ok := ctx.Deadline()
	if ok {
		remaining = time.Until(deadline)
	}

	// If there is no deadline or remaining time is less than 15s
	// Gemini requires a 10s minimum, so we give some grace and add 15s.
	// IMPORTANT: We extend the deadline but preserve cancellation from parent.
	// This allows user-initiated cancellation to still work.
	if ok && remaining < 15*time.Second {
		logging.Debug("[GEMINI] Extending deadline for minimum timeout",
			"originalRemaining", remaining,
			"newTimeout", "15s")

		// Create a merged context that:
		// 1. Respects parent cancellation (not deadline, just cancellation)
		// 2. Has a new 15s deadline
		// We use context.WithoutCancel to remove deadline, then add our own timeout,
		// but also monitor the original context's Done() channel.
		detachedCtx := context.WithoutCancel(ctx)
		extendedCtx, cancel := context.WithTimeout(detachedCtx, 15*time.Second)

		// Create a merged context that cancels when either the original OR extended cancels
		mergedCtx, mergedCancel := context.WithCancelCause(extendedCtx)
		go func() {
			select {
			case <-ctx.Done():
				// Parent was cancelled (user cancellation) - propagate it
				mergedCancel(ctx.Err())
			case <-extendedCtx.Done():
				// Extended timeout expired - already cancelled
			}
		}()

		ctx = mergedCtx
		defer func() {
			mergedCancel(nil)
			cancel()
		}()
	}

	logging.Debug("[GEMINI] SendMessages called",
		"messageCount", len(messages),
		"toolCount", len(tools))

	geminiMessages := g.convertMessages(messages)

	if len(geminiMessages) == 0 {
		logging.Error("[GEMINI] Message conversion produced zero messages",
			"originalCount", len(messages))
		return nil, fmt.Errorf("message conversion resulted in zero messages (received %d messages)", len(messages))
	}

	systemParts := []*genai.Part{}
	for _, prompt := range prompts {
		if prompt != "" {
			part := &genai.Part{Text: prompt}
			systemParts = append(systemParts, part)
		}
	}
	// Add Gemini-specific instructions for better user experience
	systemParts = append(systemParts, &genai.Part{Text: getGeminiInstructions()})

	var history []*genai.Content
	var lastMsg *genai.Content

	if len(geminiMessages) > 1 {
		history = geminiMessages[:len(geminiMessages)-1]
		lastMsg = geminiMessages[len(geminiMessages)-1]
	} else {
		history = []*genai.Content{}
		lastMsg = geminiMessages[0]
	}

	logging.Debug("[GEMINI] Sending request",
		"totalMessages", len(geminiMessages),
		"historyCount", len(history),
		"model", g.options.Model.APIModel,
		"maxTokens", g.options.MaxTokens)

	config := &genai.GenerateContentConfig{
		MaxOutputTokens: int32(g.options.MaxTokens),
		SystemInstruction: &genai.Content{
			Parts: systemParts,
		},
	}

	if g.options.Temperature != nil {
		temp := float32(*g.options.Temperature)
		config.Temperature = &temp
	}

	// Configure thinking based on model version and reasoning effort
	if g.options.ReasoningEffort != "" && g.options.ReasoningEffort != "disabled" {
		thinkingConfig := g.buildThinkingConfig(g.options.Model.APIModel, g.options.ReasoningEffort)
		if thinkingConfig != nil {
			config.ThinkingConfig = thinkingConfig
			logging.Debug("[GEMINI] Enabled thinking",
				"model", g.options.Model.APIModel,
				"effort", g.options.ReasoningEffort)
		}
	}

	if len(tools) > 0 {
		config.Tools = g.convertTools(tools)
	}
	chat, err := g.client.Chats.Create(ctx, g.options.Model.APIModel, config, history)
	if err != nil {
		logging.Error("[GEMINI] Failed to create chat", "error", err, "model", g.options.Model.APIModel)
		return nil, fmt.Errorf("failed to create Gemini chat: %w", err)
	}
	if chat == nil {
		logging.Error("[GEMINI] Chat is nil after creation", "model", g.options.Model.APIModel)
		return nil, fmt.Errorf("gemini chat creation returned nil")
	}

	attempts := 0
	for {
		attempts++
		var toolCalls []message.ToolCall

		var lastMsgParts []genai.Part
		for _, part := range lastMsg.Parts {
			lastMsgParts = append(lastMsgParts, *part)
		}
		resp, err := chat.SendMessage(ctx, lastMsgParts...)
		// If there is an error we are going to see if we can retry the call
		if err != nil {
			retry, after, retryErr := g.shouldRetry(attempts, err)
			if retryErr != nil {
				return nil, retryErr
			}
			if retry {
				logging.Warn(fmt.Sprintf("Retrying due to rate limit... attempt %d of %d", attempts, models.MaxRetries))
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(after) * time.Millisecond):
					continue
				}
			}
			return nil, retryErr
		}

		content := ""

		if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
			for _, part := range resp.Candidates[0].Content.Parts {
				switch {
				case part.Text != "":
					content = string(part.Text)
				case part.FunctionCall != nil:
					id := "call_" + uuid.New().String()
					args, _ := json.Marshal(part.FunctionCall.Args)
					toolCall := message.ToolCall{
						ID:       id,
						Name:     part.FunctionCall.Name,
						Input:    string(args),
						Type:     "function",
						Finished: true,
					}
					// Capture thought signature if present (Gemini 3.x)
					// Encode as base64 to preserve binary data through string storage
					if len(part.ThoughtSignature) > 0 {
						toolCall.ThoughtSignature = base64.StdEncoding.EncodeToString(part.ThoughtSignature)
					}
					toolCalls = append(toolCalls, toolCall)
				}
			}
		}

		logging.Debug("[GEMINI] Response received",
			"contentLength", len(content),
			"toolCallCount", len(toolCalls))
		finishReason := message.FinishReasonEndTurn
		if len(resp.Candidates) > 0 {
			finishReason = g.finishReason(resp.Candidates[0].FinishReason)
		}
		if len(toolCalls) > 0 {
			finishReason = message.FinishReasonToolUse
		}

		return &llm.DriverResponse{
			Content:      content,
			ToolCalls:    toolCalls,
			Usage:        g.usage(resp),
			FinishReason: finishReason,
		}, nil
	}
}

func (g *GeminiClient) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) <-chan llm.DriverEvent {
	eventChan := make(chan llm.DriverEvent)

	go func() {
		defer close(eventChan)

		// === ROBUSTNESS: Guaranteed completion ===
		// Track state for cleanup
		var currentContent string
		var toolCalls []message.ToolCall
		var finalResp *genai.GenerateContentResponse
		var contentStarted bool
		sentComplete := false

		// CRITICAL: Always send completion event, even on errors/panics
		defer func() {
			if r := recover(); r != nil {
				logging.Error("[GEMINI] Panic recovered in StreamResponse", "panic", r)
				eventChan <- llm.DriverEvent{
					Type:  llm.EventError,
					Error: fmt.Errorf("stream panic: %v", r),
				}
			}

			if !sentComplete {
				// Send fallback complete event with whatever we accumulated
				logging.Warn("[GEMINI] Sending fallback complete event",
					"hasContent", currentContent != "",
					"toolCallCount", len(toolCalls))

				finishReason := message.FinishReasonUnknown
				if finalResp != nil && len(finalResp.Candidates) > 0 {
					finishReason = g.finishReason(finalResp.Candidates[0].FinishReason)
				}
				if len(toolCalls) > 0 {
					finishReason = message.FinishReasonToolUse
				}

				eventChan <- llm.DriverEvent{
					Type: llm.EventComplete,
					Response: &llm.DriverResponse{
						Content:      currentContent,
						ToolCalls:    toolCalls,
						Usage:        g.usage(finalResp),
						FinishReason: finishReason,
					},
				}
			}
		}()

		// Convert and validate messages
		logging.Debug("[GEMINI] StreamResponse starting", "messageCount", len(messages))

		geminiMessages := g.convertMessagesWithValidation(messages)
		if len(geminiMessages) == 0 {
			logging.Error("[GEMINI] Message conversion produced zero messages", "originalCount", len(messages))
			eventChan <- llm.DriverEvent{
				Type:  llm.EventError,
				Error: fmt.Errorf("message conversion failed: received %d messages but produced 0", len(messages)),
			}
			return
		}

		// Build system instruction
		systemParts := []*genai.Part{}
		for _, prompt := range prompts {
			if prompt != "" {
				systemParts = append(systemParts, &genai.Part{Text: prompt})
			}
		}
		// Add Gemini-specific instructions for better user experience
		systemParts = append(systemParts, &genai.Part{Text: getGeminiInstructions()})

		// Split history and last message
		var history []*genai.Content
		var lastMsg *genai.Content

		if len(geminiMessages) > 1 {
			history = geminiMessages[:len(geminiMessages)-1]
			lastMsg = geminiMessages[len(geminiMessages)-1]
		} else {
			history = []*genai.Content{}
			lastMsg = geminiMessages[0]
		}

		logging.Debug("[GEMINI] Stream configuration",
			"totalMessages", len(geminiMessages),
			"historyCount", len(history),
			"model", g.options.Model.APIModel,
			"maxTokens", g.options.MaxTokens)

		// Configure generation
		config := &genai.GenerateContentConfig{
			MaxOutputTokens: int32(g.options.MaxTokens),
			SystemInstruction: &genai.Content{
				Parts: systemParts,
			},
		}

		if g.options.Temperature != nil {
			temp := float32(*g.options.Temperature)
			config.Temperature = &temp
		}

		// Configure thinking based on model version and reasoning effort
		if g.options.ReasoningEffort != "" && g.options.ReasoningEffort != "disabled" {
			thinkingConfig := g.buildThinkingConfig(g.options.Model.APIModel, g.options.ReasoningEffort)
			if thinkingConfig != nil {
				config.ThinkingConfig = thinkingConfig
				logging.Debug("[GEMINI] Enabled thinking for stream",
					"model", g.options.Model.APIModel,
					"effort", g.options.ReasoningEffort)
			}
		}

		if len(tools) > 0 {
			config.Tools = g.convertTools(tools)
			logging.Debug("[GEMINI] Tools configured", "toolCount", len(tools))
		}

		// Create chat session
		chat, err := g.client.Chats.Create(ctx, g.options.Model.APIModel, config, history)
		if err != nil {
			logging.Error("[GEMINI] Failed to create chat", "error", err)
			eventChan <- llm.DriverEvent{
				Type:  llm.EventError,
				Error: fmt.Errorf("failed to create chat: %w", err),
			}
			return
		}
		if chat == nil {
			logging.Error("[GEMINI] Chat creation returned nil")
			eventChan <- llm.DriverEvent{
				Type:  llm.EventError,
				Error: fmt.Errorf("chat creation returned nil"),
			}
			return
		}

		// === ROBUSTNESS: Retry loop with proper error handling ===
		attempts := 0
		for {
			attempts++
			if attempts > 1 {
				logging.Debug("[GEMINI] Retry attempt", "attempt", attempts)
			}

			// Reset per-attempt state
			currentContent = ""
			toolCalls = []message.ToolCall{}
			finalResp = nil
			contentStarted = false

			// Send message stream
			var lastMsgParts []genai.Part
			for _, part := range lastMsg.Parts {
				lastMsgParts = append(lastMsgParts, *part)
			}

			streamErr := g.processStreamResponses(ctx, chat, lastMsgParts, eventChan, &currentContent, &toolCalls, &finalResp, &contentStarted)

			// === ROBUSTNESS: Error recovery ===
			if streamErr != nil {
				if errors.Is(streamErr, io.EOF) {
					// Normal EOF - stream completed
					logging.Debug("[GEMINI] Stream completed (EOF)")
					break
				}

				// Check if we should retry
				retry, after, retryErr := g.shouldRetry(attempts, streamErr)
				if retryErr != nil {
					logging.Error("[GEMINI] Non-retriable error", "error", retryErr)
					eventChan <- llm.DriverEvent{Type: llm.EventError, Error: retryErr}
					return
				}

				if retry {
					logging.Warn("[GEMINI] Retriable error, backing off",
						"attempt", attempts,
						"maxRetries", models.MaxRetries,
						"backoffMs", after)

					select {
					case <-ctx.Done():
						logging.Debug("[GEMINI] Context cancelled during retry backoff")
						eventChan <- llm.DriverEvent{Type: llm.EventError, Error: ctx.Err()}
						return
					case <-time.After(time.Duration(after) * time.Millisecond):
						continue // Retry
					}
				}

				// Not retriable, emit error and exit
				logging.Error("[GEMINI] Stream error", "error", streamErr)
				eventChan <- llm.DriverEvent{Type: llm.EventError, Error: streamErr}
				return
			}

			// Stream completed successfully
			break
		}

		// Only emit ContentStop if we started content
		if contentStarted {
			eventChan <- llm.DriverEvent{Type: llm.EventContentStop}
		}

		// === ROBUSTNESS: Always send complete event ===
		finishReason := message.FinishReasonEndTurn
		if finalResp != nil && len(finalResp.Candidates) > 0 {
			finishReason = g.finishReason(finalResp.Candidates[0].FinishReason)
		}
		if len(toolCalls) > 0 {
			finishReason = message.FinishReasonToolUse
		}

		// If we didn't accumulate any content from deltas, try to extract from final response
		// This handles cases where Gemini returns content only in the final response
		if currentContent == "" && finalResp != nil && len(finalResp.Candidates) > 0 && finalResp.Candidates[0].Content != nil {
			for _, part := range finalResp.Candidates[0].Content.Parts {
				if part.Text != "" {
					currentContent += string(part.Text)
				}
			}
		}

		logging.Debug("[GEMINI] Stream completed successfully",
			"contentLength", len(currentContent),
			"toolCallCount", len(toolCalls),
			"finishReason", finishReason)

		eventChan <- llm.DriverEvent{
			Type: llm.EventComplete,
			Response: &llm.DriverResponse{
				Content:      currentContent,
				ToolCalls:    toolCalls,
				Usage:        g.usage(finalResp),
				FinishReason: finishReason,
			},
		}
		sentComplete = true
	}()

	return eventChan
}

// processStreamResponses handles the actual streaming logic
// Returns error if stream fails
func (g *GeminiClient) processStreamResponses(
	ctx context.Context,
	chat *genai.Chat,
	parts []genai.Part,
	eventChan chan<- llm.DriverEvent,
	currentContent *string,
	toolCalls *[]message.ToolCall,
	finalResp **genai.GenerateContentResponse,
	contentStarted *bool,
) error {
	for resp, err := range chat.SendMessageStream(ctx, parts...) {
		// Check for context cancellation at each iteration.
		// The SDK's iterator may not interrupt immediately on context cancel,
		// so we check explicitly to respond as soon as we get control back.
		select {
		case <-ctx.Done():
			logging.Debug("[GEMINI] Context cancelled during stream processing")
			return ctx.Err()
		default:
			// Not cancelled, continue processing
		}

		if err != nil {
			return err
		}

		*finalResp = resp

		if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
			continue
		}

		for _, part := range resp.Candidates[0].Content.Parts {
			switch {
			case part.Text != "":
				delta := string(part.Text)
				if delta != "" {
					// Only emit ContentStart when we first see text
					if !*contentStarted {
						eventChan <- llm.DriverEvent{Type: llm.EventContentStart}
						*contentStarted = true
					}
					eventChan <- llm.DriverEvent{
						Type:    llm.EventContentDelta,
						Content: delta,
					}
					*currentContent += delta

					// Debug: Check if text parts have thought signatures
					if len(part.ThoughtSignature) > 0 {
						logging.Debug("[GEMINI] Received text part with thought signature",
							"signatureLength", len(part.ThoughtSignature),
							"textPreview", delta[:min(50, len(delta))])
					} else {
						logging.Debug("[GEMINI] Received text part without thought signature")
					}
				}

			case part.FunctionCall != nil:
				id := "call_" + uuid.New().String()
				args, _ := json.Marshal(part.FunctionCall.Args)

				// Capture thought signature if present (Gemini thinking models)
				// Encode as base64 to preserve binary data through string storage
				thoughtSig := ""
				if len(part.ThoughtSignature) > 0 {
					thoughtSig = base64.StdEncoding.EncodeToString(part.ThoughtSignature)
					logging.Debug("[GEMINI] Received thought signature from model",
						"functionName", part.FunctionCall.Name,
						"rawLength", len(part.ThoughtSignature),
						"base64Length", len(thoughtSig))
				}

				newCall := message.ToolCall{
					ID:               id,
					Name:             part.FunctionCall.Name,
					Input:            string(args),
					Type:             "function",
					Finished:         true,
					ThoughtSignature: thoughtSig, // Only set if model provided one
				}

				// Check for duplicates
				isNew := true
				for _, existing := range *toolCalls {
					if existing.Name == newCall.Name && existing.Input == newCall.Input {
						isNew = false
						break
					}
				}

				if isNew {
					*toolCalls = append(*toolCalls, newCall)
					// CRITICAL: Emit EventToolUseStart so ProcessMessage can create tool_call blocks
					eventChan <- llm.DriverEvent{
						Type:     llm.EventToolUseStart,
						ToolCall: &newCall,
					}
				}
			}
		}
	}

	return nil
}

func (g *GeminiClient) shouldRetry(attempts int, err error) (bool, int64, error) {
	// Check if error is a rate limit error
	if attempts > models.MaxRetries {
		return false, 0, fmt.Errorf("maximum retry attempts reached for rate limit: %d retries", models.MaxRetries)
	}

	// Gemini doesn't have a standard error type we can check against
	// So we'll check the error message for rate limit indicators
	if errors.Is(err, io.EOF) {
		return false, 0, err
	}

	errMsg := err.Error()
	// Check for common retryable error messages.
	// Gemini can intermittently return INTERNAL/500 mid-stream.
	// Also retry on 503/UNAVAILABLE (overloaded) errors - these are temporary service issues
	isRateLimit := contains(errMsg, "rate limit", "quota exceeded", "too many requests")
	isInternal := contains(errMsg, "Internal error encountered", "Status: INTERNAL")
	isUnavailable := contains(errMsg, "Status: UNAVAILABLE", "503", "overloaded", "try again later")

	if !isRateLimit && !isInternal && !isUnavailable {
		return false, 0, err
	}

	// Calculate backoff with jitter
	backoffMs := 2000 * (1 << (attempts - 1))
	jitterMs := int(float64(backoffMs) * 0.2)
	retryMs := backoffMs + jitterMs

	return true, int64(retryMs), nil
}

// usage returns token usage from Gemini's response.
// usage returns token usage from Gemini's response.
// Gemini's TotalTokenCount is the total of prompt + response + tool tokens.
func (g *GeminiClient) usage(resp *genai.GenerateContentResponse) llm.TokenUsage {
	if resp == nil || resp.UsageMetadata == nil {
		return llm.TokenUsage{}
	}

	// TokenCount = TotalTokenCount (prompt + response + tool tokens)
	return llm.TokenUsage{
		TokenCount: int64(resp.UsageMetadata.TotalTokenCount),
	}
}

func (g *GeminiClient) Model() models.Model {
	return g.options.Model
}

func (g *GeminiClient) ValidateKey(ctx context.Context) error {
	// Use Gemini 2.0 Flash Lite for validation (small, fast model)
	testMessages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Say 'test' and nothing else"},
			},
		},
	}

	// Create a temporary client with the small model
	validationOpts := g.options
	registry := models.MustGetRegistry()
	if def, ok := registry.GetDefinition(string(models.Gemini25FlashLite)); ok {
		validationOpts.Model = def.ToModel()
	}
	validationOpts.MaxTokens = 100

	validationClient, err := NewClient(validationOpts)
	if err != nil {
		return err
	}

	_, err = validationClient.SendMessages(ctx, []string{}, testMessages, []tools.Tool{})
	return err
}

// Helper functions
func parseJsonToMap(jsonStr string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := json.Unmarshal([]byte(jsonStr), &result)
	return result, err
}

func convertToSchema(param *jsonschema.Schema) *genai.Schema {
	schema := &genai.Schema{
		Type:        genai.Type(param.Type),
		Description: param.Description,
	}

	// Handle enum values
	if len(param.Enum) > 0 {
		schema.Enum = make([]string, len(param.Enum))
		for i, v := range param.Enum {
			schema.Enum[i] = fmt.Sprint(v)
		}
	}

	switch param.Type {
	case "array":
		if param.Items != nil {
			schema.Items = convertToSchema(param.Items)
		}
	case "object":
		if param.Properties != nil {
			schema.Properties = make(map[string]*genai.Schema)
			for pair := param.Properties.Oldest(); pair != nil; pair = pair.Next() {
				key := pair.Key
				value := pair.Value
				schema.Properties[key] = convertToSchema(value)
			}
		}
		// CRITICAL: Set required fields for Gemini
		if len(param.Required) > 0 {
			schema.Required = param.Required
		}
	}

	return schema
}

func contains(s string, substrs ...string) bool {
	for _, substr := range substrs {
		if strings.Contains(strings.ToLower(s), strings.ToLower(substr)) {
			return true
		}
	}
	return false
}

// buildThinkingConfig creates appropriate thinking configuration based on model version
// Gemini 3.x Flash models: Use ThinkingLevel (supports minimal/low/medium/high)
// Gemini 3.x Pro models: Use ThinkingLevel (only supports low/high - NO medium)
// Gemini 2.5 models: Use ThinkingBudget (token count)
// Older models (2.0, 1.5): No thinking support (returns nil)
func (g *GeminiClient) buildThinkingConfig(modelName, reasoningEffort string) *genai.ThinkingConfig {
	// Check if this is a Gemini 3.x model
	isGemini3 := strings.Contains(modelName, "gemini-3")

	// Check if this is a Gemini 2.5 model
	isGemini25 := strings.Contains(modelName, "gemini-2.5")

	// Check model variants
	isFlash := strings.Contains(modelName, "flash")
	isPro := strings.Contains(modelName, "pro")
	isPreview := strings.Contains(modelName, "preview")

	// WORKAROUND: Gemini 3 Flash Preview crashes with 500 "Internal error" when
	// explicit thinkingConfig is combined with function calling (tool use).
	// Skip sending thinkingConfig for Flash Preview to let Google's API use
	// default dynamic thinking instead, which is more stable.
	// This appears to be a Google API bug - docs say thinking + tools is supported.
	// TODO: Re-enable when Google stabilizes Flash Preview with thinking + tools
	if isGemini3 && isFlash && isPreview {
		logging.Debug("[GEMINI] Skipping explicit thinkingConfig for Gemini 3 Flash Preview (known API issue)",
			"model", modelName,
			"requestedEffort", reasoningEffort)
		return nil
	}

	// Older models don't support thinking
	if !isGemini3 && !isGemini25 {
		logging.Debug("[GEMINI] Model does not support thinking", "model", modelName)
		return nil
	}

	config := &genai.ThinkingConfig{}

	if isGemini3 {
		// Gemini 3.x Pro: Only supports LOW and HIGH (not MEDIUM)
		// Gemini 3.x Flash: Supports MINIMAL, LOW, MEDIUM, HIGH
		if isPro {
			// Pro models only support LOW and HIGH
			switch reasoningEffort {
			case "low":
				config.ThinkingLevel = genai.ThinkingLevelLow
			case "medium", "high":
				// Map medium to high for Pro models since medium is not supported
				config.ThinkingLevel = genai.ThinkingLevelHigh
			default:
				config.ThinkingLevel = genai.ThinkingLevelLow
			}
			logging.Debug("[GEMINI] Using ThinkingLevel for Gemini 3 Pro (low/high only)",
				"level", config.ThinkingLevel,
				"requestedEffort", reasoningEffort)
		} else {
			// Flash models support all levels including MEDIUM
			switch reasoningEffort {
			case "low":
				config.ThinkingLevel = genai.ThinkingLevelLow
			case "medium":
				config.ThinkingLevel = genai.ThinkingLevelMedium
			case "high":
				config.ThinkingLevel = genai.ThinkingLevelHigh
			default:
				config.ThinkingLevel = genai.ThinkingLevelLow
			}
			logging.Debug("[GEMINI] Using ThinkingLevel for Gemini 3 Flash",
				"level", config.ThinkingLevel,
				"effort", reasoningEffort)
		}
	} else if isGemini25 {
		// Gemini 2.5: Use ThinkingBudget (token count)
		// Based on API docs: range is 0-32,768 tokens
		var budget int32
		switch reasoningEffort {
		case "low":
			budget = 1024 // Minimal thinking
		case "medium":
			budget = 8192 // Moderate thinking
		case "high":
			budget = 16384 // Thorough thinking
		default:
			budget = 8192 // Default to medium
		}
		config.ThinkingBudget = &budget
		logging.Debug("[GEMINI] Using ThinkingBudget for Gemini 2.5",
			"budget", budget,
			"effort", reasoningEffort)
	}

	return config
}
