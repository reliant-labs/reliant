package catalog

import (
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

func TestBuiltinSkills_DiscoverIncludesReliantConfig(t *testing.T) {
	snapshot := Discover(DiscoverInput{ProjectPath: t.TempDir(), LoadFullDefinitions: true})
	definition, ok := snapshot.ByName["reliant-config"]
	require.True(t, ok)
	require.Equal(t, skillscore.ScopeBuiltin, definition.Scope)
	require.Equal(t, skillscore.SkillFormatClaudeMarkdown, definition.Format)
	require.Contains(t, definition.Body, ".reliant.local/skills")
}

func TestBuiltinSkills_HaveSkillPath(t *testing.T) {
	snapshot := Discover(DiscoverInput{ProjectPath: t.TempDir(), LoadFullDefinitions: true})

	for _, def := range snapshot.Definitions {
		if def.Scope != skillscore.ScopeBuiltin {
			continue
		}
		t.Run(def.Name, func(t *testing.T) {
			require.NotEmpty(t, def.SkillPath,
				"builtin skill %q must have a non-empty SkillPath so skill tool load works", def.Name)
		})
	}
}

func TestDiscover_IncludesExternalProviderSkillRoots(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeSkill := func(dir, name, description string) {
		skillPath := filepath.Join(dir, name, "SKILL.md")
		require.NoError(t, os.MkdirAll(filepath.Dir(skillPath), 0o755))
		require.NoError(t, os.WriteFile(skillPath, []byte("---\nname: "+name+"\ndescription: "+description+"\n---\nBody"), 0o644))
	}

	// Project-scoped Claude skill
	writeSkill(filepath.Join(project, ".claude", "skills"), "claude-skill", "A Claude skill")
	// Project-scoped Codex skill
	writeSkill(filepath.Join(project, ".codex", "skills"), "codex-skill", "A Codex skill")
	// Project-scoped Codex agents skill
	writeSkill(filepath.Join(project, ".agents", "skills"), "agents-skill", "An agents skill")
	// Global Claude skill
	writeSkill(filepath.Join(home, ".claude", "skills"), "claude-global", "A global Claude skill")
	// Global Codex skill
	writeSkill(filepath.Join(home, ".codex", "skills"), "codex-global", "A global Codex skill")

	snapshot := Discover(DiscoverInput{ProjectPath: project})

	require.Contains(t, snapshot.ByName, "claude-skill")
	require.Equal(t, skillscore.ScopeClaude, snapshot.ByName["claude-skill"].Scope)

	require.Contains(t, snapshot.ByName, "codex-skill")
	require.Equal(t, skillscore.ScopeCodexProject, snapshot.ByName["codex-skill"].Scope)

	require.Contains(t, snapshot.ByName, "agents-skill")
	require.Equal(t, skillscore.ScopeCodexAgents, snapshot.ByName["agents-skill"].Scope)

	require.Contains(t, snapshot.ByName, "claude-global")
	require.Equal(t, skillscore.ScopeClaudeGlobal, snapshot.ByName["claude-global"].Scope)

	require.Contains(t, snapshot.ByName, "codex-global")
	require.Equal(t, skillscore.ScopeCodexGlobal, snapshot.ByName["codex-global"].Scope)
}

func TestDiscover_MultiRepoSources(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeSkill := func(dir, name, description string) {
		skillPath := filepath.Join(dir, name, "SKILL.md")
		require.NoError(t, os.MkdirAll(filepath.Dir(skillPath), 0o755))
		require.NoError(t, os.WriteFile(skillPath, []byte("---\nname: "+name+"\ndescription: "+description+"\n---\nBody"), 0o644))
	}

	// Nested repo "api" has a skill in .claude/skills
	writeSkill(filepath.Join(project, "api", ".claude", "skills"), "api-deploy", "Deploy API")
	// Nested repo "web" has a skill in .reliant/skills
	writeSkill(filepath.Join(project, "web", ".reliant", "skills"), "web-deploy", "Deploy Web")

	snapshot := DiscoverAll(DiscoverInput{
		ProjectPath: project,
		RepoSources: []string{"api", "web"},
	})

	// Skills from nested repos get their NormalizedKey prefixed with source.
	require.Contains(t, snapshot.ByName, "api/api-deploy")
	require.Equal(t, "api", snapshot.ByName["api/api-deploy"].Source)

	require.Contains(t, snapshot.ByName, "web/web-deploy")
	require.Equal(t, "web", snapshot.ByName["web/web-deploy"].Source)

	// Builtins should still be present.
	require.Contains(t, snapshot.ByName, "reliant-config")
}

func TestDiscover_ReliantShadowsExternalProviderSkills(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeSkill := func(dir, name, description string) {
		skillPath := filepath.Join(dir, name, "SKILL.md")
		require.NoError(t, os.MkdirAll(filepath.Dir(skillPath), 0o755))
		require.NoError(t, os.WriteFile(skillPath, []byte("---\nname: "+name+"\ndescription: "+description+"\n---\nBody"), 0o644))
	}

	// Same skill name in both Reliant and Claude paths
	writeSkill(filepath.Join(project, ".reliant", "skills"), "shared-skill", "Reliant version")
	writeSkill(filepath.Join(project, ".claude", "skills"), "shared-skill", "Claude version")

	snapshot := Discover(DiscoverInput{ProjectPath: project})

	require.Contains(t, snapshot.ByName, "shared-skill")
	require.Equal(t, skillscore.ScopeProject, snapshot.ByName["shared-skill"].Scope)
	require.Equal(t, "Reliant version", snapshot.ByName["shared-skill"].Description)
}

func TestParseSkillMarkdown_ClaudeCompatibleFrontmatter(t *testing.T) {
	t.Run("Claude fields parsed for external scope", func(t *testing.T) {
		userInvocable := true
		def, err := ParseSkillMarkdown("/tmp/my-skill/SKILL.md", skillscore.ScopeClaude, []byte(`---
name: my-skill
description: A Claude skill
argument-hint: provide a file path
disable-model-invocation: true
user-invocable: true
paths: src/**/*.ts
---
Do things.`))
		require.NoError(t, err)
		require.Equal(t, "my-skill", def.Name)
		require.Equal(t, "provide a file path", def.ArgumentHint)
		require.True(t, def.DisableModelInvocation)
		require.NotNil(t, def.UserInvocable)
		require.Equal(t, userInvocable, *def.UserInvocable)
		require.Equal(t, "src/**/*.ts", def.Paths)
	})

	t.Run("unknown fields ignored for external scope", func(t *testing.T) {
		def, err := ParseSkillMarkdown("/tmp/lenient-skill/SKILL.md", skillscore.ScopeClaudeGlobal, []byte(`---
name: lenient-skill
description: Has extra fields
model: claude-sonnet
hooks: some-hook
shell: bash
---
Body content.`))
		require.NoError(t, err)
		require.Equal(t, "lenient-skill", def.Name)
		require.Equal(t, "Has extra fields", def.Description)
	})

	t.Run("unknown fields still rejected for Reliant scope", func(t *testing.T) {
		_, err := ParseSkillMarkdown("/tmp/strict-skill/SKILL.md", skillscore.ScopeProject, []byte(`---
name: strict-skill
description: Has extra fields
model: claude-sonnet
---
Body content.`))
		require.Error(t, err)
		require.Contains(t, err.Error(), "unexpected fields")
	})

	t.Run("Claude allowed fields accepted for Reliant scope", func(t *testing.T) {
		def, err := ParseSkillMarkdown("/tmp/with-hint/SKILL.md", skillscore.ScopeProject, []byte(`---
name: with-hint
description: Has argument hint
argument-hint: some hint
---
Body.`))
		require.NoError(t, err)
		require.Equal(t, "some hint", def.ArgumentHint)
	})
}
