package worktreereclaim

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWholeRetirementCleanWorktreeAndRuntimeExclusion(t *testing.T) {
	heavyUnderShort(t, "temporary real Git whole-worktree retirement")
	f := newFixture(t)
	f.idle()
	w := f.worktree("whole", "feat/whole")
	leaseHeld := false
	checks := 0
	f.r.Acquire = func(context.Context, []string) (RecoveryLease, error) {
		leaseHeld = true
		return stripTestLease{check: func() error {
			if !leaseHeld {
				return errors.New("no admission")
			}
			return nil
		}, release: func() { leaseHeld = false }}, nil
	}
	f.r.ProcessUse = func(context.Context, []string) error {
		checks++
		if !leaseHeld {
			t.Fatal("process check outside admission")
		}
		return nil
	}
	got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	if got.Outcome != OutcomeClaimed {
		t.Fatalf("claim: %+v", got)
	}
	w.Remove = true
	got = f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	if got.Outcome != OutcomeRemoved || exists(w.Path) {
		t.Fatalf("whole cleanup: %+v", got)
	}
	if checks < 3 || leaseHeld {
		t.Fatalf("lease lifecycle checks=%d held=%v", checks, leaseHeld)
	}
}
func TestWholeRetirementPreservesProtectedAndFreshFiles(t *testing.T) {
	heavyUnderShort(t, "temporary real Git worktrees held for protected/fresh descendants")
	for _, kind := range []string{"protected", "fresh", "open-file", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			ignore(t, f, "node_modules/\n")
			f.idle()
			w := f.worktree("hold", "feat/hold")
			p := filepath.Join(w.Path, "node_modules", "pkg", "index.js")
			write(t, p, "regenerable")
			if kind == "protected" {
				p = filepath.Join(w.Path, "node_modules", "pkg", ".env")
				write(t, p, "unique")
			}
			if kind == "fresh" {
				f.r.Now = nil
				f.r.IdleFloor = time.Hour
			}
			if kind == "open-file" {
				f.r.ProcessUse = func(context.Context, []string) error { return errors.New("file open") }
			}
			if kind == "unsupported" {
				f.r.ProcessUse = func(context.Context, []string) error { return ErrProcessUseUnsupported }
			}
			// Claim may itself hold a fresh/protected tree; neither phase may remove it.
			_ = f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
			w.Remove = true
			got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
			if got.Outcome == OutcomeRemoved || !exists(p) {
				t.Fatalf("uncertain tree removed: %+v", got)
			}
		})
	}
}
func TestWholeRetirementConcurrentGitIndexChangeHoldsBeforeUnlink(t *testing.T) {
	heavyUnderShort(t, "temporary Git retirement with deterministic concurrent index write")
	f := newFixture(t)
	f.idle()
	w := f.worktree("index-change", "feat/index-change")
	changed := false
	f.r.ProcessUse = func(context.Context, []string) error {
		for _, q := range f.r.quarantined(w.ID) {
			if _, err := os.Stat(q + ".disposing"); err == nil && !changed {
				changed = true
				index := filepath.Join(adminDir(q), "index")
				file, err := os.OpenFile(index, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.Write([]byte("concurrent git mutation")); err != nil {
					t.Fatal(err)
				}
				file.Close()
			}
		}
		return nil
	}
	got := f.one(w)
	if !changed || got.Outcome != OutcomeHeld || got.Reason != ReasonQuarantined {
		t.Fatalf("concurrent index changed without hold: %+v", got)
	}
	q := f.r.quarantined(w.ID)[0]
	if !exists(filepath.Join(q, "a.txt")) || !exists(filepath.Join(q, ".git")) {
		t.Fatal("index change discarded checkout contents")
	}
}

func TestWholeRetirementDisposalJournalSurvivesMissingCheckout(t *testing.T) {
	r := &Reclaimer{Root: t.TempDir()}
	q := filepath.Join(r.quarantineRoot("missing"), "checkout")
	if err := os.MkdirAll(filepath.Dir(q), 0700); err != nil {
		t.Fatal(err)
	}
	write(t, originMarker(q), filepath.Join(r.Root, "original"))
	write(t, q+".disposing", "fence")
	restored, parked := r.RecoverQuarantined(context.Background(), "missing")
	if len(restored) != 0 || len(parked) != 1 || parked[0] != q {
		t.Fatalf("crash after tree removal lost pending disposal: restored=%v parked=%v", restored, parked)
	}
	if !exists(q + ".disposing") {
		t.Fatal("disposal evidence removed")
	}
}

func TestWholeRetirementPartialJournalNeverAutomaticallyRevives(t *testing.T) {
	heavyUnderShort(t, "temporary Git quarantine recovery")
	f := newFixture(t)
	w := f.worktree("partial", "feat/partial")
	q := f.r.newQuarantinePath(w, w.Path)
	if err := os.MkdirAll(filepath.Dir(q), 0700); err != nil {
		t.Fatal(err)
	}
	run(t, f.project, "worktree", "unlock", w.Path)
	run(t, f.project, "worktree", "move", w.Path, q)
	write(t, originMarker(q), w.Path)
	write(t, q+".disposing", "123")
	restored, parked := f.r.RecoverQuarantined(context.Background(), w.ID)
	if len(restored) != 0 || len(parked) != 1 || exists(w.Path) {
		t.Fatalf("partial checkout auto-revived: restored=%v parked=%v", restored, parked)
	}
	if !exists(filepath.Join(q, "a.txt")) {
		t.Fatal("held evidence was deleted")
	}
}
