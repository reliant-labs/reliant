// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSettingsReader struct {
	rows map[string]string
	err  error
}

func (f fakeSettingsReader) ListSettingsByKey(_ context.Context, _ string, _ string) ([]*db.Setting, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]*db.Setting, 0, len(f.rows))
	for k, v := range f.rows {
		out = append(out, &db.Setting{Key: k, Value: v})
	}
	return out, nil
}

func TestResolveLLMCall_AppliesTagPreferences(t *testing.T) {
	f64 := func(v float64) *float64 { return &v }
	moderate := models.ModelSelector{Tags: []string{models.TagModerate}}

	tests := []struct {
		name          string
		selector      models.ModelSelector
		reader        fakeSettingsReader
		temperature   *float64
		thinking      string
		wantModelID   string
		wantThinking  string
		wantTemp      *float64
		wantCompactAt int32
	}{
		{
			name:         "no preference leaves the tier untouched",
			selector:     moderate,
			wantModelID:  "claude-5.5-sonnet@anthropic",
			wantThinking: "medium",
		},
		{
			name:     "thinking level and temperature apply to the tier's model",
			selector: moderate,
			reader: fakeSettingsReader{rows: map[string]string{
				"model.tag_config.moderate": `{"thinking_level":"high","temperature":0.3}`,
			}},
			wantModelID:  "claude-5.5-sonnet@anthropic",
			wantThinking: "high",
			wantTemp:     f64(0.3),
		},
		{
			name:     "model_id replaces the tier model and drops the tier's effort",
			selector: moderate,
			reader: fakeSettingsReader{rows: map[string]string{
				"model.tag_config.moderate": `{"model_id":"claude-5.5-opus"}`,
			}},
			wantModelID:  "claude-5.5-opus@anthropic",
			wantThinking: "medium",
		},
		{
			name:     "model_id with the pref's thinking level",
			selector: moderate,
			reader: fakeSettingsReader{rows: map[string]string{
				"model.tag_config.moderate": `{"model_id":"claude-5.5-opus","thinking_level":"low"}`,
			}},
			wantModelID:  "claude-5.5-opus@anthropic",
			wantThinking: "low",
		},
		{
			name:     "model_id the user cannot serve is ignored, other fields still apply",
			selector: moderate,
			reader: fakeSettingsReader{rows: map[string]string{
				"model.tag_config.moderate": `{"model_id":"gpt-6-sol","thinking_level":"high"}`,
			}},
			wantModelID:  "claude-5.5-sonnet@anthropic",
			wantThinking: "high",
		},
		{
			name:     "model_id missing from the registry is ignored",
			selector: moderate,
			reader: fakeSettingsReader{rows: map[string]string{
				"model.tag_config.moderate": `{"model_id":"no-such-model"}`,
			}},
			wantModelID:  "claude-5.5-sonnet@anthropic",
			wantThinking: "medium",
		},
		{
			name:        "node arg beats the preference",
			selector:    moderate,
			temperature: f64(0),
			thinking:    "low",
			reader: fakeSettingsReader{rows: map[string]string{
				"model.tag_config.moderate": `{"thinking_level":"high","temperature":0.9}`,
			}},
			wantModelID:  "claude-5.5-sonnet@anthropic",
			wantThinking: "low",
			wantTemp:     f64(0),
		},
		{
			name:     "malformed JSON is ignored, not fatal",
			selector: moderate,
			reader: fakeSettingsReader{rows: map[string]string{
				"model.tag_config.moderate": `{not json`,
			}},
			wantModelID:  "claude-5.5-sonnet@anthropic",
			wantThinking: "medium",
		},
		{
			name:         "a read failure is ignored, not fatal",
			selector:     moderate,
			reader:       fakeSettingsReader{err: errors.New("db down")},
			wantModelID:  "claude-5.5-sonnet@anthropic",
			wantThinking: "medium",
		},
		{
			name:     "unknown thinking level in the pref is ignored",
			selector: moderate,
			reader: fakeSettingsReader{rows: map[string]string{
				"model.tag_config.moderate": `{"thinking_level":"ultra"}`,
			}},
			wantModelID:  "claude-5.5-sonnet@anthropic",
			wantThinking: "medium",
		},
		{
			name:     "multi-tag selector uses the FIRST tag's preference only",
			selector: models.ModelSelector{Tags: []string{models.TagModerate, models.TagFast}},
			reader: fakeSettingsReader{rows: map[string]string{
				"model.tag_config.moderate": `{"thinking_level":"high"}`,
				"model.tag_config.fast":     `{"thinking_level":"low"}`,
			}},
			wantModelID:  "claude-5.5-sonnet@anthropic",
			wantThinking: "high",
		},
		{
			name:     "id-based selectors have no tier, so no preference",
			selector: models.ModelSelector{ID: "claude-5.5-sonnet"},
			reader: fakeSettingsReader{rows: map[string]string{
				"model.tag_config.moderate": `{"thinking_level":"high","model_id":"claude-5.5-opus"}`,
			}},
			wantModelID:  "claude-5.5-sonnet@anthropic",
			wantThinking: "medium",
		},
		{
			name:     "compaction threshold is returned for the caller",
			selector: moderate,
			reader: fakeSettingsReader{rows: map[string]string{
				"model.tag_config.moderate": `{"compaction_threshold":50000}`,
			}},
			wantModelID:   "claude-5.5-sonnet@anthropic",
			wantThinking:  "medium",
			wantCompactAt: 50000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userID := "user-" + uuid.NewString()
			ctx := context.Background()
			provisionProviderKeys(t, ctx, userID, []string{"anthropic"})

			captured := &capturedDriverOptions{}
			original := drivers.GetDriver
			drivers.GetDriver = captureDriverOptionsResolver(captured)
			t.Cleanup(func() { drivers.GetDriver = original })

			spec := llmCallSpec{
				UserID:        userID,
				SessionID:     "session-" + uuid.NewString(),
				Selector:      tt.selector,
				Temperature:   tt.temperature,
				ThinkingLevel: tt.thinking,
			}
			if tt.reader.rows != nil || tt.reader.err != nil {
				spec.TagPrefsReader = tt.reader
			}
			resolved, err := resolveLLMCall(ctx, nil, spec)
			require.NoError(t, err)

			assert.Equal(t, tt.wantModelID, resolved.ModelID)
			assert.Equal(t, tt.wantThinking, resolved.ThinkingLevel)
			assert.Equal(t, tt.wantThinking, captured.ReasoningEffort)
			assert.Equal(t, tt.wantCompactAt, resolved.TagCompactionThreshold)
			if tt.wantTemp != nil {
				require.NotNil(t, resolved.Temperature)
				assert.Equal(t, *tt.wantTemp, *resolved.Temperature)
			}
		})
	}
}

func TestResolveLLMCall_NoTagPrefsReaderIgnoresPreferences(t *testing.T) {
	// Compaction/title calls leave TagPrefsReader nil: the user's tier tuning
	// must not reach housekeeping calls.
	userID := "user-" + uuid.NewString()
	ctx := context.Background()
	provisionProviderKeys(t, ctx, userID, []string{"anthropic"})
	captured := &capturedDriverOptions{}
	original := drivers.GetDriver
	drivers.GetDriver = captureDriverOptionsResolver(captured)
	t.Cleanup(func() { drivers.GetDriver = original })

	resolved, err := resolveLLMCall(ctx, nil, llmCallSpec{
		UserID:    userID,
		SessionID: "s",
		Selector:  models.ModelSelector{Tags: []string{models.TagModerate}},
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-5.5-sonnet@anthropic", resolved.ModelID)
	assert.Equal(t, "medium", resolved.ThinkingLevel)
}
