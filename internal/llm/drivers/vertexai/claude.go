// Copyright (c) 2025 Reliant Labs
package vertexai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/cache"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
	"golang.org/x/oauth2/google"
)

// Claude API structures for Vertex AI
type claudeMessage struct {
	Role    string               `json:"role"`
	Content []claudeContentBlock `json:"content"`
}

type claudeContentBlock struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text,omitempty"`
	Source       *claudeImageSource     `json:"source,omitempty"`
	ToolUseID    string                 `json:"tool_use_id,omitempty"`
	Content      interface{}            `json:"content,omitempty"`
	ID           string                 `json:"id,omitempty"`
	Name         string                 `json:"name,omitempty"`
	Input        map[string]interface{} `json:"input,omitempty"`
	IsError      bool                   `json:"is_error,omitempty"`
	CacheControl *claudeCacheControl    `json:"cache_control,omitempty"`
}

type claudeImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type claudeCacheControl struct {
	Type string `json:"type"`
}

type claudeSystemPrompt struct {
	Type         string              `json:"type"`
	Text         string              `json:"text"`
	CacheControl *claudeCacheControl `json:"cache_control,omitempty"`
}

type claudeTool struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description,omitempty"`
	InputSchema  map[string]interface{} `json:"input_schema"`
	CacheControl *claudeCacheControl    `json:"cache_control,omitempty"`
}

type claudeRequest struct {
	AnthropicVersion string               `json:"anthropic_version"`
	Messages         []claudeMessage      `json:"messages"`
	MaxTokens        int                  `json:"max_tokens"`
	System           []claudeSystemPrompt `json:"system,omitempty"`
	Tools            []claudeTool         `json:"tools,omitempty"`
	Temperature      *float64             `json:"temperature,omitempty"`
	Stream           bool                 `json:"stream,omitempty"`
}

type claudeResponse struct {
	ID           string               `json:"id"`
	Type         string               `json:"type"`
	Role         string               `json:"role"`
	Content      []claudeContentBlock `json:"content"`
	Model        string               `json:"model"`
	StopReason   string               `json:"stop_reason"`
	StopSequence string               `json:"stop_sequence,omitempty"`
	Usage        claudeUsage          `json:"usage"`
}

type claudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

type claudeStreamEvent struct {
	Type         string              `json:"type"`
	Message      *claudeResponse     `json:"message,omitempty"`
	Index        int                 `json:"index,omitempty"`
	ContentBlock *claudeContentBlock `json:"content_block,omitempty"`
	Delta        *claudeStreamDelta  `json:"delta,omitempty"`
	Usage        *claudeUsage        `json:"usage,omitempty"`
}

type claudeStreamDelta struct {
	Type         string `json:"type"`
	Text         string `json:"text,omitempty"`
	PartialJSON  string `json:"partial_json,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
	StopSequence string `json:"stop_sequence,omitempty"`
}

// sendMessagesClaude sends messages using the Claude provider via Vertex AI
func (c *VertexAIClient) sendMessagesClaude(ctx context.Context, prompts []string, messages []message.Message, toolsList []tools.Tool) (*llm.DriverResponse, error) {
	// Build the request
	req := c.buildClaudeRequest(prompts, messages, toolsList, false)

	// Get access token
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get access token: %w", err)
	}

	// Make the API request
	endpoint := c.getClaudeEndpoint(false)
	logging.Info("Vertex AI Claude: Sending request", "endpoint", endpoint, "model", c.options.Model.APIModel)

	respBody, err := c.makeClaudeRequest(ctx, endpoint, req, token)
	if err != nil {
		return nil, err
	}

	// Parse response
	var claudeResp claudeResponse
	if err := json.Unmarshal(respBody, &claudeResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return c.convertClaudeResponse(&claudeResp), nil
}

// streamResponseClaude streams responses using the Claude provider via Vertex AI
func (c *VertexAIClient) streamResponseClaude(ctx context.Context, prompts []string, messages []message.Message, toolsList []tools.Tool) <-chan llm.DriverEvent {
	eventChan := make(chan llm.DriverEvent)

	go func() {
		defer close(eventChan)

		// Build the streaming request
		req := c.buildClaudeRequest(prompts, messages, toolsList, true)

		// Get access token
		token, err := c.getAccessToken(ctx)
		if err != nil {
			eventChan <- llm.DriverEvent{Type: llm.EventError, Error: fmt.Errorf("failed to get access token: %w", err)}
			return
		}

		// Make the streaming API request
		endpoint := c.getClaudeEndpoint(true)
		logging.Info("Vertex AI Claude: Starting stream", "endpoint", endpoint, "model", c.options.Model.APIModel)

		resp, err := c.makeClaudeStreamRequest(ctx, endpoint, req, token)
		if err != nil {
			eventChan <- llm.DriverEvent{Type: llm.EventError, Error: err}
			return
		}
		defer resp.Body.Close()

		// Monitor context cancellation and close resp.Body to unblock decoder
		go func() {
			<-ctx.Done()
			resp.Body.Close()
		}()

		// Process SSE stream
		c.processClaudeStream(ctx, resp.Body, eventChan)
	}()

	return eventChan
}

// buildClaudeRequest builds a Claude API request with caching
func (c *VertexAIClient) buildClaudeRequest(prompts []string, messages []message.Message, toolsList []tools.Tool, stream bool) *claudeRequest {
	req := &claudeRequest{
		AnthropicVersion: "vertex-2023-10-16",
		MaxTokens:        int(c.options.MaxTokens),
		Stream:           stream,
	}

	// Add temperature if specified
	if c.options.Temperature != nil {
		req.Temperature = c.options.Temperature
	}

	// Convert system prompts with caching (last 2 prompts)
	if len(prompts) > 0 {
		req.System = make([]claudeSystemPrompt, 0, len(prompts))
		for i, prompt := range prompts {
			if strings.TrimSpace(prompt) == "" {
				continue // Skip empty prompts
			}

			sysPrompt := claudeSystemPrompt{
				Type: "text",
				Text: prompt,
			}

			// Apply caching to last 2 system prompts
			if cache.ShouldCacheSystemPrompt(i, len(prompts), c.options.DisableCache) {
				sysPrompt.CacheControl = &claudeCacheControl{Type: "ephemeral"}
				logging.Info("Vertex AI Claude: Caching system prompt", "index", i)
			}

			req.System = append(req.System, sysPrompt)
		}
	}

	// Convert messages
	req.Messages = c.convertMessagesToClaude(messages)

	// Convert tools with caching (last tool only)
	if len(toolsList) > 0 {
		req.Tools = c.convertToolsToClaude(toolsList)
	}

	return req
}

// convertMessagesToClaude converts internal messages to Claude format with caching
func (c *VertexAIClient) convertMessagesToClaude(messages []message.Message) []claudeMessage {
	var claudeMessages []claudeMessage

	for _, msg := range messages {
		switch msg.Role {
		case message.User:
			var content []claudeContentBlock

			if textContent := msg.Content().String(); textContent != "" {
				content = append(content, claudeContentBlock{
					Type: "text",
					Text: textContent,
				})
			}

			// Add binary content (images and documents)
			for _, binaryContent := range msg.BinaryContent() {
				blockType := "image"
				if binaryContent.MIMEType == "application/pdf" {
					blockType = "document"
				}
				content = append(content, claudeContentBlock{
					Type: blockType,
					Source: &claudeImageSource{
						Type:      "base64",
						MediaType: binaryContent.MIMEType,
						Data:      binaryContent.String("anthropic"),
					},
				})
			}

			claudeMessages = append(claudeMessages, claudeMessage{
				Role:    "user",
				Content: content,
			})

		case message.Assistant:
			var content []claudeContentBlock

			if textContent := msg.Content().String(); textContent != "" {
				content = append(content, claudeContentBlock{
					Type: "text",
					Text: textContent,
				})
			}

			// Add tool calls
			for _, toolCall := range msg.ToolCalls() {
				var input map[string]interface{}
				if toolCall.Input != "" {
					_ = json.Unmarshal([]byte(toolCall.Input), &input) //nolint:errcheck // Parsing stored JSON, empty map on failure is acceptable
				}

				content = append(content, claudeContentBlock{
					Type:  "tool_use",
					ID:    toolCall.ID,
					Name:  toolCall.Name,
					Input: input,
				})
			}

			if len(content) > 0 {
				claudeMessages = append(claudeMessages, claudeMessage{
					Role:    "assistant",
					Content: content,
				})
			}

		case message.Tool:
			// Add tool results
			var content []claudeContentBlock
			for _, result := range msg.ToolResults() {
				content = append(content, claudeContentBlock{
					Type:      "tool_result",
					ToolUseID: result.ToolCallID,
					Content:   result.Content,
					IsError:   result.IsError,
				})
			}

			if len(content) > 0 {
				claudeMessages = append(claudeMessages, claudeMessage{
					Role:    "user",
					Content: content,
				})
			}
		}
	}

	// Apply cache control to the last message (last content block)
	if len(claudeMessages) > 0 && !c.options.DisableCache {
		lastMsg := &claudeMessages[len(claudeMessages)-1]
		if len(lastMsg.Content) > 0 {
			lastBlock := &lastMsg.Content[len(lastMsg.Content)-1]
			// Only cache text and tool_result blocks
			if lastBlock.Type == "text" || lastBlock.Type == "tool_result" {
				lastBlock.CacheControl = &claudeCacheControl{Type: "ephemeral"}
				logging.Info("Vertex AI Claude: Caching last message block")
			}
		}
	}

	return claudeMessages
}

// convertToolsToClaude converts tools to Claude format with caching
func (c *VertexAIClient) convertToolsToClaude(toolsList []tools.Tool) []claudeTool {
	claudeTools := make([]claudeTool, len(toolsList))

	for i, tool := range toolsList {
		schema := tool.ParamSchema()

		// Convert schema properties
		properties := make(map[string]interface{})
		for pair := schema.Properties.Oldest(); pair != nil; pair = pair.Next() {
			properties[pair.Key] = c.convertSchemaToMap(pair.Value)
		}

		inputSchema := map[string]interface{}{
			"type":       "object",
			"properties": properties,
		}

		if len(schema.Required) > 0 {
			inputSchema["required"] = schema.Required
		}

		claudeTools[i] = claudeTool{
			Name:        tool.Name(),
			Description: tool.Description(),
			InputSchema: inputSchema,
		}

		// Cache only the last tool
		if cache.ShouldCacheTool(i, len(toolsList), 0, 0, c.options.DisableCache) {
			claudeTools[i].CacheControl = &claudeCacheControl{Type: "ephemeral"}
			logging.Info("Vertex AI Claude: Caching last tool", "name", tool.Name())
		}
	}

	return claudeTools
}

// convertSchemaToMap converts a jsonschema.Schema to a map
func (c *VertexAIClient) convertSchemaToMap(schema interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	// This is a simplified conversion - expand as needed
	if schemaMap, ok := schema.(map[string]interface{}); ok {
		for k, v := range schemaMap {
			result[k] = v
		}
	}

	return result
}

// getClaudeEndpoint returns the Vertex AI Claude API endpoint
func (c *VertexAIClient) getClaudeEndpoint(stream bool) string {
	project := os.Getenv("VERTEXAI_PROJECT")
	location := os.Getenv("VERTEXAI_LOCATION")
	if location == "" {
		location = "us-central1"
	}

	action := "rawPredict"
	if stream {
		action = "streamRawPredict"
	}

	return fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:%s",
		location, project, location, c.options.Model.APIModel, action)
}

// getAccessToken gets a Google Cloud access token
func (c *VertexAIClient) getAccessToken(ctx context.Context) (string, error) {
	creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return "", fmt.Errorf("failed to find credentials: %w", err)
	}

	token, err := creds.TokenSource.Token()
	if err != nil {
		return "", fmt.Errorf("failed to get token: %w", err)
	}

	return token.AccessToken, nil
}

// makeClaudeRequest makes an HTTP request to the Claude API
func (c *VertexAIClient) makeClaudeRequest(ctx context.Context, endpoint string, req *claudeRequest, token string) ([]byte, error) {
	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")

	client := llm.ResilientHTTPClient()
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// makeClaudeStreamRequest makes a streaming HTTP request
func (c *VertexAIClient) makeClaudeStreamRequest(ctx context.Context, endpoint string, req *claudeRequest, token string) (*http.Response, error) {
	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")

	// Use streaming HTTP client for streaming responses — includes idle timeout
	// to detect silent hangs during SSE streaming.
	streamClient := llm.StreamingHTTPClient()
	resp, err := streamClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	return resp, nil
}

// processClaudeStream processes the SSE stream from Claude API
func (c *VertexAIClient) processClaudeStream(ctx context.Context, body io.Reader, eventChan chan<- llm.DriverEvent) {
	decoder := json.NewDecoder(body)
	accumulated := &llm.DriverResponse{
		Content:   "",
		ToolCalls: []message.ToolCall{},
	}

	currentToolCall := &message.ToolCall{}

	for {
		// Check for context cancellation before each decode
		select {
		case <-ctx.Done():
			logging.Debug("VertexAI Claude stream cancelled", "ctxErr", ctx.Err())
			return
		default:
			// Not cancelled, continue
		}

		var event claudeStreamEvent
		if err := decoder.Decode(&event); err != nil {
			if err != io.EOF {
				// Don't warn if context was cancelled - that's expected
				if ctx.Err() == nil {
					logging.Warn("Stream decode error", "error", err)
				}
			}
			break
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil {
				logging.Info("Stream started", "model", event.Message.Model)
			}

		case "content_block_start":
			if event.ContentBlock != nil {
				switch event.ContentBlock.Type {
				case "text":
					eventChan <- llm.DriverEvent{Type: llm.EventContentStart}
				case "tool_use":
					currentToolCall = &message.ToolCall{
						ID:       event.ContentBlock.ID,
						Name:     event.ContentBlock.Name,
						Input:    "",
						Finished: false,
					}
					eventChan <- llm.DriverEvent{
						Type:     llm.EventToolUseStart,
						ToolCall: currentToolCall,
					}
				}
			}

		case "content_block_delta":
			if event.Delta != nil {
				if event.Delta.Type == "text_delta" && event.Delta.Text != "" {
					accumulated.Content += event.Delta.Text
					eventChan <- llm.DriverEvent{
						Type:    llm.EventContentDelta,
						Content: event.Delta.Text,
					}
				} else if event.Delta.Type == "input_json_delta" && event.Delta.PartialJSON != "" {
					currentToolCall.Input += event.Delta.PartialJSON
					eventChan <- llm.DriverEvent{
						Type: llm.EventToolUseDelta,
						ToolCall: &message.ToolCall{
							ID:    currentToolCall.ID,
							Name:  currentToolCall.Name,
							Input: event.Delta.PartialJSON,
						},
					}
				}
			}

		case "content_block_stop":
			if currentToolCall.ID != "" {
				currentToolCall.Finished = true
				accumulated.ToolCalls = append(accumulated.ToolCalls, *currentToolCall)
				eventChan <- llm.DriverEvent{
					Type:     llm.EventToolUseStop,
					ToolCall: currentToolCall,
				}
				currentToolCall = &message.ToolCall{}
			} else {
				eventChan <- llm.DriverEvent{Type: llm.EventContentStop}
			}

		case "message_delta":
			if event.Delta != nil && event.Delta.StopReason != "" {
				accumulated.FinishReason = c.convertClaudeFinishReason(event.Delta.StopReason)
			}
			if event.Usage != nil {
				// TokenCount = sum of all token usage
				total := int64(event.Usage.InputTokens) + int64(event.Usage.OutputTokens) +
					int64(event.Usage.CacheCreationInputTokens) + int64(event.Usage.CacheReadInputTokens)
				accumulated.Usage = llm.TokenUsage{
					TokenCount: total,
				}
			}

		case "message_stop":
			eventChan <- llm.DriverEvent{
				Type:     llm.EventComplete,
				Response: accumulated,
			}
			return
		}
	}

	// Send complete event if we didn't get message_stop
	eventChan <- llm.DriverEvent{
		Type:     llm.EventComplete,
		Response: accumulated,
	}
}

// convertClaudeResponse converts Claude response to internal format
func (c *VertexAIClient) convertClaudeResponse(resp *claudeResponse) *llm.DriverResponse {
	// TokenCount = sum of all token usage
	total := int64(resp.Usage.InputTokens) + int64(resp.Usage.OutputTokens) +
		int64(resp.Usage.CacheCreationInputTokens) + int64(resp.Usage.CacheReadInputTokens)
	response := &llm.DriverResponse{
		Content:      "",
		ToolCalls:    []message.ToolCall{},
		Usage:        llm.TokenUsage{TokenCount: total},
		FinishReason: c.convertClaudeFinishReason(resp.StopReason),
	}

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			response.Content += block.Text
		case "tool_use":
			inputJSON, _ := json.Marshal(block.Input)
			response.ToolCalls = append(response.ToolCalls, message.ToolCall{
				ID:       block.ID,
				Name:     block.Name,
				Input:    string(inputJSON),
				Finished: true,
			})
		}
	}

	return response
}

// convertClaudeFinishReason converts Claude finish reason to internal format
func (c *VertexAIClient) convertClaudeFinishReason(reason string) message.FinishReason {
	switch reason {
	case "end_turn":
		return message.FinishReasonEndTurn
	case "max_tokens":
		return message.FinishReasonMaxTokens
	case "tool_use":
		return message.FinishReasonToolUse
	default:
		return message.FinishReasonUnknown
	}
}

// validateClaudeKey validates Claude configuration via Vertex AI
func (c *VertexAIClient) validateClaudeKey(ctx context.Context) error {
	// Try a minimal request
	testMessages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Say 'test' and nothing else"},
			},
		},
	}

	_, err := c.sendMessagesClaude(ctx, []string{}, testMessages, nil)
	return err
}
