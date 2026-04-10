// Copyright (c) 2025 Reliant Labs
package openrouter

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/cache"
	"github.com/reliant-labs/reliant/internal/llm/drivers/openai"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/observability"
)

const Family models.Family = "openrouter"

const (
	openRouterDefaultURL = "https://openrouter.ai/api/v1"
	openRouterReferer    = "https://github.com/reliant-labs/reliant"
	openRouterTitle      = "Reliant"
)

// createClient is the driver factory function for the registry
func createClient(opts *llm.DriverOptions) (registry.Client, error) {
	// Apply OpenRouter-specific defaults
	if opts.BaseURL == "" {
		opts.BaseURL = openRouterDefaultURL
	}
	opts.ExtraHeaders = map[string]string{
		"HTTP-Referer": openRouterReferer,
		"X-Title":      openRouterTitle,
	}
	return NewClient(*opts), nil
}

// getOpenRouterAPIModel looks up the OpenRouter API model ID from the registry.
// It returns an error if the model is not found or has no openrouter provider.
func getOpenRouterAPIModel(modelID models.ModelID) (string, error) {
	reg := models.MustGetRegistry()
	def, ok := reg.GetDefinition(string(modelID))
	if !ok {
		return "", fmt.Errorf("model %s not found in registry", modelID)
	}

	for _, p := range def.Providers {
		if p.Driver == "openrouter" {
			return p.APIModel, nil
		}
	}
	return "", fmt.Errorf("model %s has no openrouter provider", modelID)
}

func init() {
	// Register models that have openrouter as a provider from the registry
	reg := models.MustGetRegistry()
	openrouterModels := reg.ListModelsByProvider("openrouter")
	m := make([]models.ModelID, 0, len(openrouterModels))
	for _, def := range openrouterModels {
		m = append(m, models.ModelID(def.ID))
	}
	models.RegisterDriverModels(Family, m)
	// Register the driver factory
	registry.RegisterDriver(Family, createClient)
}

type Client struct {
	*openai.OpenaiClient
}

// newOpenRouterRequest creates an HTTP POST request to the OpenRouter chat completions
// endpoint with all standard headers set.
func (c *Client) newOpenRouterRequest(ctx context.Context, body []byte) (*http.Request, error) {
	url := c.Options.BaseURL
	if url == "" {
		url = openRouterDefaultURL
	}
	url += "/chat/completions"

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setOpenRouterHeaders(req)
	return req, nil
}

// setOpenRouterHeaders sets the standard OpenRouter headers on an HTTP request.
func (c *Client) setOpenRouterHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Options.ApiKey)
	req.Header.Set("HTTP-Referer", openRouterReferer)
	req.Header.Set("X-Title", openRouterTitle)
	req.Header.Set("X-User", "reliant-"+getMachineIdentifier())

	// Add Anthropic beta header for cache control
	if strings.HasPrefix(c.Options.Model.APIModel, "anthropic/") {
		req.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
	}

	// Add any extra headers from options
	for key, value := range c.Options.ExtraHeaders {
		req.Header.Set(key, value)
	}
}

// mapFinishReason converts an OpenRouter finish reason string to our enum.
func mapFinishReason(reason string) message.FinishReason {
	switch reason {
	case "stop":
		return message.FinishReasonEndTurn
	case "length":
		return message.FinishReasonMaxTokens
	case "tool_calls":
		return message.FinishReasonToolUse
	case "content_filter":
		return message.FinishReasonError
	default:
		logging.Warn("OpenRouter unknown finish reason, treating as unknown", "finish_reason", reason)
		return message.FinishReasonUnknown
	}
}

// isRetryableStatus returns true if the HTTP status code warrants a retry.
func isRetryableStatus(code int) bool {
	return code == 429 || code == 500 || code == 502 || code == 503 || code == 529
}

// retryDelay calculates the delay before the next retry attempt using exponential
// backoff with 20% jitter. If the response includes a Retry-After header (in seconds),
// that value is used instead.
func retryDelay(attempt int, resp *http.Response) time.Duration {
	backoffMs := 2000 * (1 << (attempt - 1)) // 2s, 4s, 8s, ...
	jitterMs := int(float64(backoffMs) * 0.2)
	delayMs := backoffMs + jitterMs

	if resp != nil {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil {
				delayMs = secs * 1000
			}
		}
	}

	return time.Duration(delayMs) * time.Millisecond
}

// Name returns the name of the driver
func (c *Client) Name() string {
	return "openrouter"
}

func NewClient(opts llm.DriverOptions) *Client {
	// Look up the OpenRouter API model from the registry
	if apiModel, err := getOpenRouterAPIModel(opts.Model.ID); err == nil {
		opts.Model.APIModel = apiModel
	}
	return &Client{
		openai.NewClient(opts),
	}
}

// isAnthropicModel checks if the current model is an Anthropic model.
// Uses the API model prefix (e.g. "anthropic/claude-...") for reliable detection.
func (c *Client) isAnthropicModel() bool {
	return strings.HasPrefix(c.Options.Model.APIModel, "anthropic/")
}

// isGeminiModel checks if the current model is a Google Gemini model
// Gemini 3.x models require thought signatures for tool calls
func (c *Client) isGeminiModel() bool {
	// Check API model for OpenRouter format (google/gemini-*)
	apiModel := c.Options.Model.APIModel
	if strings.HasPrefix(apiModel, "google/") {
		return true
	}
	// Also check model ID for internal format
	modelStr := string(c.Options.Model.ID)
	return strings.Contains(strings.ToLower(modelStr), "gemini")
}

// convertMessagesForGemini converts messages with reasoning_details for Gemini models
// Gemini 3.x models require thought signatures to be preserved for tool calls
func (c *Client) convertMessagesForGemini(prompts []string, messages []message.Message) []map[string]interface{} {
	var result []map[string]interface{}

	// Add system messages
	for _, prompt := range prompts {
		if prompt != "" {
			result = append(result, map[string]interface{}{
				"role":    "system",
				"content": prompt,
			})
		}
	}

	// Process messages
	for _, msg := range messages {
		switch msg.Role {
		case message.User:
			userMsg := map[string]interface{}{
				"role": "user",
			}
			textParts := make([]string, 0)
			for _, tc := range msg.TextContents() {
				if tc.Text != "" {
					textParts = append(textParts, tc.Text)
				}
			}

			if len(msg.BinaryContent()) > 0 {
				// Multi-part content
				content := make([]map[string]interface{}, 0, len(textParts)+len(msg.BinaryContent()))
				for _, text := range textParts {
					content = append(content, map[string]interface{}{
						"type": "text",
						"text": text,
					})
				}
				for _, binaryContent := range msg.BinaryContent() {
					if binaryContent.MIMEType == "application/pdf" {
						content = append(content, map[string]interface{}{
							"type": "image_url",
							"image_url": map[string]string{
								"url": binaryContent.String("openrouter"),
							},
						})
					} else {
						content = append(content, map[string]interface{}{
							"type": "image_url",
							"image_url": map[string]string{
								"url": binaryContent.String("openrouter"),
							},
						})
					}
				}
				userMsg["content"] = content
			} else {
				userMsg["content"] = strings.Join(textParts, "\n\n")
			}

			result = append(result, userMsg)

		case message.Assistant:
			asstMsg := map[string]interface{}{
				"role":    "assistant",
				"content": msg.Content().String(),
			}

			toolCalls := msg.ToolCalls()
			if len(toolCalls) > 0 {
				var toolCallMaps []map[string]interface{}
				var reasoningDetails []map[string]interface{}

				for _, call := range toolCalls {
					toolCallMaps = append(toolCallMaps, map[string]interface{}{
						"id":   call.ID,
						"type": "function",
						"function": map[string]string{
							"name":      call.Name,
							"arguments": call.Input,
						},
					})

					// If tool call has a thought signature, add it to reasoning_details
					// The ID must match the tool call ID for Gemini to associate them
					// Format must match what OpenRouter sends: id, type, data, format, index
					if call.ThoughtSignature != "" {
						reasoningDetails = append(reasoningDetails, map[string]interface{}{
							"id":     call.ID,
							"type":   "reasoning.encrypted",
							"data":   call.ThoughtSignature,
							"format": "google-gemini-v1",
						})
					}
				}
				asstMsg["tool_calls"] = toolCallMaps

				// Include reasoning_details if any tool calls had thought signatures
				if len(reasoningDetails) > 0 {
					asstMsg["reasoning_details"] = reasoningDetails
				}
			}

			result = append(result, asstMsg)

		case message.Tool:
			// Each tool result becomes a separate message in OpenAI format
			for _, toolResult := range msg.ToolResults() {
				toolMsg := map[string]interface{}{
					"role":         "tool",
					"tool_call_id": toolResult.ToolCallID,
					"content":      toolResult.Content,
				}
				result = append(result, toolMsg)
				if len(toolResult.BinaryParts) > 0 {
					// OpenRouter Gemini uses OpenAI-format — inject binary parts as follow-up user message
					content := []map[string]interface{}{
						{"type": "text", "text": "Tool result attachments:"},
					}
					for _, bp := range toolResult.BinaryParts {
						content = append(content, map[string]interface{}{
							"type": "image_url",
							"image_url": map[string]string{
								"url": bp.String("openrouter"),
							},
						})
					}
					result = append(result, map[string]interface{}{
						"role":    "user",
						"content": content,
					})
				}
			}
		}
	}

	return result
}

// convertMessagesWithCacheControl overrides the OpenAI message conversion to add cache control for Anthropic models
func (c *Client) convertMessagesWithCacheControl(prompts []string, messages []message.Message) interface{} {
	// Gemini models need reasoning_details for thought signatures
	if c.isGeminiModel() {
		return c.convertMessagesForGemini(prompts, messages)
	}

	// Only Anthropic models need explicit cache control
	if !c.isAnthropicModel() || c.Options.DisableCache {
		return c.ConvertMessages(prompts, messages)
	}

	// Build messages with cache control for Anthropic models
	var result []map[string]interface{}

	// Add system messages with cache control
	for i, prompt := range prompts {
		if prompt != "" {
			sysMsg := map[string]interface{}{
				"role": "system",
			}

			// Use shared cache logic
			shouldCache := cache.ShouldCacheSystemPrompt(i, len(prompts), c.Options.DisableCache)

			if shouldCache {
				sysMsg["content"] = []map[string]interface{}{
					{
						"type": "text",
						"text": prompt,
						"cache_control": map[string]string{
							"type": "ephemeral",
						},
					},
				}
			} else {
				sysMsg["content"] = prompt
			}

			result = append(result, sysMsg)
		}
	}

	// Process messages - build without cache control first
	for _, msg := range messages {
		switch msg.Role {
		case message.User:
			userMsg := map[string]interface{}{
				"role": "user",
			}
			textParts := make([]string, 0)
			for _, tc := range msg.TextContents() {
				if tc.Text != "" {
					textParts = append(textParts, tc.Text)
				}
			}

			if len(msg.BinaryContent()) > 0 {
				// Multi-part content
				content := make([]map[string]interface{}, 0, len(textParts)+len(msg.BinaryContent()))
				for _, text := range textParts {
					content = append(content, map[string]interface{}{
						"type": "text",
						"text": text,
					})
				}
				for _, binaryContent := range msg.BinaryContent() {
					if binaryContent.MIMEType == "application/pdf" {
						content = append(content, map[string]interface{}{
							"type": "image_url",
							"image_url": map[string]string{
								"url": binaryContent.String("openrouter"),
							},
						})
					} else {
						content = append(content, map[string]interface{}{
							"type": "image_url",
							"image_url": map[string]string{
								"url": binaryContent.String("openrouter"),
							},
						})
					}
				}
				userMsg["content"] = content
			} else {
				// Simple text content
				userMsg["content"] = strings.Join(textParts, "\n\n")
			}

			result = append(result, userMsg)

		case message.Assistant:
			asstMsg := map[string]interface{}{
				"role":    "assistant",
				"content": msg.Content().String(),
			}

			if len(msg.ToolCalls()) > 0 {
				var toolCalls []map[string]interface{}
				for _, call := range msg.ToolCalls() {
					toolCalls = append(toolCalls, map[string]interface{}{
						"id":   call.ID,
						"type": "function",
						"function": map[string]string{
							"name":      call.Name,
							"arguments": call.Input,
						},
					})
				}
				asstMsg["tool_calls"] = toolCalls
			}

			result = append(result, asstMsg)

		case message.Tool:
			// Each tool result becomes a separate message in OpenAI format
			for _, toolResult := range msg.ToolResults() {
				toolMsg := map[string]interface{}{
					"role":         "tool",
					"tool_call_id": toolResult.ToolCallID,
					"content":      toolResult.Content,
				}
				result = append(result, toolMsg)
				if len(toolResult.BinaryParts) > 0 {
					// OpenAI-format tool messages don't support images — inject as follow-up user message
					content := []map[string]interface{}{
						{"type": "text", "text": "Tool result attachments:"},
					}
					for _, bp := range toolResult.BinaryParts {
						content = append(content, map[string]interface{}{
							"type": "image_url",
							"image_url": map[string]string{
								"url": bp.String("openrouter"),
							},
						})
					}
					result = append(result, map[string]interface{}{
						"role":    "user",
						"content": content,
					})
				}
			}
		}
	}

	// Apply cache control ONLY to the very last message to maintain proper ordering
	// This matches how Anthropic driver handles cache control
	if !c.Options.DisableCache && len(result) > 0 {
		lastMsg := result[len(result)-1]

		// Apply cache control based on message type
		if content, ok := lastMsg["content"].(string); ok {
			// Simple string content - convert to array format with cache control
			lastMsg["content"] = []map[string]interface{}{
				{
					"type": "text",
					"text": content,
					"cache_control": map[string]string{
						"type": "ephemeral",
					},
				},
			}
		} else if contentArray, ok := lastMsg["content"].([]map[string]interface{}); ok {
			// Already array format - add cache control to last content block
			if len(contentArray) > 0 {
				contentArray[len(contentArray)-1]["cache_control"] = map[string]string{
					"type": "ephemeral",
				}
			}
		}
	}

	return result
}

// SendMessages overrides the OpenAI implementation to handle:
// - Cache control for Anthropic models
// - Reasoning details (thought signatures) for Gemini models
func (c *Client) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) (*llm.DriverResponse, error) {
	start := time.Now()

	var resp *llm.DriverResponse
	var err error

	// Gemini models need custom handling for reasoning_details (thought signatures)
	if c.isGeminiModel() {
		resp, err = c.sendWithGeminiSupport(ctx, prompts, messages, tools)
	} else if c.isAnthropicModel() && !c.Options.DisableCache {
		// Anthropic models need explicit cache control via custom HTTP client
		resp, err = c.sendWithCacheControl(ctx, prompts, messages, tools)
	} else {
		// Other models use standard OpenAI implementation
		resp, err = c.OpenaiClient.SendMessages(ctx, prompts, messages, tools)
	}

	// Record observability metrics
	duration := time.Since(start).Seconds()
	if err != nil {
		observability.LLMRequestsTotal.WithLabelValues("openrouter", "error").Inc()
		observability.LLMRequestDuration.WithLabelValues("openrouter").Observe(duration)
		return nil, err
	}

	observability.LLMRequestsTotal.WithLabelValues("openrouter", "success").Inc()
	observability.LLMRequestDuration.WithLabelValues("openrouter").Observe(duration)
	if resp != nil {
		if resp.Usage.InputTokens > 0 {
			observability.LLMTokensTotal.WithLabelValues("openrouter", "input").Add(float64(resp.Usage.InputTokens))
		}
		if resp.Usage.OutputTokens > 0 {
			observability.LLMTokensTotal.WithLabelValues("openrouter", "output").Add(float64(resp.Usage.OutputTokens))
		}
	}

	return resp, nil
}

// StreamResponse overrides for models needing custom handling:
// - Anthropic models with cache control
// - Gemini models with reasoning_details (thought signatures)
func (c *Client) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) <-chan llm.DriverEvent {
	// Gemini models need custom handling for reasoning_details (thought signatures)
	if c.isGeminiModel() {
		return c.streamWithGeminiSupport(ctx, prompts, messages, tools)
	}

	// Anthropic models need explicit cache control
	if c.isAnthropicModel() && !c.Options.DisableCache {
		return c.streamWithCacheControl(ctx, prompts, messages, tools)
	}

	// Other models use standard OpenAI implementation
	return c.OpenaiClient.StreamResponse(ctx, prompts, messages, tools)
}

func (c *Client) Model() models.Model {
	return c.Options.Model
}

func (c *Client) ValidateKey(ctx context.Context) error {
	// Use GPT-5.4 Mini for validation (small, fast model available on OpenRouter)
	testMessages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Say 'test' and nothing else"},
			},
		},
	}

	// Create a temporary client with the small model
	validationOpts := c.Options
	registry := models.MustGetRegistry()
	if def, ok := registry.GetDefinition(string(models.GPT54Mini)); ok {
		validationOpts.Model = def.ToModel()
	}
	validationOpts.MaxTokens = 20

	validationClient := NewClient(validationOpts)

	_, err := validationClient.SendMessages(ctx, []string{}, testMessages, []tools.Tool{})
	return err
}
