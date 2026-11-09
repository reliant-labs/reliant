package cigate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

// fakeGit answers the git calls ComputeAffected makes.
type fakeGit struct {
	branch    string
	noOrigin  bool
	changed   []string
	untracked []string
}

func (f fakeGit) run(_ context.Context, _ string, args ...string) (string, error) {
	switch strings.Join(args[:2], " ") {
	case "symbolic-ref --quiet":
		if len(args) == 3 && args[2] == "refs/remotes/origin/HEAD" {
			if f.noOrigin {
				return "", errors.New("no origin/HEAD")
			}
			return "refs/remotes/origin/main", nil
		}
		return f.branch, nil
	case "rev-parse --verify":
		return "", errors.New("absent")
	case "merge-base HEAD":
		return "0123456789abcdef0123", nil
	case "diff --name-only":
		return strings.Join(f.changed, "\n"), nil
	case "ls-files --others":
		return strings.Join(f.untracked, "\n"), nil
	}
	return "", errors.New("unexpected git " + strings.Join(args, " "))
}

// listed renders a `go list -deps -test -json` stream for a module at root:
// packages a (embeds tmpl/x.tmpl), b imports a, c alone, plus their test
// mains.
func listed(root string) func(context.Context, string, []string) (io.Reader, error) {
	pkgs := []listedPackage{
		{ImportPath: "fmt", Standard: true, Dir: "/goroot/src/fmt"},
		{ImportPath: "ex.com/m/a", Name: "a", Dir: filepath.Join(root, "a"), EmbedFiles: []string{"tmpl/x.tmpl"}},
		{ImportPath: "ex.com/m/b", Name: "b", Dir: filepath.Join(root, "b"), Deps: []string{"ex.com/m/a"}},
		{ImportPath: "ex.com/m/c", Name: "c", Dir: filepath.Join(root, "c")},
		{ImportPath: "ex.com/m/nested/d", Name: "d", Dir: filepath.Join(root, "nested", "d")},
		{ImportPath: "ex.com/m/a [ex.com/m/a.test]", Name: "a", Dir: filepath.Join(root, "a"), ForTest: "ex.com/m/a", TestEmbedFiles: []string{"golden.txt"}},
		{ImportPath: "ex.com/m/a.test", Name: "main", Dir: filepath.Join(root, "a"), Deps: []string{"ex.com/m/a [ex.com/m/a.test]", "fmt"}},
		{ImportPath: "ex.com/m/b.test", Name: "main", Dir: filepath.Join(root, "b"), Deps: []string{"ex.com/m/a", "ex.com/m/b [ex.com/m/b.test]"}},
		{ImportPath: "ex.com/m/c.test", Name: "main", Dir: filepath.Join(root, "c"), Deps: []string{"ex.com/m/c [ex.com/m/c.test]"}},
		// A dependency outside the module (module cache) is never a candidate.
		{ImportPath: "other.com/x", Name: "x", Dir: "/gomodcache/other.com/x"},
		{ImportPath: "other.com/x.test", Name: "main", Dir: "/gomodcache/other.com/x", Deps: []string{"ex.com/m/a"}},
	}
	return func(context.Context, string, []string) (io.Reader, error) {
		pr, pw := io.Pipe()
		go func() {
			enc := json.NewEncoder(pw)
			for _, p := range pkgs {
				_ = enc.Encode(p)
			}
			_ = pw.Close()
		}()
		return pr, nil
	}
}

func affected(t *testing.T, g fakeGit, ignore ...string) Affected {
	t.Helper()
	root := t.TempDir()
	return ComputeAffected(context.Background(), AffectedInput{Root: root, Git: g.run, GoList: listed(root), Ignore: ignore})
}

func TestComputeAffected_SelectsWhatTheChangeReaches(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changed []string
		want    string
	}{
		{"source of a dependency reaches its importers' tests", []string{"a/a.go"}, "ex.com/m/a ex.com/m/b"},
		{"a leaf package is tested alone", []string{"b/b.go"}, "ex.com/m/b"},
		{"an embedded file is its package's source", []string{"a/tmpl/x.tmpl"}, "ex.com/m/a ex.com/m/b"},
		{"a test-only embed counts too", []string{"a/golden.txt"}, "ex.com/m/a ex.com/m/b"},
		{"a package's own testdata", []string{"c/testdata/in/case.json"}, "ex.com/m/c"},
		{"a package with no tests reaches no test binary", []string{"nested/d/d.go"}, ""},
		{"a deleted file in a live package", []string{"c/old.go"}, "ex.com/m/c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := affected(t, fakeGit{branch: "feature", changed: tc.changed})
			if a.Full {
				t.Fatalf("fell back to the full suite: %s", a.Reason)
			}
			if got := strings.Join(a.Packages, " "); got != tc.want {
				t.Fatalf("packages = %q, want %q", got, tc.want)
			}
			if a.Total != 3 {
				t.Errorf("Total = %d, want the module's 3 test packages", a.Total)
			}
		})
	}
}

func TestComputeAffected_FallsBackToTheFullSuite(t *testing.T) {
	for _, tc := range []struct {
		name string
		g    fakeGit
		why  string
	}{
		{"go.mod", fakeGit{branch: "f", changed: []string{"a/a.go", "go.mod"}}, "go.mod changed"},
		{"go.sum", fakeGit{branch: "f", changed: []string{"go.sum"}}, "go.sum changed"},
		{"testdata outside a package", fakeGit{branch: "f", changed: []string{"testdata/fixture.json"}}, "testdata/fixture.json changed"},
		{"KCL", fakeGit{branch: "f", changed: []string{"deploy/kcl/dev/main.k"}}, "main.k changed"},
		{"proto", fakeGit{branch: "f", changed: []string{"proto/services/x/v1/x.proto"}}, "x.proto changed"},
		{"migration", fakeGit{branch: "f", changed: []string{"db/migrations/001_a.up.sql"}}, "001_a.up.sql changed"},
		{"Taskfile", fakeGit{branch: "f", changed: []string{"Taskfile.yml"}}, "Taskfile.yml changed"},
		{"script", fakeGit{branch: "f", changed: []string{"scripts/x.sh"}}, "scripts/x.sh changed"},
		{"a non-embedded file in a package dir", fakeGit{branch: "f", changed: []string{"a/README.md"}}, "a/README.md changed"},
		{"Go source in no package (a deleted package)", fakeGit{branch: "f", changed: []string{"gone/gone.go"}}, "gone/gone.go changed"},
		{"an untracked non-Go file", fakeGit{branch: "f", untracked: []string{"notes.txt"}}, "notes.txt changed"},
		{"the default branch", fakeGit{branch: "main", changed: []string{"a/a.go"}}, "on the default branch"},
		{"nothing changed", fakeGit{branch: "f"}, "nothing changed since origin/main"},
		{"no origin", fakeGit{branch: "f", noOrigin: true}, "no origin/<default branch>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := affected(t, tc.g)
			if !a.Full || !strings.Contains(a.Reason, tc.why) {
				t.Fatalf("Full=%v Reason=%q, want the full suite because %q", a.Full, a.Reason, tc.why)
			}
			if !strings.HasPrefix(a.Line(), "unit tests: full suite — ") {
				t.Errorf("Line = %q", a.Line())
			}
		})
	}
	root := t.TempDir()
	forced := ComputeAffected(context.Background(), AffectedInput{Root: root, Force: "CI_FULL=1", Git: fakeGit{branch: "f", changed: []string{"a/a.go"}}.run, GoList: listed(root)})
	if !forced.Full || forced.Reason != "CI_FULL=1" {
		t.Errorf("forced: %+v", forced)
	}
}

// affected_ignore names files no Go test reads; a change to one keeps the
// branch in affected mode.
func TestComputeAffected_IgnoreGlobs(t *testing.T) {
	a := affected(t, fakeGit{branch: "f", changed: []string{"CHANGELOG.md", "docs/guide/x.png", "a/README.md", "b/b.go"}}, "*.md", "docs/**")
	if a.Full || strings.Join(a.Packages, " ") != "ex.com/m/b" {
		t.Fatalf("Full=%v (%s) packages=%v", a.Full, a.Reason, a.Packages)
	}
	if !ignoredByGlobs("deploy/kcl/dev/main.k", []string{"deploy/kcl/*/main.k"}) || ignoredByGlobs("deploy/kcl/main.k", []string{"deploy/kcl/*/main.k"}) {
		t.Error("a glob with a slash matches the whole path")
	}
}

func TestApplyAffected(t *testing.T) {
	unit := Check{Name: UnitTests, Argv: []string{"task", "test"}}
	other := check("lint", "lint")

	got := ApplyAffected([]Check{other, unit}, Affected{Packages: []string{"ex.com/m/a", "ex.com/m/b"}, Total: 9, BaseRef: "origin/main"})
	if argv := strings.Join(got[1].Argv, " "); argv != "task test -- ex.com/m/a ex.com/m/b" {
		t.Errorf("argv = %q", argv)
	}
	if !strings.Contains(got[1].Note, "2 of 9 packages") || got[0].Note != "" {
		t.Errorf("notes: %q / %q", got[1].Note, got[0].Note)
	}
	if strings.Join(unit.Argv, " ") != "task test" {
		t.Error("ApplyAffected modified its input")
	}

	none := ApplyAffected([]Check{unit}, Affected{BaseRef: "origin/main", Changed: []string{"nested/d/d.go"}})
	if none[0].NotApplicable == "" {
		t.Error("no package reached: the row is N/A, with the reason")
	}
	full := ApplyAffected([]Check{unit}, Affected{Full: true, Reason: "go.mod changed"})
	if strings.Join(full[0].Argv, " ") != "task test" || full[0].Note != "full suite: go.mod changed" {
		t.Errorf("full: %+v", full[0])
	}
}

// The real thing, once: git and `go list` over a small module, on a branch.
func TestComputeAffected_RealModule(t *testing.T) {
	if testing.Short() {
		t.Skip("runs git and `go list -deps -test` subprocesses; runs in task test")
	}
	root := gitRepo(t, map[string]string{
		"go.mod":      "module ex.com/m\n\ngo 1.22\n",
		"a/a.go":      "package a\n\nfunc A() int { return 1 }\n",
		"a/a_test.go": "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { _ = A() }\n",
		"b/b.go":      "package b\n\nimport \"ex.com/m/a\"\n\nfunc B() int { return a.A() }\n",
		"b/b_test.go": "package b\n\nimport \"testing\"\n\nfunc TestB(t *testing.T) { _ = B() }\n",
		"c/c.go":      "package c\n",
		"c/c_test.go": "package c\n\nimport \"testing\"\n\nfunc TestC(t *testing.T) {}\n",
	})
	git(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	git(t, root, "checkout", "-q", "-b", "feature")
	write(t, root, "a/a.go", "package a\n\nfunc A() int { return 2 }\n")
	env := goexec.WithTrimpath(append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod"))
	a := ComputeAffected(context.Background(), AffectedInput{Root: root, Env: env})
	if a.Full {
		t.Fatalf("full suite: %s", a.Reason)
	}
	if got := strings.Join(a.Packages, " "); got != "ex.com/m/a ex.com/m/b" || a.Total != 3 {
		t.Fatalf("packages %q of %d, want a and b of 3", got, a.Total)
	}
	if !strings.Contains(a.Line(), "2 of 3 test packages") {
		t.Errorf("Line = %q", a.Line())
	}
}

// A project reached through a symlink (macOS's /var → /private/var under
// t.TempDir, or a symlinked checkout) gets its package dirs back from go list
// symlink-resolved. They are still the module's own packages: a changed .go
// file must map to its package, not force the full suite.
func TestReadPackageGraph_SymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	if err := os.MkdirAll(filepath.Join(real, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	realDir, err := filepath.EvalSymlinks(filepath.Join(real, "a"))
	if err != nil {
		t.Fatal(err)
	}
	pkgs, err := json.Marshal(listedPackage{ImportPath: "ex.com/m/a", Name: "a", Dir: realDir})
	if err != nil {
		t.Fatal(err)
	}
	g, err := readPackageGraph(strings.NewReader(string(pkgs)), link)
	if err != nil {
		t.Fatal(err)
	}
	if pkg, ok := g.owner("a/a.go"); !ok || pkg != "ex.com/m/a" {
		t.Fatalf("owner(a/a.go) = %q, %v; want ex.com/m/a under a symlinked root", pkg, ok)
	}
}
