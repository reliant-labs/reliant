// Copyright (c) 2025 Reliant Labs

package filetree

import (
	"os"
	"path/filepath"
	"testing"
)

// Every folder the tree lists must open, or say why it cannot. These tests pin
// the two ways a real folder came up as a leaf in the Files tab: a hidden
// directory whose contents .gitignore hid, and a symbolic link to a directory
// that ReadDir reports as a link, not a directory.

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}
}

// The owner's repro (2026-11-26): `.reliant.local/` is in the project's
// .gitignore, an agent creates it and writes mcp.json into it, and the Files
// tab shows the folder with nothing to open, Refresh included. The folder was
// shown because show_hidden reveals an ignored hidden directory, but the same
// ignore rule then hid every file in it. An explicit walk of the folder turned
// the rules off and DID list mcp.json, so the chevron and the expand disagreed.
func TestWalk_IgnoredHiddenDirOpens(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	write(t, filepath.Join(root, ".gitignore"),
		".reliant.local/\n.forge/\n.vscode/*\n!.vscode/settings.json\nout/\n*.log\n")
	write(t, filepath.Join(root, "README.md"), "r")

	// The folder is created empty first, and the tree is loaded.
	mkdir(t, filepath.Join(root, ".reliant.local"))
	first, err := Walk(Options{Root: root, Depth: 2, ShowHidden: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := find(first.Nodes, ".reliant.local"); n == nil || !n.IsDir || n.Expandable() {
		t.Fatalf("an empty .reliant.local must be a directory with nothing to expand, got %+v", n)
	}

	// Then the agent writes into it, and the user presses Refresh, which
	// re-reads the root at depth 2 and each expanded directory at depth 1.
	write(t, filepath.Join(root, ".reliant.local", "mcp.json"), "{}")
	write(t, filepath.Join(root, ".forge", "cache", "state.json"), "{}")
	write(t, filepath.Join(root, ".vscode", "launch.json"), "{}")
	write(t, filepath.Join(root, ".vscode", "settings.json"), "{}")
	write(t, filepath.Join(root, ".reliant.local", "debug.log"), "l")
	write(t, filepath.Join(root, "out", "bundle.js"), "js")
	write(t, filepath.Join(root, "src", "app.log"), "l")
	write(t, filepath.Join(root, "src", "app.go"), "g")

	for _, depth := range []int{1, 2} {
		res, err := Walk(Options{Root: root, Depth: depth, ShowHidden: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{".reliant.local", ".forge", ".vscode"} {
			n := find(res.Nodes, dir)
			if n == nil {
				t.Fatalf("depth %d: %s missing: %v", depth, dir, names(res.Nodes))
			}
			if !n.Expandable() {
				t.Errorf("depth %d: %s is a folder with contents but shows no chevron", depth, dir)
			}
		}
		if depth == 2 {
			// Everything in a hidden directory is revealed, ignored or not.
			for _, p := range []string{".reliant.local/mcp.json", ".reliant.local/debug.log", ".forge/cache", ".vscode/launch.json", ".vscode/settings.json"} {
				if at(t, res.Nodes, p) == nil {
					t.Errorf("depth 2: %s missing", p)
				}
			}
			// Outside hidden directories the rules still apply.
			if find(res.Nodes, "out") != nil {
				t.Error("out/ is ignored and not hidden: it must stay out of the tree")
			}
			if at(t, res.Nodes, "src/app.log") != nil {
				t.Error("src/app.log is ignored and not inside a hidden directory: it must stay out")
			}
			if at(t, res.Nodes, "src/app.go") == nil {
				t.Error("src/app.go missing")
			}
		}
	}

	// The chevron and the expand agree: expanding .reliant.local (an explicit
	// walk of it, as the UI does) and .forge/cache below it list what the
	// parent listing promised.
	expanded, err := Walk(Options{Root: filepath.Join(root, ".reliant.local"), Depth: 1, ShowHidden: true})
	if err != nil {
		t.Fatal(err)
	}
	if find(expanded.Nodes, "mcp.json") == nil || find(expanded.Nodes, "debug.log") == nil {
		t.Errorf("expanding .reliant.local: %v", names(expanded.Nodes))
	}
	nested, err := Walk(Options{Root: filepath.Join(root, ".forge", "cache"), Depth: 1, ShowHidden: true})
	if err != nil {
		t.Fatal(err)
	}
	if find(nested.Nodes, "state.json") == nil {
		t.Errorf("expanding .forge/cache: %v", names(nested.Nodes))
	}

	// Without show_hidden none of it is listed at all.
	hidden, err := Walk(Options{Root: root, Depth: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{".reliant.local", ".forge", ".vscode"} {
		if find(hidden.Nodes, dir) != nil {
			t.Errorf("%s listed without show_hidden", dir)
		}
	}
}

// A folder of every kind, in one tree: what the tree reports for each, and
// whether a UI may expand it.
func TestWalk_EveryFolderKind(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	write(t, filepath.Join(root, ".gitignore"), "ignored/\n")
	write(t, filepath.Join(root, "real", "child.txt"), "c")
	write(t, filepath.Join(root, "ignored", "x.txt"), "x")
	mkdir(t, filepath.Join(root, ".empty"))
	write(t, filepath.Join(root, ".outer", ".inner", "f.txt"), "f")
	write(t, filepath.Join(root, "store", "id_rsa"), "k")

	symlink(t, "real", filepath.Join(root, "linkdir"))
	symlink(t, filepath.Join(root, "real", "child.txt"), filepath.Join(root, "linkfile"))
	symlink(t, "missing", filepath.Join(root, "dangling"))
	symlink(t, "selfloop", filepath.Join(root, "selfloop"))
	symlink(t, "..", filepath.Join(root, "real", "up"))
	symlink(t, ".", filepath.Join(root, "here"))
	symlink(t, "store", filepath.Join(root, "creds"))

	deny := func(p string) bool { return filepath.Base(p) == "creds" }

	res, err := Walk(Options{Root: root, Depth: 2, ShowHidden: true, DenyDir: deny})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path       string
		dir        bool
		link       LinkStatus // 0: not a link
		expandable bool
	}{
		{"real", true, 0, true},
		{"linkdir", true, LinkResolved, true},
		{"linkfile", false, LinkResolved, false},
		{"dangling", false, LinkBroken, false},
		{"selfloop", false, LinkBroken, false},
		{"real/up", true, LinkLoop, false},
		{"here", true, LinkLoop, false},
		{"creds", true, LinkDenied, false},
		{".empty", true, 0, false},
		{".outer", true, 0, true},
		{".outer/.inner", true, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			n := at(t, res.Nodes, tc.path)
			if n == nil {
				t.Fatalf("%s missing", tc.path)
			}
			if n.IsDir != tc.dir {
				t.Errorf("IsDir = %v, want %v", n.IsDir, tc.dir)
			}
			switch {
			case tc.link == 0 && n.Link != nil:
				t.Errorf("not a link, but Link = %+v", n.Link)
			case tc.link != 0 && n.Link == nil:
				t.Errorf("a link, but Link is nil")
			case tc.link != 0 && n.Link.Status != tc.link:
				t.Errorf("Link.Status = %v, want %v", n.Link.Status, tc.link)
			}
			if got := n.Expandable(); got != tc.expandable {
				t.Errorf("Expandable = %v, want %v", got, tc.expandable)
			}
			if n.Link != nil && n.Link.Target == "" {
				t.Error("a link must carry its target text")
			}
		})
	}

	if find(res.Nodes, "ignored") != nil {
		t.Error("a gitignored, non-hidden directory must stay out of the tree")
	}
	if n := find(res.Nodes, "linkfile"); n != nil && n.Size != 1 {
		t.Errorf("a link to a file reports the target's size, got %d", n.Size)
	}
	// The walk never follows a link itself, at any depth.
	if n := find(res.Nodes, "linkdir"); n != nil && len(n.Children) != 0 {
		t.Errorf("the walk descended into a link: %v", names(n.Children))
	}

	// Expanding the linked directory, as the UI does, lists the target.
	expanded, err := Walk(Options{Root: filepath.Join(root, "linkdir"), Depth: 1, ShowHidden: true})
	if err != nil {
		t.Fatal(err)
	}
	if find(expanded.Nodes, "child.txt") == nil {
		t.Errorf("expanding linkdir: %v", names(expanded.Nodes))
	}

	// A walk as deep as the budget allows still terminates without following
	// a loop: every node is in the real tree once.
	deep, err := Walk(Options{Root: root, Depth: MaxDepth, ShowHidden: true, DenyDir: deny})
	if err != nil {
		t.Fatal(err)
	}
	if deep.Truncated || deep.NodeCount > 30 {
		t.Errorf("an unbounded walk followed a link: %d nodes, truncated=%v", deep.NodeCount, deep.Truncated)
	}
}

func TestWalk_UnreadableDirIsNotExpandable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 directory; the case needs an unprivileged user")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "locked", "inside.txt"), "x")
	if err := os.Chmod(filepath.Join(root, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "locked"), 0o755) })

	for _, depth := range []int{1, 2} {
		res, err := Walk(Options{Root: root, Depth: depth})
		if err != nil {
			t.Fatal(err)
		}
		n := find(res.Nodes, "locked")
		if n == nil || !n.IsDir || n.Expandable() {
			t.Errorf("depth %d: an unreadable directory is listed as a directory with no chevron, got %+v", depth, n)
		}
	}
}

func TestWithinDir(t *testing.T) {
	for _, tc := range []struct {
		p, base string
		want    bool
	}{
		{"/a/b", "/a", true},
		{"/a", "/a", true},
		{"/ab", "/a", false},
		{"/a", "/a/b", false},
		{"/..x/y", "/", true},
	} {
		if got := withinDir(filepath.FromSlash(tc.p), filepath.FromSlash(tc.base)); got != tc.want {
			t.Errorf("withinDir(%q, %q) = %v, want %v", tc.p, tc.base, got, tc.want)
		}
	}
}
