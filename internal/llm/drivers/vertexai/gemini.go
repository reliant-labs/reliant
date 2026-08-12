// Copyright (c) 2025 Reliant Labs
package vertexai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/invopop/jsonschema"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"google.golang.org/genai"
)

// sendMessagesGemini sends messages using the Gemini provider
func (c *VertexAIClient) sendMessagesGemini(ctx context.Context, prompts []string, messages []message.Message, toolsList []tools.Tool) (*llm.DriverResponse, error) {
	// Convert messages to Gemini format
	contents := c.convertMessagesToGemini(messages)

	// Build configuration
	config := c.buildGeminiConfig(prompts, toolsList)

	// Generate content
	resp, err := c.geminiClient.Models.GenerateContent(ctx, c.options.Model.APIModel, contents, config)
	if err != nil {
		return nil, fmt.Errorf("gemini API error: %w", err)
	}

	return c.convertGeminiResponse(resp)
}

// streamResponseGemini streams responses using the Gemini provider
func (c *VertexAIClient) streamResponseGemini(ctx context.Context, prompts []string, messages []message.Message, toolsList []tools.Tool) <-chan llm.DriverEvent {
	eventChan := make(chan llm.DriverEvent)

	go func() {
		defer close(eventChan)

		contents := c.convertMessagesToGemini(messages)
		config := c.buildGeminiConfig(prompts, toolsList)

		stream := c.geminiClient.Models.GenerateContentStream(ctx, c.options.Model.APIModel, contents, config)

		accumulated := &llm.DriverResponse{
			Content:   "",
			ToolCalls: []message.ToolCall{},
		}

		for resp, err := range stream {
			if err != nil {
				eventChan <- llm.DriverEvent{Type: llm.EventError, Error: err}
				return
			}

			// Process the response
			if len(resp.Candidates) > 0 {
				candidate := resp.Candidates[0]

				// Check for error finish reasons (e.g., MALFORMED_FUNCTION_CALL)
				if isErrorFinishReason(candidate.FinishReason) {
					errMsg := fmt.Sprintf("gemini error: %s", candidate.FinishReason)
					if candidate.FinishMessage != "" {
						errMsg = fmt.Sprintf("%s: %s", errMsg, candidate.FinishMessage)
					}
					eventChan <- llm.DriverEvent{Type: llm.EventError, Error: errors.New(errMsg)}
					return
				}

				// Process content parts (handle nil Content)
				if candidate.Content != nil {
					for _, part := range candidate.Content.Parts {
						if part.Text != "" {
							text := part.Text
							accumulated.Content += text
							eventChan <- llm.DriverEvent{
								Type:    llm.EventContentDelta,
								Content: text,
							}
						}

						if part.FunctionCall != nil {
							// Handle function calls
							argsJSON, _ := json.Marshal(part.FunctionCall.Args)
							toolCall := message.ToolCall{
								ID:       part.FunctionCall.Name,
								Name:     part.FunctionCall.Name,
								Input:    string(argsJSON),
								Finished: true,
							}
							accumulated.ToolCalls = append(accumulated.ToolCalls, toolCall)
							eventChan <- llm.DriverEvent{
								Type:     llm.EventToolUseStart,
								ToolCall: &toolCall,
							}
						}
					}
				}

				// Update usage if available
				if resp.UsageMetadata != nil {
					// TokenCount = TotalTokenCount (prompt + response + tool tokens)
					accumulated.Usage = llm.TokenUsage{
						TokenCount: int64(resp.UsageMetadata.TotalTokenCount),
					}
				}

				// Set finish reason
				if candidate.FinishReason != genai.FinishReasonUnspecified {
					accumulated.FinishReason = c.convertGeminiFinishReason(candidate.FinishReason)
				}
			}
		}

		// Send complete event
		eventChan <- llm.DriverEvent{
			Type:     llm.EventComplete,
			Response: accumulated,
		}
	}()

	return eventChan
}

// buildGeminiConfig builds the generation configuration
func (c *VertexAIClient) buildGeminiConfig(prompts []string, toolsList []tools.Tool) *genai.GenerateContentConfig {
	config := &genai.GenerateContentConfig{}

	// Set system instruction
	if len(prompts) > 0 {
		var parts []*genai.Part
		for _, prompt := range prompts {
			if prompt != "" {
				parts = append(parts, &genai.Part{Text: prompt})
			}
		}
		if len(parts) > 0 {
			config.SystemInstruction = &genai.Content{Parts: parts}
		}
	}

	// Configure generation settings
	config.MaxOutputTokens = int32(c.options.MaxTokens)

	if c.options.Temperature != nil {
		temp := float32(*c.options.Temperature)
		config.Temperature = &temp
	}

	// Configure thinking based on model version and reasoning effort
	if c.options.ReasoningEffort != "" {
		thinkingConfig := c.buildThinkingConfig(c.options.Model.APIModel, c.options.ReasoningEffort)
		if thinkingConfig != nil {
			config.ThinkingConfig = thinkingConfig
		}
	}

	// Add tools if provided
	if len(toolsList) > 0 {
		config.Tools = []*genai.Tool{c.convertTools(toolsList)}
	}

	// Set safety settings based on options
	if c.options.SafetyLevel != "" {
		config.SafetySettings = c.getSafetySettings()
	}

	return config
}

// convertMessagesToGemini converts internal messages to Gemini format
func (c *VertexAIClient) convertMessagesToGemini(messages []message.Message) []*genai.Content {
	var contents []*genai.Content

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

			// Add binary content (images, etc.)
			for _, binaryContent := range msg.BinaryContent() {
				parts = append(parts, &genai.Part{
					InlineData: &genai.Blob{
						MIMEType: binaryContent.MIMEType,
						Data:     binaryContent.Data,
					},
				})
			}

			contents = append(contents, &genai.Content{
				Role:  string(genai.RoleUser),
				Parts: parts,
			})

		case message.System:
			// Gemini takes system instructions via SystemInstruction, so a
			// System message in HISTORY (compaction summary, branch note,
			// mailbox envelope) is delivered as a user turn instead — the same
			// treatment the direct Gemini driver gives it. Without this case it
			// falls through the switch and is dropped.
			if systemText := strings.TrimSpace(msg.Content().String()); systemText != "" {
				contents = append(contents, &genai.Content{
					Role:  string(genai.RoleUser),
					Parts: []*genai.Part{{Text: fmt.Sprintf("<system>\n%s\n</system>", systemText)}},
				})
			}

		case message.Assistant:
			var parts []*genai.Part

			if textContent := msg.Content().String(); textContent != "" {
				parts = append(parts, &genai.Part{Text: textContent})
			}

			// Add tool calls
			for _, toolCall := range msg.ToolCalls() {
				var args map[string]any
				if toolCall.Input != "" {
					_ = json.Unmarshal([]byte(toolCall.Input), &args) //nolint:errcheck // Parsing stored JSON, empty map on failure is acceptable
				}
				parts = append(parts, &genai.Part{
					FunctionCall: &genai.FunctionCall{
						Name: toolCall.Name,
						Args: args,
					},
				})
			}

			if len(parts) > 0 {
				contents = append(contents, &genai.Content{
					Role:  string(genai.RoleModel),
					Parts: parts,
				})
			}

		case message.Tool:
			// Add tool responses
			// In Gemini, function responses go in a "user" role message
			for _, result := range msg.ToolResults() {
				var response map[string]any
				parsed, err := parseJSONToMap(result.Content)
				if err == nil {
					response = parsed
				} else {
					response = map[string]any{"result": result.Content}
				}

				resultParts := []*genai.Part{
					{
						FunctionResponse: &genai.FunctionResponse{
							Name:     result.Name,
							Response: response,
						},
					},
				}
				for _, bp := range result.BinaryParts {
					resultParts = append(resultParts, &genai.Part{
						InlineData: &genai.Blob{
							MIMEType: bp.MIMEType,
							Data:     bp.Data,
						},
					})
				}
				contents = append(contents, &genai.Content{
					Role:  string(genai.RoleUser),
					Parts: resultParts,
				})
			}
		}
	}

	return contents
}

// convertGeminiResponse converts Gemini response to internal format
func (c *VertexAIClient) convertGeminiResponse(resp *genai.GenerateContentResponse) (*llm.DriverResponse, error) {
	if len(resp.Candidates) == 0 {
		if resp.PromptFeedback != nil {
			return nil, fmt.Errorf("response blocked: %v", resp.PromptFeedback.BlockReason)
		}
		return nil, errors.New("no candidates in response")
	}

	candidate := resp.Candidates[0]

	// Check for error finish reasons (e.g., MALFORMED_FUNCTION_CALL)
	// These should be treated as errors so the system can retry
	if isErrorFinishReason(candidate.FinishReason) {
		errMsg := fmt.Sprintf("gemini error: %s", candidate.FinishReason)
		if candidate.FinishMessage != "" {
			errMsg = fmt.Sprintf("%s: %s", errMsg, candidate.FinishMessage)
		}
		return nil, errors.New(errMsg)
	}

	response := &llm.DriverResponse{
		Content:   "",
		ToolCalls: []message.ToolCall{},
	}

	// Extract content and tool calls (handle nil Content)
	if candidate.Content != nil {
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				response.Content += part.Text
			}

			if part.FunctionCall != nil {
				argsJSON, _ := json.Marshal(part.FunctionCall.Args)
				response.ToolCalls = append(response.ToolCalls, message.ToolCall{
					ID:       part.FunctionCall.Name,
					Name:     part.FunctionCall.Name,
					Input:    string(argsJSON),
					Finished: true,
				})
			}
		}
	}

	// Set usage metadata
	if resp.UsageMetadata != nil {
		// TokenCount = TotalTokenCount (prompt + response + tool tokens)
		response.Usage = llm.TokenUsage{
			TokenCount: int64(resp.UsageMetadata.TotalTokenCount),
		}
	}

	// Set finish reason
	if candidate.FinishReason != genai.FinishReasonUnspecified {
		response.FinishReason = c.convertGeminiFinishReason(candidate.FinishReason)
	}

	return response, nil
}

// convertTools converts internal tools to Gemini format
func (c *VertexAIClient) convertTools(toolsList []tools.Tool) *genai.Tool {
	var functionDeclarations []*genai.FunctionDeclaration

	for _, tool := range toolsList {
		schema := tool.ParamSchema()

		// Convert schema properties
		properties := make(map[string]*genai.Schema)

		// Iterate over the ordered map
		for pair := schema.Properties.Oldest(); pair != nil; pair = pair.Next() {
			key := pair.Key
			value := pair.Value
			properties[key] = c.convertSchemaProperty(value)
		}

		functionDeclarations = append(functionDeclarations, &genai.FunctionDeclaration{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters: &genai.Schema{
				Type:       genai.TypeObject,
				Properties: properties,
				Required:   schema.Required,
			},
		})
	}

	return &genai.Tool{FunctionDeclarations: functionDeclarations}
}

// convertSchemaProperty converts a jsonschema.Schema to Gemini Schema format
func (c *VertexAIClient) convertSchemaProperty(prop *jsonschema.Schema) *genai.Schema {
	schema := &genai.Schema{}

	// Convert type
	if prop.Type != "" {
		switch prop.Type {
		case "string":
			schema.Type = genai.TypeString
		case "number", "integer":
			schema.Type = genai.TypeNumber
		case "boolean":
			schema.Type = genai.TypeBoolean
		case "array":
			schema.Type = genai.TypeArray
		case "object":
			schema.Type = genai.TypeObject
		default:
			schema.Type = genai.TypeString
		}
	} else {
		// Default to string if type is not specified
		schema.Type = genai.TypeString
	}

	// Add description if present
	if prop.Description != "" {
		schema.Description = prop.Description
	}

	return schema
}

// convertGeminiFinishReason converts Gemini finish reason to internal format
func (c *VertexAIClient) convertGeminiFinishReason(reason genai.FinishReason) message.FinishReason {
	switch reason {
	case genai.FinishReasonStop:
		return message.FinishReasonEndTurn
	case genai.FinishReasonMaxTokens:
		return message.FinishReasonMaxTokens
	case genai.FinishReasonMalformedFunctionCall, genai.FinishReasonUnexpectedToolCall:
		return message.FinishReasonToolUseError
	case genai.FinishReasonSafety, genai.FinishReasonRecitation, genai.FinishReasonBlocklist,
		genai.FinishReasonProhibitedContent, genai.FinishReasonSPII:
		return message.FinishReasonError
	default:
		return message.FinishReasonUnknown
	}
}

// isErrorFinishReason returns true if the finish reason should be treated as an error
func isErrorFinishReason(reason genai.FinishReason) bool {
	switch reason {
	case genai.FinishReasonMalformedFunctionCall,
		genai.FinishReasonUnexpectedToolCall,
		genai.FinishReasonSafety,
		genai.FinishReasonRecitation,
		genai.FinishReasonBlocklist,
		genai.FinishReasonProhibitedContent,
		genai.FinishReasonSPII:
		return true
	default:
		return false
	}
}

// getSafetySettings returns safety settings based on configuration
func (c *VertexAIClient) getSafetySettings() []*genai.SafetySetting {
	// Use the correct threshold constants
	threshold := genai.HarmBlockThresholdBlockMediumAndAbove

	switch c.options.SafetyLevel {
	case "low":
		threshold = genai.HarmBlockThresholdBlockOnlyHigh
	case "high":
		threshold = genai.HarmBlockThresholdBlockLowAndAbove
	}

	return []*genai.SafetySetting{
		{Category: genai.HarmCategoryHarassment, Threshold: threshold},
		{Category: genai.HarmCategoryHateSpeech, Threshold: threshold},
		{Category: genai.HarmCategorySexuallyExplicit, Threshold: threshold},
		{Category: genai.HarmCategoryDangerousContent, Threshold: threshold},
	}
}

// validateGeminiKey validates Gemini configuration
func (c *VertexAIClient) validateGeminiKey(ctx context.Context) error {
	if c.geminiClient == nil {
		return fmt.Errorf("gemini client not initialized")
	}

	// Try a simple request with minimal tokens
	contents := []*genai.Content{genai.NewContentFromText("Say 'test' and nothing else", genai.RoleUser)}
	config := &genai.GenerateContentConfig{
		MaxOutputTokens: 10,
	}

	_, err := c.geminiClient.Models.GenerateContent(ctx, c.options.Model.APIModel, contents, config)
	return err
}

// parseJSONToMap parses JSON string to map
func parseJSONToMap(jsonStr string) (map[string]any, error) {
	var result map[string]any
	err := json.Unmarshal([]byte(jsonStr), &result)
	return result, err
}

// buildThinkingConfig creates appropriate thinking configuration based on model version
// Gemini 3.x Flash models: Use ThinkingLevel (supports minimal/low/medium/high)
// Gemini 3.x Pro models: Use ThinkingLevel (only supports low/high - NO medium)
// Gemini 2.5 models: Use ThinkingBudget (token count)
// Older models (2.0, 1.5): No thinking support (returns nil)
func (c *VertexAIClient) buildThinkingConfig(modelName, reasoningEffort string) *genai.ThinkingConfig {
	// Check if this is a Gemini 3.x model
	isGemini3 := contains(modelName, "gemini-3")

	// Check if this is a Gemini 2.5 model
	isGemini25 := contains(modelName, "gemini-2.5")

	// Check model variants
	isFlash := contains(modelName, "flash")
	isPro := contains(modelName, "pro")
	isPreview := contains(modelName, "preview")

	// WORKAROUND: Gemini 3 Flash Preview crashes with 500 "Internal error" when
	// explicit thinkingConfig is combined with function calling (tool use).
	// Skip sending thinkingConfig for Flash Preview to let Google's API use
	// default dynamic thinking instead, which is more stable.
	// This appears to be a Google API bug - docs say thinking + tools is supported.
	// TODO: Re-enable when Google stabilizes Flash Preview with thinking + tools
	if isGemini3 && isFlash && isPreview {
		return nil
	}

	// Older models don't support thinking
	if !isGemini3 && !isGemini25 {
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
	}

	return config
}

// contains checks if a string contains any of the substrings
func contains(s string, substrs ...string) bool {
	for _, substr := range substrs {
		if len(s) >= len(substr) && s[:len(substr)] == substr {
			return true
		}
	}
	return false
}
