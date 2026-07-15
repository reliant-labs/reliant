package models

import "testing"

func TestCompactionThresholdForModel(t *testing.T) {
	// Empty and unknown models fall back to the global default.
	if got := CompactionThresholdForModel(""); got != GlobalDefaultCompactionThreshold {
		t.Errorf("empty model: got %d, want %d", got, GlobalDefaultCompactionThreshold)
	}
	if got := CompactionThresholdForModel("no-such-model-xyz"); got != GlobalDefaultCompactionThreshold {
		t.Errorf("unknown model: got %d, want %d", got, GlobalDefaultCompactionThreshold)
	}

	// A known model returns its declared default_compaction_threshold rather than
	// the global default. Every model in the registry declares one, so pick the
	// first and assert consistency with the registry.
	registry, err := GetRegistry()
	if err != nil {
		t.Fatalf("failed to load registry: %v", err)
	}
	all := registry.ListAll()
	if len(all) == 0 {
		t.Fatal("registry has no models")
	}

	var checked int
	for i := range all {
		def := all[i]
		if def.DefaultCompactionThreshold == nil {
			continue
		}
		want := *def.DefaultCompactionThreshold
		if got := CompactionThresholdForModel(def.ID); got != want {
			t.Errorf("model %q: got %d, want %d", def.ID, got, want)
		}
		checked++
		if checked >= 5 {
			break
		}
	}
	if checked == 0 {
		t.Error("expected at least one model to declare default_compaction_threshold")
	}
}
