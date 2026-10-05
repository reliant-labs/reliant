// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
)

func TestProbeAssistantTurnReplaysReasoningAndToolSignatures(t *testing.T) {
	r := ProbeResult{
		Thinking:  "hmm",
		Signature: "sig-1",
		Text:      "calling",
		ToolCalls: []message.ToolCall{{ID: "c1", Name: "get_secret_word", Input: "{}", ThoughtSignature: "ts-1"}},
	}
	msg := ProbeAssistantTurn(r)
	if msg.Role != message.Assistant || len(msg.Parts) != 3 {
		t.Fatalf("unexpected message: %+v", msg)
	}
	if rc, ok := msg.Parts[0].(message.ReasoningContent); !ok || rc.Signature != "sig-1" {
		t.Fatalf("reasoning part lost signature: %+v", msg.Parts[0])
	}
	if tc, ok := msg.Parts[2].(message.ToolCall); !ok || tc.ThoughtSignature != "ts-1" {
		t.Fatalf("tool call lost thought signature: %+v", msg.Parts[2])
	}

	tool := ProbeToolResultMessage(r.ToolCalls, "pineapple")
	if tool.Role != message.Tool || len(tool.Parts) != 1 {
		t.Fatalf("tool message: %+v", tool)
	}
	if tr := tool.Parts[0].(message.ToolResult); tr.ToolCallID != "c1" || tr.Content != "pineapple" {
		t.Fatalf("tool result: %+v", tr)
	}
}

func TestProbeResultErrPrefersResolve(t *testing.T) {
	if (ProbeResult{}).Err() != nil {
		t.Fatal("empty result must have no error")
	}
}
