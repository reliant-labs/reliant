// Copyright (c) 2025 Reliant Labs
package responseswire

import (
	"strings"

	"github.com/openai/openai-go/v3/responses"
	"github.com/reliant-labs/reliant/internal/llm"
)

// Message phases on a Responses output message (openai-go types them as a bare
// string enum).
const (
	PhaseCommentary  = "commentary"
	PhaseFinalAnswer = "final_answer"
)

// PrematureStop reports whether a turn that the provider called completed
// looks like the model stopped before doing the work, and how.
//
// Two shapes count, and only on a `completed` response with no tool calls and
// no `end_turn: false` (that one is already a PauseTurn, which the loop
// continues on its own):
//
//   - commentary: the last assistant message has phase "commentary" and no
//     message has phase "final_answer". The model narrated its next step and
//     stopped without taking it.
//   - reasoning_only: no visible text at all. The model thought and produced
//     neither an answer nor a tool call.
//
// Codex CLI would end the turn on both (it acts only on end_turn and tool
// calls). call_llm instead retries once with a short nudge; this function only
// recognises the shape.
//
// Messages are read from the terminal response's output when it carries any,
// and otherwise from the messages the stream delivered through
// response.output_item.done: the Codex backend may send a terminal event whose
// `output` is empty. streamedText is every output_text delta the stream
// carried, so a turn whose terminal output is empty is not mistaken for a
// reasoning-only one. A turn whose phase cannot be read is never classed as
// commentary: the default is the provider's own verdict.
func PrematureStop(resp *responses.Response, streamed []responses.ResponseOutputMessage, toolCallCount int, streamedText string) llm.PrematureStop {
	if resp == nil || resp.Status != responses.ResponseStatusCompleted || toolCallCount > 0 {
		return ""
	}
	if value, present := EndTurn(resp); present && !value {
		return ""
	}

	var messages []responses.ResponseOutputMessage
	for _, out := range resp.Output {
		if msg := out.AsMessage(); msg.Type == "message" {
			messages = append(messages, msg)
		}
	}
	if len(messages) == 0 {
		messages = streamed
	}

	hasText := strings.TrimSpace(streamedText) != ""
	sawFinal := false
	lastPhase := ""
	for _, msg := range messages {
		lastPhase = string(msg.Phase)
		if lastPhase == PhaseFinalAnswer {
			sawFinal = true
		}
		for _, part := range msg.Content {
			if part.Type == "output_text" && strings.TrimSpace(part.Text) != "" {
				hasText = true
			}
		}
	}

	if lastPhase == PhaseCommentary && !sawFinal {
		return llm.PrematureStopCommentary
	}
	if !hasText {
		return llm.PrematureStopReasoningOnly
	}
	return ""
}
