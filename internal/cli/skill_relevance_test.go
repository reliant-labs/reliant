package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseFrontmatter_ParsesRelevanceField verifies the `relevance:`
// frontmatter field (and the migration skill's `version:`) flow through
// to skillMeta verbatim.
func TestParseFrontmatter_ParsesRelevanceField(t *testing.T) {
	body := []byte("---\nname: x\ndescription: y\nrelevance: migration\nversion: v0.5.0\n---\nbody\n")
	got := parseFrontmatter(body)
	if got.Relevance != SkillRelevanceMigration {
		t.Errorf("Relevance = %q, want %q", got.Relevance, SkillRelevanceMigration)
	}
	if got.Version != "v0.5.0" {
		t.Errorf("Version = %q, want v0.5.0", got.Version)
	}
}

// TestParseFrontmatter_RelevanceDefaultsToEmpty pins the legacy default:
// skills without the field stay always-relevant.
func TestParseFrontmatter_RelevanceDefaultsToEmpty(t *testing.T) {
	got := parseFrontmatter([]byte("---\nname: x\ndescription: y\n---\nbody\n"))
	if got.Relevance != "" {
		t.Errorf("expected empty Relevance when frontmatter omits the field, got %q", got.Relevance)
	}
}

// TestListForgeShippedSkills_MigrationRelevance verifies that any shipped
// skill under migrations/ carries relevance=migration (stamped
// frontmatter, with the directory-derived default as backstop), and that
// non-migration skills don't.
//
// The shipped migration set is normally EMPTY — forge ships a migration
// only while a release inside the supported upgrade window carries a
// breaking change — so this asserts the correspondence rather than a
// count. The gate mechanism itself is covered by
// TestFilterDefaultRelevance_DropsMigrations, which does not depend on
// anything being in the registry.
func TestListForgeShippedSkills_MigrationRelevance(t *testing.T) {
	skills, err := listForgeShippedSkills()
	if err != nil {
		t.Fatalf("listForgeShippedSkills: %v", err)
	}
	for _, s := range skills {
		isMigrationDir := strings.HasPrefix(s.Path, "migrations/")
		isTagged := s.Relevance == SkillRelevanceMigration
		if isMigrationDir != isTagged {
			t.Errorf("skill %q: relevance=%q, migrations-dir=%v — the two must agree", s.Path, s.Relevance, isMigrationDir)
		}
	}
}

// TestFilterDefaultRelevance_DropsMigrations pins the relevance gate
// itself, independent of what the registry happens to hold. Feeding it
// synthetic metas is deliberate: the shipped migration set is empty most
// of the time, and a gate that is only exercised when a migration happens
// to be in the tree is a gate that silently rots between releases.
func TestFilterDefaultRelevance_DropsMigrations(t *testing.T) {
	in := []skillMeta{
		{Path: "db"},
		{Path: "migrations/v0.5.0", Relevance: SkillRelevanceMigration},
		{Path: "frontend"},
	}
	got := filterDefaultRelevance(in)
	if len(got) != 2 {
		t.Fatalf("expected 2 steady-state skills, got %d: %+v", len(got), got)
	}
	for _, s := range got {
		if s.Relevance == SkillRelevanceMigration {
			t.Errorf("migration skill %q survived the default-relevance filter", s.Path)
		}
	}
}

// plantProjectMigrationSkill writes a project-scope skill carrying
// relevance=migration and returns the project root.
//
// The listing surfaces are scope-agnostic about relevance, so a
// project-scope skill exercises the same gate a forge-shipped migration
// would — and it keeps these tests working across the long stretches when
// forge ships no migrations at all. Pinning them to a specific shipped
// skill is what made the previous versions of these tests die with the
// registry.
func plantProjectMigrationSkill(t *testing.T, path, body string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".forge", "skills", filepath.FromSlash(path))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return root
}

const testMigrationSkillBody = `---
name: v0.5.0
description: the deploy target moved from forge.yaml into KCL
relevance: migration
version: v0.5.0
---

# Migrating to v0.5.0
`

// TestListSkillsAtExcludesMigrationsByDefault covers the default listing
// surface (what reliant's cli.ListSkills sees) and the opt-in.
func TestListSkillsAtExcludesMigrationsByDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate from ~/.forge/skills
	root := plantProjectMigrationSkill(t, "migrations/v0.5.0", testMigrationSkillBody)

	byDefault, err := ListSkillsAt(root)
	if err != nil {
		t.Fatalf("ListSkillsAt: %v", err)
	}
	for _, m := range byDefault {
		if m.Relevance == SkillRelevanceMigration {
			t.Errorf("default listing contains migration skill %q", m.Path)
		}
		if m.Path == "migrations/v0.5.0" {
			t.Error("default listing contains the planted migration skill")
		}
	}

	withOpt, err := ListSkillsAtWithOptions(root, SkillListOptions{IncludeMigrations: true})
	if err != nil {
		t.Fatalf("ListSkillsAtWithOptions: %v", err)
	}
	if len(withOpt) <= len(byDefault) {
		t.Errorf("opt-in listing (%d) not larger than default (%d) — migrations missing", len(withOpt), len(byDefault))
	}
	var found *SkillMetaPublic
	for i := range withOpt {
		if withOpt[i].Path == "migrations/v0.5.0" {
			found = &withOpt[i]
			break
		}
	}
	if found == nil {
		t.Fatal("opt-in listing lacks the planted migrations/v0.5.0 skill")
	}
	if found.Relevance != SkillRelevanceMigration {
		t.Errorf("Relevance = %q, want migration", found.Relevance)
	}
}

// TestListSkillsAtExposesMigrationVersion verifies the release version
// passes through to the public listing, so a consumer doing its own
// version gating gets the same number forge gates on.
func TestListSkillsAtExposesMigrationVersion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := plantProjectMigrationSkill(t, "migrations/v0.5.0", testMigrationSkillBody)

	metas, err := ListSkillsAtWithOptions(root, SkillListOptions{IncludeMigrations: true})
	if err != nil {
		t.Fatalf("ListSkillsAtWithOptions: %v", err)
	}
	for _, m := range metas {
		if m.Path != "migrations/v0.5.0" {
			continue
		}
		if m.Version != "v0.5.0" {
			t.Errorf("Version = %q, want v0.5.0", m.Version)
		}
		return
	}
	t.Fatal("migrations/v0.5.0 not in opt-in listing")
}

// TestMigrationSkillStillLoadableByPath pins the on-demand escape hatch:
// `forge skill load migrations/<id>` (and forge project upgrade list's
// "To load" hint) must keep working even though listings hide migration
// skills. That hint is the only way an agent reaches a migration, so the
// path resolving is load-bearing.
func TestMigrationSkillStillLoadableByPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := plantProjectMigrationSkill(t, "migrations/v0.5.0", testMigrationSkillBody)

	body, scope, err := ResolveSkillContentAt(root, "migrations/v0.5.0")
	if err != nil {
		t.Fatalf("ResolveSkillContentAt: %v", err)
	}
	if scope != SkillScopeProject {
		t.Errorf("scope = %q, want project", scope)
	}
	if !strings.Contains(string(body), "Migrating to v0.5.0") {
		t.Error("loaded migration skill body looks wrong")
	}
}
