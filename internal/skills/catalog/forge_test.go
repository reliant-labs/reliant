// Copyright (c) 2025 Reliant Labs
package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	skillscore "github.com/reliant-labs/reliant/internal/skills/core"
	"github.com/stretchr/testify/require"
)

// writeForgeProjectSkill creates a minimal .forge/skills/<name>/SKILL.md under
// projectRoot. forge.yaml is the trigger for [discoverForgeSkills]; tests rely
// on this helper to manufacture a forge project on disk.
func writeForgeProjectSkill(t *testing.T, projectRoot, name, description string) {
	t.Helper()
	dir := filepath.Join(projectRoot, ".forge", "skills", name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n# " + name + "\nbody content for " + name
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644))
}

func ensureForgeYaml(t *testing.T, projectRoot string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, "forge.yaml"), []byte("name: testproj\n"), 0o644))
}

func TestParseForgeSyntheticPath_RoundTrip(t *testing.T) {
	base := "/abs/path with spaces/proj"
	skill := "api/handlers"
	encoded := forgeSyntheticPath(base, skill)

	gotBase, gotSkill, ok := parseForgeSyntheticPath(encoded)
	require.True(t, ok)
	require.Equal(t, base, gotBase)
	require.Equal(t, skill, gotSkill)
}

func TestParseForgeSyntheticPath_RejectsForeign(t *testing.T) {
	_, _, ok := parseForgeSyntheticPath("/regular/disk/path/SKILL.md")
	require.False(t, ok)

	_, _, ok = parseForgeSyntheticPath("forge://no-separator-here")
	require.False(t, ok)
}

func TestDiscoverForgeSkills_NoForgeYaml(t *testing.T) {
	dir := t.TempDir()
	require.Empty(t, discoverForgeSkills(dir, "", false))
}

func TestDiscoverForgeSkills_SynthesizesParentAndChildren(t *testing.T) {
	dir := t.TempDir()
	ensureForgeYaml(t, dir)
	writeForgeProjectSkill(t, dir, "db", "Database conventions")
	writeForgeProjectSkill(t, dir, "proto", "Proto rules")

	defs := discoverForgeSkills(dir, "", false)
	require.GreaterOrEqual(t, len(defs), 3, "expected parent + at least the two project skills")

	// First entry is the synthesized parent.
	require.Equal(t, forgeNamespace, defs[0].SkillPath)
	require.True(t, defs[0].HasChildren)
	require.Equal(t, skillscore.ScopeForge, defs[0].Scope)
	require.Contains(t, defs[0].Body, "Forge skills")
	require.Contains(t, defs[0].Body, "forge/db")
	require.Contains(t, defs[0].Body, "forge/proto")

	// Children carry forge/<name> paths and ScopeForge.
	skillPaths := make([]string, 0, len(defs))
	for _, d := range defs {
		skillPaths = append(skillPaths, d.SkillPath)
		require.Equal(t, skillscore.ScopeForge, d.Scope)
	}
	require.Contains(t, skillPaths, "forge/db")
	require.Contains(t, skillPaths, "forge/proto")

	// Children are sub-skills (have "/" in SkillPath).
	for _, d := range defs[1:] {
		require.True(t, strings.Contains(d.SkillPath, "/"), "child %q should be a sub-skill", d.SkillPath)
	}
}

func TestDiscoverForgeSkills_LoadFullDefinitionsHydratesBodies(t *testing.T) {
	dir := t.TempDir()
	ensureForgeYaml(t, dir)
	writeForgeProjectSkill(t, dir, "db", "Database conventions")

	defs := discoverForgeSkills(dir, "", true)
	require.NotEmpty(t, defs)

	var child *Definition
	for i := range defs {
		if defs[i].SkillPath == "forge/db" {
			child = &defs[i]
			break
		}
	}
	require.NotNil(t, child, "expected forge/db sub-skill")
	require.Contains(t, child.Body, "body content for db", "expected body to be loaded eagerly")
}

func TestLoadFullDefinition_ResolvesForgeSkillLazily(t *testing.T) {
	dir := t.TempDir()
	ensureForgeYaml(t, dir)
	writeForgeProjectSkill(t, dir, "db", "Database conventions")

	defs := discoverForgeSkills(dir, "", false)
	var child Definition
	found := false
	for _, d := range defs {
		if d.SkillPath == "forge/db" {
			child = d
			found = true
			break
		}
	}
	require.True(t, found)
	require.Empty(t, child.Body, "metadata-only discovery should not eager-load")

	full, err := LoadFullDefinition(child)
	require.NoError(t, err)
	require.Contains(t, full.Body, "body content for db")
}

func TestDiscover_IncludesForgeWhenForgeYamlPresent(t *testing.T) {
	dir := t.TempDir()
	ensureForgeYaml(t, dir)
	writeForgeProjectSkill(t, dir, "db", "Database conventions")

	snap := Discover(DiscoverInput{ProjectPath: dir})

	var sawParent bool
	for _, d := range snap.Definitions {
		if d.SkillPath == forgeNamespace && d.Scope == skillscore.ScopeForge {
			sawParent = true
		}
	}
	require.True(t, sawParent, "Discover() should surface the synthesized forge parent at top level")
}

func TestDiscoverAll_IncludesForgeSubSkills(t *testing.T) {
	dir := t.TempDir()
	ensureForgeYaml(t, dir)
	writeForgeProjectSkill(t, dir, "db", "Database conventions")

	snap := DiscoverAll(DiscoverInput{ProjectPath: dir})

	var sawChild bool
	for _, d := range snap.Definitions {
		if d.SkillPath == "forge/db" && d.Scope == skillscore.ScopeForge {
			sawChild = true
		}
	}
	require.True(t, sawChild, "DiscoverAll() should surface forge sub-skills")
}

func TestDiscover_SkipsForgeWhenNoForgeYaml(t *testing.T) {
	dir := t.TempDir()
	snap := Discover(DiscoverInput{ProjectPath: dir})
	for _, d := range snap.Definitions {
		require.NotEqual(t, skillscore.ScopeForge, d.Scope, "no forge.yaml → no forge skills")
	}
}

func TestForgeSkillsForInput_PrefixesNestedRepoSources(t *testing.T) {
	root := t.TempDir()
	apiDir := filepath.Join(root, "api")
	require.NoError(t, os.MkdirAll(apiDir, 0o755))
	ensureForgeYaml(t, apiDir)
	writeForgeProjectSkill(t, apiDir, "db", "Database conventions")

	defs := forgeSkillsForInput(DiscoverInput{ProjectPath: root, RepoSources: []string{"api"}})
	require.NotEmpty(t, defs)

	var sawPrefixed bool
	for _, d := range defs {
		if d.Source == "api" && d.NormalizedKey == "api/forge/db" {
			sawPrefixed = true
		}
	}
	require.True(t, sawPrefixed, "nested-repo forge skill should have NormalizedKey prefixed with the repo source")
}
