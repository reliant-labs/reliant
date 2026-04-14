// Copyright (c) 2025 Reliant Labs
package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

type OpenaiClient struct {
	Options llm.DriverOptions
	Client  openai.Client
}

// Name returns the name of the driver
func (c *OpenaiClient) Name() string {
	return "openai"
}

func NewClient(opts llm.DriverOptions) *OpenaiClient {
	// Default to medium reasoning effort if not set or disabled
	if opts.ReasoningEffort == "" {
		opts.ReasoningEffort = "medium"
	}
	// Note: "disabled" is a valid value that means no reasoning - don't override it
	openaiClientOptions := []option.RequestOption{}
	if opts.ApiKey != "" {
		openaiClientOptions = append(openaiClientOptions, option.WithAPIKey(opts.ApiKey))
	}
	if opts.BaseURL != "" {
		openaiClientOptions = append(openaiClientOptions, option.WithBaseURL(opts.BaseURL))
	}

	if opts.ExtraHeaders != nil {
		for key, value := range opts.ExtraHeaders {
			openaiClientOptions = append(openaiClientOptions, option.WithHeader(key, value))
		}
	}

	// Use streaming HTTP client with DNS resilience, ResponseHeaderTimeout (2min),
	// and idle stream timeout (5min) to detect silent hangs during streaming.
	openaiClientOptions = append(openaiClientOptions, option.WithHTTPClient(llm.StreamingHTTPClient()))
	client := openai.NewClient(openaiClientOptions...)
	return &OpenaiClient{
		Options: opts,
		Client:  client,
	}
}

// ConvertMessages converts internal messages to OpenAI format (exported for OpenRouter)
func (o *OpenaiClient) ConvertMessages(prompts []string, messages []message.Message) (openaiMessages []openai.ChatCompletionMessageParamUnion) {
	for _, prompt := range AppendOpenAIFamilyGuidance(prompts, o.Options.Model) {
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
				// Only process image files as image_url, skip non-image files like PDFs
				if isImageMimeType(binaryContent.MIMEType) {
					imageURL := openai.ChatCompletionContentPartImageImageURLParam{URL: binaryContent.String(Family)}
					imageBlock := openai.ChatCompletionContentPartImageParam{ImageURL: imageURL}
					content = append(content, openai.ChatCompletionContentPartUnionParam{OfImageURL: &imageBlock})
				} else {
					fileData := "data:" + binaryContent.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(binaryContent.Data)
					filename := filepath.Base(binaryContent.Path)
					if filename == "" || filename == "." {
						filename = "file"
					}
					filePart := openai.FileContentPart(openai.ChatCompletionContentPartFileFileParam{
						FileData: param.NewOpt(fileData),
						Filename: param.NewOpt(filename),
					})
					content = append(content, filePart)
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

			// Always set content, even if empty (OpenAI API requires it)
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
				if len(result.BinaryParts) > 0 {
					// OpenAI doesn't support images in tool messages — inject as follow-up user message
					var contentParts []openai.ChatCompletionContentPartUnionParam
					contentParts = append(contentParts, openai.ChatCompletionContentPartUnionParam{
						OfText: &openai.ChatCompletionContentPartTextParam{Text: "Tool result attachments:"},
					})
					for _, bp := range result.BinaryParts {
						if isImageMimeType(bp.MIMEType) {
							imageURL := openai.ChatCompletionContentPartImageImageURLParam{
								URL: bp.String(Family),
							}
							contentParts = append(contentParts, openai.ChatCompletionContentPartUnionParam{
								OfImageURL: &openai.ChatCompletionContentPartImageParam{ImageURL: imageURL},
							})
						} else {
							fileData := "data:" + bp.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(bp.Data)
							filename := filepath.Base(bp.Path)
							if filename == "" || filename == "." {
								filename = "file"
							}
							filePart := openai.FileContentPart(openai.ChatCompletionContentPartFileFileParam{
								FileData: param.NewOpt(fileData),
								Filename: param.NewOpt(filename),
							})
							contentParts = append(contentParts, filePart)
						}
					}
					openaiMessages = append(openaiMessages, openai.UserMessage(contentParts))
				}
			}
		}
	}

	return
}

// ConvertTools converts internal tools to OpenAI format (exported for OpenRouter)
func (o *OpenaiClient) ConvertTools(tools []tools.Tool) []openai.ChatCompletionToolUnionParam {
	openaiTools := make([]openai.ChatCompletionToolUnionParam, len(tools))

	for i, tool := range tools {
		schema := tool.ParamSchema()

		// Ensure required is always an array (even if empty) for OpenAI compatibility
		required := schema.Required
		if required == nil {
			required = []string{}
		}

		// Convert properties from OrderedMap to regular map for OpenAI
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

func (o *OpenaiClient) finishReason(reason string) message.FinishReason {
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

func (o *OpenaiClient) preparedParams(messages []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolUnionParam) openai.ChatCompletionNewParams {
	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(o.Options.Model.APIModel),
		Messages: messages,
	}

	// Only set tools if there are any
	if len(tools) > 0 {
		params.Tools = tools
	}

	// Add temperature if specified in options
	if o.Options.Temperature != nil {
		params.Temperature = openai.Float(*o.Options.Temperature)
	}

	if o.Options.Model.CanReason || o.Options.Model.UseMaxCompletionTokens {
		params.MaxCompletionTokens = openai.Int(o.Options.MaxTokens)
		// Only set reasoning effort if model can reason AND reasoning is not disabled
		if o.Options.Model.CanReason && o.Options.ReasoningEffort != "disabled" && o.Options.ReasoningEffort != "none" {
			switch o.Options.ReasoningEffort {
			case "low":
				params.ReasoningEffort = shared.ReasoningEffortLow
			case "medium":
				params.ReasoningEffort = shared.ReasoningEffortMedium
			case "high":
				params.ReasoningEffort = shared.ReasoningEffortHigh
			case "xhigh":
				params.ReasoningEffort = shared.ReasoningEffort("xhigh")
			default:
				params.ReasoningEffort = shared.ReasoningEffortMedium
			}
		}
	} else {
		params.MaxTokens = openai.Int(o.Options.MaxTokens)
	}

	return params
}

func (o *OpenaiClient) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) (response *llm.DriverResponse, err error) {
	if o.Options.Model.PreferredEndpoint == "responses" {
		return o.sendResponses(ctx, prompts, messages, tools)
	}

	params := o.preparedParams(o.ConvertMessages(prompts, messages), o.ConvertTools(tools))
	// Debug logging temporarily disabled due to config migration
	if false { // Debug disabled
		jsonData, _ := json.Marshal(params)
		logging.Debug("Prepared messages", "messages", string(jsonData))
	}
	attempts := 0
	for {
		attempts++
		var rawResp *http.Response
		openaiResponse, err := o.Client.Chat.Completions.New(
			ctx,
			params,
			option.WithResponseInto(&rawResp),
		)
		// If there is an error we are going to see if we can retry the call
		if err != nil {
			retry, after, retryErr := o.shouldRetry(attempts, err)
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

		toolCalls := o.toolCalls(*openaiResponse)
		finishReason := o.finishReason(string(openaiResponse.Choices[0].FinishReason))

		if len(toolCalls) > 0 {
			finishReason = message.FinishReasonToolUse
		}

		upstreamRequestID, upstreamProxymanID := extractUpstreamCorrelationHeaders(rawResp)

		return &llm.DriverResponse{
			Content:            content,
			ToolCalls:          toolCalls,
			Usage:              o.usage(*openaiResponse),
			FinishReason:       finishReason,
			UpstreamRequestID:  upstreamRequestID,
			UpstreamProxymanID: upstreamProxymanID,
		}, nil
	}
}

func (o *OpenaiClient) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) <-chan llm.DriverEvent {
	if o.Options.Model.PreferredEndpoint == "responses" {
		return o.streamResponses(ctx, prompts, messages, tools)
	}

	params := o.preparedParams(o.ConvertMessages(prompts, messages), o.ConvertTools(tools))
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: openai.Bool(true),
	}

	// Debug logging temporarily disabled due to config migration
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
			openaiStream := o.Client.Chat.Completions.NewStreaming(
				ctx,
				params,
				option.WithResponseInto(&streamResp),
			)

			acc := openai.ChatCompletionAccumulator{}
			currentContent := ""
			toolCalls := make([]message.ToolCall, 0)

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
				// Stream completed successfully
				var finishReason message.FinishReason

				// Check if we have choices data before accessing
				if len(acc.Choices) > 0 {
					finishReason = o.finishReason(string(acc.ChatCompletion.Choices[0].FinishReason))
					if len(acc.Choices[0].Message.ToolCalls) > 0 {
						toolCalls = append(toolCalls, o.toolCalls(acc.ChatCompletion)...)
					}
				} else if currentContent != "" {
					// Stream ended without proper completion data but we have content
					// This can happen with OpenRouter when the stream is interrupted
					logging.Warn("Stream ended without completion choices but has content (length: %d) - treating as interrupted stream", len(currentContent))
					// Send an error event to trigger retry
					eventChan <- llm.DriverEvent{
						Type:  llm.EventError,
						Error: fmt.Errorf("stream interrupted: incomplete response from OpenRouter"),
					}
					close(eventChan)
					return
				} else {
					// No content and no choices - completely empty response
					logging.Error("Stream ended with no content and no completion data")
					eventChan <- llm.DriverEvent{
						Type:  llm.EventError,
						Error: fmt.Errorf("empty response from OpenRouter"),
					}
					close(eventChan)
					return
				}

				if len(toolCalls) > 0 {
					finishReason = message.FinishReasonToolUse
				}

				upstreamRequestID, upstreamProxymanID := extractUpstreamCorrelationHeaders(streamResp)

				eventChan <- llm.DriverEvent{
					Type: llm.EventComplete,
					Response: &llm.DriverResponse{
						Content:            currentContent,
						ToolCalls:          toolCalls,
						Usage:              o.usage(acc.ChatCompletion),
						FinishReason:       finishReason,
						UpstreamRequestID:  upstreamRequestID,
						UpstreamProxymanID: upstreamProxymanID,
					},
				}
				close(eventChan)
				return
			}

			// If there is an error we are going to see if we can retry the call
			retry, after, retryErr := o.shouldRetry(attempts, err)
			if retryErr != nil {
				eventChan <- llm.DriverEvent{Type: llm.EventError, Error: retryErr}
				close(eventChan)
				return
			}
			if retry {
				logging.Warn(fmt.Sprintf("Retrying due to rate limit... attempt %d of %d", attempts, models.MaxRetries))
				select {
				case <-ctx.Done():
					// context cancelled
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

func (o *OpenaiClient) shouldRetry(attempts int, err error) (bool, int64, error) {
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

func (o *OpenaiClient) toolCalls(completion openai.ChatCompletion) []message.ToolCall {
	var toolCalls []message.ToolCall

	if len(completion.Choices) > 0 && len(completion.Choices[0].Message.ToolCalls) > 0 {
		for _, call := range completion.Choices[0].Message.ToolCalls {
			// Skip empty or invalid tool calls
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

func (o *OpenaiClient) usage(completion openai.ChatCompletion) llm.TokenUsage {
	usage := llm.TokenUsage{
		TokenCount:   completion.Usage.PromptTokens,
		InputTokens:  completion.Usage.PromptTokens,
		OutputTokens: completion.Usage.CompletionTokens,
	}
	usage.CostMicros = o.calculateCostMicros(usage)
	return usage
}

func (o *OpenaiClient) calculateCostMicros(usage llm.TokenUsage) int64 {
	model := o.Options.Model
	if model.CostPer1MIn <= 0 && model.CostPer1MOut <= 0 {
		return 0
	}

	inputCostMicros := int64(math.Round(float64(usage.InputTokens) * model.CostPer1MIn / 1_000_000 * 1_000_000))
	outputCostMicros := int64(math.Round(float64(usage.OutputTokens) * model.CostPer1MOut / 1_000_000 * 1_000_000))
	return inputCostMicros + outputCostMicros
}

func (o *OpenaiClient) Model() models.Model {
	return o.Options.Model
}

func (o *OpenaiClient) ValidateKey(ctx context.Context) error {
	// Use GPT-5.4 Mini for validation (small, fast model)
	testMessages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Say 'test' and nothing else"},
			},
		},
	}

	// Create a temporary client with the small model
	validationOpts := o.Options
	registry := models.MustGetRegistry()
	if def, ok := registry.GetDefinition(string(models.GPT54Mini)); ok {
		validationOpts.Model = def.ToModel()
	}
	// OpenAI Responses API enforces max_output_tokens >= 16.
	// Keep this small (validation request), but above the minimum.
	validationOpts.MaxTokens = 16

	validationClient := NewClient(validationOpts)

	_, err := validationClient.SendMessages(ctx, []string{}, testMessages, []tools.Tool{})
	return err
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
	// Simple filename extraction - can be improved
	parts := []rune(path)
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == '/' || parts[i] == '\\' {
			return string(parts[i+1:])
		}
	}
	return path
}

func (o *OpenaiClient) responsesInstructions(prompts []string) string {
	var sb strings.Builder
	for _, p := range AppendOpenAIFamilyGuidance(prompts, o.Options.Model) {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(p)
	}
	return sb.String()
}

func (o *OpenaiClient) convertMessagesToResponsesInput(prompts []string, messages []message.Message) responses.ResponseInputParam {
	items := make([]responses.ResponseInputItemUnionParam, 0, len(messages)+len(prompts))

	truncate64 := func(s string) string {
		if len(s) > 64 {
			return s[:64]
		}
		return s
	}

	// Represent system prompts and shared OpenAI-family guidance as a developer message input item.
	if inst := o.responsesInstructions(prompts); inst != "" {
		items = append(items, responses.ResponseInputItemParamOfMessage(inst, responses.EasyInputMessageRoleDeveloper))
	}

	for _, msg := range messages {
		switch msg.Role {
		case message.User:
			textParts := make([]string, 0)
			for _, tc := range msg.TextContents() {
				if tc.Text != "" {
					textParts = append(textParts, tc.Text)
				}
			}
			items = append(items, responses.ResponseInputItemParamOfMessage(strings.Join(textParts, "\n\n"), responses.EasyInputMessageRoleUser))

		case message.Assistant:
			// Preserve assistant tool calls for Responses so that subsequent
			// function_call_output items can reference the call_id.
			//
			// IMPORTANT: Responses function_call 'arguments' must be the JSON arguments string,
			// and 'name' must be the tool name (max length 64). Our internal ToolCall has:
			//   - ID: call_id
			//   - Name: tool name
			//   - Input: JSON arguments
			items = append(items, responses.ResponseInputItemParamOfMessage(msg.Content().String(), responses.EasyInputMessageRoleUser))
			for _, tc := range msg.ToolCalls() {
				// openai-go signature is (arguments, callID, name)
				items = append(items, responses.ResponseInputItemParamOfFunctionCall(tc.Input, tc.ID, truncate64(tc.Name)))
			}

		case message.Tool:
			for _, result := range msg.ToolResults() {
				items = append(items, responses.ResponseInputItemParamOfFunctionCallOutput(result.ToolCallID, result.Content))
			}
		}
	}

	// Debug: help identify which function_call item is violating name length constraints.
	if os.Getenv("RELIANT_DEBUG_OPENAI_RESPONSES_INPUT") == "1" {
		for i, it := range items {
			// ResponseInputItemUnionParam is a union; easiest is to JSON marshal and check for a name field.
			if b, err := json.Marshal(it); err == nil {
				var m map[string]any
				if json.Unmarshal(b, &m) == nil {
					if name, ok := m["name"].(string); ok && len(name) > 64 {
						logging.Warn("[OpenAI Responses] input item has long name", "index", i, "len", len(name), "name", name)
					}
				}
			}
		}
	}

	return responses.ResponseInputParam(items)
}

func (o *OpenaiClient) convertToolsToResponsesTools(toolList []tools.Tool) []responses.ToolUnionParam {
	result := make([]responses.ToolUnionParam, 0, len(toolList))
	for _, tool := range toolList {
		// Build strict JSON Schema the way openai-go recommends for Structured Outputs:
		// - AllowAdditionalProperties=false
		// - DoNotReference=true
		var params map[string]any
		// Prefer raw schema map when available (preserves nested structure for schema-only tools).
		type schemaMapProvider interface{ ParamSchemaMap() map[string]any }
		if p, ok := tool.(schemaMapProvider); ok {
			params = p.ParamSchemaMap()
		} else {
			schema := tool.ParamSchema()
			b, _ := json.Marshal(schema)
			_ = json.Unmarshal(b, &params) //nolint:errcheck
		}
		// Ensure root is always a strict object schema for OpenAI function tools.
		// Some upstream MCP schemas are malformed/underspecified after parsing (e.g. missing type),
		// but OpenAI Responses requires root parameters to be a closed object.
		if params == nil {
			params = map[string]any{}
		}
		params["type"] = "object"
		NormalizeResponsesToolSchema(params)
		params["additionalProperties"] = false
		if params["properties"] == nil {
			params["properties"] = map[string]any{}
		}
		if params["required"] == nil {
			params["required"] = []any{}
		}

		d := tool.Description()
		ft := &responses.FunctionToolParam{
			Name:       tool.Name(),
			Parameters: params,
			// NOTE: Keep strict=false for function tools. OpenAI's strict=true path
			// enforces a narrow JSON-schema subset and has proven brittle with
			// third-party MCP-emitted schemas. We still validate/coerce tool inputs
			// on execution, so correctness is preserved without request-level hard fails.
			Strict:      openai.Bool(false),
			Description: openai.String(d),
		}

		// Final safety check: if schema is still not OpenAI-strict after normalization,
		// fall back to a safe empty object schema rather than hard-failing the entire request.
		if err := ValidateResponsesToolSchemaStrict(params); err != nil {
			logging.Warn("[OpenAI Responses] Invalid strict tool schema after normalization; applying safe fallback",
				"tool", tool.Name(),
				"error", err,
			)
			params = map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{},
				"required":             []any{},
			}
			ft.Parameters = params
		}

		// Debug log the schema we actually send to OpenAI Responses when requested.
		// This is critical because OpenAI validates the *schema*, not our tool types.
		if os.Getenv("RELIANT_DEBUG_OPENAI_TOOL_SCHEMAS") == "1" {
			if b, err := json.MarshalIndent(ft, "", "  "); err == nil {
				logging.Info("[OpenAI Responses] function tool", "tool", tool.Name(), "payload", string(b))
			}
		}
		if d == "" {
			ft.Description = openai.String("")
		}
		result = append(result, responses.ToolUnionParam{OfFunction: ft})
	}
	return result
}

func (o *OpenaiClient) sendResponses(ctx context.Context, prompts []string, messages []message.Message, toolList []tools.Tool) (*llm.DriverResponse, error) {
	params := responses.ResponseNewParams{
		Model:             shared.ResponsesModel(o.Options.Model.APIModel),
		Input:             responses.ResponseNewParamsInputUnion{OfInputItemList: o.convertMessagesToResponsesInput(prompts, messages)},
		ParallelToolCalls: openai.Bool(true),
	}

	// NOTE: Function-call name truncation is handled in convertMessagesToResponsesInput.

	if len(toolList) > 0 {
		params.Tools = o.convertToolsToResponsesTools(toolList)
	}

	if o.Options.Temperature != nil {
		params.Temperature = openai.Float(*o.Options.Temperature)
	}

	params.MaxOutputTokens = openai.Int(o.Options.MaxTokens)
	if o.Options.Model.CanReason && o.Options.ReasoningEffort != "disabled" && o.Options.ReasoningEffort != "none" {
		effort := shared.ReasoningEffortMedium
		switch o.Options.ReasoningEffort {
		case "low":
			effort = shared.ReasoningEffortLow
		case "medium":
			effort = shared.ReasoningEffortMedium
		case "high":
			effort = shared.ReasoningEffortHigh
		case "xhigh":
			effort = shared.ReasoningEffort("xhigh")
		}
		params.Reasoning = shared.ReasoningParam{Effort: effort}
	}

	var rawResp *http.Response
	resp, err := o.Client.Responses.New(ctx, params, option.WithResponseInto(&rawResp))
	if err != nil {
		return nil, err
	}
	if resp.Error.Message != "" {
		return nil, fmt.Errorf("openai responses error: %s", resp.Error.Message)
	}

	content := resp.OutputText()
	toolCalls := make([]message.ToolCall, 0)
	for _, out := range resp.Output {
		if fc := out.AsFunctionCall(); fc.Type == "function_call" && fc.CallID != "" {
			toolCalls = append(toolCalls, message.ToolCall{
				ID:       fc.CallID,
				Name:     fc.Name,
				Input:    fc.Arguments,
				Type:     "function",
				Finished: true,
			})
		}
	}

	finishReason := message.FinishReasonEndTurn
	if len(toolCalls) > 0 {
		finishReason = message.FinishReasonToolUse
	}

	usage := llm.TokenUsage{}
	if resp.Usage.TotalTokens > 0 {
		usage.TokenCount = resp.Usage.TotalTokens
		usage.InputTokens = resp.Usage.InputTokens
		usage.OutputTokens = resp.Usage.OutputTokens
		usage.CostMicros = o.calculateCostMicros(usage)
	}

	upstreamRequestID, upstreamProxymanID := extractUpstreamCorrelationHeaders(rawResp)

	return &llm.DriverResponse{
		Content:            content,
		ToolCalls:          toolCalls,
		Usage:              usage,
		FinishReason:       finishReason,
		UpstreamRequestID:  upstreamRequestID,
		UpstreamProxymanID: upstreamProxymanID,
	}, nil
}

func extractUpstreamCorrelationHeaders(resp *http.Response) (requestID string, proxymanID string) {
	if resp == nil {
		return "", ""
	}
	return strings.TrimSpace(resp.Header.Get("x-oai-request-id")), strings.TrimSpace(resp.Header.Get("x-proxyman-id"))
}

func (o *OpenaiClient) streamResponses(ctx context.Context, prompts []string, messages []message.Message, toolList []tools.Tool) <-chan llm.DriverEvent {
	eventChan := make(chan llm.DriverEvent)

	go func() {
		defer close(eventChan)

		params := responses.ResponseNewParams{
			Model:             shared.ResponsesModel(o.Options.Model.APIModel),
			Input:             responses.ResponseNewParamsInputUnion{OfInputItemList: o.convertMessagesToResponsesInput(prompts, messages)},
			ParallelToolCalls: openai.Bool(true),
		}
		if len(toolList) > 0 {
			params.Tools = o.convertToolsToResponsesTools(toolList)
		}
		if o.Options.Temperature != nil {
			params.Temperature = openai.Float(*o.Options.Temperature)
		}
		params.MaxOutputTokens = openai.Int(o.Options.MaxTokens)
		if o.Options.Model.CanReason && o.Options.ReasoningEffort != "disabled" && o.Options.ReasoningEffort != "none" {
			effort := shared.ReasoningEffortMedium
			switch o.Options.ReasoningEffort {
			case "low":
				effort = shared.ReasoningEffortLow
			case "medium":
				effort = shared.ReasoningEffortMedium
			case "high":
				effort = shared.ReasoningEffortHigh
			case "xhigh":
				effort = shared.ReasoningEffort("xhigh")
			}
			// Enable reasoning summary streaming so we get events during thinking
			// Some models (e.g., gpt-5.2-codex) only support 'detailed' summary mode
			summaryMode := shared.ReasoningSummaryConcise
			if o.Options.Model.ReasoningSummaryMode == models.ReasoningSummaryDetailedOnly {
				summaryMode = shared.ReasoningSummaryDetailed
			}
			params.Reasoning = shared.ReasoningParam{
				Effort:  effort,
				Summary: summaryMode,
			}
		}

		var streamResp *http.Response
		stream := o.Client.Responses.NewStreaming(ctx, params, option.WithResponseInto(&streamResp))

		currentContent := ""
		toolCallsByID := make(map[string]*message.ToolCall)
		var finalResp *responses.Response

		for stream.Next() {
			ev := stream.Current()

			switch v := ev.AsAny().(type) {
			case responses.ResponseTextDeltaEvent:
				if v.Delta != "" {
					currentContent += v.Delta
					eventChan <- llm.DriverEvent{Type: llm.EventContentDelta, Content: v.Delta}
				}
			case responses.ResponseOutputItemAddedEvent:
				// Detect when reasoning item is added - signals start of thinking
				if reasoning := v.Item.AsReasoning(); reasoning.Type == "reasoning" {
					eventChan <- llm.DriverEvent{Type: llm.EventThinkingStart}
				}
			case responses.ResponseOutputItemDoneEvent:
				// Detect when reasoning item completes
				if reasoning := v.Item.AsReasoning(); reasoning.Type == "reasoning" {
					eventChan <- llm.DriverEvent{Type: llm.EventThinkingStop}
				}
			case responses.ResponseReasoningSummaryPartAddedEvent:
				// Streaming reasoning summary delta
				if v.Part.Text != "" {
					eventChan <- llm.DriverEvent{Type: llm.EventThinkingDelta, Content: v.Part.Text}
				}
			case responses.ResponseReasoningSummaryPartDoneEvent:
				// Reasoning summary part complete - no action needed
			case responses.ResponseFunctionCallArgumentsDeltaEvent:
				// This delta references the output item, not the call_id directly.
				// We'll stitch call_id/name from the final response.
				if v.Delta == "" {
					continue
				}
				itemID := v.ItemID
				if itemID == "" {
					continue
				}
				tc := toolCallsByID[itemID]
				if tc == nil {
					tc = &message.ToolCall{ID: itemID, Type: "function"}
					toolCallsByID[itemID] = tc
					eventChan <- llm.DriverEvent{Type: llm.EventToolUseStart, ToolCall: tc}
				}
				tc.Input += v.Delta
				eventChan <- llm.DriverEvent{Type: llm.EventToolUseDelta, ToolCall: &message.ToolCall{ID: itemID, Input: v.Delta}}
			case responses.ResponseFunctionCallArgumentsDoneEvent:
				itemID := v.ItemID
				if itemID == "" {
					continue
				}
				tc := toolCallsByID[itemID]
				if tc == nil {
					tc = &message.ToolCall{ID: itemID, Type: "function"}
					toolCallsByID[itemID] = tc
					eventChan <- llm.DriverEvent{Type: llm.EventToolUseStart, ToolCall: tc}
				}
				// Done event contains full args + name; prefer it.
				tc.Name = v.Name
				tc.Input = v.Arguments
			case responses.ResponseCompletedEvent:
				finalResp = &v.Response
			default:
				// Unhandled event type - ignore
			}
		}

		if err := stream.Err(); err != nil {
			logging.Error("[OpenAI Responses] Stream error", "error", err)
			eventChan <- llm.DriverEvent{Type: llm.EventError, Error: err}
			return
		}

		// If we received a completed response, map function_call output items into tool calls.
		if finalResp != nil {
			for _, out := range finalResp.Output {
				if fc := out.AsFunctionCall(); fc.Type == "function_call" && fc.CallID != "" {
					itemID := fc.ID
					if itemID == "" {
						itemID = fc.CallID
					}
					tc := toolCallsByID[itemID]
					if tc == nil {
						tc = &message.ToolCall{ID: itemID, Type: "function"}
						toolCallsByID[itemID] = tc
					}
					// Ensure the ID we expose to our system matches the tool_call_id used for tool outputs.
					// That must be call_id.
					tc.ID = fc.CallID
					tc.Name = fc.Name
					if tc.Input == "" {
						tc.Input = fc.Arguments
					}
				}
			}
		}

		finalToolCalls := make([]message.ToolCall, 0, len(toolCallsByID))
		for _, tc := range toolCallsByID {
			// Drop incomplete ones without a call_id.
			if tc.ID == "" || tc.Name == "" {
				continue
			}
			tc.Finished = true
			finalToolCalls = append(finalToolCalls, *tc)
			eventChan <- llm.DriverEvent{Type: llm.EventToolUseStop, ToolCall: tc}
		}

		finishReason := message.FinishReasonEndTurn
		if len(finalToolCalls) > 0 {
			finishReason = message.FinishReasonToolUse
		}

		usage := llm.TokenUsage{}
		if finalResp != nil && finalResp.Usage.TotalTokens > 0 {
			usage.TokenCount = finalResp.Usage.TotalTokens
			usage.InputTokens = finalResp.Usage.InputTokens
			usage.OutputTokens = finalResp.Usage.OutputTokens
			usage.CostMicros = o.calculateCostMicros(usage)
		}

		upstreamRequestID, upstreamProxymanID := extractUpstreamCorrelationHeaders(streamResp)

		eventChan <- llm.DriverEvent{
			Type: llm.EventComplete,
			Response: &llm.DriverResponse{
				Content:            currentContent,
				ToolCalls:          finalToolCalls,
				Usage:              usage,
				FinishReason:       finishReason,
				UpstreamRequestID:  upstreamRequestID,
				UpstreamProxymanID: upstreamProxymanID,
			},
		}
	}()

	return eventChan
}
