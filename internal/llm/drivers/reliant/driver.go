// Copyright (c) 2025 Reliant Labs
//
// This package implements the llm.Driver interface declared in
// internal/llm/types.go. Its behavioral contract already exists upstream, and
// the exported methods here are that interface's implementation plus
// provider-specific wire handling. A local contract.go would restate an
// interface this package does not own.
//
//forge:exclude-contract: llm.Driver implementation for the Reliant-managed gateway; its contract is llm.Driver
package reliant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
	accesstoken "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/reliant/internal/chatmarkers"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/cache"
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
	if accesstoken.HasFormat(trimmedKey) {
		return accesstoken.Prefix
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
	if accesstoken.HasFormat(trimmedKey) {
		return "reliant_access_token"
	}
	if strings.HasPrefix(trimmedKey, "sk-") {
		return "openai_compatible"
	}
	return "unknown"
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

	logging.Debug("Reliant driver client configured",
		"base_url", opts.BaseURL,
		"api_key_prefix", apiKeyPrefixForLog(opts.ApiKey),
		"api_key_type", apiKeyTypeForLog(opts.ApiKey),
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

// isClaudeModel reports whether this request will be routed to Claude by the
// gateway. LiteLLM maps our `claude-*` api_models onto `vertex_ai/claude-*`,
// which is the Anthropic dialect and the only route that reads cache_control.
//
// The gate is deliberately narrow. LiteLLM strips cache_control for real
// openai.com hosts, but its Gemini path is untraced (see
// research/PROMPT_CACHE_TTL_LITELLM.md §5) — a key that survives into a
// `contents` translation it has no field for is an unknown, and an unknown on
// every request is not worth a cache we cannot confirm.
func (c *ReliantClient) isClaudeModel() bool {
	return strings.HasPrefix(strings.ToLower(c.Options.Model.APIModel), "claude")
}

// claudeCacheControl is the single place a breakpoint's cache_control is built,
// so every breakpoint in one request carries the same TTL. Anthropic requires
// longer-TTL breakpoints to precede shorter ones; a uniform TTL can never
// violate that.
func claudeCacheControl() map[string]any {
	return map[string]any{"type": "ephemeral", "ttl": cache.ExtendedTTL}
}

// setCacheControl marks a param struct with a cache_control extra field.
// openai-go has no typed field for it — it is an Anthropic-dialect key LiteLLM
// reads out of the OpenAI-shaped body — so it rides as an extra field.
//
// Not to be confused with ChatCompletionContentPartTextParam's typed
// PromptCacheBreakpoint: that is OpenAI's own prompt-cache scheme, which
// neither LiteLLM nor Anthropic reads.
func setCacheControl(target interface{ SetExtraFields(map[string]any) }) {
	target.SetExtraFields(map[string]any{"cache_control": claudeCacheControl()})
}

// applyClaudeCacheBreakpoints adds Anthropic prompt-cache breakpoints to an
// already-converted Chat Completions request, in place.
//
// Without this the managed gateway got NO caching at all for Claude: every turn
// rebilled the whole prompt at base input price, while the direct Anthropic
// driver paid 0.1x for the same prefix. The strategy mirrors
// internal/llm/drivers/anthropic/base.go so a model behaves the same whichever
// route reaches it — at most 4 breakpoints: the last tool definition, the last
// two system prompts, and the last message.
//
// Placements are the ones verified in LiteLLM's source
// (research/PROMPT_CACHE_TTL_LITELLM.md §1); they differ by role, so each
// case below is a distinct wire shape rather than one generic rule.
func (c *ReliantClient) applyClaudeCacheBreakpoints(messages []openai.ChatCompletionMessageParamUnion, toolParams []openai.ChatCompletionToolUnionParam) {
	if c.Options.DisableCache || !c.isClaudeModel() {
		return
	}

	// Last tool definition, at the TOP level of the tool object. LiteLLM also
	// accepts it inside "function", but top level takes precedence there and is
	// the shape its own transformation reads first.
	if last := len(toolParams) - 1; last >= 0 {
		if fn := toolParams[last].OfFunction; fn != nil &&
			cache.ShouldCacheTool(last, len(toolParams), 0, 0, false) {
			setCacheControl(fn)
		}
	}

	// Last two system prompts, as a cache_control on a text content PART.
	// ConvertMessages emits them with string content, so they are rewritten to
	// single-part array content here — LiteLLM's system translation only looks
	// for the key inside a content part.
	var systemIndices []int
	for i := range messages {
		if messages[i].OfSystem != nil {
			systemIndices = append(systemIndices, i)
		}
	}
	for position, index := range systemIndices {
		if !cache.ShouldCacheSystemPrompt(position, len(systemIndices), false) {
			continue
		}
		system := messages[index].OfSystem
		parts := system.Content.OfArrayOfContentParts
		if len(parts) == 0 {
			text := system.Content.OfString.Or("")
			if strings.TrimSpace(text) == "" {
				// An empty text block with cache_control is an Anthropic 400.
				continue
			}
			parts = []openai.ChatCompletionContentPartTextParam{{Text: text}}
			system.Content.OfString = param.Opt[string]{}
		}
		setCacheControl(&parts[len(parts)-1])
		system.Content.OfArrayOfContentParts = parts
	}

	if len(messages) == 0 {
		return
	}
	last := &messages[len(messages)-1]
	switch {
	case last.OfTool != nil:
		// Tool results take a MESSAGE-LEVEL key, which LiteLLM moves onto the
		// tool_result block. String content is fine, so the message is left as
		// ConvertMessages built it.
		setCacheControl(last.OfTool)

	case last.OfUser != nil:
		// Including history System messages: ConvertMessages emits those as a
		// user turn wrapped in <system>, so they arrive here as OfUser.
		parts := last.OfUser.Content.OfArrayOfContentParts
		if len(parts) == 0 {
			text := last.OfUser.Content.OfString.Or("")
			if strings.TrimSpace(text) == "" {
				return
			}
			part := openai.ChatCompletionContentPartTextParam{Text: text}
			setCacheControl(&part)
			last.OfUser.Content.OfString = param.Opt[string]{}
			last.OfUser.Content.OfArrayOfContentParts = []openai.ChatCompletionContentPartUnionParam{{OfText: &part}}
			return
		}
		// Mark the last TEXT part. When an attachment makes the final part an
		// image, the breakpoint moves back to the last text part rather than
		// onto the image: LiteLLM's add_cache_control_to_content only copies the
		// key from text parts, so marking an image part would silently drop the
		// breakpoint and we would pay for a cache that was never written. A
		// message with no text part at all gets none.
		for i := len(parts) - 1; i >= 0; i-- {
			if parts[i].OfText != nil {
				setCacheControl(parts[i].OfText)
				return
			}
		}

	// An assistant-last message is skipped. LiteLLM reads a message-level
	// cache_control for tool results only, and nothing in its source copies the
	// key off an assistant message — so a breakpoint there would be dropped
	// silently. It is also rare: a request is built to be answered, so the last
	// message is a user turn or a tool result almost every time.
	default:
	}
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

func isClaudeAPIModel(apiModel string) bool {
	return strings.HasPrefix(apiModel, "claude-")
}

// wireReasoningEffort returns the reasoning_effort LiteLLM should receive, or
// "" to omit it. LiteLLM translates it per upstream: Claude adaptive models get
// thinking{adaptive}+output_config.effort (low..max), older Claude gets a
// budget_tokens ladder, Gemini 3+ gets thinkingLevel. Gemini only accepts
// low/medium/high, as do budget-mode Claude models (xhigh 400s there), so
// levels above high clamp to high unless the model is adaptive.
func (c *ReliantClient) wireReasoningEffort() string {
	if !c.Options.Model.CanReason {
		return ""
	}
	effort := strings.ToLower(strings.TrimSpace(c.Options.ReasoningEffort))
	switch effort {
	case "", "disabled", "none", "off":
		return ""
	}
	if c.Options.Model.ThinkingMode != "adaptive" && (effort == "xhigh" || effort == "max") {
		return "high"
	}
	return effort
}

func (c *ReliantClient) preparedParams(messages []openai.ChatCompletionMessageParamUnion, toolParams []openai.ChatCompletionToolUnionParam) openai.ChatCompletionNewParams {
	// Marked here rather than inside ConvertMessages/ConvertTools so the
	// breakpoints are decided once, with the whole request in view: the
	// allocation depends on the system-prompt, message and tool counts
	// together, and the last message's role decides its wire shape.
	c.applyClaudeCacheBreakpoints(messages, toolParams)

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

	effort := c.wireReasoningEffort()
	if effort != "" {
		params.ReasoningEffort = shared.ReasoningEffort(effort)
	}

	// Claude rejects a non-default temperature while thinking is engaged.
	thinkingOnClaude := effort != "" && isClaudeAPIModel(c.Options.Model.APIModel)
	if c.Options.Temperature != nil && !thinkingOnClaude {
		params.Temperature = openai.Float(*c.Options.Temperature)
	}

	// LiteLLM handles max_completion_tokens upstream, so we always use
	// MaxTokens for simplicity.
	params.MaxTokens = openai.Int(c.Options.MaxTokens)

	return params
}

func (c *ReliantClient) SendMessages(ctx context.Context, prompts []string, messages []message.Message, toolList []tools.Tool) (response *llm.DriverResponse, err error) {
	params := c.preparedParams(c.ConvertMessages(prompts, messages), c.ConvertTools(toolList))

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
			Content:   content,
			ToolCalls: toolCalls,
			Usage: c.usage(*openaiResponse,
				gatewayReportedCost(rawResp, openaiResponse.Usage),
				cacheCreationInputTokens(openaiResponse.Usage)),
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
			// The accumulator sums the usage fields it knows about, which drops
			// LiteLLM's `cost` and `cache_creation_input_tokens` — both live in
			// the chunk's extra fields, so they have to be taken from the usage
			// chunk itself as it goes past.
			reportedCost := 0.0
			reportedCacheCreation := int64(0)

			for openaiStream.Next() {
				chunk := openaiStream.Current()
				acc.AddChunk(chunk)

				if cost := usageCost(chunk.Usage); cost > 0 {
					reportedCost = cost
				}
				if written := cacheCreationInputTokens(chunk.Usage); written > 0 {
					reportedCacheCreation = written
				}

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
						Usage:              c.usage(acc.ChatCompletion, reportedCost, reportedCacheCreation),
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
	// credit-exhaustion signal (see control-plane/internal/llmproxy). Retrying
	// is futile — the caller's wallet is empty and only adding credit clears
	// it. Surface as a terminal error so the workflow stops retrying and the
	// frontend can open the upgrade-required modal.
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
// returned when the reliant-managed (LiteLLM virtual key) credit is exhausted.
// The marker survives Temporal's JSON stringification of activity
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
// reliant-managed LLM (LiteLLM virtual key) credit is exhausted.
// Only the reliant driver emits this — user-provided keys to
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
	// (e.g. "You're out of Reliant credit. Add credit to your account to
	// continue.").
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
// from LiteLLM (proxied by control-plane/internal/llmproxy) signals that the
// caller's Reliant credit is exhausted. The proxy emits:
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
		// The id is ours (see llm.NewToolCallID): the gateway relays the
		// upstream provider's, which may repeat across responses. The one part
		// of it the next request needs is a Gemini thought signature LiteLLM
		// appends to it, and that is carried over onto ours. Streamed deltas
		// were accumulated by index, so nothing else needs the gateway's id.
		for _, call := range completion.Choices[0].Message.ToolCalls {
			if call.Function.Name == "" {
				logging.Warn("Skipping tool call with no tool name",
					"id", call.ID)
				continue
			}

			toolCall := message.ToolCall{
				ID:       llm.NewToolCallIDKeepingThoughtSignature(call.ID),
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

// usage converts the completion's token counts, pairing them with the cost and
// cache-write count the gateway reported for this request. Cost is passthrough:
// reliant keeps no per-token price table, so a request the gateway did not
// price costs 0 rather than an estimate.
//
// cacheCreation is a parameter rather than read from completion for the same
// reason cost is: it lives in the usage object's extra fields, and the stream
// accumulator does not carry those forward, so a stream has to capture it off
// the usage chunk as it goes past.
func (c *ReliantClient) usage(completion openai.ChatCompletion, cost float64, cacheCreation int64) llm.TokenUsage {
	cachedInputTokens := completion.Usage.PromptTokensDetails.CachedTokens
	inputTokens := completion.Usage.PromptTokens - cachedInputTokens
	if inputTokens < 0 {
		inputTokens = completion.Usage.PromptTokens
		cachedInputTokens = 0
	}
	if cost < 0 {
		cost = 0
	}
	if cacheCreation < 0 {
		cacheCreation = 0
	}
	return llm.TokenUsage{
		TokenCount:        completion.Usage.TotalTokens,
		InputTokens:       inputTokens,
		OutputTokens:      completion.Usage.CompletionTokens,
		CachedInputTokens: cachedInputTokens,
		Cost:              cost,
		// Carry the cache split through so a gateway turn can be attributed
		// the same way a direct-Anthropic one can. Both numbers were available
		// and dropped, which is why `[CallLLM] Usage` showed nothing for
		// managed traffic even on a full cache hit.
		CacheReadInputTokens:     cachedInputTokens,
		CacheCreationInputTokens: cacheCreation,
	}
}

// cacheCreationInputTokens reads the `cache_creation_input_tokens` field
// LiteLLM adds to the usage object from Anthropic's own usage. It is not part of
// the OpenAI schema, so the SDK has no field for it and it survives only in the
// decoded extra fields — the same path as `cost`.
//
// Its counterpart, cache READS, does have a home in the OpenAI schema
// (prompt_tokens_details.cached_tokens), so it is read there rather than here.
func cacheCreationInputTokens(usage openai.CompletionUsage) int64 {
	field, ok := usage.JSON.ExtraFields["cache_creation_input_tokens"]
	if !ok {
		return 0
	}
	tokens, err := strconv.ParseInt(strings.TrimSpace(field.Raw()), 10, 64)
	if err != nil || tokens < 0 {
		return 0
	}
	return tokens
}

// gatewayReportedCost resolves LiteLLM's cost for one request, in the order it
// is actually available: the response header, then the `-original` header, then
// the usage object in the body.
//
// LiteLLM only sends `x-litellm-response-cost` on the /v1/chat/completions
// route; /v1/messages carries `-original` alone. `-original` is trusted only
// when positive, because LiteLLM writes a zero there for requests it did not
// price, and taking that as a known-zero would mask a real number in the body.
func gatewayReportedCost(resp *http.Response, usage openai.CompletionUsage) float64 {
	if resp != nil {
		for _, header := range []string{"x-litellm-response-cost", "x-litellm-response-cost-original"} {
			if cost := parseReportedCost(resp.Header.Get(header)); cost > 0 {
				return cost
			}
		}
	}
	return usageCost(usage)
}

// usageCost reads the `cost` field LiteLLM adds to the usage object. It is not
// part of the OpenAI schema, so the SDK has no field for it and it survives
// only in the decoded extra fields. Streams report cost nowhere else — there is
// no header on an SSE response — so this is the only path for them.
func usageCost(usage openai.CompletionUsage) float64 {
	field, ok := usage.JSON.ExtraFields["cost"]
	if !ok {
		return 0
	}
	return parseReportedCost(field.Raw())
}

func parseReportedCost(raw string) float64 {
	cost, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || cost < 0 {
		return 0
	}
	return cost
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
