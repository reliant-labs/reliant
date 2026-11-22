package cli

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHistoryResolvePackedPrefix(t *testing.T) {
	if testing.Short() {
		t.Skip("creates and packs a real git repository; runs in task test")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "feat: first")
	first := git("rev-parse", "HEAD")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "fix: second")
	second := git("rev-parse", "HEAD")
	// A ref named like the first commit's abbreviation must not shadow it.
	git("update-ref", "refs/tags/"+first[:12], second)
	git("gc")
	h := gitHistory{dir: filepath.Join(dir, ".git")}
	if got := h.Resolve(first[:12]); got != first {
		t.Fatalf("Resolve packed prefix = %q, want %q", got, first)
	}
	if got := h.Resolve("000000000000"); got != "" {
		t.Fatalf("Resolve missing prefix = %q", got)
	}
	got := crossedRange("v0.0.0-20261119000001-"+first[:12], "v0.0.0-20261119000002-"+second[:12], nil, h)
	if len(got) != 1 || got[0].Source != "git" || got[0].Commits == nil || *got[0].Commits != 1 || len(got[0].Fixed) != 1 {
		t.Fatalf("packed no-base range = %+v", got)
	}
}
