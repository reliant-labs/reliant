// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// countForgeFrameworkSkills counts skills surfaced under forge's synthetic
// "forge/" namespace — the ones that exist only for a project with a forge.yaml.
func countForgeFrameworkSkills(projectPath string) int {
	skills, _ := indexSkills(projectPath)
	n := 0
	for _, s := range skills {
		if strings.HasPrefix(s.GetSkillPath(), "forge/") {
			n++
		}
	}
	return n
}

// TestIndexSkills_ForgeYamlAppearingInvalidatesTheCache guards the one cache
// transition a generation workflow makes on purpose.
//
// forge's framework skills are surfaced only for a project that has a
// forge.yaml (internal/skills/catalog/forge.go drops every `emit: forge` skill
// without one). forge-one-shot's FIRST phase creates that file — and the same
// prompt then tells the agent to work from `forge/db`, `forge/proto`,
// `forge/architecture` and three more. The index is memoized per project path
// with a 60s TTL, so on a plain TTL the daemon keeps serving the pre-project
// catalog across exactly that transition: every one of those skill loads 404s
// and the agent falls back to memory. Measured: run b25c1f1d's scaffold agent
// loaded none of them.
//
// The guard drives the real cache: index a directory with no forge.yaml, create
// the file, and require the very next index to see the forge skills.
func TestIndexSkills_ForgeYamlAppearingInvalidatesTheCache(t *testing.T) {
	projectPath := t.TempDir()
	t.Cleanup(func() {
		skillsIndexMu.Lock()
		delete(skillsIndexCache, projectPath)
		skillsIndexMu.Unlock()
	})

	if before := countForgeFrameworkSkills(projectPath); before != 0 {
		t.Fatalf("a directory with no forge.yaml must surface no forge/* skills, got %d", before)
	}

	// The project is born.
	if err := os.WriteFile(filepath.Join(projectPath, "forge.yaml"), []byte("name: testproj\n"), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}

	if after := countForgeFrameworkSkills(projectPath); after == 0 {
		t.Fatal("the index was served from cache after forge.yaml appeared: the agent that " +
			"just ran `forge project new` gets a catalog with no forge/* skills in it, so " +
			"every skill load in the phase that needs them 404s for up to a TTL")
	}
}
