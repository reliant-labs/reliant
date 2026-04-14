package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

func TestSupportsOpenAIFamilyGuidance(t *testing.T) {
	tests := []struct {
		name  string
		model models.Model
		want  bool
	}{
		{
			name:  "gpt-5.5 enabled",
			model: models.Model{ID: models.GPT55, APIModel: "gpt-5.5"},
			want:  true,
		},
		{
			name:  "gpt-5.4 enabled",
			model: models.Model{ID: models.GPT54, APIModel: "gpt-5.4"},
			want:  true,
		},
		{
			name:  "gpt-5.4-mini enabled",
			model: models.Model{ID: models.GPT54Mini, APIModel: "gpt-5.4-mini"},
			want:  true,
		},
		{
			name:  "gpt-5 prefix fallback enabled",
			model: models.Model{APIModel: "gpt-5-future"},
			want:  true,
		},
		{
			name:  "non gpt model disabled",
			model: models.Model{APIModel: "claude-opus-4-6"},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SupportsOpenAIFamilyGuidance(tt.model); got != tt.want {
				t.Fatalf("SupportsOpenAIFamilyGuidance() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOpenAIFamilyAgentGuidance_PreservesReliantIdentity(t *testing.T) {
	guidance := OpenAIFamilyAgentGuidance(models.Model{ID: models.GPT54, APIModel: "gpt-5.4"})
	if !strings.Contains(guidance, "You are Reliant") {
		t.Fatalf("expected guidance to preserve Reliant identity, got: %s", guidance)
	}
	if !strings.Contains(guidance, "multi-agent, multi-worktree") {
		t.Fatalf("expected guidance to preserve Reliant runtime context, got: %s", guidance)
	}
	if !strings.Contains(guidance, "current project/worktree context") {
		t.Fatalf("expected guidance to preserve project/worktree awareness, got: %s", guidance)
	}
	if !strings.Contains(guidance, "planning and task workflow") {
		t.Fatalf("expected guidance to preserve Reliant execution model, got: %s", guidance)
	}
	if !strings.Contains(guidance, "recoverable workspace state") {
		t.Fatalf("expected guidance to preserve graceful recovery behavior, got: %s", guidance)
	}
	if !strings.Contains(guidance, "<personality_and_writing_controls>") {
		t.Fatalf("expected guidance to include personality and writing controls, got: %s", guidance)
	}
	if !strings.Contains(guidance, "calm, direct, practical tone") {
		t.Fatalf("expected guidance to preserve writing style controls, got: %s", guidance)
	}
	if !strings.Contains(guidance, "<task_update_handling>") {
		t.Fatalf("expected guidance to include task update handling, got: %s", guidance)
	}
	if !strings.Contains(guidance, "<empty_result_recovery>") {
		t.Fatalf("expected guidance to include empty result recovery, got: %s", guidance)
	}
	if !strings.Contains(guidance, "explicitly marked [blocked]") {
		t.Fatalf("expected guidance to preserve strengthened completeness contract, got: %s", guidance)
	}
	if !strings.Contains(guidance, "routine tool calls") {
		t.Fatalf("expected guidance to preserve strengthened user updates spec, got: %s", guidance)
	}
	if !strings.Contains(guidance, "<frontend_defaults>") {
		t.Fatalf("expected guidance to preserve frontend defaults block, got: %s", guidance)
	}
	if !strings.Contains(guidance, "generic templates") {
		t.Fatalf("expected guidance to preserve compact frontend quality guidance, got: %s", guidance)
	}
}

func TestAppendOpenAIFamilyGuidance(t *testing.T) {
	prompts := []string{"primary instruction"}

	appended := AppendOpenAIFamilyGuidance(prompts, models.Model{ID: models.GPT54, APIModel: "gpt-5.4"})
	if len(appended) != 2 {
		t.Fatalf("expected prompt + guidance, got %d items", len(appended))
	}
	if appended[0] != "primary instruction" {
		t.Fatalf("first prompt = %q, want original prompt", appended[0])
	}
	if appended[1] != OpenAIFamilyAgentGuidance(models.Model{ID: models.GPT54, APIModel: "gpt-5.4"}) {
		t.Fatal("expected shared guidance to be appended as final block")
	}
	if len(prompts) != 1 {
		t.Fatal("AppendOpenAIFamilyGuidance should not mutate input slice")
	}

	unmodified := AppendOpenAIFamilyGuidance(prompts, models.Model{APIModel: "claude-opus-4-6"})
	if len(unmodified) != 1 || unmodified[0] != "primary instruction" {
		t.Fatalf("expected non-OpenAI-family model to keep prompts unchanged, got %#v", unmodified)
	}
}

func TestConvertMessages_AppendsGuidanceOnceForChatCompletions(t *testing.T) {
	client := NewClient(llm.DriverOptions{
		Model: models.Model{ID: models.GPT54, APIModel: "gpt-5.4"},
	})

	messages := client.ConvertMessages([]string{"system prompt"}, []message.Message{{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "hello"}},
	}})

	b, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}
	jsonStr := string(b)

	// json.Marshal escapes angle brackets, so search for the escaped form.
	if count := strings.Count(jsonStr, `\u003coutput_contract\u003e`); count != 1 {
		t.Fatalf("expected shared guidance exactly once in chat completions payload, got %d: %s", count, jsonStr)
	}
	if !strings.Contains(jsonStr, "system prompt") {
		t.Fatalf("expected original prompt to remain in payload, got: %s", jsonStr)
	}
}

func TestResponsesInstructions_AppendsGuidanceOnceForSupportedModel(t *testing.T) {
	client := NewClient(llm.DriverOptions{
		Model: models.Model{ID: models.GPT54, APIModel: "gpt-5.4"},
	})

	instructions := client.responsesInstructions([]string{"system prompt"})
	if !strings.Contains(instructions, "system prompt") {
		t.Fatalf("expected original prompt in instructions, got: %s", instructions)
	}
	if count := strings.Count(instructions, "<output_contract>"); count != 1 {
		t.Fatalf("expected shared guidance exactly once in responses instructions, got %d", count)
	}
}

func TestResponsesInstructions_SkipsGuidanceForUnsupportedModel(t *testing.T) {
	client := NewClient(llm.DriverOptions{
		Model: models.Model{APIModel: "claude-opus-4-6"},
	})

	instructions := client.responsesInstructions([]string{"system prompt"})
	if instructions != "system prompt" {
		t.Fatalf("expected unsupported model to keep original instructions only, got %q", instructions)
	}
}

func TestConvertMessagesToResponsesInput_UsesSingleDeveloperMessageWithGuidance(t *testing.T) {
	client := NewClient(llm.DriverOptions{
		Model: models.Model{ID: models.GPT54, APIModel: "gpt-5.4"},
	})

	items := client.convertMessagesToResponsesInput([]string{"system prompt"}, []message.Message{{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "hello"}},
	}})

	b, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal items: %v", err)
	}
	jsonStr := string(b)

	// json.Marshal escapes angle brackets, so search for the escaped form.
	if count := strings.Count(jsonStr, `\u003coutput_contract\u003e`); count != 1 {
		t.Fatalf("expected shared guidance exactly once in responses input, got %d: %s", count, jsonStr)
	}
	if count := strings.Count(jsonStr, "\"role\":\"developer\""); count != 1 {
		t.Fatalf("expected exactly one developer message in responses input, got %d: %s", count, jsonStr)
	}
}
