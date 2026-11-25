// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

func TestParsePorcelainZ(t *testing.T) {
	// A modified file, a rename (followed by its source path, which is not a
	// second change) and an untracked file.
	out := []byte(" M b.go\x00R  new.go\x00old.go\x00?? a.txt\x00")
	paths, count := parsePorcelainZ(out, 20)
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}
	if want := []string{"a.txt", "b.go", "new.go"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	// The count stays whole when the names are capped.
	paths, count = parsePorcelainZ(out, 1)
	if count != 3 || len(paths) != 1 {
		t.Fatalf("capped = %v, %d; want one path of 3", paths, count)
	}
}

func TestExtraWorktrees(t *testing.T) {
	out := []byte("worktree /p/main\nHEAD abc\nbranch refs/heads/main\n\n" +
		"worktree /w/feature\nHEAD def\nbranch refs/heads/feature/x\n\n" +
		"worktree /w/detached\nHEAD 123\ndetached\n")
	got := extraWorktrees(out)
	want := []unmovedWorktree{{Path: "/w/feature", Branch: "feature/x"}, {Path: "/w/detached"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("extraWorktrees = %+v, want %+v", got, want)
	}
	if got := extraWorktrees([]byte("worktree /p/main\nHEAD abc\nbranch refs/heads/main\n")); got != nil {
		t.Fatalf("a checkout with no other worktree = %+v, want none", got)
	}
}

// TestGitUnmovedWork_ReportsWhatAFreshCloneWouldNotCarry builds a checkout
// with a nested repository and every kind of work a fresh clone elsewhere
// leaves behind, and reads it back through the command.
func TestGitUnmovedWork_ReportsWhatAFreshCloneWouldNotCarry(t *testing.T) {
	testkit.Slow(t, "creates real git checkouts, a stash and a worktree on disk")
	setGitCloneTestEnv(t)
	origin := newTestOriginRepo(t)
	root := filepath.Join(t.TempDir(), "project")
	gitRun(t, "", "clone", origin, root)
	nested := filepath.Join(root, "forge")
	gitRun(t, "", "clone", origin, nested)

	// Root: an uncommitted edit, an untracked file, a stash, a local-only
	// branch with one commit, and a worktree.
	writeFile(t, filepath.Join(root, "README.md"), "edited\n")
	writeFile(t, filepath.Join(root, "notes.txt"), "draft\n")
	writeFile(t, filepath.Join(root, "stashed.txt"), "stash me\n")
	gitRun(t, root, "add", "stashed.txt")
	gitRun(t, root, "stash", "push", "-m", "wip", "--", "stashed.txt")
	gitRun(t, root, "branch", "local-only")
	gitRun(t, root, "worktree", "add", filepath.Join(t.TempDir(), "wt"), "local-only")
	// One commit on local-only, made in the worktree.
	wtPath := worktreePathOf(t, root)
	writeFile(t, filepath.Join(wtPath, "feature.txt"), "feature\n")
	gitRun(t, wtPath, "add", "feature.txt")
	gitRun(t, wtPath, "commit", "-m", "feature")

	payload, _ := json.Marshal(gitUnmovedWorkRequest{Path: root, Repos: []string{"", "forge", "missing"}})
	raw, err := handleGitUnmovedWork(context.Background(), payload)
	if err != nil {
		t.Fatalf("git.unmoved_work: %v", err)
	}
	var resp gitUnmovedWorkResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Repos) != 3 {
		t.Fatalf("repos = %+v, want 3", resp.Repos)
	}

	// The nested clone is itself untracked in the root's status ("forge/"),
	// which is also work a fresh clone of the root does not carry.
	rootRepo := resp.Repos[0]
	if rootRepo.Error != "" {
		t.Fatalf("root: unexpected error %q", rootRepo.Error)
	}
	if rootRepo.ChangedFileCount != 3 || !reflect.DeepEqual(rootRepo.ChangedFiles, []string{"README.md", "forge/", "notes.txt"}) {
		t.Errorf("root changes = %d %v, want README.md, forge/ and notes.txt", rootRepo.ChangedFileCount, rootRepo.ChangedFiles)
	}
	if rootRepo.StashCount != 1 {
		t.Errorf("root stashes = %d, want 1", rootRepo.StashCount)
	}
	if want := []unmovedBranch{{Name: "local-only", Commits: 1}}; !reflect.DeepEqual(rootRepo.UnpushedBranches, want) {
		t.Errorf("root unpushed = %+v, want %+v", rootRepo.UnpushedBranches, want)
	}
	if len(rootRepo.Worktrees) != 1 || rootRepo.Worktrees[0].Branch != "local-only" {
		t.Errorf("root worktrees = %+v, want the local-only worktree", rootRepo.Worktrees)
	}

	// The nested repository is clean: nothing of it stays behind.
	clean := resp.Repos[1]
	if clean.Error != "" || clean.ChangedFileCount != 0 || len(clean.UnpushedBranches) != 0 || clean.StashCount != 0 || len(clean.Worktrees) != 0 {
		t.Errorf("clean nested repo = %+v, want nothing left behind", clean)
	}
	// A repository that is not on disk says so, and the others still report.
	if resp.Repos[2].Error == "" {
		t.Errorf("missing repo = %+v, want an error naming it", resp.Repos[2])
	}

	// It wrote nothing: the root's status is what it was.
	again, _ := exec.Command("git", "-C", root, "status", "--porcelain").Output()
	if len(again) == 0 {
		t.Fatalf("the checkout's changes disappeared")
	}
}

func TestGitUnmovedWork_RefusesAPathNotOnTheMachine(t *testing.T) {
	payload, _ := json.Marshal(gitUnmovedWorkRequest{Path: filepath.Join(t.TempDir(), "nope")})
	if _, err := handleGitUnmovedWork(context.Background(), payload); err == nil {
		t.Fatal("a checkout path that is not a directory must fail the command")
	}
}

// worktreePathOf is the path of root's only extra worktree.
func worktreePathOf(t *testing.T, root string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	wts := extraWorktrees(out)
	if len(wts) != 1 {
		t.Fatalf("worktrees = %+v", wts)
	}
	return wts[0].Path
}
