// Copyright (c) 2025 Reliant Labs
package preset

import (
	"fmt"
	"testing"
)

func TestLoadAllPresets(t *testing.T) {
	loader := NewLoaderForProject("/tmp")
	presets, err := loader.LoadAll()
	if err != nil {
		t.Fatalf("Error: %v", err)
	}
	fmt.Printf("Loaded %d presets:\n", len(presets))
	for _, p := range presets {
		fmt.Printf("  - %s (source: %s, tag: %s)\n", p.Name, p.Source, p.Tag)
	}
	if len(presets) == 0 {
		t.Error("Expected some builtin presets but got none")
	}
}
