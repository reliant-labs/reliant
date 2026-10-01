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

// A signature must survive alongside the input-envelope metadata, which is
// encoded into the input field rather than carried as its own proto field.
func TestToolCallProtoConversions_SignatureSurvivesInputEnvelope(t *testing.T) {
	t.Parallel()

	const sig = "sig-with-envelope"
	protoCalls := messageToolCallsToProto([]message.ToolCall{{
		ID:               "tc2",
		Name:             "spawn",
		Input:            `{"prompt":"go"}`,
		SpawnWorkflow:    "builtin://agent",
		ThoughtSignature: sig,
	}})
	require.Len(t, protoCalls, 1)
	require.Equal(t, sig, protoCalls[0].GetThoughtSignature())

	back := protoToolCallsToMessage(protoCalls)
	require.Len(t, back, 1)
	assert.Equal(t, sig, back[0].ThoughtSignature)
	assert.Equal(t, `{"prompt":"go"}`, back[0].Input, "the envelope is still decoded")
	assert.Equal(t, "builtin://agent", back[0].SpawnWorkflow)
}

// The proto ToolCallMsg must expose the field at all; without it the whole
// chain silently loses the signature.
func TestToolCallMsg_HasThoughtSignatureField(t *testing.T) {
	t.Parallel()

	fd := (&reliantv1.ToolCallMsg{}).ProtoReflect().Descriptor().Fields().ByName("thought_signature")
	require.NotNil(t, fd, "ToolCallMsg must declare thought_signature")
}
