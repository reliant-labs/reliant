// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// The file readers (gitfiles.go) stand in for git, so they are checked
// against it: in every state below, whatever they answer must be exactly
// what git answers, and where they decline the reaper asks git instead.
func TestGitFileReadersAgreeWithGit(t *testing.T) {
	testkit.Slow(t, "builds real git repositories and runs git against them")
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the file readers always defer to git on Windows")
	}
	f := newFixture(t)
	ctx := context.Background()
	locked := f.worktree("locked", "feat/locked")
	plain := f.worktree("plain", "feat/plain")
	run(t, f.project, "worktree", "unlock", plain.Path)
	noReason := f.worktree("noreason", "feat/noreason")
	run(t, f.project, "worktree", "unlock", noReason.Path)
	run(t, f.project, "worktree", "lock", noReason.Path)
	spaced := f.worktree("spaced", "feat/spaced")
	run(t, f.project, "worktree", "unlock", spaced.Path)
	run(t, f.project, "worktree", "lock", "--reason", "  keep: my experiment  ", spaced.Path)
	quoted := f.worktree("quoted", "feat/quoted")
	run(t, f.project, "worktree", "unlock", quoted.Path)
	run(t, f.project, "worktree", "lock", "--reason", "two\nlines \"and\" a \\", quoted.Path)
	moved := f.worktree("moved", "feat/moved")
	run(t, f.project, "worktree", "unlock", moved.Path)
	movedTo := filepath.Join(f.root, ".reclaim", "moved", "root-1-aa")
	if err := os.MkdirAll(filepath.Dir(movedTo), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, f.project, "worktree", "move", moved.Path, movedTo)
	pruned := f.worktree("pruned", "feat/pruned")
	run(t, f.project, "worktree", "unlock", pruned.Path)
	if err := os.RemoveAll(filepath.Join(f.project, ".git", "worktrees", "pruned")); err != nil {
		t.Fatal(err)
	}
	// A symlink to a checkout: git resolves it, so must the readers.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(locked.Path, link); err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]struct {
		path       string
		filesAgree bool // false: the readers must decline
	}{
		"locked with a reason":  {locked.Path, true},
		"unlocked":              {plain.Path, true},
		"locked with no reason": {noReason.Path, true},
		"reason with spaces":    {spaced.Path, true},
		"reason git quotes":     {quoted.Path, false},
		"moved to quarantine":   {movedTo, true},
		"admin directory gone":  {pruned.Path, false},
		"through a symlink":     {link, true},
		"main checkout":         {f.project, false},
	} {
		t.Run(name, func(t *testing.T) {
			wantInfo, wantErr := inspectLockWithGit(ctx, c.path)
			gotInfo, ok := lockFromFiles(c.path)
			if ok != c.filesAgree {
				t.Fatalf("lockFromFiles answered=%v, want %v (%+v; git says %+v, %v)", ok, c.filesAgree, gotInfo, wantInfo, wantErr)
			}
			if !ok {
				return
			}
			if wantErr != nil || gotInfo != wantInfo {
				t.Fatalf("files say %+v, git says %+v (%v)", gotInfo, wantInfo, wantErr)
			}
			wantCommon, err := commonDirWithGit(ctx, c.path)
			if gotCommon, ok := linkedCommonDir(c.path); !ok || err != nil || gotCommon != wantCommon {
				t.Fatalf("common dir: files %q (%v), git %q (%v)", gotCommon, ok, wantCommon, err)
			}
			wantAdmin, err := git(ctx, c.path, nil, "rev-parse", "--absolute-git-dir")
			if gotAdmin, ok := linkedAdminDir(c.path); !ok || err != nil || gotAdmin != wantAdmin {
				t.Fatalf("admin dir: files %q (%v), git %q (%v)", gotAdmin, ok, wantAdmin, err)
			}
		})
	}

	// HEAD: on a branch, and detached.
	detached := f.worktree("detached", "feat/detached")
	run(t, detached.Path, "checkout", "-q", "--detach")
	for name, path := range map[string]string{"on a branch": locked.Path, "detached": detached.Path} {
		t.Run("HEAD "+name, func(t *testing.T) {
			admin, ok := linkedAdminDir(path)
			if !ok {
				t.Fatal("not read as a linked worktree")
			}
			symbolic, sha, ok := headFromFiles(admin)
			if !ok {
				t.Fatal("headFromFiles declined an ordinary HEAD")
			}
			_, serr := git(ctx, path, nil, "symbolic-ref", "-q", "HEAD")
			if symbolic != (serr == nil) {
				t.Fatalf("files say symbolic=%v, git symbolic-ref says %v", symbolic, serr)
			}
			if !symbolic {
				if want := run(t, path, "rev-parse", "--verify", "HEAD^{commit}"); sha != want {
					t.Fatalf("detached HEAD: files %s, git %s", sha, want)
				}
			}
		})
	}
}
