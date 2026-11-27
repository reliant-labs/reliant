// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

func symlinkT(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}
}

// expandable is the rule the web applies to a node of this reply (after the
// server's convertFsProxyNode): a directory with has_children or loaded
// children, and not a link the daemon refuses to follow.
func expandable(n *fsFileNode) bool {
	if n.Type != "directory" {
		return false
	}
	if n.SymlinkStatus != "" && n.SymlinkStatus != "resolved" {
		return false
	}
	return n.HasChildren || len(n.Children) > 0
}

// Every folder in the Files tab opens, or says why it cannot, through the real
// fs.get_tree handler: a gitignored hidden directory (the owner's
// `.reliant.local`), a symlinked directory, a symlink loop, a dangling link, a
// link into a credential store, an empty hidden directory and a nested hidden
// directory.
func TestHandleFSGetTree_EveryFolderOpens(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	writeFileT(t, filepath.Join(home, ".ssh", "id_rsa"), "key")

	root := filepath.Join(home, "proj")
	writeFileT(t, filepath.Join(root, "README.md"), "r")
	gitRun(t, root, "init", "-q")
	writeFileT(t, filepath.Join(root, ".gitignore"), ".reliant.local/\nnode_out/\n")
	writeFileT(t, filepath.Join(root, ".reliant.local", "mcp.json"), "{}")
	writeFileT(t, filepath.Join(root, "node_out", "x.js"), "x")
	writeFileT(t, filepath.Join(root, "real", "child.txt"), "c")
	writeFileT(t, filepath.Join(root, ".outer", ".inner", "f.txt"), "f")
	if err := os.MkdirAll(filepath.Join(root, ".empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkT(t, "real", filepath.Join(root, "linkdir"))
	symlinkT(t, "..", filepath.Join(root, "real", "up"))
	symlinkT(t, "missing", filepath.Join(root, "dangling"))
	symlinkT(t, filepath.Join(home, ".ssh"), filepath.Join(root, "creds"))

	call := func(path, viewerRoot string, depth int) (fsGetTreeResponse, error) {
		payload, err := json.Marshal(fsGetTreeRequest{Path: path, ShowHidden: true, Depth: depth, ViewerRoot: viewerRoot})
		if err != nil {
			t.Fatal(err)
		}
		out, err := handleFSGetTree(context.Background(), payload)
		if err != nil {
			return fsGetTreeResponse{}, err
		}
		var resp fsGetTreeResponse
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatal(err)
		}
		return resp, nil
	}

	resp, err := call(root, root, 2)
	if err != nil {
		t.Fatal(err)
	}
	nodeAt := func(path ...string) *fsFileNode {
		nodes := resp.Nodes
		var n *fsFileNode
		for _, seg := range path {
			if n = findNode(nodes, seg); n == nil {
				return nil
			}
			nodes = n.Children
		}
		return n
	}

	tests := []struct {
		path       []string
		typ        string
		symlink    string
		expandable bool
	}{
		{[]string{".reliant.local"}, "directory", "", true},
		{[]string{"real"}, "directory", "", true},
		{[]string{"linkdir"}, "directory", "resolved", true},
		{[]string{"real", "up"}, "directory", "loop", false},
		{[]string{"dangling"}, "file", "broken", false},
		{[]string{"creds"}, "directory", "denied", false},
		{[]string{".empty"}, "directory", "", false},
		{[]string{".outer"}, "directory", "", true},
		{[]string{".outer", ".inner"}, "directory", "", true},
	}
	for _, tc := range tests {
		n := nodeAt(tc.path...)
		if n == nil {
			t.Errorf("%v missing", tc.path)
			continue
		}
		if n.Type != tc.typ || n.SymlinkStatus != tc.symlink || expandable(n) != tc.expandable {
			t.Errorf("%v: type=%q symlink=%q expandable=%v, want type=%q symlink=%q expandable=%v",
				tc.path, n.Type, n.SymlinkStatus, expandable(n), tc.typ, tc.symlink, tc.expandable)
		}
		if tc.symlink != "" && n.SymlinkTarget == "" {
			t.Errorf("%v: a link must carry its target", tc.path)
		}
	}
	if nodeAt("node_out") != nil {
		t.Error("a gitignored, non-hidden directory must stay out of the tree")
	}
	if nodeAt(".reliant.local", "mcp.json") == nil {
		t.Error(".reliant.local must list mcp.json")
	}

	// Expanding the symlinked directory lists its target.
	linked, err := call(filepath.Join(root, "linkdir"), root, 1)
	if err != nil {
		t.Fatal(err)
	}
	if findNode(linked.Nodes, "child.txt") == nil {
		t.Errorf("expanding linkdir: %+v", linked.Nodes)
	}

	// Even a caller claiming the store itself as its workspace cannot exempt it.
	store := filepath.Join(home, ".ssh")
	if _, err := call(store, store, 1); !errors.Is(err, daemonpolicy.ErrViewerCredential) {
		t.Errorf("listing a credential store as its own viewer root = %v, want ErrViewerCredential", err)
	}

	// Naming the link into ~/.ssh outright is refused like the viewer's read.
	if _, err := call(filepath.Join(root, "creds"), root, 1); !errors.Is(err, daemonpolicy.ErrViewerCredential) {
		t.Errorf("listing a credential store through a link = %v, want ErrViewerCredential", err)
	}
}

func TestHandleFSGetTree_WorkspaceBoundaryUnconfined(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	outside := filepath.Join(parent, "workspace-other")
	writeFileT(t, filepath.Join(root, "inside", "child.txt"), "inside")
	writeFileT(t, filepath.Join(outside, "private.txt"), "outside")
	symlinkT(t, outside, filepath.Join(root, "escape"))
	symlinkT(t, "inside", filepath.Join(root, "allowed"))
	alias := filepath.Join(parent, "workspace-alias")
	symlinkT(t, root, alias)

	call := func(path, viewerRoot string) ([]byte, error) {
		t.Helper()
		payload, err := json.Marshal(fsGetTreeRequest{Path: path, ViewerRoot: viewerRoot, Depth: 1})
		if err != nil {
			t.Fatal(err)
		}
		return handleFSGetTree(context.Background(), payload)
	}
	for _, viewerRoot := range []string{root, alias} {
		out, err := call(root, viewerRoot)
		if err != nil {
			t.Fatal(err)
		}
		var resp fsGetTreeResponse
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatal(err)
		}
		n := findNode(resp.Nodes, "escape")
		if n == nil || n.SymlinkStatus != "denied" || expandable(n) {
			t.Fatalf("out-of-workspace link must be denied: %+v", n)
		}
		for _, path := range []string{filepath.Join(root, "escape"), outside} {
			if _, err := call(path, viewerRoot); !errors.Is(err, os.ErrPermission) {
				t.Errorf("listing %s: %v, want permission denied", path, err)
			}
		}
		for _, path := range []string{filepath.Join(root, "allowed"), filepath.Join(root, "inside")} {
			out, err := call(path, viewerRoot)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(out, &resp); err != nil {
				t.Fatal(err)
			}
			if findNode(resp.Nodes, "child.txt") == nil {
				t.Errorf("in-workspace expansion %s missing child", path)
			}
		}
	}
}

// The directory picker (fs.list_dir) enters a symlinked directory: it is
// listed as a directory, a dangling link as a file.
func TestHandleFSListDir_SymlinkedDirIsADirectory(t *testing.T) {
	root := t.TempDir()
	writeFileT(t, filepath.Join(root, "real", "f.txt"), "f")
	symlinkT(t, "real", filepath.Join(root, "linkdir"))
	symlinkT(t, "missing", filepath.Join(root, "dangling"))

	payload, err := json.Marshal(fsListDirRequest{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	out, err := handleFSListDir(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	var resp fsListDirResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range resp.Entries {
		got[e.Name] = e.IsDir
	}
	if !got["linkdir"] {
		t.Error("a symlink to a directory must be listed as a directory")
	}
	if isDir, ok := got["dangling"]; !ok || isDir {
		t.Error("a dangling link must be listed as a file")
	}
}
