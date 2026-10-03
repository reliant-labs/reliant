// Copyright (c) 2025 Reliant Labs
package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
)

// marshalItems renders a converted input param the way the SDK sends it, so the
// assertions are about the bytes the provider actually receives.
//
// Asserting on the Go param struct instead would miss the case this test
// exists for: Phase is `json:"phase,omitzero"`, so an empty phase has to vanish
// from the wire rather than arrive as "".
func marshalItems(t *testing.T, msgs []message.Message) string {
	t.Helper()
	client := &CodexClient{}
	data, err := json.Marshal(client.convertMessages(msgs))
	if err != nil {
		t.Fatalf("marshal input items: %v", err)
	}
	return string(data)
}

func assistantMessage(text, phase string) message.Message {
	return message.Message{
		Role:  message.Assistant,
		Parts: []message.ContentPart{message.TextContent{Text: text, Phase: phase}},
	}
}

// TestConvertMessages_AssistantReplaysAsAssistantWithPhase pins both halves of
// the replay defect.
//
// The driver used to emit assistant history as EasyInputMessageRoleUser, with
// the comment "SDK uses user role for assistant history". That is false —
// EasyInputMessageRoleAssistant exists — and it made the model read its own
// words as if the user had said them. That only became load-bearing with
// CallLLMArgs.continue_turn, which calls the provider again with history
// ending in the model's OWN text: the whole tail of a continued turn was
// mis-attributed.
//
// Phase is the second half. openai-go v3.55.0 documents it on
// EasyInputMessageParam: "For models like gpt-5.3-codex and beyond, when
// sending follow-up requests, preserve and resend phase on all assistant
// messages — dropping it can degrade performance."
func TestConvertMessages_AssistantReplaysAsAssistantWithPhase(t *testing.T) {
	got := marshalItems(t, []message.Message{assistantMessage("I'll inspect the auth setup.", "commentary")})

	if !strings.Contains(got, `"role":"assistant"`) {
		t.Errorf("assistant history must replay with role assistant, got: %s", got)
	}
	if strings.Contains(got, `"role":"user"`) {
		t.Errorf("assistant history must not replay as the user, got: %s", got)
	}
	if !strings.Contains(got, `"phase":"commentary"`) {
		t.Errorf("phase must be resent on assistant messages, got: %s", got)
	}
}

// TestConvertMessages_AssistantWithoutPhaseOmitsPhase covers history saved
// before phase was persisted, and every non-Responses provider that never
// reports one. An empty phase must be ABSENT, not "": the field is an enum of
// "commentary" | "final_answer", so sending an empty string would be an invalid
// value rather than a missing one.
func TestConvertMessages_AssistantWithoutPhaseOmitsPhase(t *testing.T) {
	got := marshalItems(t, []message.Message{assistantMessage("done", "")})

	if !strings.Contains(got, `"role":"assistant"`) {
		t.Errorf("assistant history must replay with role assistant, got: %s", got)
	}
	if strings.Contains(got, `"phase"`) {
		t.Errorf("a phase-less assistant message must omit the phase key, got: %s", got)
	}
}

// TestConvertMessages_AssistantPhaseFromLastTextPart pins which phase wins when
// a turn narrated before answering. The parts are concatenated into one item,
// so one phase has to characterise the whole thing, and it is the final
// emission that says how the turn ended — the same rule as
// responseswire.AssistantPhase.
func TestConvertMessages_AssistantPhaseFromLastTextPart(t *testing.T) {
	got := marshalItems(t, []message.Message{{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.TextContent{Text: "thinking out loud", Phase: "commentary"},
			message.TextContent{Text: "here is the answer", Phase: "final_answer"},
		},
	}})

	if !strings.Contains(got, `"phase":"final_answer"`) {
		t.Errorf("the last text part's phase must win, got: %s", got)
	}
	if strings.Contains(got, `"phase":"commentary"`) {
		t.Errorf("an earlier part's phase must not win, got: %s", got)
	}
}

// TestConvertMessages_UserStaysUser guards the obvious regression in the other
// direction: the fix must not flip user history to assistant.
func TestConvertMessages_UserStaysUser(t *testing.T) {
	got := marshalItems(t, []message.Message{{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "hello"}},
	}})

	if !strings.Contains(got, `"role":"user"`) {
		t.Errorf("user history must stay role user, got: %s", got)
	}
	if strings.Contains(got, `"role":"assistant"`) {
		t.Errorf("user history must not become assistant, got: %s", got)
	}
}
