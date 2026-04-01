package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	skillcatalog "github.com/reliant-labs/reliant/internal/skills/catalog"
	skillprompt "github.com/reliant-labs/reliant/internal/skills/prompt"
)

func BenchmarkDefaultRuntimeResolveTurn_AutoSelection(b *testing.B) {
	project := b.TempDir()
	home := b.TempDir()
	b.Setenv("HOME", home)

	writeSkill := func(path, content string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatalf("mkdir failed: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			b.Fatalf("write failed: %v", err)
		}
	}

	writeSkill(filepath.Join(project, ".reliant", "skills", "sql-debug", "SKILL.md"), `---
name: sql-debug
description: Analyze SQL queries and index usage
---
Use SQL diagnostics.`)
	writeSkill(filepath.Join(project, ".reliant", "skills", "frontend-ui", "SKILL.md"), `---
name: frontend-ui
description: Debug visual regressions and CSS
---
Use UI diagnostics.`)
	writeSkill(filepath.Join(project, ".reliant", "skills", "sql-debug", "guide.md"), "sql query planner explain analyze index joins where")

	skillcatalog.DefaultCatalogIndex().Invalidate(project)

	input := ResolveTurnInput{
		ProjectPath:       project,
		LatestUserMessage: "Need help with postgres query performance",
		ActivationMode:    "auto",
		SupportingLimits: SupportingFilesLimits{
			MaxFiles: 8,
			MaxBytes: 4000,
		},
		RetrievalConfig: RetrievalConfig{
			MaxFiles:       8,
			MaxChunks:      4,
			ChunkBytes:     120,
			ChunkOverlap:   20,
			MaxPromptBytes: 600,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res := DefaultRuntime().ResolveTurn(context.Background(), input)
		if res.ActiveSkill == nil {
			b.Fatal("expected active skill")
		}
	}
}

func BenchmarkBuildAvailableSkillsSectionWithLimits(b *testing.B) {
	skillsList := make([]Skill, 0, 300)
	for i := 0; i < 300; i++ {
		skillsList = append(skillsList, Skill{
			Name:        "skill-bench-" + string(rune('a'+(i%26))) + string(rune('0'+(i%10))),
			Description: "benchmark description for rendering available skills section",
			Format:      SkillFormatClaudeMarkdown,
			Path:        filepath.ToSlash(filepath.Join("/tmp", "bench", "skill-bench", string(rune('a'+(i%26))), "SKILL.md")),
			Scope:       ScopeProject,
		})
	}

	limits := AvailableSkillsRenderLimits{MaxSkills: 64, MaxBytes: 6000}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		section := skillprompt.BuildAvailableSkillsSection(skillSliceToDefinitions(skillsList), limits, skillprompt.AvailableSkillsRenderOptions{})
		if section == "" {
			b.Fatal("expected non-empty section")
		}
	}
}
