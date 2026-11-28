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

// inspectWithSource is inspectRetirementTree with the source checkout given
// rather than asked of git, and nothing tracked, so the rule runs without a
// repository.
func inspectWithSource(t *testing.T, root, source string) (*retirementTree, error) {
	t.Helper()
	tree, err := retirementRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	tree.trackedRead, tree.sourceRead, tree.source = true, true, source
	tree.entries, tree.bytes, tree.newest, err = tree.inventory(context.Background())
	if err != nil {
		tree.close()
		return nil, err
	}
	return tree, nil
}

// sourceFixture is a checkout (retirementFixture) and a source checkout
// beside it holding the dev secrets and .env a worktree is given.
func sourceFixture(t *testing.T) (root, source string) {
	t.Helper()
	root = retirementFixture(t)
	source = t.TempDir()
	write(t, filepath.Join(source, "secrets", "dev.yaml"), "token: abc\nurl: https://dev\n")
	write(t, filepath.Join(source, ".env"), "KEY=1\n")
	return root, source
}

// A never-listed path the source checkout holds a copy of loses nothing when
// the checkout goes, so the inventory accepts it, as the classifier does
// (sourceCovers): a byte-identical file, or a secrets/dev.yaml whose every
// value the source's holds. A never-listed directory is accepted only when
// everything in it is. Anything else still holds the tree.
func TestRetirementTreeAcceptsWhatTheSourceCovers(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, root, source string)
		// held is the protected descendant named, "" for accepted.
		held string
	}{
		{"identical copies", func(t *testing.T, root, _ string) {
			write(t, filepath.Join(root, "secrets", "dev.yaml"), "token: abc\nurl: https://dev\n")
			write(t, filepath.Join(root, ".env"), "KEY=1\n")
		}, ""},
		{"dev secrets the source's values cover", func(t *testing.T, root, _ string) {
			write(t, filepath.Join(root, "secrets", "dev.yaml"), "# local\nurl: https://dev\ntoken: abc\n")
		}, ""},
		{"dev secrets with a value the source lacks", func(t *testing.T, root, _ string) {
			write(t, filepath.Join(root, "secrets", "dev.yaml"), "token: old\n")
		}, "secrets/dev.yaml"},
		{".env unlike the source's", func(t *testing.T, root, _ string) {
			write(t, filepath.Join(root, ".env"), "KEY=2\n")
		}, ".env"},
		{"a file only here beside a copy", func(t *testing.T, root, _ string) {
			write(t, filepath.Join(root, "secrets", "dev.yaml"), "token: abc\nurl: https://dev\n")
			write(t, filepath.Join(root, "secrets", "prod.yaml"), "token: prod\n")
		}, "secrets/prod.yaml"},
		{"an empty never-listed directory", func(t *testing.T, root, _ string) {
			if err := os.MkdirAll(filepath.Join(root, "secrets"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "secrets"},
		{"a symlink in a never-listed directory", func(t *testing.T, root, source string) {
			write(t, filepath.Join(root, "secrets", "dev.yaml"), "token: abc\nurl: https://dev\n")
			if err := os.Symlink(filepath.Join(source, ".env"), filepath.Join(root, "secrets", "link")); err != nil {
				t.Fatal(err)
			}
		}, "secrets/link"},
		{"a source path that is this very file", func(t *testing.T, root, source string) {
			write(t, filepath.Join(root, "data", "only.csv"), "only copy\n")
			if err := os.Symlink(filepath.Join(root, "data"), filepath.Join(source, "data")); err != nil {
				t.Fatal(err)
			}
		}, "data/only.csv"},
		// A source path through a symlinked directory is not a copy, even
		// when it reaches an identical file (review B1, PR #3646).
		{"source secrets linking out of the source", func(t *testing.T, root, source string) {
			const secret = "token: abc\nurl: https://dev\n"
			write(t, filepath.Join(root, "secrets", "dev.yaml"), secret)
			outside := t.TempDir()
			write(t, filepath.Join(outside, "dev.yaml"), secret)
			if err := os.RemoveAll(filepath.Join(source, "secrets")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(source, "secrets")); err != nil {
				t.Fatal(err)
			}
		}, "secrets/dev.yaml"},
		{"source secrets linking into the checkout", func(t *testing.T, root, source string) {
			const secret = "token: abc\nurl: https://dev\n"
			write(t, filepath.Join(root, "secrets", "dev.yaml"), secret)
			write(t, filepath.Join(root, "stash", "dev.yaml"), secret)
			if err := os.RemoveAll(filepath.Join(source, "secrets")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, "stash"), filepath.Join(source, "secrets")); err != nil {
				t.Fatal(err)
			}
		}, "secrets/dev.yaml"},
		{"source secrets linking elsewhere in the source", func(t *testing.T, root, source string) {
			const secret = "token: abc\nurl: https://dev\n"
			write(t, filepath.Join(root, "secrets", "dev.yaml"), secret)
			write(t, filepath.Join(source, "vault", "dev.yaml"), secret)
			if err := os.RemoveAll(filepath.Join(source, "secrets")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(source, "vault"), filepath.Join(source, "secrets")); err != nil {
				t.Fatal(err)
			}
		}, "secrets/dev.yaml"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, source := sourceFixture(t)
			c.setup(t, root, source)
			tree, err := inspectWithSource(t, root, source)
			if tree != nil {
				defer tree.close()
			}
			switch {
			case c.held == "" && err != nil:
				t.Fatalf("a covered copy held the tree: %v", err)
			case c.held != "" && (err == nil || err.Error() != "protected descendant: "+c.held):
				t.Fatalf("err = %v, want protected descendant: %s", err, c.held)
			}
		})
	}
}

// With no source checkout nothing is a copy: the never-listed directory holds
// at once, under the name it always has.
func TestRetirementTreeWithoutSourceHoldsNeverListed(t *testing.T) {
	root, _ := sourceFixture(t)
	write(t, filepath.Join(root, "secrets", "dev.yaml"), "token: abc\nurl: https://dev\n")
	tree, err := inspectWithSource(t, root, "")
	if tree != nil {
		tree.close()
	}
	if err == nil || err.Error() != "protected descendant: secrets" {
		t.Fatalf("err = %v, want protected descendant: secrets", err)
	}
}

// Coverage is judged again when the tree is validated for deletion: a source
// copy that changed since the inventory, or a worktree copy that did, stops
// it.
func TestRetirementTreeValidateRejudgesSourceCopies(t *testing.T) {
	for name, change := range map[string]func(root, source string) string{
		"source changed":   func(_, source string) string { return filepath.Join(source, ".env") },
		"worktree changed": func(root, _ string) string { return filepath.Join(root, ".env") },
	} {
		t.Run(name, func(t *testing.T) {
			root, source := sourceFixture(t)
			write(t, filepath.Join(root, ".env"), "KEY=1\n")
			tree, err := inspectWithSource(t, root, source)
			if err != nil {
				t.Fatalf("inventory: %v", err)
			}
			defer tree.close()
			if err := tree.validate(context.Background()); err != nil {
				t.Fatalf("validate before the change: %v", err)
			}
			write(t, change(root, source), "KEY=2\n")
			if err := tree.validate(context.Background()); err == nil {
				t.Fatal("validate accepted a copy that changed after the inventory")
			}
		})
	}
}

// remove judges a source-covered file again as it is about to unlink it: a
// source copy that changes after validate leaves the worktree's copy in place.
// Unchanged, the whole checkout goes and the source's copies stay.
func TestRetirementTreeRemoveRejudgesSourceCopiesAtTheDeletionBoundary(t *testing.T) {
	t.Run("source changed after validate", func(t *testing.T) {
		root, source := sourceFixture(t)
		p := filepath.Join(root, "secrets", "dev.yaml")
		write(t, p, "token: abc\nurl: https://dev\n")
		tree, err := inspectWithSource(t, root, source)
		if err != nil {
			t.Fatalf("inventory: %v", err)
		}
		defer tree.close()
		changed := false
		err = tree.remove(context.Background(), func() error {
			if !changed {
				changed = true
				write(t, filepath.Join(source, "secrets", "dev.yaml"), "token: rotated\n")
			}
			return nil
		})
		if err == nil || !strings.Contains(err.Error(), "source copy changed at deletion boundary") {
			t.Fatalf("remove = %v, want the deletion-boundary hold", err)
		}
		if b, err := os.ReadFile(p); err != nil || string(b) != "token: abc\nurl: https://dev\n" {
			t.Fatalf("the worktree's copy was lost: %q, %v", b, err)
		}
	})
	t.Run("unchanged", func(t *testing.T) {
		root, source := sourceFixture(t)
		write(t, filepath.Join(root, "secrets", "dev.yaml"), "token: abc\nurl: https://dev\n")
		write(t, filepath.Join(root, ".env"), "KEY=1\n")
		tree, err := inspectWithSource(t, root, source)
		if err != nil {
			t.Fatalf("inventory: %v", err)
		}
		defer tree.close()
		if err := tree.remove(context.Background(), func() error { return nil }); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("checkout still there: %v", err)
		}
		for _, rel := range []string{"secrets/dev.yaml", ".env"} {
			if !exists(filepath.Join(source, rel)) {
				t.Fatalf("source lost %s", rel)
			}
		}
	})
}
