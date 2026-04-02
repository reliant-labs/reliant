// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	toolsPkg "github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/observability"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// mcpToolPrefix is the prefix added to tool names to make them appear as MCP tools
const mcpToolPrefix = "mcp__reliant__"

// ClaudeCodeClient is the driver for Claude Code API (sk-ant-oat-* keys)
// It uses Bearer authentication and custom headers to match the Claude Code client.
type ClaudeCodeClient struct {
	*baseClient
}

// NewClaudeCodeClient creates a new Claude Code API client for sk-ant-oat keys
func NewClaudeCodeClient(opts llm.DriverOptions) *ClaudeCodeClient {
	clientOptions := []option.RequestOption{}

	// For sk-ant-oat keys, we DON'T use WithAPIKey (which sets X-Api-Key header)
	// Instead, we use Bearer authentication with exact header casing to match Claude Code

	// Headers that need exact lowercase casing (Go canonicalizes by default)
	lowercaseHeaders := map[string]string{
		"host":           "api.anthropic.com",
		"connection":     "keep-alive",
		"authorization":  "Bearer " + opts.ApiKey,
		"x-app":          "cli",
		"content-type":   "application/json",
		"anthropic-beta": "claude-code-20250219,oauth-2025-04-20,context-management-2025-06-27,prompt-caching-scope-2026-01-05,adaptive-thinking-2026-01-28",
		"anthropic-dangerous-direct-browser-access": "true",
		"anthropic-version":                         "2023-06-01",
		"accept-language":                           "*",
		"sec-fetch-mode":                            "cors",
		"accept-encoding":                           "br, gzip, deflate",
	}

	// Build the transport chain:
	// tokenRefreshTransport -> lowercaseHeaderTransport -> ResilientTransport (DNS fallback + caching)
	baseTransport := otelhttp.NewTransport(llm.ResilientTransport())

	lcTransport := &lowercaseHeaderTransport{
		base:    baseTransport,
		headers: lowercaseHeaders,
	}

	var finalTransport http.RoundTripper = lcTransport
	if opts.TokenRefresher != nil {
		finalTransport = &tokenRefreshTransport{
			base:             lcTransport,
			opts:             &opts,
			lowercaseHeaders: lowercaseHeaders,
		}
	}

	// Wrap the transport chain with idle timeout detection.
	// This ensures streaming responses that go silent for >5min are aborted.
	customHTTPClient := &http.Client{
		Transport: llm.WrapWithIdleTimeout(finalTransport),
	}

	clientOptions = append(clientOptions,
		option.WithHTTPClient(customHTTPClient),
		// Headers with standard casing (these get canonicalized but match Claude Code)
		option.WithHeader("Accept", "application/json"),
		option.WithHeader("X-Stainless-Retry-Count", "0"),
		option.WithHeader("X-Stainless-Timeout", "600"),
		option.WithHeader("X-Stainless-Lang", "js"),
		option.WithHeader("X-Stainless-Package-Version", "0.70.0"),
		option.WithHeader("X-Stainless-OS", "MacOS"),
		option.WithHeader("X-Stainless-Arch", "arm64"),
		option.WithHeader("X-Stainless-Runtime", "node"),
		option.WithHeader("X-Stainless-Runtime-Version", "v25.5.0"),
		option.WithHeader("User-Agent", "claude-cli/2.1.32 (external, cli)"),
	)

	// Support validate (short timeout for validation requests)
	if opts.MaxTokens < 10 {
		clientOptions = append(clientOptions, option.WithRequestTimeout(10*time.Second))
	}

	return &ClaudeCodeClient{
		baseClient: newBase(opts, clientOptions),
	}
}

// tokenRefreshTransport wraps another RoundTripper and transparently refreshes
// the OAuth access token when it's expired. On successful refresh it updates the
// authorization header in lowercaseHeaders so subsequent requests use the new token.
type tokenRefreshTransport struct {
	base             http.RoundTripper
	opts             *llm.DriverOptions
	lowercaseHeaders map[string]string
}

func (t *tokenRefreshTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Check if token needs refresh before the request
	if t.opts.TokenRefresher != nil && t.opts.RefreshToken != "" && !t.opts.TokenExpiresAt.IsZero() {
		if time.Now().After(t.opts.TokenExpiresAt.Add(-5 * time.Minute)) {
			newToken, newExpiry, err := t.opts.TokenRefresher(t.opts.RefreshToken)
			if err != nil {
				logging.Warn("Failed to refresh Claude OAuth token", "error", err)
				// Continue with the existing token; the API will reject if truly expired
			} else {
				logging.Info("Refreshed Claude OAuth token", "new_expiry", newExpiry)
				// Update the stored state so future requests use the new token
				t.opts.ApiKey = newToken
				t.opts.TokenExpiresAt = newExpiry
				// Update the authorization header in the lowercase headers map
				t.lowercaseHeaders["authorization"] = "Bearer " + newToken
			}
		}
	}
	return t.base.RoundTrip(req)
}

// lowercaseHeaderTransport is a custom RoundTripper that sets headers with exact casing
// to match Claude Code's header format. Go's net/http canonicalizes headers by default.
type lowercaseHeaderTransport struct {
	base    http.RoundTripper
	headers map[string]string // lowercase key -> value
}

func (t *lowercaseHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Add beta=true query param
	q := req.URL.Query()
	q.Set("beta", "true")
	req.URL.RawQuery = q.Encode()

	// Set headers with exact casing by accessing the map directly
	for key, value := range t.headers {
		// Delete any canonicalized version first
		req.Header.Del(key)
		// Set with exact casing using map access (bypasses canonicalization)
		req.Header[key] = []string{value}
	}
	return t.base.RoundTrip(req)
}

// Name returns the driver name
func (c *ClaudeCodeClient) Name() string {
	return "claude-code"
}

// convertTools overrides the base implementation to prefix tool names with MCP prefix
// This makes tools appear as if they come from an MCP server
func (c *ClaudeCodeClient) convertTools(tools []toolsPkg.Tool) []anthropic.ToolUnionParam {
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

		// Only prefix tool name if it's not already an MCP tool
		toolName := tool.Name()
		if !strings.HasPrefix(toolName, "mcp__") {
			toolName = mcpToolPrefix + toolName
		}

		toolParam := anthropic.ToolParam{
			Name:        toolName,
			Description: anthropic.String(tool.Description()),
			InputSchema: inputSchema,
		}

		// Cache only the last tool
		if !c.options.DisableCache && i == len(tools)-1 {
			logging.Debug("ClaudeCode: Caching last tool", "name", toolName, "index", i)
			toolParam.CacheControl = anthropic.CacheControlEphemeralParam{
				Type: "ephemeral",
			}
		}

		anthropicTools[i] = anthropic.ToolUnionParam{OfTool: &toolParam}
	}

	return anthropicTools
}

// toolCalls overrides the base implementation to strip the MCP prefix from tool names
func (c *ClaudeCodeClient) toolCalls(msg anthropic.Message) ([]message.ToolCall, error) {
	toolCalls, err := c.baseClient.toolCalls(msg)
	if err != nil {
		return nil, err
	}

	// Strip MCP prefix from tool names
	for i := range toolCalls {
		toolCalls[i].Name = strings.TrimPrefix(toolCalls[i].Name, mcpToolPrefix)
	}

	return toolCalls, nil
}

// buildCompleteEvent overrides the base implementation to strip MCP prefix from tool names
// manualThinkingSignature is used as a fallback if SDK accumulation doesn't capture it.
func (c *ClaudeCodeClient) buildCompleteEvent(accumulatedMessage anthropic.Message, manualThinkingSignature string) llm.DriverEvent {
	content := ""
	thinking := ""
	thinkingSignature := ""

	for _, block := range accumulatedMessage.Content {
		switch v := block.AsAny().(type) {
		case anthropic.TextBlock:
			content += v.Text
		case anthropic.ThinkingBlock:
			thinking += v.Thinking
			if v.Signature != "" {
				thinkingSignature = v.Signature
			}
		}
	}

	// Use manual signature as fallback if SDK didn't capture it
	if thinkingSignature == "" && manualThinkingSignature != "" {
		thinkingSignature = manualThinkingSignature
	}

	finishReason := message.FinishReasonUnknown
	if accumulatedMessage.StopReason != "" {
		finishReason = c.finishReason(string(accumulatedMessage.StopReason))
	}

	toolCalls, err := c.toolCalls(accumulatedMessage)
	if err != nil {
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
			Usage:             c.usage(accumulatedMessage),
			FinishReason:      finishReason,
		},
	}
}

// streamResponseInternal handles streaming with MCP prefix stripping for tool names
func (c *ClaudeCodeClient) streamResponseInternal(ctx context.Context, params anthropic.MessageNewParams) <-chan llm.DriverEvent {
	eventChan := make(chan llm.DriverEvent)

	go func() {
		defer close(eventChan)

		stream := c.client.Messages.NewStreaming(ctx, params)
		accumulated := anthropic.Message{}
		gotMessageStop := false
		currentToolID := ""
		currentToolName := ""

		// Manual accumulation of thinking signature
		var manualThinkingSignature string

		// Process stream events
		for stream.Next() {
			event := stream.Current()

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
					// Strip MCP prefix from tool name for the event
					currentToolName = strings.TrimPrefix(e.ContentBlock.Name, mcpToolPrefix)
					eventChan <- llm.DriverEvent{
						Type: llm.EventToolUseStart,
						ToolCall: &message.ToolCall{
							ID:       e.ContentBlock.ID,
							Name:     currentToolName,
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
					manualThinkingSignature += e.Delta.Signature
					continue
				}
				if e.Delta.Type == "text_delta" && e.Delta.Text != "" {
					eventChan <- llm.DriverEvent{Type: llm.EventContentDelta, Content: e.Delta.Text}
					continue
				}
				if e.Delta.Type == "input_json_delta" && currentToolID != "" {
					rawInput := e.Delta.PartialJSON
					eventChan <- llm.DriverEvent{
						Type: llm.EventToolUseDelta,
						ToolCall: &message.ToolCall{
							ID:       currentToolID,
							Name:     currentToolName,
							Finished: false,
							Input:    rawInput,
						},
					}
				}

			case anthropic.ContentBlockStopEvent:
				if currentToolID != "" {
					eventChan <- llm.DriverEvent{
						Type:     llm.EventToolUseStop,
						ToolCall: &message.ToolCall{ID: currentToolID, Name: currentToolName},
					}
					currentToolID = ""
					currentToolName = ""
				} else {
					eventChan <- llm.DriverEvent{Type: llm.EventContentStop}
				}

			case anthropic.MessageStopEvent:
				gotMessageStop = true
				logging.Info("Message stop event",
					"reason", accumulated.StopReason,
					"blocks", len(accumulated.Content))
				eventChan <- c.buildCompleteEvent(accumulated, manualThinkingSignature)
			}
		}

		// Check for stream errors
		if err := stream.Err(); err != nil && !errors.Is(err, io.EOF) {
			logging.Warn("Stream error", "error", err)
			eventChan <- llm.DriverEvent{Type: llm.EventError, Error: err}
		}

		// Always send complete event if not already sent
		if !gotMessageStop {
			logging.Warn("Stream ended without MessageStopEvent, using accumulated data",
				"blocks", len(accumulated.Content))
			eventChan <- c.buildCompleteEvent(accumulated, manualThinkingSignature)
		}
	}()

	return eventChan
}

func (c *ClaudeCodeClient) preparedMessages(prompts []string, messages []anthropic.MessageParam, tools []anthropic.ToolUnionParam) anthropic.MessageNewParams {
	// Prepend Claude Code prompts: always include base prompt, optionally include second prompt
	if c.options.IgnoreDefaultPrompts {
		// Only include the first (base) prompt - skip the second prompt (used for compaction)
		prompts = append([]string{getClaudeCodeBasePrompt()}, prompts...)
	} else {
		prompts = append([]string{getClaudeCodeBasePrompt(), getClaudeCodeSecondPrompt(c.options.WorkingDirectory)}, prompts...)
	}
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(c.options.Model.APIModel),
		MaxTokens: c.options.MaxTokens,
		Messages:  messages,
		Tools:     tools,
		Thinking:  c.getThinkingConfig(),
		System:    c.prepareSystemPrompts(prompts),
	}

	// Attach user metadata for Claude Code keys using stored OAuth account data
	if metadata, err := GetUserMetadata(c.options.ApiKey, c.options.SessionID, c.options.AccountUUID); err == nil && metadata != nil {
		params.Metadata.UserID = anthropic.String(metadata.UserID)
		logging.Debug("ClaudeCode: Attached user metadata", "user_id", metadata.UserID)
	} else if err != nil {
		logging.Warn("ClaudeCode: Failed to get user metadata", "error", err)
	}

	return params
}

func (c *ClaudeCodeClient) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []toolsPkg.Tool) (*llm.DriverResponse, error) {
	start := time.Now()
	preparedMessages := c.preparedMessages(prompts, c.convertMessages(messages), c.convertTools(tools))

	// Log session ID if provided
	logFields := []any{"total_messages", len(messages), "total_tools", len(tools)}
	if c.options.SessionID != nil && *c.options.SessionID != "" {
		logFields = append(logFields, "session_id", *c.options.SessionID)
	}
	logging.Debug("=== ClaudeCode: Sending messages ===", logFields...)

	anthropicResponse, err := c.client.Messages.New(ctx, preparedMessages)
	if err != nil {
		logging.Error("Error in ClaudeCode API call", "error", err)
		observability.LLMRequestsTotal.WithLabelValues("claude-code", "error").Inc()
		observability.LLMRequestDuration.WithLabelValues("claude-code").Observe(time.Since(start).Seconds())
		return nil, err
	}

	content := ""
	for _, block := range anthropicResponse.Content {
		if text, ok := block.AsAny().(anthropic.TextBlock); ok {
			content += text.Text
		}
	}

	logging.Debug("ClaudeCode SendMessages: Response details",
		"stop_reason", anthropicResponse.StopReason,
		"stop_sequence", anthropicResponse.StopSequence,
		"content_blocks", len(anthropicResponse.Content))

	finishReason := c.finishReason(string(anthropicResponse.StopReason))
	logging.Debug("ClaudeCode SendMessages: Finish reason result", "finish_reason", finishReason)

	toolCalls, err := c.toolCalls(*anthropicResponse)
	if err != nil {
		// MalformedJSONError indicates transient streaming corruption
		logging.Warn("ClaudeCode SendMessages: Tool call JSON validation failed (will retry)", "error", err)
		observability.LLMRequestsTotal.WithLabelValues("claude-code", "error").Inc()
		observability.LLMRequestDuration.WithLabelValues("claude-code").Observe(time.Since(start).Seconds())
		return nil, err
	}

	usage := c.usage(*anthropicResponse)

	// Record metrics
	observability.LLMRequestsTotal.WithLabelValues("claude-code", "success").Inc()
	observability.LLMRequestDuration.WithLabelValues("claude-code").Observe(time.Since(start).Seconds())
	if usage.InputTokens > 0 {
		observability.LLMTokensTotal.WithLabelValues("claude-code", "input").Add(float64(usage.InputTokens))
	}
	if usage.OutputTokens > 0 {
		observability.LLMTokensTotal.WithLabelValues("claude-code", "output").Add(float64(usage.OutputTokens))
	}

	return &llm.DriverResponse{
		Content:      content,
		ToolCalls:    toolCalls,
		Usage:        usage,
		FinishReason: finishReason,
	}, nil
}

func (c *ClaudeCodeClient) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tools []toolsPkg.Tool) <-chan llm.DriverEvent {
	preparedMessages := c.preparedMessages(prompts, c.convertMessages(messages), c.convertTools(tools))

	// Log session ID if provided
	logFields := []any{"total_messages", len(messages), "total_tools", len(tools)}
	if c.options.SessionID != nil && *c.options.SessionID != "" {
		logFields = append(logFields, "session_id", *c.options.SessionID)
	}
	logging.Debug("=== ClaudeCode: Streaming messages ===", logFields...)

	return c.streamResponseInternal(ctx, preparedMessages)
}

func (c *ClaudeCodeClient) Model() models.Model {
	return c.options.Model
}

func (c *ClaudeCodeClient) ValidateKey(ctx context.Context) error {
	logging.Info("Validating ClaudeCode API key with Claude 3.5 Haiku model...")
	testMessages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Say 'test' and nothing else"},
			},
		},
	}

	// Create a temporary client with the small model
	validationOpts := c.options
	registry := models.MustGetRegistry()
	if def, ok := registry.GetDefinition(string(models.Claude45Haiku)); ok {
		validationOpts.Model = def.ToModel()
	}
	validationOpts.MaxTokens = 100

	validationClient := NewClaudeCodeClient(validationOpts)

	_, err := validationClient.SendMessages(ctx, []string{}, testMessages, []toolsPkg.Tool{})
	return err
}

// IsClaudeCodeKey returns true if the API key is a Claude Code key (sk-ant-oat prefix)
func IsClaudeCodeKey(apiKey string) bool {
	return strings.HasPrefix(apiKey, "sk-ant-oat")
}