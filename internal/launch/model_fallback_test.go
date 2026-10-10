// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// claudeOnlyRepo is prod user 30542add's provider settings on 2026-10-10:
// Codex disconnected, Claude connected — here as an Anthropic API key, which
// BuildAvailableDrivers registers under the same anthropic driver.
type claudeOnlyRepo struct{ db.Repository }

func (claudeOnlyRepo) GetProviderAPIKeys(context.Context, string) (map[string]string, error) {
	return map[string]string{"anthropic": "****"}, nil
}

func (claudeOnlyRepo) GetProviderAPIKey(_ context.Context, _ string, provider string) (string, error) {
	if provider != "anthropic" {
		return "", errors.New("no key for " + provider)
	}
	return "sk-ant-api03-test", nil
}

// THE REGRESSION (prod chat 66a045ce, 2026-10-10T14:41:32Z): "continue" was
// refused with "invalid_argument: workflow input validation failed: input
// 'model': model 'gpt-5.6-sol@codex' is not available: none of required
// providers [codex] available for model gpt-5.6-sol". A model the user's
// providers cannot serve right now is not an invalid input.
//
// The API key provider is process-global and has no way back to "unset", so
// this leaves claudeOnlyRepo installed. That is safe in this package: every
// other test here uses the "mock" model, which validation resolves without
// reading providers.
func TestValidateWorkflowInputs_UnservableModelIsNotAValidationError(t *testing.T) {
	drivers.InitializeAPIKeyProvider(claudeOnlyRepo{})
	launcher := NewLauncher(nil, nil, nil, nil, "")
	ctx := context.Background()

	errs := launcher.ValidateWorkflowInputs(ctx, "user-1", "builtin://agent", "project-1", map[string]interface{}{
		"mode":  "auto",
		"model": map[string]interface{}{"id": "gpt-5.6-sol@codex"},
	})
	assert.Empty(t, errs, "an unservable model must reach the run, which explains it, not refuse the send")

	errs = launcher.ValidateWorkflowInputs(ctx, "user-1", "builtin://agent", "project-1", map[string]interface{}{
		"mode":  "auto",
		"model": map[string]interface{}{"id": "no-such-model"},
	})
	require.Len(t, errs, 1, "an unknown model is still a bad input")
	assert.Contains(t, errs[0].Error(), "model not found")
}

// A send moves the chat's model input — top level or inside a group — onto
// what the connected providers offer, in the caller's own maps, and reports
// each move for the chat notice.
func TestSubstituteModelInputs_RewritesUnservableSelectorsInPlace(t *testing.T) {
	modelSchema := &reliantv1.Input{Type: "model", Config: &reliantv1.Input_ModelInput{ModelInput: &reliantv1.ModelInputConfig{}}}
	schemas := map[string]*reliantv1.Input{
		"model": modelSchema,
		"review": {Type: "group", Config: &reliantv1.Input_GroupInput{GroupInput: &reliantv1.GroupInputConfig{
			Inputs: map[string]*reliantv1.Input{"model": modelSchema},
		}}},
		"mode": {Type: "string"},
	}
	review := map[string]interface{}{"model": map[string]interface{}{"id": "gpt-5.6-sol@codex"}}
	inputs := map[string]interface{}{
		"mode":   "auto",
		"model":  map[string]interface{}{"id": "gpt-5.6-sol@codex", "thinking_level": "max"},
		"review": review,
	}
	available := models.AvailableDrivers{Drivers: map[models.DriverID]models.DriverConfig{
		"anthropic": {DriverID: "anthropic", APIKey: "sk-ant-api03-test", Enabled: true},
	}}

	subs := substituteModelInputs(inputs, schemas, available, "")

	require.Len(t, subs, 2)
	assert.Equal(t, "model", subs[0].Input)
	assert.Equal(t, "review.model", subs[1].Input)
	for _, sub := range subs {
		assert.Equal(t, "gpt-5.6-sol@codex", sub.From)
		assert.True(t, strings.HasSuffix(sub.To, "@anthropic"), sub.To)
		assert.Contains(t, sub.Notice, "Codex (ChatGPT) is not connected")
	}
	assert.Equal(t, subs[0].To, inputs["model"].(map[string]interface{})["id"], "the caller's inputs carry the substitute")
	assert.Equal(t, subs[1].To, review["model"].(map[string]interface{})["id"], "including inside a group")
	assert.Equal(t, "auto", inputs["mode"])

	again := substituteModelInputs(inputs, schemas, available, "")
	assert.Empty(t, again, "a substituted selector is servable, so a second pass changes nothing")
}
