package forgereach

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// These tests drive real git and go in throwaway repositories, so they are
// skipped under -short (child processes read files Go's test cache cannot see).
func needsToolchain(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("runs git and go in a scratch repository; runs in task test")
	}
	for _, tool := range []string{"git", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
}

func put(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@example.test", "-c", "commit.gpgsign=false", "-c", "core.autocrlf=false"}, args...)
	out, err := Git(dir, full...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// newRepo makes a git repository holding files and returns its root and the
// commit that holds them.
func newRepo(t *testing.T, files map[string]string) (root, head string) {
	t.Helper()
	root = t.TempDir()
	git(t, root, "init", "-q")
	for rel, content := range files {
		put(t, root, rel, content)
	}
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "base")
	return root, git(t, root, "rev-parse", "HEAD")
}

func TestGitReportsStdoutAndFailures(t *testing.T) {
	needsToolchain(t)
	root, head := newRepo(t, map[string]string{"a.txt": "a"})
	out, err := Git(root, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(out)) != head {
		t.Fatalf("Git rev-parse = %q, %v; want %s", out, err, head)
	}
	_, err = Git(root, "rev-parse", "--verify", "no-such-rev")
	if err == nil || !strings.Contains(err.Error(), "git rev-parse --verify no-such-rev") {
		t.Errorf("err = %v, want the failing command named", err)
	}
	if _, err := Git(t.TempDir(), "rev-parse", "HEAD"); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("outside a repository: err = %v, want git's own message", err)
	}
}

func TestChangedFilesListsTrackedAndUntrackedSorted(t *testing.T) {
	needsToolchain(t)
	root, head := newRepo(t, map[string]string{"keep.txt": "k", "edit.txt": "1", "gone.txt": "g", "dir/in.txt": "x"})
	put(t, root, "edit.txt", "2")
	put(t, root, "z-new.txt", "n")
	put(t, root, "a-new/deep.txt", "n")
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	// A checkout nested beside the sources is not a file of this module.
	nested := filepath.Join(root, "consumer")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, nested, "init", "-q")
	put(t, nested, "f.txt", "x")
	git(t, nested, "add", "-A")
	git(t, nested, "commit", "-q", "-m", "n")

	got, err := ChangedFiles(root, head)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a-new/deep.txt", "edit.txt", "gone.txt", "z-new.txt"}
	if !slices.Equal(got, want) {
		t.Errorf("ChangedFiles = %v, want %v", got, want)
	}

	clean, _ := newRepo(t, map[string]string{"f": "x"})
	if got, err := ChangedFiles(clean, "HEAD"); err != nil || len(got) != 0 {
		t.Errorf("clean checkout: %v, %v; want none", got, err)
	}
}

func TestChangedFilesReportsAnUnknownBase(t *testing.T) {
	needsToolchain(t)
	root, _ := newRepo(t, map[string]string{"f": "x"})
	if _, err := ChangedFiles(root, "origin/not-a-branch"); err == nil || !strings.Contains(err.Error(), "origin/not-a-branch") {
		t.Errorf("err = %v, want the unknown base named", err)
	}
	if _, err := ChangedFiles(t.TempDir(), "HEAD"); err == nil {
		t.Error("expected an error outside a repository")
	}
}

func TestExportRevWritesTheTreeWithoutRepositoryState(t *testing.T) {
	needsToolchain(t)
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not on PATH")
	}
	root, head := newRepo(t, map[string]string{"a.txt": "one", "sub/b.txt": "two"})
	put(t, root, "a.txt", "changed after the commit")
	dst := t.TempDir()
	if err := ExportRev(root, head, dst); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{"a.txt": "one", "sub/b.txt": "two"} {
		b, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(rel)))
		if err != nil || string(b) != want {
			t.Errorf("%s = %q, %v; want the committed %q", rel, b, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); err == nil {
		t.Error("the export carries repository state")
	}

	err := ExportRev(root, "no-such-rev", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "git archive no-such-rev") {
		t.Errorf("unknown rev: err = %v", err)
	}
	err = ExportRev(root, head, filepath.Join(t.TempDir(), "missing", "dst"))
	if err == nil || !strings.Contains(err.Error(), "extract") && !strings.Contains(err.Error(), "tar") {
		t.Errorf("unwritable destination: err = %v, want an extraction error", err)
	}
}

func TestGoListRunsInTheDirectoryAndReportsFailures(t *testing.T) {
	needsToolchain(t)
	root, _ := newRepo(t, map[string]string{"go.mod": "module example.com/m\n\ngo 1.21\n", "m.go": "package m\n"})
	out, err := GoList(root, "-m")
	if err != nil || strings.TrimSpace(string(out)) != "example.com/m" {
		t.Fatalf("GoList -m = %q, %v", out, err)
	}
	_, err = GoList(root, "./nope/...")
	if err == nil || !strings.Contains(err.Error(), "go list ./nope/...") || !strings.Contains(err.Error(), root) {
		t.Errorf("err = %v, want the command and directory named", err)
	}
}

const modPrefix = "module example.com/m\n\ngo 1.21\n"

func TestDepsChanged(t *testing.T) {
	needsToolchain(t)
	base := modPrefix + "\nrequire example.com/a v1.0.0\n"
	cases := []struct {
		name    string
		changed []string
		newMod  string // go.mod on disk after the change ("" = unchanged)
		want    bool
	}{
		{"neither file changed", []string{"main.go"}, "", false},
		{"go.sum alone", []string{"go.sum"}, "", true},
		{"go.mod comment only", []string{"go.mod"}, base + "// a note\n", false},
		{"go.mod indirect marker only", []string{"go.mod"}, modPrefix + "\nrequire example.com/a v1.0.0 // indirect\n", false},
		{"go.mod version bump", []string{"go.mod"}, modPrefix + "\nrequire example.com/a v1.1.0\n", true},
		{"go.mod added requirement", []string{"go.mod"}, base + "require example.com/b v1.0.0\n", true},
		{"go.mod removed requirement", []string{"go.mod"}, modPrefix, true},
		{"go.mod new replace", []string{"go.mod"}, base + "replace example.com/a => ../a\n", true},
		{"go.mod go version", []string{"go.mod"}, strings.Replace(base, "go 1.21", "go 1.22", 1), true},
		{"go.mod toolchain", []string{"go.mod"}, base + "toolchain go1.22.1\n", true},
		{"go.mod unparseable now", []string{"go.mod"}, "not a go.mod {{{", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, head := newRepo(t, map[string]string{"go.mod": base, "main.go": "package m\n"})
			if tc.newMod != "" {
				put(t, root, "go.mod", tc.newMod)
			}
			if got := depsChanged(root, head, tc.changed); got != tc.want {
				t.Errorf("depsChanged = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("go.mod missing at the base counts as changed", func(t *testing.T) {
		root, head := newRepo(t, map[string]string{"main.go": "package m\n"})
		put(t, root, "go.mod", base)
		if !depsChanged(root, head, []string{"go.mod"}) {
			t.Error("a go.mod that did not exist at base must count as a dependency change")
		}
	})
	t.Run("go.mod deleted from disk counts as changed", func(t *testing.T) {
		root, head := newRepo(t, map[string]string{"go.mod": base})
		if err := os.Remove(filepath.Join(root, "go.mod")); err != nil {
			t.Fatal(err)
		}
		if !depsChanged(root, head, []string{"go.mod"}) {
			t.Error("a deleted go.mod must count as a dependency change")
		}
	})
}

// fixtureModule is a tiny forge-shaped module: pkg/a is what a consumer
// imports and it imports internal/b; internal/c and cmd/tool are not reachable
// from pkg/.
var fixtureModule = map[string]string{
	"go.mod":               modPrefix,
	"pkg/a/a.go":           "package a\n\nimport _ \"example.com/m/internal/b\"\n",
	"pkg/a/a.proto":        "syntax = \"proto3\";\n",
	"pkg/gone/g.go":        "package gone\n",
	"internal/b/b.go":      "package b\n",
	"internal/c/c.go":      "package c\n",
	"internal/c/c_test.go": "package c\n",
	"cmd/tool/main.go":     "package main\n\nfunc main() {}\n",
	"root.go":              "package m\n",
}

func TestComputeMapsChangedFilesOntoWhatAConsumerSees(t *testing.T) {
	needsToolchain(t)
	root, head := newRepo(t, fixtureModule)
	if err := os.RemoveAll(filepath.Join(root, "pkg", "gone")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		changed    []string
		packages   []string
		generated  []string
		all        bool
		wantReach  bool
		wantAPIHit bool
	}{
		{name: "docs and tests reach nothing", changed: []string{"README.md", "internal/c/c_test.go", "pkg/a/testdata/x.go"}},
		{name: "a package consumers import", changed: []string{"pkg/a/a.go"}, packages: []string{"example.com/m/pkg/a"}, wantReach: true, wantAPIHit: true},
		{name: "a proto in that package", changed: []string{"pkg/a/a.proto"}, packages: []string{"example.com/m/pkg/a"}, wantReach: true, wantAPIHit: true},
		{name: "an internal package pkg/ imports", changed: []string{"internal/b/b.go"}, packages: []string{"example.com/m/internal/b"}, wantReach: true, wantAPIHit: true},
		{name: "an internal package nothing in pkg/ imports", changed: []string{"internal/c/c.go", "cmd/tool/main.go", "root.go"}},
		{name: "a deleted package under pkg/", changed: []string{"pkg/gone/g.go"}, packages: []string{"example.com/m/pkg/gone"}, wantReach: true, wantAPIHit: true},
		{name: "two files of one package are listed once", changed: []string{"pkg/a/a.go", "pkg/a/a.proto"}, packages: []string{"example.com/m/pkg/a"}, wantReach: true, wantAPIHit: true},
		{name: "packages come out sorted", changed: []string{"pkg/gone/g.go", "internal/b/b.go", "pkg/a/a.go"}, packages: []string{"example.com/m/internal/b", "example.com/m/pkg/a", "example.com/m/pkg/gone"}, wantReach: true, wantAPIHit: true},
		{name: "a template changes generated code only", changed: []string{"internal/templates/project/x.tmpl"}, generated: []string{"internal/templates/project/x.tmpl"}, wantReach: true},
		{name: "a skill document does not", changed: []string{"internal/templates/project/skills/forge/SKILL.md"}},
		{name: "a generator go file is both generated and (if imported) API", changed: []string{"internal/generator/gen.go"}, generated: []string{"internal/generator/gen.go"}, wantReach: true},
		{name: "go.sum alone changes everything", changed: []string{"go.sum"}, all: true, wantReach: true, wantAPIHit: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Compute(root, head, tc.changed)
			if err != nil {
				t.Fatal(err)
			}
			if r.Module != "example.com/m" {
				t.Errorf("Module = %q", r.Module)
			}
			if !slices.Equal(r.Packages, tc.packages) {
				t.Errorf("Packages = %v, want %v", r.Packages, tc.packages)
			}
			if !slices.Equal(r.Generated, tc.generated) {
				t.Errorf("Generated = %v, want %v", r.Generated, tc.generated)
			}
			if r.All != tc.all || r.Any() != tc.wantReach || r.API() != tc.wantAPIHit {
				t.Errorf("All=%v Any=%v API=%v, want %v %v %v", r.All, r.Any(), r.API(), tc.all, tc.wantReach, tc.wantAPIHit)
			}
		})
	}
}

func TestComputeReportsToolFailures(t *testing.T) {
	needsToolchain(t)
	// Not a module at all: go list -m fails.
	if _, err := Compute(t.TempDir(), "HEAD", []string{"pkg/a/a.go"}); err == nil || !strings.Contains(err.Error(), "go list") {
		t.Errorf("no module: err = %v, want a go list failure", err)
	}
	// A module whose package graph does not load: a changed Go file makes
	// Compute load it and surface the error.
	root, head := newRepo(t, map[string]string{
		"go.mod":     modPrefix,
		"pkg/a/a.go": "package a\n\nimport _ \"example.com/m/does/not/exist\"\n",
	})
	if _, err := Compute(root, head, []string{"pkg/a/a.go"}); err == nil {
		t.Error("expected the unloadable package graph to be reported")
	}
}
