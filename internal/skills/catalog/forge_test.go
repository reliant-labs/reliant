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

// isolateHome points HOME at a fresh temp dir for the duration of the test
// so user-installed ~/.forge/skills/ (or other home-scoped skill sources)
// don't leak into discovery. Discover now always calls forgecli.ListSkills
// regardless of forge.yaml, so dev-machine state under HOME can otherwise
// silently change assertions about which skills surface.
func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
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

// TestDiscoverForgeSkills_NoForgeYaml verifies the dual-stream contract:
// without forge.yaml, framework skills (and the synthesized "forge"
// parent) are NOT surfaced, but general/both forge skills still are —
// they apply to any project. Reliant pulls them in unconditionally so
// methodology like testing-methodology / code-review keeps working
// outside forge projects, where it used to live as a reliant builtin.
func TestDiscoverForgeSkills_NoForgeYaml(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	defs := discoverForgeSkills(dir, "", false)
	require.NotEmpty(t, defs, "general forge skills should surface even without forge.yaml")

	for _, d := range defs {
		require.NotEqual(t, forgeNamespace, d.SkillPath,
			"forge framework parent must NOT surface without forge.yaml, got %q", d.SkillPath)
		require.False(t, strings.HasPrefix(d.SkillPath, forgeNamespace+"/"),
			"forge/* framework sub-skills must NOT surface without forge.yaml, got %q", d.SkillPath)
	}

	// At least one well-known general skill should be present so the
	// guard above isn't passing by enumerating zero skills.
	var sawGeneral bool
	for _, d := range defs {
		if d.SkillPath == "testing-methodology" {
			sawGeneral = true
		}
	}
	require.True(t, sawGeneral, "expected general skill testing-methodology to surface unconditionally")
}

// TestDiscoverForgeSkills_EmitBothSurfacesAtBarePath verifies that
// dual-audience skills (Emit "both", like `debug`) surface at the bare
// path "debug" — NOT under the "forge/" namespace prefix — regardless
// of whether forge.yaml is present. That bare-path placement is what
// makes preset references like `recommended_skills: [debug]` resolve
// in any project, while the forge framework parent + "forge/*" children
// stay gated on forge.yaml.
func TestDiscoverForgeSkills_EmitBothSurfacesAtBarePath(t *testing.T) {
	isolateHome(t)
	cases := []struct {
		name       string
		withForge  bool
	}{
		{"without forge.yaml", false},
		{"with forge.yaml", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.withForge {
				ensureForgeYaml(t, dir)
			}
			defs := discoverForgeSkills(dir, "", false)

			var sawDebugAtBarePath, sawForgePrefixedDebug bool
			for _, d := range defs {
				switch d.SkillPath {
				case "debug":
					sawDebugAtBarePath = true
				case "forge/debug":
					sawForgePrefixedDebug = true
				}
			}
			require.True(t, sawDebugAtBarePath,
				"emit:both skill `debug` should surface at bare path %q regardless of forge.yaml", "debug")
			require.False(t, sawForgePrefixedDebug,
				"emit:both skill `debug` must NOT surface under the forge/ namespace — that's reserved for emit:forge skills")
		})
	}
}

// TestLoadFullDefinition_EmitBothRendersByAudience verifies the end-to-end
// audience-aware body rendering: a `debug` skill (Emit "both") loaded in a
// non-forge project has its `@forge-only` sections stripped, while the
// same skill loaded in a forge project carries the full body including
// the framework-specific tooling. This is the whole point of the
// dual-audience model — without it, general consumers would see leaked
// references to `forge` CLI commands and other framework specifics.
func TestLoadFullDefinition_EmitBothRendersByAudience(t *testing.T) {
	isolateHome(t)

	findDebug := func(t *testing.T, defs []Definition) Definition {
		t.Helper()
		for _, d := range defs {
			if d.SkillPath == "debug" {
				return d
			}
		}
		t.Fatalf("expected to find emit:both skill `debug` at bare path")
		return Definition{}
	}

	t.Run("without forge.yaml: @forge-only stripped", func(t *testing.T) {
		dir := t.TempDir()
		defs := discoverForgeSkills(dir, "", true)
		debug := findDebug(t, defs)

		require.NotEmpty(t, debug.Body, "debug skill body should be hydrated")
		require.NotContains(t, debug.Body, "@forge-only",
			"@forge-only block markers must be absent from a general-audience render")
		require.NotContains(t, debug.Body, "Forge Debug Tools",
			"forge-specific section header must be stripped for general audience")
		require.NotContains(t, debug.Body, "forge debug start",
			"forge CLI commands must be stripped for general audience")
		// The general methodology must survive the strip.
		require.Contains(t, debug.Body, "Triage First",
			"general methodology section must remain in the stripped body")
		require.Contains(t, debug.Body, "Parallel Investigation",
			"general methodology section must remain in the stripped body")
	})

	t.Run("with forge.yaml: full body retained", func(t *testing.T) {
		dir := t.TempDir()
		ensureForgeYaml(t, dir)
		defs := discoverForgeSkills(dir, "", true)
		debug := findDebug(t, defs)

		require.NotEmpty(t, debug.Body, "debug skill body should be hydrated")
		require.Contains(t, debug.Body, "Forge Debug Tools",
			"forge audience must see the forge-specific section header")
		require.Contains(t, debug.Body, "forge debug start",
			"forge audience must see the forge-specific CLI commands")
		require.Contains(t, debug.Body, "Triage First",
			"the methodology section must be present in either audience")
	})
}

func TestDiscoverForgeSkills_SynthesizesParentAndChildren(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	ensureForgeYaml(t, dir)
	writeForgeProjectSkill(t, dir, "db", "Database conventions")
	writeForgeProjectSkill(t, dir, "proto", "Proto rules")

	defs := discoverForgeSkills(dir, "", false)
	require.GreaterOrEqual(t, len(defs), 3, "expected parent + at least the two project skills")

	// Find the synthesized framework parent. Its position depends on
	// general skills being interleaved with framework children, so we
	// can't index-assert it like before.
	var parent *Definition
	for i := range defs {
		if defs[i].SkillPath == forgeNamespace {
			parent = &defs[i]
		}
	}
	require.NotNil(t, parent, "expected synthesized forge framework parent")
	require.True(t, parent.HasChildren)
	require.Equal(t, skillscore.ScopeForge, parent.Scope)
	require.Contains(t, parent.Body, "Forge skills")
	require.Contains(t, parent.Body, "forge/db")
	require.Contains(t, parent.Body, "forge/proto")

	// All defs carry ScopeForge; framework children sit under "forge/".
	skillPaths := make([]string, 0, len(defs))
	for _, d := range defs {
		skillPaths = append(skillPaths, d.SkillPath)
		require.Equal(t, skillscore.ScopeForge, d.Scope)
	}
	require.Contains(t, skillPaths, "forge/db")
	require.Contains(t, skillPaths, "forge/proto")
}

func TestDiscoverForgeSkills_LoadFullDefinitionsHydratesBodies(t *testing.T) {
	isolateHome(t)
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
	isolateHome(t)
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
	isolateHome(t)
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
	isolateHome(t)
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

// TestDiscover_SkipsForgeFrameworkWhenNoForgeYaml verifies the new
// dual-stream contract at the Discover() level: without forge.yaml,
// the framework parent and any "forge/*" sub-skills must not appear,
// but ScopeForge general skills DO surface (at their bare path) so
// methodology applies in non-forge projects too.
func TestDiscover_SkipsForgeFrameworkWhenNoForgeYaml(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	snap := Discover(DiscoverInput{ProjectPath: dir})
	for _, d := range snap.Definitions {
		if d.Scope != skillscore.ScopeForge {
			continue
		}
		require.NotEqual(t, forgeNamespace, d.SkillPath,
			"forge framework parent must NOT surface without forge.yaml")
		require.False(t, strings.HasPrefix(d.SkillPath, forgeNamespace+"/"),
			"forge/* framework sub-skills must NOT surface without forge.yaml, got %q", d.SkillPath)
	}
}

func TestForgeSkillsForInput_PrefixesNestedRepoSources(t *testing.T) {
	isolateHome(t)
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