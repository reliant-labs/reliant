// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// signedThinkingDriver answers exactly as Anthropic did on chat 7c37ad6c:
// stop_reason "end_turn" with ONE content block — a signed thinking block whose
// text arrives on the final response rather than as streamed deltas.
//
// The provider spent 399 output tokens here. This is a turn that did real work,
// not an empty response.
type signedThinkingDriver struct {
	thinking  string
	signature string
	redacted  string
}

func (m *signedThinkingDriver) Name() string                          { return "mock" }
func (m *signedThinkingDriver) Model() models.Model                   { return models.Model{ID: "mock-model"} }
func (m *signedThinkingDriver) ValidateKey(ctx context.Context) error { return nil }

func (m *signedThinkingDriver) SendMessages(ctx context.Context, prompts []string, messages []message.Message, availableTools []tools.Tool) (*llm.DriverResponse, error) {
	return &llm.DriverResponse{FinishReason: message.FinishReasonEndTurn}, nil
}

func (m *signedThinkingDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, availableTools []tools.Tool) <-chan llm.DriverEvent {
	ch := make(chan llm.DriverEvent, 1)
	// No content deltas and no thinking deltas — only the authoritative
	// completion. This is the shape that used to vanish.
	ch <- llm.DriverEvent{
		Type: llm.EventComplete,
		Response: &llm.DriverResponse{
			Thinking:          m.thinking,
			ThinkingSignature: m.signature,
			RedactedThinking:  m.redacted,
			FinishReason:      message.FinishReasonEndTurn,
			Usage:             llm.TokenUsage{TokenCount: 704059},
		},
	}
	close(ch)
	return ch
}

// The end-to-end assertion for the stall on chat 7c37ad6c.
//
// Before the fix: thinkingParts stayed empty (the final response's Thinking was
// never read), so the turn looked content-free. It was reported as
// "provider_empty_response", nothing was persisted, and the next send replayed a
// byte-identical prompt — which failed identically. Twice in thirty seconds,
// then indefinitely.
//
// After: the reasoning and its signature reach the output, so the turn is
// persistable and the NEXT request carries the thinking block back. That is what
// makes a resend able to converge instead of reproducing itself.
func TestCallLLM_SignedThinkingTurn_IsKeptNotReportedAsEmpty(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-signed", "user-signed")
	chat := h.CreateTestChat(ctx, "chat-signed", project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	activity := NewCallLLMActivity(
		h.Repo(), nil, nil, &staticConfigProvider{},
		func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
			return &signedThinkingDriver{
				thinking:  "weighing the two approaches",
				signature: "sig-1456",
			}, nil
		},
		nil,
	)

	var output reliantv1.CallLLMOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, buildPlainCallLLMInput(chat.ID), &output))

	// The reasoning and the signature both reach the output. Without BOTH, the
	// block cannot be replayed: Anthropic verifies the signature against the
	// thinking it signs, and a half-populated block is dropped at convert time.
	require.NotNil(t, output.GetThinking())
	assert.Equal(t, "weighing the two approaches", output.GetThinking().GetContent(),
		"the reasoning was on the final response and must survive to the output boundary")
	assert.Equal(t, "sig-1456", output.GetThinking().GetSignature())

	// It must NOT be reported as an empty provider response. That error is for
	// turns that genuinely produced nothing; using it here is what told the
	// user to resend into a loop that could not converge.
	updates, err := h.Repo().GetLatestNonMessageUpdatesPerEntity(ctx, chat.ID)
	require.NoError(t, err)
	for _, u := range updates {
		var p map[string]interface{}
		if json.Unmarshal([]byte(u.Data), &p) != nil {
			continue
		}
		if p["update_type"] == "error" {
			assert.NotEqual(t, "provider_empty_response", p["activity_type"],
				"a signed thinking turn did real work — reporting it as an empty "+
					"response is what produced the resend loop")
		}
	}
}

// The same for sealed reasoning: opaque, but real work that must be kept and
// replayed rather than reported as nothing.
func TestCallLLM_RedactedThinkingTurn_IsKeptNotReportedAsEmpty(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-redacted", "user-redacted")
	chat := h.CreateTestChat(ctx, "chat-redacted", project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	activity := NewCallLLMActivity(
		h.Repo(), nil, nil, &staticConfigProvider{},
		func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
			return &signedThinkingDriver{redacted: "EncryptedOpaquePayload=="}, nil
		},
		nil,
	)

	var output reliantv1.CallLLMOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, buildPlainCallLLMInput(chat.ID), &output))

	require.NotNil(t, output.GetThinking())
	assert.Equal(t, "EncryptedOpaquePayload==", output.GetThinking().GetRedacted())

	// And the ciphertext must never be presented as something the model said.
	assert.Empty(t, output.GetThinking().GetContent(),
		"a sealed payload is not readable reasoning")
	assert.Empty(t, output.GetMessage().GetText(),
		"a sealed payload must never surface as assistant speech")
}
