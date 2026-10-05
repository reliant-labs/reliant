package models

import "testing"

// Adaptive-thinking Claude models 400 if temperature is sent at all, so the
// catalog must mark every one of them temperature_mode: omit.
func TestAdaptiveThinkingModelsOmitTemperature(t *testing.T) {
	reg := MustGetRegistry()
	checked := 0
	for _, def := range reg.ListAll() {
		id := def.ID
		if def.DriverSettings == nil || def.DriverSettings.ThinkingMode != "adaptive" {
			continue
		}
		checked++
		if def.DriverSettings.TemperatureMode != "omit" {
			t.Errorf("model %q has thinking_mode: adaptive but temperature_mode=%q; want omit", id, def.DriverSettings.TemperatureMode)
		}
	}
	if checked == 0 {
		t.Fatal("no adaptive-thinking models found; test is vacuous")
	}
}

func TestSupportsTemperature(t *testing.T) {
	anyMode := &ModelDefinition{ID: "m-any", Providers: []ProviderMapping{
		{Driver: "openrouter", APIModel: "x/m"},
		{Driver: "anthropic", APIModel: "claude-x"},
		{Driver: "copilot", APIModel: "claude-x"},
	}}
	omit := &ModelDefinition{ID: "m-omit", DriverSettings: &DriverSettings{TemperatureMode: "omit"},
		Providers: []ProviderMapping{{Driver: "openrouter", APIModel: "x/m"}}}
	copilotGPT := &ModelDefinition{ID: "g", Providers: []ProviderMapping{{Driver: "copilot", APIModel: "gpt-5"}}}

	cases := []struct {
		name   string
		def    *ModelDefinition
		driver string
		want   bool
	}{
		{"any via openrouter", anyMode, "openrouter", true},
		{"omit model", omit, "openrouter", false},
		{"anthropic driver ignores", anyMode, "anthropic", false},
		{"claude oauth family ignores", anyMode, "claude", false},
		{"copilot serving claude ignores", anyMode, "copilot", false},
		{"copilot serving gpt honors", copilotGPT, "copilot", true},
		{"nil def", nil, "openrouter", false},
	}
	for _, c := range cases {
		if got := SupportsTemperature(c.def, c.driver); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
