// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// newFixture's repos are built once per test binary and copied per test.
//
// Every git process is a fork and an exec, and on a loaded machine that is
// hundreds of milliseconds each. The fixture's bare origin and project with one
// pushed commit are seven of them, identical in every test, so the git work is
// done once and each test gets a byte copy, which costs no process at all. The
// one absolute path the copy carries, the project's origin URL, is rewritten
// to the copy's own origin.

var fixtureTemplate struct {
	once sync.Once
	dir  string // holds project/ and origin.git/
	err  error
}

// heavyUnderShort skips, under -short, a test that drives thirty or more git
// processes. Such a test runs in full mode (go test without -short: main and
// nightly CI, and before committing a change here).
//
// The package's cost is process spawns and nothing else: no test waits on a
// clock, and a tmpfs for the repos measured no faster. A git process costs
// ~70ms of wall time on a shared dev machine at ordinary load, so thirty is
// the repo's 2s line for a -short test; and with every heavy slot busy the
// machine sustains only ~30 execs a second across ALL its processes. Under
// -short the package ran ~3100 git processes and took 44-108s, then timed out
// at task test:affected's 60s with ~2100. The reaper's own git work per pass
// is already cut and pinned (gitbudget_test.go) — one full pass is ~31 — so
// what is left in -short is the tests below a full pass plus the two guards,
// TestGitProcessesPerPass and TestGitFileReadersAgreeWithGit.
func heavyUnderShort(t *testing.T, why string) {
	t.Helper()
	if testing.Short() {
		t.Skip(why + "; skipped under -short")
	}
}

// templateRoot is where templates are built; TestMain removes it.
var templateRoot string

func TestMain(m *testing.M) {
	code := m.Run()
	if templateRoot != "" {
		_ = os.RemoveAll(templateRoot)
	}
	os.Exit(code)
}

// copyFixtureTemplate fills base (empty) with project/ and origin.git/, the
// state newFixture starts every test from.
func copyFixtureTemplate(t *testing.T, base string) {
	t.Helper()
	fixtureTemplate.once.Do(func() {
		dir, err := os.MkdirTemp("", "worktreereclaim-template-")
		if err == nil {
			// Canonical for the same reason as newFixture's base: the URL
			// rewritten below must be byte-identical to what was written.
			dir, err = filepath.EvalSymlinks(dir)
		}
		if err == nil {
			templateRoot = dir
			err = buildFixtureTemplate(dir)
		}
		fixtureTemplate.dir, fixtureTemplate.err = dir, err
	})
	if fixtureTemplate.err != nil {
		t.Fatalf("build fixture template: %v", fixtureTemplate.err)
	}
	if err := copyTree(fixtureTemplate.dir, base); err != nil {
		t.Fatalf("copy fixture template: %v", err)
	}
	cfg := filepath.Join(base, "project", ".git", "config")
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	from, to := filepath.Join(fixtureTemplate.dir, "origin.git"), filepath.Join(base, "origin.git")
	if !strings.Contains(string(b), from) {
		t.Fatalf("fixture template config does not name its origin %s:\n%s", from, b)
	}
	if err := os.WriteFile(cfg, []byte(strings.ReplaceAll(string(b), from, to)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func buildFixtureTemplate(base string) error {
	project, origin := filepath.Join(base, "project"), filepath.Join(base, "origin.git")
	steps := []struct {
		dir  string
		args []string
	}{
		{base, []string{"init", "-q", "--bare", "-b", "main", origin}},
		{base, []string{"init", "-q", "-b", "main", project}},
	}
	for _, s := range steps {
		if err := templateGit(s.dir, s.args...); err != nil {
			return err
		}
	}
	// No background housekeeping: every commit, fetch and push would
	// otherwise spawn a `git maintenance run --auto` or `gc --auto` child, a
	// process each on a machine where a process costs tens of milliseconds.
	// Nothing the reaper decides depends on housekeeping having run.
	for _, repo := range []string{project, origin} {
		for _, kv := range [][2]string{{"maintenance.auto", "false"}, {"gc.auto", "0"}, {"receive.autoGC", "false"}} {
			if err := templateGit(repo, "config", kv[0], kv[1]); err != nil {
				return err
			}
		}
	}
	if err := os.WriteFile(filepath.Join(project, "a.txt"), []byte("one\n"), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"add", "."},
		{"commit", "-q", "-m", "init"},
		{"remote", "add", "origin", origin},
		{"push", "-q", "origin", "main"},
	} {
		if err := templateGit(project, args...); err != nil {
			return err
		}
	}
	return nil
}

// templateGit runs git the way run does, without a *testing.T: the template
// outlives whichever test happened to build it.
func templateGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return nil
}

// copyTree copies the directories, regular files and symlinks under src to
// dst, keeping permissions and modification times (so the copied index's
// recorded stat data still matches the files it describes).
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.Type().IsRegular():
			if err := copyFile(path, target, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chtimes(target, info.ModTime(), info.ModTime())
		default:
			return fmt.Errorf("copyTree: %s is neither a file, a directory nor a symlink", path)
		}
	})
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
