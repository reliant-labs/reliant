package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecoverableIdleWholeWorktreeProofNeverDeletesLiveFiles(t *testing.T) {
	heavyUnderShort(t, "real git clean pushed/merged whole-worktree eligibility")
	f := newFixture(t)
	f.idle()
	ignore(t, f, "node_modules/\n/tmp/\n")
	w := f.worktree("idle-whole", "feat/idle-whole")
	w.State = StateActive
	w.Fence = ""
	w.ProbeRecoverableAfterSeconds = 3600
	p := filepath.Join(w.Path, "node_modules", "pkg", "index.js")
	write(t, p, "cache")
	got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	if !got.RecoverableIdle || !exists(w.Path) || !exists(p) || len(got.Stripped) != 0 {
		t.Fatalf("read-only proof: %+v", got)
	}
}
func TestRecoverableIdleRetainsUniqueFreshAndUncertainWork(t *testing.T) {
	heavyUnderShort(t, "real git whole-worktree preservation cases")
	for _, kind := range []string{"untracked", "dirty", "protected", "fresh-cache", "fresh-tool", "unsupported", "process", "keep", "retiring", "unpushed"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.idle()
			ignore(t, f, "node_modules/\n.tools/\n/tmp/\n")
			w := f.worktree("idle-hold", "feat/idle-hold")
			w.State = StateActive
			w.Fence = ""
			w.ProbeRecoverableAfterSeconds = 3600
			w.ProbeIdle = true // stale/combined caller must not bypass stronger proof
			p := filepath.Join(w.Path, "node_modules", "pkg", "index.js")
			write(t, p, "cache")
			switch kind {
			case "untracked":
				write(t, filepath.Join(w.Path, "unique.txt"), "only copy")
			case "dirty":
				write(t, filepath.Join(w.Path, "a.txt"), "modified")
			case "protected":
				write(t, filepath.Join(w.Path, "node_modules", "pkg", ".env"), "only secret")
			case "fresh-cache":
				now := f.r.now()
				if err := os.Chtimes(p, now, now); err != nil {
					t.Fatal(err)
				}
			case "fresh-tool":
				tool := filepath.Join(w.Path, ".tools", "bin", "tool")
				write(t, tool, "binary")
				now := f.r.now()
				if err := os.Chtimes(tool, now, now); err != nil {
					t.Fatal(err)
				}
			case "unsupported":
				f.r.ProcessUse = func(context.Context, []string) error { return ErrProcessUseUnsupported }
			case "process":
				f.r.Cwds = func(context.Context) ([]string, error) { return []string{w.Path}, nil }
			case "keep":
				w.KeepFiles = true
			case "retiring":
				w.Retire = "123"
			case "unpushed":
				f.commitIn(w, "feat/idle-hold", "unique.txt", "not pushed", false)
			}
			got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
			if got.RecoverableIdle || got.Outcome == OutcomeIdleDone || !exists(p) || !exists(w.Path) || len(got.Stripped) != 0 {
				t.Fatalf("%s lost preservation: %+v", kind, got)
			}
		})
	}
}
func TestRecoverableIdleUsesConfiguredFloor(t *testing.T) {
	heavyUnderShort(t, "real git idle floor proof")
	f := newFixture(t)
	f.idle()
	w := f.worktree("floor", "feat/floor")
	w.State = StateActive
	w.Fence = ""
	w.ProbeRecoverableAfterSeconds = int64((365 * 24 * time.Hour) / time.Second)
	if got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]; got.RecoverableIdle {
		t.Fatalf("ignored configured idle floor: %+v", got)
	}
}
