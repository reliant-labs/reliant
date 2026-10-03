// Copyright (c) 2025 Reliant Labs
package responseswire

import (
	"strings"

	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// AssistantHistoryItem builds the Responses input item that replays one
// assistant message from history.
//
// It lives here for the same reason FinishReason does: two driver packages
// speak the Responses API and each had its own copy of this conversion, each
// wrong in the same two ways.
//
// ROLE. Both drivers emitted assistant history as EasyInputMessageRoleUser,
// the codex copy carrying the comment "SDK uses user role for assistant
// history". That is simply false — EasyInputMessageRoleAssistant is one of the
// four documented roles on EasyInputMessageParam — and the consequence is that
// the model read its own prior words as if the user had said them. That was
// survivable while history always ended with a user message or a tool result.
// CallLLMArgs.continue_turn makes it load-bearing: a continued turn calls the
// provider again with history ending in the model's OWN text, so the entire
// tail of the request was mis-attributed to the user.
//
// PHASE. openai-go v3.55.0 documents Phase on EasyInputMessageParam: "Labels
// an `assistant` message as intermediate commentary (`commentary`) or the final
// answer (`final_answer`). For models like `gpt-5.3-codex` and beyond, when
// sending follow-up requests, preserve and resend phase on all assistant
// messages — dropping it can degrade performance." We captured it (see
// AssistantPhase) but dropped it on the way back out.
//
// WHY EasyInputMessageParam RATHER THAN ResponseOutputMessageParam. Both carry
// a Phase field, so either could express this. EasyInputMessage wins because
// ResponseOutputMessageParam declares `ID string json:"id,omitzero" api:"required"`
// — it models an item the API RETURNED, and the id it requires is the
// provider's own output-message id. We do not have one: history is rebuilt from
// our content blocks, whose ids are ours, and inventing or omitting a provider
// id on a replayed item is a request the API is entitled to reject.
// EasyInputMessageParam has no id at all and is the SDK's declared shape for
// "previous assistant responses" ("Can also contain previous assistant
// responses" — its Content doc), which is exactly what this is.
//
// The message's text parts are concatenated into one item, matching what both
// drivers already did. Phase comes from the LAST text part that carries one:
// the parts become a single item, so one phase has to characterise the whole
// thing, and it is the final emission that says how the turn ended — the same
// rule AssistantPhase applies when reading a response.
//
// Returns ok=false when the message has no text worth sending, which is the
// drivers' existing behaviour (an assistant message may be tool calls only).
func AssistantHistoryItem(msg *message.Message) (item responses.ResponseInputItemUnionParam, ok bool) {
	textParts := msg.TextContents()
	texts := make([]string, 0, len(textParts))
	phase := ""
	for _, part := range textParts {
		if trimmed := strings.TrimSpace(part.Text); trimmed != "" {
			texts = append(texts, trimmed)
		}
		// Read phase from every part, not only the ones with text: a phase is
		// a property of the emission, and dropping it because its own text was
		// whitespace would lose a label the turn genuinely carried.
		if part.Phase != "" {
			phase = part.Phase
		}
	}

	combined := strings.Join(texts, "\n\n")
	if combined == "" {
		return responses.ResponseInputItemUnionParam{}, false
	}

	assistant := responses.EasyInputMessageParam{
		Role: responses.EasyInputMessageRoleAssistant,
		Content: responses.EasyInputMessageContentUnionParam{
			OfString: param.NewOpt(combined),
		},
	}
	// Assigned only when present. Phase is `omitzero` over an enum of
	// "commentary" | "final_answer", so leaving it zero omits the key, while
	// assigning "" would send an invalid value instead of a missing one.
	if phase != "" {
		assistant.Phase = responses.EasyInputMessagePhase(phase)
	}

	return responses.ResponseInputItemUnionParam{OfMessage: &assistant}, true
}
