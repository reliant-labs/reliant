package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultRuntime_DiscoverHonorsDisabledDefinitionPathSet(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	skillPath := filepath.Join(project, ".reliant", "skills", "test-skill", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(skillPath), 0o755))
	require.NoError(t, os.WriteFile(skillPath, []byte(`---
name: test-skill
description: test skill
---
body`), 0o644))

	runtime := DefaultRuntime()

	discovered := runtime.Discover(context.Background(), DiscoverInput{
		ProjectPath:         project,
		LoadFullDefinitions: false,
	})
	require.Contains(t, discovered.ByName, "test-skill")

	disabled := map[string]struct{}{CanonicalDefinitionPath(skillPath): {}}
	discoveredDisabled := runtime.Discover(context.Background(), DiscoverInput{
		ProjectPath:               project,
		DisabledDefinitionPathSet: disabled,
		LoadFullDefinitions:       false,
	})
	require.NotContains(t, discoveredDisabled.ByName, "test-skill")
}

func TestDefaultRuntime_ResolveTurnProducesPromptSectionsAndHints(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	skillPath := filepath.Join(project, ".reliant", "skills", "debug-sql", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(skillPath), 0o755))
	require.NoError(t, os.WriteFile(skillPath, []byte(`---
name: debug-sql
description: Debug SQL queries and explain plans
---
Use SQL diagnostics.`), 0o644))

	runtime := DefaultRuntime()

	resolved := runtime.ResolveTurn(context.Background(), ResolveTurnInput{
		ProjectPath:        project,
		LatestUserMessage:  "/debug-sql",
		RecentUserMessages: []string{"Need help with postgres query latency"},
		ActivationMode:     "auto",
		AvailableSkillsLimits: AvailableSkillsRenderLimits{
			MaxSkills: 10,
			MaxBytes:  4000,
		},
	})
	require.NotNil(t, resolved.ActiveSkill)
	require.Equal(t, "debug-sql", resolved.ActiveSkill.Name)
	require.Contains(t, resolved.AvailableSkillsSection, "<available_skills>")
	require.Contains(t, resolved.AvailableSkillsSection, "debug-sql")
	require.Contains(t, resolved.ActiveSkillSection, "<active_skill>")
	require.Contains(t, resolved.ActiveSkillSection, "name: debug-sql")
	require.Empty(t, resolved.WarningHints)

	missing := runtime.ResolveTurn(context.Background(), ResolveTurnInput{
		ProjectPath:       project,
		LatestUserMessage: "/missing-skill",
		ActivationMode:    "auto",
	})
	require.Nil(t, missing.ActiveSkill)
	require.NotEmpty(t, missing.WarningHints)
}

func TestDefaultRuntime_ResolveTurn_ToolIntegrationModeOmitsLocationsAndSupportingFiles(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	skillDir := filepath.Join(project, ".reliant", "skills", "debug-sql")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: debug-sql
description: Debug SQL queries and explain plans
---
Use SQL diagnostics.`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "guide.md"), []byte("sql examples"), 0o644))

	runtime := DefaultRuntime()
	resolved := runtime.ResolveTurn(context.Background(), ResolveTurnInput{
		ProjectPath:        project,
		LatestUserMessage:  "/debug-sql",
		RecentUserMessages: []string{"Need help with postgres query latency"},
		ActivationMode:     "auto",
		IntegrationMode:    "tool",
	})

	require.NotNil(t, resolved.ActiveSkill)
	require.Contains(t, resolved.AvailableSkillsSection, "<available_skills>")
	require.NotContains(t, resolved.AvailableSkillsSection, "<location>")
	require.Contains(t, resolved.ActiveSkillSection, "<active_skill>")
	require.NotContains(t, resolved.ActiveSkillSection, "supporting_files:")
}
