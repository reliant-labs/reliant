//go:build linux

package worktreereclaim

import (
	"context"
	"path/filepath"
	"testing"
)

func TestTrackedSetIncludesEveryDirectoryOnTheWay(t *testing.T) {
	t.Parallel()
	got := trackedSet([]byte("deploy/secrets/README.md\x00internal/secrets/key.go\x00main.go\x00"))
	for _, p := range []string{"deploy", "deploy/secrets", "deploy/secrets/README.md", "internal", "internal/secrets", "internal/secrets/key.go", "main.go"} {
		if !got[p] {
			t.Errorf("trackedSet is missing %q", p)
		}
	}
	for _, p := range []string{"", ".", "deploy/secrets/dev.yaml", "secrets"} {
		if got[p] {
			t.Errorf("trackedSet names %q, which nothing tracks", p)
		}
	}
	if len(trackedSet(nil)) != 0 {
		t.Error("no output names nothing")
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
