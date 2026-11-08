//go:build linux

package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestTrackedSetIncludesEveryDirectoryOnTheWay(t *testing.T) {
	t.Parallel()
	ls := func(recs ...string) []byte { return []byte(strings.Join(recs, "\x00") + "\x00") }
	got := trackedSet(ls(
		"100644 blob aaaa\tdeploy/secrets/README.md",
		"100755 blob bbbb\tinternal/secrets/key.go",
		"100644 blob cccc\tmain.go",
		"120000 blob dddd\tsecrets",
		"160000 commit eeee\tvendor/creds",
		"120000 blob ffff\tlinks/only/secrets",
	))
	for p, want := range map[string]trackedKind{
		"deploy": trackedDir, "deploy/secrets": trackedDir, "deploy/secrets/README.md": trackedFile,
		"internal": trackedDir, "internal/secrets": trackedDir, "internal/secrets/key.go": trackedFile,
		"main.go": trackedFile,
	} {
		if got[p] != want {
			t.Errorf("trackedSet[%q] = %v, want %v", p, got[p], want)
		}
	}
	// A symlink or submodule is not in the commit's content: never exempt,
	// nor are directories that hold only them.
	for _, p := range []string{"", ".", "deploy/secrets/dev.yaml", "secrets", "vendor", "vendor/creds", "links", "links/only/secrets"} {
		if got[p] != trackedNone {
			t.Errorf("trackedSet names %q (%v), which no regular file makes tracked", p, got[p])
		}
	}
	if len(trackedSet(nil)) != 0 {
		t.Error("no output names nothing")
	}
}

// HEAD tracks a never-listed name as a symlink: the link is not exempt (what
// it points at is not in the commit), so it holds the checkout as an
// untracked one would.
func TestRetirementTreeDoesNotExemptATrackedSymlink(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "builds a git fixture")
	f := newFixture(t)
	if err := os.Symlink("/nonexistent/vault", filepath.Join(f.project, "secrets")); err != nil {
		t.Fatal(err)
	}
	run(t, f.project, "add", ".")
	run(t, f.project, "commit", "-q", "-m", "tracked secrets link")
	run(t, f.project, "push", "-q", "origin", "main")
	w := f.worktree("tracked-link", "feat/tracked-link")
	if fi, err := os.Lstat(filepath.Join(w.Path, "secrets")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("fixture: want the checkout's secrets to be the tracked symlink: %v", err)
	}

	tree, err := inspectRetirementTree(context.Background(), w.Path)
	if tree != nil {
		tree.close()
	}
	if err == nil || !strings.Contains(err.Error(), "protected descendant: secrets") {
		t.Fatalf("a tracked never-listed symlink was exempt: err = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(w.Path, "secrets")); err != nil {
		t.Fatal("inspection deleted the link")
	}
}

// A never-listed name that HEAD tracks (a deploy/secrets/ of manifests, an
// internal/secrets Go package) is source the remote has, not the user's data:
// it no longer holds the checkout. An untracked or ignored file under the same
// directory still does, and a HEAD that moves after inspection fails
// validation, since the exemption was read from the old one.
func TestRetirementTreeExemptsOnlyWhatHeadTracks(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "builds a git fixture")
	f := newFixture(t)
	write(t, filepath.Join(f.project, "deploy", "secrets", "README.md"), "how secrets are bound\n")
	write(t, filepath.Join(f.project, "internal", "secrets", "secrets.go"), "package secrets\n")
	write(t, filepath.Join(f.project, ".gitignore"), "deploy/secrets/*.yaml\n")
	run(t, f.project, "add", ".")
	run(t, f.project, "commit", "-q", "-m", "tracked secrets dirs")
	run(t, f.project, "push", "-q", "origin", "main")
	w := f.worktree("tracked", "feat/tracked")

	tree, err := inspectRetirementTree(context.Background(), w.Path)
	if err != nil {
		t.Fatalf("tracked never-listed paths held the checkout: %v", err)
	}
	if err := tree.validate(context.Background()); err != nil {
		t.Fatalf("validate: %v", err)
	}
	commit(t, w.Path, "later.txt", "moved on\n")
	if err := tree.validate(context.Background()); err == nil {
		t.Fatal("a checkout whose HEAD moved after inspection validated")
	}
	tree.close()

	for _, rel := range []string{"deploy/secrets/dev.yaml", "internal/secrets/local.key"} {
		t.Run(rel, func(t *testing.T) {
			w := f.worktree("held-"+filepath.Base(rel), "feat/held-"+filepath.Base(rel))
			p := filepath.Join(w.Path, filepath.FromSlash(rel))
			write(t, p, "only copy")
			tree, err := inspectRetirementTree(context.Background(), w.Path)
			if tree != nil {
				tree.close()
			}
			if err == nil {
				t.Fatalf("%s is not tracked, yet did not hold the checkout", rel)
			}
			if !exists(p) {
				t.Fatal("inspection deleted data")
			}
		})
	}
}

// End to end: an archived, clean, merged worktree whose project tracks a
// secrets/ directory is retired, which every control-plane and reliant
// checkout used to be refused.
func TestReconcile_TrackedSecretsDirectoryDoesNotHold(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "~40 git processes")
	f := newFixture(t)
	f.idle()
	write(t, filepath.Join(f.project, "deploy", "secrets", "README.md"), "how secrets are bound\n")
	run(t, f.project, "add", ".")
	run(t, f.project, "commit", "-q", "-m", "tracked secrets dir")
	run(t, f.project, "push", "-q", "origin", "main")
	w := f.worktree("w-tracked", "feat/tracked")
	if got := f.one(w); got.Outcome != OutcomeRemoved {
		t.Fatalf("got %+v, want removed", got)
	}
	if exists(w.Path) {
		t.Fatal("directory still exists")
	}
}

// A path HEAD tracks is exempt only as the kind HEAD tracks it as: a file HEAD
// has at `secrets` that is a symlink or a directory on disk is the user's.
func TestTrackedPathRequiresTheKindHeadTracks(t *testing.T) {
	t.Parallel()
	tree := &retirementTree{trackedRead: true, tracked: map[string]trackedKind{"secrets": trackedFile, "deploy/secrets": trackedDir}}
	for _, c := range []struct {
		rel  string
		mode uint32
		want bool
	}{
		{"secrets", unix.S_IFREG, true},
		{"secrets", unix.S_IFLNK, false},
		{"secrets", unix.S_IFDIR, false},
		{"deploy/secrets", unix.S_IFDIR, true},
		{"deploy/secrets", unix.S_IFLNK, false},
		{"deploy/secrets", unix.S_IFREG, false},
		{"other", unix.S_IFREG, false},
	} {
		if got := tree.trackedPath(context.Background(), c.rel, c.mode); got != c.want {
			t.Errorf("trackedPath(%q, %o) = %v, want %v", c.rel, c.mode, got, c.want)
		}
	}
}
