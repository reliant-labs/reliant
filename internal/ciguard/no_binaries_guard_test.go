package ciguard

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// scripts/check-no-binaries.sh refuses a binary file, or a new file over the
// size cap, anywhere in the range it judges. ci.yml's pr-checks job runs it
// over HEAD^1..HEAD on every pull request; pre-commit's hook runs it over the staged
// change, which is empty in CI, so that hook alone never judged a pull
// request. These tests pin the workflow step and the script's verdicts:
// control-plane#1952 merged a 5.9 MB build binary through a merge commit
// while the same script read only staged files and relied on file(1).

var noBinariesScript = filepath.Join("..", "..", "scripts", "check-no-binaries.sh")

func TestNoBinariesGuardRunsOnEveryPullRequest(t *testing.T) {
	raw, err := os.ReadFile(ciWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	var wf stepsWorkflow
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	found := false
	for name, j := range wf.Jobs {
		for _, s := range j.Steps {
			if !strings.Contains(s.Run, "check-no-binaries.sh") {
				continue
			}
			found = true
			if name != "pr-checks" {
				t.Errorf("job %q runs the binary guard; it belongs in pr-checks, which every pull request runs, so no job is added", name)
			}
			if s.If != "" {
				t.Errorf("the binary guard step has if: %q; every pull request must run it", s.If)
			}
			if !strings.Contains(s.Run, "--range HEAD^1 HEAD") {
				t.Errorf("the binary guard step runs %q; it must judge --range HEAD^1 HEAD, the whole change of the merge commit", s.Run)
			}
		}
		if !found || name != "pr-checks" {
			continue
		}
		// The guard judges every commit against its parent, so the checkout
		// must carry the pull request's whole history, not depth 2.
		for _, s := range j.Steps {
			if strings.HasPrefix(s.Uses, "actions/checkout@") && s.With["fetch-depth"] != "0" {
				t.Errorf("pr-checks checks out with fetch-depth %q; the binary guard needs fetch-depth: 0", s.With["fetch-depth"])
			}
		}
	}
	if !found {
		t.Error("ci.yml does not run scripts/check-no-binaries.sh: a pull request could commit a binary again")
	}
}

// elfBytes is the start of a compiled Linux executable: magic, then NUL padding.
var elfBytes = "\x7fELF\x02\x01\x01" + strings.Repeat("\x00", 64) + "payload"

// binGuardRepo is a repo on branch feature, forked from main's one commit.
func binGuardRepo(t *testing.T) (dir string, git func(...string), write func(rel, content string)) {
	t.Helper()
	for _, bin := range []string{"bash", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	// Read by this process so the test cache sees an edit to the script.
	if _, err := os.ReadFile(noBinariesScript); err != nil {
		t.Fatal(err)
	}
	dir = t.TempDir()
	git = func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write = func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("README.md", "hello\n")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "feature")
	return dir, git, write
}

// runNoBinaries runs the script in repo and returns its output and exit code.
func runNoBinaries(t *testing.T, repo string, env []string, args ...string) (string, int) {
	t.Helper()
	abs, err := filepath.Abs(noBinariesScript)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(context.Background(), "bash", append([]string{abs}, args...)...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &ee):
		return string(out), ee.ExitCode()
	}
	t.Fatalf("run %s: %v", abs, err)
	return "", 0
}

func TestNoBinariesGuardVerdicts(t *testing.T) {
	testkit.Slow(t, "execs bash and git")
	t.Parallel()
	t.Run("a text-only change passes", func(t *testing.T) {
		t.Parallel()
		dir, git, write := binGuardRepo(t)
		write("a.go", "package a\n")
		write("README.md", "hello\nworld\n")
		git("add", "-A")
		git("commit", "-q", "-m", "text")
		if out, code := runNoBinaries(t, dir, nil, "--range", "main", "HEAD"); code != 0 {
			t.Fatalf("exit %d, want 0:\n%s", code, out)
		}
	})
	t.Run("a binary that arrives through a merge commit fails", func(t *testing.T) {
		t.Parallel()
		dir, git, write := binGuardRepo(t)
		git("checkout", "-q", "-b", "side", "main")
		write("tools/x/x", elfBytes)
		git("add", "-A")
		git("commit", "-q", "-m", "side adds a build output")
		git("checkout", "-q", "feature")
		git("merge", "-q", "--no-ff", "--no-edit", "side")
		out, code := runNoBinaries(t, dir, nil, "--range", "main", "HEAD")
		if code != 1 || !strings.Contains(out, "tools/x/x: binary file") {
			t.Fatalf("exit %d, want 1 naming tools/x/x:\n%s", code, out)
		}
	})
	t.Run("a new file over the cap fails; a grown existing one does not", func(t *testing.T) {
		t.Parallel()
		dir, git, write := binGuardRepo(t)
		write("data/dump.json", strings.Repeat("x", 2048)+"\n")
		write("README.md", strings.Repeat("x", 2048)+"\n")
		git("add", "-A")
		git("commit", "-q", "-m", "big")
		out, code := runNoBinaries(t, dir, []string{"MAX_NEW_FILE_BYTES=1024"}, "--range", "main", "HEAD")
		if code != 1 || !strings.Contains(out, "data/dump.json: new file of 2049 bytes") || strings.Contains(out, "README.md") {
			t.Fatalf("exit %d, want 1 naming data/dump.json only:\n%s", code, out)
		}
	})
	t.Run("a binary committed and deleted again in the branch fails", func(t *testing.T) {
		t.Parallel()
		dir, git, write := binGuardRepo(t)
		write("bin/tool", elfBytes)
		write("data/dump.json", strings.Repeat("x", 2048)+"\n")
		git("add", "-A")
		git("commit", "-q", "-m", "oops")
		git("rm", "-q", "bin/tool", "data/dump.json")
		git("commit", "-q", "-m", "remove it again")
		out, code := runNoBinaries(t, dir, []string{"MAX_NEW_FILE_BYTES=1024"}, "--range", "main", "HEAD")
		if code != 1 || !strings.Contains(out, "bin/tool: binary file") || !strings.Contains(out, "data/dump.json: new file of 2049 bytes") {
			t.Fatalf("exit %d, want 1 naming bin/tool and data/dump.json:\n%s", code, out)
		}
	})
	symlinkToELF := func(t *testing.T) (string, func(...string)) {
		t.Helper()
		dir, git, write := binGuardRepo(t)
		if err := os.Symlink("README.md", filepath.Join(dir, "tool")); err != nil {
			t.Skipf("cannot create a symlink: %v", err)
		}
		git("add", "-A")
		git("commit", "-q", "-m", "a symlink")
		if err := os.Remove(filepath.Join(dir, "tool")); err != nil {
			t.Fatal(err)
		}
		write("tool", elfBytes+strings.Repeat("\x00", 2048))
		git("add", "-A")
		return dir, git
	}
	t.Run("a symlink replaced by an executable fails in the staged mode", func(t *testing.T) {
		t.Parallel()
		dir, _ := symlinkToELF(t)
		out, code := runNoBinaries(t, dir, []string{"MAX_NEW_FILE_BYTES=1024"})
		if code != 1 || !strings.Contains(out, "tool: binary file") || !strings.Contains(out, "tool: new file of") {
			t.Fatalf("exit %d, want 1 naming tool as binary and over the cap:\n%s", code, out)
		}
	})
	t.Run("a symlink replaced by an executable fails in the range mode", func(t *testing.T) {
		t.Parallel()
		dir, git := symlinkToELF(t)
		git("commit", "-q", "-m", "symlink becomes a binary")
		out, code := runNoBinaries(t, dir, []string{"MAX_NEW_FILE_BYTES=1024"}, "--range", "HEAD^", "HEAD")
		if code != 1 || !strings.Contains(out, "tool: binary file") || !strings.Contains(out, "tool: new file of") {
			t.Fatalf("exit %d, want 1 naming tool as binary and over the cap:\n%s", code, out)
		}
	})
	t.Run("a shallow clone that stops inside the range fails loudly", func(t *testing.T) {
		t.Parallel()
		dir, git, write := binGuardRepo(t)
		for _, n := range []string{"1", "2"} {
			write("f"+n, n+"\n")
			git("add", "-A")
			git("commit", "-q", "-m", n)
		}
		// What actions/checkout's fetch-depth: 2 gives a pull request: the
		// merge commit and its parents, not the parents' history.
		git("checkout", "-q", "main")
		write("g", "main moved\n")
		git("add", "-A")
		git("commit", "-q", "-m", "main moved")
		git("merge", "-q", "--no-ff", "--no-edit", "feature")
		clone := t.TempDir()
		git("clone", "-q", "--depth", "2", "--branch", "main", "file://"+dir, clone)
		if out, code := runNoBinaries(t, clone, nil, "--range", "HEAD^1", "HEAD"); code != 2 || !strings.Contains(out, "shallow") {
			t.Fatalf("exit %d, want 2 naming the shallow clone:\n%s", code, out)
		}
	})
	t.Run("an allowlisted glob passes", func(t *testing.T) {
		t.Parallel()
		dir, git, write := binGuardRepo(t)
		write("docs/images/a/shot.png", "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
		write(".binary-allowlist", "# screenshots\ndocs/images/*.png\n")
		git("add", "-A")
		git("commit", "-q", "-m", "image")
		if out, code := runNoBinaries(t, dir, nil, "--range", "main", "HEAD"); code != 0 {
			t.Fatalf("exit %d, want 0:\n%s", code, out)
		}
	})
	t.Run("the staged mode judges the index", func(t *testing.T) {
		t.Parallel()
		dir, git, write := binGuardRepo(t)
		write("bin/tool", elfBytes)
		git("add", "-A")
		if out, code := runNoBinaries(t, dir, nil); code != 1 || !strings.Contains(out, "bin/tool: binary file") {
			t.Fatalf("exit %d, want 1:\n%s", code, out)
		}
	})
	t.Run("a base that does not resolve fails loudly", func(t *testing.T) {
		t.Parallel()
		dir, _, _ := binGuardRepo(t)
		if out, code := runNoBinaries(t, dir, nil, "--range", "no-such-ref", "HEAD"); code != 2 || !strings.Contains(out, "refusing to pass") {
			t.Fatalf("exit %d, want 2:\n%s", code, out)
		}
	})
	t.Run("no git on PATH fails loudly", func(t *testing.T) {
		t.Parallel()
		dir, _, _ := binGuardRepo(t)
		bash, err := exec.LookPath("bash")
		if err != nil {
			t.Skip("needs bash")
		}
		abs, err := filepath.Abs(noBinariesScript)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(context.Background(), bash, abs, "--range", "main", "HEAD")
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + t.TempDir(), "HOME=" + t.TempDir()}
		out, err := cmd.CombinedOutput()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 2 || !strings.Contains(string(out), "git is not on PATH") {
			t.Fatalf("%v, want exit 2 naming git:\n%s", err, out)
		}
	})
}
