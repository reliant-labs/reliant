package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	skillmaterialize "github.com/reliant-labs/reliant/internal/skills/materialize"
	skillprompt "github.com/reliant-labs/reliant/internal/skills/prompt"
	"github.com/stretchr/testify/require"
)

func TestDefaultRuntimeResolveTurn_ExplicitSkillLoadsSupportingFilesWithRetrievalBudget(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	skillDir := filepath.Join(project, ".reliant", "skills", "debug-sql")
	write(filepath.Join(skillDir, "SKILL.md"), `---
name: debug-sql
description: Debug SQL and database query behavior
---
Use SQL diagnostics and explain plans.`)
	write(filepath.Join(skillDir, "guide.md"), "sql query planner explain analyze index joins where clause")
	write(filepath.Join(skillDir, "ui.md"), "frontend css colors spacing typography")

	res := DefaultRuntime().ResolveTurn(context.Background(), ResolveTurnInput{
		ProjectPath:        project,
		LatestUserMessage:  "/debug-sql",
		RecentUserMessages: []string{"need help with postgres join query"},
		ActivationMode:     "auto",
		SupportingLimits: SupportingFilesLimits{
			MaxFiles: 8,
			MaxBytes: 5000,
		},
		RetrievalConfig: RetrievalConfig{
			MaxFiles:       8,
			MaxChunks:      2,
			ChunkBytes:     80,
			ChunkOverlap:   10,
			MaxPromptBytes: 120,
		},
	})

	require.NotNil(t, res.ActiveSkill)
	require.Equal(t, "debug-sql", res.ActiveSkill.Name)
	require.NotEmpty(t, res.ActiveSkill.Files)

	// Retrieval should bias toward the SQL-relevant file and keep content under budget.
	var total int
	var foundSQL bool
	for _, f := range res.ActiveSkill.Files {
		total += len(f.Content)
		if f.RelativePath == "guide.md" {
			foundSQL = true
		}
	}
	require.True(t, foundSQL)
	require.LessOrEqual(t, total, 120)

	hasInfoNotice := false
	for _, n := range res.Notices {
		if n.Level == NoticeLevelInfo {
			hasInfoNotice = true
			require.Contains(t, n.Message, "skill() debug-sql")
		}
	}
	require.True(t, hasInfoNotice)
}

func TestDefaultRuntimeResolveTurn_AutoSelectionDoesNotEmitSkillSummaryNotice(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	skillDir := filepath.Join(project, ".reliant", "skills", "sql-debug")
	write(filepath.Join(skillDir, "SKILL.md"), `---
name: sql-debug
description: Analyze SQL performance and schema bottlenecks
---
Use SQL diagnostics.`)

	res := DefaultRuntime().ResolveTurn(context.Background(), ResolveTurnInput{
		ProjectPath:       project,
		LatestUserMessage: "Need help debugging a SQL query",
		ActivationMode:    "auto",
	})

	require.NotNil(t, res.ActiveSkill)
	for _, n := range res.Notices {
		require.NotContains(t, n.Message, "skill() sql-debug")
	}
}

func TestDefaultRuntimeResolveTurn_ExplicitModeDisablesAutoSelection(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	skillDir := filepath.Join(project, ".reliant", "skills", "sql-debug")
	write(filepath.Join(skillDir, "SKILL.md"), `---
name: sql-debug
description: Analyze SQL performance and schema bottlenecks
---
Use SQL diagnostics.`)

	res := DefaultRuntime().ResolveTurn(context.Background(), ResolveTurnInput{
		ProjectPath:       project,
		LatestUserMessage: "Need help debugging a SQL query",
		ActivationMode:    "explicit",
	})

	require.Nil(t, res.ActiveSkill)
}

func TestDefaultRuntimeResolveTurn_AutoSelectionUsesRecentMessagesFallback(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	write(filepath.Join(project, ".reliant", "skills", "sql-debug", "SKILL.md"), `---
name: sql-debug
description: Inspect SQL query plans and optimize indexes
---
Use SQL diagnostics.`)
	write(filepath.Join(project, ".reliant", "skills", "frontend-ui", "SKILL.md"), `---
name: frontend-ui
description: Debug UI and visual issues
---
Use UI workflows.`)

	res := DefaultRuntime().ResolveTurn(context.Background(), ResolveTurnInput{
		ProjectPath:        project,
		LatestUserMessage:  "Can you help?",
		RecentUserMessages: []string{"postgres query timing is very slow"},
		ActivationMode:     "auto",
	})

	require.NotNil(t, res.ActiveSkill)
	require.Equal(t, "sql-debug", res.ActiveSkill.Name)
}

func TestDefaultRuntimeResolveTurn_IgnoresExternalProviderSkills(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	write(filepath.Join(project, ".claude", "skills", "external-sql", "SKILL.md"), `---
name: external-sql
description: SQL diagnostics from external scope
---
Use this external SQL skill.`)

	res := DefaultRuntime().ResolveTurn(context.Background(), ResolveTurnInput{
		ProjectPath:       project,
		LatestUserMessage: "Need help debugging SQL joins",
		ActivationMode:    "auto",
	})

	require.Nil(t, res.ActiveSkill)
	for _, n := range res.Notices {
		require.NotContains(t, n.Message, "untrusted")
	}
}

func TestDefaultRuntimeResolveTurn_AutoSelectionUsesReliantSkillWhenExternalProviderVariantExists(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	write(filepath.Join(project, ".claude", "skills", "playwright-cli", "SKILL.md"), `---
name: playwright-cli
description: Run Playwright tests with cli automation and browser fixtures
---
Use this external playwright helper.`)
	write(filepath.Join(project, ".reliant", "skills", "playwright", "SKILL.md"), `---
name: playwright
description: Playwright browser testing and automation workflows
---
Use the trusted reliant playwright workflow.`)

	res := DefaultRuntime().ResolveTurn(context.Background(), ResolveTurnInput{
		ProjectPath:       project,
		LatestUserMessage: "Need help with playwright browser tests",
		ActivationMode:    "auto",
	})

	require.NotNil(t, res.ActiveSkill)
	require.Equal(t, "playwright", res.ActiveSkill.Name)
	require.Equal(t, ScopeProject, res.ActiveSkill.Scope)
	for _, n := range res.Notices {
		require.NotContains(t, n.Message, "untrusted")
	}
}

func TestDefaultRuntimeResolveTurn_ExplicitInvocationDoesNotActivateExternalProviderSkill(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	write(filepath.Join(project, ".claude", "skills", "external-sql", "SKILL.md"), `---
name: external-sql
description: SQL diagnostics from external scope
---
Use this external SQL skill.`)

	res := DefaultRuntime().ResolveTurn(context.Background(), ResolveTurnInput{
		ProjectPath:       project,
		LatestUserMessage: "/external-sql",
		ActivationMode:    "auto",
	})

	require.Nil(t, res.ActiveSkill)
	foundMissingWarning := false
	for _, n := range res.Notices {
		if n.Level == NoticeLevelWarning && strings.Contains(n.Message, "was not found") {
			foundMissingWarning = true
			break
		}
	}
	require.True(t, foundMissingWarning)
}

func TestSelectSupportingFileContent_RespectsMaxFilesAndBudget(t *testing.T) {
	files := []SupportingFile{
		{RelativePath: "one.md", Content: "alpha beta gamma delta epsilon zeta eta theta"},
		{RelativePath: "two.md", Content: "sql query explain plan index join where"},
		{RelativePath: "three.md", Content: "frontend css spacing colors typography"},
	}

	selected := skillmaterialize.SelectSupportingFileContent(files, "sql join query", RetrievalConfig{
		MaxFiles:       2,
		MaxChunks:      2,
		ChunkBytes:     40,
		ChunkOverlap:   10,
		MaxPromptBytes: 70,
	})

	require.NotEmpty(t, selected)
	require.LessOrEqual(t, len(selected), 2)

	total := 0
	for _, s := range selected {
		total += len(s.Content)
	}
	require.LessOrEqual(t, total, 70)
}

func TestBuildRetrievalQuery_DedupesAndPreservesOrder(t *testing.T) {
	q := skillmaterialize.BuildRetrievalQuery("need SQL help", "Need SQL help", "postgres join issue", "")
	require.Equal(t, "need SQL help\npostgres join issue", q)
}

func TestWarningHintsForPrompt_OnlyWarnings(t *testing.T) {
	hints := skillprompt.WarningHintsForPrompt([]Notice{
		{Level: NoticeLevelInfo, Message: "skill() docx"},
		{Level: NoticeLevelWarning, Message: "Requested skill missing"},
	})
	require.Equal(t, []string{"Requested skill missing"}, hints)
}

func TestDefaultRuntimeResolveTurn_AutoSelectionPrefersLogicalFrontendMatchOverLocalDocx(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	write(filepath.Join(project, ".reliant.local", "skills", "docx", "SKILL.md"), `---
name: docx
description: Use this skill whenever the user wants to create, read, edit, or manipulate Word documents (.docx files).
---
Use docx workflows.`)

	write(filepath.Join(home, ".reliant", "skills", "frontend-design", "SKILL.md"), `---
name: frontend-design
description: Use this skill for website redesigns, homepage updates, hero sections, landing pages, and frontend UI changes.
---
Use frontend workflows.`)

	res := DefaultRuntime().ResolveTurn(context.Background(), ResolveTurnInput{
		ProjectPath:       project,
		LatestUserMessage: "lets change the hero to be smaller",
		ActivationMode:    "auto",
	})

	require.NotNil(t, res.ActiveSkill)
	require.Equal(t, "frontend-design", res.ActiveSkill.Name)
}

func TestDefaultRuntimeResolveTurn_AutoSelectionDoesNotPrioritizeScopeForDifferentSkillsOnTie(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	write(filepath.Join(project, ".reliant.local", "skills", "z-local", "SKILL.md"), `---
name: z-local
description: Handles hero redesign and frontend updates.
---
local skill`)
	write(filepath.Join(home, ".reliant", "skills", "a-global", "SKILL.md"), `---
name: a-global
description: Handles hero redesign and frontend updates.
---
global skill`)

	res := DefaultRuntime().ResolveTurn(context.Background(), ResolveTurnInput{
		ProjectPath:       project,
		LatestUserMessage: "redesign the hero section",
		ActivationMode:    "auto",
	})

	require.NotNil(t, res.ActiveSkill)
	// Tie-break should be deterministic by name/path, not by scope for different skills.
	require.Equal(t, "a-global", res.ActiveSkill.Name)
}

func TestDefaultRuntimeResolveTurn_AutoSelectionPrioritizesLatestTurnOverHistory(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	write(filepath.Join(project, ".reliant.local", "skills", "docx", "SKILL.md"), `---
name: docx
description: Create and edit .docx Word documents.
---
Use docx workflows.`)
	write(filepath.Join(project, ".reliant", "skills", "frontend-design", "SKILL.md"), `---
name: frontend-design
description: Use this skill for homepage redesigns, hero section updates, and frontend UI work.
---
Use frontend workflows.`)

	res := DefaultRuntime().ResolveTurn(context.Background(), ResolveTurnInput{
		ProjectPath:        project,
		LatestUserMessage:  "make the hero smaller",
		RecentUserMessages: []string{"please generate a Word doc with this content"},
		ActivationMode:     "auto",
	})

	require.NotNil(t, res.ActiveSkill)
	require.Equal(t, "frontend-design", res.ActiveSkill.Name)
}

func TestDefaultRuntimeResolveTurn_MetaSkillCatalogQueriesDoNotAutoActivateSkills(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	write(filepath.Join(project, ".reliant", "skills", "docx", "SKILL.md"), `---
name: docx
description: Create, edit, and manipulate Word documents.
---
Use docx workflows.`)
	write(filepath.Join(home, ".reliant", "skills", "frontend-design", "SKILL.md"), `---
name: frontend-design
description: Create distinctive, production-grade frontend interfaces with high design quality. Use this skill when the user asks to build web components, pages, artifacts, posters, or applications.
---
Use frontend workflows.`)

	res := DefaultRuntime().ResolveTurn(context.Background(), ResolveTurnInput{
		ProjectPath:       project,
		LatestUserMessage: "what skills do you have",
		ActivationMode:    "auto",
	})

	require.Nil(t, res.ActiveSkill)
	require.Contains(t, res.AvailableSkillsSection, "frontend-design")
	require.Contains(t, res.AvailableSkillsSection, "docx")
}

func TestDefaultRuntimeResolveTurn_MetaSkillCatalogQueriesDoNotRevivePriorDomainContext(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	write(filepath.Join(project, ".reliant", "skills", "frontend-design", "SKILL.md"), `---
name: frontend-design
description: Use this skill for homepage redesigns, hero section updates, and frontend UI work.
---
Use frontend workflows.`)

	res := DefaultRuntime().ResolveTurn(context.Background(), ResolveTurnInput{
		ProjectPath:        project,
		LatestUserMessage:  "what skills do you have",
		RecentUserMessages: []string{"i want to redesign my home page"},
		ActivationMode:     "auto",
	})

	require.Nil(t, res.ActiveSkill)
}
