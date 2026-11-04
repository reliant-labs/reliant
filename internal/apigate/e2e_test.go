package main

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// A fixture module under testdata/<case>/{old,new}: the same pkg/widget at the
// merge base and in the PR.
func copyFixtureTree(t *testing.T, dst, src string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir,
		"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// prRepo commits the case's old tree as the base, then replaces pkg/ with the
// new tree (the PR's working tree), returning the repo.
func prRepo(t *testing.T, fixture string) string {
	t.Helper()
	repo := t.TempDir()
	copyFixtureTree(t, repo, filepath.Join("testdata", fixture, "old"))
	git(t, repo, "init", "-q")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "base")
	if err := os.RemoveAll(filepath.Join(repo, "pkg")); err != nil {
		t.Fatal(err)
	}
	copyFixtureTree(t, repo, filepath.Join("testdata", fixture, "new"))
	return repo
}

func gate(t *testing.T, repo string, args ...string) (string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	err := run(append([]string{"-dir", repo, "-base", "HEAD"}, args...), &out, &errb)
	return out.String(), errb.String(), err
}

const fixtureCodemod = "package codemod\n\nfunc init() {\n\tregister(Codemod{\n\t\tName: \"widget-close\",\n\t})\n}\n"

func slow(t *testing.T) {
	t.Helper()
	testkit.Slow(t, "runs go list and git against a scratch module",
		testkit.Inputs("internal/apigate/**"))
}

func TestDiffAPIReportsRemoval(t *testing.T) {
	slow(t)
	repo := prRepo(t, "removal")
	oldTree := t.TempDir()
	copyFixtureTree(t, oldTree, filepath.Join("testdata", "removal", "old"))
	breaks, _, err := diffAPI(oldTree, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(breaks) != 1 || breaks[0].Pkg != "pkg/widget" || !strings.Contains(breaks[0].Message, "Close") {
		t.Fatalf("breaks = %v, want exactly pkg/widget Close removed", breaks)
	}
}

func TestRemovalWithoutDeclarationFails(t *testing.T) {
	slow(t)
	repo := prRepo(t, "removal")
	_, stderr, err := gate(t, repo)
	if err == nil {
		t.Fatal("removing an exported function with no declaration must fail")
	}
	for _, want := range []string{"pkg/widget: Close", "API-BREAK:", "api-break"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not mention %q:\n%s", want, stderr)
		}
	}
}

func TestRemovalDeclaredButNoCodemodFails(t *testing.T) {
	slow(t)
	repo := prRepo(t, "removal")
	_, stderr, err := gate(t, repo, "-label", "api-break")
	if err == nil {
		t.Fatal("a label alone, with no codemod or playbook in the diff, must fail")
	}
	if !strings.Contains(stderr, "ships no codemod") {
		t.Errorf("stderr: %s", stderr)
	}
}

func TestRemovalDeclaredWithCodemodPasses(t *testing.T) {
	slow(t)
	repo := prRepo(t, "removal")
	if err := os.MkdirAll(filepath.Join(repo, "internal", "codemod"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "internal", "codemod", "widgetclose.go"), []byte(fixtureCodemod), 0o644); err != nil {
		t.Fatal(err)
	}
	body := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(body, []byte("Drop widget.Close.\n\nAPI-BREAK: codemod=widget-close\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := gate(t, repo, "-body-file", body)
	if err != nil {
		t.Fatalf("declared + codemod must pass: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "declared and shipped") || !strings.Contains(stdout, "Close") {
		t.Errorf("stdout should name the declared break: %s", stdout)
	}
}

func TestAdditiveChangePasses(t *testing.T) {
	slow(t)
	repo := prRepo(t, "additive")
	stdout, stderr, err := gate(t, repo)
	if err != nil {
		t.Fatalf("an additive change needs no declaration: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "no incompatible change") {
		t.Errorf("stdout: %s", stdout)
	}
}

func TestDocsOnlyDiffSkipsTheTypeCheck(t *testing.T) {
	slow(t)
	repo := prRepo(t, "additive")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "head")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := gate(t, repo)
	if err != nil || !strings.Contains(stdout, "touches nothing") {
		t.Fatalf("err=%v stdout=%s", err, stdout)
	}
}
