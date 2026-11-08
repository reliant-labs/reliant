//go:build linux

package worktreereclaim

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func retirementFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, ".git"), "gitdir: fixture")
	write(t, filepath.Join(root, "src", "main.go"), "source")
	write(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "regenerable")
	return root
}
func TestRetirementTreeRejectsProtectedDescendants(t *testing.T) {
	for _, rel := range []string{"tmp/local.sqlite", "tmp/.env", "node_modules/pkg/data/only-copy.csv", ".tools/secrets/key"} {
		t.Run(rel, func(t *testing.T) {
			root := retirementFixture(t)
			p := filepath.Join(root, rel)
			write(t, p, "preserved")
			tree, err := inspectRetirementTree(context.Background(), root)
			if tree != nil {
				tree.close()
			}
			if err == nil {
				t.Fatal("protected descendant authorized")
			}
			if !exists(p) {
				t.Fatal("inspection deleted data")
			}
		})
	}
}

// A never-listed name inside a package an install put in node_modules, and
// recorded there, is that package's content (packages ship tests/data, .env
// fixtures, *.db samples), both when the whole checkout is inspected and when
// node_modules alone is. The same names anywhere else still hold — including
// in a package.json-bearing directory no install recorded.
func TestRetirementTreeInstalledPackageContent(t *testing.T) {
	pkgs := map[string]string{
		"npm":        "node_modules/pkg",
		"scoped":     "node_modules/@scope/lib",
		"pnpm":       "node_modules/.pnpm/@protobufjs+utf8@1.1.2/node_modules/@protobufjs/utf8",
		"pnpm-peers": "node_modules/.pnpm/react-dom@18.2.0_react@18.2.0/node_modules/react-dom",
		"nested":     "node_modules/outer/node_modules/inner",
	}
	for name, pkg := range pkgs {
		t.Run(name, func(t *testing.T) {
			root := retirementFixture(t)
			switch name {
			case "pnpm":
				recordInstall(t, filepath.Join(root, "node_modules"), []string{"@protobufjs/utf8@1.1.2"})
			case "pnpm-peers":
				recordInstall(t, filepath.Join(root, "node_modules"), []string{"react-dom@18.2.0(react@18.2.0)", "react@18.2.0"})
			default:
				recordInstall(t, filepath.Join(root, "node_modules"), nil, pkg, "node_modules/outer")
			}
			write(t, filepath.Join(root, pkg, "package.json"), `{"name":"x"}`)
			for _, rel := range []string{"tests/data/fixture.bin", "test/.env", "samples/demo.sqlite", "lib/secrets/index.js"} {
				write(t, filepath.Join(root, pkg, rel), "package content")
			}
			if name == "nested" {
				write(t, filepath.Join(root, "node_modules/outer/package.json"), `{"name":"outer"}`)
			}
			for _, inspect := range []struct {
				what string
				path string
				fn   func(context.Context, string) (*retirementTree, error)
			}{{"checkout", root, inspectRetirementTree}, {"node_modules", filepath.Join(root, "node_modules"), inspectRegenerableTree}} {
				tree, err := inspect.fn(context.Background(), inspect.path)
				if err != nil {
					t.Fatalf("%s: installed package content held the tree: %v", inspect.what, err)
				}
				if err := tree.validate(context.Background()); err != nil {
					t.Fatalf("%s: validate: %v", inspect.what, err)
				}
				tree.close()
			}
		})
	}
	// Each hold case: the protected file, and how node_modules is recorded.
	for _, hold := range []struct {
		rel  string
		pnpm []string
		npm  []string
	}{
		{"node_modules/data/only-copy.csv", nil, []string{"node_modules/lib"}},                // node_modules' own data/
		{"node_modules/pkg/data/only-copy.csv", nil, []string{"node_modules/pkg"}},            // pkg has no package.json
		{"node_modules/@scope/data/only-copy.csv", nil, []string{"node_modules/@scope/data"}}, // a scope directory is no package
		{"node_modules/.cache/secrets/token", []string{}, []string{"node_modules/.cache"}},    // a tool cache, not a package
		{"node_modules/pkg/package.json/data/x.csv", nil, []string{"node_modules/pkg"}},       // a directory is no manifest
		// A package.json proves nothing: a hand-kept package no install
		// recorded holds, whether node_modules has a record or none.
		{"node_modules/mine/data/only-copy.csv", nil, []string{"node_modules/lib"}},
		{"node_modules/mine/.env", nil, nil},
		// pnpm's store shape without pnpm's record, or a store entry that is
		// not the package its id names.
		{"node_modules/.pnpm/mine@1.0.0/node_modules/mine/data/only-copy.csv", nil, []string{"node_modules/lib"}},
		{"node_modules/.pnpm/other@1.0.0/node_modules/mine/data/only-copy.csv", []string{"other@1.0.0", "mine@1.0.0"}, nil},
		// Both pnpm markers present, but the lockfile does not name the
		// package, or names it at another version.
		{"node_modules/.pnpm/mine@1.0.0/node_modules/mine/data/only-copy.csv", []string{"lib@1.0.0"}, nil},
		{"node_modules/.pnpm/mine@1.0.0/node_modules/mine/.env", []string{}, nil},
		{"node_modules/.pnpm/mine@1.0.0/node_modules/mine/data/only-copy.csv", []string{"mine@2.0.0", "mine@1.0.0-beta"}, nil},
		// The lockfile names the package and version, but not the exact
		// id the store directory is named after: a suffix it does not
		// name, another peer set, or a malformed peer suffix.
		{"node_modules/.pnpm/mine@1.0.0_evil/node_modules/mine/data/only-copy.csv", []string{"mine@1.0.0"}, nil},
		{"node_modules/.pnpm/mine@1.0.0_react@18.2.0/node_modules/mine/data/only-copy.csv", []string{"mine@1.0.0", "mine@1.0.0(react@17.0.0)"}, nil},
		{"node_modules/.pnpm/mine@1.0.0_react@18.2.0/node_modules/mine/data/only-copy.csv", []string{"mine@1.0.0(react@18.2.0", "mine@1.0.0(react@18.2.0)x"}, nil},
		{"node_modules/.pnpm/mine@1.0.0/node_modules/mine/data/only-copy.csv", []string{"mine@1.0.0_evil"}, nil},
	} {
		t.Run("holds/"+hold.rel, func(t *testing.T) {
			root := retirementFixture(t)
			if hold.npm != nil || hold.pnpm != nil {
				recordInstall(t, filepath.Join(root, "node_modules"), hold.pnpm, hold.npm...)
			}
			write(t, filepath.Join(root, "node_modules/lib/package.json"), `{"name":"lib"}`)
			for _, dir := range []string{"node_modules/mine", "node_modules/.pnpm/mine@1.0.0/node_modules/mine", "node_modules/.pnpm/other@1.0.0/node_modules/mine", "node_modules/.pnpm/mine@1.0.0_evil/node_modules/mine", "node_modules/.pnpm/mine@1.0.0_react@18.2.0/node_modules/mine"} {
				if strings.HasPrefix(hold.rel, dir+"/") {
					write(t, filepath.Join(root, dir, "package.json"), `{"name":"mine"}`)
				}
			}
			p := filepath.Join(root, hold.rel)
			write(t, p, "preserved")
			for _, inspect := range []struct {
				path string
				fn   func(context.Context, string) (*retirementTree, error)
			}{{root, inspectRetirementTree}, {filepath.Join(root, "node_modules"), inspectRegenerableTree}} {
				tree, err := inspect.fn(context.Background(), inspect.path)
				if tree != nil {
					tree.close()
				}
				if err == nil {
					t.Fatalf("%s: protected descendant authorized", inspect.path)
				}
			}
			if b, err := os.ReadFile(p); err != nil || string(b) != "preserved" {
				t.Fatalf("protected content touched: %q %v", b, err)
			}
		})
	}
}

func TestRetirementTreeMutationAndContainment(t *testing.T) {
	for _, kind := range []string{"new-protected", "new-file", "file-replaced", "ancestor-symlink", "root-symlink", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			root := retirementFixture(t)
			tree, err := inspectRetirementTree(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			defer tree.close()
			outside := t.TempDir()
			protected := filepath.Join(outside, "only-copy")
			write(t, protected, "external")
			switch kind {
			case "new-protected":
				write(t, filepath.Join(root, "node_modules", "pkg", ".env"), "new")
			case "new-file":
				write(t, filepath.Join(root, "new.txt"), "new")
			case "file-replaced":
				p := filepath.Join(root, "src", "main.go")
				if err := os.Rename(p, p+".old"); err != nil {
					t.Fatal(err)
				}
				write(t, p, "replacement")
			case "ancestor-symlink":
				if err := os.Rename(filepath.Join(root, "src"), filepath.Join(root, "old-src")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(root, "src")); err != nil {
					t.Fatal(err)
				}
			case "root-symlink":
				if err := os.Rename(root, root+"-original"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.RemoveAll(root + "-original") })
				if err := os.Symlink(outside, root); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				cancel()
			}
			if err := tree.remove(ctx, func() error { return nil }); err == nil {
				t.Fatal("changed/uncertain tree removed")
			}
			if !exists(protected) {
				t.Fatal("escaped pinned root")
			}
		})
	}
}
func TestRetirementTreeRemovesOnlyInventoriedTree(t *testing.T) {
	root := retirementFixture(t)
	tree, err := inspectRetirementTree(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.close()
	if err := tree.remove(context.Background(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if exists(root) {
		t.Fatal("safe tree was not removed")
	}
}
func TestRetirementTreePermissionErrorPreservesRecoveryPointer(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory mode")
	}
	root := retirementFixture(t)
	tree, err := inspectRetirementTree(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.close()
	locked := filepath.Join(root, "node_modules", "pkg")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0700)
	if err := tree.remove(context.Background(), func() error { return nil }); err == nil {
		t.Fatal("permission uncertainty must hold")
	}
	if !exists(filepath.Join(root, ".git")) {
		t.Fatal("recovery pointer removed on permission error")
	}
}

func TestRetirementTreePartialFailureKeepsGitPointer(t *testing.T) {
	root := retirementFixture(t)
	tree, err := inspectRetirementTree(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.close()
	calls := 0
	err = tree.remove(context.Background(), func() error {
		calls++
		if calls == 2 {
			return errors.New("process became active")
		}
		return nil
	})
	if err == nil {
		t.Fatal("failure lost")
	}
	if !exists(filepath.Join(root, ".git")) {
		t.Fatal("partial deletion discarded recovery registration")
	}
}

// A process of unknown scope (an exec, a terminal shell) may open a file in
// the tree just after an inspection passes and write it after the unlink.
// The removal keeps the unlinked file open until the next inspection, sees
// that it was written, puts its bytes back and stops. The inspections here
// are the check callback: the writer opens after the first one and has
// written and closed before the second, so neither inspection sees it.
func TestRetirementTreePutsBackAFileWrittenAfterTheLastInspection(t *testing.T) {
	root := retirementFixture(t)
	tree, err := inspectRetirementTree(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.close()
	// Deepest first: node_modules/pkg/index.js is the first entry unlinked.
	target := filepath.Join(root, "node_modules", "pkg", "index.js")
	var writer *os.File
	calls := 0
	err = tree.remove(context.Background(), func() error {
		calls++
		switch calls {
		case 1:
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			writer = f
		case 2:
			if exists(target) {
				t.Fatal("the first entry was not unlinked before the second inspection")
			}
			if _, err := writer.WriteString(" plus late work"); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "put back") {
		t.Fatalf("a write after the last inspection must stop retirement, got %v", err)
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil || string(got) != "regenerable plus late work" {
		t.Fatalf("late bytes lost: %q, %v", got, readErr)
	}
	if !exists(filepath.Join(root, ".git")) {
		t.Fatal("stopped retirement discarded the recovery pointer")
	}
}

// A process still holding an unlinked file is seen by the next inspection:
// the file's bytes are put back before retirement stops.
func TestRetirementTreePutsBackAFileHeldAtTheNextInspection(t *testing.T) {
	root := retirementFixture(t)
	tree, err := inspectRetirementTree(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.close()
	target := filepath.Join(root, "node_modules", "pkg", "index.js")
	calls := 0
	err = tree.remove(context.Background(), func() error {
		calls++
		if calls == 2 {
			return errors.New("workspace open by process 42")
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "open by process") {
		t.Fatalf("inspection failure lost: %v", err)
	}
	if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "regenerable" {
		t.Fatalf("held file not put back: %q, %v", got, readErr)
	}
}

// A process that keeps the file open past the inspection that stops
// retirement keeps writing it: the file put back is the same inode, so a
// write after settle lands in the file the tree names.
func TestRetirementTreeKeepsWritesOfAWriterStillOpenAfterPutBack(t *testing.T) {
	root := retirementFixture(t)
	tree, err := inspectRetirementTree(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.close()
	target := filepath.Join(root, "node_modules", "pkg", "index.js")
	var writer *os.File
	calls := 0
	err = tree.remove(context.Background(), func() error {
		calls++
		switch calls {
		case 1:
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			writer = f
		case 2:
			return errors.New("workspace open by process 42")
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "open by process") {
		t.Fatalf("inspection failure lost: %v", err)
	}
	if _, err := writer.WriteString(" plus later work"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "regenerable plus later work" {
		t.Fatalf("write after put-back lost: %q, %v", got, readErr)
	}
	left, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range left {
		if strings.HasPrefix(e.Name(), retirementHoldingPrefix) {
			t.Fatalf("holding name left behind: %s", e.Name())
		}
	}
}

// A name created after the last inspection is not in the inventory: it is
// never unlinked, and the directory holding it is kept with it.
func TestRetirementTreeKeepsAFileCreatedAfterTheLastInspection(t *testing.T) {
	root := retirementFixture(t)
	tree, err := inspectRetirementTree(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.close()
	late := filepath.Join(root, "src", "late.go")
	calls := 0
	err = tree.remove(context.Background(), func() error {
		calls++
		if calls == 1 {
			write(t, late, "late work")
		}
		return nil
	})
	if err == nil {
		t.Fatal("a directory holding new work was removed")
	}
	if got, readErr := os.ReadFile(late); readErr != nil || string(got) != "late work" {
		t.Fatalf("new file lost: %q, %v", got, readErr)
	}
	if !exists(filepath.Join(root, ".git")) {
		t.Fatal("stopped retirement discarded the recovery pointer")
	}
}

// Every node_modules holds symlinks (.bin, pnpm's whole layout) and pnpm
// hard-links its store into it. Refusing either held every worktree that had
// run an install, which were the worktrees that filled the disk. Removing a
// link never touches what it names.
func TestRetirementTreeRemovesLinksWithoutTouchingTheirTargets(t *testing.T) {
	root := retirementFixture(t)
	outside := filepath.Join(t.TempDir(), "only-copy")
	write(t, outside, "external")
	if err := os.Symlink(outside, filepath.Join(root, "node_modules", "pkg", "bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(root, "node_modules", "pkg", "linked.js")); err != nil {
		t.Fatal(err)
	}
	tree, err := inspectRetirementTree(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.close()
	if err := tree.remove(context.Background(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if exists(root) {
		t.Fatal("tree was not removed")
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "external" {
		t.Fatalf("link target touched: %q %v", b, err)
	}
}

// A regenerable subtree is removed whole, the directory itself included,
// and holds like a checkout does on a protected name or a nested repository.
func TestRegenerableTreeRemovesTheDirectoryAndHoldsLikeRetirement(t *testing.T) {
	root := retirementFixture(t)
	modules := filepath.Join(root, "node_modules")
	tree, err := inspectRegenerableTree(context.Background(), modules)
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.remove(context.Background(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	tree.close()
	if exists(modules) || !exists(filepath.Join(root, "src", "main.go")) || !exists(filepath.Join(root, ".git")) {
		t.Fatal("subtree removal reached outside node_modules, or left it")
	}
	for _, rel := range []string{".env", "dep/.git", "dep/data/rows.csv"} {
		t.Run(rel, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "node_modules")
			write(t, filepath.Join(dir, "pkg", "index.js"), "x")
			write(t, filepath.Join(dir, filepath.FromSlash(rel)), "keep")
			tree, err := inspectRegenerableTree(context.Background(), dir)
			if tree != nil {
				tree.close()
			}
			if err == nil {
				t.Fatal("held content authorized")
			}
		})
	}
}

// pnpmStoreDir names a lockfile key's virtual-store directory as pnpm does,
// and refuses keys that are not a package id with a well-formed peer suffix
// or whose directory pnpm hashes.
func TestPnpmStoreDir(t *testing.T) {
	for _, c := range []struct{ key, name, dir string }{
		{"mine@1.0.0", "mine", "mine@1.0.0"},
		{"/mine@1.0.0", "mine", "mine@1.0.0"},
		{"@scope/lib@1.0.0", "@scope/lib", "@scope+lib@1.0.0"},
		{"react-dom@18.2.0(react@18.2.0)", "react-dom", "react-dom@18.2.0_react@18.2.0"},
		{"a@1.0.0(@types/react@18.0.0)(react@18.2.0)", "a", "a@1.0.0_@types+react@18.0.0_react@18.2.0"},
		{"a@1.0.0(b@2.0.0(c@3.0.0))(c@3.0.0)", "a", "a@1.0.0_b@2.0.0_c@3.0.0__c@3.0.0"},
		{"mine@1.0.0_evil", "mine", "mine@1.0.0_evil"},
	} {
		name, dir, ok := pnpmStoreDir(c.key)
		if !ok || name != c.name || dir != c.dir {
			t.Errorf("pnpmStoreDir(%q) = %q, %q, %v; want %q, %q", c.key, name, dir, ok, c.name, c.dir)
		}
	}
	for _, key := range []string{
		"", "mine", "@scope/lib", "mine@", "/mine/1.0.0", "file:../mine",
		"mine@1.0.0(react@18.2.0", "mine@1.0.0(react@18.2.0)x", "mine@1.0.0()", "mine@1.0.0)(",
		"Mine@1.0.0", "mine@1.0.0(" + strings.Repeat("p", 120) + "@1.0.0)",
	} {
		if name, dir, ok := pnpmStoreDir(key); ok {
			t.Errorf("pnpmStoreDir(%q) = %q, %q; want not ok", key, name, dir)
		}
	}
}
