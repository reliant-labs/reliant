// Copyright (c) 2025 Reliant Labs
package diskwatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// initRepo makes a repository with one commit and returns its canonical path.
func initRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "init", "-q", "-b", "main")
	gitT(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// worktreeMachine is a home whose Reliant worktree root holds a Reliant
// workspace of repo A, an agent's plain `git worktree add` of repo B straight
// under the root and a worktree nested in a workspace; plus a clone and a
// worktree of repo C that lives outside the root.
func worktreeMachine(t *testing.T) (home string, repoA, repoB string) {
	t.Helper()
	base := t.TempDir()
	home = filepath.Join(base, "home")
	root := filepath.Join(home, ".reliant", "worktrees")
	repoA = initRepo(t, filepath.Join(base, "projects", "a"))
	repoB = initRepo(t, filepath.Join(base, "projects", "b"))
	repoC := initRepo(t, filepath.Join(base, "projects", "c"))
	gitT(t, repoA, "worktree", "add", "-q", "-b", "ws", filepath.Join(root, "proj", "ws-1234abcd", "a"))
	gitT(t, repoB, "worktree", "add", "-q", "-b", "agent", filepath.Join(root, "agent-made"))
	gitT(t, repoB, "worktree", "add", "-q", "-b", "nested", filepath.Join(root, "proj", "other-5678abcd", "b-review"))
	gitT(t, repoC, "worktree", "add", "-q", "-b", "outside", filepath.Join(base, "tmp", "c-wt"))
	// A clone is a repository of its own, not a linked worktree.
	initRepo(t, filepath.Join(root, "proj", "clone-9999abcd", "c"))
	return home, repoA, repoB
}

func TestWorktreeRepos_FindsTheRepositoryOfEveryLinkedWorktreeUnderTheRoot(t *testing.T) {
	home, repoA, repoB := worktreeMachine(t)
	got := worktreeRepos(filepath.Join(home, ".reliant", "worktrees"))
	want := []string{repoA, repoB}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("worktreeRepos = %v, want %v", got, want)
	}
	if got := worktreeRepos(filepath.Join(home, "missing")); len(got) != 0 {
		t.Fatalf("a missing root found %v", got)
	}
}

func TestWorktreeReap_ReapsEveryRepositoryOnItsScheduleFromTheReaperTick(t *testing.T) {
	home, repoA, repoB := worktreeMachine(t)
	fs := &fakeFS{
		mounts: map[string]fsStat{home: {Dev: 8, Bsize: 4096, Blocks: 1 << 20, Bfree: 1 << 19, Bavail: 1 << 19, Files: 1000, Ffree: 900}},
		exists: map[string]bool{home: true},
	}
	env := Environment{Getenv: func(string) string { return "" }, Home: home, GOOS: "test"}
	now := time.Now()
	w := watcherOn(t, fs, env, &now)
	var mu sync.Mutex
	var reaped []string
	w.worktreeReap.run = func(_ context.Context, repo string) (reaperRun, error) {
		mu.Lock()
		defer mu.Unlock()
		reaped = append(reaped, repo)
		return reaperRun{Stdout: "removed worktree: x (abc)\n"}, nil
	}
	w.ConfigureReaper(ReaperConfig{Enabled: true, Interval: time.Hour, PressureInterval: 10 * time.Minute, PolicyPath: filepath.Join(t.TempDir(), "storage.json")})
	// The cache pass ran a moment ago, so this tick is the worktree reap's.
	w.reaper.lastRun = now
	ctx := context.Background()

	if !w.reaperTick(ctx) {
		t.Fatal("the first tick did not reap worktrees")
	}
	if want := []string{repoA, repoB}; !reflect.DeepEqual(reaped, want) {
		t.Fatalf("reaped %v, want %v", reaped, want)
	}
	now = now.Add(time.Hour + time.Minute)
	w.reaper.lastRun = now
	if w.reaperTick(ctx) || len(reaped) != 2 {
		t.Fatalf("reaped again before WorktreeReapInterval: %v", reaped)
	}
	now = now.Add(WorktreeReapInterval)
	w.reaper.lastRun = now
	if !w.reaperTick(ctx) || len(reaped) != 4 {
		t.Fatalf("did not reap again after WorktreeReapInterval: %v", reaped)
	}
}

func TestWorktreeReap_RunsForgesReaperWithRemovalOverTheRootsCheckouts(t *testing.T) {
	got := worktreeReapCommandArgs("/r")
	want := []string{"forge", "storage", "worktrees", "--repo", "/r", "--apply", "--reliant-managed"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	if _, err := execWorktreeReap(context.Background(), "/r"); err != errReaperUnderTest {
		t.Fatalf("execWorktreeReap under test = %v, want errReaperUnderTest", err)
	}
}
