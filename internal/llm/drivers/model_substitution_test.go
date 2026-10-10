// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
	"github.com/reliant-labs/reliant/internal/llm/drivers/codex"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// claudeOnly is prod user 30542add's providers on 2026-10-10: Codex
// disconnected (UpdateProviderAPIKey 2026-10-09T22:56:22Z), Claude connected
// (CompleteClaudeOAuth 22:56:40Z), which the resolver registers as anthropic.
func claudeOnly() models.AvailableDrivers {
	return models.AvailableDrivers{Drivers: map[models.DriverID]models.DriverConfig{
		"anthropic": cfgFor("anthropic"),
	}}
}

// THE REGRESSION (prod chat 66a045ce). The chat was pinned to
// gpt-5.6-sol@codex, its owner had disconnected Codex, and a send of
// "continue" was refused. A send now moves the pin to what the connected
// providers offer for the same tier, and says so.
func TestSubstituteUnservableModel_PinnedToDisconnectedProviderMovesToSameTier(t *testing.T) {
	t.Parallel()
	selector := map[string]interface{}{"id": "gpt-5.6-sol@codex", "thinking_level": "max"}

	sub, ok := SubstituteUnservableModel(selector, claudeOnly())
	require.True(t, ok, "a pin no connected provider serves must be substituted when a connected provider serves its tier")

	want, err := models.MustGetRegistry().Resolve(models.ModelSelector{Tags: []string{models.TagPowerful}}, []string{"anthropic"})
	require.NoError(t, err)
	assert.Equal(t, "gpt-5.6-sol@codex", sub.From)
	assert.Equal(t, want.Definition.ID+"@anthropic", sub.To, "gpt-5.6-sol's tier is powerful; the substitute is powerful's pick on anthropic")
	assert.Equal(t, sub.To, sub.Selector["id"], "the replacement is pinned like the picker pins")
	assert.Equal(t, want.ThinkingLevel, sub.Selector["thinking_level"], "a different model runs at its tier's effort, not the old model's")
	assert.Equal(t, "max", selector["thinking_level"], "the caller's selector is not mutated")

	assert.Equal(t,
		"gpt-5.6-sol runs only on Codex (ChatGPT), and Codex (ChatGPT) is not connected. So this chat continues on "+
			want.Definition.ID+" via Anthropic (Claude), the same tier on a provider you have connected. "+
			"To go back, reconnect Codex (ChatGPT) in Settings → Providers, then pick gpt-5.6-sol in the composer's model picker.",
		sub.Notice)

	reg := models.MustGetRegistry()
	_, err = reg.Resolve(models.ModelSelector{ID: sub.To}, []string{"anthropic"})
	assert.NoError(t, err, "the substitute must be servable by the providers it was chosen from")
}

// A provider that rejected the stored credential is named with its reason,
// the same words the run's error uses.
func TestSubstituteUnservableModel_RejectedProviderNamesTheRejection(t *testing.T) {
	t.Parallel()
	available := claudeOnly()
	available.Unavailable = map[models.DriverID]string{
		"codex": "Codex (ChatGPT) rejected the saved credential (HTTP 401: token revoked)",
	}
	sub, ok := SubstituteUnservableModel(map[string]interface{}{"id": "gpt-5.6-sol@codex"}, available)
	require.True(t, ok)
	assert.Contains(t, sub.Notice, "gpt-5.6-sol runs only on Codex (ChatGPT), and Codex (ChatGPT) rejected the saved credential (HTTP 401: token revoked).")
	assert.NotContains(t, sub.Notice, "is not connected")
}

// The same model on another connected provider beats any tier pick, and keeps
// the effort the user chose.
func TestSubstituteUnservableModel_SameModelElsewhereKeepsTheEffort(t *testing.T) {
	t.Parallel()
	sub, ok := SubstituteUnservableModel(map[string]interface{}{"id": "claude-5.5-opus@copilot", "thinking_level": "low"}, claudeOnly())
	require.True(t, ok)
	assert.Equal(t, "claude-5.5-opus@anthropic", sub.To)
	assert.Equal(t, "low", sub.Selector["thinking_level"])
}

// Only a selector that cannot be served is substituted. One that resolves,
// one that names no model, a tag selector and a user with nothing that could
// stand in are all left alone.
func TestSubstituteUnservableModel_LeavesEverythingElseAlone(t *testing.T) {
	t.Parallel()
	codexToo := claudeOnly()
	codexToo.Drivers["codex"] = cfgFor("codex")

	for name, tc := range map[string]struct {
		selector  interface{}
		available models.AvailableDrivers
	}{
		"servable pin":       {map[string]interface{}{"id": "gpt-5.6-sol@codex"}, codexToo},
		"servable unpinned":  {map[string]interface{}{"id": "claude-5.5-opus"}, claudeOnly()},
		"unknown model":      {map[string]interface{}{"id": "no-such-model@codex"}, claudeOnly()},
		"tag selector":       {map[string]interface{}{"tags": []interface{}{"flagship"}}, claudeOnly()},
		"test model":         {map[string]interface{}{"id": "mock"}, claudeOnly()},
		"no provider at all": {map[string]interface{}{"id": "gpt-5.6-sol@codex"}, models.AvailableDrivers{}},
		"string selector":    {"gpt-5.6-sol@codex", claudeOnly()},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, ok := SubstituteUnservableModel(tc.selector, tc.available)
			assert.False(t, ok)
		})
	}
}

// providerKeysRepo is the slice of db.Repository BuildAvailableDrivers reads
// for API-key providers. Any other method panics (nil embedded interface).
type providerKeysRepo struct {
	db.Repository
	keys map[string]string
}

func (r providerKeysRepo) GetProviderAPIKeys(context.Context, string) (map[string]string, error) {
	masked := make(map[string]string, len(r.keys))
	for provider := range r.keys {
		masked[provider] = "****"
	}
	return masked, nil
}

func (r providerKeysRepo) GetProviderAPIKey(_ context.Context, _ string, provider string) (string, error) {
	key, ok := r.keys[provider]
	if !ok {
		return "", errors.New("no key for " + provider)
	}
	return key, nil
}

// useProviderKeys installs a user whose only credentials are API keys, and
// restores the previous provider when the test ends. Tests using it must not
// be parallel: the provider is process-global.
func useProviderKeys(t *testing.T, keys map[string]string) {
	t.Helper()
	providerMu.Lock()
	previous := globalAPIKeyProvider
	globalAPIKeyProvider = &APIKeyProvider{repo: providerKeysRepo{keys: keys}}
	providerMu.Unlock()
	previousClient := AccountAvailabilityClient
	AccountAvailabilityClient = noReportClients
	t.Cleanup(func() {
		providerMu.Lock()
		globalAPIKeyProvider = previous
		providerMu.Unlock()
		AccountAvailabilityClient = previousClient
	})
}

// The send path's validation names the pinned provider, why it cannot serve
// and both fixes, as the run's resolution error does — it used to say "none
// of required providers [codex] available for model gpt-5.6-sol. Check your
// API key configuration in Settings", which is what prod returned to
// "continue" in chat 66a045ce.
func TestValidateModelSelector_PinnedProviderUnavailableIsExplained(t *testing.T) {
	useProviderKeys(t, map[string]string{"anthropic": "sk-ant-api03-test"})

	err := ValidateModelSelector(context.Background(), "user-1", map[string]interface{}{"id": "gpt-5.6-sol@codex"})
	require.Error(t, err)
	assert.ErrorIs(t, err, drivererrors.ErrNoServableProvider, "callers tell availability from a bad input by this sentinel")
	assert.Equal(t,
		"model 'gpt-5.6-sol@codex' is not available: gpt-5.6-sol runs only on Codex (ChatGPT), and Codex (ChatGPT) is not connected. "+
			"Reconnect Codex (ChatGPT) in Settings → Providers, or pick a model from a connected provider in the composer's model picker",
		err.Error())
}

// A provider declared unavailable recovers by itself: nothing about the
// verdict is persisted, so the first report the provider accepts puts it
// back, without the user reconnecting. (The reporter's own cache bounds how
// long a verdict is reused; see codex TestReportAvailability_RejectionRecovers.)
func TestWithAccountAvailability_RejectedProviderRecoversWhenAccepted(t *testing.T) {
	t.Parallel()
	configured := func() map[models.DriverID]models.DriverConfig {
		return map[models.DriverID]models.DriverConfig{"codex": cfgFor("codex"), "anthropic": cfgFor("anthropic")}
	}
	codexReport := &fakeReporter{err: fmt.Errorf("codex models request: %w", registry.RejectedCredential(401, "token expired"))}
	build := reporters(map[models.DriverID]*fakeReporter{"codex": codexReport})

	rejected := withAccountAvailability(context.Background(), configured(), build)
	require.NotContains(t, rejected.Drivers, models.DriverID("codex"))
	require.Contains(t, rejected.Unavailable, models.DriverID("codex"))

	codexReport.err = nil
	codexReport.report = codexServes(codexAPIModel(t, "gpt-5.6-sol"))
	recovered := withAccountAvailability(context.Background(), configured(), build)
	assert.Contains(t, recovered.Drivers, models.DriverID("codex"))
	assert.Empty(t, recovered.Unavailable)
	reg := models.MustGetRegistry().WithAvailability(recovered.Availability)
	resolved, err := reg.Resolve(models.ModelSelector{ID: "gpt-5.6-sol@codex"}, servableProviderIDs(recovered))
	require.NoError(t, err, "the pinned model is servable again")
	assert.Equal(t, "codex", resolved.Provider.Driver)
}

// What a Codex user's model picker lists after #673, through the production
// path — ListAvailableModels' aggregation with the real availability client
// (clientForDriver → codex.NewClient → GET /codex/models) — against the
// /codex/models fixture. gpt-6.1-sol is codex's default; the request carries
// the client_version the backend gates gpt-6-sol and gpt-6-luna on.
func TestAggregateAvailableModels_CodexUserIsOfferedGPT61Sol(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("codex/testdata/codex_models.json")
	require.NoError(t, err)
	token := mintCodexJWT(t, "acct-picker-gpt61", time.Now().Add(time.Hour))

	var seenVersion, seenAuth, seenPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenVersion, seenAuth, seenPath = r.URL.Query().Get("client_version"), r.Header.Get("Authorization"), r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	available := models.AvailableDrivers{Drivers: map[models.DriverID]models.DriverConfig{
		"codex": {DriverID: "codex", APIKey: token, BaseURL: srv.URL, Enabled: true, UserID: "user-codex"},
	}}
	infos, err := aggregateAvailableModels(context.Background(), available, clientForDriver)
	require.NoError(t, err)

	assert.Equal(t, codex.CodexVersion, seenVersion)
	assert.Equal(t, "0.155.0", seenVersion, "the version #673 moved to; gpt-6-sol and gpt-6-luna need it")
	assert.Equal(t, "Bearer "+token, seenAuth)
	assert.True(t, strings.HasSuffix(seenPath, "/models"), "GET %s", seenPath)

	enabled := map[string]bool{}
	for _, info := range infos {
		if info.DriverID == "codex" && info.Enabled {
			enabled[info.ID] = true
		}
	}
	for _, id := range []string{"gpt-6.1-sol", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol"} {
		assert.Truef(t, enabled[id], "a Codex user's picker must offer %s; got %v", id, enabled)
	}
}
