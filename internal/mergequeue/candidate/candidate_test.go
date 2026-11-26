// Copyright (c) 2025 Reliant Labs
package candidate_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/mergequeue/candidate"
	"github.com/reliant-labs/reliant/internal/mergequeue/fakegithub"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
	"github.com/reliant-labs/reliant/internal/mergequeue/gitport"
)

// regenScript is a deterministic generator: gen/all.txt is the sorted
// concatenation of src/*.txt.
const regenScript = "#!/bin/sh\nset -e\nmkdir -p gen\ncat src/*.txt | sort > gen/all.txt\n"

var genGlobs = []string{"gen/**"}

type fixture struct {
	t  *testing.T
	g  *fakegithub.GitHub
	r  *fakegithub.Repo
	wk *gitport.Repo
}

// newFixture seeds main with src/a.txt, its generated gen/all.txt, the
// generator and two plain files, and an empty queue workdir.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	g := fakegithub.New(t)
	r := g.NewRepo("acme/widgets")
	r.Commit("main", "base", map[string]string{
		"regen.sh": regenScript, "src/a.txt": "a\n", "gen/all.txt": "a\n",
		"x.txt": "x\n", "y.txt": "y\n",
	})
	return &fixture{t: t, g: g, r: r, wk: workdir(t, nil)}
}

func workdir(t *testing.T, env []string) *gitport.Repo {
	t.Helper()
	wk, err := gitport.Init(t.Context(), filepath.Join(t.TempDir(), "work.git"), gitport.Options{Env: env})
	if err != nil {
		t.Fatalf("init workdir: %v", err)
	}
	return wk
}

// pr branches off main, commits files and opens a PR.
func (f *fixture) pr(branch string, files map[string]string) candidate.Entry {
	f.t.Helper()
	f.r.Branch(branch, "main")
	head := f.r.Commit(branch, "work on "+branch, files)
	title := "Change " + branch
	n := f.r.OpenPR(fakegithub.PROptions{Title: title, Head: branch})
	return candidate.Entry{Repo: f.r.ID(), Number: n, Head: head, Title: title}
}

func (f *fixture) ports(wk *gitport.Repo) candidate.Ports {
	return candidate.Ports{Git: wk, GitHub: f.g, Remote: f.r.URL()}
}

func (f *fixture) build(wk *gitport.Repo, entries []candidate.Entry, cfg candidate.Config) (candidate.Candidate, []candidate.Ejection) {
	f.t.Helper()
	cand, ej, err := candidate.Build(f.t.Context(), f.ports(wk), f.r.Head("main"), entries, cfg)
	if err != nil {
		f.t.Fatalf("Build: %v", err)
	}
	return cand, ej
}

// land squash-merges the included entries in order, as the lander does, and
// checks main^ and tree(main) == T_i after each one: what Q5's tree check
// relies on.
func (f *fixture) land(cand candidate.Candidate) {
	f.t.Helper()
	ctx := f.t.Context()
	prev := f.r.Head("main")
	for i, e := range cand.Included {
		res, err := f.g.MergePullRequest(ctx, f.r.ID(), e.Number, ghport.MergeRequest{Method: ghport.MergeSquash, ExpectedHeadSHA: e.Head})
		if err != nil {
			f.t.Fatalf("land #%d: %v", e.Number, err)
		}
		c, err := f.g.GetCommit(ctx, f.r.ID(), res.SHA)
		if err != nil {
			f.t.Fatal(err)
		}
		if !slices.Equal(c.Parents, []string{prev}) {
			f.t.Fatalf("land #%d: main^ = %v, want %s", e.Number, c.Parents, prev)
		}
		if c.TreeSHA != cand.Trees[i] {
			f.t.Fatalf("land #%d: tree(main) = %s, want T_%d = %s", e.Number, c.TreeSHA, i+1, cand.Trees[i])
		}
		prev = res.SHA
	}
}

func numbers(es []candidate.Entry) []int {
	var out []int
	for _, e := range es {
		out = append(out, e.Number)
	}
	return out
}

func content(t *testing.T, g *fakegithub.GitHub, r *fakegithub.Repo, path, ref string) string {
	t.Helper()
	b, err := g.GetContents(t.Context(), r.ID(), path, ref)
	if err != nil {
		t.Fatalf("read %s at %s: %v", path, ref, err)
	}
	return string(b)
}

func TestCleanBatchOfThree(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.r.Head("main")
	entries := []candidate.Entry{
		f.pr("one", map[string]string{"x.txt": "x1\n"}),
		f.pr("two", map[string]string{"two.txt": "2\n"}),
		f.pr("three", map[string]string{"y.txt": fakegithub.Deleted, "three/y.txt": "y\n"}),
	}
	cand, ej := f.build(f.wk, entries, candidate.Config{BatchID: "b1"})
	if len(ej) != 0 {
		t.Fatalf("ejections: %+v", ej)
	}
	if cand.Base != m || cand.Ref != "refs/heads/reliant-queue/b1" || len(cand.Trees) != 3 {
		t.Fatalf("candidate = %+v", cand)
	}
	if !slices.Equal(numbers(cand.Included), numbers(entries)) {
		t.Fatalf("included %v, want %v", numbers(cand.Included), numbers(entries))
	}
	ref, err := f.g.GetRef(t.Context(), f.r.ID(), cand.Ref)
	if err != nil || ref.SHA != cand.Commit {
		t.Fatalf("candidate ref = %+v, %v; want %s", ref, err, cand.Commit)
	}
	if f.r.Tree(cand.Commit) != cand.Trees[2] {
		t.Fatal("the candidate's tree is not T_3")
	}

	// One commit per PR, titled with its title, carrying its trailers.
	sha := cand.Commit
	for i := 2; i >= 0; i-- {
		c, err := f.g.GetCommit(t.Context(), f.r.ID(), sha)
		if err != nil {
			t.Fatal(err)
		}
		e := entries[i]
		if title, _, _ := strings.Cut(c.Message, "\n"); title != e.Title {
			t.Errorf("commit %d title %q, want %q", i+1, title, e.Title)
		}
		tr := gitport.ParseTrailers(c.Message)
		for _, want := range []gitport.Trailer{gitport.PRTrailer("acme/widgets", e.Number, e.Head), gitport.BatchTrailer("b1")} {
			if !slices.Contains(tr, want) {
				t.Errorf("commit %d trailers %v lack %v", i+1, tr, want)
			}
		}
		if len(c.Parents) != 1 {
			t.Fatalf("commit %d has parents %v", i+1, c.Parents)
		}
		sha = c.Parents[0]
	}
	if sha != m {
		t.Fatalf("the candidate is not on M: reached %s", sha)
	}

	f.land(cand)
}

func TestConflictingMiddleEntryIsEjected(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	one := f.pr("one", map[string]string{"one.txt": "1\n"})
	two := f.pr("two", map[string]string{"y.txt": "pr\n"})
	three := f.pr("three", map[string]string{"three.txt": "3\n"})
	f.r.Commit("main", "main moves", map[string]string{"y.txt": "main\n"})

	cand, ej := f.build(f.wk, []candidate.Entry{one, two, three}, candidate.Config{BatchID: "b2", Generated: genGlobs, Regen: "sh regen.sh"})
	if len(ej) != 1 || ej[0].Entry.Number != two.Number || ej[0].Reason != candidate.ReasonConflict || !slices.Equal(ej[0].Paths, []string{"y.txt"}) {
		t.Fatalf("ejections = %+v, want #%d conflict on y.txt", ej, two.Number)
	}
	if !slices.Equal(numbers(cand.Included), []int{one.Number, three.Number}) || len(cand.Trees) != 2 {
		t.Fatalf("included %v, trees %d", numbers(cand.Included), len(cand.Trees))
	}
	f.land(cand)
}

func TestConflictWithAnEarlierEntryIsABatchConflict(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	one := f.pr("one", map[string]string{"x.txt": "one\n"})
	two := f.pr("two", map[string]string{"x.txt": "two\n"})

	cand, ej := f.build(f.wk, []candidate.Entry{one, two}, candidate.Config{BatchID: "b3"})
	if len(ej) != 1 || ej[0].Entry.Number != two.Number || ej[0].Reason != candidate.ReasonBatchConflict || !slices.Equal(ej[0].Paths, []string{"x.txt"}) {
		t.Fatalf("ejections = %+v, want #%d batch conflict on x.txt", ej, two.Number)
	}
	if !slices.Equal(numbers(cand.Included), []int{one.Number}) {
		t.Fatalf("included %v", numbers(cand.Included))
	}
}

// generatedConflict queues a clean PR, then one that conflicts with main in
// gen/all.txt alone: both added a source file and regenerated.
func generatedConflict(f *fixture) (one, two candidate.Entry) {
	one = f.pr("one", map[string]string{"x.txt": "x1\n"})
	two = f.pr("two", map[string]string{"src/b.txt": "b\n", "gen/all.txt": "a\nb\n"})
	f.r.Commit("main", "main adds c", map[string]string{"src/c.txt": "c\n", "gen/all.txt": "a\nc\n"})
	return one, two
}

func TestGeneratedOnlyConflictIsRegeneratedAndKept(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	one, two := generatedConflict(f)
	m := f.r.Head("main")
	cfg := candidate.Config{BatchID: "b4", Generated: genGlobs, Regen: "sh regen.sh"}

	cand, ej := f.build(f.wk, []candidate.Entry{one, two}, cfg)
	if len(ej) != 0 {
		t.Fatalf("ejections: %+v", ej)
	}
	if len(cand.Regenerated) != 1 {
		t.Fatalf("regenerated = %+v", cand.Regenerated)
	}
	rg := cand.Regenerated[0]
	if rg.Entry != two || rg.Base != m || !slices.Equal(rg.Conflicts, []string{"gen/all.txt"}) {
		t.Fatalf("regenerated = %+v", rg)
	}
	// The regen commit is the PR's new head: M merged into the reviewed
	// head, regenerated.
	if f.r.Head("two") != rg.NewHead || cand.Included[1].Head != rg.NewHead {
		t.Fatalf("PR head %s, included head %s, want %s", f.r.Head("two"), cand.Included[1].Head, rg.NewHead)
	}
	c, err := f.g.GetCommit(t.Context(), f.r.ID(), rg.NewHead)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Parents, []string{two.Head, m}) {
		t.Fatalf("regen commit parents %v, want [%s %s]", c.Parents, two.Head, m)
	}
	if got := content(t, f.g, f.r, "gen/all.txt", rg.NewHead); got != "a\nb\nc\n" {
		t.Fatalf("regenerated gen/all.txt = %q", got)
	}
	if got := content(t, f.g, f.r, "gen/all.txt", cand.Commit); got != "a\nb\nc\n" {
		t.Fatalf("candidate gen/all.txt = %q", got)
	}
	f.land(cand)
}

func TestRegenTouchingANonGeneratedPathEjects(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	one, two := generatedConflict(f)
	cfg := candidate.Config{BatchID: "b5", Generated: genGlobs, Regen: "sh regen.sh && echo touched > NOTES.md"}

	cand, ej := f.build(f.wk, []candidate.Entry{one, two}, cfg)
	if len(ej) != 1 || ej[0].Entry != two || ej[0].Reason != candidate.ReasonNotGeneratedOnly || !slices.Equal(ej[0].Paths, []string{"NOTES.md"}) {
		t.Fatalf("ejections = %+v, want #%d not generated-only on NOTES.md", ej, two.Number)
	}
	if f.r.Head("two") != two.Head {
		t.Fatal("the PR branch was pushed to")
	}
	if !slices.Equal(numbers(cand.Included), []int{one.Number}) || len(cand.Regenerated) != 0 {
		t.Fatalf("candidate = %+v", cand)
	}
}

func TestFailingRegenEjects(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, two := generatedConflict(f)
	cand, ej := f.build(f.wk, []candidate.Entry{two}, candidate.Config{BatchID: "b6", Generated: genGlobs, Regen: "echo boom; exit 3"})
	if len(ej) != 1 || ej[0].Reason != candidate.ReasonRegenFailed || !strings.Contains(ej[0].Detail, "boom") {
		t.Fatalf("ejections = %+v", ej)
	}
	if cand.Commit != "" || len(cand.Included) != 0 {
		t.Fatalf("an empty batch pushed a candidate: %+v", cand)
	}
	if _, err := f.g.GetRef(t.Context(), f.r.ID(), cand.Ref); !ghport.IsNotFound(err) {
		t.Fatalf("candidate ref: %v, want not found", err)
	}
}

func TestPushIsIdempotent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	one, two := generatedConflict(f)
	cfg := candidate.Config{BatchID: "b7", Generated: genGlobs, Regen: "sh regen.sh"}
	first, _ := f.build(f.wk, []candidate.Entry{one, two}, cfg)

	// A retry in a fresh workdir writes commits at another time, so with
	// other shas: it must adopt both what the first attempt pushed to the
	// candidate ref and the regen commit on the PR branch.
	later := workdir(t, []string{"GIT_AUTHOR_DATE=2030-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2030-01-01T00:00:00Z"})
	again, ej := f.build(later, []candidate.Entry{one, two}, cfg)
	if len(ej) != 0 {
		t.Fatalf("retry ejected %+v", ej)
	}
	if again.Commit != first.Commit || !slices.Equal(again.Trees, first.Trees) || again.Regenerated[0].NewHead != first.Regenerated[0].NewHead {
		t.Fatalf("retry built %+v, first %+v", again, first)
	}
	// The same attempt again in the same workdir: an up-to-date push.
	if same, _ := f.build(later, []candidate.Entry{one, two}, cfg); same.Commit != first.Commit {
		t.Fatalf("second retry pushed %s, want %s", same.Commit, first.Commit)
	}

	// A different candidate under the same batch id is refused.
	_, _, err := candidate.Build(t.Context(), f.ports(f.wk), f.r.Head("main"), []candidate.Entry{one}, cfg)
	if !errors.Is(err, candidate.ErrRefTaken) {
		t.Fatalf("a different candidate on %s: %v, want ErrRefTaken", first.Ref, err)
	}
}

func TestGeneratedOnlyRule(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	base := f.r.Head("main")
	gen := f.r.Commit("main", "gen only", map[string]string{"gen/all.txt": "z\n", "gen/sub/new.txt": "n\n"})
	src := f.r.Commit("main", "and a source file", map[string]string{"src/a.txt": "a2\n"})
	if err := f.wk.Fetch(t.Context(), f.r.URL(), src); err != nil {
		t.Fatal(err)
	}
	tree := func(rev string) string { return f.r.Tree(rev) }
	ok, off, err := candidate.GeneratedOnly(t.Context(), f.wk, tree(gen), tree(base), genGlobs)
	if err != nil || !ok || off != nil {
		t.Fatalf("generated-only diff: ok=%v off=%v err=%v", ok, off, err)
	}
	ok, off, err = candidate.GeneratedOnly(t.Context(), f.wk, tree(src), tree(base), genGlobs)
	if err != nil || ok || !slices.Equal(off, []string{"src/a.txt"}) {
		t.Fatalf("source diff: ok=%v off=%v err=%v", ok, off, err)
	}
}

// Review B1 on #3794: the regen command works in a standalone copy with a
// private repository, never a linked worktree of the queue's repository, so
// whatever it does to git cannot reach the refs, objects or config the batch is
// built from.
func TestRegenCannotTouchTheQueueRepository(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	one, two := generatedConflict(f)
	regen := "sh regen.sh" +
		" && test -d .git && ! test -e .git/commondir && ! test -s .git/objects/info/alternates" +
		" && git update-ref refs/heads/evil HEAD" +
		" && git config core.hooksPath /nowhere"
	cand, ej := f.build(f.wk, []candidate.Entry{one, two}, candidate.Config{BatchID: "b-iso", Generated: genGlobs, Regen: regen})
	if len(ej) != 0 || len(cand.Regenerated) != 1 {
		t.Fatalf("ejections %+v, regenerated %+v: the regen did not run in an isolated copy", ej, cand.Regenerated)
	}
	dir := f.wk.Dir()
	if _, err := os.Stat(filepath.Join(dir, "refs", "heads", "evil")); err == nil {
		t.Fatal("the regen command created a ref in the queue's repository")
	}
	cfg, err := os.ReadFile(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), "/nowhere") {
		t.Fatal("the regen command rewrote the queue repository's config")
	}
}
