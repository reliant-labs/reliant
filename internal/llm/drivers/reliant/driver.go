// Copyright (c) 2025 Reliant Labs
package reliant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

type ReliantClient struct {
	Options llm.DriverOptions
	Client  openai.Client
}

// Name returns the name of the driver
func (c *ReliantClient) Name() string {
	return "reliant"
}

// getReliantAPIModel looks up the Reliant API model ID from the registry.
func getReliantAPIModel(modelID models.ModelID) (string, error) {
	reg := models.MustGetRegistry()
	def, ok := reg.GetDefinition(string(modelID))
	if !ok {
		return "", fmt.Errorf("model %s not found in registry", modelID)
	}
	for _, p := range def.Providers {
		if p.Driver == "reliant" {
			return p.APIModel, nil
		}
	}
	return "", fmt.Errorf("model %s has no reliant provider", modelID)
}

func NewClient(opts llm.DriverOptions) *ReliantClient {
	// Look up the correct Reliant API model from the registry
	if apiModel, err := getReliantAPIModel(opts.Model.ID); err == nil {
		opts.Model.APIModel = apiModel
	}

	clientOptions := []option.RequestOption{}
	if opts.ApiKey != "" {
		clientOptions = append(clientOptions, option.WithAPIKey(opts.ApiKey))
	}
	if opts.BaseURL != "" {
		clientOptions = append(clientOptions, option.WithBaseURL(opts.BaseURL))
	}
	if opts.ExtraHeaders != nil {
		for key, value := range opts.ExtraHeaders {
			clientOptions = append(clientOptions, option.WithHeader(key, value))
		}
	}
	clientOptions = append(clientOptions, option.WithHTTPClient(llm.StreamingHTTPClient()))
	client := openai.NewClient(clientOptions...)
	return &ReliantClient{
		Options: opts,
		Client:  client,
	}
}

// ConvertMessages converts internal messages to OpenAI chat completion format.
func (c *ReliantClient) ConvertMessages(prompts []string, messages []message.Message) []openai.ChatCompletionMessageParamUnion {
	var openaiMessages []openai.ChatCompletionMessageParamUnion

	for _, prompt := range prompts {
		if strings.TrimSpace(prompt) != "" {
			openaiMessages = append(openaiMessages, openai.SystemMessage(prompt))
		}
	}

	for _, msg := range messages {
		switch msg.Role {
		case message.User:
			var content []openai.ChatCompletionContentPartUnionParam
			textContents := msg.TextContents()
			combinedText := make([]string, 0, len(textContents))
			for _, tc := range textContents {
				if tc.Text != "" {
					combinedText = append(combinedText, tc.Text)
				}
			}
			for _, text := range combinedText {
				textBlock := openai.ChatCompletionContentPartTextParam{Text: text}
				content = append(content, openai.ChatCompletionContentPartUnionParam{OfText: &textBlock})
			}
			for _, binaryContent := range msg.BinaryContent() {
				if isImageMimeType(binaryContent.MIMEType) {
					imageURL := openai.ChatCompletionContentPartImageImageURLParam{URL: binaryContent.String(Family)}
					imageBlock := openai.ChatCompletionContentPartImageParam{ImageURL: imageURL}
					content = append(content, openai.ChatCompletionContentPartUnionParam{OfImageURL: &imageBlock})
				} else {
					description := fmt.Sprintf("[Attachment: %s (type: %s)]", extractFilenameFromPath(binaryContent.Path), binaryContent.MIMEType)
					textBlock := openai.ChatCompletionContentPartTextParam{Text: description}
					content = append(content, openai.ChatCompletionContentPartUnionParam{OfText: &textBlock})
				}
			}

			if len(content) == 0 {
				fallbackText := strings.Join(combinedText, "\n\n")
				openaiMessages = append(openaiMessages, openai.UserMessage(fallbackText))
			} else {
				openaiMessages = append(openaiMessages, openai.UserMessage(content))
			}

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

	return openaiMessages
}

// ConvertTools converts internal tools to OpenAI chat completion format.
func (c *ReliantClient) ConvertTools(toolList []tools.Tool) []openai.ChatCompletionToolUnionParam {
	openaiTools := make([]openai.ChatCompletionToolUnionParam, len(toolList))

	for i, tool := range toolList {
		params := geminiCompatibleToolParameters(tool)
		if !c.isGeminiModel() {
			schema := tool.ParamSchema()
			params = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
				"required":   []any{},
			}
			if schema != nil {
				required := make([]any, 0)
				if schema.Required != nil {
					required = make([]any, 0, len(schema.Required))
					for _, r := range schema.Required {
						required = append(required, r)
					}
				}

				properties := make(map[string]any)
				if schema.Properties != nil {
					for pair := schema.Properties.Oldest(); pair != nil; pair = pair.Next() {
						properties[pair.Key] = pair.Value
					}
				}
				params["properties"] = properties
				params["required"] = required
			}
		}

		openaiTools[i] = openai.ChatCompletionFunctionTool(
			openai.FunctionDefinitionParam{
				Name:        tool.Name(),
				Description: openai.String(tool.Description()),
				Parameters:  openai.FunctionParameters(params),
			},
		)
	}

	return openaiTools
}

func (c *ReliantClient) finishReason(reason string) message.FinishReason {
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

func (c *ReliantClient) preparedParams(messages []openai.ChatCompletionMessageParamUnion, toolParams []openai.ChatCompletionToolUnionParam) openai.ChatCompletionNewParams {
	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(c.Options.Model.APIModel),
		Messages: messages,
	}

	if len(toolParams) > 0 {
		params.Tools = toolParams
	}

	if c.Options.Temperature != nil {
		params.Temperature = openai.Float(*c.Options.Temperature)
	}

	// LiteLLM handles reasoning and max_completion_tokens upstream,
	// so we always use MaxTokens for simplicity.
	params.MaxTokens = openai.Int(c.Options.MaxTokens)

	return params
}

func (c *ReliantClient) SendMessages(ctx context.Context, prompts []string, messages []message.Message, toolList []tools.Tool) (response *llm.DriverResponse, err error) {
	params := c.preparedParams(c.ConvertMessages(prompts, messages), c.ConvertTools(toolList))

	if false { // Debug disabled
		jsonData, _ := json.Marshal(params)
		logging.Debug("Prepared messages", "messages", string(jsonData))
	}

	attempts := 0
	for {
		attempts++
		var rawResp *http.Response
		openaiResponse, err := c.Client.Chat.Completions.New(
			ctx,
			params,
			option.WithResponseInto(&rawResp),
		)
		if err != nil {
			retry, after, retryErr := c.shouldRetry(attempts, err)
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
		if openaiResponse.Choices[0].Message.Content != "" {
			content = openaiResponse.Choices[0].Message.Content
		}

		toolCalls := c.toolCalls(*openaiResponse)
		finishReason := c.finishReason(string(openaiResponse.Choices[0].FinishReason))

		if len(toolCalls) > 0 {
			finishReason = message.FinishReasonToolUse
		}

		upstreamRequestID, upstreamProxymanID := extractUpstreamCorrelationHeaders(rawResp)

		return &llm.DriverResponse{
			Content:            content,
			ToolCalls:          toolCalls,
			Usage:              c.usage(*openaiResponse),
			FinishReason:       finishReason,
			UpstreamRequestID:  upstreamRequestID,
			UpstreamProxymanID: upstreamProxymanID,
		}, nil
	}
}

func (c *ReliantClient) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, toolList []tools.Tool) <-chan llm.DriverEvent {
	params := c.preparedParams(c.ConvertMessages(prompts, messages), c.ConvertTools(toolList))
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: openai.Bool(true),
	}

	if false { // Debug disabled
		jsonData, _ := json.Marshal(params)
		logging.Debug("Prepared messages", "messages", string(jsonData))
	}

	attempts := 0
	eventChan := make(chan llm.DriverEvent)

	go func() {
		for {
			attempts++
			var streamResp *http.Response
			openaiStream := c.Client.Chat.Completions.NewStreaming(
				ctx,
				params,
				option.WithResponseInto(&streamResp),
			)

			acc := openai.ChatCompletionAccumulator{}
			currentContent := ""
			toolCallResults := make([]message.ToolCall, 0)

			for openaiStream.Next() {
				chunk := openaiStream.Current()
				acc.AddChunk(chunk)

				for _, choice := range chunk.Choices {
					if choice.Delta.Content != "" {
						eventChan <- llm.DriverEvent{
							Type:    llm.EventContentDelta,
							Content: choice.Delta.Content,
						}
						currentContent += choice.Delta.Content
					}
				}
			}

			err := openaiStream.Err()
			if err == nil || errors.Is(err, io.EOF) {
				var finishReason message.FinishReason

				if len(acc.Choices) > 0 {
					finishReason = c.finishReason(string(acc.ChatCompletion.Choices[0].FinishReason))
					if len(acc.Choices[0].Message.ToolCalls) > 0 {
						toolCallResults = append(toolCallResults, c.toolCalls(acc.ChatCompletion)...)
					}
				} else if currentContent != "" {
					logging.Warn("Stream ended without completion choices but has content (length: %d) - treating as interrupted stream", len(currentContent))
					eventChan <- llm.DriverEvent{
						Type:  llm.EventError,
						Error: fmt.Errorf("stream interrupted: incomplete response from Reliant proxy"),
					}
					close(eventChan)
					return
				} else {
					logging.Error("Stream ended with no content and no completion data")
					eventChan <- llm.DriverEvent{
						Type:  llm.EventError,
						Error: fmt.Errorf("empty response from Reliant proxy"),
					}
					close(eventChan)
					return
				}

				if len(toolCallResults) > 0 {
					finishReason = message.FinishReasonToolUse
				}

				upstreamRequestID, upstreamProxymanID := extractUpstreamCorrelationHeaders(streamResp)

				eventChan <- llm.DriverEvent{
					Type: llm.EventComplete,
					Response: &llm.DriverResponse{
						Content:            currentContent,
						ToolCalls:          toolCallResults,
						Usage:              c.usage(acc.ChatCompletion),
						FinishReason:       finishReason,
						UpstreamRequestID:  upstreamRequestID,
						UpstreamProxymanID: upstreamProxymanID,
					},
				}
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
				logging.Warn(fmt.Sprintf("Retrying due to rate limit... attempt %d of %d", attempts, models.MaxRetries))
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
	}()

	return eventChan
}

func (c *ReliantClient) shouldRetry(attempts int, err error) (bool, int64, error) {
	var apierr *openai.Error
	if !errors.As(err, &apierr) {
		return false, 0, err
	}

	if apierr.StatusCode != 429 && apierr.StatusCode != 500 {
		return false, 0, err
	}

	if attempts > models.MaxRetries {
		return false, 0, fmt.Errorf("maximum retry attempts reached for rate limit: %d retries", models.MaxRetries)
	}

	retryMs := 0
	retryAfterValues := apierr.Response.Header.Values("Retry-After")

	backoffMs := 2000 * (1 << (attempts - 1))
	jitterMs := int(float64(backoffMs) * 0.2)
	retryMs = backoffMs + jitterMs
	if len(retryAfterValues) > 0 {
		if _, err := fmt.Sscanf(retryAfterValues[0], "%d", &retryMs); err == nil {
			retryMs = retryMs * 1000
		}
	}
	return true, int64(retryMs), nil
}

func (c *ReliantClient) toolCalls(completion openai.ChatCompletion) []message.ToolCall {
	var toolCalls []message.ToolCall

	if len(completion.Choices) > 0 && len(completion.Choices[0].Message.ToolCalls) > 0 {
		for _, call := range completion.Choices[0].Message.ToolCalls {
			if call.ID == "" || call.Function.Name == "" {
				logging.Warn("Skipping empty/invalid tool call",
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

func (c *ReliantClient) usage(completion openai.ChatCompletion) llm.TokenUsage {
	return llm.TokenUsage{
		TokenCount: completion.Usage.PromptTokens,
	}
}

func (c *ReliantClient) Model() models.Model {
	return c.Options.Model
}

func (c *ReliantClient) ValidateKey(ctx context.Context) error {
	// Validate by listing models — this checks the key is accepted by the proxy
	// without needing to know which models are available.
	_, err := c.Client.Models.List(ctx)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}
	return nil
}

// isImageMimeType checks if a MIME type represents an image
func isImageMimeType(mimeType string) bool {
	switch mimeType {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "image/bmp", "image/svg+xml":
		return true
	default:
		return false
	}
}

// extractFilenameFromPath extracts the filename from a file path
func extractFilenameFromPath(path string) string {
	parts := []rune(path)
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == '/' || parts[i] == '\\' {
			return string(parts[i+1:])
		}
	}
	return path
}

// extractUpstreamCorrelationHeaders extracts request correlation headers from the HTTP response
func extractUpstreamCorrelationHeaders(resp *http.Response) (requestID string, proxymanID string) {
	if resp == nil {
		return "", ""
	}
	return strings.TrimSpace(resp.Header.Get("x-oai-request-id")), strings.TrimSpace(resp.Header.Get("x-proxyman-id"))
}