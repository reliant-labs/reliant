// Copyright (c) 2025 Reliant Labs
package gitport_test

import (
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/mergequeue/fakegithub"
	"github.com/reliant-labs/reliant/internal/mergequeue/gitport"
)

// fixture is a remote (a fake GitHub repository) and a local bare clone of it.
type fixture struct {
	remote *fakegithub.Repo
	local  *gitport.Repo
}

// newFixture seeds the remote with main (a.txt, b.txt) and the branches
// given, each a set of changes off main, then clones it.
func newFixture(t *testing.T, branches map[string]map[string]string, mainNext map[string]string) fixture {
	t.Helper()
	remote := fakegithub.New(t).NewRepo("acme/widgets")
	remote.Commit("main", "base", map[string]string{"a.txt": "a\n", "b.txt": "b\n"})
	for name, files := range branches {
		remote.Branch(name, "main")
		remote.Commit(name, "change on "+name, files)
	}
	if mainNext != nil {
		remote.Commit("main", "main moves", mainNext)
	}
	local, err := gitport.Clone(t.Context(), remote.URL(), filepath.Join(t.TempDir(), "work.git"), gitport.Options{})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	return fixture{remote: remote, local: local}
}

// git runs a read-only git command against the local clone.
func (f fixture) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"--git-dir=" + f.local.Dir()}, args...)...)
	cmd.Env = gitport.HermeticEnv(gitport.Identity{})
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func (f fixture) resolve(t *testing.T, rev string) string {
	t.Helper()
	sha, err := f.local.ResolveCommit(t.Context(), rev)
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

func TestSquashWritesOneCommitWithTrailers(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]map[string]string{"pr": {"a.txt": "a2\n", "c.txt": "c\n"}}, map[string]string{"b.txt": "b2\n"})
	ctx := t.Context()
	base := f.resolve(t, "origin/main")
	head := f.resolve(t, "origin/pr")

	res, err := f.local.Squash(ctx, gitport.SquashInput{
		Base: base, Head: head, Title: "feat: change a (#3)", Body: "Why it changed.",
		Trailer: []gitport.Trailer{gitport.PRTrailer("acme/widgets", 3, head), gitport.BatchTrailer("42")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(f.git(t, "rev-parse", res.Commit+"^@")); got != base {
		t.Fatalf("parents = %q, want only the base %s", got, base)
	}
	tree, err := f.local.TreeOf(ctx, res.Commit)
	if err != nil || tree != res.Tree {
		t.Fatalf("TreeOf = %s, %v; want %s", tree, err, res.Tree)
	}
	for path, want := range map[string]string{"a.txt": "a2\n", "b.txt": "b2\n", "c.txt": "c\n"} {
		if got := f.git(t, "show", res.Commit+":"+path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	msg := f.git(t, "log", "-1", "--format=%B", res.Commit)
	want := "feat: change a (#3)\n\nWhy it changed.\n\nReliant-PR: acme/widgets#3 head=" + head + "\nReliant-Batch: 42\n"
	if strings.TrimRight(msg, "\n") != strings.TrimRight(want, "\n") {
		t.Fatalf("message = %q, want %q", msg, want)
	}
	// git itself reads the block as trailers.
	if got := f.git(t, "log", "-1", "--format=%(trailers:key=Reliant-PR,valueonly)", res.Commit); strings.TrimSpace(got) != "acme/widgets#3 head="+head {
		t.Fatalf("git's trailer parse = %q", got)
	}
	tr := gitport.ParseTrailers(msg)
	if len(tr) != 2 || tr[0].Key != gitport.TrailerPR || tr[1] != gitport.BatchTrailer("42") {
		t.Fatalf("ParseTrailers = %+v", tr)
	}
	ref, err := gitport.ParsePRTrailer(tr[0].Value)
	if err != nil || ref != (gitport.PRRef{Repo: "acme/widgets", Number: 3, Head: head}) {
		t.Fatalf("ParsePRTrailer = %+v, %v", ref, err)
	}
}

func TestSquashSequenceChainsTrees(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]map[string]string{
		"one": {"a.txt": "a1\n"},
		"two": {"two.txt": "2\n"},
	}, nil)
	ctx := t.Context()
	t1, err := f.local.Squash(ctx, gitport.SquashInput{Base: "origin/main", Head: "origin/one", Title: "one"})
	if err != nil {
		t.Fatal(err)
	}
	t2, err := f.local.Squash(ctx, gitport.SquashInput{Base: t1.Commit, Head: "origin/two", Title: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if t1.Tree == t2.Tree {
		t.Fatal("T_2 == T_1")
	}
	if got := f.git(t, "show", t2.Commit+":a.txt") + f.git(t, "show", t2.Commit+":two.txt"); got != "a1\n2\n" {
		t.Fatalf("T_2 does not hold both changes: %q", got)
	}
	if got := strings.TrimSpace(f.git(t, "rev-parse", t2.Commit+"^")); got != t1.Commit {
		t.Fatalf("T_2's parent = %s, want T_1 %s", got, t1.Commit)
	}
}

func TestSquashConflictListsPaths(t *testing.T) {
	t.Parallel()
	f := newFixture(t,
		map[string]map[string]string{"pr": {"b.txt": "pr\n", "a.txt": "pr\n", "clean.txt": "c\n"}},
		map[string]string{"a.txt": "main\n", "b.txt": "main\n"},
	)
	_, err := f.local.Squash(t.Context(), gitport.SquashInput{Base: "origin/main", Head: "origin/pr", Title: "x"})
	var ce *gitport.ConflictError
	if !errors.As(err, &ce) || !errors.Is(err, gitport.ErrConflict) {
		t.Fatalf("err = %v, want a conflict", err)
	}
	if ce.Op != "squash" || !slices.Equal(ce.Paths, []string{"a.txt", "b.txt"}) || ce.Tree == "" {
		t.Fatalf("conflict = %+v", ce)
	}
}

func TestSquashRejectsBadInput(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]map[string]string{"pr": {"a.txt": "a2\n"}}, nil)
	ctx := t.Context()
	// Each refusal must name its own cause, so none passes on an unrelated error.
	if _, err := f.local.Squash(ctx, gitport.SquashInput{Base: "origin/main", Head: "origin/pr"}); err == nil || !strings.Contains(err.Error(), "title") {
		t.Fatalf("a squash without a title = %v, want a title refusal", err)
	}
	if _, err := f.local.Squash(ctx, gitport.SquashInput{Base: "origin/main", Head: "origin/nope", Title: "x"}); err == nil || !strings.Contains(err.Error(), `"origin/nope" is not a commit`) {
		t.Fatalf("a squash of a missing head = %v, want origin/nope refused", err)
	}
	if _, err := f.local.Squash(ctx, gitport.SquashInput{Base: "origin/main", Head: "origin/pr", Title: "x",
		Trailer: []gitport.Trailer{{Key: "Bad Key", Value: "v"}}}); err == nil || !strings.Contains(err.Error(), `trailer key "Bad Key"`) {
		t.Fatalf("a trailer key with a space = %v, want it refused", err)
	}
}

func TestRevertIsExact(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]map[string]string{"pr": {"a.txt": "a2\n", "new.txt": "n\n"}}, nil)
	ctx := t.Context()
	m := f.resolve(t, "origin/main")
	landed, err := f.local.Squash(ctx, gitport.SquashInput{Base: m, Head: "origin/pr", Title: "feat: a"})
	if err != nil {
		t.Fatal(err)
	}
	mTree, _ := f.local.TreeOf(ctx, m)

	// Reverting the tip restores exactly the tree before it.
	rv, err := f.local.Revert(ctx, gitport.RevertInput{Commit: landed.Commit, Trailer: []gitport.Trailer{gitport.BatchTrailer("r1")}})
	if err != nil {
		t.Fatal(err)
	}
	if rv.Tree != mTree {
		t.Fatalf("revert tree = %s, want M's %s", rv.Tree, mTree)
	}
	if got := strings.TrimSpace(f.git(t, "rev-parse", rv.Commit+"^")); got != landed.Commit {
		t.Fatalf("revert parent = %s", got)
	}
	msg := f.git(t, "log", "-1", "--format=%B", rv.Commit)
	if !strings.HasPrefix(msg, "Revert \"feat: a\"\n\nThis reverts commit "+landed.Commit+".\n\nReliant-Batch: r1") {
		t.Fatalf("revert message = %q", msg)
	}

}

func TestRevertOntoLaterWork(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]map[string]string{
		"pr":    {"a.txt": "a2\n"},
		"other": {"b.txt": "b2\n"},
	}, nil)
	ctx := t.Context()
	s1, err := f.local.Squash(ctx, gitport.SquashInput{Base: "origin/main", Head: "origin/pr", Title: "pr"})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := f.local.Squash(ctx, gitport.SquashInput{Base: s1.Commit, Head: "origin/other", Title: "other"})
	if err != nil {
		t.Fatal(err)
	}
	rv, err := f.local.Revert(ctx, gitport.RevertInput{Commit: s1.Commit, Onto: s2.Commit})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(f.git(t, "rev-parse", rv.Commit+"^@")); got != s2.Commit {
		t.Fatalf("revert parents = %q, want only s2 %s", got, s2.Commit)
	}
	// s1 undone and s2 kept is exactly main's base plus b2: origin/other's tree.
	want, err := f.local.TreeOf(ctx, "origin/other")
	if err != nil {
		t.Fatal(err)
	}
	if rv.Tree != want {
		t.Fatalf("revert of s1 onto s2: tree %s, want origin/other's %s (a.txt %q, b.txt %q)",
			rv.Tree, want, f.git(t, "show", rv.Commit+":a.txt"), f.git(t, "show", rv.Commit+":b.txt"))
	}
}

func TestRevertConflictsWithLaterWorkOnTheSameLine(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]map[string]string{"pr": {"a.txt": "a2\n"}}, nil)
	ctx := t.Context()
	s1, err := f.local.Squash(ctx, gitport.SquashInput{Base: "origin/main", Head: "origin/pr", Title: "pr"})
	if err != nil {
		t.Fatal(err)
	}

	// Later work directly on top of s1 changes a.txt's line again: land s1 on
	// the remote, commit there, and fetch it back. The setup must succeed, so
	// the conflict asserted below can only come from Revert.
	if err := f.local.Push(ctx, f.remote.URL(), gitport.PushInput{SHA: s1.Commit, Ref: "refs/heads/landed", ExpectedOld: strings.Repeat("0", 40)}); err != nil {
		t.Fatalf("push s1: %v", err)
	}
	later := f.remote.Commit("landed", "later", map[string]string{"a.txt": "a3\n"})
	if err := f.local.Fetch(ctx, f.remote.URL(), "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := strings.TrimSpace(f.git(t, "rev-parse", later+"^@")); got != s1.Commit {
		t.Fatalf("later's parents = %q, want only s1 %s", got, s1.Commit)
	}
	if got := f.git(t, "show", later+":a.txt"); got != "a3\n" {
		t.Fatalf("later's a.txt = %q, want a3", got)
	}

	rv, err := f.local.Revert(ctx, gitport.RevertInput{Commit: s1.Commit, Onto: later})
	var ce *gitport.ConflictError
	if !errors.As(err, &ce) || !errors.Is(err, gitport.ErrConflict) {
		t.Fatalf("Revert = %+v, %v; want a conflict", rv, err)
	}
	if ce.Op != "revert" || !slices.Equal(ce.Paths, []string{"a.txt"}) || ce.Tree == "" {
		t.Fatalf("conflict = %+v, want op revert on a.txt with a tree", ce)
	}
	if rv != (gitport.Result{}) {
		t.Fatalf("a conflicting revert returned a commit: %+v", rv)
	}
}

func TestRevertRefusesAMergeCommit(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]map[string]string{"pr": {"a.txt": "a2\n"}}, nil)
	tree := strings.TrimSpace(f.git(t, "rev-parse", "origin/pr^{tree}"))
	cmd := exec.Command("git", "--git-dir="+f.local.Dir(), "commit-tree", tree, "-p", "origin/main", "-p", "origin/pr", "-m", "merge")
	cmd.Env = gitport.HermeticEnv(gitport.Identity{})
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.local.Revert(t.Context(), gitport.RevertInput{Commit: strings.TrimSpace(string(out))})
	if err == nil || !strings.Contains(err.Error(), "2 parents") {
		t.Fatalf("err = %v, want a refusal of a two-parent commit", err)
	}
}

func TestPush(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]map[string]string{"pr": {"a.txt": "a2\n"}}, nil)
	ctx := t.Context()
	res, err := f.local.Squash(ctx, gitport.SquashInput{Base: "origin/main", Head: "origin/pr", Title: "candidate"})
	if err != nil {
		t.Fatal(err)
	}
	const ref = "refs/heads/reliant-queue/7"
	zero := strings.Repeat("0", 40)
	if err := f.local.Push(ctx, f.remote.URL(), gitport.PushInput{SHA: res.Commit, Ref: ref, ExpectedOld: zero}); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := f.remote.Head("reliant-queue/7"); got != res.Commit {
		t.Fatalf("remote ref = %s, want %s", got, res.Commit)
	}
	// A retried create of the same sha is an up-to-date no-op; "must not
	// exist" refuses any other sha once the ref exists.
	if err := f.local.Push(ctx, f.remote.URL(), gitport.PushInput{SHA: res.Commit, Ref: ref, ExpectedOld: zero}); err != nil {
		t.Fatalf("a retried identical push failed: %v", err)
	}
	main := f.resolve(t, "origin/main")
	if err := f.local.Push(ctx, f.remote.URL(), gitport.PushInput{SHA: main, Ref: ref, ExpectedOld: zero}); err == nil {
		t.Fatal("a must-not-exist push over an existing ref succeeded")
	}
	// A non-fast-forward needs force.
	if err := f.local.Push(ctx, f.remote.URL(), gitport.PushInput{SHA: main, Ref: ref}); err == nil {
		t.Fatal("a non-fast-forward push without force succeeded")
	}
	if err := f.local.Push(ctx, f.remote.URL(), gitport.PushInput{SHA: main, Ref: ref, ExpectedOld: res.Commit}); err != nil {
		t.Fatalf("lease push: %v", err)
	}
	if err := f.local.Push(ctx, f.remote.URL(), gitport.PushInput{SHA: res.Commit, Ref: ref, Force: true}); err != nil {
		t.Fatalf("force push: %v", err)
	}
	if err := f.local.Push(ctx, f.remote.URL(), gitport.PushInput{SHA: res.Commit, Ref: "heads/x"}); err == nil {
		t.Fatal("an unqualified ref was accepted")
	}
}

func TestOpenAndFetch(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil, nil)
	ctx := t.Context()
	again, err := gitport.Open(ctx, f.local.Dir(), gitport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	next := f.remote.Commit("main", "more", map[string]string{"z.txt": "z\n"})
	if err := again.Fetch(ctx, f.remote.URL(), "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		t.Fatal(err)
	}
	if got := f.resolve(t, "origin/main"); got != next {
		t.Fatalf("origin/main = %s after fetch, want %s", got, next)
	}
	if _, err := gitport.Open(ctx, t.TempDir(), gitport.Options{}); err == nil {
		t.Fatal("Open of an empty directory succeeded")
	}
	if _, err := gitport.Open(ctx, f.local.Dir(), gitport.Options{GitBin: filepath.Join(t.TempDir(), "no-git")}); err == nil {
		t.Fatal("Open with a missing git binary succeeded")
	}
}
