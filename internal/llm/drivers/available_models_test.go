// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// fakeClient satisfies registry.Client but NOT registry.ModelLister, so
// AvailableModelsFor falls back to the static registry list for it (a static
// provider).
type fakeClient struct{ name string }

func (f *fakeClient) Name() string { return f.name }
func (f *fakeClient) SendMessages(context.Context, []string, []message.Message, []tools.Tool) (*llm.DriverResponse, error) {
	return nil, nil
}
func (f *fakeClient) StreamResponse(context.Context, []string, []message.Message, []tools.Tool) <-chan llm.DriverEvent {
	return nil
}
func (f *fakeClient) ValidateKey(context.Context) error { return nil }

// fakeLister additionally implements registry.ModelLister, so it stands in for a
// dynamic provider (like Copilot) returning its own per-account list.
type fakeLister struct {
	fakeClient
	list []models.ModelInfo
}

func (f *fakeLister) GetAvailableModels(context.Context) ([]models.ModelInfo, error) {
	return f.list, nil
}

func cfgFor(id models.DriverID) models.DriverConfig {
	return models.DriverConfig{DriverID: id, APIKey: "test-key", Enabled: true}
}

func TestAggregateAvailableModels_StaticProviderReturnsRegistryList(t *testing.T) {
	reg := models.MustGetRegistry()
	want := reg.ModelsForDriver("anthropic")
	if len(want) == 0 {
		t.Fatal("precondition: expected anthropic to serve registry models")
	}

	available := models.AvailableDrivers{Drivers: map[models.DriverID]models.DriverConfig{
		"anthropic": cfgFor("anthropic"),
	}}

	// Static client: not a ModelLister, so AvailableModelsFor must return the
	// registry list for the family.
	build := func(id models.DriverID, _ models.DriverConfig) (registry.Client, error) {
		return &fakeClient{name: string(id)}, nil
	}

	got, err := aggregateAvailableModels(context.Background(), available, build)
	if err != nil {
		t.Fatalf("aggregateAvailableModels: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("static provider: got %d models, want %d", len(got), len(want))
	}
	for _, mi := range got {
		if mi.DriverID != "anthropic" {
			t.Errorf("unexpected driver %q", mi.DriverID)
		}
		if !mi.Enabled {
			t.Errorf("static provider model %q should be enabled", mi.ID)
		}
	}
}

func TestAggregateAvailableModels_DynamicProviderReturnsItsList(t *testing.T) {
	dynamicList := []models.ModelInfo{
		{ID: "model-a", DriverID: "copilot", DisplayName: "Model A", Enabled: true},
		{ID: "model-b", DriverID: "copilot", DisplayName: "Model B", Enabled: false}, // disabled by account policy
	}

	available := models.AvailableDrivers{Drivers: map[models.DriverID]models.DriverConfig{
		"copilot": cfgFor("copilot"),
	}}

	build := func(_ models.DriverID, _ models.DriverConfig) (registry.Client, error) {
		return &fakeLister{fakeClient: fakeClient{name: "copilot"}, list: dynamicList}, nil
	}

	got, err := aggregateAvailableModels(context.Background(), available, build)
	if err != nil {
		t.Fatalf("aggregateAvailableModels: %v", err)
	}
	if len(got) != len(dynamicList) {
		t.Fatalf("dynamic provider: got %d models, want %d", len(got), len(dynamicList))
	}
	byID := map[string]models.ModelInfo{}
	for _, mi := range got {
		byID[mi.ID] = mi
	}
	if !byID["model-a"].Enabled {
		t.Error("model-a should be enabled")
	}
	if byID["model-b"].Enabled {
		t.Error("model-b should be disabled per the dynamic provider's list")
	}
}

func TestAggregateAvailableModels_FailOpenOnError(t *testing.T) {
	reg := models.MustGetRegistry()
	want := reg.ModelsForDriver("anthropic")
	if len(want) == 0 {
		t.Fatal("precondition: expected anthropic to serve registry models")
	}

	available := models.AvailableDrivers{Drivers: map[models.DriverID]models.DriverConfig{
		"anthropic": cfgFor("anthropic"),
	}}

	// Builder errors for this provider; the aggregator must fail open and fall
	// back to the static registry list rather than dropping the provider.
	build := func(_ models.DriverID, _ models.DriverConfig) (registry.Client, error) {
		return nil, errors.New("boom: cannot build client")
	}

	got, err := aggregateAvailableModels(context.Background(), available, build)
	if err != nil {
		t.Fatalf("aggregateAvailableModels should not error (fail-open): %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("fail-open: got %d models, want static list of %d", len(got), len(want))
	}
}
