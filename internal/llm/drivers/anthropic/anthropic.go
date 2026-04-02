// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"context"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/bedrock"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	toolsPkg "github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/observability"
)

// AnthropicClient is the driver for the standard Anthropic API (sk-ant-* keys)
type AnthropicClient struct {
	*baseClient
}

// Name returns the driver name
func (c *AnthropicClient) Name() string {
	return "anthropic"
}

// NewAnthropicClient creates a new Anthropic API client
func NewAnthropicClient(opts llm.DriverOptions) *AnthropicClient {
	clientOptions := []option.RequestOption{}
	clientOptions = append(clientOptions, option.WithAPIKey(opts.ApiKey))

	// Use streaming HTTP client with DNS resilience, ResponseHeaderTimeout (2min),
	// and idle stream timeout (5min) to detect silent hangs during streaming.
	clientOptions = append(clientOptions, option.WithHTTPClient(llm.StreamingHTTPClient()))

	if opts.UseBedrock {
		clientOptions = append(clientOptions, bedrock.WithLoadDefaultConfig(context.Background()))
	}

	return &AnthropicClient{
		baseClient: newBase(opts, clientOptions),
	}
}

func (c *AnthropicClient) preparedMessages(prompts []string, messages []anthropic.MessageParam, tools []anthropic.ToolUnionParam) anthropic.MessageNewParams {
	return anthropic.MessageNewParams{
		Model:     anthropic.Model(c.options.Model.APIModel),
		MaxTokens: c.options.MaxTokens,
		Messages:  messages,
		Tools:     tools,
		Thinking:  c.getThinkingConfig(),
		System:    c.prepareSystemPrompts(prompts),
	}
}

func (c *AnthropicClient) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []toolsPkg.Tool) (*llm.DriverResponse, error) {
	start := time.Now()
	preparedMessages := c.preparedMessages(prompts, c.convertMessages(messages), c.convertTools(tools))

	// Log session ID if provided
	logFields := []any{"total_messages", len(messages), "total_tools", len(tools)}
	if c.options.SessionID != nil && *c.options.SessionID != "" {
		logFields = append(logFields, "session_id", *c.options.SessionID)
	}
	logging.Debug("=== Anthropic: Sending messages ===", logFields...)

	anthropicResponse, err := c.client.Messages.New(ctx, preparedMessages)
	if err != nil {
		logging.Error("Error in Anthropic API call", "error", err)
		observability.LLMRequestsTotal.WithLabelValues("anthropic", "error").Inc()
		observability.LLMRequestDuration.WithLabelValues("anthropic").Observe(time.Since(start).Seconds())
		return nil, err
	}

	content := ""
	for _, block := range anthropicResponse.Content {
		if text, ok := block.AsAny().(anthropic.TextBlock); ok {
			content += text.Text
		}
	}

	logging.Debug("Anthropic SendMessages: Response details",
		"stop_reason", anthropicResponse.StopReason,
		"stop_sequence", anthropicResponse.StopSequence,
		"content_blocks", len(anthropicResponse.Content))

	finishReason := c.finishReason(string(anthropicResponse.StopReason))
	logging.Debug("Anthropic SendMessages: Finish reason result", "finish_reason", finishReason)

	toolCalls, err := c.toolCalls(*anthropicResponse)
	if err != nil {
		// MalformedJSONError indicates transient streaming corruption
		logging.Warn("Anthropic SendMessages: Tool call JSON validation failed (will retry)", "error", err)
		observability.LLMRequestsTotal.WithLabelValues("anthropic", "error").Inc()
		observability.LLMRequestDuration.WithLabelValues("anthropic").Observe(time.Since(start).Seconds())
		return nil, err
	}

	usage := c.usage(*anthropicResponse)

	// Record metrics
	observability.LLMRequestsTotal.WithLabelValues("anthropic", "success").Inc()
	observability.LLMRequestDuration.WithLabelValues("anthropic").Observe(time.Since(start).Seconds())
	if usage.InputTokens > 0 {
		observability.LLMTokensTotal.WithLabelValues("anthropic", "input").Add(float64(usage.InputTokens))
	}
	if usage.OutputTokens > 0 {
		observability.LLMTokensTotal.WithLabelValues("anthropic", "output").Add(float64(usage.OutputTokens))
	}

	return &llm.DriverResponse{
		Content:      content,
		ToolCalls:    toolCalls,
		Usage:        usage,
		FinishReason: finishReason,
	}, nil
}

func (c *AnthropicClient) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tools []toolsPkg.Tool) <-chan llm.DriverEvent {
	preparedMessages := c.preparedMessages(prompts, c.convertMessages(messages), c.convertTools(tools))

	// Log session ID if provided
	logFields := []any{"total_messages", len(messages), "total_tools", len(tools)}
	if c.options.SessionID != nil && *c.options.SessionID != "" {
		logFields = append(logFields, "session_id", *c.options.SessionID)
	}
	logging.Debug("=== Anthropic: Streaming messages ===", logFields...)

	return c.streamResponseInternal(ctx, preparedMessages)
}

func (c *AnthropicClient) Model() models.Model {
	return c.options.Model
}

func (c *AnthropicClient) ValidateKey(ctx context.Context) error {
	logging.Info("Validating Anthropic API key with Claude 3.5 Haiku model...")
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

	validationClient := NewAnthropicClient(validationOpts)

	_, err := validationClient.SendMessages(ctx, []string{}, testMessages, []toolsPkg.Tool{})
	return err
}
