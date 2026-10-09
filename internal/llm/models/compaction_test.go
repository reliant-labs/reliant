package models

import "testing"

// TestDeriveCompactionThreshold pins the core derivation: 0.85 × real window,
// with the global fallback when the window is unknown.
func TestDeriveCompactionThreshold(t *testing.T) {
	tests := []struct {
		name          string
		contextWindow int
		want          int
	}{
		{name: "1M window derives 850k", contextWindow: 1_000_000, want: 850_000},
		{name: "200k window derives 170k", contextWindow: 200_000, want: 170_000},
		{name: "400k window derives 340k", contextWindow: 400_000, want: 340_000},
		{name: "unknown window falls back to global default", contextWindow: 0, want: UnknownModelCompactionFloor},
		{name: "negative window falls back to global default", contextWindow: -1, want: UnknownModelCompactionFloor},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeriveCompactionThreshold(tc.contextWindow); got != tc.want {
				t.Errorf("DeriveCompactionThreshold(%d) = %d, want %d", tc.contextWindow, got, tc.want)
			}
		})
	}
}

// TestCompactionThresholdForDefinition verifies the resolution order at the
// definition layer: an explicit per-model override wins; otherwise the value is
// derived from the real window; a nil definition falls back to the global default.
func TestCompactionThresholdForDefinition(t *testing.T) {
	override := 12345
	tests := []struct {
		name string
		def  *ModelDefinition
		want int
	}{
		{name: "nil definition falls back", def: nil, want: UnknownModelCompactionFloor},
		{
			name: "derives from 1M window",
			def:  &ModelDefinition{Capabilities: ModelCapabilities{MaxContextWindow: 1_000_000}},
			want: 850_000,
		},
		{
			name: "derives from 200k window",
			def:  &ModelDefinition{Capabilities: ModelCapabilities{MaxContextWindow: 200_000}},
			want: 170_000,
		},
		{
			name: "explicit per-model override wins over derivation",
			def:  &ModelDefinition{Capabilities: ModelCapabilities{MaxContextWindow: 1_000_000}, DefaultCompactionThreshold: &override},
			want: override,
		},
		{
			name: "no window falls back to global default",
			def:  &ModelDefinition{Capabilities: ModelCapabilities{MaxContextWindow: 0}},
			want: UnknownModelCompactionFloor,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompactionThresholdForDefinition(tc.def); got != tc.want {
				t.Errorf("CompactionThresholdForDefinition = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestEffectiveContextWindow verifies the per-provider window override: a
// provider that serves a smaller window than the model-wide capability shrinks
// the effective window; a larger (or absent) override leaves the model-wide
// window; an empty driver ignores overrides.
func TestEffectiveContextWindow(t *testing.T) {
	def := &ModelDefinition{
		Capabilities: ModelCapabilities{MaxContextWindow: 1_050_000},
		Providers: []ProviderMapping{
			{Driver: "codex", APIModel: "gpt-5.5", MaxContextWindow: 272_000},
			{Driver: "openai", APIModel: "gpt-5.5"},
			{Driver: "bigger", APIModel: "gpt-5.5", MaxContextWindow: 2_000_000},
		},
	}
	tests := []struct {
		name           string
		def            *ModelDefinition
		providerDriver string
		want           int
	}{
		{name: "nil definition", def: nil, providerDriver: "codex", want: 0},
		{name: "empty driver uses model-wide window", def: def, providerDriver: "", want: 1_050_000},
		{name: "codex shrinks to provider window", def: def, providerDriver: "codex", want: 272_000},
		{name: "provider without override uses model-wide window", def: def, providerDriver: "openai", want: 1_050_000},
		{name: "larger override is ignored (model-wide is the ceiling)", def: def, providerDriver: "bigger", want: 1_050_000},
		{name: "unknown driver uses model-wide window", def: def, providerDriver: "nope", want: 1_050_000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveContextWindow(tc.def, tc.providerDriver); got != tc.want {
				t.Errorf("EffectiveContextWindow = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestCompactionThresholdForProvider verifies the provider-aware trigger: the
// same model compacts sooner when reached via a small-window provider. This is
// the gpt-5.5@codex regression: the platform window derives 892.5k but the codex
// backend must compact at 0.85 × 272k so it never overflows.
func TestCompactionThresholdForProvider(t *testing.T) {
	def := &ModelDefinition{
		Capabilities: ModelCapabilities{MaxContextWindow: 1_050_000},
		Providers: []ProviderMapping{
			{Driver: "codex", APIModel: "gpt-5.5", MaxContextWindow: 272_000},
			{Driver: "openai", APIModel: "gpt-5.5"},
		},
	}
	if got := CompactionThresholdForProvider(def, "openai"); got != int(1_050_000*CompactionThresholdFraction) {
		t.Errorf("openai provider: got %d, want %d", got, int(1_050_000*CompactionThresholdFraction))
	}
	if got := CompactionThresholdForProvider(def, "codex"); got != int(272_000*CompactionThresholdFraction) {
		t.Errorf("codex provider: got %d, want %d", got, int(272_000*CompactionThresholdFraction))
	}
	// An explicit per-model default still wins over provider derivation.
	override := 12345
	def.DefaultCompactionThreshold = &override
	if got := CompactionThresholdForProvider(def, "codex"); got != override {
		t.Errorf("explicit override: got %d, want %d", got, override)
	}
}

// TestGPT5CodexProviderWindowRegistered pins gpt-5.5's codex window in
// models.yaml: /codex/models advertises 272000 for it, far below the platform's
// 1,050,000, so its codex window is declared as 272000 + 128000 max output and
// the derived prompt ceiling is the 272000 @codex has always run at — even
// when /codex/models is unreachable and no advertised limit is applied.
//
// The list covers only models the ChatGPT-account backend actually serves.
// gpt-5.4, gpt-5.4-mini, gpt-5.3-codex and gpt-5.2-codex used to be here, but
// the backend refuses them for a ChatGPT account, so their codex provider
// mapping was removed and there is no codex window to pin.
func TestGPT5CodexProviderWindowRegistered(t *testing.T) {
	const codexCeiling = 272_000
	registry, err := GetRegistry()
	if err != nil {
		t.Fatalf("failed to load registry: %v", err)
	}
	for _, id := range []string{"gpt-5.5"} {
		def, ok := registry.GetDefinition(id)
		if !ok {
			t.Errorf("model %q not found in registry", id)
			continue
		}
		if got := ProviderPromptCeiling(def, "codex"); got != codexCeiling {
			t.Errorf("model %q via codex: prompt ceiling = %d, want %d", id, got, codexCeiling)
		}
		if got := CompactionThresholdForProvider(def, "codex"); got != int(codexCeiling*CompactionThresholdFraction) {
			t.Errorf("model %q via codex: threshold = %d, want %d", id, got, int(codexCeiling*CompactionThresholdFraction))
		}
	}
}

func TestPromptCeiling(t *testing.T) {
	tests := []struct {
		name              string
		window, maxOutput int
		want              int
	}{
		{name: "GPT-5.6: 1,050,000 window, 128,000 output", window: 1_050_000, maxOutput: 128_000, want: 922_000},
		{name: "unknown window", window: 0, maxOutput: 128_000, want: 0},
		{name: "no declared output reserves nothing", window: 200_000, maxOutput: 0, want: 200_000},
		{name: "output that would fill the window reserves nothing", window: 8_192, maxOutput: 8_192, want: 8_192},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := PromptCeiling(tc.window, tc.maxOutput); got != tc.want {
				t.Errorf("PromptCeiling(%d, %d) = %d, want %d", tc.window, tc.maxOutput, got, tc.want)
			}
		})
	}
}

// TestCompactionThresholdForProvider_CodexGPTWindows: the GPT-5.6 family and
// GPT-6 Astra publish a 1,050,000-token context window with 128,000 max output
// (developers.openai.com/api/docs/models/<id>.md), so the catalog prompt ceiling
// is 922,000 and an unpinned codex chat compacts at 85% of it. When the
// account's /codex/models advertises a lower limit (872,000), that applies.
func TestCompactionThresholdForProvider_CodexGPTWindows(t *testing.T) {
	reg := MustGetRegistry()
	live := reg.WithAvailability(func(driver, _ string) ModelAvailability {
		if driver == "codex" {
			return ModelAvailability{ContextWindow: 872_000}
		}
		return ModelAvailability{}
	})
	for _, id := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-6-astra"} {
		def, ok := reg.GetDefinition(id)
		if !ok {
			t.Fatalf("model %q not in registry", id)
		}
		if got := CompactionThresholdForProvider(def, "codex"); got != 783_700 {
			t.Errorf("%s@codex from the catalog: threshold = %d, want 783700 (0.85 × 922k)", id, got)
		}
		resolved, err := live.Resolve(ModelSelector{ID: id + "@codex"}, []string{"codex"})
		if err != nil {
			t.Fatalf("resolve %s@codex: %v", id, err)
		}
		if got := CompactionThresholdForProvider(&resolved.Definition, "codex"); got != 741_200 {
			t.Errorf("%s@codex with /models 872k: threshold = %d, want 741200 (0.85 × 872k)", id, got)
		}
	}
}

// TestCompactionThresholdForModel verifies the registry-backed read path used by
// the UI context-usage denominator: unknown/empty IDs fall back, and every
// registered model resolves to 0.85 × its real context window (no per-model YAML
// magic numbers remain).
func TestCompactionThresholdForModel(t *testing.T) {
	// Empty and unknown models fall back to the global default.
	if got := CompactionThresholdForModel(""); got != UnknownModelCompactionFloor {
		t.Errorf("empty model: got %d, want %d", got, UnknownModelCompactionFloor)
	}
	if got := CompactionThresholdForModel("no-such-model-xyz"); got != UnknownModelCompactionFloor {
		t.Errorf("unknown model: got %d, want %d", got, UnknownModelCompactionFloor)
	}

	registry, err := GetRegistry()
	if err != nil {
		t.Fatalf("failed to load registry: %v", err)
	}
	all := registry.ListAll()
	if len(all) == 0 {
		t.Fatal("registry has no models")
	}

	// Every registered model derives its threshold from its real context window.
	for i := range all {
		def := all[i]
		want := CompactionThresholdForDefinition(&all[i])
		if got := CompactionThresholdForModel(def.ID); got != want {
			t.Errorf("model %q: got %d, want %d (derived from window %d)",
				def.ID, got, want, def.Capabilities.MaxContextWindow)
		}
	}

	// Spot-check a known 1M-window flagship model: 0.85 × (1,000,000 window −
	// 64,000 max output).
	if def, ok := registry.GetDefinition("claude-4.8-opus"); ok {
		if def.DefaultCompactionThreshold != nil {
			t.Errorf("claude-4.8-opus should not declare a per-model default_compaction_threshold; got %d", *def.DefaultCompactionThreshold)
		}
		if got := CompactionThresholdForModel("claude-4.8-opus"); got != 795_600 {
			t.Errorf("claude-4.8-opus (1M window, 64k output): got %d, want 795600", got)
		}
	}
}

func TestCompactionThresholdCeiling(t *testing.T) {
	override := func(v int) *ModelDefinition {
		return &ModelDefinition{DefaultCompactionThreshold: &v}
	}
	tests := []struct {
		name   string
		def    *ModelDefinition
		window int
		want   int
	}{
		{name: "unknown window has no ceiling", def: nil, window: 0, want: 0},
		{name: "272k window (gpt-5.6 on codex)", def: &ModelDefinition{}, window: 272_000, want: 231_200},
		{name: "1M window", def: nil, window: 1_000_000, want: 850_000},
		{name: "higher per-model default raises the ceiling", def: override(950_000), window: 1_000_000, want: 950_000},
		{name: "lower per-model default does not lower it", def: override(100_000), window: 1_000_000, want: 850_000},
		{name: "a per-model default past the window stops at the window", def: override(2_000_000), window: 1_000_000, want: 1_000_000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompactionThresholdCeiling(tc.def, tc.window); got != tc.want {
				t.Errorf("CompactionThresholdCeiling = %d, want %d", got, tc.want)
			}
		})
	}
}
