// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/converter"
)

// GenerateTitleWorkflow builds its activity input as a map[string]interface{}
// (that is what CreateChat sends), while the activity receives the typed
// GenerateTitleInput. Temporal's payload converter bridges the two, so the
// fallback flag only works if the JSON tag matches the map key exactly.
//
// A mocked activity cannot catch a mismatch here — it would silently decode to
// false, the fallback would quietly re-run the failing LLM path, and a chat
// would be left untitled. This test crosses that seam for real.
func TestGenerateTitleInput_FallbackFlagSurvivesPayloadConversion(t *testing.T) {
	dc := converter.GetDefaultDataConverter()

	workflowInput := map[string]interface{}{
		"chat_id":       "chat-123",
		"first_message": "creating titles of chats is broken",
		"use_fallback":  true,
	}

	payload, err := dc.ToPayload(workflowInput)
	require.NoError(t, err)

	var decoded GenerateTitleInput
	require.NoError(t, dc.FromPayload(payload, &decoded))

	assert.Equal(t, "chat-123", decoded.ChatID)
	assert.Equal(t, "creating titles of chats is broken", decoded.FirstMessage)
	assert.True(t, decoded.UseFallback,
		`use_fallback must decode to UseFallback; check the json tag on GenerateTitleInput`)
}

// The normal path omits the key entirely, which must mean "call the LLM".
func TestGenerateTitleInput_DefaultsToLLMPath(t *testing.T) {
	dc := converter.GetDefaultDataConverter()

	payload, err := dc.ToPayload(map[string]interface{}{
		"chat_id":       "chat-123",
		"first_message": "some message",
	})
	require.NoError(t, err)

	var decoded GenerateTitleInput
	require.NoError(t, dc.FromPayload(payload, &decoded))

	assert.False(t, decoded.UseFallback, "absent use_fallback must mean the LLM path")
}
