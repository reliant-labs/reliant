// Copyright (c) 2025 Reliant Labs
package openrouter

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/cache"
	"github.com/reliant-labs/reliant/internal/llm/drivers/openai"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

const Family models.Family = "openrouter"

// createClient is the driver factory function for the registry
func createClient(opts *llm.DriverOptions) (registry.Client, error) {
	// Apply OpenRouter-specific defaults
	if opts.BaseURL == "" {
		opts.BaseURL = "https://openrouter.ai/api/v1"
	}
	opts.ExtraHeaders = map[string]string{
		"HTTP-Referer": "reliant-labs.io",
		"X-Title":      "Reliant",
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

// isAnthropicModel checks if the current model is an Anthropic model
func (c *Client) isAnthropicModel() bool {
	modelStr := string(c.Options.Model.ID)
	return strings.Contains(modelStr, "claude") || strings.Contains(modelStr, "haiku") ||
		strings.Contains(modelStr, "sonnet") || strings.Contains(modelStr, "opus")
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
					content = append(content, map[string]interface{}{
						"type": "image_url",
						"image_url": map[string]string{
							"url": binaryContent.String("openrouter"),
						},
					})
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
					content = append(content, map[string]interface{}{
						"type": "image_url",
						"image_url": map[string]string{
							"url": binaryContent.String("openrouter"),
						},
					})
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
	// Gemini models need custom handling for reasoning_details (thought signatures)
	if c.isGeminiModel() {
		return c.sendWithGeminiSupport(ctx, prompts, messages, tools)
	}

	// Anthropic models need explicit cache control via custom HTTP client
	if c.isAnthropicModel() && !c.Options.DisableCache {
		return c.sendWithCacheControl(ctx, prompts, messages, tools)
	}

	// Other models use standard OpenAI implementation
	return c.OpenaiClient.SendMessages(ctx, prompts, messages, tools)
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
