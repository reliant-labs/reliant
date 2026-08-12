// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// assistantWithThinking builds an assistant message carrying a thinking block.
func assistantWithThinking(thinking, signature string) message.Message {
	parts := []message.ContentPart{message.TextContent{Text: "the answer"}}
	if thinking != "" || signature != "" {
		parts = append([]message.ContentPart{
			message.ReasoningContent{Thinking: thinking, Signature: signature},
		}, parts...)
	}
	return message.Message{ID: "m-1", Role: message.Assistant, Parts: parts}
}

// countThinkingBlocks reports how many thinking blocks convertMessages emitted.
func countThinkingBlocks(t *testing.T, opts llm.DriverOptions, msgs []message.Message) int {
	t.Helper()
	b := &baseClient{options: opts}
	converted := b.convertMessages(msgs)

	n := 0
	for _, m := range converted {
		for _, blk := range m.Content {
			if blk.OfThinking != nil {
				n++
			}
		}
	}
	return n
}

// adaptiveOpts mirrors a real adaptive-thinking model (opus-5 / sonnet-5 /
// fable-5): ThinkingMode "adaptive" and NO ReasoningEffort, because adaptive
// thinking carries effort in output_config rather than a token budget.
func adaptiveOpts() llm.DriverOptions {
	return llm.DriverOptions{
		Model:     models.Model{APIModel: "claude-5-sonnet", CanReason: true, ThinkingMode: "adaptive"},
		MaxTokens: 8192,
	}
}

// TestConvertMessages_AdaptiveModelReplaysThinkingBlock is the regression test
// for the cache-invalidation bug.
//
// The replay gate used to test only isThinkingEnabled(), which is false for
// adaptive models (they set no ReasoningEffort). Meanwhile getThinkingConfig
// and applyClaudeCodeExtras DO enable thinking for them, so every request asked
// for thinking while no assistant turn ever replayed its thinking block. The
// rebuilt history stopped matching the provider's cached prefix, and
// cacheReadPct collapsed 99% -> 11% on exactly the turns that thought most.
func TestConvertMessages_AdaptiveModelReplaysThinkingBlock(t *testing.T) {
	got := countThinkingBlocks(t, adaptiveOpts(),
		[]message.Message{assistantWithThinking("deliberating", "sig-abc")})

	if got != 1 {
		t.Fatalf("adaptive model replayed %d thinking blocks, want 1 — "+
			"the request asks for thinking, so history must carry it back", got)
	}
}

// A budget model with reasoning enabled must keep replaying thinking blocks.
func TestConvertMessages_BudgetModelStillReplaysThinkingBlock(t *testing.T) {
	opts := llm.DriverOptions{
		Model:           models.Model{APIModel: "claude-haiku-4-5", CanReason: true, ThinkingMode: "budget"},
		ReasoningEffort: "medium",
		MaxTokens:       8192,
	}

	if got := countThinkingBlocks(t, opts,
		[]message.Message{assistantWithThinking("deliberating", "sig-abc")}); got != 1 {
		t.Fatalf("budget model replayed %d thinking blocks, want 1", got)
	}
}

// TestConvertMessages_NeverReplaysThinkingWithoutSignature covers the API
// contract directly: Anthropic rejects a thinking block whose signature is
// absent or does not verify. A half-populated block must be dropped rather than
// sent — losing the cache on that turn is strictly better than a 400 that fails
// the whole request.
func TestConvertMessages_NeverReplaysThinkingWithoutSignature(t *testing.T) {
	if got := countThinkingBlocks(t, adaptiveOpts(),
		[]message.Message{assistantWithThinking("deliberating", "")}); got != 0 {
		t.Fatalf("replayed %d thinking blocks with an empty signature, want 0", got)
	}
}

// Thinking is NOT enabled for the request when the model can't reason, so no
// thinking block may be replayed — sending one is an API error.
func TestConvertMessages_NonReasoningModelDropsThinking(t *testing.T) {
	opts := llm.DriverOptions{
		Model:     models.Model{APIModel: "claude-haiku-4-5", CanReason: false, ThinkingMode: "budget"},
		MaxTokens: 8192,
	}

	if got := countThinkingBlocks(t, opts,
		[]message.Message{assistantWithThinking("deliberating", "sig-abc")}); got != 0 {
		t.Fatalf("replayed %d thinking blocks for a non-reasoning model, want 0", got)
	}
}

// An assistant turn that produced no thinking (short answer, or a provider that
// returned none) converts cleanly with thinking enabled. This is the case that
// would break if we ever required a thinking block on every assistant message.
func TestConvertMessages_AssistantWithoutThinkingIsFine(t *testing.T) {
	msgs := []message.Message{assistantWithThinking("", "")}

	if got := countThinkingBlocks(t, adaptiveOpts(), msgs); got != 0 {
		t.Fatalf("replayed %d thinking blocks for a turn that had none, want 0", got)
	}

	b := &baseClient{options: adaptiveOpts()}
	converted := b.convertMessages(msgs)
	if len(converted) != 1 {
		t.Fatalf("converted %d messages, want 1", len(converted))
	}
	if len(converted[0].Content) == 0 {
		t.Fatal("assistant message lost all content blocks")
	}
}
