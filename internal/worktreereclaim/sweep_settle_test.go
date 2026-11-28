package worktreereclaim

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitAdd runs `git worktree add path branch` in repo, as recreating a
// workspace's checkout does, and returns its output and error.
func gitAdd(repo, path, branch string) (string, error) {
	cmd := exec.Command("git", "worktree", "add", path, branch)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// obs 6d590acc: a workspace whose checkouts the machine sweep was reclaiming
// when it stopped could not be recreated: the parked copy under
// .reliant-reclaim still had the branch checked out, so `git worktree add`
// refused it. SettleParkedFor puts a parked copy back, or finishes an
// interrupted deletion so the checkout is recreated and its capture applied.
func TestSettleParkedForLetsTheCheckoutBeRecreated(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~50 git processes over real worktrees")
	requireOpenat2(t)
	ctx := context.Background()

	t.Run("parked before its lock goes home", func(t *testing.T) {
		t.Parallel()
		s := newSweepFixture(t)
		orig := s.plain(filepath.Join(s.tmp, "ws", "api"), "w5/facade-a")
		q := parkLikeACrash(t, s, orig, CaptureKey(orig), "")
		if out, err := gitAdd(s.project, orig, "w5/facade-a"); err == nil || !strings.Contains(out, reclaimQuarantineDir) {
			t.Fatalf("the fixture must reproduce the refusal: %v %s", err, out)
		}
		back, held := s.r.SettleParkedFor(ctx, s.project, orig, s.state)
		if !back || held != "" {
			t.Fatalf("got back=%v held=%q, want it moved back", back, held)
		}
		if info, err := inspectLock(ctx, orig); err != nil || !info.Registered {
			t.Fatalf("not registered at its path: %+v, %v", info, err)
		}
		if exists(q) {
			t.Fatal("the parked copy is still there")
		}
	})

	t.Run("an interrupted deletion is finished, then recreated", func(t *testing.T) {
		t.Parallel()
		s := newSweepFixture(t)
		orig := s.plain(filepath.Join(s.tmp, "ws", "web"), "w5/facade-b")
		write(t, filepath.Join(orig, "notes.txt"), "untracked, captured\n")
		q, _ := parkMidDeletion(t, s, orig)
		if out, err := gitAdd(s.project, orig, "w5/facade-b"); err == nil || !strings.Contains(out, reclaimQuarantineDir) {
			t.Fatalf("the fixture must reproduce the refusal: %v %s", err, out)
		}
		back, held := s.r.SettleParkedFor(ctx, s.project, orig, s.state)
		if back || held != "" {
			t.Fatalf("got back=%v held=%q, want the deletion finished", back, held)
		}
		if exists(q) || exists(q+".disposing") {
			t.Fatal("the remains are still there")
		}
		if out, err := gitAdd(s.project, orig, "w5/facade-b"); err != nil {
			t.Fatalf("recreating the checkout: %v\n%s", err, out)
		}
	})

	t.Run("nothing parked is a no-op", func(t *testing.T) {
		t.Parallel()
		s := newSweepFixture(t)
		orig := filepath.Join(s.tmp, "ws", "gone")
		if back, held := s.r.SettleParkedFor(ctx, s.project, orig, s.state); back || held != "" {
			t.Fatalf("got back=%v held=%q", back, held)
		}
	})
}

func TestSettleParkedForHoldsWhenDiscoveryFails(t *testing.T) {
	heavyUnderShort(t, "git processes over real worktrees")
	requireOpenat2(t)
	s := newSweepFixture(t)
	orig := s.plain(filepath.Join(s.tmp, "ws", "api"), "discovery-failure")
	q := parkLikeACrash(t, s, orig, CaptureKey(orig), "")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	// Leave repository resolution working, but make enumeration fail.
	write(t, filepath.Join(bin, "git"), "#!/bin/sh\ncase \"$*\" in\n*'worktree list --porcelain'*) exit 1 ;;\nesac\nexec \"$RELIANT_TEST_REAL_GIT\" \"$@\"\n")
	if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RELIANT_TEST_REAL_GIT", realGit)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx := context.Background()
	common := repoCommonDir(ctx, s.project)
	if common == "" {
		t.Fatal("repository resolution must still succeed")
	}
	if _, anchors := discoverWorktrees(ctx, []string{common}); len(anchors) != 0 {
		t.Fatal("the fixture must fail discovery")
	}
	back, held := s.r.SettleParkedFor(ctx, s.project, orig, s.state)
	if back || !strings.Contains(held, "could not enumerate worktrees") {
		t.Fatalf("got back=%v held=%q, want a discovery hold", back, held)
	}
	if !exists(q) || exists(orig) {
		t.Fatal("failed discovery must leave the parked checkout untouched")
	}
}
