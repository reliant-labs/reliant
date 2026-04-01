package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	skillcatalog "github.com/reliant-labs/reliant/internal/skills/catalog"
	skillmaterialize "github.com/reliant-labs/reliant/internal/skills/materialize"
	"github.com/stretchr/testify/require"
)

func TestDiscover_PrecedenceAndDiagnostics(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	write(filepath.Join(home, ".reliant", "skills", "test-skill", "SKILL.md"), `---
name: test-skill
description: global skill
---
body`)
	write(filepath.Join(project, ".reliant", "skills", "test-skill", "SKILL.md"), `---
name: test-skill
description: reliant project
---
body`)
	write(filepath.Join(project, ".reliant.local", "skills", "test-skill", "SKILL.md"), `---
name: test-skill
description: local winner
---
body`)
	write(filepath.Join(project, ".claude", "skills", "test-skill", "SKILL.md"), `---
name: test-skill
description: claude project
---
body`)
	write(filepath.Join(project, ".codex", "skills", "test-skill", "SKILL.md"), `---
name: test-skill
description: codex project
---
body`)
	write(filepath.Join(project, ".agents", "skills", "test-skill", "SKILL.md"), `---
name: test-skill
description: codex agents project
---
body`)

	write(filepath.Join(project, ".reliant", "skills", "bad", "SKILL.md"), "no frontmatter")

	result := DefaultRuntime().Discover(context.Background(), DiscoverInput{ProjectPath: project})
	require.Contains(t, result.ByName, "test-skill")
	require.Equal(t, "local winner", result.ByName["test-skill"].Description)
	require.Equal(t, SkillFormatClaudeMarkdown, result.ByName["test-skill"].Format)
	require.Empty(t, result.ByName["test-skill"].Body, "discovery should only parse frontmatter by default")
	require.NotEmpty(t, result.Diagnostics)
	require.Contains(t, result.ShadowedBy, filepath.Join(project, ".reliant", "skills", "test-skill", "SKILL.md"))
	require.Contains(t, result.ShadowedBy, filepath.Join(home, ".reliant", "skills", "test-skill", "SKILL.md"))
	require.NotContains(t, result.ShadowedBy, filepath.Join(project, ".claude", "skills", "test-skill", "SKILL.md"))
	require.NotContains(t, result.ShadowedBy, filepath.Join(project, ".codex", "skills", "test-skill", "SKILL.md"))
	require.NotContains(t, result.ShadowedBy, filepath.Join(project, ".agents", "skills", "test-skill", "SKILL.md"))
}

func TestDiscoverWithOptions_LoadFullDefinitions(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	skillPath := filepath.Join(project, ".reliant", "skills", "with-body", "SKILL.md")
	write(skillPath, `---
name: with-body
description: has body
---
This body must be loaded when requested.`)

	metaOnly := DefaultRuntime().Discover(context.Background(), DiscoverInput{ProjectPath: project})
	require.Contains(t, metaOnly.ByName, "with-body")
	require.Empty(t, metaOnly.ByName["with-body"].Body)

	full := DefaultRuntime().Discover(context.Background(), DiscoverInput{
		ProjectPath:         project,
		LoadFullDefinitions: true,
	})
	require.Contains(t, full.ByName, "with-body")
	require.Equal(t, "This body must be loaded when requested.", full.ByName["with-body"].Body)
}

func TestDiscover_CollectsSupportingFiles(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	skillDir := filepath.Join(project, ".reliant", "skills", "with-files")
	write(filepath.Join(skillDir, "SKILL.md"), `---
name: with-files
description: has files
---
Use files`)
	write(filepath.Join(skillDir, "guide.md"), "line1\nline2")
	write(filepath.Join(skillDir, "examples", "snippet.txt"), "hello world")

	result := DefaultRuntime().Discover(context.Background(), DiscoverInput{ProjectPath: project})
	skill, ok := result.ByName["with-files"]
	require.True(t, ok)
	require.Empty(t, skill.Files, "supporting files are loaded lazily for the active skill")

	active, diagnostics := skillmaterialize.LoadSupportingFiles(skillToDefinition(skill), SupportingFilesLimits{})
	require.Empty(t, diagnostics)
	skill = activeSkillToSkill(active)
	require.Len(t, skill.Files, 2)
	require.Equal(t, "examples/snippet.txt", skill.Files[0].RelativePath)
	require.Equal(t, "guide.md", skill.Files[1].RelativePath)
}

func TestLoadSupportingFiles_ExcludesLegalMetaFiles(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	skillDir := filepath.Join(project, ".reliant", "skills", "frontend-design")
	write(filepath.Join(skillDir, "SKILL.md"), `---
name: frontend-design
description: frontend helper
---
Use files`)
	write(filepath.Join(skillDir, "guide.md"), "guide")
	write(filepath.Join(skillDir, "licensee.md"), "not a legal meta file")
	write(filepath.Join(skillDir, "LICENSE.txt"), strings.Repeat("license-content", 800))
	write(filepath.Join(skillDir, "NOTICE"), strings.Repeat("notice-content", 800))
	write(filepath.Join(skillDir, "COPYING"), strings.Repeat("copying-content", 800))
	write(filepath.Join(skillDir, "COPYRIGHT.md"), strings.Repeat("copyright-content", 800))
	write(filepath.Join(skillDir, "LICENSE-MIT"), strings.Repeat("mit-license", 800))

	result := DefaultRuntime().Discover(context.Background(), DiscoverInput{ProjectPath: project})
	skill, ok := result.ByName["frontend-design"]
	require.True(t, ok)

	active, diagnostics := skillmaterialize.LoadSupportingFiles(skillToDefinition(skill), SupportingFilesLimits{MaxFiles: 8, MaxBytes: 32})
	require.Empty(t, diagnostics)
	skill = activeSkillToSkill(active)
	require.Len(t, skill.Files, 2)
	require.Equal(t, "guide.md", skill.Files[0].RelativePath)
	require.Equal(t, "licensee.md", skill.Files[1].RelativePath)
	require.False(t, skill.Files[0].Truncated)
	require.False(t, skill.Files[1].Truncated)
}

func TestLoadSupportingFiles_ExcludesOpenAIMetadataAndAssetImages(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	skillDir := filepath.Join(project, ".reliant", "skills", "gh-fix-ci")
	write(filepath.Join(skillDir, "SKILL.md"), `---
name: gh-fix-ci
description: fix github actions failures
---
Use gh and diagnostics.`)
	write(filepath.Join(skillDir, "scripts", "inspect_pr_checks.py"), "print('ok')")
	write(filepath.Join(skillDir, "agents", "openai.yaml"), "interface:\n  display_name: GitHub Fix CI")
	write(filepath.Join(skillDir, "assets", "github.png"), "fake image bytes")
	write(filepath.Join(skillDir, "assets", "README.md"), "asset docs")

	result := DefaultRuntime().Discover(context.Background(), DiscoverInput{ProjectPath: project})
	skill, ok := result.ByName["gh-fix-ci"]
	require.True(t, ok)

	active, diagnostics := skillmaterialize.LoadSupportingFiles(skillToDefinition(skill), SupportingFilesLimits{MaxFiles: 16, MaxBytes: 4096})
	require.Empty(t, diagnostics)
	skill = activeSkillToSkill(active)
	require.Len(t, skill.Files, 2)
	require.Equal(t, "assets/README.md", skill.Files[0].RelativePath)
	require.Equal(t, "scripts/inspect_pr_checks.py", skill.Files[1].RelativePath)
}

func TestIsExcludedSupportingFileName(t *testing.T) {
	tests := []struct {
		name     string
		excluded bool
	}{
		{name: "LICENSE", excluded: true},
		{name: "license.txt", excluded: true},
		{name: "LICENSE-MIT", excluded: true},
		{name: "license_apache", excluded: true},
		{name: "LICENCE.txt", excluded: true},
		{name: "NOTICE", excluded: true},
		{name: "notice.md", excluded: true},
		{name: "COPYING", excluded: true},
		{name: "COPYRIGHT.md", excluded: true},
		{name: "licensee.md", excluded: false},
		{name: "noticeme.md", excluded: false},
		{name: "copyingfile", excluded: false},
		{name: "", excluded: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.excluded, skillmaterialize.ShouldExcludeSupportingFileName(tc.name))
		})
	}
}

func TestIsExcludedSupportingFilePath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		excluded bool
	}{
		{name: "agents openai metadata", path: "agents/openai.yaml", excluded: true},
		{name: "agents nested file", path: "agents/nested/config.yml", excluded: true},
		{name: "top-level asset image", path: "assets/github.png", excluded: true},
		{name: "asset svg", path: "assets/icons/logo.svg", excluded: true},
		{name: "asset markdown is kept", path: "assets/README.md", excluded: false},
		{name: "scripts file is kept", path: "scripts/inspect_pr_checks.py", excluded: false},
		{name: "references file is kept", path: "references/troubleshooting.md", excluded: false},
		{name: "empty path", path: "", excluded: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.excluded, skillmaterialize.ShouldExcludeSupportingFilePath(tc.path))
		})
	}
}

func TestDiscover_NestedSkillsAndSameScopeLexicalWinner(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	write(filepath.Join(project, ".reliant", "skills", "zeta", "dup-skill", "SKILL.md"), `---
name: dup-skill
description: zeta version
---
body`)
	write(filepath.Join(project, ".reliant", "skills", "alpha", "dup-skill", "SKILL.md"), `---
name: dup-skill
description: alpha version
---
body`)

	result := DefaultRuntime().Discover(context.Background(), DiscoverInput{ProjectPath: project})
	require.Contains(t, result.ByName, "dup-skill")
	require.Equal(t, "alpha version", result.ByName["dup-skill"].Description)
}

func TestDiscover_GracefulWithMissingDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	result := DefaultRuntime().Discover(context.Background(), DiscoverInput{ProjectPath: t.TempDir()})
	require.NotContains(t, result.ByName, "test-skill")
	require.Contains(t, result.ByName, "skill-creator")
	require.NotEmpty(t, result.Skills)
	require.Empty(t, result.Diagnostics)
}

func TestDiscover_IgnoresExternalProviderSkillRoots(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	write(filepath.Join(project, ".claude", "skills", "external-only", "SKILL.md"), `---
name: external-only
description: from claude
---
body`)
	write(filepath.Join(project, ".codex", "skills", "external-codex", "SKILL.md"), `---
name: external-codex
description: from codex project
---
body`)
	write(filepath.Join(project, ".agents", "skills", "external-agents", "SKILL.md"), `---
name: external-agents
description: from agents project
---
body`)
	write(filepath.Join(home, ".codex", "skills", "external-global", "SKILL.md"), `---
name: external-global
description: from codex global
---
body`)
	write(filepath.Join(home, ".claude", "skills", "external-global-claude", "SKILL.md"), `---
name: external-global-claude
description: from claude global
---
body`)

	result := DefaultRuntime().Discover(context.Background(), DiscoverInput{ProjectPath: project})
	require.NotContains(t, result.ByName, "external-only")
	require.NotContains(t, result.ByName, "external-codex")
	require.NotContains(t, result.ByName, "external-agents")
	require.NotContains(t, result.ByName, "external-global")
	require.NotContains(t, result.ByName, "external-global-claude")
}

func TestDiscoverWithLimits_CacheInvalidationReflectsNewSkill(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	result1 := DefaultRuntime().Discover(context.Background(), DiscoverInput{ProjectPath: project})
	require.NotContains(t, result1.ByName, "new-skill")

	write(filepath.Join(project, ".reliant", "skills", "new-skill", "SKILL.md"), `---
name: new-skill
description: new one
---
body`)

	resultCached := DefaultRuntime().Discover(context.Background(), DiscoverInput{ProjectPath: project})
	require.NotContains(t, resultCached.ByName, "new-skill", "cache should still serve old result before invalidation")

	skillcatalog.DefaultCatalogIndex().Invalidate(project)
	result2 := DefaultRuntime().Discover(context.Background(), DiscoverInput{ProjectPath: project})
	require.Contains(t, result2.ByName, "new-skill")
}
