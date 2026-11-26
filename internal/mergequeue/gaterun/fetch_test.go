// Copyright (c) 2025 Reliant Labs
package gaterun

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitEnv() []string {
	return append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// candidateRemote is a directory of bare repositories holding acme/widgets
// with reliant-queue/b1 at a commit, which it returns.
func candidateRemote(t *testing.T) (base, sha string) {
	t.Helper()
	base = t.TempDir()
	bare := filepath.Join(base, "acme", "widgets.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, bare, "init", "-q", "--bare")
	work := t.TempDir()
	git(t, work, "init", "-q")
	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, work, "add", ".")
	git(t, work, "commit", "-q", "-m", "candidate")
	sha = git(t, work, "rev-parse", "HEAD")
	git(t, work, "push", "-q", bare, "HEAD:refs/heads/reliant-queue/b1")
	return base, sha
}

func TestGitFetcherChecksOutTheCandidateWithNoCredential(t *testing.T) {
	base, sha := candidateRemote(t)
	// The credential reaches git for the one fetch through its
	// environment, the way the daemon's helper config does.
	env := append(gitEnv(), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=!echo password=secret-token")
	f := GitFetcher{BaseURL: "file://" + base, Env: func() []string { return env }}
	dir := t.TempDir()
	if err := f.Fetch(context.Background(), FetchSpec{Repo: "acme/widgets", Ref: "reliant-queue/b1", SHA: sha, Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if got := git(t, dir, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("checked out %s, want %s", got, sha)
	}
	if _, err := os.Stat(filepath.Join(dir, "main.go")); err != nil {
		t.Fatalf("the tree is not checked out: %v", err)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if strings.Contains(string(cfg), "secret-token") || strings.Contains(string(cfg), "credential") || strings.Contains(string(cfg), "file://") {
		t.Fatalf("the fetched repository's config holds a remote or credential:\n%s", cfg)
	}
}

func TestGitFetcherRefusesAMovedCandidate(t *testing.T) {
	base, _ := candidateRemote(t)
	f := GitFetcher{BaseURL: "file://" + base, Env: gitEnv}
	err := f.Fetch(context.Background(), FetchSpec{Repo: "acme/widgets", Ref: "reliant-queue/b1", SHA: testSHA, Dir: t.TempDir()})
	if !errors.Is(err, ErrCandidateMoved) {
		t.Fatalf("a ref at another sha: %v", err)
	}
}

func TestCheckNoCredential(t *testing.T) {
	for _, cfg := range []string{
		"[credential]\n\thelper = store\n",
		"[http]\n\textraHeader = AUTHORIZATION: basic abc\n",
		"[remote \"origin\"]\n\turl = https://x-access-token:ghs_abc@github.com/acme/widgets.git\n",
	} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := checkNoCredential(dir); err == nil {
			t.Fatalf("config not refused:\n%s", cfg)
		}
	}
}
