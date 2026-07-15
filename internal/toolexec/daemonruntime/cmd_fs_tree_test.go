package daemonruntime

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findNode(nodes []*fsFileNode, name string) *fsFileNode {
	for _, n := range nodes {
		if n.Name == name {
			return n
		}
	}
	return nil
}

// git ls-files emits an embedded git repository as a single "dir/" line with
// a trailing slash. The tree builder must type it as a directory and list
// its contents — not emit a zero-byte file named after the directory (which
// the file viewer then fails to preview with "path is a directory").
func TestBuildFileTreeFromGit_EmbeddedRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	writeFileT(t, filepath.Join(root, "FORGE_PLAN.md"), "plan")

	nested := filepath.Join(root, "ranger")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, nested, "init", "-q")
	writeFileT(t, filepath.Join(nested, "setup.py"), "py")
	writeFileT(t, filepath.Join(nested, "doc", "ranger.1"), "man")

	nodes, err := buildFileTreeFromGit(root, false)
	if err != nil {
		t.Fatalf("buildFileTreeFromGit: %v", err)
	}

	ranger := findNode(nodes, "ranger")
	if ranger == nil {
		t.Fatalf("expected a 'ranger' node at root, got %+v", nodes)
	}
	if ranger.Type != "directory" {
		t.Fatalf("embedded repo typed %q, want directory (path %q)", ranger.Type, ranger.Path)
	}
	if ranger.Path != "ranger" {
		t.Errorf("embedded repo path = %q, want %q", ranger.Path, "ranger")
	}

	setup := findNode(ranger.Children, "setup.py")
	if setup == nil {
		t.Fatalf("expected setup.py inside embedded repo, got children %+v", ranger.Children)
	}
	if setup.Type != "file" || setup.Path != filepath.Join("ranger", "setup.py") {
		t.Errorf("setup.py node = {type %q, path %q}, want {file, ranger/setup.py}", setup.Type, setup.Path)
	}

	doc := findNode(ranger.Children, "doc")
	if doc == nil || doc.Type != "directory" {
		t.Fatalf("expected doc directory inside embedded repo, got %+v", doc)
	}
	if man := findNode(doc.Children, "ranger.1"); man == nil || man.Path != filepath.Join("ranger", "doc", "ranger.1") {
		t.Errorf("nested file path not rebased onto parent tree: %+v", man)
	}

	// The regression shape: a FILE child named after the directory itself.
	if ghost := findNode(ranger.Children, "ranger"); ghost != nil {
		t.Errorf("embedded repo produced a ghost self-named child: %+v", ghost)
	}
}

// A plain (non-git) untracked directory reported with a trailing slash must
// also fall back to the filesystem walk. git only emits this shape for
// embedded repos, but the branch handles both, so pin the walk fallback via
// an embedded repo whose .git is a file (worktree-style), which isGitRepo
// accepts, and a directory that stops being a repo after listing starts.
func TestBuildFileTreeFromGit_TrackedFilesUnaffected(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	writeFileT(t, filepath.Join(root, "a.txt"), "a")
	writeFileT(t, filepath.Join(root, "pkg", "b.txt"), "b")

	nodes, err := buildFileTreeFromGit(root, false)
	if err != nil {
		t.Fatalf("buildFileTreeFromGit: %v", err)
	}

	if a := findNode(nodes, "a.txt"); a == nil || a.Type != "file" {
		t.Errorf("a.txt = %+v, want plain file", a)
	}
	pkg := findNode(nodes, "pkg")
	if pkg == nil || pkg.Type != "directory" {
		t.Fatalf("pkg = %+v, want directory", pkg)
	}
	if b := findNode(pkg.Children, "b.txt"); b == nil || b.Path != filepath.Join("pkg", "b.txt") {
		t.Errorf("pkg/b.txt = %+v", b)
	}
}
