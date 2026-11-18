// Copyright (c) 2025 Reliant Labs
package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	internalcli "github.com/reliant-labs/forge/internal/cli"
)

// plantProjectMigrationSkill writes a project-scope skill carrying
// relevance=migration and returns the project root.
//
// forge ships no migration skills most of the time — the registry holds
// one only while a release inside the supported upgrade window carries a
// breaking change, and skills are deleted once they age out. These tests
// pin the public API's BEHAVIOUR toward migration skills, which has to
// hold whether or not the binary currently ships any, so they supply
// their own instead of reaching for one that may not be there.
func plantProjectMigrationSkill(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".forge", "skills", "migrations", "v0.5.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	body := `---
name: v0.5.0
description: the deploy target moved from forge.yaml into KCL
relevance: migration
version: v0.5.0
---

# Migrating to v0.5.0
`
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return root
}

// TestListSkillsSignatureStable is a compile-time pin of the public API
// reliant links against: ListSkills(projectRoot string) ([]Skill, error)
// and LoadSkill(projectRoot, skillPath string) ([]byte, error) must keep
// these exact signatures — relevance filtering was added behind them
// additively (ListSkillsWithOptions / ListSkillsOptions).
func TestListSkillsSignatureStable(t *testing.T) {
	var _ = ListSkills
	var _ = LoadSkill
	var _ = ListSkillsWithOptions
	var _ func(string, string, RenderSkillOptions) ([]byte, error) = RenderSkill
}

// TestRenderSkillMatchesTheCLI pins what harnesses inject: RenderSkill with
// an explicit mount name is LoadSkill's body with every `forge <cmd>`
// rewritten to that mount, and the default name is the one this process
// reports — the same one RenderProjectMemory renders.
func TestRenderSkillMatchesTheCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	raw, err := LoadSkill("", "forge")
	if err != nil {
		t.Fatalf("LoadSkill: %v", err)
	}
	asForge, err := RenderSkill("", "forge", RenderSkillOptions{CLIName: "forge"})
	if err != nil {
		t.Fatalf("RenderSkill as forge: %v", err)
	}
	if string(asForge) != string(raw) {
		t.Error("RenderSkill under the bare `forge` name must equal LoadSkill's body")
	}

	mounted, err := RenderSkill("", "forge/forge", RenderSkillOptions{CLIName: "reliant forge"})
	if err != nil {
		t.Fatalf("RenderSkill as reliant forge: %v", err)
	}
	if want := regexp.MustCompile(`\bforge `).ReplaceAllString(string(raw), "reliant forge "); string(mounted) != want {
		t.Error("RenderSkill(reliant forge) is not LoadSkill's body with `forge ` rewritten to `reliant forge `")
	}

	def, err := RenderSkill("", "forge", RenderSkillOptions{})
	if err != nil {
		t.Fatalf("RenderSkill default: %v", err)
	}
	if name := internalcli.Name(); name != "forge" && !strings.Contains(string(def), name+" project new") {
		t.Errorf("default CLIName did not rewrite to this process's name %q", name)
	}
}

// TestListSkillsExcludesMigrationsByDefault pins the default-listing
// contract for harness consumers: one-time migration skills are hidden
// unless explicitly opted in, and the relevance class is exposed on the
// metadata so consumers can make their own call.
func TestListSkillsExcludesMigrationsByDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate from ~/.forge/skills
	root := plantProjectMigrationSkill(t)

	defaults, err := ListSkills(root)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(defaults) == 0 {
		t.Fatal("no skills listed")
	}
	for _, s := range defaults {
		if s.Relevance == "migration" {
			t.Errorf("default listing contains migration skill %q", s.Path)
		}
		if strings.HasPrefix(s.Path, "migrations/") {
			t.Errorf("default listing contains migrations/ path %q", s.Path)
		}
	}

	all, err := ListSkillsWithOptions(root, ListSkillsOptions{IncludeMigrationSkills: true})
	if err != nil {
		t.Fatalf("ListSkillsWithOptions: %v", err)
	}
	if len(all) <= len(defaults) {
		t.Errorf("opt-in listing (%d) not larger than default (%d)", len(all), len(defaults))
	}
	found := false
	for _, s := range all {
		if !strings.HasPrefix(s.Path, "migrations/") {
			continue
		}
		found = true
		if s.Relevance != "migration" {
			t.Errorf("skill %q: Relevance = %q, want migration", s.Path, s.Relevance)
		}
	}
	if !found {
		t.Error("opt-in listing has no migrations/ skills")
	}
}

// TestLoadSkillStillServesMigrations pins the load-by-path escape hatch:
// listings hide migration skills, but LoadSkill must keep serving them
// (forge project upgrade list points agents at exactly these paths, so
// this is the only route by which a migration is ever read).
func TestLoadSkillStillServesMigrations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := plantProjectMigrationSkill(t)

	body, err := LoadSkill(root, "migrations/v0.5.0")
	if err != nil {
		t.Fatalf("LoadSkill: %v", err)
	}
	if !strings.Contains(string(body), "Migrating to v0.5.0") {
		t.Error("migration skill body looks wrong")
	}
}
