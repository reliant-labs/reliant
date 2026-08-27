// Copyright (c) 2025 Reliant Labs

package filetree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}

func find(nodes []*Node, name string) *Node {
	for _, n := range nodes {
		if n.Name == name {
			return n
		}
	}
	return nil
}

// names flattens a level to its entry names, for terse assertions.
func names(nodes []*Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Name)
	}
	return out
}

// at resolves a slash-separated path through a walked tree.
func at(t *testing.T, nodes []*Node, path string) *Node {
	t.Helper()
	var cur *Node
	for _, seg := range splitSegments(path) {
		cur = find(nodes, seg)
		if cur == nil {
			return nil
		}
		nodes = cur.Children
	}
	return cur
}

// ---------------------------------------------------------------------------
// depth semantics
// ---------------------------------------------------------------------------

// The defect this package exists to fix was a default: saying nothing about
// depth meant "recurse without bound". These cases pin the replacement contract
// — 0 is the server default, N is N levels, -1 is budget-bounded — with the
// deep tree that would have exposed the old behavior.
func TestWalk_DepthSemantics(t *testing.T) {
	root := t.TempDir()
	// a/b/c/d/e.txt — five levels below the root.
	write(t, filepath.Join(root, "a", "b", "c", "d", "e.txt"), "leaf")
	write(t, filepath.Join(root, "top.txt"), "top")

	tests := []struct {
		name string
		// depth as the caller passes it.
		depth int
		// deepest existing path that must be present in the result.
		wantPresent string
		// shallowest path that must be ABSENT (the boundary).
		wantAbsent string
	}{
		{name: "1 = immediate children only", depth: 1, wantPresent: "a", wantAbsent: "a/b"},
		{name: "2 = children and grandchildren", depth: 2, wantPresent: "a/b", wantAbsent: "a/b/c"},
		{name: "3 = three levels", depth: 3, wantPresent: "a/b/c", wantAbsent: "a/b/c/d"},
		{
			name:        "0 = the default, NOT unlimited",
			depth:       0,
			wantPresent: "a/b",
			wantAbsent:  "a/b/c",
		},
		{
			name:        "-1 = as deep as the budget allows",
			depth:       MaxDepth,
			wantPresent: "a/b/c/d/e.txt",
		},
		{
			name:        "any negative value normalizes to MaxDepth",
			depth:       -42,
			wantPresent: "a/b/c/d/e.txt",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Walk(Options{Root: root, Depth: tc.depth})
			if err != nil {
				t.Fatalf("Walk: %v", err)
			}
			if tc.wantPresent != "" && at(t, res.Nodes, tc.wantPresent) == nil {
				t.Errorf("depth %d: %q missing; got top level %v", tc.depth, tc.wantPresent, names(res.Nodes))
			}
			if tc.wantAbsent != "" && at(t, res.Nodes, tc.wantAbsent) != nil {
				t.Errorf("depth %d: %q leaked past the boundary", tc.depth, tc.wantAbsent)
			}
		})
	}

	// The default is genuinely DefaultTreeDepth, not merely "some bound".
	zero, err := Walk(Options{Root: root, Depth: 0})
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := Walk(Options{Root: root, Depth: DefaultTreeDepth})
	if err != nil {
		t.Fatal(err)
	}
	if zero.NodeCount != explicit.NodeCount {
		t.Errorf("depth 0 produced %d nodes, depth %d produced %d — 0 must mean the default",
			zero.NodeCount, DefaultTreeDepth, explicit.NodeCount)
	}
}

// ---------------------------------------------------------------------------
// node budget
// ---------------------------------------------------------------------------

// Depth alone does not save a caller from one directory holding two hundred
// thousand entries. The node budget is the backstop, and it has to fire on both
// shapes of excess: wide and deep.
func TestWalk_NodeBudget(t *testing.T) {
	t.Run("wide directory truncates at the budget", func(t *testing.T) {
		root := t.TempDir()
		for i := 0; i < 50; i++ {
			write(t, filepath.Join(root, fmt.Sprintf("f%02d.txt", i)), "x")
		}

		res, err := Walk(Options{Root: root, Depth: 1, MaxNodes: 10})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Truncated {
			t.Error("50 entries under a budget of 10 must report truncation")
		}
		if res.NodeCount != 10 {
			t.Errorf("NodeCount = %d, want exactly the budget (10)", res.NodeCount)
		}
		if len(res.Nodes) != 10 {
			t.Errorf("returned %d nodes, want 10", len(res.Nodes))
		}
	})

	t.Run("deep chain truncates at the budget", func(t *testing.T) {
		root := t.TempDir()
		deep := root
		for i := 0; i < 30; i++ {
			deep = filepath.Join(deep, fmt.Sprintf("d%02d", i))
		}
		mkdir(t, deep)

		res, err := Walk(Options{Root: root, Depth: MaxDepth, MaxNodes: 5})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Truncated {
			t.Error("a 30-deep chain under a budget of 5 must report truncation")
		}
		if res.NodeCount != 5 {
			t.Errorf("NodeCount = %d, want 5", res.NodeCount)
		}
	})

	t.Run("a tree within budget is not marked truncated", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "a.txt"), "a")
		write(t, filepath.Join(root, "sub", "b.txt"), "b")

		res, err := Walk(Options{Root: root, Depth: MaxDepth})
		if err != nil {
			t.Fatal(err)
		}
		if res.Truncated {
			t.Error("a three-node tree must not report truncation")
		}
		if res.NodeCount != 3 {
			t.Errorf("NodeCount = %d, want 3 (a.txt, sub, sub/b.txt)", res.NodeCount)
		}
	})

	t.Run("MaxDepth is still capped by the default budget", func(t *testing.T) {
		// Not a filesystem assertion — a statement about the contract: there is
		// no option combination that yields an unbounded walk.
		res, err := Walk(Options{Root: t.TempDir(), Depth: MaxDepth})
		if err != nil {
			t.Fatal(err)
		}
		if res.NodeCount > MaxTreeNodes {
			t.Errorf("NodeCount %d exceeded MaxTreeNodes %d", res.NodeCount, MaxTreeNodes)
		}
	})
}

// ---------------------------------------------------------------------------
// skip set
// ---------------------------------------------------------------------------

func TestIsSkippedDir(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		// The union of the two lists this package replaced.
		{".git", true},
		{"node_modules", true},
		{"dist", true},
		{"build", true},
		{"__pycache__", true},
		{".reliant", true},
		{"vendor", true},
		{"bower_components", true},
		{"jspm_packages", true},
		{".next", true},
		{".nuxt", true},
		{"target", true},
		{"coverage", true},
		{"tmp", true},
		{"temp", true},
		// The families they missed.
		{"Library", true},
		{"Builds", true},
		{"Temp", true},
		{"Obj", true},
		{"Logs", true},
		{"Pods", true},
		{".venv", true},
		{"venv", true},
		{".tox", true},
		{".gradle", true},
		{".idea", true},
		{"DerivedData", true},
		{".terraform", true},
		{".cache", true},
		// Case-insensitivity in both directions: Unity ships capitalised
		// directory names and spells them `[Ll]ibrary/` in .gitignore.
		{"LIBRARY", true},
		{"NODE_MODULES", true},
		{"library", true},
		{"derived_data", false},
		// Names that must NOT be swept up.
		{"src", false},
		{"lib", false},
		{"libraries", false},
		{"builder", false},
		{"distribution", false},
		{"cache", false},
		{"assets", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSkippedDir(tc.name); got != tc.want {
				t.Errorf("IsSkippedDir(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// The skip set applies to directories only, and only below the root: a caller
// that explicitly names a skipped directory still gets to see inside it.
func TestWalk_SkipSet(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "src", "main.go"), "go")
	write(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "js")
	write(t, filepath.Join(root, "Library", "ScriptAssemblies", "big.dll"), "dll")
	// A FILE whose name collides with a skipped directory name must survive:
	// the skip set is about directories.
	write(t, filepath.Join(root, "build"), "this is a file, not a directory")

	res, err := Walk(Options{Root: root, Depth: MaxDepth})
	if err != nil {
		t.Fatal(err)
	}
	if find(res.Nodes, "node_modules") != nil {
		t.Errorf("node_modules leaked into the tree: %v", names(res.Nodes))
	}
	if find(res.Nodes, "Library") != nil {
		t.Errorf("Unity Library leaked into the tree: %v", names(res.Nodes))
	}
	if n := find(res.Nodes, "build"); n == nil || n.IsDir {
		t.Errorf("a FILE named build must survive the directory skip set: %+v", n)
	}
	if at(t, res.Nodes, "src/main.go") == nil {
		t.Errorf("real source missing: %v", names(res.Nodes))
	}

	// Explicit navigation into a skipped directory lists it.
	inner, err := Walk(Options{Root: filepath.Join(root, "node_modules"), Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if find(inner.Nodes, "pkg") == nil {
		t.Errorf("naming a skipped directory outright must list it: %v", names(inner.Nodes))
	}
}

// ---------------------------------------------------------------------------
// gitignore
// ---------------------------------------------------------------------------

func TestWalk_Gitignore(t *testing.T) {
	t.Run("root .gitignore excludes files and directories", func(t *testing.T) {
		root := t.TempDir()
		gitInit(t, root)
		write(t, filepath.Join(root, ".gitignore"), "generated.txt\nout/\n*.log\n")
		write(t, filepath.Join(root, "generated.txt"), "gen")
		write(t, filepath.Join(root, "keep.txt"), "keep")
		write(t, filepath.Join(root, "app.log"), "log")
		write(t, filepath.Join(root, "out", "bundle.js"), "js")
		write(t, filepath.Join(root, "kept", "file.txt"), "f")

		res, err := Walk(Options{Root: root, Depth: MaxDepth, ShowHidden: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, gone := range []string{"generated.txt", "app.log", "out"} {
			if find(res.Nodes, gone) != nil {
				t.Errorf("%q is gitignored but appeared: %v", gone, names(res.Nodes))
			}
		}
		for _, kept := range []string{"keep.txt", "kept"} {
			if find(res.Nodes, kept) == nil {
				t.Errorf("%q is tracked but is missing: %v", kept, names(res.Nodes))
			}
		}
	})

	t.Run("the Unity case: bracket class matches the real directory name", func(t *testing.T) {
		root := t.TempDir()
		gitInit(t, root)
		// Verbatim from a real Unity .gitignore.
		write(t, filepath.Join(root, ".gitignore"), "[Ll]ibrary/\n[Bb]uild[s]/\n[Uu]ser[Ss]ettings/\n")
		write(t, filepath.Join(root, "UserSettings", "Layouts", "default.dwlt"), "x")
		write(t, filepath.Join(root, "Assets", "Scripts", "Player.cs"), "cs")

		res, err := Walk(Options{Root: root, Depth: MaxDepth, ShowHidden: true})
		if err != nil {
			t.Fatal(err)
		}
		if find(res.Nodes, "UserSettings") != nil {
			t.Errorf("[Uu]ser[Ss]ettings/ did not match UserSettings: %v", names(res.Nodes))
		}
		if at(t, res.Nodes, "Assets/Scripts/Player.cs") == nil {
			t.Errorf("tracked Unity sources missing: %v", names(res.Nodes))
		}
	})

	t.Run("nested .gitignore applies to its own subtree only", func(t *testing.T) {
		root := t.TempDir()
		gitInit(t, root)
		write(t, filepath.Join(root, "a", ".gitignore"), "secret.txt\n")
		write(t, filepath.Join(root, "a", "secret.txt"), "s")
		write(t, filepath.Join(root, "a", "public.txt"), "p")
		write(t, filepath.Join(root, "b", "secret.txt"), "s")

		res, err := Walk(Options{Root: root, Depth: MaxDepth, ShowHidden: true})
		if err != nil {
			t.Fatal(err)
		}
		if at(t, res.Nodes, "a/secret.txt") != nil {
			t.Error("a/.gitignore did not exclude a/secret.txt")
		}
		if at(t, res.Nodes, "a/public.txt") == nil {
			t.Error("a/public.txt should be listed")
		}
		if at(t, res.Nodes, "b/secret.txt") == nil {
			t.Error("a/.gitignore must NOT reach into b/")
		}
	})

	t.Run("negation re-includes", func(t *testing.T) {
		root := t.TempDir()
		gitInit(t, root)
		write(t, filepath.Join(root, ".gitignore"), "*.bin\n!keep.bin\n")
		write(t, filepath.Join(root, "drop.bin"), "d")
		write(t, filepath.Join(root, "keep.bin"), "k")

		res, err := Walk(Options{Root: root, Depth: 1, ShowHidden: true})
		if err != nil {
			t.Fatal(err)
		}
		if find(res.Nodes, "drop.bin") != nil {
			t.Errorf("*.bin should have excluded drop.bin: %v", names(res.Nodes))
		}
		if find(res.Nodes, "keep.bin") == nil {
			t.Errorf("!keep.bin should have re-included it: %v", names(res.Nodes))
		}
	})

	t.Run("ancestor rules apply when the walk starts below the repo root", func(t *testing.T) {
		root := t.TempDir()
		gitInit(t, root)
		write(t, filepath.Join(root, ".gitignore"), "*.log\n")
		write(t, filepath.Join(root, "sub", "app.log"), "l")
		write(t, filepath.Join(root, "sub", "app.go"), "g")

		res, err := Walk(Options{Root: filepath.Join(root, "sub"), Depth: 1, ShowHidden: true})
		if err != nil {
			t.Fatal(err)
		}
		if find(res.Nodes, "app.log") != nil {
			t.Errorf("repo-root .gitignore must govern a subdirectory walk: %v", names(res.Nodes))
		}
		if find(res.Nodes, "app.go") == nil {
			t.Errorf("app.go missing: %v", names(res.Nodes))
		}
	})

	t.Run("naming an ignored directory outright lists its contents", func(t *testing.T) {
		root := t.TempDir()
		gitInit(t, root)
		write(t, filepath.Join(root, ".gitignore"), "out/\n")
		write(t, filepath.Join(root, "out", "bundle.js"), "js")

		res, err := Walk(Options{Root: filepath.Join(root, "out"), Depth: 1, ShowHidden: true})
		if err != nil {
			t.Fatal(err)
		}
		if find(res.Nodes, "bundle.js") == nil {
			t.Errorf("explicit navigation into an ignored dir must still list it: %v", names(res.Nodes))
		}
	})

	t.Run("no repository means no gitignore rules", func(t *testing.T) {
		root := t.TempDir()
		// A .gitignore with no repository around it is inert, exactly as git
		// treats it.
		write(t, filepath.Join(root, ".gitignore"), "data.txt\n")
		write(t, filepath.Join(root, "data.txt"), "d")

		res, err := Walk(Options{Root: root, Depth: 1, ShowHidden: true})
		if err != nil {
			t.Fatal(err)
		}
		if find(res.Nodes, "data.txt") == nil {
			t.Errorf("outside a repository the walk must not apply .gitignore: %v", names(res.Nodes))
		}
	})

	t.Run(".git/info/exclude is honoured", func(t *testing.T) {
		root := t.TempDir()
		gitInit(t, root)
		write(t, filepath.Join(root, ".git", "info", "exclude"), "# comment\n\nlocal-notes.md\n")
		write(t, filepath.Join(root, "local-notes.md"), "notes")
		write(t, filepath.Join(root, "README.md"), "readme")

		res, err := Walk(Options{Root: root, Depth: 1, ShowHidden: true})
		if err != nil {
			t.Fatal(err)
		}
		if find(res.Nodes, "local-notes.md") != nil {
			t.Errorf(".git/info/exclude not applied: %v", names(res.Nodes))
		}
		if find(res.Nodes, "README.md") == nil {
			t.Errorf("README.md missing: %v", names(res.Nodes))
		}
	})

	t.Run("an unreadable .gitignore fails open", func(t *testing.T) {
		root := t.TempDir()
		gitInit(t, root)
		// A directory named .gitignore cannot be read as a file. The walk must
		// carry on rather than disappear.
		mkdir(t, filepath.Join(root, ".gitignore"))
		write(t, filepath.Join(root, "keep.txt"), "k")

		res, err := Walk(Options{Root: root, Depth: 1})
		if err != nil {
			t.Fatalf("an unreadable .gitignore must not fail the walk: %v", err)
		}
		if find(res.Nodes, "keep.txt") == nil {
			t.Errorf("keep.txt missing: %v", names(res.Nodes))
		}
	})
}

// The crash came in through a caller that always asks for hidden files and
// filters them client-side. show_hidden must therefore not be an escape hatch
// out of the bounds.
func TestWalk_ShowHiddenDoesNotDisableBounds(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	write(t, filepath.Join(root, ".gitignore"), "out/\n")
	write(t, filepath.Join(root, "out", "bundle.js"), "js")
	write(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "js")
	write(t, filepath.Join(root, ".env"), "SECRET=1")

	res, err := Walk(Options{Root: root, Depth: MaxDepth, ShowHidden: true})
	if err != nil {
		t.Fatal(err)
	}
	if find(res.Nodes, "out") != nil {
		t.Error("show_hidden must not disable gitignore")
	}
	if find(res.Nodes, "node_modules") != nil {
		t.Error("show_hidden must not disable the skip set")
	}
	if find(res.Nodes, ".env") == nil {
		t.Error("show_hidden must still reveal dotfiles")
	}

	hidden, err := Walk(Options{Root: root, Depth: 1, ShowHidden: false})
	if err != nil {
		t.Fatal(err)
	}
	if find(hidden.Nodes, ".env") != nil {
		t.Error("dotfiles must stay hidden without show_hidden")
	}
}

// ---------------------------------------------------------------------------
// has_children
// ---------------------------------------------------------------------------

// The chevron must never promise children that expanding would not reveal. The
// boundary probe and the walk share one predicate, so this is a test that the
// sharing actually holds across every filter: skip set, gitignore and hidden.
func TestWalk_HasChildrenAgreesWithTheWalk(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	write(t, filepath.Join(root, ".gitignore"), "*.tmp\n")

	// real: a genuine child. chevron expected.
	write(t, filepath.Join(root, "level1", "real", "child.txt"), "c")
	// onlySkipped: everything inside is in the skip set.
	mkdir(t, filepath.Join(root, "level1", "onlySkipped", "node_modules"))
	// onlyIgnored: everything inside is gitignored.
	write(t, filepath.Join(root, "level1", "onlyIgnored", "scratch.tmp"), "t")
	// onlyHidden: everything inside is a dotfile.
	write(t, filepath.Join(root, "level1", "onlyHidden", ".dotfile"), "d")
	// empty: nothing inside at all.
	mkdir(t, filepath.Join(root, "level1", "empty"))
	// nestedGitignore: excluded by a .gitignore inside the boundary dir itself,
	// which the probe has to read to answer correctly.
	write(t, filepath.Join(root, "level1", "nestedIgnore", ".gitignore"), "*\n")
	write(t, filepath.Join(root, "level1", "nestedIgnore", "anything.txt"), "a")

	// Depth 2 puts every dir under level1 exactly at the boundary.
	res, err := Walk(Options{Root: root, Depth: 2})
	if err != nil {
		t.Fatal(err)
	}
	level1 := find(res.Nodes, "level1")
	if level1 == nil {
		t.Fatalf("level1 missing: %v", names(res.Nodes))
	}

	tests := []struct {
		dir  string
		want bool
	}{
		{"real", true},
		{"onlySkipped", false},
		{"onlyIgnored", false},
		{"onlyHidden", false},
		{"empty", false},
		{"nestedIgnore", false},
	}

	for _, tc := range tests {
		t.Run(tc.dir, func(t *testing.T) {
			node := find(level1.Children, tc.dir)
			if node == nil {
				t.Fatalf("%s missing at the boundary: %v", tc.dir, names(level1.Children))
			}
			if len(node.Children) != 0 {
				t.Fatalf("%s should be AT the boundary, but carries children: %v", tc.dir, names(node.Children))
			}
			if node.HasChildren != tc.want {
				t.Errorf("%s HasChildren = %v, want %v", tc.dir, node.HasChildren, tc.want)
			}

			// The hint must equal what an expand actually returns. This is the
			// property; the table above is just the evidence for it.
			expanded, err := Walk(Options{Root: filepath.Join(root, "level1", tc.dir), Depth: 1})
			if err != nil {
				t.Fatalf("expand %s: %v", tc.dir, err)
			}
			// An explicitly-named ignored dir turns its rules off, so compare
			// against the case the UI can actually reach: a dir the walk showed.
			if tc.dir != "nestedIgnore" && tc.dir != "onlyIgnored" {
				if got := len(expanded.Nodes) > 0; got != node.HasChildren {
					t.Errorf("%s: chevron says %v but expanding returned %d children",
						tc.dir, node.HasChildren, len(expanded.Nodes))
				}
			}
		})
	}
}

// Directories above the boundary carry loaded children AND the hint, so a UI
// that reads only has_children behaves the same at every level.
func TestWalk_HasChildrenSetAboveTheBoundary(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a", "b", "c.txt"), "c")

	res, err := Walk(Options{Root: root, Depth: 3})
	if err != nil {
		t.Fatal(err)
	}
	a := find(res.Nodes, "a")
	if a == nil || !a.HasChildren || len(a.Children) == 0 {
		t.Fatalf("a should carry both children and the hint: %+v", a)
	}
	b := find(a.Children, "b")
	if b == nil || !b.HasChildren || len(b.Children) == 0 {
		t.Fatalf("a/b should carry both children and the hint: %+v", b)
	}
}

// ---------------------------------------------------------------------------
// node shape and paths
// ---------------------------------------------------------------------------

func TestWalk_NodeShape(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "hello.txt"), "hello world") // 11 bytes
	write(t, filepath.Join(root, "pkg", "inner.txt"), "b")

	res, err := Walk(Options{Root: root, RelBase: "/", Depth: 2})
	if err != nil {
		t.Fatal(err)
	}

	hello := find(res.Nodes, "hello.txt")
	if hello == nil {
		t.Fatalf("hello.txt missing: %v", names(res.Nodes))
	}
	if hello.IsDir {
		t.Error("hello.txt typed as a directory")
	}
	if hello.Size != 11 {
		t.Errorf("Size = %d, want 11 (a live stat, not a cached or index value)", hello.Size)
	}
	if hello.ModTime.IsZero() {
		t.Error("ModTime is zero")
	}
	if hello.Path != filepath.Join("/", "hello.txt") {
		t.Errorf("Path = %q, want it rebased onto RelBase", hello.Path)
	}

	inner := at(t, res.Nodes, "pkg/inner.txt")
	if inner == nil {
		t.Fatal("pkg/inner.txt missing")
	}
	if want := filepath.Join("/", "pkg", "inner.txt"); inner.Path != want {
		t.Errorf("child Path = %q, want %q", inner.Path, want)
	}
}

func TestWalk_UnreadableRootErrors(t *testing.T) {
	_, err := Walk(Options{Root: filepath.Join(t.TempDir(), "does-not-exist")})
	if err == nil {
		t.Error("a root that cannot be read must return an error")
	}
}

// ---------------------------------------------------------------------------
// WalkHashable
// ---------------------------------------------------------------------------

func TestWalkHashable(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "a")
	write(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "js")
	write(t, filepath.Join(root, "Library", "cache.bin"), "b")
	write(t, filepath.Join(root, "src", "main.go"), "go")

	var visited []string
	truncated, err := WalkHashable(root, 0, func(path string, _ os.DirEntry) error {
		rel, _ := filepath.Rel(root, path)
		visited = append(visited, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Error("a four-entry tree must not truncate under the default budget")
	}
	for _, v := range visited {
		if strings.HasPrefix(v, "node_modules") || strings.HasPrefix(v, "Library") {
			t.Errorf("skipped directory reached the hash: %q", v)
		}
	}
	if !slices.Contains(visited, "src/main.go") {
		t.Errorf("real sources missing from the hash walk: %v", visited)
	}

	// The root itself is never skipped, even when its own name is in the set.
	skippedRoot := filepath.Join(root, "node_modules")
	count := 0
	if _, err := WalkHashable(skippedRoot, 0, func(string, os.DirEntry) error {
		count++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Error("naming a skipped directory as the root must still walk it")
	}
}
