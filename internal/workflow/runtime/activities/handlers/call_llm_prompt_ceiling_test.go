package handlers

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// codexAdvertisedLimit is what GET /codex/models advertises as
// max_context_window for gpt-5.6-terra (testdata/codex_models.json in the codex
// driver). It is below the catalog-derived prompt ceiling (1,050,000 window −
// 128,000 max output = 922,000), so it is the one that applies on codex.
const codexAdvertisedLimit = 872_000

// terraOnCodex resolves gpt-5.6-terra@codex the way CallLLM does, with the
// account's /codex/models report applied.
func terraOnCodex(t *testing.T) *models.ModelDefinition {
	t.Helper()
	live := func(driver, modelID string) models.ModelAvailability {
		if driver == "codex" && modelID == "gpt-5.6-terra" {
			return models.ModelAvailability{ContextWindow: codexAdvertisedLimit}
		}
		return models.ModelAvailability{}
	}
	resolved, err := models.MustGetRegistry().WithAvailability(live).
		Resolve(models.ModelSelector{ID: "gpt-5.6-terra@codex"}, []string{"codex"})
	require.NoError(t, err)
	return &resolved.Definition
}

// TestPromptCeiling_TerraOnCodexIs872k: the prompt ceiling for terra on codex
// is the backend's advertised 872k, not the 272k default working window that
// /codex/models also reports (context_window) and the catalog used to carry.
// Prod accepted 698,604-token terra prompts, so 272k was never the limit.
func TestPromptCeiling_TerraOnCodexIs872k(t *testing.T) {
	def := terraOnCodex(t)
	require.EqualValues(t, codexAdvertisedLimit, promptCeiling(def, "codex", def.ToModel()))
}

// TestResolveCompactionThreshold_TerraOnCodexResolvesAgainst872k: both an
// unpinned chat and a 1M pin compact at 85% of the 872k ceiling. Before, both
// resolved to 231,200 (85% of 272k).
func TestResolveCompactionThreshold_TerraOnCodexResolvesAgainst872k(t *testing.T) {
	def := terraOnCodex(t)
	ceiling := promptCeiling(def, "codex", def.ToModel())
	const want = 741_200 // 0.85 × 872,000

	if got := resolveCompactionThreshold(&reliantv1.CallLLMArgs{}, 0, def, "codex", ceiling); got != want {
		t.Errorf("unpinned threshold = %d, want %d", got, want)
	}
	pinned := &reliantv1.CallLLMArgs{Model: selectorWithThreshold(1_000_000)}
	if got := resolveCompactionThreshold(pinned, 0, def, "codex", ceiling); got != want {
		t.Errorf("1M pin capped to %d, want %d", got, want)
	}
}

// toolResultHistory is a turn whose tool result is about resultTokens tokens.
// countOnCall, when positive, is the provider-reported context size stored on
// the assistant turn that made the call.
func toolResultHistory(resultTokens int, countOnCall int64) []message.Message {
	return []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "read the logs"}}},
		{Role: message.Assistant, TokenCount: countOnCall, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc_1", Name: "view", Input: "{}"},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{
			ToolCallID: "tc_1", Name: "view", Content: strings.Repeat("r", resultTokens*message.CharsPerToken),
		}}},
	}
}

func toolResultContent(msgs []message.Message) string {
	return msgs[2].Parts[0].(message.ToolResult).Content
}

// TestTrimBackstop_TerraOnCodexEngagesAt95PercentOf872k: the backstop sits at
// 95% of the 872k ceiling (828,400 tokens). An 800k-token context is left alone
// — before, the 272k window put the backstop at 258,400 and this was shredded —
// and an 850k one is trimmed.
func TestTrimBackstop_TerraOnCodexEngagesAt95PercentOf872k(t *testing.T) {
	def := terraOnCodex(t)
	ceiling := promptCeiling(def, "codex", def.ToModel())

	below := toolResultHistory(800_000, 0)
	original := toolResultContent(below)
	if message.TrimMessagesToFitContextWindow(below, nil, nil, ceiling) {
		t.Error("trimmed an 800k-token context; the backstop is 95% of 872k = 828,400")
	}
	if toolResultContent(below) != original {
		t.Error("800k-token tool result was rewritten")
	}

	above := toolResultHistory(850_000, 0)
	if !message.TrimMessagesToFitContextWindow(above, nil, nil, ceiling) {
		t.Error("did not trim an 850k-token context; the backstop is 95% of 872k = 828,400")
	}
}

// TestTokenCount_698kOnTerraCodexIsTrusted: a stored 698,604-token count (the
// largest terra prompt prod saw in 14 days) is inside the 872k ceiling, so it is
// trusted as the context size and drives the backstop: with a 140k tool result
// after it the context is ~838k, past 828,400, and the result is trimmed.
// Before, the count was above the 272k window, was discarded as unreliable, and
// the ~140k character estimate trimmed nothing.
func TestTokenCount_698kOnTerraCodexIsTrusted(t *testing.T) {
	def := terraOnCodex(t)
	ceiling := promptCeiling(def, "codex", def.ToModel())

	msgs := toolResultHistory(140_000, 698_604)
	original := toolResultContent(msgs)
	if !message.TrimMessagesToFitContextWindow(msgs, nil, nil, ceiling) {
		t.Fatal("698,604 count + 140k result was not trimmed: the count was not trusted as the context size")
	}
	if len(toolResultContent(msgs)) >= len(original) {
		t.Error("tool result was not shortened")
	}

	// Control: the count alone is below the backstop, so nothing is trimmed.
	small := toolResultHistory(1_000, 698_604)
	if message.TrimMessagesToFitContextWindow(small, nil, nil, ceiling) {
		t.Error("trimmed a ~700k context; the backstop is 828,400")
	}
}
