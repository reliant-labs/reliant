// Copyright (c) 2025 Reliant Labs
package fakegithub_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/mergequeue/fakegithub"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
)

// seed makes a repo whose main has a.txt and b.txt, and a PR branch "pr"
// (from main) that changes a.txt.
func seed(t *testing.T) (*fakegithub.GitHub, *fakegithub.Repo, int) {
	t.Helper()
	g := fakegithub.New(t)
	r := g.NewRepo("acme/widgets")
	r.Commit("main", "base", map[string]string{"a.txt": "a\n", "b.txt": "b\n"})
	r.Branch("pr", "main")
	r.Commit("pr", "change a", map[string]string{"a.txt": "a2\n"})
	n := r.OpenPR(fakegithub.PROptions{Title: "Change a", Head: "pr"})
	return g, r, n
}

func squash(head string) ghport.MergeRequest {
	return ghport.MergeRequest{Method: ghport.MergeSquash, ExpectedHeadSHA: head}
}

func content(t *testing.T, g *fakegithub.GitHub, r *fakegithub.Repo, path, ref string) string {
	t.Helper()
	b, err := g.GetContents(t.Context(), r.ID(), path, ref)
	if err != nil {
		t.Fatalf("GetContents(%s@%s): %v", path, ref, err)
	}
	return string(b)
}

func TestSquashMergeWritesOneCommitOnTheCurrentBaseTip(t *testing.T) {
	t.Parallel()
	g, r, n := seed(t)
	ctx := t.Context()
	// main moves after the PR branched: the squash goes onto the new tip.
	tip := r.Commit("main", "other", map[string]string{"c.txt": "c\n"})
	pr, err := g.GetPullRequest(ctx, r.ID(), n)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Mergeable != ghport.MergeableClean || pr.State != ghport.PRStateOpen || pr.BaseRef != "main" || pr.HeadRef != "pr" {
		t.Fatalf("open PR = %+v", pr)
	}

	res, err := g.MergePullRequest(ctx, r.ID(), n, squash(pr.HeadSHA))
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if got := r.Head("main"); got != res.SHA {
		t.Fatalf("main = %s, want the merge commit %s", got, res.SHA)
	}
	c, err := g.GetCommit(ctx, r.ID(), res.SHA)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Parents) != 1 || c.Parents[0] != tip {
		t.Fatalf("parents = %v, want exactly the base tip %s", c.Parents, tip)
	}
	if c.TreeSHA != r.Tree(res.SHA) {
		t.Fatalf("tree %s != %s", c.TreeSHA, r.Tree(res.SHA))
	}
	if got := strings.TrimSpace(c.Message); got != "Change a (#1)" {
		t.Fatalf("message = %q, want GitHub's default squash title", got)
	}
	for path, want := range map[string]string{"a.txt": "a2\n", "b.txt": "b\n", "c.txt": "c\n"} {
		if got := content(t, g, r, path, "main"); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}

	pr, err = g.GetPullRequest(ctx, r.ID(), n)
	if err != nil {
		t.Fatal(err)
	}
	if !pr.Merged || pr.State != ghport.PRStateClosed || pr.MergeCommitSHA != res.SHA {
		t.Fatalf("merged PR = %+v", pr)
	}
}

func TestMergeUsesTheCallersTitleAndMessage(t *testing.T) {
	t.Parallel()
	g, r, n := seed(t)
	req := squash(r.Head("pr"))
	req.CommitTitle = "feat: a (#1)"
	req.CommitMessage = "Body.\n\nReliant-Batch: 7"
	res, err := g.MergePullRequest(t.Context(), r.ID(), n, req)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := g.GetCommit(t.Context(), r.ID(), res.SHA)
	if c.Message != "feat: a (#1)\n\nBody.\n\nReliant-Batch: 7\n" {
		t.Fatalf("message = %q", c.Message)
	}
}

func TestMergeRefusesAMovedHead(t *testing.T) {
	t.Parallel()
	g, r, n := seed(t)
	old := r.Head("pr")
	now := r.Commit("pr", "push", map[string]string{"a.txt": "a3\n"})
	_, err := g.MergePullRequest(t.Context(), r.ID(), n, squash(old))
	var hm *ghport.HeadMismatchError
	if !errors.As(err, &hm) || !errors.Is(err, ghport.ErrHeadMismatch) {
		t.Fatalf("err = %v, want a head mismatch", err)
	}
	if hm.Expected != old || hm.Actual != now {
		t.Fatalf("mismatch = %+v", hm)
	}
	if retry, _ := ghport.Retryable(err); retry {
		t.Fatal("a head mismatch must not be retryable")
	}
	if pr, _ := g.GetPullRequest(t.Context(), r.ID(), n); pr.Merged || pr.HeadSHA != now {
		t.Fatalf("PR after refused merge = %+v", pr)
	}
}

func TestMergeRefusesAConflict(t *testing.T) {
	t.Parallel()
	g, r, n := seed(t)
	before := r.Commit("main", "clash", map[string]string{"a.txt": "other\n"})
	pr, err := g.GetPullRequest(t.Context(), r.ID(), n)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Mergeable != ghport.MergeableConflicting {
		t.Fatalf("mergeable = %s, want conflicting", pr.Mergeable)
	}
	_, err = g.MergePullRequest(t.Context(), r.ID(), n, squash(pr.HeadSHA))
	var nm *ghport.NotMergeableError
	if !errors.As(err, &nm) || nm.Reason != ghport.NotMergeableConflict {
		t.Fatalf("err = %v, want not mergeable (conflict)", err)
	}
	if !strings.Contains(nm.Message, "a.txt") {
		t.Fatalf("message %q does not name the conflicting path", nm.Message)
	}
	if r.Head("main") != before {
		t.Fatal("a refused merge moved main")
	}
}

func TestMergeOfAClosedPRIsNotMergeable(t *testing.T) {
	t.Parallel()
	g, r, n := seed(t)
	r.ClosePR(n)
	_, err := g.MergePullRequest(t.Context(), r.ID(), n, squash(r.Head("pr")))
	var nm *ghport.NotMergeableError
	if !errors.As(err, &nm) || nm.Reason != ghport.NotMergeableClosed {
		t.Fatalf("err = %v, want not mergeable (closed)", err)
	}
	r.ReopenPR(n)
	if _, err := g.MergePullRequest(t.Context(), r.ID(), n, squash(r.Head("pr"))); err != nil {
		t.Fatalf("merge after reopen: %v", err)
	}
}

func TestSecondMergeIsAlreadyMerged(t *testing.T) {
	t.Parallel()
	g, r, n := seed(t)
	head := r.Head("pr")
	res, err := g.MergePullRequest(t.Context(), r.ID(), n, squash(head))
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.MergePullRequest(t.Context(), r.ID(), n, squash(head))
	var am *ghport.AlreadyMergedError
	if !errors.As(err, &am) || !errors.Is(err, ghport.ErrAlreadyMerged) {
		t.Fatalf("err = %v, want already merged", err)
	}
	if am.MergeCommitSHA != res.SHA || am.HeadSHA != head {
		t.Fatalf("already merged = %+v, want sha %s head %s", am, res.SHA, head)
	}
	if r.Head("main") != res.SHA {
		t.Fatal("the second call moved main")
	}
}

func TestLostMergeResponseRetriesToAlreadyMerged(t *testing.T) {
	t.Parallel()
	g, r, n := seed(t)
	head := r.Head("pr")
	g.Inject(fakegithub.Fault{Method: fakegithub.MethodMergePullRequest, Err: fakegithub.ServerError(502), AfterEffect: true})
	_, err := g.MergePullRequest(t.Context(), r.ID(), n, squash(head))
	if retry, _ := ghport.Retryable(err); !retry || !errors.Is(err, ghport.ErrTransient) {
		t.Fatalf("err = %v, want a retryable 5xx", err)
	}
	merged := r.Head("main")
	_, err = g.MergePullRequest(t.Context(), r.ID(), n, squash(head))
	var am *ghport.AlreadyMergedError
	if !errors.As(err, &am) || am.MergeCommitSHA != merged || am.HeadSHA != head {
		t.Fatalf("retry err = %v, want already merged as %s", err, merged)
	}
}

func TestRateLimitAndServerErrorFaults(t *testing.T) {
	t.Parallel()
	g, r, n := seed(t)
	reset := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	g.Inject(fakegithub.Fault{Method: fakegithub.MethodMergePullRequest, Err: fakegithub.RateLimit(reset)})
	g.Inject(fakegithub.Fault{Times: 2, Err: fakegithub.ServerError(503)})
	before := r.Head("main")

	_, err := g.MergePullRequest(t.Context(), r.ID(), n, squash(r.Head("pr")))
	retry, at := ghport.Retryable(err)
	if !retry || !at.Equal(reset) || !errors.Is(err, ghport.ErrRateLimited) {
		t.Fatalf("err = %v (retry %v at %v), want a rate limit until %v", err, retry, at, reset)
	}
	for i := range 2 {
		if _, err := g.GetRef(t.Context(), r.ID(), "refs/heads/main"); !errors.Is(err, ghport.ErrTransient) {
			t.Fatalf("call %d: err = %v, want a 5xx", i, err)
		}
	}
	if r.Head("main") != before {
		t.Fatal("a fault before the effect moved main")
	}
	if _, err := g.MergePullRequest(t.Context(), r.ID(), n, squash(r.Head("pr"))); err != nil {
		t.Fatalf("merge after the faults drained: %v", err)
	}
	calls := g.CallsTo(fakegithub.MethodMergePullRequest)
	if len(calls) != 2 || calls[0].Fault == nil || calls[1].Fault != nil {
		t.Fatalf("merge calls = %+v", calls)
	}
}

func TestForeignWriteMovesMainBetweenMerges(t *testing.T) {
	t.Parallel()
	g, r, n1 := seed(t)
	r.Branch("pr2", "main")
	r.Commit("pr2", "change b", map[string]string{"b.txt": "b2\n"})
	n2 := r.OpenPR(fakegithub.PROptions{Title: "Change b", Head: "pr2"})

	first, err := g.MergePullRequest(t.Context(), r.ID(), n1, squash(r.Head("pr")))
	if err != nil {
		t.Fatal(err)
	}
	r.ForeignWrite(fakegithub.MethodMergePullRequest, "main", map[string]string{"x.txt": "x\n"})
	second, err := g.MergePullRequest(t.Context(), r.ID(), n2, squash(r.Head("pr2")))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := g.GetCommit(t.Context(), r.ID(), second.SHA)
	if c.Parents[0] == first.SHA {
		t.Fatal("main^ is the previous merge; the foreign write did not land between them")
	}
	foreign, _ := g.GetCommit(t.Context(), r.ID(), c.Parents[0])
	if len(foreign.Parents) != 1 || foreign.Parents[0] != first.SHA {
		t.Fatalf("the foreign commit %+v is not on top of the first merge", foreign)
	}
	// The hook was one-shot.
	if _, err := g.GetRef(t.Context(), r.ID(), "refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	if r.Head("main") != second.SHA {
		t.Fatal("the foreign write fired twice")
	}
}

func TestDivergeNextSquashChangesTheLandedTreeOnce(t *testing.T) {
	t.Parallel()
	g, r, n1 := seed(t)
	r.Branch("pr2", "main")
	r.Commit("pr2", "change b", map[string]string{"b.txt": "b2\n"})
	n2 := r.OpenPR(fakegithub.PROptions{Head: "pr2"})

	r.DivergeNextSquash("surprise.txt", "github did this\n")
	res, err := g.MergePullRequest(t.Context(), r.ID(), n1, squash(r.Head("pr")))
	if err != nil {
		t.Fatal(err)
	}
	if got := content(t, g, r, "surprise.txt", res.SHA); got != "github did this\n" {
		t.Fatalf("surprise.txt = %q", got)
	}
	res2, err := g.MergePullRequest(t.Context(), r.ID(), n2, squash(r.Head("pr2")))
	if err != nil {
		t.Fatal(err)
	}
	// The second merge is a normal one on top: its own change, nothing new.
	c, _ := g.GetCommit(t.Context(), r.ID(), res2.SHA)
	if r.TreeWithFile(r.Tree(res.SHA), "b.txt", "b2\n") != c.TreeSHA {
		t.Fatal("the divergence applied to more than one merge")
	}

	// OverrideSquash replaces the computed tree outright.
	r.Branch("pr3", "main")
	r.Commit("pr3", "change c", map[string]string{"c.txt": "c\n"})
	n3 := r.OpenPR(fakegithub.PROptions{Head: "pr3"})
	var seen fakegithub.SquashCall
	r.OverrideSquash(func(c fakegithub.SquashCall) string { seen = c; return r.Tree(c.BaseTip) })
	res3, err := g.MergePullRequest(t.Context(), r.ID(), n3, squash(r.Head("pr3")))
	if err != nil {
		t.Fatal(err)
	}
	if seen.PR != n3 || seen.BaseTip != res2.SHA || seen.Tree == r.Tree(res2.SHA) {
		t.Fatalf("override saw %+v", seen)
	}
	if r.Tree(res3.SHA) != r.Tree(res2.SHA) {
		t.Fatal("the override's tree was not the one landed")
	}
}

func TestRefs(t *testing.T) {
	t.Parallel()
	g, r, n := seed(t)
	ctx, id := t.Context(), r.ID()
	main := r.Head("main")
	pr := r.Head("pr")
	const q = "refs/heads/reliant-queue/1"

	if _, err := g.GetRef(ctx, id, q); !ghport.IsNotFound(err) {
		t.Fatalf("GetRef(missing) = %v", err)
	}
	if err := g.CreateRef(ctx, id, q, main); err != nil {
		t.Fatal(err)
	}
	if err := g.CreateRef(ctx, id, q, main); !errors.Is(err, ghport.ErrAlreadyExists) {
		t.Fatalf("CreateRef(existing) = %v", err)
	}
	if err := g.CreateRef(ctx, id, "refs/heads/nope", strings.Repeat("1", 40)); !errors.Is(err, ghport.ErrValidation) {
		t.Fatalf("CreateRef(unknown sha) = %v", err)
	}
	if err := g.CreateRef(ctx, id, "heads/short", main); !errors.Is(err, ghport.ErrValidation) {
		t.Fatalf("CreateRef(unqualified) = %v", err)
	}
	// main -> pr is a fast-forward; back is not.
	if err := g.UpdateRef(ctx, id, q, pr, false); err != nil {
		t.Fatalf("fast-forward: %v", err)
	}
	if err := g.UpdateRef(ctx, id, q, main, false); !errors.Is(err, ghport.ErrNotFastForward) {
		t.Fatalf("rewind without force = %v", err)
	}
	if err := g.UpdateRef(ctx, id, q, main, true); err != nil {
		t.Fatalf("forced rewind: %v", err)
	}
	if ref, err := g.GetRef(ctx, id, q); err != nil || ref.SHA != main || ref.Name != q {
		t.Fatalf("GetRef = %+v, %v", ref, err)
	}
	if err := g.UpdateRef(ctx, id, "refs/heads/missing", main, true); !ghport.IsNotFound(err) {
		t.Fatalf("UpdateRef(missing) = %v", err)
	}
	if err := g.DeleteRef(ctx, id, q); err != nil {
		t.Fatal(err)
	}
	if err := g.DeleteRef(ctx, id, q); !ghport.IsNotFound(err) {
		t.Fatalf("DeleteRef(missing) = %v", err)
	}
	if err := ghport.DeleteBranch(ctx, g, id, "reliant-queue/1"); err != nil {
		t.Fatalf("DeleteBranch is idempotent: %v", err)
	}

	// Deleting an open PR's head branch closes it, as on GitHub.
	if err := ghport.DeleteBranch(ctx, g, id, "pr"); err != nil {
		t.Fatal(err)
	}
	got, _ := g.GetPullRequest(ctx, id, n)
	if got.State != ghport.PRStateClosed || got.Merged || got.HeadSHA != pr {
		t.Fatalf("PR after its branch was deleted = %+v", got)
	}
	if _, err := g.GetRef(ctx, ghport.Repo{Owner: "acme", Name: "nope"}, "refs/heads/main"); !ghport.IsNotFound(err) {
		t.Fatalf("unknown repo = %v", err)
	}
	if _, err := g.GetPullRequest(ctx, id, 99); !ghport.IsNotFound(err) {
		t.Fatalf("unknown PR = %v", err)
	}
}

func TestMergeThenDeleteBranchLeavesThePRMerged(t *testing.T) {
	t.Parallel()
	g, r, n := seed(t)
	if _, err := g.MergePullRequest(t.Context(), r.ID(), n, squash(r.Head("pr"))); err != nil {
		t.Fatal(err)
	}
	if err := ghport.DeleteBranch(t.Context(), g, r.ID(), "pr"); err != nil {
		t.Fatal(err)
	}
	if pr, _ := g.GetPullRequest(t.Context(), r.ID(), n); !pr.Merged {
		t.Fatalf("PR = %+v, want still merged", pr)
	}
	if r.Head("pr") != "" {
		t.Fatal("branch survived its delete")
	}
}

func TestCheckRuns(t *testing.T) {
	t.Parallel()
	g, r, _ := seed(t)
	ctx, id := t.Context(), r.ID()
	sha := r.Head("main")

	a, err := g.CreateCheckRun(ctx, id, ghport.CreateCheckRun{Name: "Reliant", HeadSHA: sha})
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != ghport.CheckQueued || a.ID == 0 {
		t.Fatalf("created = %+v", a)
	}
	b, err := g.CreateCheckRun(ctx, id, ghport.CreateCheckRun{Name: "other", HeadSHA: sha, Conclusion: ghport.ConclusionNeutral})
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != ghport.CheckCompleted {
		t.Fatalf("a conclusion implies completed: %+v", b)
	}
	if _, err := g.CreateCheckRun(ctx, id, ghport.CreateCheckRun{Name: "x", HeadSHA: sha, Status: ghport.CheckInProgress, Conclusion: ghport.ConclusionSuccess}); !errors.Is(err, ghport.ErrValidation) {
		t.Fatalf("in_progress with a conclusion = %v", err)
	}
	if _, err := g.CreateCheckRun(ctx, id, ghport.CreateCheckRun{Name: "x", HeadSHA: "nope"}); !errors.Is(err, ghport.ErrValidation) {
		t.Fatalf("unknown sha = %v", err)
	}

	a, err = g.UpdateCheckRun(ctx, id, a.ID, ghport.UpdateCheckRun{
		Conclusion: ghport.ConclusionSuccess,
		Output:     &ghport.CheckOutput{Title: "Landed", Summary: "batch 7"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != ghport.CheckCompleted || a.Conclusion != ghport.ConclusionSuccess || a.Output.Summary != "batch 7" {
		t.Fatalf("updated = %+v", a)
	}
	if _, err := g.UpdateCheckRun(ctx, id, 999, ghport.UpdateCheckRun{Status: ghport.CheckInProgress}); !ghport.IsNotFound(err) {
		t.Fatalf("update of a missing run = %v", err)
	}

	all, err := g.ListCheckRuns(ctx, id, sha, ghport.ListCheckRuns{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != b.ID || all[1].ID != a.ID {
		t.Fatalf("list = %+v, want newest first", all)
	}
	named, _ := g.ListCheckRuns(ctx, id, sha, ghport.ListCheckRuns{Name: "Reliant"})
	if len(named) != 1 || named[0].ID != a.ID {
		t.Fatalf("list by name = %+v", named)
	}
	if other, _ := g.ListCheckRuns(ctx, id, r.Head("pr"), ghport.ListCheckRuns{}); len(other) != 0 {
		t.Fatalf("runs leaked onto another sha: %+v", other)
	}
}

func TestContentsAndCommits(t *testing.T) {
	t.Parallel()
	g, r, _ := seed(t)
	ctx, id := t.Context(), r.ID()
	sub := r.Commit("main", "nested", map[string]string{"dir/c.txt": "c\n", "b.txt": fakegithub.Deleted})

	for _, ref := range []string{"main", "refs/heads/main", sub} {
		if got := content(t, g, r, "dir/c.txt", ref); got != "c\n" {
			t.Fatalf("dir/c.txt@%s = %q", ref, got)
		}
	}
	if got := content(t, g, r, "a.txt", "pr"); got != "a2\n" {
		t.Fatalf("a.txt@pr = %q", got)
	}
	for _, c := range []struct{ path, ref string }{{"b.txt", "main"}, {"dir", "main"}, {"a.txt", "nope"}} {
		if _, err := g.GetContents(ctx, id, c.path, c.ref); !ghport.IsNotFound(err) {
			t.Errorf("GetContents(%s@%s) = %v, want not found", c.path, c.ref, err)
		}
	}
	c, err := g.GetCommit(ctx, id, sub)
	if err != nil {
		t.Fatal(err)
	}
	if c.SHA != sub || c.TreeSHA != r.Tree(sub) || len(c.Parents) != 1 || strings.TrimSpace(c.Message) != "nested" {
		t.Fatalf("commit = %+v", c)
	}
	if _, err := g.GetCommit(ctx, id, strings.Repeat("2", 40)); !ghport.IsNotFound(err) {
		t.Fatalf("missing commit = %v", err)
	}
}

func TestBeforeCallHookAndCancelledContext(t *testing.T) {
	t.Parallel()
	g, r, _ := seed(t)
	var seen []fakegithub.Method
	remove := g.BeforeCall("", func(c fakegithub.Call) { seen = append(seen, c.Method) })
	_, _ = g.GetRef(t.Context(), r.ID(), "refs/heads/main")
	remove()
	_, _ = g.GetRef(t.Context(), r.ID(), "refs/heads/main")
	if len(seen) != 1 || seen[0] != fakegithub.MethodGetRef {
		t.Fatalf("hook saw %v", seen)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := g.GetRef(ctx, r.ID(), "refs/heads/main"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled call = %v", err)
	}
}
