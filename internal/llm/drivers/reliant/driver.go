// Copyright (c) 2025 Reliant Labs
//
// forge:exclude-contract
//
// This package implements the llm.Driver interface declared in
// internal/llm/types.go. Its behavioral contract already exists upstream, and
// the exported methods here are that interface's implementation plus
// provider-specific wire handling. A local contract.go would restate an
// interface this package does not own.
package reliant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/reliant-labs/reliant/internal/chatmarkers"
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

func apiKeyPrefixForLog(apiKey string) string {
	trimmedKey := strings.TrimSpace(apiKey)
	if trimmedKey == "" {
		return "none"
	}
	if strings.HasPrefix(trimmedKey, "rlnt_") {
		return "rlnt_"
	}
	if strings.HasPrefix(trimmedKey, "rly_") {
		return "rly_"
	}
	if strings.HasPrefix(trimmedKey, "sk-") {
		return "sk-"
	}
	return "other"
}

func apiKeyTypeForLog(apiKey string) string {
	trimmedKey := strings.TrimSpace(apiKey)
	if trimmedKey == "" {
		return "empty"
	}
	if strings.HasPrefix(trimmedKey, "rlnt_") || strings.HasPrefix(trimmedKey, "rly_") {
		return "managed_reliant"
	}
	if strings.HasPrefix(trimmedKey, "sk-") {
		return "openai_compatible"
	}
	return "unknown"
}

func hasExtraHeader(headers map[string]string, targetKey string) bool {
	for key := range headers {
		if strings.EqualFold(key, targetKey) {
			return true
		}
	}
	return false
}

func extraHeaderKeysForLog(headers map[string]string) []string {
	if len(headers) == 0 {
		return nil
	}

	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func NewClient(opts llm.DriverOptions) *ReliantClient {
	// Look up the correct Reliant API model from the registry
	if apiModel, err := getReliantAPIModel(opts.Model.ID); err == nil {
		opts.Model.APIModel = apiModel
	}

	logging.Info("Reliant driver client configured",
		"base_url", opts.BaseURL,
		"api_key_prefix", apiKeyPrefixForLog(opts.ApiKey),
		"api_key_type", apiKeyTypeForLog(opts.ApiKey),
		"has_x_reliant_managed_key", hasExtraHeader(opts.ExtraHeaders, "X-Reliant-Managed-Key"),
		"extra_header_keys", extraHeaderKeysForLog(opts.ExtraHeaders),
		"model", opts.Model.ID,
		"api_model", opts.Model.APIModel,
	)

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
	client := llm.NewOpenAISDKClient(clientOptions...)
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
	case "content_filter":
		// The OpenAI-shaped spelling of a provider refusal. Like Anthropic's
		// "refusal" it can arrive with no content at all.
		return message.FinishReasonRefusal
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

func (c *ReliantClient) preparedParams(messages []openai.ChatCompletionMessageParamUnion, toolParams []openai.ChatCompletionToolUnionParam) openai.ChatCompletionNewParams {
	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(c.Options.Model.APIModel),
		Messages: messages,
	}

	if len(toolParams) > 0 {
		params.Tools = toolParams
		// A tool_choice naming a tool absent from Tools is a provider 400, so
		// only pin when the named tool is actually in this request's list.
		if c.Options.ForceToolChoice != "" && toolListHasFunction(toolParams, c.Options.ForceToolChoice) {
			params.ToolChoice = openai.ToolChoiceOptionFunctionToolChoice(
				openai.ChatCompletionNamedToolChoiceFunctionParam{Name: c.Options.ForceToolChoice},
			)
		}
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
			retry, wait, retryErr := c.shouldRetry(attempts, err)
			if retryErr != nil {
				return nil, retryErr
			}
			if retry {
				logging.Warn("Retrying Reliant API request",
					"attempt", attempts,
					"max_retries", models.MaxRetries,
					"after_ms", wait.Delay.Milliseconds(),
				)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(wait.Delay):
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
					logging.Warn(fmt.Sprintf("Stream ended without completion choices but has content (length: %d) - treating as interrupted stream", len(currentContent)))
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

			retry, wait, retryErr := c.shouldRetry(attempts, err)
			if retryErr != nil {
				eventChan <- llm.DriverEvent{Type: llm.EventError, Error: retryErr}
				close(eventChan)
				return
			}
			if retry {
				logging.Warn("Retrying Reliant API request",
					"attempt", attempts,
					"max_retries", models.MaxRetries,
					"after_ms", wait.Delay.Milliseconds(),
				)
				// Announce the wait BEFORE taking it. The whole ladder runs inside
				// one Temporal activity attempt, so without this the run emits
				// nothing at all while it sleeps: measured on run b7aa4056, eight of
				// ten fan-out units spent ~113s of their ~129s life here and every
				// supervision surface read it as "the model is thinking".
				select {
				case eventChan <- llm.DriverEvent{Type: llm.EventRetryWait, Retry: &wait}:
				case <-ctx.Done():
					close(eventChan)
					return
				}
				select {
				case <-ctx.Done():
					if ctx.Err() != nil {
						eventChan <- llm.DriverEvent{Type: llm.EventError, Error: ctx.Err()}
					}
					close(eventChan)
					return
				case <-time.After(wait.Delay):
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

// shouldRetry decides whether a failed request is retryable and, when it is,
// describes the wait about to be taken (attempt, delay, provider status, and the
// decision reason). The description is returned rather than only logged so the
// caller can publish it: a backoff that exists only in a log line is invisible
// to every surface a supervisor reads.
func (c *ReliantClient) shouldRetry(attempts int, err error) (bool, llm.RetryWait, error) {
	var apierr *openai.Error
	if !errors.As(err, &apierr) {
		return false, llm.RetryWait{}, err
	}

	retry, reason := shouldRetryReliantAPIError(apierr)
	c.logReliantAPIError(attempts, apierr, retry, reason)
	if !retry {
		// Surface the reliant-managed quota-exhausted case as a typed,
		// marker-bearing error so the frontend can detect it across the
		// Temporal serialization boundary (see ErrReliantManagedQuotaExhausted).
		if reason == "reliant_managed_quota_exhausted" {
			return false, llm.RetryWait{}, wrapReliantManagedQuotaError(apierr)
		}
		return false, llm.RetryWait{}, err
	}

	if attempts > models.MaxRetries {
		return false, llm.RetryWait{}, reliantRetryExhaustedError(apierr)
	}

	return true, llm.RetryWait{
		Attempt:     attempts,
		MaxAttempts: models.MaxRetries,
		Delay:       time.Duration(retryDelayMs(attempts, apierr)) * time.Millisecond,
		StatusCode:  apierr.StatusCode,
		Reason:      reason,
	}, nil
}

func shouldRetryReliantAPIError(apierr *openai.Error) (bool, string) {
	if apierr == nil {
		return false, "missing_api_error"
	}

	errText := strings.ToLower(strings.Join([]string{
		apierr.Message,
		apierr.Type,
		apierr.Code,
		apierr.RawJSON(),
	}, " "))

	if containsAny(errText,
		"reauthentication is needed",
		"application-default login",
		"invalid api key",
		"invalid authentication credentials",
		"authentication failed",
		"unauthorized",
		"forbidden",
		"permission denied",
		"refresherror",
	) {
		return false, "terminal_auth_config_error"
	}

	// HTTP 429 + error.code == "insufficient_quota" is the reliant-managed
	// free-tier global budget exhaustion signal (see
	// control-plane/internal/service/llmproxy/proxy.go). Retrying is futile —
	// the budget is hard-capped for the month. Surface as a terminal error
	// so the workflow stops retrying and the frontend can open the
	// upgrade-required modal.
	if apierr.StatusCode == 429 && isReliantManagedQuotaError(apierr) {
		return false, "reliant_managed_quota_exhausted"
	}

	switch apierr.StatusCode {
	case 429:
		return true, "http_429"
	case 502, 503, 504:
		return true, "transient_gateway_error"
	case 500:
		if containsAny(errText,
			"internal server error",
			"overloaded",
			"service unavailable",
			"gateway timeout",
			"bad gateway",
			"try again",
			"temporary",
			"timeout",
		) {
			return true, "transient_upstream_500"
		}
		return false, "non_retryable_500"
	default:
		return false, fmt.Sprintf("non_retryable_status_%d", apierr.StatusCode)
	}
}

func retryDelayMs(attempts int, apierr *openai.Error) int64 {
	retryMs := 0
	if apierr != nil && apierr.Response != nil {
		retryAfterValues := apierr.Response.Header.Values("Retry-After")
		if len(retryAfterValues) > 0 {
			if _, err := fmt.Sscanf(retryAfterValues[0], "%d", &retryMs); err == nil {
				return int64(retryMs * 1000)
			}
		}
	}

	backoffMs := 2000 * (1 << (attempts - 1))
	jitterMs := int(float64(backoffMs) * 0.2)
	return int64(backoffMs + jitterMs)
}

func reliantRetryExhaustedError(apierr *openai.Error) error {
	if apierr != nil && apierr.StatusCode == 429 {
		return fmt.Errorf("maximum retry attempts reached for rate limit: %d retries", models.MaxRetries)
	}
	statusCode := 0
	if apierr != nil {
		statusCode = apierr.StatusCode
	}
	return fmt.Errorf("maximum retry attempts reached for transient Reliant API error (status %d): %d retries", statusCode, models.MaxRetries)
}

func (c *ReliantClient) logReliantAPIError(attempts int, apierr *openai.Error, retry bool, reason string) {
	if apierr == nil {
		return
	}

	logFn := logging.Error
	if retry {
		logFn = logging.Warn
	}

	requestID, proxymanID := extractUpstreamCorrelationHeaders(apierr.Response)
	litellmCallID := ""
	litellmModelID := ""
	if apierr.Response != nil {
		litellmCallID = strings.TrimSpace(apierr.Response.Header.Get("x-litellm-call-id"))
		litellmModelID = strings.TrimSpace(apierr.Response.Header.Get("x-litellm-model-id"))
	}

	logFn("Reliant API request failed",
		"attempt", attempts,
		"retryable", retry,
		"decision_reason", reason,
		"status_code", apierr.StatusCode,
		"error_type", strings.TrimSpace(apierr.Type),
		"error_code", strings.TrimSpace(apierr.Code),
		"message", summarizeReliantAPIError(apierr),
		"request_id", requestID,
		"litellm_call_id", litellmCallID,
		"litellm_model_id", litellmModelID,
		"proxyman_id", proxymanID,
	)
}

func summarizeReliantAPIError(apierr *openai.Error) string {
	if apierr == nil {
		return ""
	}

	text := strings.TrimSpace(apierr.Message)
	if text == "" {
		text = strings.TrimSpace(apierr.RawJSON())
	}
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 320 {
		return text[:320] + "…"
	}
	return text
}

func containsAny(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

// ReliantManagedQuotaMarker is a stable substring baked into the error message
// returned when the reliant-managed (LiteLLM virtual key) free-tier budget is
// exhausted. The marker survives Temporal's JSON stringification of activity
// errors so the frontend can detect the case in the chat-error stream and
// surface the upgrade-required modal.
//
// The canonical contract lives in internal/chatmarkers — this constant is a
// thin local alias kept for legacy call-site / test readability. New code
// should refer to chatmarkers.KindReliantManagedQuotaExhausted directly.
const ReliantManagedQuotaMarker = string(chatmarkers.KindReliantManagedQuotaExhausted)

// DefaultReliantUpgradeURL is the path embedded when the upstream proxy didn't
// supply one. The frontend resolves it against window.location.origin.
const DefaultReliantUpgradeURL = "/billing/plans"

// ErrReliantManagedQuotaExhausted is the sentinel error returned when the
// reliant-managed LLM (LiteLLM virtual key) free-tier global budget is
// exhausted. Only the reliant driver emits this — user-provided keys to
// OpenAI / Anthropic / etc. surface their own provider's quota errors
// unchanged, which is correct because that's the user's own billing
// relationship, not ours.
//
// The error's Error() string embeds ReliantManagedQuotaMarker and the
// upgrade URL so the frontend can recognize it after Temporal serializes
// the activity error to a string.
type ErrReliantManagedQuotaExhausted struct {
	// UpgradeURL is the path/URL the user should be sent to in order to
	// upgrade their plan. Sourced from the proxy's `error.upgrade_url` JSON
	// field; falls back to DefaultReliantUpgradeURL when missing.
	UpgradeURL string
	// Message is the upstream proxy's human-readable error message
	// (e.g. "Free tier quota exceeded — please upgrade your plan.").
	Message string
}

// Error returns a string carrying the chatmarkers.KindReliantManagedQuotaExhausted
// marker + upgrade URL so downstream consumers (notably the chat-update stream
// on the frontend) can detect the case via a substring scan after Temporal
// stringifies the activity error. Format:
// `<message> [RELIANT_MANAGED_QUOTA_EXHAUSTED:<url>]`.
func (e *ErrReliantManagedQuotaExhausted) Error() string {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = "reliant-managed LLM quota exhausted"
	}
	url := e.UpgradeURL
	if url == "" {
		url = DefaultReliantUpgradeURL
	}
	return chatmarkers.Wrap(chatmarkers.KindReliantManagedQuotaExhausted, url, msg)
}

// isReliantManagedQuotaError returns true when the OpenAI-shape error body
// from LiteLLM (proxied by control-plane/internal/service/llmproxy) signals
// the free-tier global budget is exhausted. The proxy emits:
//
//	{"error":{"message":"…","type":"insufficient_quota","code":"insufficient_quota","upgrade_url":"…"}}
//
// We match on `code == "insufficient_quota"` to keep the check tight; the
// `type` field is a secondary fallback because some intermediaries (LiteLLM
// pass-throughs) may mutate one field but not both.
func isReliantManagedQuotaError(apierr *openai.Error) bool {
	if apierr == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(apierr.Code), "insufficient_quota") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(apierr.Type), "insufficient_quota") {
		return true
	}
	// Fall back to scanning the raw JSON — handles malformed openai-go
	// decoding paths where Code/Type didn't populate.
	return strings.Contains(strings.ToLower(apierr.RawJSON()), `"insufficient_quota"`)
}

// reliantManagedQuotaUpgradeURL pulls `error.upgrade_url` out of the proxy's
// response body. The openai-go Error struct doesn't expose vendor-specific
// fields directly, so we parse the raw JSON. Returns "" when not present;
// the caller substitutes DefaultReliantUpgradeURL.
func reliantManagedQuotaUpgradeURL(apierr *openai.Error) string {
	if apierr == nil {
		return ""
	}
	raw := apierr.RawJSON()
	if raw == "" {
		return ""
	}
	var envelope struct {
		Error struct {
			UpgradeURL string `json:"upgrade_url"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return ""
	}
	return strings.TrimSpace(envelope.Error.UpgradeURL)
}

// wrapReliantManagedQuotaError builds the typed sentinel error from an
// openai.Error known to represent the insufficient_quota case. Callers should
// have already gated on isReliantManagedQuotaError.
func wrapReliantManagedQuotaError(apierr *openai.Error) error {
	upgradeURL := reliantManagedQuotaUpgradeURL(apierr)
	if upgradeURL == "" {
		upgradeURL = DefaultReliantUpgradeURL
	}
	message := summarizeReliantAPIError(apierr)
	return &ErrReliantManagedQuotaExhausted{
		UpgradeURL: upgradeURL,
		Message:    message,
	}
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
	cachedInputTokens := completion.Usage.PromptTokensDetails.CachedTokens
	inputTokens := completion.Usage.PromptTokens - cachedInputTokens
	if inputTokens < 0 {
		inputTokens = completion.Usage.PromptTokens
		cachedInputTokens = 0
	}
	cost := (float64(inputTokens) * c.Options.Model.CostPer1MIn / 1_000_000) +
		(float64(completion.Usage.CompletionTokens) * c.Options.Model.CostPer1MOut / 1_000_000) +
		(float64(cachedInputTokens) * c.Options.Model.CostPer1MInCached / 1_000_000)
	if cost < 0 {
		cost = 0
	}
	return llm.TokenUsage{
		TokenCount:   completion.Usage.TotalTokens,
		InputTokens:  inputTokens,
		OutputTokens: completion.Usage.CompletionTokens,
		Cost:         cost,
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
