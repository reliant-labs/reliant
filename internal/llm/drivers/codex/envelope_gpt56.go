// Copyright (c) 2025 Reliant Labs
package codex

import (
	"encoding/json"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

// The GPT-5.6 family (sol / luna / terra) speaks a different request envelope
// than GPT-5.5 and earlier. Both shapes are current and served by the same
// backend; which one applies is decided by the MODEL, not the client version.
//
// Ground truth is a codex-tui 0.146.0 capture that switches between families on
// a single connection (reliant/.dev/codex/, analyzed in SOL_PROTOCOL_NOTES.md):
//
//	                        gpt-5.6-*            gpt-5.5 (same client)
//	top-level instructions  absent               present
//	top-level tools         absent               present
//	input[0]                additional_tools     (none)
//	system prompt           developer message    top-level instructions
//	reasoning.context       "all_turns"          absent
//	parallel_tool_calls     false                true
//
// Note the prompt TEXT is byte-identical across the two paths; only the
// delivery mechanism differs. This is transport, not new prompt content.
//
// On tools: the captured client declares a single `exec` tool of type "custom"
// that takes JavaScript and proxies ~200 nested tools. We deliberately do not
// do that. In the same tools body it also declares ordinary type:"function"
// tools (`wait`, `request_user_input`), which the server echoes back happily —
// so code-mode is a context-budget optimization for a client with a huge MCP
// surface, not a requirement of the model. We send plain function tools and get
// ordinary function_call items back, so the existing streaming path applies
// unchanged.

// isGPT56 reports whether the model uses the GPT-5.6 request envelope.
func isGPT56(id models.ModelID) bool {
	switch id {
	case models.GPT56Sol, models.GPT56Luna, models.GPT56Terra:
		return true
	default:
		return false
	}
}

// envelopeName labels which request shape a model uses, for logging.
func envelopeName(id models.ModelID) string {
	if isGPT56(id) {
		return "gpt-5.6"
	}
	return "legacy"
}

// modelSupportsReasoningSummaries reports whether we ask the model for reasoning
// summaries (the source of visible "thinking").
//
// GPT-5.3-Codex-Spark genuinely rejects them.
//
// GPT-5.6 is listed here provisionally, NOT because it is known to refuse them.
// The reference client never sends `reasoning.summary` for ANY model — including
// gpt-5.5, where we know summaries work — so the capture cannot distinguish
// "model does not support summaries" from "client never asked". Until someone
// sends a summary request to a 5.6 model and observes the result, asking would
// be guessing against an untested field on a brand-new model family; not asking
// costs only visible thinking, which the capture shows the reference client also
// does without.
//
// To flip this once tested, give the model a reasoning_summary_mode in
// models.yaml and drop it from this list — no other code needs to change.
func modelSupportsReasoningSummaries(id models.ModelID) bool {
	switch id {
	case models.GPT53CodexSpark, models.GPT56Sol, models.GPT56Luna, models.GPT56Terra:
		return false
	default:
		return true
	}
}

// codexReasoningEffort maps our internal effort string onto the wire value.
//
// The SDK's ReasoningEffort constants stop at xhigh; gpt-5.6 adds "max" and
// "ultra" above it. The field is a plain string on the wire, so unknown-to-SDK
// values pass through by construction — we validate against the model's
// declared thinking_levels upstream rather than guessing here.
func codexReasoningEffort(effort string) shared.ReasoningEffort {
	switch effort {
	case "low":
		return shared.ReasoningEffortLow
	case "medium":
		return shared.ReasoningEffortMedium
	case "high":
		return shared.ReasoningEffortHigh
	case "":
		return shared.ReasoningEffortMedium
	default:
		return shared.ReasoningEffort(effort)
	}
}

// newGPT56ReasoningParam builds the reasoning block for a 5.6 request.
//
// `context: "all_turns"` is not modeled by the SDK's ReasoningParam, so the
// whole struct is constructed via param.Override to get the extra key onto the
// wire. The captured client sends it on every 5.6 turn; gpt-5.5 sends nothing
// and the server defaults it to "current_turn".
//
// summary is included only when the caller asks for one, so enabling summaries
// for this family later is a models.yaml change rather than a code change.
func newGPT56ReasoningParam(effort string, summary shared.ReasoningSummary) shared.ReasoningParam {
	fields := map[string]any{
		"effort":  string(codexReasoningEffort(effort)),
		"context": "all_turns",
	}
	if summary != "" {
		fields["summary"] = string(summary)
	}
	return param.Override[shared.ReasoningParam](fields)
}

// developerMessageItem builds a developer-role message carrying one text part.
//
// The SDK's EasyInputMessage role enum has no "developer" variant, so this is
// constructed as a raw item. The shape mirrors the capture exactly: a message
// item whose content is a list of input_text parts.
func developerMessageItem(text string) responses.ResponseInputItemUnionParam {
	return param.Override[responses.ResponseInputItemUnionParam](map[string]any{
		"type": "message",
		"role": "developer",
		"content": []any{
			map[string]any{"type": "input_text", "text": text},
		},
	})
}

// additionalToolsItem wraps converted tools into the `additional_tools` input
// item that gpt-5.6 expects in place of a top-level `tools` array.
//
// The SDK has no variant for this item type, so it is marshaled from the
// already-converted SDK tool params: encoding them to JSON and back yields
// exactly the tool objects the server expects, without re-deriving schemas.
func additionalToolsItem(toolParams []responses.ToolUnionParam) (responses.ResponseInputItemUnionParam, error) {
	encoded, err := json.Marshal(toolParams)
	if err != nil {
		return responses.ResponseInputItemUnionParam{}, err
	}
	var raw []any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return responses.ResponseInputItemUnionParam{}, err
	}

	return param.Override[responses.ResponseInputItemUnionParam](map[string]any{
		"type":  "additional_tools",
		"role":  "developer",
		"tools": raw,
	}), nil
}

// logRequestShape records what we are about to send, at Info.
//
// This exists because the two most common Codex questions — "which model
// actually ran?" and "did my thinking level survive?" — cannot be answered from
// the model's own prose (it has no reliable knowledge of its identity, and we
// send no identity statement) nor from the selection UI (validation gates
// upstream can silently downgrade an effort). Both are answerable only from the
// wire, so both sides of the exchange are logged and meant to be read together.
func (c *CodexClient) logRequestShape(params *responses.ResponseNewParams) {
	toolCount := len(params.Tools)
	additionalTools := 0
	for _, item := range params.Input.OfInputItemList {
		// The 5.6 envelope carries tools as a raw override item, so they are not
		// visible in params.Tools and have to be counted from the payload.
		if raw, ok := rawItemFields(item); ok && raw["type"] == "additional_tools" {
			if declared, ok := raw["tools"].([]any); ok {
				additionalTools = len(declared)
			}
		}
	}

	effort, summary, context := describeReasoning(params.Reasoning)

	logging.Info("[Codex] Request",
		"model", string(c.options.Model.ID),
		"apiModel", string(params.Model),
		"envelope", envelopeName(c.options.Model.ID),
		"requestedEffort", c.options.ReasoningEffort,
		"sentEffort", effort,
		"sentSummary", summary,
		"reasoningContext", context,
		"topLevelTools", toolCount,
		"additionalTools", additionalTools,
		"inputItems", len(params.Input.OfInputItemList),
		"hasInstructions", params.Instructions.Valid(),
		"sessionID", c.sessionID,
	)
}

// logServedResponse records what the server says it actually did.
//
// The echoed model and effort are authoritative in a way the request is not:
// they reflect any server-side substitution or clamping. When these disagree
// with the request line above, that difference IS the bug report.
func (c *CodexClient) logServedResponse(resp *responses.Response, upstreamRequestID string) {
	if resp == nil {
		return
	}

	served := string(resp.Model)
	requested := c.options.Model.APIModel
	if served != "" && requested != "" && served != requested {
		logging.Warn("[Codex] Served model differs from requested model",
			"requested", requested,
			"served", served,
			"responseID", resp.ID,
			"upstreamRequestID", upstreamRequestID,
		)
	}

	logging.Info("[Codex] Served",
		"model", string(c.options.Model.ID),
		"servedModel", served,
		"servedEffort", string(resp.Reasoning.Effort),
		"servedSummary", string(resp.Reasoning.Summary),
		"status", string(resp.Status),
		"reasoningTokens", resp.Usage.OutputTokensDetails.ReasoningTokens,
		"cachedTokens", resp.Usage.InputTokensDetails.CachedTokens,
		"totalTokens", resp.Usage.TotalTokens,
		"responseID", resp.ID,
		"upstreamRequestID", upstreamRequestID,
	)
}

// rawItemFields exposes the payload of an input item built via param.Override,
// which stores its fields as raw JSON rather than in typed struct members.
func rawItemFields(item responses.ResponseInputItemUnionParam) (map[string]any, bool) {
	encoded, err := json.Marshal(item)
	if err != nil {
		return nil, false
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, false
	}
	return fields, true
}

// describeReasoning reports the reasoning block as it will appear on the wire.
// The 5.6 block is built via param.Override, so `context` lives in raw JSON and
// is invisible to the typed fields.
func describeReasoning(reasoning shared.ReasoningParam) (effort, summary, context string) {
	effort = string(reasoning.Effort)
	summary = string(reasoning.Summary)

	encoded, err := json.Marshal(reasoning)
	if err != nil {
		return effort, summary, ""
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return effort, summary, ""
	}
	if v, ok := fields["effort"].(string); ok {
		effort = v
	}
	if v, ok := fields["summary"].(string); ok {
		summary = v
	}
	if v, ok := fields["context"].(string); ok {
		context = v
	}
	return effort, summary, context
}

// applyGPT56Envelope rewrites params from the 5.5 shape into the 5.6 shape.
//
// It is written as a transform over the already-built 5.5 params rather than a
// parallel builder so the two paths cannot drift: everything that is not
// explicitly moved here is shared by construction.
func applyGPT56Envelope(
	params *responses.ResponseNewParams,
	instructions string,
	toolParams []responses.ToolUnionParam,
) error {
	prefix := make([]responses.ResponseInputItemUnionParam, 0, 2)

	// Tools lead the input list, ahead of the system prompt, matching the capture.
	if len(toolParams) > 0 {
		item, err := additionalToolsItem(toolParams)
		if err != nil {
			return err
		}
		prefix = append(prefix, item)
	}
	if strings.TrimSpace(instructions) != "" {
		prefix = append(prefix, developerMessageItem(instructions))
	}

	if len(prefix) > 0 {
		existing := params.Input.OfInputItemList
		combined := make(responses.ResponseInputParam, 0, len(prefix)+len(existing))
		combined = append(combined, prefix...)
		combined = append(combined, existing...)
		params.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: combined}
	}

	// Both now live inside `input`; sending them at top level too would duplicate
	// the prompt and the tool definitions. Both fields are `omitzero`, so zeroing
	// them drops the keys from the payload entirely rather than sending nulls.
	params.Instructions = param.Opt[string]{}
	params.Tools = nil

	// codex-tui pins this false for 5.6 and true for 5.5.
	params.ParallelToolCalls = openai.Bool(false)

	return nil
}
