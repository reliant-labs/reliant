package cigate

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// BranchRange is what the gate's secret-scan row scans: the commits a
// branch adds over its merge-base with origin/<default> (or CI_BASE), the last
// commit on the default branch, and the whole history for a first commit or a
// clone that never pushed — never the history a project had before it pinned
// the forge with the row, and never less than the change when the base cannot
// be resolved.
func TestBranchRange(t *testing.T) {
	testkit.Slow(t, "execs git")
	root := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	ctx := context.Background()
	rangeOf := func(base string) (string, string) {
		t.Helper()
		r, why, err := BranchRange(ctx, root, base)
		if err != nil {
			t.Fatalf("BranchRange(%q): %v", base, err)
		}
		return r, why
	}
	run("init", "-q", "-b", "main")
	run("commit", "-q", "--allow-empty", "-m", "first")
	if r, why := rangeOf(""); r != "" || !strings.Contains(why, "first commit") {
		t.Errorf("first commit: %q (%s), want the whole history", r, why)
	}
	run("commit", "-q", "--allow-empty", "-m", "second")
	if r, why := rangeOf(""); r != "" || !strings.Contains(why, "no origin/<default branch>") {
		t.Errorf("no remote: %q (%s), want every commit", r, why)
	}
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	if r, why := rangeOf(""); r != "HEAD^..HEAD" || !strings.Contains(why, "default branch main") {
		t.Errorf("on main: %q (%s), want the last commit", r, why)
	}
	// An explicit base on the default branch (CI_BASE=origin/main after a
	// push): HEAD adds nothing over it, so the last commit.
	if r, why := rangeOf("origin/main"); r != "HEAD^..HEAD" || !strings.Contains(why, "adds no commit") {
		t.Errorf("explicit base equal to HEAD: %q (%s), want the last commit", r, why)
	}
	base := run("rev-parse", "HEAD")
	run("switch", "-q", "-c", "topic")
	run("commit", "-q", "--allow-empty", "-m", "a")
	run("commit", "-q", "--allow-empty", "-m", "b")
	if r, why := rangeOf(""); r != base+"..HEAD" || !strings.Contains(why, "since origin/main") {
		t.Errorf("on a branch: %q (%s), want %s..HEAD", r, why, base)
	}
	if r, _ := rangeOf("HEAD~1"); r != run("rev-parse", "HEAD~1")+"..HEAD" {
		t.Errorf("an explicit base (CI_BASE) must win: %q", r)
	}
	// A merge group / pull request merge commit: detached HEAD, explicit base
	// the target branch. Every commit of the branch is in range, not just the
	// merge commit.
	run("switch", "-q", "--detach", "main")
	run("merge", "-q", "--no-ff", "--no-edit", "topic")
	if r, _ := rangeOf("origin/main"); r != base+"..HEAD" {
		t.Errorf("merge group with CI_BASE=origin/main: %q, want %s..HEAD", r, base)
	}
	// A base that does not resolve (not fetched, a typo) fails closed rather
	// than narrowing to the last commit.
	if r, why, err := BranchRange(ctx, root, "origin/nope"); err == nil {
		t.Errorf("unresolvable base: %q (%s), want an error", r, why)
	} else if !strings.Contains(err.Error(), "merge-base") {
		t.Errorf("unresolvable base: error %v should name the missing merge-base", err)
	}
	// So does a base with no common history (a shallow or unrelated clone).
	run("switch", "-q", "--orphan", "unrelated")
	run("commit", "-q", "--allow-empty", "-m", "x")
	run("commit", "-q", "--allow-empty", "-m", "y")
	if r, why, err := BranchRange(ctx, root, "origin/main"); err == nil {
		t.Errorf("no merge-base: %q (%s), want an error", r, why)
	}
	if r, why, err := BranchRange(ctx, root, ""); err == nil {
		t.Errorf("no merge-base with the implicit base: %q (%s), want an error", r, why)
	}
}
