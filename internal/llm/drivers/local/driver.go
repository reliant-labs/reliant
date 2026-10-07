// Copyright (c) 2025 Reliant Labs
//
// This package implements the llm.Driver interface declared in
// internal/llm/types.go. Its behavioral contract already exists upstream, and
// the exported methods here are that interface's implementation plus
// provider-specific wire handling. A local contract.go would restate an
// interface this package does not own.
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: a provider driver IS an outbound adapter behind llm.Driver (the contract lives in internal/llm); a per-driver contract.go would duplicate it; tracked in H-RELIANT-CI-lint follow-ups
//forge:exclude-contract: llm.Driver implementation for local OpenAI-compatible servers (Ollama, LM Studio); its contract is llm.Driver
package local

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Family is the driver family id for local OpenAI-compatible servers.
const Family models.Family = "local"

func init() {
	registry.RegisterDriver(Family, createClient)
}

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
	// Retries are owned by shouldRetry, which retries only 429/500/503. The
	// SDK's own retry loop would back off through a refused connection or a
	// dead relay before the user sees any error.
	openaiClientOptions := []option.RequestOption{option.WithMaxRetries(0)}

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

	// A relay transport carries every byte: the base URL is a placeholder that
	// is never dialed, and the daemon on the user's machine talks to the real
	// server. Appended last so it overrides the SDK client's default.
	if opts.Transport != nil {
		openaiClientOptions = append(openaiClientOptions, option.WithHTTPClient(&http.Client{Transport: opts.Transport}))
	}

	client := llm.NewOpenAISDKClient(openaiClientOptions...)
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

		case message.System:
			// History System messages are content (compaction summary, branch
			// note, mailbox envelope), delivered as a user turn wrapped in
			// <system> tags. Without this case they fall through the switch
			// and are dropped before the request is built.
			if systemText := strings.TrimSpace(msg.Content().String()); systemText != "" {
				openaiMessages = append(openaiMessages,
					openai.UserMessage(fmt.Sprintf("<system>\n%s\n</system>", systemText)),
				)
			}

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

// toolListHasFunction reports whether name is present in an already-converted
// Chat Completions tool list. tool_choice naming an absent tool is a provider
// 400, so callers must check this before pinning.
func toolListHasFunction(tools []openai.ChatCompletionToolUnionParam, name string) bool {
	for _, tool := range tools {
		if fn := tool.GetFunction(); fn != nil && fn.Name == name {
			return true
		}
	}
	return false
}

func (c *LocalClient) preparedParams(messages []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolUnionParam) openai.ChatCompletionNewParams {
	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(c.Options.Model.APIModel),
		Messages: messages,
	}

	// Only set tools if there are any
	if len(tools) > 0 {
		params.Tools = tools
		// A tool_choice naming a tool absent from Tools is a provider 400, so
		// only pin when the named tool is actually in this request's list.
		if c.Options.ForceToolChoice != "" && toolListHasFunction(tools, c.Options.ForceToolChoice) {
			params.ToolChoice = openai.ToolChoiceOptionFunctionToolChoice(
				openai.ChatCompletionNamedToolChoiceFunctionParam{Name: c.Options.ForceToolChoice},
			)
		}
	}

	// Add temperature if specified in options
	if c.Options.Temperature != nil {
		params.Temperature = openai.Float(*c.Options.Temperature)
	}

	// Only low/medium/high are ever sent: that is the vocabulary servers that
	// accept reasoning_effort at all (Ollama gpt-oss, LM Studio) understand.
	// The caller only sets a level for models that declared support, because
	// Ollama 400s on any level a model does not take (qwen3 takes none).
	if effort := serverReasoningEffort(c.Options.ReasoningEffort); effort != "" {
		params.ReasoningEffort = effort
	}

	// max_tokens, not max_completion_tokens: the older name is what every
	// local server implements.
	if c.Options.MaxTokens > 0 {
		params.MaxTokens = openai.Int(c.Options.MaxTokens)
	}

	return params
}

func (c *LocalClient) toolCalls(completion openai.ChatCompletion) []message.ToolCall {
	var toolCalls []message.ToolCall

	if len(completion.Choices) > 0 && len(completion.Choices[0].Message.ToolCalls) > 0 {
		// The id is ours, not the server's, which may be missing or repeated
		// within or across responses; see llm.NewToolCallID.
		for _, call := range completion.Choices[0].Message.ToolCalls {
			if call.Function.Name == "" {
				logging.Warn("Skipping tool call with no tool name from local model",
					"id", call.ID)
				continue
			}

			toolCall := message.ToolCall{
				ID:       llm.NewToolCallID(),
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
