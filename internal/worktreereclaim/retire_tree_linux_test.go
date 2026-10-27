//go:build linux

package worktreereclaim

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
