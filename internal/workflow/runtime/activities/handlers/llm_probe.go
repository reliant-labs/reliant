// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/invopop/jsonschema"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// ProbeSpec is one live LLM request issued through the SAME request path
// workflow activities use (resolveLLMCall -> drivers.GetDriver ->
// prepareHistoryForLLM -> driver.StreamResponse). It exists so a probe
// exercises production, not a re-implementation of it.
type ProbeSpec struct {
	UserID string

	// Exactly one of ModelID (optionally "id@driver") or Tags selects the model.
	ModelID string
	Tags    []string
	// Providers pins the source of a local model ("endpoint:<id>" or
	// "local:<daemonID>").
	Providers []string

	// ThinkingLevel "" means "no explicit level" (registry/tag default applies).
	ThinkingLevel string
	// Temperature nil means unset; a pointer so 0 is a real value.
	Temperature *float64
	MaxTokens   *int64

	SystemPrompts []string
	History       []message.Message
	Tools         []tools.Tool

	// Local supplies the daemon directory and relay for a "<name>@local" model.
	Local *LocalModelSpec
}

// ProbeResult is everything a streamed turn produced, collected the way
// call_llm's stream state collects it.
type ProbeResult struct {
	// What the shared request path resolved.
	ResolvedModelID string
	ProviderDriver  string
	// ServedBy is the Name() of the driver that actually carried the request,
	// as opposed to ProviderDriver, the provider the registry picked. They can
	// disagree only through a bug in driver selection, and when they did (a
	// stale copilot allowlist silently re-routed nine "copilot" models) the
	// probe reported the registry's pick and every cell looked green.
	ServedBy           string
	APIModel           string
	EffectiveThinking  string
	EffectiveTemp      *float64
	ModelTemperatureOK bool // false when the model's temperature_mode is "omit"

	Text           string
	Thinking       string
	Signature      string
	RedactedThking string
	ToolCalls      []message.ToolCall
	Usage          llm.TokenUsage
	FinishReason   message.FinishReason
	Latency        time.Duration
	EventCount     int

	// ThinkingEvents counts thinking_delta events that carried text; it is how a
	// probe tells "driver streamed thinking" from "only the final response had it".
	ThinkingEvents int
	// EventTypes counts every driver event type seen, for diagnosing a stream.
	EventTypes map[string]int

	// ResolveErr is set when resolveLLMCall failed (catalog / credential / driver
	// construction); StreamErr when the stream itself reported an error.
	ResolveErr error
	StreamErr  error
}

// Err returns the first failure, resolution before stream.
func (r ProbeResult) Err() error {
	if r.ResolveErr != nil {
		return r.ResolveErr
	}
	return r.StreamErr
}

// ProbeLLMCall runs one request and collects the full streamed turn.
func ProbeLLMCall(ctx context.Context, spec ProbeSpec) ProbeResult {
	var out ProbeResult

	selector := models.ModelSelector{ID: spec.ModelID, Tags: spec.Tags, Providers: spec.Providers}
	resolved, err := resolveLLMCall(ctx, nil, llmCallSpec{
		UserID:        spec.UserID,
		SessionID:     "llm-probe",
		Selector:      selector,
		Temperature:   spec.Temperature,
		ThinkingLevel: spec.ThinkingLevel,
		MaxTokens:     spec.MaxTokens,
		Local:         spec.Local,
	})
	if err != nil {
		out.ResolveErr = err
		return out
	}
	out.ResolvedModelID = resolved.ModelID
	out.ProviderDriver = resolved.ProviderDriver
	if resolved.Driver != nil {
		out.ServedBy = resolved.Driver.Name()
	}
	out.EffectiveThinking = resolved.ThinkingLevel
	out.EffectiveTemp = resolved.Temperature
	out.ModelTemperatureOK = resolved.Model.TemperatureMode != models.TemperatureModeOmit
	if resolved.Definition != nil {
		for _, p := range resolved.Definition.Providers {
			if p.Driver == resolved.ProviderDriver {
				out.APIModel = p.APIModel
				break
			}
		}
	}

	history := prepareHistoryForLLM("llm-probe", append([]message.Message(nil), spec.History...),
		spec.SystemPrompts, spec.Tools, resolved.Model.ContextWindow)

	start := time.Now()
	events := resolved.Driver.StreamResponse(ctx, spec.SystemPrompts, history, spec.Tools)
	var streamed strings.Builder
	var thinking strings.Builder
	for ev := range events {
		out.EventCount++
		if out.EventTypes == nil {
			out.EventTypes = map[string]int{}
		}
		out.EventTypes[string(ev.Type)]++
		switch ev.Type {
		case llm.EventContentDelta:
			streamed.WriteString(ev.Content)
		case llm.EventThinkingDelta:
			thinking.WriteString(ev.Thinking)
			if ev.Thinking != "" {
				out.ThinkingEvents++
			}
		case llm.EventError:
			if out.StreamErr == nil {
				out.StreamErr = ev.Error
				if out.StreamErr == nil {
					out.StreamErr = fmt.Errorf("driver emitted an error event with no error")
				}
			}
		case llm.EventComplete:
			if ev.Response == nil {
				continue
			}
			r := ev.Response
			out.Usage = r.Usage
			out.FinishReason = r.FinishReason
			out.Signature = r.ThinkingSignature
			out.RedactedThking = r.RedactedThinking
			// Mirrors handleComplete: the final response is authoritative.
			if r.Content != "" {
				streamed.Reset()
				streamed.WriteString(r.Content)
			}
			if r.Thinking != "" {
				thinking.Reset()
				thinking.WriteString(r.Thinking)
			}
			out.ToolCalls = nil
			for i, tc := range r.ToolCalls {
				out.ToolCalls = append(out.ToolCalls, message.ToolCall{
					ID: tc.ID, Name: tc.Name, Input: tc.Input,
					BlockIndex: i, ThoughtSignature: tc.ThoughtSignature,
				})
			}
		}
	}
	out.Latency = time.Since(start)
	out.Text = streamed.String()
	out.Thinking = thinking.String()
	if out.StreamErr == nil && ctx.Err() != nil {
		out.StreamErr = ctx.Err()
	}
	return out
}

// ProbeUserMessage builds a user turn.
func ProbeUserMessage(text string) message.Message {
	return message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: text}}}
}

// ProbeAssistantTurn rebuilds the assistant message the way production
// persists then replays it (call_llm -> save_message -> messageconv): reasoning
// (with signature), redacted reasoning, text, then tool calls carrying their
// ThoughtSignature.
func ProbeAssistantTurn(r ProbeResult) message.Message {
	var parts []message.ContentPart
	if r.Thinking != "" || r.Signature != "" {
		parts = append(parts, message.ReasoningContent{Thinking: r.Thinking, Signature: r.Signature})
	}
	if r.RedactedThking != "" {
		parts = append(parts, message.RedactedReasoningContent{Data: r.RedactedThking})
	}
	if r.Text != "" {
		parts = append(parts, message.TextContent{Text: r.Text})
	}
	for _, tc := range r.ToolCalls {
		parts = append(parts, tc)
	}
	return message.Message{Role: message.Assistant, Parts: parts}
}

// ProbeToolResultMessage builds the tool turn answering the given calls.
func ProbeToolResultMessage(calls []message.ToolCall, content string) message.Message {
	parts := make([]message.ContentPart, 0, len(calls))
	for _, tc := range calls {
		parts = append(parts, message.ToolResult{ToolCallID: tc.ID, Name: tc.Name, Content: content})
	}
	return message.Message{Role: message.Tool, Parts: parts}
}

// ProbeSecretWordTool is a trivial no-argument tool used for round-trip probes.
type ProbeSecretWordTool struct{}

type probeSecretWordParams struct {
	Reason string `json:"reason" jsonschema:"description=Why the word is needed (any short text)"`
}

func (ProbeSecretWordTool) Name() string { return "get_secret_word" }
func (ProbeSecretWordTool) Description() string {
	return "Returns the secret word. Call this tool whenever the user asks for the secret word."
}

// ParamSchema builds the schema exactly as production tools do
// (tools.ToolWrapper.fullParamSchema): inline, no $ref/$defs. A bare
// jsonschema.Reflect emits a root $ref with empty properties, which no real
// tool sends — and which grok on Copilot rejects — so a probe built on it was
// testing a request shape production never makes.
func (ProbeSecretWordTool) ParamSchema() *jsonschema.Schema {
	reflector := jsonschema.Reflector{
		AllowAdditionalProperties: false,
		DoNotReference:            true,
	}
	return tools.ResolveSchemaRefs(reflector.Reflect(&probeSecretWordParams{}))
}
func (ProbeSecretWordTool) RequiresPermission(_ *rctx.ToolContext, _ tools.ToolCall) (bool, error) {
	return false, nil
}
func (ProbeSecretWordTool) Run(_ *rctx.ToolContext, _ tools.ToolCall) (tools.ToolResponse, error) {
	return tools.NewTextResponse("pineapple"), nil
}
