// Copyright (c) 2025 Reliant Labs
package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
)

// marshalResponsesInput renders converted input items as the SDK sends them.
// See the codex twin (assistant_replay_test.go) for why this asserts on wire
// bytes rather than on the param struct: Phase is `omitzero`, so "absent" and
// "empty string" are only distinguishable after marshaling.
func marshalResponsesInput(t *testing.T, msgs []message.Message) string {
	t.Helper()
	client := &OpenaiClient{}
	data, err := json.Marshal(client.convertMessagesToResponsesInput(nil, msgs))
	if err != nil {
		t.Fatalf("marshal responses input: %v", err)
	}
	return string(data)
}

// TestConvertMessagesToResponsesInput_AssistantReplaysAsAssistantWithPhase is
// the openai-driver half of the same defect the codex driver had: assistant
// history was emitted with EasyInputMessageRoleUser, so a continued turn fed
// the model its own text as if the user had written it.
func TestConvertMessagesToResponsesInput_AssistantReplaysAsAssistantWithPhase(t *testing.T) {
	got := marshalResponsesInput(t, []message.Message{{
		Role:  message.Assistant,
		Parts: []message.ContentPart{message.TextContent{Text: "working on it", Phase: "commentary"}},
	}})

	if !strings.Contains(got, `"role":"assistant"`) {
		t.Errorf("assistant history must replay with role assistant, got: %s", got)
	}
	if !strings.Contains(got, `"phase":"commentary"`) {
		t.Errorf("phase must be resent on assistant messages, got: %s", got)
	}
}

// TestConvertMessagesToResponsesInput_AssistantWithoutPhaseOmitsPhase: phase is
// an enum, so a message that has none must omit the key rather than send "".
func TestConvertMessagesToResponsesInput_AssistantWithoutPhaseOmitsPhase(t *testing.T) {
	got := marshalResponsesInput(t, []message.Message{{
		Role:  message.Assistant,
		Parts: []message.ContentPart{message.TextContent{Text: "done"}},
	}})

	if !strings.Contains(got, `"role":"assistant"`) {
		t.Errorf("assistant history must replay with role assistant, got: %s", got)
	}
	if strings.Contains(got, `"phase"`) {
		t.Errorf("a phase-less assistant message must omit the phase key, got: %s", got)
	}
}
