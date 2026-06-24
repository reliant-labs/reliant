package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturedDriverOptions struct {
	Preferences     models.Preferences
	ReasoningEffort string
}

func captureDriverOptionsResolver(captured *capturedDriverOptions) drivers.DriverResolver {
	return func(ctx context.Context, userID string, prefs models.Preferences, opts ...llm.DriverOption) (llm.Driver, error) {
		captured.Preferences = append(models.Preferences(nil), prefs...)

		driverOpts := llm.DriverOptions{}
		for _, opt := range opts {
			opt(&driverOpts)
		}
		captured.ReasoningEffort = driverOpts.ReasoningEffort

		return &mockLLMDriverForIdempotency{}, nil
	}
}

func TestCallLLMActivity_ReconcilesThinkingLevelForInjectedResolvers(t *testing.T) {
	tests := []struct {
		name                string
		modelID             string
		thinkingLevel       string
		wantReasoningEffort string
		wantPreferenceModel models.ModelID
		wantPreferenceThink string
	}{
		{
			name:                "mock non-reasoning model disables explicit thinking",
			modelID:             "mock",
			thinkingLevel:       "low",
			wantPreferenceModel: "mock-model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewIdempotencyTestHelper(t)
			defer h.Cleanup()

			ctx := context.Background()
			project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
			chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
			h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

			captured := &capturedDriverOptions{}
			activityInstance := NewCallLLMActivity(
				h.Repo(),
				nil,
				nil,
				&staticConfigProvider{},
				captureDriverOptionsResolver(captured),
				nil,
			)

			input := callLLMInput(chat.ID, chat.ID, tt.modelID)
			input.Node.GetCallLlm().ThinkingLevel = &reliantv1.CelString{
				Value: &reliantv1.CelString_Literal{Literal: tt.thinkingLevel},
			}

			var output CallLLMOutput
			err := h.ExecuteActivity(activityInstance.Execute, input, &output)
			require.NoError(t, err)
			require.Len(t, captured.Preferences, 1)
			assert.Equal(t, tt.wantPreferenceModel, captured.Preferences[0].ModelID)
			assert.Equal(t, tt.wantPreferenceThink, captured.Preferences[0].ThinkingMode)
			assert.Equal(t, tt.wantReasoningEffort, captured.ReasoningEffort)
		})
	}
}
