// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Gemini 3.x requires the thought_signature to be echoed back on every
// functionCall part, so the message↔proto conversions on either side of the
// CallLLM output boundary must both carry it.
func TestToolCallProtoConversions_RoundTripThoughtSignature(t *testing.T) {
	t.Parallel()

	const sig = "CrYBAdHtim9sig"
	protoCalls := messageToolCallsToProto([]message.ToolCall{{
		ID:               "tc1",
		Name:             "view",
		Input:            `{"file_path":"a.go"}`,
		ThoughtSignature: sig,
	}})
	require.Len(t, protoCalls, 1)
	assert.Equal(t, sig, protoCalls[0].GetThoughtSignature(), "messageToolCallsToProto must not drop the signature")

	back := protoToolCallsToMessage(protoCalls)
	require.Len(t, back, 1)
	assert.Equal(t, sig, back[0].ThoughtSignature, "protoToolCallsToMessage must not drop the signature")
	assert.Equal(t, "tc1", back[0].ID)
	assert.Equal(t, `{"file_path":"a.go"}`, back[0].Input)
}

// A tool call recorded in a history from before the capability set still
// carries the input envelope call_llm used to wrap spawn calls in. It must
// still decode — to the bare input — with the signature intact.
func TestToolCallProtoConversions_LegacyInputEnvelopeStillDecodes(t *testing.T) {
	t.Parallel()

	const sig = "sig-with-envelope"
	back := protoToolCallsToMessage([]*reliantv1.ToolCallMsg{{
		Id:               "tc2",
		Name:             "spawn",
		Input:            `{"input":"{\"prompt\":\"go\"}","__reliant_tool_meta__":{"available_presets":["general"]}}`,
		ThoughtSignature: sig,
	}})
	require.Len(t, back, 1)
	assert.Equal(t, sig, back[0].ThoughtSignature)
	assert.Equal(t, `{"prompt":"go"}`, back[0].Input, "the envelope is still decoded")

	// And nothing writes the envelope any more.
	protoCalls := messageToolCallsToProto([]message.ToolCall{{ID: "tc3", Name: "spawn", Input: `{"prompt":"go"}`}})
	require.Len(t, protoCalls, 1)
	assert.Equal(t, `{"prompt":"go"}`, protoCalls[0].GetInput())
}

// The proto ToolCallMsg must expose the field at all; without it the whole
// chain silently loses the signature.
func TestToolCallMsg_HasThoughtSignatureField(t *testing.T) {
	t.Parallel()

	fd := (&reliantv1.ToolCallMsg{}).ProtoReflect().Descriptor().Fields().ByName("thought_signature")
	require.NotNil(t, fd, "ToolCallMsg must declare thought_signature")
}
