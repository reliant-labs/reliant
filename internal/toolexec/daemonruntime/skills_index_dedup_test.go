// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func writeTestSkill(t *testing.T, dir, name, body string) {
	t.Helper()
	skillDir := filepath.Join(dir, ".reliant", "skills", name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + name + " skill\n---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeClone makes dir look like its own git repository to repo discovery: a
// .git DIRECTORY, as a separate clone has.
//
// Not a linked worktree (a .git FILE naming a shared gitdir): repo.Discover
// collapses every checkout of one repository into one source (#648), so
// linked worktrees never reach the index as separate sources and cannot
// exercise this dedup. Separate clones of one repo still do — each is its own
// repository, carrying the same skills.
func writeClone(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// A workspace holding several checkouts of one repo indexed every skill once
// per checkout: the prod workspace of ~30 checkouts synced 23.6 MB of skills,
// most of it forge's embedded catalog repeated per nested repo. The synced
// catalog is addressed by path, so only the first copy was ever loadable.
// Discovery now collapses linked worktrees (#648); separate clones of one
// repo remain distinct sources, and this is what keeps them from repeating
// every skill.
//
// Identical copies are indexed once; a same-path skill whose content differs
// is still indexed, and a skill only one checkout has is still there.
func TestBuildSkillsIndex_IndexesEachDistinctSkillOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("enumerates forge's embedded skill catalog once per checkout (~2s); runs in the full lane")
	}
	root := t.TempDir()
	writeTestSkill(t, root, "deploy", "Deploy with the house script.")
	checkouts := []string{"app", "app-feature-a", "app-feature-b"}
	for _, checkout := range checkouts {
		dir := filepath.Join(root, checkout)
		writeClone(t, dir)
		writeTestSkill(t, dir, "deploy", "Deploy with the house script.")
	}
	// Every clone must be its own source, or the assertions below would be
	// about discovery rather than about the index.
	if sources := discoverRepoSources(context.Background(), root); len(sources) != len(checkouts) {
		t.Fatalf("discovery found sources %v, want the %d clones %v", sources, len(checkouts), checkouts)
	}
	writeTestSkill(t, filepath.Join(root, "app-feature-b"), "migrate", "Only this branch has it.")
	// Same path, edited on one branch: a different skill, not a repeat.
	writeTestSkill(t, filepath.Join(root, "app-feature-a"), "deploy-canary", "v1")
	writeTestSkill(t, filepath.Join(root, "app-feature-b"), "deploy-canary", "v2, edited")

	skills, _ := buildSkillsIndex(root)

	type identity struct{ path, scope, hash string }
	seen := map[identity]string{}
	byPath := map[string]int{}
	for _, s := range skills {
		id := identity{s.GetSkillPath(), s.GetScope(), s.GetContentHash()}
		if prev, dup := seen[id]; dup {
			t.Errorf("skill %q (%s) indexed twice: from source %q and %q", s.GetSkillPath(), s.GetScope(), prev, s.GetSource())
		}
		seen[id] = s.GetSource()
		byPath[s.GetSkillPath()]++
	}

	if byPath["deploy"] != 1 {
		t.Errorf("deploy: %d copies indexed, want 1 (root + three identical checkouts)", byPath["deploy"])
	}
	// The copy kept is the first in catalog order — the one a load by path
	// already resolved to — which is the project root's when it has one.
	if src := seen[identity{"deploy", "project", findHash(skills, "deploy")}]; src != "" {
		t.Errorf("the deploy copy kept is from source %q, want the project root's", src)
	}
	if byPath["migrate"] != 1 {
		t.Errorf("migrate (one checkout only): %d copies, want 1", byPath["migrate"])
	}
	if byPath["deploy-canary"] != 2 {
		t.Errorf("deploy-canary: %d copies, want 2 (the two checkouts' contents differ)", byPath["deploy-canary"])
	}
	// forge's general methodology skills come from its embedded catalog,
	// identically for every checkout.
	if byPath["debug"] > 1 {
		t.Errorf("forge's debug skill indexed %d times, want once", byPath["debug"])
	}
}

func findHash(skills []*reliantv1.IndexedSkill, path string) string {
	for _, s := range skills {
		if s.GetSkillPath() == path {
			return s.GetContentHash()
		}
	}
	return ""
}
