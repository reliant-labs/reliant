// Copyright (c) 2025 Reliant Labs
package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// LocalClient implements the registry.Client interface for local model servers.
// It wraps the OpenAI SDK and points it at a local endpoint (Ollama, LM Studio, etc.)
type LocalClient struct {
	Options llm.DriverOptions
	Client  openai.Client
}

// Name returns the name of the driver
func (c *LocalClient) Name() string {
	return "local"
}

// NewClient creates a new LocalClient with the given options.
// The BaseURL option is required for local clients.
func NewClient(opts llm.DriverOptions) *LocalClient {
	openaiClientOptions := []option.RequestOption{}

	// BaseURL is required for local clients
	if opts.BaseURL != "" {
		openaiClientOptions = append(openaiClientOptions, option.WithBaseURL(opts.BaseURL))
	}

	// Some local servers may require an API key (even a dummy one)
	// Ollama doesn't require one, but LM Studio might
	if opts.ApiKey != "" {
		openaiClientOptions = append(openaiClientOptions, option.WithAPIKey(opts.ApiKey))
	} else {
		// Use a placeholder API key - some OpenAI SDK implementations require a non-empty key
		openaiClientOptions = append(openaiClientOptions, option.WithAPIKey("local"))
	}

	if opts.ExtraHeaders != nil {
		for key, value := range opts.ExtraHeaders {
			openaiClientOptions = append(openaiClientOptions, option.WithHeader(key, value))
		}
	}

	client := openai.NewClient(openaiClientOptions...)
	return &LocalClient{
		Options: opts,
		Client:  client,
	}
}

// createClient is the driver factory function for the registry
func createClient(opts *llm.DriverOptions) (registry.Client, error) {
	if opts.BaseURL == "" {
		return nil, fmt.Errorf("local driver requires a base URL")
	}
	return NewClient(*opts), nil
}

// ConvertMessages converts internal messages to OpenAI format
func (c *LocalClient) ConvertMessages(prompts []string, messages []message.Message) (openaiMessages []openai.ChatCompletionMessageParamUnion) {
	// Add system message first
	for _, prompt := range prompts {
		if prompt != "" {
			openaiMessages = append(openaiMessages, openai.SystemMessage(prompt))
		}
	}

	for _, msg := range messages {
		switch msg.Role {
		case message.User:
			var content []openai.ChatCompletionContentPartUnionParam
			textBlock := openai.ChatCompletionContentPartTextParam{Text: msg.Content().String()}
			content = append(content, openai.ChatCompletionContentPartUnionParam{OfText: &textBlock})

			// Note: Most local models don't support images, but we include them in case they do
			for _, binaryContent := range msg.BinaryContent() {
				if isImageMimeType(binaryContent.MIMEType) {
					imageURL := openai.ChatCompletionContentPartImageImageURLParam{URL: binaryContent.String(Family)}
					imageBlock := openai.ChatCompletionContentPartImageParam{ImageURL: imageURL}
					content = append(content, openai.ChatCompletionContentPartUnionParam{OfImageURL: &imageBlock})
				}
			}

			openaiMessages = append(openaiMessages, openai.UserMessage(content))

		case message.Assistant:
			assistantMsg := openai.ChatCompletionAssistantMessageParam{
				Role: "assistant",
			}

			assistantMsg.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
				OfString: openai.String(msg.Content().String()),
			}

			if len(msg.ToolCalls()) > 0 {
				assistantMsg.ToolCalls = make([]openai.ChatCompletionMessageToolCallUnionParam, len(msg.ToolCalls()))
				for i, call := range msg.ToolCalls() {
					functionCall := openai.ChatCompletionMessageFunctionToolCallParam{
						ID:   call.ID,
						Type: "function",
						Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
							Name:      call.Name,
							Arguments: call.Input,
						},
					}
					assistantMsg.ToolCalls[i] = openai.ChatCompletionMessageToolCallUnionParam{
						OfFunction: &functionCall,
					}
				}
			}

			openaiMessages = append(openaiMessages, openai.ChatCompletionMessageParamUnion{
				OfAssistant: &assistantMsg,
			})

		case message.Tool:
			for _, result := range msg.ToolResults() {
				openaiMessages = append(openaiMessages,
					openai.ToolMessage(result.Content, result.ToolCallID),
				)
			}
		}
	}

	return
}

// ConvertTools converts internal tools to OpenAI format
func (c *LocalClient) ConvertTools(tools []tools.Tool) []openai.ChatCompletionToolUnionParam {
	openaiTools := make([]openai.ChatCompletionToolUnionParam, len(tools))

	for i, tool := range tools {
		schema := tool.ParamSchema()

		// Ensure required is always an array (even if empty) for OpenAI compatibility
		required := schema.Required
		if required == nil {
			required = []string{}
		}

		// Convert properties from OrderedMap to regular map
		properties := make(map[string]interface{})
		if schema.Properties != nil {
			for pair := schema.Properties.Oldest(); pair != nil; pair = pair.Next() {
				properties[pair.Key] = pair.Value
			}
		}

		openaiTools[i] = openai.ChatCompletionFunctionTool(
			openai.FunctionDefinitionParam{
				Name:        tool.Name(),
				Description: openai.String(tool.Description()),
				Parameters: openai.FunctionParameters{
					"type":       "object",
					"properties": properties,
					"required":   required,
				},
			},
		)
	}

	return openaiTools
}

func (c *LocalClient) finishReason(reason string) message.FinishReason {
	switch reason {
	case "stop":
		return message.FinishReasonEndTurn
	case "length":
		return message.FinishReasonMaxTokens
	case "tool_calls":
		return message.FinishReasonToolUse
	default:
		return message.FinishReasonUnknown
	}
}

func (c *LocalClient) preparedParams(messages []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolUnionParam) openai.ChatCompletionNewParams {
	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(c.Options.Model.APIModel),
		Messages: messages,
	}

	// Only set tools if there are any
	if len(tools) > 0 {
		params.Tools = tools
	}

	// Add temperature if specified in options
	if c.Options.Temperature != nil {
		params.Temperature = openai.Float(*c.Options.Temperature)
	}

	// Local models generally don't support reasoning effort or max_completion_tokens
	// Use the standard max_tokens parameter
	if c.Options.MaxTokens > 0 {
		params.MaxTokens = openai.Int(c.Options.MaxTokens)
	}

	return params
}

func (c *LocalClient) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) (response *llm.DriverResponse, err error) {
	params := c.preparedParams(c.ConvertMessages(prompts, messages), c.ConvertTools(tools))

	// Debug logging temporarily disabled
	if false {
		jsonData, _ := json.Marshal(params)
		logging.Debug("Local driver prepared messages", "messages", string(jsonData))
	}

	attempts := 0
	for {
		attempts++
		openaiResponse, err := c.Client.Chat.Completions.New(ctx, params)
		if err != nil {
			retry, after, retryErr := c.shouldRetry(attempts, err)
			if retryErr != nil {
				return nil, retryErr
			}
			if retry {
				logging.Warn(fmt.Sprintf("Retrying local request... attempt %d of %d", attempts, models.MaxRetries))
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(after) * time.Millisecond):
					continue
				}
			}
			return nil, retryErr
		}

		if len(openaiResponse.Choices) == 0 {
			return nil, fmt.Errorf("local model returned no choices")
		}

		content := ""
		if openaiResponse.Choices[0].Message.Content != "" {
			content = openaiResponse.Choices[0].Message.Content
		}

		toolCalls := c.toolCalls(*openaiResponse)
		finishReason := c.finishReason(string(openaiResponse.Choices[0].FinishReason))

		if len(toolCalls) > 0 {
			finishReason = message.FinishReasonToolUse
		}

		return &llm.DriverResponse{
			Content:      content,
			ToolCalls:    toolCalls,
			Usage:        c.usage(*openaiResponse),
			FinishReason: finishReason,
		}, nil
	}
}

func (c *LocalClient) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) <-chan llm.DriverEvent {
	params := c.preparedParams(c.ConvertMessages(prompts, messages), c.ConvertTools(tools))
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: openai.Bool(true),
	}

	eventChan := make(chan llm.DriverEvent)

	go func() {
		attempts := 0
		for {
			attempts++
			stream := c.Client.Chat.Completions.NewStreaming(ctx, params)

			var currentToolCalls []message.ToolCall
			var finalUsage llm.TokenUsage

			for stream.Next() {
				chunk := stream.Current()

				if len(chunk.Choices) > 0 {
					choice := chunk.Choices[0]

					// Handle content
					if choice.Delta.Content != "" {
						eventChan <- llm.DriverEvent{
							Type:    llm.EventContentDelta,
							Content: choice.Delta.Content,
						}
					}

					// Handle tool calls
					for _, tc := range choice.Delta.ToolCalls {
						// Expand currentToolCalls if needed
						for len(currentToolCalls) <= int(tc.Index) {
							currentToolCalls = append(currentToolCalls, message.ToolCall{})
						}

						idx := int(tc.Index)
						if tc.ID != "" {
							currentToolCalls[idx].ID = tc.ID
							currentToolCalls[idx].Type = "function"
						}
						if tc.Function.Name != "" {
							currentToolCalls[idx].Name = tc.Function.Name
						}
						if tc.Function.Arguments != "" {
							currentToolCalls[idx].Input += tc.Function.Arguments
						}
					}

					// Check for finish
					if choice.FinishReason != "" {
						finishReason := c.finishReason(string(choice.FinishReason))
						if len(currentToolCalls) > 0 {
							finishReason = message.FinishReasonToolUse
							// Mark tool calls as finished
							for i := range currentToolCalls {
								currentToolCalls[i].Finished = true
							}
						}

						eventChan <- llm.DriverEvent{
							Type: llm.EventComplete,
							Response: &llm.DriverResponse{
								ToolCalls:    currentToolCalls,
								Usage:        finalUsage,
								FinishReason: finishReason,
							},
						}
					}
				}

				// Capture usage from stream
				if chunk.Usage.PromptTokens > 0 {
					finalUsage.TokenCount = chunk.Usage.PromptTokens
				}
			}

			if err := stream.Err(); err != nil {
				if errors.Is(err, io.EOF) {
					close(eventChan)
					return
				}

				retry, after, retryErr := c.shouldRetry(attempts, err)
				if retryErr != nil {
					eventChan <- llm.DriverEvent{Type: llm.EventError, Error: retryErr}
					close(eventChan)
					return
				}
				if retry {
					logging.Warn(fmt.Sprintf("Retrying local stream... attempt %d of %d", attempts, models.MaxRetries))
					select {
					case <-ctx.Done():
						if ctx.Err() != nil {
							eventChan <- llm.DriverEvent{Type: llm.EventError, Error: ctx.Err()}
						}
						close(eventChan)
						return
					case <-time.After(time.Duration(after) * time.Millisecond):
						continue
					}
				}
				eventChan <- llm.DriverEvent{Type: llm.EventError, Error: retryErr}
				close(eventChan)
				return
			}

			close(eventChan)
			return
		}
	}()

	return eventChan
}

func (c *LocalClient) shouldRetry(attempts int, err error) (bool, int64, error) {
	var apierr *openai.Error
	if !errors.As(err, &apierr) {
		// Not an OpenAI API error - could be network error, retry with backoff
		if attempts <= models.MaxRetries {
			backoffMs := 1000 * (1 << (attempts - 1)) // 1s, 2s, 4s...
			return true, int64(backoffMs), nil
		}
		return false, 0, err
	}

	if apierr.StatusCode != 429 && apierr.StatusCode != 500 && apierr.StatusCode != 503 {
		return false, 0, err
	}

	if attempts > models.MaxRetries {
		return false, 0, fmt.Errorf("maximum retry attempts reached: %d retries", models.MaxRetries)
	}

	backoffMs := 2000 * (1 << (attempts - 1))
	jitterMs := int(float64(backoffMs) * 0.2)
	retryMs := backoffMs + jitterMs

	retryAfterValues := apierr.Response.Header.Values("Retry-After")
	if len(retryAfterValues) > 0 {
		if _, scanErr := fmt.Sscanf(retryAfterValues[0], "%d", &retryMs); scanErr == nil {
			retryMs = retryMs * 1000
		}
	}

	return true, int64(retryMs), nil
}

func (c *LocalClient) toolCalls(completion openai.ChatCompletion) []message.ToolCall {
	var toolCalls []message.ToolCall

	if len(completion.Choices) > 0 && len(completion.Choices[0].Message.ToolCalls) > 0 {
		for _, call := range completion.Choices[0].Message.ToolCalls {
			if call.ID == "" || call.Function.Name == "" {
				logging.Warn("Skipping empty/invalid tool call from local model",
					"id", call.ID,
					"name", call.Function.Name)
				continue
			}

			toolCall := message.ToolCall{
				ID:       call.ID,
				Name:     call.Function.Name,
				Input:    call.Function.Arguments,
				Type:     "function",
				Finished: true,
			}
			toolCalls = append(toolCalls, toolCall)
		}
	}

	return toolCalls
}

func (c *LocalClient) usage(completion openai.ChatCompletion) llm.TokenUsage {
	return llm.TokenUsage{
		TokenCount: completion.Usage.PromptTokens,
	}
}

func (c *LocalClient) Model() models.Model {
	return c.Options.Model
}

// ValidateKey validates that we can connect to the local server.
// For local servers, we send a minimal request to verify connectivity.
func (c *LocalClient) ValidateKey(ctx context.Context) error {
	// For local servers, we just verify we can reach the endpoint
	// by sending a minimal completion request
	testMessages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Say 'ok'"},
			},
		},
	}

	validationOpts := c.Options
	validationOpts.MaxTokens = 10 // Minimal tokens for validation

	validationClient := NewClient(validationOpts)
	_, err := validationClient.SendMessages(ctx, nil, testMessages, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to local model server at %s: %w", c.Options.BaseURL, err)
	}

	return nil
}

// isImageMimeType checks if the given MIME type is an image type
func isImageMimeType(mimeType string) bool {
	switch mimeType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}
