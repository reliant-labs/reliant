package opindex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// diffRepo is a scratch git repository for the Changes tests.
type diffRepo struct {
	t   *testing.T
	dir string
}

func newDiffRepo(t *testing.T) *diffRepo {
	t.Helper()
	r := &diffRepo{t: t, dir: t.TempDir()}
	r.git("init", "-q")
	return r
}

func (r *diffRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *diffRepo) write(files map[string]string) {
	r.t.Helper()
	writeTree(r.t, r.dir, files)
}

func (r *diffRepo) commit(subject string) string {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "-m", subject)
	return r.git("rev-parse", "HEAD")
}

func (r *diffRepo) changes(dir, base, head string) []FileChange {
	r.t.Helper()
	got, err := Changes(context.Background(), dir, base, head)
	if err != nil {
		r.t.Fatal(err)
	}
	return got
}

// With a head, Changes compares the two commits' trees: whatever the
// checkout holds — staged, unstaged or untracked — is not read.
func TestChangesCommittedHead(t *testing.T) {
	testkit.Slow(t, "execs git")
	r := newDiffRepo(t)
	r.write(map[string]string{"a.txt": "1\n2\n3\n", "b.txt": "1\n2\n3\n", "c.txt": "1\n"})
	base := r.commit("base")
	r.write(map[string]string{"a.txt": "1\nTWO\n3\n"})
	head := r.commit("head")

	r.write(map[string]string{"b.txt": "1\n2\nTHREE\n"}) // unstaged
	r.write(map[string]string{"c.txt": "ONE\n"})
	r.git("add", "c.txt")                        // staged
	r.write(map[string]string{"u.txt": "new\n"}) // untracked

	want := []FileChange{{Path: "a.txt", Status: "M", NewRanges: []LineRange{{2, 1}}, OldRanges: []LineRange{{2, 1}}}}
	if got := r.changes(r.dir, base, head); !reflect.DeepEqual(got, want) {
		t.Errorf("Changes(base, head) = %+v, want only the committed change %+v", got, want)
	}
}

// Without a head, Changes is base against the working tree: a staged-only
// change, an unstaged one, a staged new file and every untracked file that
// is not ignored — empty and binary ones included, with no ranges — and only
// those under dir, with paths relative to it.
func TestChangesDirtyWorktree(t *testing.T) {
	testkit.Slow(t, "execs git")
	r := newDiffRepo(t)
	r.write(map[string]string{
		".gitignore":    "*.log\n",
		"app/a.txt":     "1\n2\n3\n",
		"app/b.txt":     "1\n2\n3\n",
		"outside.txt":   "1\n",
		"app/gone.txt":  "1\n2\n",
		"app/keep.txt":  "1\n",
		"app/sub/x.txt": "1\n",
	})
	base := r.commit("base")
	app := filepath.Join(r.dir, "app")

	r.write(map[string]string{"app/a.txt": "1\nTWO\n3\n"})
	r.git("add", "app/a.txt") // staged only: the working tree agrees with the index
	r.write(map[string]string{"app/b.txt": "1\n2\n3\n4\n"})
	r.write(map[string]string{"app/g.txt": "staged new\n"})
	r.git("add", "app/g.txt")
	if err := os.Remove(filepath.Join(app, "gone.txt")); err != nil { // unstaged deletion
		t.Fatal(err)
	}
	r.write(map[string]string{
		"app/sub/u.go":    "package sub\n\nfunc U() {}", // no trailing newline: still three lines
		"app/scratch.log": "ignored\n",
		"outside-new.txt": "not under dir\n",
		"app/empty.txt":   "",
	})
	if err := os.WriteFile(filepath.Join(app, "blob.bin"), []byte{'P', 'K', 0, 1, '\n'}, 0o644); err != nil {
		t.Fatal(err)
	}

	want := []FileChange{
		{Path: "a.txt", Status: "M", NewRanges: []LineRange{{2, 1}}, OldRanges: []LineRange{{2, 1}}},
		{Path: "b.txt", Status: "M", NewRanges: []LineRange{{4, 1}}, OldRanges: []LineRange{{3, 0}}},
		{Path: "g.txt", Status: "A", NewRanges: []LineRange{{1, 1}}, OldRanges: []LineRange{{0, 0}}},
		{Path: "gone.txt", Status: "D", NewRanges: []LineRange{{0, 0}}, OldRanges: []LineRange{{1, 2}}},
		// Untracked, in ls-files order.
		{Path: "blob.bin", Status: "A"},
		{Path: "empty.txt", Status: "A"},
		{Path: "sub/u.go", Status: "A", NewRanges: []LineRange{{1, 3}}},
	}
	if got := r.changes(app, base, ""); !reflect.DeepEqual(got, want) {
		t.Errorf("Changes(base, working tree):\n got %+v\nwant %+v", got, want)
	}
}

// A change staged and then undone on disk is no change: the working tree,
// not the index, is the head side.
func TestChangesWorktreeWinsOverIndex(t *testing.T) {
	testkit.Slow(t, "execs git")
	r := newDiffRepo(t)
	r.write(map[string]string{"a.txt": "1\n2\n3\n"})
	base := r.commit("base")
	r.write(map[string]string{"a.txt": "1\nTWO\n3\n"})
	r.git("add", "a.txt")
	r.write(map[string]string{"a.txt": "1\n2\n3\n"})
	if staged := r.git("diff", "--cached", "--name-only"); staged != "a.txt" {
		t.Fatalf("setup: the index should hold the edit, staged = %q", staged)
	}

	if got := r.changes(r.dir, base, ""); len(got) != 0 {
		t.Errorf("Changes(base, working tree) = %+v, want nothing: the working tree matches base", got)
	}
}
