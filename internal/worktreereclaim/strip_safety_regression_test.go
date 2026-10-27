//go:build cleanup_acceptance

package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// These are desired safety invariants, intentionally red on the held cleanup
// candidates. All mutations and attempted deletion are within t.TempDir().
func TestStripSafetyProtectedDescendants(t *testing.T) {
	heavyUnderShort(t, "creates temporary git worktree for deletion safety regression")
	for _, rel := range []string{"tmp/local.sqlite", "tmp/.env", "tmp/data/only-copy.csv", "tmp/secrets/key"} {
		t.Run(rel, func(t *testing.T) {
			f := newFixture(t)
			ignore(t, f, "/tmp/\n")
			w := f.worktree("protected", "feat/protected")
			protected := filepath.Join(w.Path, filepath.FromSlash(rel))
			write(t, protected, "irreplaceable fixture data")
			res := Result{}
			f.r.strip(context.Background(), w, nil, []string{w.Path}, &res, func() error { return nil })
			if !exists(protected) {
				t.Fatalf("protected descendant %s was deleted through allowed ancestor: %+v", rel, res)
			}
		})
	}
}

func TestStripSafetyAncestorReplacement(t *testing.T) {
	heavyUnderShort(t, "creates temporary git worktree and deterministically swaps ancestor")
	if os.PathSeparator == '\\' {
		t.Skip("requires symlinks")
	}
	f := newFixture(t)
	ignore(t, f, "node_modules/\n")
	w := f.worktree("ancestor", "feat/ancestor")
	parent := filepath.Join(w.Path, "web")
	write(t, filepath.Join(parent, "node_modules", "generated"), "rebuildable")
	external := t.TempDir()
	protected := filepath.Join(external, "node_modules", "only-copy")
	write(t, protected, "not part of the inspected tree")
	swapped := false
	res := Result{}
	f.r.strip(context.Background(), w, nil, []string{w.Path}, &res, func() error {
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
	if !swapped {
		t.Fatal("test did not reach destructive boundary")
	}
	if !exists(protected) {
		t.Fatalf("ancestor replacement deleted an uninspected external tree: %+v", res)
	}
}

func TestStripSafetyNewProtectedFileAtDeletionBoundary(t *testing.T) {
	heavyUnderShort(t, "creates temporary git worktree and writes protected data at deletion boundary")
	f := newFixture(t)
	ignore(t, f, "/tmp/\n")
	w := f.worktree("new-data", "feat/new-data")
	write(t, filepath.Join(w.Path, "tmp", "generated"), "rebuildable")
	protected := filepath.Join(w.Path, "tmp", ".env")
	res := Result{}
	f.r.strip(context.Background(), w, nil, []string{w.Path}, &res, func() error {
		write(t, protected, "new external writer's data")
		return nil
	})
	if !exists(protected) {
		t.Fatalf("new protected data deleted after last inspection: %+v", res)
	}
}
