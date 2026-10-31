package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetirementGitIndexMutationDuringClassificationPreservesStagedWork(t *testing.T) {
	heavyUnderShort(t, "real automatic retirement with deterministic late index mutation")
	f := newFixture(t)
	f.idle()
	w := f.worktree("late-index", "feat/late-index")
	write(t, filepath.Join(w.Path, "a.txt"), "new committed content")
	run(t, w.Path, "add", "a.txt")
	run(t, w.Path, "commit", "-q", "-m", "branch")
	changed := false
	oid := ""
	f.r.PRMerged = func(_ context.Context, checkout, branch, head string) bool {
		if strings.Contains(checkout, string(filepath.Separator)+".reclaim"+string(filepath.Separator)) && !changed {
			changed = true
			source := filepath.Join(t.TempDir(), "staged.txt")
			write(t, source, "unique intermediate staged work")
			oid = run(t, checkout, "hash-object", "-w", source)
			run(t, checkout, "update-index", "--cacheinfo", "100644,"+oid+",a.txt")
		}
		return true
	}
	f.r.ProcessUse = func(context.Context, []string) error { f.r.mu.Lock(); f.r.netCache = nil; f.r.mu.Unlock(); return nil }
	got := f.one(w)
	if !changed || got.Outcome != OutcomeHeld || !exists(filepath.Join(w.Path, "a.txt")) {
		t.Fatalf("index mutation not preserved/restored: %+v", got)
	}
	index := run(t, w.Path, "ls-files", "--stage", "a.txt")
	if !strings.Contains(index, oid) {
		t.Fatalf("sole staged work lost: %s", index)
	}
}

func TestRetirementGitPrivateStateMutationAtDisposalPreservesHistory(t *testing.T) {
	heavyUnderShort(t, "real automatic retirement private Git history mutations")
	for _, kind := range []string{"private-ref", "reflog", "pseudo-ref", "resolved-head"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.idle()
			w := f.worktree("private-history", "feat/private-history")
			tip := run(t, f.project, "commit-tree", "HEAD^{tree}", "-p", "HEAD", "-m", "private work")
			changed := false
			mutatedPath := ""
			f.r.ProcessUse = func(context.Context, []string) error {
				for _, q := range f.r.quarantined(w.ID) {
					if _, err := os.Stat(q + ".disposing"); err == nil && !changed {
						changed = true
						mutatedPath = q
						switch kind {
						case "private-ref":
							run(t, q, "update-ref", "refs/worktree/only-copy", tip)
						case "reflog":
							path := filepath.Join(adminDir(q), "logs", "HEAD")
							file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
							if err != nil {
								t.Fatal(err)
							}
							file.WriteString(run(t, q, "rev-parse", "HEAD") + " " + tip + " Test <test@example.invalid> 1 +0000\tprivate history\n")
							file.Close()
						case "pseudo-ref":
							write(t, filepath.Join(adminDir(q), "ORIG_HEAD"), tip+"\n")
						case "resolved-head":
							run(t, q, "update-ref", "refs/heads/feat/private-history", tip)
						}
					}
				}
				return nil
			}
			got := f.one(w)
			if !changed || got.Outcome != OutcomeHeld || got.Reason != ReasonQuarantined {
				t.Fatalf("private state mutation deleted or not held: %+v", got)
			}
			if !exists(filepath.Join(mutatedPath, "a.txt")) || !exists(filepath.Join(mutatedPath, ".git")) {
				t.Fatal("private history mutation lost checkout/administration")
			}
			if kind == "private-ref" && run(t, mutatedPath, "rev-parse", "refs/worktree/only-copy") != tip {
				t.Fatal("sole private reference lost")
			}
		})
	}
}
