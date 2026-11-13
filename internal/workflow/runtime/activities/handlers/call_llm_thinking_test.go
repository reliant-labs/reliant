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
	Scope           drivers.Scope
	Preferences     models.Preferences
	ReasoningEffort string
	Model           models.Model
}

// captureDriverOptionsResolver is a drivers.GetDriver replacement that records
// what the default-account path asked for.
func captureDriverOptionsResolver(captured *capturedDriverOptions) func(context.Context, drivers.Scope, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
	return func(ctx context.Context, scope drivers.Scope, prefs models.Preferences, opts ...llm.DriverOption) (llm.Driver, error) {
		captured.Scope = scope
		captured.Preferences = append(models.Preferences(nil), prefs...)

		driverOpts := llm.DriverOptions{}
		for _, opt := range opts {
			opt(&driverOpts)
		}
		captured.ReasoningEffort = driverOpts.ReasoningEffort
		captured.Model = driverOpts.Model

		return &mockLLMDriverForIdempotency{}, nil
	}
}

// asInjectedResolver adapts a GetDriver replacement to the injected-resolver
// seam, which is handed the user alone.
func asInjectedResolver(get func(context.Context, drivers.Scope, models.Preferences, ...llm.DriverOption) (llm.Driver, error)) drivers.DriverResolver {
	return func(ctx context.Context, userID string, prefs models.Preferences, opts ...llm.DriverOption) (llm.Driver, error) {
		return get(ctx, drivers.PersonalScope(userID), prefs, opts...)
	}
}

func TestCallLLMActivity_ReconcilesThinkingLevelForInjectedResolvers(t *testing.T) {
	t.Parallel()

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
				asInjectedResolver(captureDriverOptionsResolver(captured)),
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
