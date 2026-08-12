// Copyright (c) 2025 Reliant Labs
package openrouter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// ReasoningDetail represents a reasoning detail from OpenRouter responses
// Used by Gemini and other thinking models to preserve thought context
type ReasoningDetail struct {
	Type      string `json:"type"`                // "reasoning.summary", "reasoning.encrypted", "reasoning.text"
	ID        string `json:"id,omitempty"`        // Links to tool call ID for encrypted type
	Format    string `json:"format,omitempty"`    // e.g., "anthropic-claude-v1", "openai-responses-v1"
	Index     int    `json:"index,omitempty"`     // Sequential index
	Data      string `json:"data,omitempty"`      // Encrypted reasoning data (for reasoning.encrypted)
	Text      string `json:"text,omitempty"`      // Plain text reasoning (for reasoning.text)
	Summary   string `json:"summary,omitempty"`   // Summary text (for reasoning.summary)
	Signature string `json:"signature,omitempty"` // Optional signature for text type
}

// ReasoningConfig represents the reasoning configuration for OpenRouter API
// Used by Gemini and other thinking models to control reasoning effort
type ReasoningConfig struct {
	Effort string `json:"effort,omitempty"` // "low", "medium", "high" (provider/model dependent)
}

// OpenRouterRequest represents the request structure for OpenRouter API
type OpenRouterRequest struct {
	Model       string                   `json:"model"`
	Messages    []map[string]interface{} `json:"messages"`
	Tools       []map[string]interface{} `json:"tools,omitempty"`
	Temperature *float64                 `json:"temperature,omitempty"`
	MaxTokens   int64                    `json:"max_tokens,omitempty"`
	Stream      bool                     `json:"stream"`
	Reasoning   *ReasoningConfig         `json:"reasoning,omitempty"`
}

// OpenRouterResponse represents the response structure from OpenRouter API
type OpenRouterResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role             string            `json:"role"`
			Content          string            `json:"content"`
			ReasoningDetails []ReasoningDetail `json:"reasoning_details,omitempty"` // Gemini thought signatures
			ToolCalls        []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
		// Cache-specific fields
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

// getMachineIdentifier returns a consistent machine identifier for OpenRouter user tracking
func getMachineIdentifier() string {
	// Try to get hostname first
	hostname, err := os.Hostname()
	if err != nil {
		// Fallback to UUID if hostname fails
		return uuid.New().String()
	}

	// Create a hash of the hostname for privacy
	hash := sha256.Sum256([]byte(hostname))
	return hex.EncodeToString(hash[:])[:16] // Use first 16 chars
}

// sendWithCacheControl sends a request with cache control fields preserved
func (c *Client) sendWithCacheControl(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) (*llm.DriverResponse, error) {
	// Convert messages with cache control (this already tracks the 4 block limit)
	convertedMessages := c.convertMessagesWithCacheControl(prompts, messages)

	// Convert tools with cache control for the last tool
	var convertedTools []map[string]interface{}
	if len(tools) > 0 {
		openaiTools := c.ConvertTools(tools)
		convertedTools = make([]map[string]interface{}, len(openaiTools))

		for i, tool := range openaiTools {
			// Convert tool to map via JSON round-trip
			toolJSON, _ := json.Marshal(tool)
			var toolMap map[string]interface{}
			_ = json.Unmarshal(toolJSON, &toolMap) //nolint:errcheck // Round-trip marshal/unmarshal always succeeds

			// Cache the last tool using shared logic
			if i == len(tools)-1 && !c.Options.DisableCache {
				// Add cache control to the last tool
				if function, ok := toolMap["function"].(map[string]interface{}); ok {
					function["cache_control"] = map[string]string{
						"type": "ephemeral",
					}
				}
			}

			convertedTools[i] = toolMap
		}
	}

	// Build request
	request := OpenRouterRequest{
		Model:       c.Options.Model.APIModel,
		Messages:    convertedMessages.([]map[string]interface{}),
		Temperature: c.Options.Temperature,
		MaxTokens:   c.Options.MaxTokens,
		Stream:      false,
	}

	if len(convertedTools) > 0 {
		request.Tools = convertedTools
	}

	// Marshal request to JSON
	requestBody, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Log the request for debugging
	bodyLen := len(requestBody)
	previewLen := 500
	if bodyLen < previewLen {
		previewLen = bodyLen
	}
	logging.Debug("OpenRouter request with cache control",
		"model", request.Model,
		"hasTools", len(convertedTools) > 0,
		"messageCount", len(request.Messages),
		"bodyPreview", string(requestBody)[:previewLen])

	// Send request with retry logic for transient errors
	httpClient := llm.ResilientHTTPClient()
	var body []byte
	for attempt := 1; ; attempt++ {
		httpReq, err := c.newOpenRouterRequest(ctx, requestBody)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		resp, err := httpClient.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("failed to send request: %w", err)
		}

		body, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read response: %w", err)
		}

		bodyLen2 := len(body)
		previewLen2 := 500
		if bodyLen2 < previewLen2 {
			previewLen2 = bodyLen2
		}
		logging.Debug("OpenRouter response", "status", resp.StatusCode, "bodySize", bodyLen2, "preview", string(body[:previewLen2]))

		if resp.StatusCode == http.StatusOK {
			break
		}

		if !isRetryableStatus(resp.StatusCode) || attempt > models.MaxRetries {
			return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
		}

		delay := retryDelay(attempt, resp)
		logging.Warn("OpenRouter retrying request",
			"status", resp.StatusCode,
			"attempt", attempt,
			"maxRetries", models.MaxRetries,
			"delay", delay)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
			continue
		}
	}

	// Parse response
	var openRouterResp OpenRouterResponse
	if err := json.Unmarshal(body, &openRouterResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Check for API errors
	if openRouterResp.Error != nil {
		return nil, fmt.Errorf("API error: %s", openRouterResp.Error.Message)
	}

	// Convert to driver response
	if len(openRouterResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	choice := openRouterResp.Choices[0]

	// Build a map of reasoning_details by ID for Gemini thought signatures
	// The ID in reasoning_details links to the tool call ID
	reasoningByID := make(map[string]ReasoningDetail)
	for _, rd := range choice.Message.ReasoningDetails {
		if rd.ID != "" {
			reasoningByID[rd.ID] = rd
			logging.Debug("OpenRouter reasoning detail found",
				"id", rd.ID,
				"type", rd.Type,
				"hasData", rd.Data != "")
		}
	}

	// Convert tool calls with thought signatures
	var toolCalls []message.ToolCall
	for _, tc := range choice.Message.ToolCalls {
		toolCall := message.ToolCall{
			ID:       tc.ID,
			Name:     tc.Function.Name,
			Input:    tc.Function.Arguments,
			Type:     tc.Type,
			Finished: true,
		}

		// Associate reasoning_details with this tool call
		// OpenRouter uses the tool call ID as the reasoning detail ID
		if rd, ok := reasoningByID[tc.ID]; ok {
			// For encrypted type, the data field contains the thought signature
			if rd.Type == "reasoning.encrypted" && rd.Data != "" {
				toolCall.ThoughtSignature = rd.Data
				logging.Debug("OpenRouter associated thought signature with tool call",
					"toolCallID", tc.ID,
					"toolName", tc.Function.Name,
					"signatureLength", len(rd.Data))
			}
		}

		toolCalls = append(toolCalls, toolCall)
	}

	// Determine finish reason
	finishReason := mapFinishReason(choice.FinishReason)
	if len(toolCalls) > 0 {
		finishReason = message.FinishReasonToolUse
	}

	// Calculate token usage with cache information
	inputTokens := openRouterResp.Usage.PromptTokens
	outputTokens := openRouterResp.Usage.CompletionTokens
	cachedTokens := openRouterResp.Usage.PromptTokensDetails.CachedTokens
	cacheCreationTokens := openRouterResp.Usage.CacheCreationInputTokens
	cacheReadTokens := openRouterResp.Usage.CacheReadInputTokens

	// Log usage details for debugging
	logging.Debug("OpenRouter token usage parsing",
		"promptTokens", inputTokens,
		"cachedTokens", cachedTokens,
		"cacheCreationInputTokens", cacheCreationTokens,
		"cacheReadInputTokens", cacheReadTokens,
		"completionTokens", outputTokens)

	return &llm.DriverResponse{
		Content:      choice.Message.Content,
		ToolCalls:    toolCalls,
		FinishReason: finishReason,
		Usage: llm.TokenUsage{
			TokenCount:   int64(openRouterResp.Usage.TotalTokens),
			InputTokens:  int64(inputTokens),
			OutputTokens: int64(outputTokens),
			// Already parsed above for the debug line; carry them out so the
			// cache split survives past this function.
			CacheReadInputTokens:     int64(cacheReadTokens),
			CacheCreationInputTokens: int64(cacheCreationTokens),
		},
	}, nil
}

// sendWithGeminiSupport sends a request with reasoning_details support for Gemini models
// Gemini 3.x models require thought signatures to be preserved for tool calls
func (c *Client) sendWithGeminiSupport(ctx context.Context, prompts []string, messages []message.Message, toolList []tools.Tool) (*llm.DriverResponse, error) {
	// Convert messages with Gemini-specific handling (includes reasoning_details)
	convertedMessages := c.convertMessagesForGemini(prompts, messages)

	// Convert tools to OpenAI format
	var convertedTools []map[string]interface{}
	if len(toolList) > 0 {
		openaiTools := c.ConvertTools(toolList)
		convertedTools = make([]map[string]interface{}, len(openaiTools))
		for i, tool := range openaiTools {
			toolJSON, _ := json.Marshal(tool)
			var toolMap map[string]interface{}
			_ = json.Unmarshal(toolJSON, &toolMap) //nolint:errcheck // Round-trip marshal/unmarshal always succeeds
			convertedTools[i] = toolMap
		}
	}

	// Build request
	request := OpenRouterRequest{
		Model:       c.Options.Model.APIModel,
		Messages:    convertedMessages,
		Temperature: c.Options.Temperature,
		MaxTokens:   c.Options.MaxTokens,
		Stream:      false,
	}

	if len(convertedTools) > 0 {
		request.Tools = convertedTools
	}

	// Add reasoning config for Gemini thinking models
	if c.Options.ReasoningEffort != "" && c.Options.ReasoningEffort != "disabled" {
		request.Reasoning = &ReasoningConfig{
			Effort: c.Options.ReasoningEffort,
		}
		logging.Debug("OpenRouter Gemini request with reasoning",
			"effort", c.Options.ReasoningEffort)
	}

	// Marshal request to JSON
	requestBody, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Log the request for debugging
	bodyLen := len(requestBody)
	previewLen := 500
	if bodyLen < previewLen {
		previewLen = bodyLen
	}
	logging.Debug("OpenRouter Gemini request",
		"model", request.Model,
		"hasTools", len(convertedTools) > 0,
		"messageCount", len(request.Messages),
		"hasReasoning", request.Reasoning != nil,
		"bodyPreview", string(requestBody)[:previewLen])

	// Send request with retry logic for transient errors
	httpClient := llm.ResilientHTTPClient()
	var body []byte
	for attempt := 1; ; attempt++ {
		httpReq, err := c.newOpenRouterRequest(ctx, requestBody)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		resp, err := httpClient.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("failed to send request: %w", err)
		}

		body, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read response: %w", err)
		}

		bodyLen2 := len(body)
		previewLen2 := 500
		if bodyLen2 < previewLen2 {
			previewLen2 = bodyLen2
		}
		logging.Debug("OpenRouter Gemini response", "status", resp.StatusCode, "bodySize", bodyLen2, "preview", string(body[:previewLen2]))

		if resp.StatusCode == http.StatusOK {
			break
		}

		if !isRetryableStatus(resp.StatusCode) || attempt > models.MaxRetries {
			return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
		}

		delay := retryDelay(attempt, resp)
		logging.Warn("OpenRouter retrying Gemini request",
			"status", resp.StatusCode,
			"attempt", attempt,
			"maxRetries", models.MaxRetries,
			"delay", delay)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
			continue
		}
	}

	// Parse response
	var openRouterResp OpenRouterResponse
	if err := json.Unmarshal(body, &openRouterResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Check for API errors
	if openRouterResp.Error != nil {
		return nil, fmt.Errorf("API error: %s", openRouterResp.Error.Message)
	}

	// Convert to driver response
	if len(openRouterResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	choice := openRouterResp.Choices[0]

	// Build a map of reasoning_details by ID for Gemini thought signatures
	reasoningByID := make(map[string]ReasoningDetail)
	for _, rd := range choice.Message.ReasoningDetails {
		if rd.ID != "" {
			reasoningByID[rd.ID] = rd
			logging.Debug("OpenRouter Gemini reasoning detail found",
				"id", rd.ID,
				"type", rd.Type,
				"hasData", rd.Data != "")
		}
	}

	// Convert tool calls with thought signatures
	var toolCalls []message.ToolCall
	for _, tc := range choice.Message.ToolCalls {
		toolCall := message.ToolCall{
			ID:       tc.ID,
			Name:     tc.Function.Name,
			Input:    tc.Function.Arguments,
			Type:     tc.Type,
			Finished: true,
		}

		// Associate reasoning_details with this tool call
		if rd, ok := reasoningByID[tc.ID]; ok {
			if rd.Type == "reasoning.encrypted" && rd.Data != "" {
				toolCall.ThoughtSignature = rd.Data
				logging.Debug("OpenRouter Gemini associated thought signature with tool call",
					"toolCallID", tc.ID,
					"toolName", tc.Function.Name,
					"signatureLength", len(rd.Data))
			}
		}

		toolCalls = append(toolCalls, toolCall)
	}

	// Determine finish reason
	finishReason := mapFinishReason(choice.FinishReason)
	if len(toolCalls) > 0 {
		finishReason = message.FinishReasonToolUse
	}

	return &llm.DriverResponse{
		Content:      choice.Message.Content,
		ToolCalls:    toolCalls,
		FinishReason: finishReason,
		Usage: llm.TokenUsage{
			TokenCount:   int64(openRouterResp.Usage.TotalTokens),
			InputTokens:  int64(openRouterResp.Usage.PromptTokens),
			OutputTokens: int64(openRouterResp.Usage.CompletionTokens),
		},
	}, nil
}
