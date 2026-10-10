// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// noReportClients builds clients that report no account availability, so a
// test of credential assembly never reaches a provider's real catalog.
func noReportClients(_ context.Context, id models.DriverID, _ models.DriverConfig) (registry.Client, error) {
	return &fakeClient{name: string(id)}, nil
}

// fakeReporter is a dynamic provider (Copilot, Codex) whose account catalog
// answers with report/err.
type fakeReporter struct {
	fakeClient
	report registry.ProviderAvailability
	err    error
}

func (f *fakeReporter) ReportAvailability(context.Context) (registry.ProviderAvailability, error) {
	return f.report, f.err
}

func (f *fakeReporter) GetAvailableModels(ctx context.Context) ([]models.ModelInfo, error) {
	report, err := f.ReportAvailability(ctx)
	if err != nil {
		return nil, err
	}
	return registry.ApplyAvailability(models.MustGetRegistry().ModelsForDriver(f.name), report), nil
}

func reporters(byDriver map[models.DriverID]*fakeReporter) clientBuilder {
	return func(_ context.Context, id models.DriverID, _ models.DriverConfig) (registry.Client, error) {
		if r, ok := byDriver[id]; ok {
			r.name = string(id)
			return r, nil
		}
		return &fakeClient{name: string(id)}, nil
	}
}

// What GitHub answered chat dfd85515's Copilot driver, wrapped exactly as
// copilot.modelsStatusError wraps it.
var errCopilotBadlyFormatted = fmt.Errorf("copilot models request: %w",
	registry.RejectedCredential(400, "bad request: Authorization header is badly formatted"))

// codexServes is a Codex account catalog serving the given api_models, and
// nothing else (/codex/models is authoritative).
func codexServes(apiModels ...string) registry.ProviderAvailability {
	report := registry.ProviderAvailability{
		Models:         map[string]models.ModelAvailability{},
		Authoritative:  true,
		UnlistedReason: "not available on your ChatGPT plan's Codex backend",
	}
	for _, m := range apiModels {
		report.Models[m] = models.ModelAvailability{}
	}
	return report
}

func codexAPIModel(t *testing.T, modelID string) string {
	t.Helper()
	def, ok := models.MustGetRegistry().GetDefinition(modelID)
	require.True(t, ok, "catalog must define %s", modelID)
	for _, p := range def.Providers {
		if p.Driver == "codex" {
			return p.APIModel
		}
	}
	t.Fatalf("%s has no codex mapping", modelID)
	return ""
}

func resolveFlagship(t *testing.T, available models.AvailableDrivers) (*models.ResolvedModel, error) {
	t.Helper()
	reg := models.MustGetRegistry().WithAvailability(available.Availability)
	var providers []string
	for id, cfg := range available.Drivers {
		if cfg.IsConfigured() {
			providers = append(providers, string(id))
		}
	}
	return reg.Resolve(models.ModelSelector{Tags: []string{"flagship"}}, providers)
}

// THE REGRESSION (chat dfd85515). The user's only working provider was Codex;
// Copilot's stored credential was rejected by GitHub. Resolution treated the
// rejected Copilot as servable, and [flagship] sub-agents went to
// claude-5.5-sonnet@copilot — every call a terminal 400 that paused the chat.
func TestWithAccountAvailability_RejectedCredentialNeverServes(t *testing.T) {
	t.Parallel()
	drivers := map[models.DriverID]models.DriverConfig{
		"copilot": cfgFor("copilot"),
		"codex":   cfgFor("codex"),
	}
	build := reporters(map[models.DriverID]*fakeReporter{
		"copilot": {err: errCopilotBadlyFormatted},
		"codex":   {report: codexServes(codexAPIModel(t, "gpt-5.6-sol"), codexAPIModel(t, "gpt-5.5"))},
	})

	available := withAccountAvailability(context.Background(), drivers, build)

	assert.NotContains(t, available.Drivers, models.DriverID("copilot"),
		"a provider that rejected the credential must not be a configured driver")
	assert.Equal(t,
		"GitHub Copilot rejected the saved credential (HTTP 400: bad request: Authorization header is badly formatted)",
		available.Unavailable["copilot"],
		"the reason must name the provider and what it said")
	require.Contains(t, available.Drivers, models.DriverID("codex"))

	resolved, err := resolveFlagship(t, available)
	require.NoError(t, err, "a configured, working Codex must serve [flagship]")
	assert.Equal(t, "codex", resolved.Provider.Driver,
		"[flagship] must resolve to the working provider, got %s@%s", resolved.Definition.ID, resolved.Provider.Driver)
}

// 401 and 403 are refusals too: a revoked token, an account with no seat.
func TestWithAccountAvailability_AuthStatusesAreVerdicts(t *testing.T) {
	t.Parallel()
	for _, status := range []int{400, 401, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			drivers := map[models.DriverID]models.DriverConfig{"copilot": cfgFor("copilot")}
			build := reporters(map[models.DriverID]*fakeReporter{
				"copilot": {err: fmt.Errorf("copilot models request: %w", registry.RejectedCredential(status, "nope"))},
			})
			available := withAccountAvailability(context.Background(), drivers, build)
			assert.NotContains(t, available.Drivers, models.DriverID("copilot"))
			assert.Contains(t, available.Unavailable, models.DriverID("copilot"))
		})
	}
}

// An OUTAGE is not a verdict on the credential: the provider stays servable
// so a catalog blip never strands a chat on an otherwise healthy provider.
func TestWithAccountAvailability_OutageFailsOpen(t *testing.T) {
	t.Parallel()
	for name, err := range map[string]error{
		"timeout":   errors.New("copilot models request failed: context deadline exceeded"),
		"5xx":       errors.New("copilot models request returned status 502: bad gateway"),
		"429":       errors.New("copilot models request returned status 429: slow down"),
		"bad parse": errors.New("failed to parse copilot models response: unexpected EOF"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			drivers := map[models.DriverID]models.DriverConfig{"copilot": cfgFor("copilot")}
			build := reporters(map[models.DriverID]*fakeReporter{"copilot": {err: err}})
			available := withAccountAvailability(context.Background(), drivers, build)
			assert.Contains(t, available.Drivers, models.DriverID("copilot"), "an outage must fail open")
			assert.Empty(t, available.Unavailable)
		})
	}
}

// The picker agrees with resolution: a rejected provider's models are not
// offered (they used to fall back to the static list, "fail-open").
func TestAggregateAvailableModels_RejectedCredentialIsOmitted(t *testing.T) {
	t.Parallel()
	available := models.AvailableDrivers{Drivers: map[models.DriverID]models.DriverConfig{
		"copilot": cfgFor("copilot"),
	}}
	build := reporters(map[models.DriverID]*fakeReporter{"copilot": {err: errCopilotBadlyFormatted}})

	got, err := aggregateAvailableModels(context.Background(), available, build)
	require.NoError(t, err)
	assert.Empty(t, got, "a provider that rejected the credential must offer no models")
}

// A model pinned to a provider the account cannot use fails as a typed
// no-servable-provider error, which the runtime treats as terminal.
func TestWithAccountAvailability_PinnedToRejectedProviderIsNoServableProvider(t *testing.T) {
	t.Parallel()
	drivers := map[models.DriverID]models.DriverConfig{
		"copilot": cfgFor("copilot"),
		"codex":   cfgFor("codex"),
	}
	build := reporters(map[models.DriverID]*fakeReporter{"copilot": {err: errCopilotBadlyFormatted}})
	available := withAccountAvailability(context.Background(), drivers, build)

	reg := models.MustGetRegistry().WithAvailability(available.Availability)
	_, err := reg.Resolve(models.ModelSelector{ID: "claude-5.5-sonnet@copilot"}, []string{"codex"})
	require.Error(t, err)
	assert.ErrorIs(t, err, drivererrors.ErrNoServableProvider)
	var pinned *models.ProviderUnavailableError
	require.ErrorAs(t, err, &pinned)
	assert.Contains(t, pinned.Explain(available.Unavailable), "GitHub Copilot rejected the saved credential")
}
