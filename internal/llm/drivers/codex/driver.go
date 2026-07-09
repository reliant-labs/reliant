// Copyright (c) 2025 Reliant Labs
package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
	openaidriver "github.com/reliant-labs/reliant/internal/llm/drivers/openai"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

const (
	// CodexBaseURL is the base URL for the Codex API (SDK appends /responses)
	CodexBaseURL = "https://chatgpt.com/backend-api/codex"

	// CodexVersion is the version header to send.
	// Keep aligned with a current codex-tui (CLI) release; backend may gate on this.
	CodexVersion = "0.143.0"

	// CodexOriginator identifies the client to the Codex backend.
	CodexOriginator = "codex-tui"

	// CodexUserAgent is the user agent string, matching the codex-tui CLI identity.
	CodexUserAgent = "codex-tui/0.143.0 (Mac OS 14.3.0; arm64) Apple_Terminal/453 (codex-tui; 0.143.0)"

	// CodexBetaFeatures advertises the beta features the codex-tui client opts into.
	CodexBetaFeatures = "remote_compaction_v2"
)

// codexInstallationNamespace is a fixed namespace UUID used to derive a stable,
// per-user installation_id via UUIDv5 (SHA-1). It has no meaning beyond seeding a
// deterministic derivation; keep it constant so derived ids stay stable forever.
var codexInstallationNamespace = uuid.MustParse("6f0d3c2a-7b1e-4a5c-9e8d-3f2b1a0c4d5e")

// deriveInstallationID returns a stable installation_id for the given Reliant user.
// Real codex-tui persists installation_id per install; we instead derive it
// deterministically from the user id (UUIDv5) so it is stable across restarts and
// turns, unique per user, and requires NO storage. When userID is empty (dev / no
// authenticated user) we fall back to a random UUID so behavior is unchanged.
//
// keyID is a stable per-credential discriminator (the ChatGPT account id) so that
// two upstream accounts belonging to the same Reliant user derive DISTINCT
// installation ids — otherwise one "installation" would span multiple accounts,
// which providers that enforce one-account-per-device (e.g. Codex) may flag.
func deriveInstallationID(userID, keyID string) string {
	if userID == "" {
		return uuid.New().String()
	}
	return uuid.NewSHA1(codexInstallationNamespace, []byte(userID+":"+keyID+":codex-installation")).String()
}

// CodexClient implements the LLM driver interface for the Codex API
// using the official openai-go SDK with a custom base URL and OAuth token auth.
type CodexClient struct {
	options        llm.DriverOptions
	client         openai.Client
	accountID      string // Account ID extracted from JWT
	accessToken    string // OAuth access token
	sessionID      string
	installationID string // Stable per-process installation identifier (codex-tui telemetry)
}

// Name returns the name of the driver
func (c *CodexClient) Name() string {
	return "codex"
}

// NewClient creates a new Codex client backed by the openai-go SDK
func NewClient(opts llm.DriverOptions) (*CodexClient, error) {
	// Default reasoning effort
	if opts.ReasoningEffort == "" {
		opts.ReasoningEffort = "medium"
	}

	// Session-scoped ids (session-id / thread-id / x-codex-window-id) must be stable
	// across all turns of one conversation, matching real codex-tui. Source them from
	// the caller-provided per-conversation SessionID; only mint a fresh one when the
	// caller supplies none (preserving prior behavior).
	sessionID := ""
	if opts.SessionID != nil {
		sessionID = strings.TrimSpace(*opts.SessionID)
	}
	if sessionID == "" {
		sessionID = uuid.New().String()
	}

	client := &CodexClient{
		options:   opts,
		sessionID: sessionID,
		// installation_id is DERIVED (not persisted) from the Reliant user id so it is
		// stable per user across restarts/turns without any storage. See deriveInstallationID.
		installationID: deriveInstallationID(opts.UserID, opts.AccountUUID),
	}

	// Common SDK options for all auth paths.
	// These mirror the session-scoped headers the codex-tui CLI sends. Per-request
	// headers (x-client-request-id, x-codex-turn-metadata) are attached per call.
	//
	// HTTP-vs-WS note: the ground-truth capture is a WebSocket handshake. WS-only
	// headers (Upgrade/Connection/Sec-WebSocket-*, and openai-beta:
	// responses_websockets=...) are intentionally NOT sent over the HTTP transport.
	sdkOpts := []option.RequestOption{
		option.WithBaseURL(CodexBaseURL),
		option.WithHeader("version", CodexVersion),
		option.WithHeader("originator", CodexOriginator),
		option.WithHeader("user-agent", CodexUserAgent),
		option.WithHeader("x-codex-beta-features", CodexBetaFeatures),
		option.WithHeader("session-id", sessionID),
		option.WithHeader("thread-id", sessionID),
		option.WithHeader("x-codex-window-id", sessionID+":0"),
	}

	bearerToken := strings.TrimSpace(opts.BearerToken)
	if bearerToken == "" {
		// Resolver currently passes driver credentials via ApiKey.
		// For Codex this credential is the OAuth access token.
		bearerToken = strings.TrimSpace(opts.ApiKey)
	}

	if bearerToken == "" {
		return nil, fmt.Errorf("codex authentication required: connect Codex from Settings")
	}

	// Static token path (runtime OAuth token injection)
	client.accessToken = bearerToken
	var err error
	client.accountID, err = extractAccountIDFromJWT(bearerToken)
	if err != nil {
		return nil, fmt.Errorf("failed to extract account ID from provided token: %w", err)
	}

	sdkOpts = append(sdkOpts,
		option.WithAPIKey(bearerToken),
		option.WithHeader("chatgpt-account-id", client.accountID),
	)

	client.client = openai.NewClient(sdkOpts...)

	return client, nil
}

// extractAccountIDFromJWT extracts the chatgpt_account_id from a JWT token
func extractAccountIDFromJWT(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid JWT format: expected 3 parts, got %d", len(parts))
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	var claims CodexJWTClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("failed to parse JWT claims: %w", err)
	}

	if claims.Auth == nil || claims.Auth.ChatGPTAccountID == "" {
		return "", fmt.Errorf("JWT does not contain chatgpt_account_id")
	}

	return claims.Auth.ChatGPTAccountID, nil
}

// convertInstructions concatenates prompts into a single instructions string.
// It also appends the shared OpenAI-family guidance for GPT-5/Codex-style models.
func (c *CodexClient) convertInstructions(prompts []string) string {
	var sb strings.Builder
	for _, p := range openaidriver.AppendOpenAIFamilyGuidance(prompts, c.options.Model) {
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

// isImageMimeType checks if a MIME type represents an image.
// Keep this in parity with openai driver behavior.
func isImageMimeType(mimeType string) bool {
	switch mimeType {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "image/bmp", "image/svg+xml":
		return true
	default:
		return false
	}
}

// extractFilenameFromPath extracts the filename from a file path.
func extractFilenameFromPath(path string) string {
	parts := []rune(path)
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == '/' || parts[i] == '\\' {
			return string(parts[i+1:])
		}
	}
	return path
}

// convertMessages converts internal messages to SDK Responses input format
func (c *CodexClient) convertMessages(messages []message.Message) responses.ResponseInputParam {
	items := make([]responses.ResponseInputItemUnionParam, 0, len(messages)*2)

	truncate64 := func(s string) string {
		if len(s) > 64 {
			return s[:64]
		}
		return s
	}

	for _, msg := range messages {
		switch msg.Role {
		case message.User:
			binaryContents := msg.BinaryContent()
			textContents := msg.TextContents()
			combinedText := make([]string, 0, len(textContents))
			for _, tc := range textContents {
				trimmed := strings.TrimSpace(tc.Text)
				if trimmed != "" {
					combinedText = append(combinedText, trimmed)
				}
			}
			userText := strings.Join(combinedText, "\n\n")
			if len(binaryContents) == 0 {
				if userText == "" {
					continue
				}
				items = append(items, responses.ResponseInputItemParamOfMessage(
					userText,
					responses.EasyInputMessageRoleUser,
				))
				break
			}

			content := make(responses.ResponseInputMessageContentListParam, 0, len(binaryContents)+len(combinedText))
			for _, text := range combinedText {
				content = append(content, responses.ResponseInputContentParamOfInputText(text))
			}
			for _, binaryContent := range binaryContents {
				if isImageMimeType(binaryContent.MIMEType) {
					image := responses.ResponseInputImageParam{
						Detail:   responses.ResponseInputImageDetailAuto,
						ImageURL: param.NewOpt(binaryContent.String("openai")),
					}
					content = append(content, responses.ResponseInputContentUnionParam{OfInputImage: &image})
				} else {
					fileData := "data:" + binaryContent.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(binaryContent.Data)
					filename := extractFilenameFromPath(binaryContent.Path)
					if filename == "" {
						filename = "file"
					}
					filePart := responses.ResponseInputFileParam{
						FileData: param.NewOpt(fileData),
						Filename: param.NewOpt(filename),
					}
					content = append(content, responses.ResponseInputContentUnionParam{OfInputFile: &filePart})
				}
			}

			items = append(items, responses.ResponseInputItemParamOfMessage(content, responses.EasyInputMessageRoleUser))

		case message.Assistant:
			// Assistant text as a message input item
			assistantText := strings.TrimSpace(msg.Content().String())
			if assistantText != "" {
				items = append(items, responses.ResponseInputItemParamOfMessage(
					assistantText,
					responses.EasyInputMessageRoleUser, // SDK uses "user" role for assistant history
				))
			}

			// Tool calls as separate function_call items
			for _, tc := range msg.ToolCalls() {
				items = append(items, responses.ResponseInputItemParamOfFunctionCall(
					tc.Input, tc.ID, truncate64(tc.Name),
				))
			}

		case message.Tool:
			for _, result := range msg.ToolResults() {
				items = append(items, responses.ResponseInputItemParamOfFunctionCallOutput(
					result.ToolCallID, result.Content,
				))
			}

		case message.System:
			systemText := strings.TrimSpace(msg.Content().String())
			if systemText != "" {
				items = append(items, responses.ResponseInputItemParamOfMessage(
					systemText,
					responses.EasyInputMessageRoleDeveloper,
				))
			}
		}
	}

	return responses.ResponseInputParam(items)
}

func validateCodexToolName(toolName string) error {
	if len(toolName) > 64 {
		return fmt.Errorf("invalid tool name %q: exceeds codex max length of 64", toolName)
	}
	if strings.Contains(toolName, "::") || strings.Contains(toolName, "/") {
		return fmt.Errorf("invalid tool name %q: contains forbidden scoped delimiters ('::' or '/')", toolName)
	}
	return nil
}

func pruneDuplicateCodexToolNames(toolList []tools.Tool) []tools.Tool {
	if len(toolList) <= 1 {
		return toolList
	}

	type namedTool struct {
		name  string
		index int
		tool  tools.Tool
	}

	named := make([]namedTool, 0, len(toolList))
	for idx, tool := range toolList {
		named = append(named, namedTool{name: tool.Name(), index: idx, tool: tool})
	}

	sort.Slice(named, func(i, j int) bool {
		if named[i].name == named[j].name {
			return named[i].index < named[j].index
		}
		return named[i].name < named[j].name
	})

	pruned := make([]tools.Tool, 0, len(named))
	logged := make(map[string]bool)
	seen := make(map[string]struct{})
	for _, entry := range named {
		if _, exists := seen[entry.name]; exists {
			if !logged[entry.name] {
				logging.Warn("[Codex] Pruned duplicate tool name deterministically",
					"toolName", entry.name,
					"strategy", "lexicographic-name-then-first-index")
				logged[entry.name] = true
			}
			continue
		}
		seen[entry.name] = struct{}{}
		pruned = append(pruned, entry.tool)
	}

	return pruned
}

func firstCodexResponseToolName(toolList []tools.Tool) (string, bool) {
	for _, tool := range toolList {
		if tools.IsResponseTool(tool) {
			return tool.Name(), true
		}
	}
	return "", false
}

// convertTools converts internal tools to SDK tool format
func (c *CodexClient) convertTools(toolList []tools.Tool) ([]responses.ToolUnionParam, error) {
	result := make([]responses.ToolUnionParam, 0, len(toolList))

	for idx, tool := range toolList {
		toolName := tool.Name()
		if err := validateCodexToolName(toolName); err != nil {
			return nil, fmt.Errorf("codex preflight rejected tool[%d]: %w", idx, err)
		}
		var params map[string]any
		// Prefer raw schema map when available (preserves nested structure)
		type schemaMapProvider interface{ ParamSchemaMap() map[string]any }
		if p, ok := tool.(schemaMapProvider); ok {
			params = p.ParamSchemaMap()
		} else {
			schema := tool.ParamSchema()
			b, _ := json.Marshal(schema)
			_ = json.Unmarshal(b, &params) //nolint:errcheck
		}
		if params == nil {
			params = map[string]any{}
		}
		params["type"] = "object"
		openaidriver.NormalizeResponsesToolSchema(params)
		params["additionalProperties"] = false
		if params["properties"] == nil {
			params["properties"] = map[string]any{}
		}
		if params["required"] == nil {
			params["required"] = []any{}
		}

		d := tool.Description()
		ft := &responses.FunctionToolParam{
			Name:        toolName,
			Parameters:  params,
			Strict:      openai.Bool(false), // Codex API may not fully support strict mode
			Description: openai.String(d),
		}
		if err := openaidriver.ValidateResponsesToolSchemaStrict(params); err != nil {
			logging.Warn("[Codex] Invalid strict tool schema after normalization; applying safe fallback",
				"tool", toolName,
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
		if d == "" {
			ft.Description = openai.String("")
		}

		result = append(result, responses.ToolUnionParam{OfFunction: ft})
	}

	return result, nil
}

// turnContext holds the per-request identifiers the codex-tui client sends as
// headers and echoes into the request body's client_metadata.
type turnContext struct {
	requestID        string // x-client-request-id
	turnID           string
	windowID         string
	turnMetadataJSON string // x-codex-turn-metadata (JSON string)
	startedAtMs      int64
}

// newTurnContext builds a fresh per-turn identity matching the shape codex-tui sends.
func (c *CodexClient) newTurnContext() turnContext {
	requestID := uuid.New().String()
	turnID := uuid.New().String()
	windowID := c.sessionID + ":0"
	startedAtMs := time.Now().UnixMilli()

	meta := map[string]any{
		"installation_id":         c.installationID,
		"session_id":              c.sessionID,
		"thread_id":               c.sessionID,
		"turn_id":                 turnID,
		"window_id":               windowID,
		"request_kind":            "turn",
		"thread_source":           "user",
		"sandbox":                 "seatbelt",
		"turn_started_at_unix_ms": startedAtMs,
	}
	metaJSON, _ := json.Marshal(meta) //nolint:errcheck

	return turnContext{
		requestID:        requestID,
		turnID:           turnID,
		windowID:         windowID,
		turnMetadataJSON: string(metaJSON),
		startedAtMs:      startedAtMs,
	}
}

// requestOptions returns the per-request SDK options (correlation header capture
// plus the per-turn codex-tui headers) for a given turn.
func (c *CodexClient) requestOptions(tc turnContext, into **http.Response) []option.RequestOption {
	return []option.RequestOption{
		option.WithResponseInto(into),
		option.WithHeader("x-client-request-id", tc.requestID),
		option.WithHeader("x-codex-turn-metadata", tc.turnMetadataJSON),
	}
}

// applyClientMetadata attaches the Codex backend's client_metadata extension to the
// request body. It is not a typed OpenAI Responses field, so it goes through
// SetExtraFields. Values mirror what codex-tui echoes for a turn.
func (c *CodexClient) applyClientMetadata(params *responses.ResponseNewParams, tc turnContext) {
	params.SetExtraFields(map[string]any{
		"client_metadata": map[string]any{
			"x-codex-window-id":                  tc.windowID,
			"turn_id":                            tc.turnID,
			"x-codex-ws-stream-request-start-ms": strconv.FormatInt(tc.startedAtMs, 10),
			"x-codex-installation-id":            c.installationID,
			"session_id":                         c.sessionID,
			"thread_id":                          c.sessionID,
			"x-codex-turn-metadata":              tc.turnMetadataJSON,
		},
	})
}

// buildParams constructs the SDK request params for both streaming and non-streaming paths
func (c *CodexClient) buildParams(prompts []string, messages []message.Message, toolList []tools.Tool) (responses.ResponseNewParams, error) {
	inputItems := c.convertMessages(messages)
	if len(inputItems) == 0 {
		return responses.ResponseNewParams{}, drivererrors.NewEmptyInputError("codex", len(messages), len(prompts), len(toolList))
	}

	// GPT-5.4 is exposed through Codex driver support matrix for future compatibility,
	// but gpt-5.4-pro is explicitly OpenAI-only per model contract.
	if c.options.Model.ID == models.GPT54Pro {
		return responses.ResponseNewParams{}, fmt.Errorf("model %s is openai-only and is not supported by codex driver", models.GPT54Pro)
	}

	params := responses.ResponseNewParams{
		Model:             shared.ResponsesModel(c.options.Model.APIModel),
		Input:             responses.ResponseNewParamsInputUnion{OfInputItemList: inputItems},
		ParallelToolCalls: openai.Bool(true),
		Store:             openai.Bool(false),
		ToolChoice:        responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: openai.Opt(responses.ToolChoiceOptionsAuto)},
		// codex-tui pins verbosity low; this matches the capture and suits terse
		// agentic tool-use loops. There is no per-request verbosity option to plumb.
		Text: responses.ResponseTextConfigParam{Verbosity: responses.ResponseTextConfigVerbosityLow},
		// Prompt cache key mirrors codex-tui, which keys on the session/thread id.
		PromptCacheKey: openai.String(c.sessionID),
	}

	// Set instructions from prompts
	if inst := c.convertInstructions(prompts); inst != "" {
		params.Instructions = openai.String(inst)
	}

	// Add tools
	if len(toolList) > 0 {
		validatedTools := pruneDuplicateCodexToolNames(toolList)
		convertedTools, err := c.convertTools(validatedTools)
		if err != nil {
			return responses.ResponseNewParams{}, err
		}
		params.Tools = convertedTools
		if responseToolName, ok := firstCodexResponseToolName(validatedTools); ok {
			params.ToolChoice = responses.ResponseNewParamsToolChoiceUnion{
				OfFunctionTool: &responses.ToolChoiceFunctionParam{Name: responseToolName},
			}
		}
	}

	// Temperature
	if c.options.Temperature != nil {
		params.Temperature = openai.Float(*c.options.Temperature)
	}

	// NOTE: max_output_tokens is NOT supported by the Codex API (chatgpt.com backend).
	// Omitting intentionally -- unlike the official OpenAI API.

	// Reasoning configuration
	if c.options.Model.CanReason && c.options.ReasoningEffort != "disabled" && c.options.ReasoningEffort != "none" {
		effort := c.options.ReasoningEffort
		if effort == "" {
			effort = "medium"
		}
		reasoningEffort := shared.ReasoningEffortMedium
		switch effort {
		case "low":
			reasoningEffort = shared.ReasoningEffortLow
		case "medium":
			reasoningEffort = shared.ReasoningEffortMedium
		case "high":
			reasoningEffort = shared.ReasoningEffortHigh
		case "xhigh":
			reasoningEffort = shared.ReasoningEffort("xhigh")
		}

		// Use model-specific reasoning summary mode.
		// GPT-5.3-Codex-Spark does not support reasoning summaries, so omit Summary entirely.
		if c.options.Model.ID == models.GPT53CodexSpark {
			params.Reasoning = shared.ReasoningParam{
				Effort: reasoningEffort,
			}
		} else {
			summaryMode := shared.ReasoningSummaryConcise
			if c.options.Model.ReasoningSummaryMode == models.ReasoningSummaryDetailedOnly {
				summaryMode = shared.ReasoningSummaryDetailed
			}
			params.Reasoning = shared.ReasoningParam{
				Effort:  reasoningEffort,
				Summary: summaryMode,
			}
			params.Include = []responses.ResponseIncludable{
				responses.ResponseIncludableReasoningEncryptedContent,
			}
		}
	}

	return params, nil
}

// SendMessages sends messages to the Codex API and returns a complete response
func (c *CodexClient) SendMessages(ctx context.Context, prompts []string, messages []message.Message, toolList []tools.Tool) (*llm.DriverResponse, error) {
	tc := c.newTurnContext()
	params, err := c.buildParams(prompts, messages, toolList)
	if err != nil {
		return nil, fmt.Errorf("invalid codex request: %w", err)
	}
	c.applyClientMetadata(&params, tc)

	var rawResp *http.Response
	resp, err := c.client.Responses.New(ctx, params, c.requestOptions(tc, &rawResp)...)
	if err != nil {
		return nil, fmt.Errorf("codex API request failed: %w", AugmentAPIError(err))
	}
	if resp.Error.Message != "" {
		return nil, fmt.Errorf("codex API error: %s", resp.Error.Message)
	}

	// Extract text content
	content := resp.OutputText()

	// Extract tool calls from response output items
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

	// Determine finish reason
	finishReason := c.resolveFinishReason(resp, len(toolCalls))

	usage := llm.TokenUsage{}
	if resp.Usage.TotalTokens > 0 {
		usage.TokenCount = resp.Usage.TotalTokens
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

// StreamResponse sends messages to the Codex API and returns a stream of events
func (c *CodexClient) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, toolList []tools.Tool) <-chan llm.DriverEvent {
	eventChan := make(chan llm.DriverEvent)

	go func() {
		defer close(eventChan)

		tc := c.newTurnContext()
		params, err := c.buildParams(prompts, messages, toolList)
		if err != nil {
			logging.Error("[Codex] Request preflight failed",
				"error", err,
				"messageCount", len(messages),
				"promptCount", len(prompts),
				"toolCount", len(toolList))
			eventChan <- llm.DriverEvent{Type: llm.EventError, Error: err}
			return
		}
		c.applyClientMetadata(&params, tc)
		var streamResp *http.Response
		stream := c.client.Responses.NewStreaming(ctx, params, c.requestOptions(tc, &streamResp)...)

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
				// Detect reasoning item start
				if reasoning := v.Item.AsReasoning(); reasoning.Type == "reasoning" {
					eventChan <- llm.DriverEvent{Type: llm.EventThinkingStart}
				}

			case responses.ResponseOutputItemDoneEvent:
				// Detect reasoning item completion
				if reasoning := v.Item.AsReasoning(); reasoning.Type == "reasoning" {
					eventChan <- llm.DriverEvent{Type: llm.EventThinkingStop}
				}

				// Handle function call completion
				if fc := v.Item.AsFunctionCall(); fc.Type == "function_call" && fc.CallID != "" {
					itemID := fc.ID
					if itemID == "" {
						itemID = fc.CallID
					}
					tc := toolCallsByID[itemID]
					if tc == nil {
						tc = &message.ToolCall{Type: "function"}
						toolCallsByID[itemID] = tc
					}
					tc.ID = fc.CallID
					tc.Name = fc.Name
					tc.Input = fc.Arguments
					tc.Finished = true
					eventChan <- llm.DriverEvent{Type: llm.EventToolUseStop, ToolCall: tc}
				}

			case responses.ResponseReasoningSummaryPartAddedEvent:
				if v.Part.Text != "" {
					eventChan <- llm.DriverEvent{Type: llm.EventThinkingDelta, Content: v.Part.Text}
				}

			case responses.ResponseReasoningSummaryPartDoneEvent:
				// Reasoning summary part complete — no action needed

			case responses.ResponseFunctionCallArgumentsDeltaEvent:
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
				eventChan <- llm.DriverEvent{
					Type:     llm.EventToolUseDelta,
					ToolCall: &message.ToolCall{ID: itemID, Input: v.Delta},
				}

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
				tc.Name = v.Name
				tc.Input = v.Arguments

			case responses.ResponseCompletedEvent:
				finalResp = &v.Response

			default:
				// Unhandled event type — ignore
			}
		}

		if err := stream.Err(); err != nil {
			err = AugmentAPIError(err)
			logging.Error("[Codex] Stream error", "error", err)
			eventChan <- llm.DriverEvent{Type: llm.EventError, Error: err}
			return
		}

		// Reconcile tool calls from the final response
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
					tc.ID = fc.CallID
					tc.Name = fc.Name
					if tc.Input == "" {
						tc.Input = fc.Arguments
					}
				}
			}
		}

		// Build final tool calls list
		finalToolCalls := make([]message.ToolCall, 0, len(toolCallsByID))
		for _, tc := range toolCallsByID {
			if tc.ID == "" || tc.Name == "" {
				continue
			}
			tc.Finished = true
			finalToolCalls = append(finalToolCalls, *tc)
			eventChan <- llm.DriverEvent{Type: llm.EventToolUseStop, ToolCall: tc}
		}

		// Determine finish reason
		finishReason := message.FinishReasonEndTurn
		if finalResp != nil {
			finishReason = c.resolveFinishReason(finalResp, len(finalToolCalls))
		} else if len(finalToolCalls) > 0 {
			finishReason = message.FinishReasonToolUse
		}

		// Token usage
		usage := llm.TokenUsage{}
		if finalResp != nil && finalResp.Usage.TotalTokens > 0 {
			usage.TokenCount = finalResp.Usage.TotalTokens
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

// resolveFinishReason determines the finish reason from the response status
func extractUpstreamCorrelationHeaders(resp *http.Response) (requestID string, proxymanID string) {
	if resp == nil {
		return "", ""
	}
	return strings.TrimSpace(resp.Header.Get("x-oai-request-id")), strings.TrimSpace(resp.Header.Get("x-proxyman-id"))
}

func (c *CodexClient) resolveFinishReason(resp *responses.Response, toolCallCount int) message.FinishReason {
	finishReason := message.FinishReasonEndTurn

	switch resp.Status {
	case responses.ResponseStatusIncomplete:
		finishReason = message.FinishReasonMaxTokens
		reason := resp.IncompleteDetails.Reason
		if reason == "" {
			reason = "unknown"
		}
		logging.Warn("[Codex] Response incomplete",
			"status", resp.Status,
			"reason", reason,
			"responseID", resp.ID)

	case responses.ResponseStatusFailed:
		finishReason = message.FinishReasonError
		logging.Error("[Codex] Response failed",
			"status", resp.Status,
			"error", resp.Error.Message,
			"code", resp.Error.Code,
			"responseID", resp.ID)

	case responses.ResponseStatusCancelled:
		finishReason = message.FinishReasonCancelled
		logging.Warn("[Codex] Response cancelled",
			"responseID", resp.ID)
	}

	// Tool use takes precedence (unless response was incomplete/failed/cancelled)
	if toolCallCount > 0 && finishReason == message.FinishReasonEndTurn {
		finishReason = message.FinishReasonToolUse
	}

	return finishReason
}

// Model returns the model configuration for this driver
func (c *CodexClient) Model() models.Model {
	return c.options.Model
}

// ValidateKey validates the OAuth access token for this driver.
func (c *CodexClient) ValidateKey(ctx context.Context) error {
	if c.accessToken == "" {
		return fmt.Errorf("no access token available")
	}
	_, err := extractAccountIDFromJWT(c.accessToken)
	if err != nil {
		return err
	}
	if IsTokenExpired(c.accessToken) {
		return fmt.Errorf("codex session expired, please reconnect Codex")
	}
	return nil
}
