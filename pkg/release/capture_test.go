package release

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitInit(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("remote", "add", "origin", "git@github.com:acme/app.git")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-q", "-m", "init")
	return dir
}

func TestCaptureProvenance(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := gitInit(t)
	ctx := context.Background()
	opts := CaptureOptions{ForgeVersion: "v0.1.44", WorktreeKey: "", Host: "h1"}

	clean, err := CaptureProvenance(ctx, dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if clean.Dirty || clean.Branch != "main" || clean.Repo != "github.com/acme/app" || clean.ForgeVersion != "v0.1.44" || clean.Worktree.Label != "main" {
		t.Fatalf("clean capture: %+v", clean)
	}
	headTree, _ := gitOut(ctx, dir, "rev-parse", "HEAD^{tree}")
	if clean.Tree != headTree {
		t.Fatalf("a clean tree must equal HEAD^{tree}: %s vs %s", clean.Tree, headTree)
	}
	if err := clean.Validate(); err != nil {
		t.Fatal(err)
	}

	// Stage something, so we can prove the user's index is untouched.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "add", "a.txt").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("u\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stagedBefore, _ := gitOut(ctx, dir, "diff", "--cached", "--name-only")

	dirty, err := CaptureProvenance(ctx, dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty.Dirty || dirty.Tree == "" || dirty.Tree == headTree {
		t.Fatalf("dirty capture must hash the built content: %+v", dirty)
	}
	again, _ := CaptureProvenance(ctx, dir, opts)
	if again.Tree != dirty.Tree {
		t.Fatal("two captures of one dirty state must agree")
	}
	stagedAfter, _ := gitOut(ctx, dir, "diff", "--cached", "--name-only")
	if stagedAfter != stagedBefore || strings.Contains(stagedAfter, "untracked.txt") {
		t.Fatalf("capture touched the user's index: before %q after %q", stagedBefore, stagedAfter)
	}

	// Not a git checkout: no commit, no error.
	plain, err := CaptureProvenance(ctx, t.TempDir(), opts)
	if err != nil || plain.Commit != "" || plain.Tree != "" {
		t.Fatalf("non-git dir: %+v %v", plain, err)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// A parked worktree or cloned repo inside the project is another checkout's
// content: it must not make this checkout dirty (a 2026-10-12 prod deploy cut
// and built a release for an unchanged tree because of one), but every other
// untracked file still counts.
func TestInspectCheckout_NestedCheckoutsAreNotDirt(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := gitInit(t)
	ctx := context.Background()
	opts := CaptureOptions{ForgeVersion: "v0.1.44"}
	clean, err := CaptureProvenance(ctx, dir, opts)
	if err != nil || clean.Dirty {
		t.Fatalf("baseline: %+v %v", clean, err)
	}

	gitRun(t, dir, "worktree", "add", "-q", "-b", "other", filepath.Join(dir, "linked-worktree"))
	if err := os.MkdirAll(filepath.Join(dir, "cloned"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, filepath.Join(dir, "cloned"), "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "cloned", "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	changes, err := InspectCheckout(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if changes.Dirty() || len(changes.Paths) != 0 {
		t.Fatalf("nested checkouts counted as changes: %+v", changes)
	}
	if got := strings.Join(changes.Nested, " "); got != "cloned/ linked-worktree/" {
		t.Fatalf("nested = %q, want both checkouts listed", got)
	}
	withNested, err := CaptureProvenance(ctx, dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if withNested.Dirty || withNested.Tree != clean.Tree {
		t.Fatalf("a tree with only nested checkouts must equal the clean one: %+v vs %+v", withNested, clean)
	}

	// A real untracked file still counts, nested checkouts or not, and the
	// hashed tree contains it but neither checkout.
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("r"), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, _ = InspectCheckout(ctx, dir)
	if !changes.Dirty() || strings.Join(changes.Paths, " ") != "real.txt" {
		t.Fatalf("paths = %v, want [real.txt]", changes.Paths)
	}
	dirty, err := CaptureProvenance(ctx, dir, opts)
	if err != nil || !dirty.Dirty || dirty.Tree == clean.Tree {
		t.Fatalf("dirty capture: %+v %v", dirty, err)
	}
	listing, _ := gitOut(ctx, dir, "ls-tree", "-r", "--name-only", dirty.Tree)
	if strings.Contains(listing, "cloned") || strings.Contains(listing, "linked-worktree") || !strings.Contains(listing, "real.txt") {
		t.Fatalf("hashed tree = %q: want real.txt and no nested checkout", listing)
	}
}

// A rename is two porcelain entries in -z mode; the origin must not be read
// as a path of its own.
func TestInspectCheckout_ParsesRenamesAndLeadingSpace(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories; runs in task test")
	}
	dir := gitInit(t)
	ctx := context.Background()
	gitRun(t, dir, "mv", "a.txt", "b b.txt")
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, err := InspectCheckout(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(changes.Paths, "|"); got != "b b.txt|c.txt" {
		t.Fatalf("paths = %q", got)
	}
}

// The commit time is the committer date of HEAD in UTC, recorded for a clean
// checkout only: a dirty tree's bytes are no commit's, so the time of HEAD
// would be a claim about content it does not describe.
func TestCaptureProvenance_CommitTime(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories; runs in task test")
	}
	dir := gitInit(t)
	ctx := context.Background()
	opts := CaptureOptions{ForgeVersion: "v0.1.44"}

	// A committer date in a non-UTC zone, distinct from the author date.
	cmd := exec.Command("git", "-C", dir, "-c", "user.name=t", "-c", "user.email=t@t",
		"commit", "-q", "--amend", "--no-edit", "--date", "2026-01-02T03:04:05+00:00")
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE=2026-03-04T10:30:00+05:30")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("amend: %v\n%s", err, out)
	}
	want := time.Date(2026, 3, 4, 5, 0, 0, 0, time.UTC)

	clean, err := CaptureProvenance(ctx, dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if clean.CommitTime == nil || !clean.CommitTime.Equal(want) || clean.CommitTime.Location() != time.UTC {
		t.Fatalf("clean commit time = %v, want %v (committer date, UTC)", clean.CommitTime, want)
	}
	if err := clean.Validate(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err := CaptureProvenance(ctx, dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty.Dirty || dirty.CommitTime != nil {
		t.Fatalf("a dirty capture must leave the commit time unset: %+v", dirty)
	}

	plain, err := CaptureProvenance(ctx, t.TempDir(), opts)
	if err != nil || plain.CommitTime != nil {
		t.Fatalf("non-git dir: %+v %v", plain, err)
	}
}

// D4: a release names its commit's subject and author, dirty or not — a dirty
// build's commit is still the base it was built on.
func TestCaptureProvenanceRecordsCommitSubjectAndAuthor(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories; runs in task test")
	}
	dir, shas := gitHistory(t, 2)
	ctx := context.Background()
	clean, err := CaptureProvenance(ctx, dir, CaptureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if clean.Commit != shas[1] || clean.CommitSubject != "c2" || clean.CommitAuthor != "bob" {
		t.Fatalf("clean capture: commit %s subject %q author %q", clean.Commit, clean.CommitSubject, clean.CommitAuthor)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err := CaptureProvenance(ctx, dir, CaptureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !dirty.Dirty || dirty.CommitSubject != "c2" || dirty.CommitAuthor != "bob" || dirty.CommitTime != nil {
		t.Fatalf("dirty capture: dirty %v subject %q author %q commit time %v", dirty.Dirty, dirty.CommitSubject, dirty.CommitAuthor, dirty.CommitTime)
	}
	if err := dirty.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestProvenanceValidateCommitText(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name string
		p    Provenance
		ok   bool
	}{
		{"subject and author", Provenance{Commit: commit, CommitSubject: "fix: x", CommitAuthor: "alice"}, true},
		{"subject without commit", Provenance{CommitSubject: "fix: x"}, false},
		{"author without commit", Provenance{CommitAuthor: "alice"}, false},
		{"two-line subject", Provenance{Commit: commit, CommitSubject: "fix\nmore"}, false},
		{"over-long author", Provenance{Commit: commit, CommitAuthor: strings.Repeat("a", MaxCommitAuthorBytes+1)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.p.Validate(); (err == nil) != tc.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}
