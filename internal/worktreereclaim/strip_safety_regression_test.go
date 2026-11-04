//go:build linux && cleanup_acceptance

package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Original red proofs are retained at b6767a9a. The approved whole-worktree
// primitive must preserve the same protected/redirected data, not just stop
// calling the old function. Every path below is temporary fixture data.
func TestStripSafetyProtectedDescendants(t *testing.T) {
	for _, rel := range []string{"tmp/local.sqlite", "tmp/.env", "tmp/data/only-copy.csv", "tmp/secrets/key"} {
		t.Run(rel, func(t *testing.T) {
			root := retirementFixture(t)
			p := filepath.Join(root, rel)
			write(t, p, "unique")
			tree, err := inspectRetirementTree(context.Background(), root)
			if tree != nil {
				tree.close()
			}
			if err == nil {
				t.Fatal("protected descendant authorized")
			}
			if !exists(p) {
				t.Fatal("protected data lost")
			}
		})
	}
}
func TestStripSafetyAncestorReplacement(t *testing.T) {
	root := retirementFixture(t)
	tree, err := inspectRetirementTree(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.close()
	parent := filepath.Join(root, "node_modules")
	external := t.TempDir()
	protected := filepath.Join(external, "pkg", "index.js")
	write(t, protected, "external data")
	swapped := false
	err = tree.remove(context.Background(), func() error {
		if !swapped {
			swapped = true
			if err := os.Rename(parent, parent+"-inspected"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(external, parent); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	})
	if !swapped || err == nil {
		t.Fatal("replacement was not held")
	}
	if !exists(protected) {
		t.Fatal("external tree deleted")
	}
}
func TestStripSafetyNewProtectedFileAtDeletionBoundary(t *testing.T) {
	root := retirementFixture(t)
	tree, err := inspectRetirementTree(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.close()
	p := filepath.Join(root, "node_modules", "pkg", ".env")
	written := false
	err = tree.remove(context.Background(), func() error {
		if !written {
			written = true
			write(t, p, "new protected data")
		}
		return nil
	})
	if err == nil {
		t.Fatal("unexpected new contents must hold partial retirement")
	}
	if !exists(p) || !exists(filepath.Join(root, ".git")) {
		t.Fatal("protected data or recovery pointer lost")
	}
}
