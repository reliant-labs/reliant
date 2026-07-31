package daemonruntime

import (
	"context"
	"encoding/json"
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

// getTree drives the real fs.get_tree handler end-to-end (JSON in, JSON out),
// exercising the request depth→remaining-depth mapping and the serialized node
// shape the proxy consumes.
func getTree(t *testing.T, path string, showHidden bool, depth int) []*fsFileNode {
	t.Helper()
	payload, err := json.Marshal(fsGetTreeRequest{Path: path, ShowHidden: showHidden, Depth: depth})
	if err != nil {
		t.Fatal(err)
	}
	out, err := handleFSGetTree(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleFSGetTree: %v", err)
	}
	var resp struct {
		Nodes []*fsFileNode `json:"nodes"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal tree: %v", err)
	}
	return resp.Nodes
}

// The tree is now served from a live filesystem walk, not the git index. A file
// removed with plain rm (not `git rm`, so the index still lists it) must be
// ABSENT, and surviving files must report their real on-disk size — dissolving
// both the staleness bug and the "size":0 that the git branch produced.
func TestHandleFSGetTree_LiveWalkReflectsFilesystem(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	writeFileT(t, filepath.Join(root, "keep.txt"), "hello world") // 11 bytes
	writeFileT(t, filepath.Join(root, "ghost.txt"), "gone")
	gitRun(t, root, "add", "-A") // both files now live in the git index

	// Remove ghost.txt from the working tree WITHOUT `git rm`: git ls-files
	// --cached would still report it, so the old git-index tree kept a ghost.
	if err := os.Remove(filepath.Join(root, "ghost.txt")); err != nil {
		t.Fatal(err)
	}

	nodes := getTree(t, root, false, 0)

	if ghost := findNode(nodes, "ghost.txt"); ghost != nil {
		t.Errorf("rm'd file still present in tree: %+v (git-index staleness not dissolved)", ghost)
	}
	keep := findNode(nodes, "keep.txt")
	if keep == nil {
		t.Fatalf("keep.txt missing from tree: %+v", nodes)
	}
	if keep.Size != 11 {
		t.Errorf("keep.txt size = %d, want 11 (real stat size, not git-index 0)", keep.Size)
	}
	if keep.Modified == "" {
		t.Errorf("keep.txt has empty modified time; expected a real mtime")
	}
}

// depth=1 must return only immediate children (no grandchildren) with
// has_children set on non-empty directories; depth=0 must return the full
// recursive tree (back-compat with existing callers).
func TestHandleFSGetTree_Depth(t *testing.T) {
	root := t.TempDir()
	writeFileT(t, filepath.Join(root, "top.txt"), "a")
	writeFileT(t, filepath.Join(root, "pkg", "inner.txt"), "b")
	writeFileT(t, filepath.Join(root, "pkg", "sub", "deep.txt"), "c")
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	// depth = 1: immediate children only.
	lvl1 := getTree(t, root, false, 1)

	pkg := findNode(lvl1, "pkg")
	if pkg == nil {
		t.Fatalf("pkg missing at depth 1: %+v", lvl1)
	}
	if len(pkg.Children) != 0 {
		t.Errorf("depth 1 leaked grandchildren under pkg: %+v", pkg.Children)
	}
	if !pkg.HasChildren {
		t.Errorf("non-empty dir pkg must have has_children=true at the depth boundary")
	}
	if empty := findNode(lvl1, "empty"); empty == nil || empty.HasChildren {
		t.Errorf("empty dir must have has_children=false, got %+v", empty)
	}

	// depth = 2: children + grandchildren, but not great-grandchildren.
	lvl2 := getTree(t, root, false, 2)
	pkg2 := findNode(lvl2, "pkg")
	if pkg2 == nil || findNode(pkg2.Children, "inner.txt") == nil {
		t.Fatalf("depth 2 should include pkg/inner.txt: %+v", pkg2)
	}
	sub := findNode(pkg2.Children, "sub")
	if sub == nil {
		t.Fatalf("depth 2 should include pkg/sub: %+v", pkg2.Children)
	}
	if len(sub.Children) != 0 {
		t.Errorf("depth 2 leaked great-grandchildren under pkg/sub: %+v", sub.Children)
	}
	if !sub.HasChildren {
		t.Errorf("pkg/sub must have has_children=true at the depth-2 boundary")
	}

	// depth = 0: full recursive tree.
	full := getTree(t, root, false, 0)
	fpkg := findNode(full, "pkg")
	if fpkg == nil {
		t.Fatalf("pkg missing in full tree: %+v", full)
	}
	fsub := findNode(fpkg.Children, "sub")
	if fsub == nil {
		t.Fatalf("pkg/sub missing in full tree: %+v", fpkg.Children)
	}
	if deep := findNode(fsub.Children, "deep.txt"); deep == nil {
		t.Errorf("depth 0 must recurse fully to pkg/sub/deep.txt: %+v", fsub.Children)
	}
}

// A nested checkout (embedded git repository) is just another directory to the
// live walk: it recurses into it and lists its contents, skipping only the .git
// dir. This preserves the behavior the old git-index branch special-cased,
// without emitting a zero-byte ghost file named after the directory.
func TestHandleFSGetTree_EmbeddedRepoRecursion(t *testing.T) {
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

	nodes := getTree(t, root, false, 0)

	ranger := findNode(nodes, "ranger")
	if ranger == nil {
		t.Fatalf("expected a 'ranger' node at root, got %+v", nodes)
	}
	if ranger.Type != "directory" {
		t.Fatalf("embedded repo typed %q, want directory (path %q)", ranger.Type, ranger.Path)
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

	// The old regression shape: a FILE child named after the directory itself.
	if ghost := findNode(ranger.Children, "ranger"); ghost != nil {
		t.Errorf("embedded repo produced a ghost self-named child: %+v", ghost)
	}

	// The nested repo's own .git dir must be skipped, not listed.
	if git := findNode(ranger.Children, ".git"); git != nil {
		t.Errorf("nested .git should be skipped, got %+v", git)
	}
}
