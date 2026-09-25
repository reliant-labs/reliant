// Copyright (c) 2025 Reliant Labs
//
// This package implements the llm.Driver interface declared in
// internal/llm/types.go. Its behavioral contract already exists upstream, and
// the exported methods here are that interface's implementation plus
// provider-specific wire handling. A local contract.go would restate an
// interface this package does not own.
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: a provider driver IS an outbound adapter behind llm.Driver (the contract lives in internal/llm); a per-driver contract.go would duplicate it; tracked in H-RELIANT-CI-lint follow-ups
//forge:exclude-contract: llm.Driver implementation for Antigravity (Google cloudcode); its contract is llm.Driver
package antigravity

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/geminiwire"
	"github.com/reliant-labs/reliant/internal/llm/models"
	toolsPkg "github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Client talks to the Antigravity cloudcode endpoint.
//
// Transport is hand-rolled rather than going through google.golang.org/genai:
// see the package comment in envelope.go for why the SDK cannot decode this
// endpoint's frames.
type Client struct {
	options    llm.DriverOptions
	httpClient *http.Client
}

// ManagedSystemPrompt is the seam for the Antigravity managed system prompt.
//
// The prompt itself is owned by prompts.go — the embedded agyprompts/*.txt
// blocks (see prompts_embed.go) plus the per-request rendering of the dynamic
// sections. That file is expected to expose BuildSystemInstruction and to
// assign it here from its own init(), which keeps the driver ignorant of how
// the prompt is assembled and lets the two land independently.
//
// Until that assignment exists this returns "", so a request carries only the
// caller's own prompts: degraded, but not wrong, and never a half-built
// managed prompt.
//
// apiModel is the BASE model id (no effort suffix); workingDirectory is the
// project or worktree the agent is operating in. If BuildSystemInstruction
// needs more per-request context than these two (the capture's dynamic
// sections also carry a conversation id, a workspace list, and the available
// skills/subagents/slash commands), widen this signature rather than reaching
// into Client — the seam is meant to move.
var ManagedSystemPrompt = func(apiModel, workingDirectory string) string { return "" }

// NewClient builds an Antigravity client. opts.ApiKey is the Google OAuth
// access token (ya29…); token refresh is installed by the resolver via
// WithTokenRefresher and applied by the transport below.
func NewClient(opts llm.DriverOptions) (*Client, error) {
	if opts.Model.APIModel == "" {
		return nil, fmt.Errorf("antigravity: model has no api_model")
	}
	base := llm.StreamingHTTPClient()
	// NewTokenRefreshTransport (auth.go) owns the authorization header whenever
	// a refresher is configured, and returns its argument unchanged when one is
	// not — which is exactly when the identity transport has to supply the
	// header itself. Stamping it unconditionally would overwrite a freshly
	// rotated token with the stale one captured at construction.
	refreshing := NewTokenRefreshTransport(base.Transport, &opts)
	httpClient := &http.Client{
		Transport: &clientIdentityTransport{
			base:           refreshing,
			setAuthFromOpt: refreshing == base.Transport,
			opts:           &opts,
		},
		Timeout: base.Timeout,
	}
	return &Client{options: opts, httpClient: httpClient}, nil
}

// Name returns the driver name
func (c *Client) Name() string {
	return string(Family)
}

// Model returns the model configuration for this driver
func (c *Client) Model() models.Model {
	return c.options.Model
}

// requestModelID is the id that goes in the envelope's top-level "model"
// field: the catalog's api_model plus the effort suffix Antigravity uses to
// advertise reasoning variants.
func (c *Client) requestModelID() string {
	return modelIDForEffort(c.options.Model.APIModel, c.options.ReasoningEffort)
}

// convertMessages maps Reliant's domain messages onto Gemini contents.
// Ported from the gemini driver — the inner payload is standard Gemini — with
// one divergence: thought signatures stay base64 strings end to end instead of
// being decoded into []byte for the SDK's Part type.
func convertMessages(messages []message.Message) []*content {
	var history []*content

	for _, msg := range messages {
		switch msg.Role {
		case message.User:
			var parts []*part
			for _, text := range msg.TextContents() {
				if text.Text == "" {
					continue
				}
				parts = append(parts, &part{Text: text.Text})
			}
			for _, binary := range msg.BinaryContent() {
				parts = append(parts, &part{InlineData: &blob{
					MIMEType: binary.MIMEType,
					Data:     binary.Data,
				}})
			}
			if len(parts) > 0 {
				history = append(history, &content{Role: "user", Parts: parts})
			}

		case message.System:
			// System messages (e.g. compaction summaries) become user turns;
			// the real system instruction travels in its own field.
			if text := msg.Content().String(); text != "" {
				history = append(history, &content{
					Role:  "user",
					Parts: []*part{{Text: text}},
				})
			}

		case message.Assistant:
			var parts []*part
			// Reasoning goes FIRST, because it is what the rest of the turn
			// was derived from and the server reads the parts in order.
			//
			// Replaying it is not about showing the model its own thoughts: it
			// is the signature. Gemini 3.x signs a reasoning step and rejects
			// a later request that replays the step unsigned. In the failing
			// conversation the signature lived on the THINKING block (968
			// bytes) while the tool_call block had none, so dropping reasoning
			// here discarded the only copy and the next request 400'd.
			//
			// A signature with no readable text is still emitted — the store
			// deliberately keeps those rows (see db.IsBlockValid), and they
			// are exactly the ones that keep a thread moving.
			if reasoning := msg.ReasoningContent(); reasoning.Thinking != "" || reasoning.Signature != "" {
				parts = append(parts, &part{
					Text:             reasoning.Thinking,
					Thought:          true,
					ThoughtSignature: reasoning.Signature,
				})
			}
			if text := msg.Content().String(); text != "" {
				parts = append(parts, &part{Text: text})
			}
			for _, call := range msg.ToolCalls() {
				args, _ := parseJSONToMap(call.Input)
				parts = append(parts, &part{
					FunctionCall: &functionCall{
						Name: prefixedToolName(call.Name),
						Args: args,
					},
					// Echoed back verbatim: Gemini 3.x rejects a follow-up
					// turn whose prior function call lost its signature.
					ThoughtSignature: call.ThoughtSignature,
				})
			}
			if len(parts) > 0 {
				history = append(history, &content{Role: "model", Parts: parts})
			}

		case message.Tool:
			for _, result := range msg.ToolResults() {
				response := map[string]any{"result": result.Content}
				if parsed, err := parseJSONToMap(result.Content); err == nil {
					response = parsed
				}

				toolName, thoughtSignature := resolveToolCall(messages, result.ToolCallID, result.Name)
				if toolName == "" {
					logging.Warn("[ANTIGRAVITY] Tool result without tool name", "toolCallID", result.ToolCallID)
					continue
				}

				parts := []*part{{
					FunctionResponse: &functionResponse{
						Name:     prefixedToolName(toolName),
						Response: response,
					},
					ThoughtSignature: thoughtSignature,
				}}
				for _, bp := range result.BinaryParts {
					parts = append(parts, &part{InlineData: &blob{
						MIMEType: bp.MIMEType,
						Data:     bp.Data,
					}})
				}
				// Gemini requires tool results on the "user" turn.
				history = append(history, &content{Role: "user", Parts: parts})
			}
		}
	}

	return history
}

// resolveToolCall finds the tool name and thought signature for a tool result,
// preferring the name the result already carries.
func resolveToolCall(messages []message.Message, toolCallID, resultName string) (string, string) {
	name := resultName
	signature := ""
	for _, msg := range messages {
		if msg.Role != message.Assistant {
			continue
		}
		for _, call := range msg.ToolCalls() {
			if call.ID != toolCallID {
				continue
			}
			if name == "" {
				name = call.Name
			}
			signature = call.ThoughtSignature
			return name, signature
		}
	}
	return name, signature
}

// convertMessagesWithValidation never returns an empty content list, which the
// API rejects, and never returns a content with zero usable parts.
func convertMessagesWithValidation(messages []message.Message) []*content {
	history := convertMessages(messages)

	if len(history) == 0 {
		logging.Warn("[ANTIGRAVITY] Message conversion produced zero messages, using fallback",
			"originalCount", len(messages))
		history = append(history, &content{
			Role:  "user",
			Parts: []*part{{Text: "[System: Message conversion failed. Please respond with a brief acknowledgment.]"}},
		})
	}

	for i, c := range history {
		if c == nil {
			continue
		}
		usable := false
		for _, p := range c.Parts {
			// A bare thought signature counts as usable content. It carries no
			// text, but it is what the server verifies to let the model resume
			// its own reasoning — appending an "[Empty message]" placeholder
			// beside it would inject fake user-visible text into a turn whose
			// only job is to carry the signature.
			if p != nil && (p.Text != "" || p.ThoughtSignature != "" ||
				p.FunctionCall != nil || p.FunctionResponse != nil || p.InlineData != nil) {
				usable = true
				break
			}
		}
		if !usable {
			logging.Warn("[ANTIGRAVITY] No usable parts in message, adding placeholder", "index", i, "role", c.Role)
			c.Parts = append(c.Parts, &part{Text: "[Empty message]"})
		}
	}

	return history
}

// systemInstruction assembles the managed Antigravity prompt followed by
// Reliant's own caller prompts, matching the capture's single-text-part shape
// on a "user"-role instruction.
func (c *Client) systemInstruction(prompts []string) *content {
	var blocks []string
	if managed := ManagedSystemPrompt(c.options.Model.APIModel, c.options.WorkingDirectory); strings.TrimSpace(managed) != "" {
		blocks = append(blocks, managed)
	}
	for _, prompt := range prompts {
		if strings.TrimSpace(prompt) == "" {
			continue
		}
		blocks = append(blocks, prompt)
	}
	if len(blocks) == 0 {
		return nil
	}
	return &content{
		Role:  "user",
		Parts: []*part{{Text: strings.Join(blocks, "\n\n")}},
	}
}

// buildEnvelope assembles the full double-wrapped request body.
func (c *Client) buildEnvelope(messages []message.Message, prompts []string, toolList []toolsPkg.Tool) *requestEnvelope {
	generation := &generationConfig{MaxOutputTokens: c.options.MaxTokens}
	if c.options.Temperature != nil {
		temp := *c.options.Temperature
		generation.Temperature = &temp
	}
	// Thinking budget and the effort suffix are SEPARATE controls that both
	// travel: the suffix picks the effort variant, the budget (-1) leaves the
	// amount unbounded, exactly as the capture does.
	if effort := strings.ToLower(c.options.ReasoningEffort); effort != "" && effort != "disabled" {
		generation.ThinkingConfig = &thinkingConfig{
			IncludeThoughts: true,
			ThinkingBudget:  unboundedThinkingBudget,
		}
	}

	return &requestEnvelope{
		Project:     envelopeProject,
		RequestID:   c.newRequestID(),
		Model:       c.requestModelID(),
		UserAgent:   envelopeUserAgent,
		RequestType: envelopeRequestType,
		Request: &generateReq{
			Contents:          convertMessagesWithValidation(messages),
			SystemInstruction: c.systemInstruction(prompts),
			Tools:             convertTools(toolList),
			ToolConfig:        buildToolConfig(c.options.ForceToolChoice, toolList),
			GenerationConfig:  generation,
			SessionID:         c.sessionID(),
		},
	}
}

// newRequestID mirrors the capture's structured id
// (agent/<conversationId>/<epochMs>/<trajectoryId>/<turn>). Whether the server
// parses it or treats it as opaque is unknown, so the shape is reproduced
// rather than replaced with a bare UUID.
func (c *Client) newRequestID() string {
	conversation := "00000000-0000-0000-0000-000000000000"
	if c.options.SessionID != nil && *c.options.SessionID != "" {
		conversation = *c.options.SessionID
	}
	return fmt.Sprintf("agent/%s/%d/%s/1", conversation, time.Now().UnixMilli(), uuid.New().String())
}

// sessionID is a STRING holding a signed 64-bit integer, per the capture.
// Derived from the caller's session id so every turn of one conversation
// reports the same value.
func (c *Client) sessionID() string {
	if c.options.SessionID == nil || *c.options.SessionID == "" {
		return strconv.FormatInt(rand.Int63(), 10)
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(*c.options.SessionID))
	return strconv.FormatInt(int64(h.Sum64()), 10)
}

// SendMessages returns a complete response.
//
// Antigravity exposes only :streamGenerateContent, so there is no separate
// unary call to make: this consumes the stream and aggregates it rather than
// inventing an endpoint the provider does not serve.
func (c *Client) SendMessages(ctx context.Context, prompts []string, messages []message.Message, toolList []toolsPkg.Tool) (*llm.DriverResponse, error) {
	var last *llm.DriverResponse
	for event := range c.StreamResponse(ctx, prompts, messages, toolList) {
		switch event.Type {
		case llm.EventError:
			return nil, event.Error
		case llm.EventComplete:
			last = event.Response
		}
	}
	if last == nil {
		return nil, fmt.Errorf("antigravity: stream ended without a complete event")
	}
	return last, nil
}

// ValidateKey checks the stored OAuth token by making one small request.
func (c *Client) ValidateKey(ctx context.Context) error {
	testMessages := []message.Message{{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "Say 'test' and nothing else"}},
	}}

	validationOpts := c.options
	validationOpts.MaxTokens = 100
	validationOpts.ReasoningEffort = "disabled"

	validationClient, err := NewClient(validationOpts)
	if err != nil {
		return err
	}
	_, err = validationClient.SendMessages(ctx, nil, testMessages, nil)
	return err
}

// usage maps Antigravity's usageMetadata onto Reliant's counters.
func usage(resp *generateResp) llm.TokenUsage {
	if resp == nil || resp.UsageMetadata == nil {
		return llm.TokenUsage{}
	}
	m := resp.UsageMetadata
	return llm.TokenUsage{
		TokenCount:           m.TotalTokenCount,
		InputTokens:          m.PromptTokenCount,
		OutputTokens:         m.CandidatesTokenCount,
		CachedInputTokens:    m.CachedContentTokenCount,
		CacheReadInputTokens: m.CachedContentTokenCount,
	}
}

// finishReason maps Gemini finish reasons onto Reliant's.
//
// Antigravity's OWN vocabulary gap from geminiwire.FinishReason: an empty
// reason means "no finishReason field on this frame", which happens on every
// non-terminal SSE frame in a stream, not just the last one — so here (and
// only here) empty maps to EndTurn rather than Unknown. gemini and vertexai
// never see an empty string: the genai SDK's Candidate.FinishReason is only
// populated on the terminal response in the first place.
func finishReason(reason string) message.FinishReason {
	if reason == "" {
		return message.FinishReasonEndTurn
	}
	return geminiwire.FinishReason(geminiwire.SourceAntigravity, reason)
}

func parseJSONToMap(raw string) (map[string]any, error) {
	var out map[string]any
	err := json.Unmarshal([]byte(raw), &out)
	return out, err
}

// isRetryableStatus reports whether an HTTP status is worth retrying: rate
// limits and transient gateway/server errors.
func isRetryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// retryDelay honors Retry-After when the server sends one, and otherwise backs
// off exponentially with jitter, matching the gemini driver's ladder.
func retryDelay(attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if after := resp.Header.Get("Retry-After"); after != "" {
			if seconds, err := strconv.Atoi(after); err == nil && seconds > 0 {
				return time.Duration(seconds) * time.Second
			}
		}
	}
	backoff := 2000 * (1 << (attempt - 1))
	jitter := int(float64(backoff) * 0.2)
	return time.Duration(backoff+jitter) * time.Millisecond
}

// clientIdentityTransport stamps the fixed Antigravity client identity onto
// every request, plus a Bearer token when no refreshing transport is installed.
//
// It sits OUTSIDE NewTokenRefreshTransport (auth.go's), which owns the
// authorization header whenever a refresher is configured. This one only fills
// in the header for a configuration that has no refresher — otherwise it would
// overwrite a freshly rotated token with the stale one captured at construction.
type clientIdentityTransport struct {
	base           http.RoundTripper
	setAuthFromOpt bool
	opts           *llm.DriverOptions
}

func (t *clientIdentityTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", userAgentHeader)
	req.Header.Set("Content-Type", "application/json")
	if t.setAuthFromOpt && t.opts.ApiKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.opts.ApiKey)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}
