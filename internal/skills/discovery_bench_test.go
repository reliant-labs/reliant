package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	skillcatalog "github.com/reliant-labs/reliant/internal/skills/catalog"
)

func writeSkillForBench(b *testing.B, path, name, description string) {
	b.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.Fatalf("mkdir failed: %v", err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\nbody"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		b.Fatalf("write failed: %v", err)
	}
}

func BenchmarkDiscoverWithLimits_Cached(b *testing.B) {
	project := b.TempDir()
	home := b.TempDir()
	b.Setenv("HOME", home)

	for i := 0; i < 120; i++ {
		name := "bench-skill-" + string(rune('a'+(i%26))) + "-" + string(rune('a'+((i/26)%26))) + "-" + string(rune('0'+(i%10)))
		writeSkillForBench(b, filepath.Join(project, ".reliant", "skills", name, "SKILL.md"), name, "benchmark skill")
	}

	skillcatalog.DefaultCatalogIndex().Invalidate(project)
	_ = DefaultRuntime().Discover(context.Background(), DiscoverInput{ProjectPath: project})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = DefaultRuntime().Discover(context.Background(), DiscoverInput{ProjectPath: project})
	}
}

func BenchmarkDiscoverWithLimits_Uncached(b *testing.B) {
	project := b.TempDir()
	home := b.TempDir()
	b.Setenv("HOME", home)

	for i := 0; i < 120; i++ {
		name := "bench-uncached-" + string(rune('a'+(i%26))) + "-" + string(rune('a'+((i/26)%26))) + "-" + string(rune('0'+(i%10)))
		writeSkillForBench(b, filepath.Join(project, ".reliant", "skills", name, "SKILL.md"), name, "benchmark skill")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		skillcatalog.DefaultCatalogIndex().Invalidate(project)
		_ = DefaultRuntime().Discover(context.Background(), DiscoverInput{ProjectPath: project})
	}
}

func BenchmarkDiscoverWithOptions_LoadFullDefinitions(b *testing.B) {
	project := b.TempDir()
	home := b.TempDir()
	b.Setenv("HOME", home)

	for i := 0; i < 120; i++ {
		name := "bench-full-" + string(rune('a'+(i%26))) + "-" + string(rune('a'+((i/26)%26))) + "-" + string(rune('0'+(i%10)))
		writeSkillForBench(b, filepath.Join(project, ".reliant", "skills", name, "SKILL.md"), name, "benchmark skill")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		skillcatalog.DefaultCatalogIndex().Invalidate(project)
		_ = DefaultRuntime().Discover(context.Background(), DiscoverInput{ProjectPath: project, LoadFullDefinitions: true})
	}
}
