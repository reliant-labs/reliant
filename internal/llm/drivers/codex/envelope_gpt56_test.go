// Copyright (c) 2025 Reliant Labs
package codex

import (
	"encoding/json"
	"testing"

	"github.com/openai/openai-go/v3/shared"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	llmtools "github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// marshalParams builds a request for the given model and returns it as a
// decoded generic map, so assertions run against the bytes that actually go on
// the wire rather than against SDK structs.
func marshalParams(t *testing.T, model models.Model, effort string, toolList []llmtools.Tool) map[string]any {
	t.Helper()

	client := &CodexClient{
		options: llm.DriverOptions{Model: model, ReasoningEffort: effort},
	}
	messages := []message.Message{{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "hello"}},
	}}

	params, err := client.buildParams([]string{"system prompt"}, messages, toolList)
	if err != nil {
		t.Fatalf("buildParams returned error: %v", err)
	}

	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("failed to marshal params: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("failed to unmarshal params: %v", err)
	}
	return decoded
}

func gpt56Model(id models.ModelID, apiModel string) models.Model {
	return models.Model{ID: id, APIModel: apiModel, CanReason: true}
}

// The 5.6 envelope moves the system prompt and tools into `input`. Asserting on
// the serialized payload is the point: the whole feature is a wire-format
// change, so a test against SDK structs would not catch a regression that
// silently stops emitting the fields.
func TestBuildParams_GPT56UsesAdditionalToolsEnvelope(t *testing.T) {
	for _, modelID := range []models.ModelID{models.GPT56Sol, models.GPT56Luna, models.GPT56Terra} {
		t.Run(string(modelID), func(t *testing.T) {
			payload := marshalParams(t,
				gpt56Model(modelID, string(modelID)),
				"high",
				[]llmtools.Tool{stubTool{name: "bash"}},
			)

			if _, ok := payload["instructions"]; ok {
				t.Error("expected no top-level instructions on gpt-5.6; prompt belongs in input")
			}
			if _, ok := payload["tools"]; ok {
				t.Error("expected no top-level tools on gpt-5.6; tools belong in additional_tools")
			}

			if ptc, ok := payload["parallel_tool_calls"].(bool); !ok || ptc {
				t.Errorf("expected parallel_tool_calls=false on gpt-5.6, got %v", payload["parallel_tool_calls"])
			}

			input, ok := payload["input"].([]any)
			if !ok || len(input) < 3 {
				t.Fatalf("expected at least 3 input items (tools, prompt, message), got %#v", payload["input"])
			}

			// Tools lead, then the system prompt, then the conversation.
			first, _ := input[0].(map[string]any)
			if first["type"] != "additional_tools" {
				t.Errorf("expected input[0].type=additional_tools, got %v", first["type"])
			}
			if first["role"] != "developer" {
				t.Errorf("expected input[0].role=developer, got %v", first["role"])
			}
			declared, ok := first["tools"].([]any)
			if !ok || len(declared) != 1 {
				t.Fatalf("expected exactly 1 declared tool, got %#v", first["tools"])
			}
			tool, _ := declared[0].(map[string]any)
			if tool["name"] != "bash" {
				t.Errorf("expected tool name bash, got %v", tool["name"])
			}
			// We deliberately do NOT use codex-tui's `exec` code-mode tool; our
			// tools go over as ordinary function tools.
			if tool["type"] != "function" {
				t.Errorf("expected tool type=function, got %v", tool["type"])
			}

			second, _ := input[1].(map[string]any)
			if second["type"] != "message" || second["role"] != "developer" {
				t.Errorf("expected input[1] to be a developer message, got %#v", second)
			}
			content, ok := second["content"].([]any)
			if !ok || len(content) != 1 {
				t.Fatalf("expected one content part in the prompt message, got %#v", second["content"])
			}
			part, _ := content[0].(map[string]any)
			if part["type"] != "input_text" {
				t.Errorf("expected prompt part type=input_text, got %v", part["type"])
			}
			if text, _ := part["text"].(string); text == "" {
				t.Error("expected the system prompt text to be carried in the developer message")
			}
		})
	}
}

func TestBuildParams_GPT56ReasoningCarriesContextAndNoSummary(t *testing.T) {
	payload := marshalParams(t, gpt56Model(models.GPT56Sol, "gpt-5.6-sol"), "max", nil)

	reasoning, ok := payload["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("expected a reasoning block, got %#v", payload["reasoning"])
	}
	if reasoning["effort"] != "max" {
		t.Errorf("expected effort=max to pass through, got %v", reasoning["effort"])
	}
	if reasoning["context"] != "all_turns" {
		t.Errorf("expected context=all_turns, got %v", reasoning["context"])
	}
	// We do not currently request summaries for 5.6. This is a provisional
	// choice, not a proven model limitation: the reference client never sends
	// `reasoning.summary` for any model, so the capture cannot tell us whether
	// 5.6 would honor one. See modelSupportsReasoningSummaries.
	if _, ok := reasoning["summary"]; ok {
		t.Errorf("expected no summary on gpt-5.6, got %v", reasoning["summary"])
	}
}

// Enabling summaries for 5.6 later should be a models.yaml change, so the
// builder must actually thread a requested summary onto the wire.
func TestNewGPT56ReasoningParam_IncludesSummaryWhenRequested(t *testing.T) {
	raw, err := json.Marshal(newGPT56ReasoningParam("high", shared.ReasoningSummaryDetailed))
	if err != nil {
		t.Fatalf("failed to marshal reasoning param: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("failed to unmarshal reasoning param: %v", err)
	}

	if decoded["summary"] != "detailed" {
		t.Errorf("expected summary=detailed to reach the wire, got %v", decoded["summary"])
	}
	if decoded["context"] != "all_turns" {
		t.Errorf("expected context to survive alongside summary, got %v", decoded["context"])
	}
}

// `ultra` is above xhigh and unknown to the SDK's enum. It must survive as a
// raw string rather than being clamped to a value the SDK happens to know.
func TestBuildParams_GPT56PassesThroughUltraEffort(t *testing.T) {
	payload := marshalParams(t, gpt56Model(models.GPT56Terra, "gpt-5.6-terra"), "ultra", nil)

	reasoning, _ := payload["reasoning"].(map[string]any)
	if reasoning["effort"] != "ultra" {
		t.Errorf("expected effort=ultra to survive serialization, got %v", reasoning["effort"])
	}
}

// The 5.5 and earlier envelope must be untouched by the 5.6 work.
func TestBuildParams_PreGPT56KeepsLegacyEnvelope(t *testing.T) {
	payload := marshalParams(t,
		models.Model{ID: models.GPT55, APIModel: "gpt-5.5", CanReason: true},
		"xhigh",
		[]llmtools.Tool{stubTool{name: "bash"}},
	)

	if instructions, _ := payload["instructions"].(string); instructions == "" {
		t.Error("expected gpt-5.5 to keep top-level instructions")
	}
	tools, ok := payload["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected gpt-5.5 to keep top-level tools, got %#v", payload["tools"])
	}
	if ptc, ok := payload["parallel_tool_calls"].(bool); !ok || !ptc {
		t.Errorf("expected parallel_tool_calls=true on gpt-5.5, got %v", payload["parallel_tool_calls"])
	}

	reasoning, _ := payload["reasoning"].(map[string]any)
	if _, ok := reasoning["context"]; ok {
		t.Error("expected no reasoning.context on gpt-5.5")
	}
	if reasoning["summary"] == nil {
		t.Error("expected gpt-5.5 to still request a reasoning summary")
	}

	input, _ := payload["input"].([]any)
	for i, item := range input {
		if entry, _ := item.(map[string]any); entry["type"] == "additional_tools" {
			t.Fatalf("gpt-5.5 must not carry an additional_tools item (found at input[%d])", i)
		}
	}
}

// The diagnostic logging is the only way to answer "which model actually ran
// and at what effort" from a live session, so the helpers behind it are worth
// pinning — particularly for 5.6, where the reasoning block and the tool list
// are raw override payloads that typed field access cannot see.
func TestDescribeReasoning_ReadsRawOverrideFields(t *testing.T) {
	effort, summary, context := describeReasoning(newGPT56ReasoningParam("ultra", ""))
	if effort != "ultra" {
		t.Errorf("expected effort=ultra, got %q", effort)
	}
	if context != "all_turns" {
		t.Errorf("expected context=all_turns, got %q", context)
	}
	if summary != "" {
		t.Errorf("expected no summary, got %q", summary)
	}

	// The legacy path uses typed fields; both must read correctly.
	effort, summary, context = describeReasoning(shared.ReasoningParam{
		Effort:  shared.ReasoningEffortHigh,
		Summary: shared.ReasoningSummaryConcise,
	})
	if effort != "high" || summary != "concise" {
		t.Errorf("expected high/concise, got %q/%q", effort, summary)
	}
	if context != "" {
		t.Errorf("expected no context on the legacy envelope, got %q", context)
	}
}

func TestRawItemFields_SeesAdditionalTools(t *testing.T) {
	client := &CodexClient{
		options: llm.DriverOptions{Model: gpt56Model(models.GPT56Sol, "gpt-5.6-sol"), ReasoningEffort: "high"},
	}
	messages := []message.Message{{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "hello"}},
	}}

	params, err := client.buildParams([]string{"system prompt"}, messages, []llmtools.Tool{stubTool{name: "bash"}})
	if err != nil {
		t.Fatalf("buildParams returned error: %v", err)
	}

	found := 0
	for _, item := range params.Input.OfInputItemList {
		raw, ok := rawItemFields(item)
		if !ok || raw["type"] != "additional_tools" {
			continue
		}
		declared, _ := raw["tools"].([]any)
		found = len(declared)
	}
	if found != 1 {
		t.Errorf("expected to count 1 tool inside additional_tools, got %d", found)
	}
}

func TestEnvelopeName(t *testing.T) {
	if got := envelopeName(models.GPT56Sol); got != "gpt-5.6" {
		t.Errorf("expected gpt-5.6, got %q", got)
	}
	if got := envelopeName(models.GPT55); got != "legacy" {
		t.Errorf("expected legacy, got %q", got)
	}
}

func TestIsGPT56(t *testing.T) {
	for _, id := range []models.ModelID{models.GPT56Sol, models.GPT56Luna, models.GPT56Terra} {
		if !isGPT56(id) {
			t.Errorf("expected %s to use the gpt-5.6 envelope", id)
		}
	}
	for _, id := range []models.ModelID{models.GPT55, models.GPT54, models.GPT53Codex, models.GPT53CodexSpark} {
		if isGPT56(id) {
			t.Errorf("expected %s to use the legacy envelope", id)
		}
	}
}

func TestModelSupportsReasoningSummaries(t *testing.T) {
	noSummaries := []models.ModelID{
		models.GPT53CodexSpark,
		models.GPT56Sol, models.GPT56Luna, models.GPT56Terra,
	}
	for _, id := range noSummaries {
		if modelSupportsReasoningSummaries(id) {
			t.Errorf("expected %s to be excluded from summary requests", id)
		}
	}
	if !modelSupportsReasoningSummaries(models.GPT55) {
		t.Error("expected gpt-5.5 to support reasoning summaries")
	}
}

// Spark is the only model that opts out of the encrypted-content include;
// 5.6 returns encrypted reasoning that must be round-tripped even though we
// do not request a summary.
func TestBuildParams_GPT56StillIncludesEncryptedReasoning(t *testing.T) {
	payload := marshalParams(t, gpt56Model(models.GPT56Sol, "gpt-5.6-sol"), "high", nil)

	include, ok := payload["include"].([]any)
	if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Errorf("expected gpt-5.6 to include reasoning.encrypted_content, got %#v", payload["include"])
	}
}
