// Copyright (c) 2025 Reliant Labs
package fakegithub_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/mergequeue/fakegithub"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
	"github.com/reliant-labs/reliant/internal/mergequeue/gitport"
)

// text is a 12-line file, with some lines replaced: enough shared content for
// git's rename detection, and distinct hunks for non-overlapping edits.
func text(edits map[int]string) string {
	var b strings.Builder
	for i := 1; i <= 12; i++ {
		line := fmt.Sprintf("line %d of a reasonably long shared file", i)
		if e, ok := edits[i]; ok {
			line = e
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func clone(t *testing.T, r *fakegithub.Repo) *gitport.Repo {
	t.Helper()
	g, err := gitport.Clone(t.Context(), r.URL(), filepath.Join(t.TempDir(), "work.git"), gitport.Options{})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	return g
}

// TestGitPortSquashTreeEqualsGitHubSquashTree is the equivalence the lander's
// tree check (tree(main) == T_i) rests on: for the same base tip and head,
// gitport.Squash (in-memory merge-tree) predicts exactly the tree the fake's
// GitHub squash merge (porcelain merge --squash in a worktree) lands.
func TestGitPortSquashTreeEqualsGitHubSquashTree(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		base     map[string]string
		mainNext map[string]string // applied to main after the PR branched
		pr       map[string]string
		conflict []string
	}{
		{
			name:     "disjoint files",
			base:     map[string]string{"a.txt": "a\n", "c.txt": "c\n"},
			mainNext: map[string]string{"c.txt": "c2\n"},
			pr:       map[string]string{"a.txt": "a2\n", "new/d.txt": "d\n"},
		},
		{
			name:     "same file, separate hunks",
			base:     map[string]string{"f.txt": text(nil)},
			mainNext: map[string]string{"f.txt": text(map[int]string{1: "main edits line 1"})},
			pr:       map[string]string{"f.txt": text(map[int]string{11: "pr edits line 11"})},
		},
		{
			name:     "rename on the head, edit on the base",
			base:     map[string]string{"big.txt": text(nil)},
			mainNext: map[string]string{"big.txt": text(map[int]string{2: "main edits line 2"})},
			pr:       map[string]string{"big.txt": fakegithub.Deleted, "moved/big.txt": text(nil)},
		},
		{
			name: "delete on the head",
			base: map[string]string{"a.txt": "a\n", "b.txt": "b\n"},
			pr:   map[string]string{"b.txt": fakegithub.Deleted},
		},
		{
			name: "base did not move",
			base: map[string]string{"a.txt": "a\n"},
			pr:   map[string]string{"a.txt": "a2\n"},
		},
		{
			name:     "both sides add the same file",
			base:     map[string]string{"a.txt": "a\n"},
			mainNext: map[string]string{"same.txt": "same\n", "a.txt": "a2\n"},
			pr:       map[string]string{"same.txt": "same\n"},
		},
		{
			name:     "head already contained in the base",
			base:     map[string]string{"a.txt": "a\n"},
			mainNext: map[string]string{"a.txt": "a2\n"},
			pr:       map[string]string{"a.txt": "a2\n"},
		},
		{
			name:     "conflict",
			base:     map[string]string{"a.txt": "a\n", "b.txt": "b\n", "ok.txt": "ok\n"},
			mainNext: map[string]string{"a.txt": "main\n", "b.txt": "main\n"},
			pr:       map[string]string{"a.txt": "pr\n", "b.txt": "pr\n", "ok.txt": "ok2\n"},
			conflict: []string{"a.txt", "b.txt"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := fakegithub.New(t)
			r := g.NewRepo("acme/eq")
			r.Commit("main", "base", tc.base)
			r.Branch("pr", "main")
			head := r.Commit("pr", "pr", tc.pr)
			if tc.mainNext != nil {
				r.Commit("main", "main moves", tc.mainNext)
			}
			n := r.OpenPR(fakegithub.PROptions{Title: tc.name, Head: "pr"})

			// The queue's prediction, from its own clone.
			local := clone(t, r)
			pred, perr := local.Squash(t.Context(), gitport.SquashInput{
				Base: "refs/remotes/origin/main", Head: "refs/remotes/origin/pr", Title: tc.name,
			})

			// GitHub's merge.
			res, merr := g.MergePullRequest(t.Context(), r.ID(), n, squash(head))

			if tc.conflict != nil {
				var ce *gitport.ConflictError
				if !errors.As(perr, &ce) || !slices.Equal(ce.Paths, tc.conflict) {
					t.Fatalf("gitport: %v, want conflicts %v", perr, tc.conflict)
				}
				var nm *ghport.NotMergeableError
				if !errors.As(merr, &nm) || nm.Reason != ghport.NotMergeableConflict {
					t.Fatalf("github: %v, want not mergeable", merr)
				}
				for _, p := range tc.conflict {
					if !strings.Contains(nm.Message, p) {
						t.Errorf("github conflict %q does not name %s", nm.Message, p)
					}
				}
				return
			}
			if perr != nil || merr != nil {
				t.Fatalf("gitport: %v; github: %v", perr, merr)
			}
			if landed := r.Tree(res.SHA); landed != pred.Tree {
				t.Fatalf("GitHub landed tree %s, gitport predicted %s", landed, pred.Tree)
			}
		})
	}
}

// TestBatchLandsTreeByTree runs the whole D1 sequence end to end: build T_1..T_n
// with gitport, push the candidate to reliant-queue/<batch>, land each PR with
// a squash merge bound to its head, and check main^ == the previous tip and
// tree(main) == T_i after each one.
func TestBatchLandsTreeByTree(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	g := fakegithub.New(t)
	r := g.NewRepo("acme/batch")
	r.Commit("main", "base", map[string]string{"f.txt": text(nil), "x.txt": "x\n"})
	var prs []int
	var heads []string
	for i, edit := range []map[string]string{
		{"f.txt": text(map[int]string{1: "one"})},
		{"f.txt": text(map[int]string{6: "two"}), "two.txt": "2\n"},
		{"x.txt": fakegithub.Deleted, "three/x.txt": "x\n"},
	} {
		branch := fmt.Sprintf("pr%d", i+1)
		r.Branch(branch, "main")
		heads = append(heads, r.Commit(branch, branch, edit))
		prs = append(prs, r.OpenPR(fakegithub.PROptions{Title: "PR " + branch, Head: branch}))
	}

	local := clone(t, r)
	m, err := local.ResolveCommit(ctx, "refs/remotes/origin/main")
	if err != nil {
		t.Fatal(err)
	}
	tip := m
	var trees []string
	for i, n := range prs {
		res, err := local.Squash(ctx, gitport.SquashInput{
			Base: tip, Head: heads[i], Title: fmt.Sprintf("PR pr%d (#%d)", i+1, n),
			Trailer: []gitport.Trailer{gitport.PRTrailer(r.ID().String(), n, heads[i]), gitport.BatchTrailer("b1")},
		})
		if err != nil {
			t.Fatalf("squash %d: %v", i+1, err)
		}
		tip = res.Commit
		trees = append(trees, res.Tree)
	}
	if err := local.Push(ctx, r.URL(), gitport.PushInput{SHA: tip, Ref: "refs/heads/reliant-queue/b1", ExpectedOld: strings.Repeat("0", 40)}); err != nil {
		t.Fatalf("push candidate: %v", err)
	}
	if ref, err := g.GetRef(ctx, r.ID(), "refs/heads/reliant-queue/b1"); err != nil || ref.SHA != tip {
		t.Fatalf("candidate ref = %+v, %v; want %s", ref, err, tip)
	}

	prev := m
	for i, n := range prs {
		res, err := g.MergePullRequest(ctx, r.ID(), n, squash(heads[i]))
		if err != nil {
			t.Fatalf("land %d: %v", i+1, err)
		}
		c, err := g.GetCommit(ctx, r.ID(), res.SHA)
		if err != nil {
			t.Fatal(err)
		}
		if len(c.Parents) != 1 || c.Parents[0] != prev {
			t.Fatalf("land %d: main^ = %v, want %s", i+1, c.Parents, prev)
		}
		if c.TreeSHA != trees[i] {
			t.Fatalf("land %d: tree(main) = %s, want T_%d = %s", i+1, c.TreeSHA, i+1, trees[i])
		}
		prev = res.SHA
	}
	if r.Tree("main") != trees[len(trees)-1] {
		t.Fatal("main's final tree is not the candidate's")
	}
}

// TestPushToPRBranchIsANewHead is the regen path: the queue pushes a commit to
// the PR branch through git, and GitHub then reads it as the PR's head.
func TestPushToPRBranchIsANewHead(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	g, r, n := seed(t)
	r.Commit("main", "main moves", map[string]string{"gen.txt": "main\n"})
	local := clone(t, r)
	regen, err := local.Squash(ctx, gitport.SquashInput{
		Base: "refs/remotes/origin/pr", Head: "refs/remotes/origin/main", Title: "Merge main and regenerate",
	})
	if err != nil {
		t.Fatal(err)
	}
	old := r.Head("pr")
	if err := local.Push(ctx, r.URL(), gitport.PushInput{SHA: regen.Commit, Ref: "refs/heads/pr", ExpectedOld: old}); err != nil {
		t.Fatalf("push: %v", err)
	}
	pr, err := g.GetPullRequest(ctx, r.ID(), n)
	if err != nil {
		t.Fatal(err)
	}
	if pr.HeadSHA != regen.Commit {
		t.Fatalf("PR head = %s, want the pushed %s", pr.HeadSHA, regen.Commit)
	}
	if _, err := g.MergePullRequest(ctx, r.ID(), n, squash(old)); !errors.Is(err, ghport.ErrHeadMismatch) {
		t.Fatalf("merge at the old head = %v, want a mismatch", err)
	}
	if _, err := g.MergePullRequest(ctx, r.ID(), n, squash(regen.Commit)); err != nil {
		t.Fatalf("merge at the new head: %v", err)
	}
	// A compare-and-swap push against a stale expectation is refused.
	if err := local.Push(ctx, r.URL(), gitport.PushInput{SHA: regen.Commit, Ref: "refs/heads/main", ExpectedOld: old}); err == nil {
		t.Fatal("a push with a stale lease succeeded")
	}
}
