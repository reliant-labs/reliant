package testcapture

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/forge/pkg/testreport"
)

const (
	fakeCommit = "0123456789abcdef0123456789abcdef01234567"
	fakeTree   = "fedcba9876543210fedcba9876543210fedcba98"
)

// fakeBegin is begin with the checkout's git facts stated, so a test runs no
// git.
func fakeBegin(root string, env map[string]string) BeginOptions {
	return BeginOptions{
		Dir:          root,
		ProjectRoot:  root,
		ForgeVersion: "v1.2.3",
		Getenv:       envOf(env),
		Now:          func() time.Time { return time.Date(2026, 11, 21, 1, 2, 3, 0, time.UTC) },
		Provenance: func(context.Context, string) (release.Provenance, error) {
			return release.Provenance{
				Commit: fakeCommit, Tree: fakeTree, Branch: "main", Repo: "github.com/acme/shop",
				Worktree: release.Worktree{Path: root, Host: "h1"},
			}, nil
		},
		ResolveRev: func(_ context.Context, _, rev string) (string, error) {
			if rev == "origin/main" {
				return "baseba5e0000000000000000000000000000baee", nil
			}
			return "", errors.New("not a commit here")
		},
	}
}

func TestBeginWritesARunUnderTheProject(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: shop\nforge_version: v1.2.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := fakeBegin(root, map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_RUN_ID": "42", "GITHUB_REPOSITORY": "acme/shop"})
	opts.Base = "origin/main"
	res := Begin(context.Background(), opts)
	if !res.Recording || res.Reused || len(res.Warnings) > 0 {
		t.Fatalf("Begin = %+v", res)
	}
	if filepath.Dir(res.Dir) != testreport.RunsRoot(root) {
		t.Fatalf("run dir %s is not under %s", res.Dir, testreport.RunsRoot(root))
	}
	m, err := testreport.ReadRunManifest(res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != filepath.Base(res.Dir) || m.Commit != fakeCommit || m.TreeHash != fakeTree || m.Dirty ||
		m.Branch != "main" || m.BaseSHA != "baseba5e0000000000000000000000000000baee" || m.Project != "shop" ||
		m.ForgeVersion != "v1.2.3" || m.Host != "h1" || m.Repo != "github.com/acme/shop" ||
		m.CI == nil || m.CI.RunID != "42" || m.Lanes == nil || len(m.Lanes) != 0 ||
		!m.CreatedAt.Equal(time.Date(2026, 11, 21, 1, 2, 3, 0, time.UTC)) {
		t.Fatalf("run.json = %+v (ci %+v)", m, m.CI)
	}
	// The records keep themselves out of git, whatever the project ignores.
	if data, err := os.ReadFile(filepath.Join(root, ".forge", "tests", ".gitignore")); err != nil || !strings.Contains(string(data), "*") {
		t.Fatalf(".forge/tests/.gitignore = %q, %v", data, err)
	}
}

func TestBeginReusesTheRunFORGE_TEST_RUNNames(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	first := Begin(context.Background(), fakeBegin(root, nil))
	before, err := os.ReadFile(filepath.Join(first.Dir, testreport.RunManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	opts := fakeBegin(root, map[string]string{EnvRun: first.Dir})
	opts.Provenance = func(context.Context, string) (release.Provenance, error) {
		t.Error("a reused run read git again")
		return release.Provenance{}, nil
	}
	again := Begin(context.Background(), opts)
	if again.Dir != first.Dir || !again.Reused || !again.Recording {
		t.Fatalf("Begin with %s set = %+v, want %s reused", EnvRun, again, first.Dir)
	}
	after, _ := os.ReadFile(filepath.Join(first.Dir, testreport.RunManifestFile))
	if string(after) != string(before) {
		t.Fatal("reusing the run rewrote its run.json")
	}
	runs, _ := os.ReadDir(testreport.RunsRoot(root))
	if len(runs) != 1 {
		t.Fatalf("%d runs, want the one", len(runs))
	}
}

func TestBeginCreatesTheRunWhereFORGE_TEST_RUNPoints(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	chosen := filepath.Join(t.TempDir(), "ci-run")
	res := Begin(context.Background(), fakeBegin(root, map[string]string{EnvRun: chosen}))
	if res.Dir != chosen || !res.Recording || res.Reused {
		t.Fatalf("Begin = %+v, want a new run at %s", res, chosen)
	}
	if _, err := testreport.ReadRunManifest(chosen); err != nil {
		t.Fatal(err)
	}
}

func TestBeginWithTestsOffWritesNothing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	res := Begin(context.Background(), fakeBegin(root, map[string]string{EnvTests: "off"}))
	if res.Recording || res.Dir == "" || strings.HasPrefix(res.Dir, root) {
		t.Fatalf("Begin with %s=off = %+v, want a scratch directory outside the project", EnvTests, res)
	}
	t.Cleanup(func() { _ = os.RemoveAll(res.Dir) })
	if _, err := os.Stat(filepath.Join(root, ".forge")); !os.IsNotExist(err) {
		t.Fatalf("FORGE_TESTS=off wrote into the project: %v", err)
	}
}

// begin runs in front of a lane, so a run it cannot create still yields a
// directory: a lane told to write its coverage there must not fail for it.
func TestBeginFallsBackToAScratchDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	blocker := filepath.Join(root, ".forge")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := fakeBegin(root, nil)
	opts.Provenance = func(context.Context, string) (release.Provenance, error) {
		return release.Provenance{}, errors.New("git is broken")
	}
	res := Begin(context.Background(), opts)
	t.Cleanup(func() { _ = os.RemoveAll(res.Dir) })
	if res.Recording || res.Dir == "" || len(res.Warnings) < 2 {
		t.Fatalf("Begin = %+v, want a scratch directory and the warnings", res)
	}
	if fi, err := os.Stat(res.Dir); err != nil || !fi.IsDir() {
		t.Fatalf("scratch directory %s: %v", res.Dir, err)
	}
}

func TestBeginRecordsAGitCheckout(t *testing.T) {
	testkit.Slow(t, "runs git against a throwaway repository")
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "trunk")
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")
	opts := BeginOptions{Dir: root, Getenv: envOf(nil), Base: "HEAD"}
	for i := range 2 {
		// The second run proves the first left the checkout clean.
		res := Begin(context.Background(), opts)
		if !res.Recording || len(res.Warnings) > 0 {
			t.Fatalf("run %d: %+v", i, res)
		}
		m, err := testreport.ReadRunManifest(res.Dir)
		if err != nil {
			t.Fatal(err)
		}
		if m.Commit != git("rev-parse", "HEAD") || m.TreeHash != git("rev-parse", "HEAD^{tree}") || m.Dirty ||
			m.Branch != "trunk" || m.BaseSHA != m.Commit || m.Project != "shop" {
			t.Fatalf("run %d: run.json = %+v", i, m)
		}
	}
}
