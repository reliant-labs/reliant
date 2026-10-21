// Copyright (c) 2025 Reliant Labs
package daemonruntime

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

// Fixture repos are built once per test binary and copied per test.
//
// Every git process is a fork and an exec, and on a loaded machine that is
// hundreds of milliseconds each. A fixture repo is three to seven of them,
// identical in every test that asks for it, so the git work is done once
// (under TestMain's testRoot) and each test gets a byte copy, which costs no
// process at all. A plain repository's .git records no absolute path, so the
// copy is a complete, independent repo.

type repoTemplate struct {
	once sync.Once
	dir  string
	err  error
}

var repoTemplates struct {
	sync.Mutex
	byKey map[string]*repoTemplate
}

// copyOfRepoTemplate returns a fresh copy, at t.TempDir()/repo, of the repo
// build creates in an empty directory. build runs once per key per binary, so
// one key must always mean the same repo.
func copyOfRepoTemplate(t *testing.T, key string, build func(dir string) error) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "repo")
	repoFromTemplate(t, dst, key, build)
	return dst
}

// repoFromTemplate is copyOfRepoTemplate into dst, which must be absent or
// empty.
func repoFromTemplate(t *testing.T, dst, key string, build func(dir string) error) {
	t.Helper()
	repoTemplates.Lock()
	if repoTemplates.byKey == nil {
		repoTemplates.byKey = map[string]*repoTemplate{}
	}
	tmpl := repoTemplates.byKey[key]
	if tmpl == nil {
		tmpl = &repoTemplate{}
		repoTemplates.byKey[key] = tmpl
	}
	repoTemplates.Unlock()

	tmpl.once.Do(func() {
		dir, err := os.MkdirTemp(testRoot, "repo-template-")
		if err == nil {
			err = build(dir)
		}
		tmpl.dir, tmpl.err = dir, err
	})
	if tmpl.err != nil {
		t.Fatalf("build fixture repo %q: %v", key, tmpl.err)
	}
	if err := copyTree(tmpl.dir, dst); err != nil {
		t.Fatalf("copy fixture repo %q: %v", key, err)
	}
}

// fixtureGit runs git in dir with a fixed config and identity, for building
// a template: whichever test builds it first may have pointed the process's
// git config anywhere. The fixed config has no automatic maintenance, whose
// detached process would otherwise be adding and removing a lock file in the
// template while tests copy it (noAutoMaintenanceConfig).
func fixtureGit(dir string, args ...string) error {
	cmd := exec.Command("git", noAutoMaintenance(args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %v\n%s", args, err, out)
	}
	return nil
}

// buildCommittedRepo is a template build: a repo on main with one commit of
// file (content), then the named branches.
func buildCommittedRepo(file, content string, branches ...string) func(dir string) error {
	return func(dir string) error {
		if err := fixtureGit(dir, "init", "-q", "-b", "main"); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
			return err
		}
		if err := fixtureGit(dir, "add", "."); err != nil {
			return err
		}
		if err := fixtureGit(dir, "commit", "-q", "-m", "init"); err != nil {
			return err
		}
		for _, b := range branches {
			if err := fixtureGit(dir, "branch", b); err != nil {
				return err
			}
		}
		return nil
	}
}

// Pins noAutoMaintenanceConfig: neither a template build nor a test's own git
// under TestMain's config starts `git maintenance run` after a commit. Every
// git version traces the run it starts (2.43 in the foreground, 2.5x with
// --detach), so this holds the line on any git, not only the ones that race.
func TestTestGitRunsNoAutoMaintenance(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	// An absolute GIT_TRACE is a file every git process appends its trace to.
	traced := func(t *testing.T, run func(dir string)) {
		t.Helper()
		trace := filepath.Join(t.TempDir(), "trace")
		t.Setenv("GIT_TRACE", trace)
		run(t.TempDir())
		got, err := os.ReadFile(trace)
		if err != nil {
			t.Fatalf("git wrote no trace: %v", err)
		}
		if !strings.Contains(string(got), "commit") {
			t.Fatalf("the trace shows no commit, so it proves nothing:\n%s", got)
		}
		if strings.Contains(string(got), "maintenance run") {
			t.Fatalf("a commit started automatic maintenance:\n%s", got)
		}
	}

	t.Run("template build", func(t *testing.T) {
		traced(t, func(dir string) {
			if err := buildCommittedRepo("a.txt", "a\n", "b")(dir); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("shared config", func(t *testing.T) {
		traced(t, func(dir string) {
			rcGit(t, dir, "init", "-q", "-b", "main")
			rcGit(t, dir, "commit", "-q", "--allow-empty", "-m", "x")
		})
	})
}

// copyTree copies the directories, regular files and symlinks under src to
// dst, keeping permissions.
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
			return copyFile(path, target, info.Mode().Perm())
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
