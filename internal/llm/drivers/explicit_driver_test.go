// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// An explicit "model@driver" is a decision, not a hint. When that driver cannot
// serve the model, the call must fail and say so. It must never quietly run on
// some other configured provider.
//
// The silent fallback was real and invisible: copilot's driver carried a stale
// three-model allowlist, so every newly mapped copilot model resolved to
// "@copilot" in the registry and then ran on anthropic / codex / antigravity
// instead — a different bill and different behaviour, with the request still
// labelled copilot everywhere it was recorded.
func TestDefaultGetDriver_ExplicitDriverThatCannotServeTheModelFails(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	InitializeAPIKeyProvider(repo)

	ctx := context.Background()
	userID := "explicit-driver-user"
	// openrouter CAN serve claude-5-sonnet, so the old fallback had somewhere
	// to go; reliant (the explicit pick) cannot.
	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "openrouter", "sk-or-test"))
	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "reliant", "rlat_abcdef0123456789abcdef0123456789"))
	require.False(t, models.CanDriverUseModel("reliant", models.ModelID("claude-5-sonnet")),
		"precondition: the reliant gateway does not serve claude-5-sonnet")
	require.True(t, models.CanDriverUseModel("openrouter", models.ModelID("claude-5-sonnet")),
		"precondition: openrouter does, so a silent fallback had somewhere to go")

	driver, err := defaultGetDriver(ctx, userID, models.Preferences{{ModelID: "claude-5-sonnet@reliant"}})

	require.Error(t, err, "an unservable explicit driver must fail, got driver %v", driverName(driver))
	assert.Contains(t, err.Error(), "reliant")
	assert.Contains(t, err.Error(), "claude-5-sonnet")
}

// Without an explicit driver, auto-selection is still the behaviour: the caller
// asked for a model, not a provider.
func TestDefaultGetDriver_UnpinnedModelStillAutoSelects(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	InitializeAPIKeyProvider(repo)

	ctx := context.Background()
	userID := "unpinned-driver-user"
	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "openrouter", "sk-or-test"))

	driver, err := defaultGetDriver(ctx, userID, models.Preferences{{ModelID: "claude-5-sonnet"}})

	require.NoError(t, err)
	assert.Equal(t, "openrouter", driverName(driver))
}

func driverName(d interface{ Name() string }) string {
	if d == nil {
		return "<nil>"
	}
	return d.Name()
}
