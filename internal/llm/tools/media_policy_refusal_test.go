// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/drivers/videogen"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// A generation the org's policy refuses, at resolution or at the gateway,
// answers the model with a tool result that says so (control-plane
// docs/design/org-policies.md §5.1): the conversation goes on, the call is
// made once, and the result tells the model not to repeat it.

func TestGenerateImage_APolicyRefusalIsAToolResultTheConversationGoesOnFrom(t *testing.T) {
	refused := &orgpolicy.Refusal{Reason: orgpolicy.ReasonModelNotAllowed, Detail: "gpt-image-2.5-flare is not allowed"}

	t.Run("at resolution", func(t *testing.T) {
		resolves := 0
		tool := &generateImageTool{repo: newFakeAttachmentRepo(), resolve: func(context.Context, string, models.ModelSelector) (ImageGenerator, error) {
			resolves++
			return nil, refused
		}}
		resp, err := tool.Execute(generateImageCtx(t), GenerateImageParams{Prompt: "x", Tier: "fast"})
		require.NoError(t, err, "a refusal is a result, never a failed activity")
		assert.True(t, resp.IsError)
		assert.Equal(t, refused.ToolResult("Image generation"), resp.Content)
		assert.Equal(t, 1, resolves, "no other tier is tried: the policy, not the tier, is why")
	})

	t.Run("at the gateway", func(t *testing.T) {
		generator := &fakeImageGenerator{err: refused}
		tool := &generateImageTool{repo: newFakeAttachmentRepo(), resolve: resolverFor(generator)}
		resp, err := tool.Execute(generateImageCtx(t), GenerateImageParams{Prompt: "x"})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "Image generation was refused: Your organization's policy does not allow this model")
		assert.Contains(t, resp.Content, "do not retry")
		assert.NotContains(t, resp.Content, "ORG_POLICY", "the chat marker is for a paused turn, not a tool result")
		assert.Equal(t, 1, generator.calls)
	})
}

func TestVideoErrorMessage_APolicyRefusal(t *testing.T) {
	refusal := &orgpolicy.Refusal{Reason: orgpolicy.ReasonOrgSpendCapReached}
	err := &videogen.Error{Kind: videogen.KindPolicyRefused, Message: refusal.Message(), Refusal: refusal}
	got := videoErrorMessage("Video generation could not be started", err)
	assert.Equal(t, refusal.ToolResult("Video generation"), got)
	assert.Contains(t, got, "caps the organization's AI spend")
}

func TestMediaToolUnavailable_ExplainsAPolicyRefusal(t *testing.T) {
	mediaAvailabilityMu.Lock()
	prev := mediaAvailabilityCheck
	mediaAvailabilityMu.Unlock()
	SetMediaAvailabilityCheck(func(context.Context, string, models.Modality) error {
		return &orgpolicy.Refusal{Reason: orgpolicy.ReasonProviderNotAllowed}
	})
	t.Cleanup(func() { SetMediaAvailabilityCheck(prev) })

	msg, modality := mediaToolUnavailable(generateImageCtx(t).Context, GenerateImageToolName)
	assert.Equal(t, models.ModalityImage, modality)
	assert.Contains(t, msg, "Image generation was refused: Your organization's policy does not allow this provider")
}

// A generation only the person's own accounts could make, in an org chat
// whose person has not chosen to spend them there, is a decision about who
// pays: shown as it is, with no other tier tried.
func TestGenerateImage_AFundingRefusalIsShownAsItIs(t *testing.T) {
	refused := models.NewMediaPersonalFundingError(models.ModalityImage, false)
	resolves := 0
	tool := &generateImageTool{repo: newFakeAttachmentRepo(), resolve: func(context.Context, string, models.ModelSelector) (ImageGenerator, error) {
		resolves++
		return nil, refused
	}}
	resp, err := tool.Execute(generateImageCtx(t), GenerateImageParams{Prompt: "x", Tier: "fast"})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Equal(t, refused.Message, resp.Content)
	assert.Equal(t, 1, resolves)
	assert.Contains(t, refused.Message, "Image generation was not started")
}
