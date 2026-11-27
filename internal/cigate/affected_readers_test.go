package cigate

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// readerGit is fakeGit plus `git ls-files --cached`: the tracked files.
type readerGit struct {
	fakeGit
	tracked []string
}

func (r readerGit) run(ctx context.Context, dir string, args ...string) (string, error) {
	if len(args) >= 2 && args[0] == "ls-files" && args[1] == "--cached" {
		return strings.Join(r.tracked, "\n"), nil
	}
	return r.fakeGit.run(ctx, dir, args...)
}

// readerSrc is package c's test: it locates the repo root and reads
// deploy/kcl/ under it, and builds a fixture path under t.TempDir().
const readerSrc = `package c

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func repoRootDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..")
}

func TestReads(t *testing.T) {
	_, _ = os.ReadFile(filepath.Join(repoRootDir(), "deploy", "kcl"))
	_, _ = os.ReadFile(filepath.Join(t.TempDir(), "docs", "guide"))
	_ = "config/settings.yaml"
}
`

func readerRoot(t *testing.T) (string, []string) {
	t.Helper()
	root := t.TempDir()
	write(t, root, "c/c_test.go", readerSrc)
	tracked := []string{"c/c_test.go", "deploy/kcl/main.k", "deploy/kcl/lib/x.k", "docs/guide/index.md", "config/settings.yaml", "scripts/run.sh"}
	for _, f := range tracked[1:] {
		write(t, root, f, "x")
	}
	return root, tracked
}

func TestAffectedReaders(t *testing.T) {
	cases := []struct {
		name     string
		readers  bool
		changed  string
		wantFull bool
		wantPkgs string
		wantSel  string
	}{
		{name: "file under a joined root dir", readers: true, changed: "deploy/kcl/lib/x.k", wantPkgs: "ex.com/m/c", wantSel: "ex.com/m/c (names deploy/kcl)"},
		{name: "literal naming the file", readers: true, changed: "config/settings.yaml", wantPkgs: "ex.com/m/c", wantSel: "config/settings.yaml changed → ex.com/m/c (its"},
		{name: "join onto TempDir names nothing", readers: true, changed: "docs/guide/index.md", wantFull: true},
		{name: "no reader still goes full", readers: true, changed: "scripts/run.sh", wantFull: true},
		{name: "off by default", readers: false, changed: "deploy/kcl/main.k", wantFull: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, tracked := readerRoot(t)
			g := readerGit{fakeGit: fakeGit{branch: "f", changed: []string{tc.changed}}, tracked: tracked}
			a := ComputeAffected(context.Background(), AffectedInput{Root: root, Git: g.run, GoList: listed(root), Readers: tc.readers})
			if a.Full != tc.wantFull {
				t.Fatalf("full = %v (%s), want %v", a.Full, a.Reason, tc.wantFull)
			}
			if tc.wantFull {
				if !strings.Contains(a.Reason, tc.changed) {
					t.Errorf("reason %q does not name %s", a.Reason, tc.changed)
				}
				return
			}
			if got := strings.Join(a.Packages, ","); got != tc.wantPkgs {
				t.Errorf("packages = %q, want %q", got, tc.wantPkgs)
			}
			if len(a.Selections) != 1 || !strings.Contains(a.Selections[0], tc.wantSel) {
				t.Errorf("selections = %q, want one containing %q", a.Selections, tc.wantSel)
			}
		})
	}
}

// A package can read Go source without importing its owner. Keep that
// reader even past a zero transitive cap, without changing ignore or fallback.
func TestAffectedReaders_GoSourceWithoutImport(t *testing.T) {
	for _, tc := range []struct {
		name, changed string
		readers       bool
		ignore        []string
		want          []string
		deferred      []string
		full          bool
	}{
		{name: "reader is a direct root", changed: "a/a.go", readers: true,
			want: []string{"ex.com/m/a", "ex.com/m/c"}, deferred: []string{"ex.com/m/b"}},
		{name: "opt in still required", changed: "a/a.go",
			want: []string{"ex.com/m/a"}, deferred: []string{"ex.com/m/b"}},
		{name: "ignore still wins", changed: "a/a.go", readers: true, ignore: []string{"a/a.go"}},
		{name: "unplaced Go still falls back", changed: "outside/source.go", readers: true, full: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "c/c_test.go", `package c

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSource(t *testing.T) {
	_, _ = os.ReadFile(filepath.Join("..", "a", "a.go"))
	_, _ = os.ReadFile(filepath.Join("..", "outside", "source.go"))
}
`)
			write(t, root, "a/a.go", "package a\n")
			write(t, root, "outside/source.go", "package outside\n")
			git := readerGit{fakeGit: fakeGit{branch: "f", changed: []string{tc.changed}}, tracked: []string{"a/a.go", "c/c_test.go", "outside/source.go"}}
			a := ComputeAffected(context.Background(), AffectedInput{Root: root, Git: git.run, GoList: listed(root),
				Readers: tc.readers, Ignore: tc.ignore, Reach: &config.CIGateAffectedReach{Mode: ReachDirect}})
			if a.Full != tc.full || !slices.Equal(a.Packages, tc.want) || !slices.Equal(a.Deferred, tc.deferred) {
				t.Fatalf("full=%v (%s), packages=%v, deferred=%v; want full=%v, packages=%v, deferred=%v",
					a.Full, a.Reason, a.Packages, a.Deferred, tc.full, tc.want, tc.deferred)
			}
		})
	}
}

// Both packages must be selected: finding one reader must not narrow away
// another whose literal Join is used after changing to the repo root.
func TestAffectedReaders_LiteralJoinWorkingDirectory(t *testing.T) {
	for _, expr := range []string{
		`filepath.Join("deploy", "kcl")`,
		`path.Join("deploy", "kcl")`,
		`filepath.Join("deploy")`,
		`filepath.Join(deployDir)`,
	} {
		t.Run(expr, func(t *testing.T) {
			root, tracked := readerRoot(t)
			write(t, root, "b/b_test.go", `package b
const deployDir = "deploy"
func TestReads(t *testing.T) {
    os.Chdir(repoRoot(t))
    os.ReadDir(`+expr+`)
}
`)
			tracked = append(tracked, "b/b_test.go", "b/deploy/kcl/local.k")
			write(t, root, "b/deploy/kcl/local.k", "x")
			for _, tc := range []struct {
				changed, want string
			}{
				{"deploy/kcl/main.k", "ex.com/m/b,ex.com/m/c"},
				{"b/deploy/kcl/local.k", "ex.com/m/b"},
			} {
				g := readerGit{fakeGit: fakeGit{branch: "f", changed: []string{tc.changed}}, tracked: tracked}
				a := ComputeAffected(context.Background(), AffectedInput{Root: root, Git: g.run, GoList: listed(root), Readers: true})
				if got := strings.Join(a.Packages, ","); a.Full || got != tc.want {
					t.Errorf("%s: full=%v packages=%q, want %q (%s)", tc.changed, a.Full, got, tc.want, a.Reason)
				}
			}
		})
	}
}

// A familiar helper name is not proof of a repository read. Without a
// proven reader the planner must retain its full-suite fallback.
func TestAffectedReaders_UnprovenRootHelpers(t *testing.T) {
	for _, helper := range []struct{ declaration, call string }{
		{`func repoRoot() string { return t.TempDir() }`, `repoRoot()`},
		{`func RepoRoot() string { return "fixtures" }`, `RepoRoot()`},
		{`func findRoot() string { return "fixtures" }`, `findRoot()`},
		{`func repoRootFromTest() string { return t.TempDir() }`, `repoRootFromTest()`},
		{`func repoRoot() string { return ".." }`, `fixture.repoRoot()`},
		{`func (fixture) repoRoot() string { return ".." }; func repoRoot() string { return t.TempDir() }`, `repoRoot()`},
	} {
		t.Run(helper.call+helper.declaration, func(t *testing.T) {
			root, tracked := readerRoot(t)
			write(t, root, "c/c_test.go", `package c
`+helper.declaration+`
func TestReads(t *testing.T) { os.ReadDir(filepath.Join(`+helper.call+`, "deploy", "kcl")) }
`)
			g := readerGit{fakeGit: fakeGit{branch: "f", changed: []string{"deploy/kcl/main.k"}}, tracked: tracked}
			a := ComputeAffected(context.Background(), AffectedInput{Root: root, Git: g.run, GoList: listed(root), Readers: true})
			if !a.Full {
				t.Fatalf("unproven helper narrowed to %v", a.Packages)
			}
		})
	}
}

func TestReaderScan_PackageNotLocatingRootIsNotScanned(t *testing.T) {
	root := t.TempDir()
	write(t, root, "c/c_test.go", "package c\n\nvar _ = \"config/settings.yaml\"\n")
	write(t, root, "config/settings.yaml", "x")
	g := readerGit{fakeGit: fakeGit{branch: "f", changed: []string{"config/settings.yaml"}}, tracked: []string{"c/c_test.go", "config/settings.yaml"}}
	a := ComputeAffected(context.Background(), AffectedInput{Root: root, Git: g.run, GoList: listed(root), Readers: true})
	if !a.Full {
		t.Fatalf("a package that never locates the root reads only its own dir; got packages %v", a.Packages)
	}
}
