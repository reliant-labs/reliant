// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
)

// gitCounter records the git processes the reaper starts under a context
// from counting.
type gitCounter struct {
	mu   sync.Mutex
	runs [][]string
}

// counting returns ctx with c told of every git process started under it.
func (c *gitCounter) counting(ctx context.Context) context.Context {
	return withGitObserver(ctx, func(args []string) {
		c.mu.Lock()
		c.runs = append(c.runs, append([]string(nil), args...))
		c.mu.Unlock()
	})
}

// take returns how many git processes ran since the last take, and a
// breakdown by subcommand, then resets.
func (c *gitCounter) take() (int, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	by := map[string]int{}
	for _, a := range c.runs {
		by[subcommand(a)]++
	}
	n := len(c.runs)
	c.runs = nil
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return by[keys[i]] > by[keys[j]] || by[keys[i]] == by[keys[j]] && keys[i] < keys[j]
	})
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, by[k]))
	}
	return n, strings.Join(parts, " ")
}

// subcommand names a git invocation by its first non-option word, plus the
// worktree verb ("worktree list").
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-c" {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		if a == "worktree" && i+1 < len(args) {
			return a + " " + args[i+1]
		}
		return a
	}
	return "?"
}

// The reaper's own git processes for one full automatic pass (phase one, then
// phase two) over one worktree, in the three shapes a sweep meets most. Each
// git process is a fork and an exec; on a loaded machine that is tens of
// milliseconds, and a daemon sweeps every archived worktree it has. The
// budgets fail a change that adds processes to the common path; lower them
// when a change removes some. Before the reaper read worktree registrations
// from their files and remembered what it had asked of a commit pair, these
// were 47, 143 and 71.
func TestGitProcessesPerPass(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		budget int
		setup  func(f *fixture, w Worktree)
	}{
		// Exact index/private-history proof is bound before/after classification
		// and each deletion. These required fresh resolved-ref reads replace the
		// unsafe post-classification-only baseline (P1 regression coverage).
		{"clean, on main", 38, func(*fixture, Worktree) {}},
		{"unpushed commit, held for remote recovery", 54, func(f *fixture, w Worktree) {
			commit(f.t, w.Path, "b.txt", "b\n")
		}},
		{"squash-merged and pruned", 49, func(f *fixture, w Worktree) {
			f.commitIn(w, "feat/x", "one.txt", "1\n", true)
			f.squashMerge("feat/x")
			f.pruned(w)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.idle()
			w := f.worktree("gb", "feat/x")
			tc.setup(f, w)
			c := &gitCounter{}
			got := f.oneIn(c.counting(context.Background()), w)
			n, by := c.take()
			if strings.HasPrefix(tc.name, "unpushed") {
				if got.Outcome != OutcomeHeld || !exists(w.Path) || len(got.SnapshotRefs) != 0 {
					t.Fatalf("got %+v, want held without local snapshot fallback", got)
				}
			} else if got.Outcome != OutcomeRemoved {
				t.Fatalf("got %+v, want removed", got)
			}
			t.Logf("%d git processes: %s", n, by)
			if n > tc.budget {
				t.Errorf("one pass started %d git processes, budget %d: %s", n, tc.budget, by)
			}
		})
	}
}
