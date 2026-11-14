//go:build !windows

// scripts/ci-changed-packages.sh is bash, and this test drives it with real
// git and go over a throwaway module. It runs on Windows in CI (Git Bash), but
// the test's shell fixtures (a fake `go`) are POSIX, so the file builds for
// every OS but Windows rather than skipping there at run time.

package ciguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

var (
	changedPackagesScript = filepath.Join("..", "..", "scripts", "ci-changed-packages.sh")
	testPackagesScript    = filepath.Join("..", "..", "scripts", "ci-test-packages.sh")
)

// TestChangedPackagesPlan runs scripts/ci-changed-packages.sh over a
// throwaway module named like forge's, so its guard list applies: a changed
// package and its direct importers (from code and from tests) are reached, a
// package two imports away is not, a testdata file reaches the package that
// owns it, docs reach nothing, a guard is dropped, go.sum reaches every
// package, and a failing go list fails the script instead of reading as
// "nothing to test".
func TestChangedPackagesPlan(t *testing.T) {
	if testing.Short() {
		t.Skip("runs scripts/ci-changed-packages.sh with real git and go list over a throwaway module; runs in task test")
	}
	for _, tool := range []string{"bash", "git", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s not on PATH", tool)
		}
	}
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for dst, src := range map[string]string{
		"scripts/ci-changed-packages.sh": changedPackagesScript,
		"scripts/ci-test-packages.sh":    testPackagesScript,
	} {
		body, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		write(dst, string(body))
	}
	run := func(env []string, args ...string) (string, error) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t",
			"GIT_COMMITTER_EMAIL=t@t", "GOFLAGS=-mod=mod", "GOWORK=off")
		cmd.Env = goexec.WithTrimpath(append(cmd.Env, env...))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	sh := func(args ...string) string {
		t.Helper()
		out, err := run(nil, args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return out
	}
	commit := func(msg string) {
		t.Helper()
		sh("git", "add", "-A")
		sh("git", "commit", "-qm", msg)
	}
	const mod = "github.com/reliant-labs/forge"
	plan := func() []string {
		t.Helper()
		var got []string
		for _, l := range strings.Split(strings.TrimSpace(sh("bash", "scripts/ci-changed-packages.sh", "HEAD^1")), "\n") {
			if l != "" {
				got = append(got, strings.TrimPrefix(strings.TrimPrefix(l, mod), "/"))
			}
		}
		return got
	}

	write("go.mod", "module "+mod+"\n\ngo 1.22\n")
	write("a/a.go", "package a\n\nconst A = 1\n")
	write("b/b.go", "package b\n\nimport \""+mod+"/a\"\n\nconst B = a.A\n")
	write("c/c.go", "package c\n\nimport \""+mod+"/b\"\n\nconst C = b.B\n")
	write("d/d.go", "package d\n")
	write("d/d_test.go", "package d_test\n\nimport \""+mod+"/a\"\n\nvar _ = a.A\n")
	write("x/x.go", "package x\n")
	write("x/testdata/f.txt", "v1\n")
	write("internal/removalguard/g.go", "package removalguard\n\nimport \""+mod+"/a\"\n\nvar _ = a.A\n")
	write("docs/d.md", "v1\n")
	sh("git", "init", "-q", "-b", "main")
	commit("base")

	write("a/a.go", "package a\n\nconst A = 2\n")
	write("x/testdata/f.txt", "v2\n")
	write("docs/d.md", "v2\n")
	commit("change")
	if got, want := plan(), []string{"a", "b", "d", "x"}; !slices.Equal(got, want) {
		t.Errorf("a change to a and x's testdata reached %q, want %q (c is two imports away; the guard is dropped)", got, want)
	}

	write("docs/d.md", "v3\n")
	write(".github/workflows/w.yml", "on: push\n")
	commit("docs")
	if got := plan(); len(got) != 0 {
		t.Errorf("a docs and workflow change reached %q, want nothing", got)
	}

	write("go.sum", "\n")
	commit("deps")
	if got, want := plan(), []string{"a", "b", "c", "d", "x"}; !slices.Equal(got, want) {
		t.Errorf("a go.sum change reached %q, want every package but the guard %q", got, want)
	}

	// A go list that fails (a toolchain it cannot load) fails the script; it
	// never reads as "nothing to test".
	badGo := t.TempDir()
	if err := os.WriteFile(filepath.Join(badGo, "go"), []byte("#!/bin/sh\necho 'go: toolchain not available' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := run([]string{"PATH=" + badGo + ":" + os.Getenv("PATH")}, "bash", "scripts/ci-changed-packages.sh", "HEAD^1")
	if err == nil || !strings.Contains(out, "failed") {
		t.Errorf("a failing go list: err=%v, want the script to fail:\n%s", err, out)
	}
}
