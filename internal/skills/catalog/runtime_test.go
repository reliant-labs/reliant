package catalog

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	skillscore "github.com/reliant-labs/reliant/internal/skills/core"
	"github.com/stretchr/testify/require"
)

func TestParseSkillMarkdown(t *testing.T) {
	t.Run("valid skill", func(t *testing.T) {
		s, err := ParseSkillMarkdown("/tmp/explain-code/SKILL.md", skillscore.ScopeProject, []byte(`---
name: explain-code
description: Explain code clearly
license: Apache-2.0
compatibility: claude
metadata:
  author: reliant
allowed-tools: Bash(git:*) Read
---
# Explain code
Use diagrams.
`))
		require.NoError(t, err)
		require.Equal(t, "explain-code", s.Name)
		require.Equal(t, "explain-code", s.NormalizedKey)
		require.Equal(t, "Explain code clearly", s.Description)
		require.Equal(t, "# Explain code\nUse diagrams.", s.Body)
		require.Equal(t, skillscore.SkillFormatClaudeMarkdown, s.Format)
		require.Equal(t, []string{"Bash(git:*)", "Read"}, s.AllowedTools)
	})

	t.Run("frontmatter only parser leaves body empty", func(t *testing.T) {
		s, err := ParseSkillMarkdownFrontmatter("/tmp/explain-code/SKILL.md", skillscore.ScopeProject, []byte(`---
name: explain-code
description: Explain code clearly
---
# Explain code
Use diagrams.
`))
		require.NoError(t, err)
		require.Equal(t, "explain-code", s.Name)
		require.Empty(t, s.Body)
	})

	t.Run("missing frontmatter", func(t *testing.T) {
		_, err := ParseSkillMarkdown("/tmp/example/SKILL.md", skillscore.ScopeProject, []byte("# no frontmatter"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "missing YAML frontmatter")
	})

	t.Run("missing required fields", func(t *testing.T) {
		_, err := ParseSkillMarkdown("/tmp/example/SKILL.md", skillscore.ScopeProject, []byte(`---
name: 
---
body`))
		require.Error(t, err)
		require.Contains(t, err.Error(), "missing required field: name")
	})

	t.Run("unknown frontmatter fields rejected", func(t *testing.T) {
		_, err := ParseSkillMarkdown("/tmp/example/SKILL.md", skillscore.ScopeProject, []byte(`---
name: example
description: desc
extra-field: nope
---
body`))
		require.Error(t, err)
		require.Contains(t, err.Error(), "unexpected fields in frontmatter")
		require.Contains(t, err.Error(), "extra-field")
	})
}

func TestLoadFullDefinition(t *testing.T) {
	project := t.TempDir()
	skillDir := filepath.Join(project, ".reliant", "skills", "loadable")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: loadable
description: test loader
---
full body instructions`), 0o644))

	metaOnly, err := ParseSkillMarkdownFrontmatter(filepath.Join(skillDir, "SKILL.md"), skillscore.ScopeProject, []byte(`---
name: loadable
description: test loader
---
full body instructions`))
	require.NoError(t, err)
	require.Empty(t, metaOnly.Body)

	loaded, err := LoadFullDefinition(metaOnly)
	require.NoError(t, err)
	require.Equal(t, "full body instructions", loaded.Body)
}

func TestValidateSkillCoreFields_NameRules(t *testing.T) {
	tests := []struct {
		name    string
		skill   string
		wantErr string
	}{
		{name: "valid ascii", skill: "pdf-processing"},
		{name: "valid unicode lowercase", skill: "résumé-helper"},
		{name: "uppercase invalid", skill: "PDF-processing", wantErr: "must be lowercase"},
		{name: "leading hyphen invalid", skill: "-pdf", wantErr: "must not start or end with hyphen"},
		{name: "trailing hyphen invalid", skill: "pdf-", wantErr: "must not start or end with hyphen"},
		{name: "consecutive hyphen invalid", skill: "pdf--processing", wantErr: "must not contain consecutive hyphens"},
		{name: "invalid punctuation", skill: "pdf_processing", wantErr: "only lowercase letters, digits, and hyphens"},
		{name: "too long", skill: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", wantErr: "1-64 characters"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSkillCoreFields(tc.skill, "desc", "", map[string]string{"owner": "test"})
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestValidateAgentSkillMarkdownFrontmatter_ParentDirMustMatch(t *testing.T) {
	err := ValidateAgentSkillMarkdownFrontmatter("/tmp/alpha/SKILL.md", "beta", "desc", "", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must match parent directory")

	require.NoError(t, ValidateAgentSkillMarkdownFrontmatter("/tmp/alpha/SKILL.md", "alpha", "desc", "", nil))
}

func TestValidateAgentSkillMarkdownFrontmatter_UsesNFKCForParentDirMatch(t *testing.T) {
	err := ValidateAgentSkillMarkdownFrontmatter("/tmp/résumé-helper/SKILL.md", "résumé-helper", "desc", "", nil)
	require.NoError(t, err)
}

func TestCatalogIndex_DiscoverReturnsImmutableClones(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	skillPath := filepath.Join(project, ".reliant", "skills", "immutable-skill", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(skillPath), 0o755))
	require.NoError(t, os.WriteFile(skillPath, []byte(`---
name: immutable-skill
description: Original description
metadata:
  team: core
allowed-tools: Bash(git:*) Read
---
Skill body`), 0o644))

	idx := NewCatalogIndex()
	first := idx.Discover(context.Background(), DiscoverInput{ProjectPath: project, LoadFullDefinitions: true})
	require.Contains(t, first.ByName, "immutable-skill")

	mutated := first.ByName["immutable-skill"]
	mutated.Name = "mutated"
	mutated.Description = "mutated description"
	mutated.Metadata["team"] = "mutated"
	mutated.AllowedTools[0] = "Write"
	first.ByName["immutable-skill"] = mutated
	first.Definitions[0].Name = "mutated-in-slice"
	first.Definitions[0].Description = "mutated-in-slice"

	second := idx.Discover(context.Background(), DiscoverInput{ProjectPath: project, LoadFullDefinitions: true})
	require.Contains(t, second.ByName, "immutable-skill")
	require.Equal(t, "immutable-skill", second.ByName["immutable-skill"].Name)
	require.Equal(t, "Original description", second.ByName["immutable-skill"].Description)
	require.Equal(t, "core", second.ByName["immutable-skill"].Metadata["team"])
	require.Equal(t, []string{"Bash(git:*)", "Read"}, second.ByName["immutable-skill"].AllowedTools)
	require.Equal(t, "immutable-skill", second.Definitions[0].Name)
}

func TestCatalogIndex_InvalidateProjectAndGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectOne := t.TempDir()
	projectTwo := t.TempDir()

	writeSkill := func(project, description string) {
		skillPath := filepath.Join(project, ".reliant", "skills", "demo", "SKILL.md")
		require.NoError(t, os.MkdirAll(filepath.Dir(skillPath), 0o755))
		require.NoError(t, os.WriteFile(skillPath, []byte("---\nname: demo\ndescription: "+description+"\n---\nBody"), 0o644))
	}

	writeSkill(projectOne, "one-v1")
	writeSkill(projectTwo, "two-v1")

	idx := NewCatalogIndex()
	resOne := idx.Discover(context.Background(), DiscoverInput{ProjectPath: projectOne})
	resTwo := idx.Discover(context.Background(), DiscoverInput{ProjectPath: projectTwo})
	require.Equal(t, "one-v1", resOne.ByName["demo"].Description)
	require.Equal(t, "two-v1", resTwo.ByName["demo"].Description)

	writeSkill(projectOne, "one-v2")
	writeSkill(projectTwo, "two-v2")

	cachedOne := idx.Discover(context.Background(), DiscoverInput{ProjectPath: projectOne})
	cachedTwo := idx.Discover(context.Background(), DiscoverInput{ProjectPath: projectTwo})
	require.Equal(t, "one-v1", cachedOne.ByName["demo"].Description)
	require.Equal(t, "two-v1", cachedTwo.ByName["demo"].Description)

	idx.Invalidate(projectOne)
	afterProjectInvalidate := idx.Discover(context.Background(), DiscoverInput{ProjectPath: projectOne})
	require.Equal(t, "one-v2", afterProjectInvalidate.ByName["demo"].Description)

	stillCachedProjectTwo := idx.Discover(context.Background(), DiscoverInput{ProjectPath: projectTwo})
	require.Equal(t, "two-v1", stillCachedProjectTwo.ByName["demo"].Description)

	idx.Invalidate("")
	afterGlobalInvalidate := idx.Discover(context.Background(), DiscoverInput{ProjectPath: projectTwo})
	require.Equal(t, "two-v2", afterGlobalInvalidate.ByName["demo"].Description)
}

func TestCatalogIndex_PreloadProjects_DedupesAndNormalizesPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	project := t.TempDir()
	skillPath := filepath.Join(project, ".reliant", "skills", "demo", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(skillPath), 0o755))
	require.NoError(t, os.WriteFile(skillPath, []byte("---\nname: demo\ndescription: preload\n---\nBody"), 0o644))

	idx := NewCatalogIndex()
	idx.PreloadProjects(context.Background(), []string{"", project, filepath.Clean(filepath.Join(project, ".")), project + string(filepath.Separator)})

	require.Equal(t, 1, idx.SnapshotCount(), "duplicate/normalized project paths should only warm one snapshot")
}

func TestCatalogIndex_PreloadProject_RespectsCancelledContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	project := t.TempDir()
	skillPath := filepath.Join(project, ".reliant", "skills", "demo", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(skillPath), 0o755))
	require.NoError(t, os.WriteFile(skillPath, []byte("---\nname: demo\ndescription: preload\n---\nBody"), 0o644))

	idx := NewCatalogIndex()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	idx.PreloadProject(ctx, project)

	require.Zero(t, idx.SnapshotCount())
}

func TestBuiltinSkills_DiscoverIncludesSkillCreator(t *testing.T) {
	snapshot := Discover(DiscoverInput{ProjectPath: t.TempDir(), LoadFullDefinitions: true})
	definition, ok := snapshot.ByName["skill-creator"]
	require.True(t, ok)
	require.Equal(t, skillscore.ScopeBuiltin, definition.Scope)
	require.Equal(t, skillscore.SkillFormatClaudeMarkdown, definition.Format)
	require.Contains(t, definition.Body, ".reliant.local/skills")
}
