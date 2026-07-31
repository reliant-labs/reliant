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
		{name: "unknown window falls back to global default", contextWindow: 0, want: GlobalDefaultCompactionThreshold},
		{name: "negative window falls back to global default", contextWindow: -1, want: GlobalDefaultCompactionThreshold},
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
		{name: "nil definition falls back", def: nil, want: GlobalDefaultCompactionThreshold},
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
			want: GlobalDefaultCompactionThreshold,
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

// TestGPT5CodexProviderWindowRegistered pins the real fix in models.yaml: every
// codex-served GPT-5.x model whose platform window exceeds the codex backend cap
// declares the 272k per-provider override, so an @codex session's compaction
// threshold derives from 272k rather than the (unreachable) platform window.
func TestGPT5CodexProviderWindowRegistered(t *testing.T) {
	const codexWindow = 272_000
	registry, err := GetRegistry()
	if err != nil {
		t.Fatalf("failed to load registry: %v", err)
	}
	for _, id := range []string{"gpt-5.5", "gpt-5.4", "gpt-5.3-codex", "gpt-5.2-codex", "gpt-5.4-mini"} {
		def, ok := registry.GetDefinition(id)
		if !ok {
			t.Errorf("model %q not found in registry", id)
			continue
		}
		if got := EffectiveContextWindow(def, "codex"); got != codexWindow {
			t.Errorf("model %q via codex: effective window = %d, want %d", id, got, codexWindow)
		}
		if got := CompactionThresholdForProvider(def, "codex"); got != int(codexWindow*CompactionThresholdFraction) {
			t.Errorf("model %q via codex: threshold = %d, want %d", id, got, int(codexWindow*CompactionThresholdFraction))
		}
	}
}

// TestCompactionThresholdForModel verifies the registry-backed read path used by
// the UI context-usage denominator: unknown/empty IDs fall back, and every
// registered model resolves to 0.85 × its real context window (no per-model YAML
// magic numbers remain).
func TestCompactionThresholdForModel(t *testing.T) {
	// Empty and unknown models fall back to the global default.
	if got := CompactionThresholdForModel(""); got != GlobalDefaultCompactionThreshold {
		t.Errorf("empty model: got %d, want %d", got, GlobalDefaultCompactionThreshold)
	}
	if got := CompactionThresholdForModel("no-such-model-xyz"); got != GlobalDefaultCompactionThreshold {
		t.Errorf("unknown model: got %d, want %d", got, GlobalDefaultCompactionThreshold)
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

	// Spot-check a known 1M-window flagship model derives to 850k.
	if def, ok := registry.GetDefinition("claude-4.8-opus"); ok {
		if def.DefaultCompactionThreshold != nil {
			t.Errorf("claude-4.8-opus should not declare a per-model default_compaction_threshold; got %d", *def.DefaultCompactionThreshold)
		}
		if got := CompactionThresholdForModel("claude-4.8-opus"); got != 850_000 {
			t.Errorf("claude-4.8-opus (1M window): got %d, want 850000", got)
		}
	}
}
