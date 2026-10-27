package worktreereclaim

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Establish Git's boundary explicitly: worktree remove checks porcelain
// cleanliness, NOT the preservation policy for ignored files or live users.
func TestGitWorktreeRemoveContract(t *testing.T) {
	heavyUnderShort(t, "creates temporary git worktrees to verify non-force removal behavior")
	for _, kind := range []string{"clean", "tracked-edit", "untracked", "ignored-data", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			ignore(t, f, "/tmp/\n")
			w := f.worktree("git-remove", "feat/git-remove")
			run(t, f.project, "worktree", "unlock", w.Path)
			external := filepath.Join(t.TempDir(), "only-copy")
			write(t, external, "outside the checkout")
			switch kind {
			case "tracked-edit":
				write(t, filepath.Join(w.Path, "a.txt"), "modified")
			case "untracked":
				write(t, filepath.Join(w.Path, "untracked.txt"), "unique")
			case "ignored-data":
				write(t, filepath.Join(w.Path, "tmp", "local.sqlite"), "unique ignored data")
			case "symlink":
				if err := os.MkdirAll(filepath.Join(w.Path, "tmp"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Dir(external), filepath.Join(w.Path, "tmp", "linked")); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("git", "-C", f.project, "worktree", "remove", w.Path)
			output, err := cmd.CombinedOutput()
			refuse := kind == "tracked-edit" || kind == "untracked"
			if refuse && err == nil {
				t.Fatalf("Git unexpectedly removed %s", kind)
			}
			if !refuse && err != nil {
				t.Fatalf("Git refused %s: %v %s", kind, err, output)
			}
			if !exists(external) {
				t.Fatal("Git followed symlink to external data")
			}
			t.Logf("git remove without --force: kind=%s refused=%v checkoutStillExists=%v", kind, err != nil, exists(w.Path))
		})
	}
}
