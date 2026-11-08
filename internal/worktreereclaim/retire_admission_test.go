package worktreereclaim

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The evidence admission (AcquireClean) is taken only on the path that has
// just proven every checkout clean and pushed; every other caller, and a
// Reclaimer that has none, takes the machine-wide one (owner decision,
// 2026-11-07).
func TestRetirementAdmissionFollowsTheProof(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		proven    bool
		withClean bool
		want      string
	}{
		{"proven takes evidence admission", true, true, "clean"},
		{"unproven keeps the machine-wide rule", false, true, "strict"},
		{"no evidence admission falls back", true, false, "strict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			var took []string
			lease := func(kind string) func(context.Context, []string) (RecoveryLease, error) {
				return func(context.Context, []string) (RecoveryLease, error) {
					took = append(took, kind)
					return nil, errors.New(kind + " refused")
				}
			}
			r := &Reclaimer{Root: root, Cwds: func(context.Context) ([]string, error) { return nil, nil },
				ProcessUse: func(context.Context, []string) error { return nil }, Acquire: lease("strict")}
			if tc.withClean {
				r.AcquireClean = lease("clean")
			}
			got := r.retireWorkspace(context.Background(), Worktree{ID: "w", Path: root}, []string{root}, nil, tc.proven)
			if got.Outcome != OutcomeHeld || got.Reason != ReasonInUse {
				t.Fatalf("refused admission must hold in_use: %+v", got)
			}
			if len(took) != 1 || took[0] != tc.want {
				t.Fatalf("took %v, want only %s", took, tc.want)
			}
		})
	}
}

// Uncommitted work never reaches the evidence admission: with the
// machine-wide rule refusing (a shell is open) and the evidence one free, a
// dirty archived worktree is held at both phases and nothing is touched.
func TestRetirementUncommittedWorkNeverTakesEvidenceAdmission(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "builds a git fixture")
	f := newFixture(t)
	f.idle()
	cleanTaken := false
	f.r.Acquire = func(context.Context, []string) (RecoveryLease, error) {
		return nil, errors.New("open terminal session has unknown filesystem scope")
	}
	f.r.AcquireClean = func(context.Context, []string) (RecoveryLease, error) {
		cleanTaken = true
		return finalizeTestLease{}, nil
	}
	w := f.worktree("dirty", "feat/dirty")
	p := filepath.Join(w.Path, "notes.md")
	write(t, p, "uncommitted\n")
	got := f.one(w)
	if got.Outcome == OutcomeRemoved || got.Reason != ReasonDirty {
		t.Fatalf("got %+v, want held dirty", got)
	}
	w.Remove, w.Fence = true, "100"
	if got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]; got.Outcome == OutcomeRemoved {
		t.Fatalf("phase two removed uncommitted work: %+v", got)
	}
	if cleanTaken {
		t.Fatal("uncommitted work took the evidence admission")
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != "uncommitted\n" {
		t.Fatalf("uncommitted work touched: %q %v", b, err)
	}
}

// The proof is taken again on the PARKED checkout: work that appears after
// the move-aside, while the evidence admission is held, puts the checkout
// back where it was and deletes nothing.
func TestRetirementReprovesCleanAfterMoveAside(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "builds a git fixture")
	f := newFixture(t)
	f.idle()
	f.r.Acquire = func(context.Context, []string) (RecoveryLease, error) {
		return nil, errors.New("managed background process has unknown filesystem scope")
	}
	f.r.AcquireClean = func(context.Context, []string) (RecoveryLease, error) { return finalizeTestLease{}, nil }
	w := f.worktree("late", "feat/late")
	quarantine := f.r.quarantineRoot(w.ID)
	wrote := ""
	f.r.ProcessUse = func(context.Context, []string) error {
		if wrote != "" {
			return nil
		}
		entries, _ := os.ReadDir(quarantine)
		for _, e := range entries {
			if parked := filepath.Join(quarantine, e.Name()); e.IsDir() && exists(filepath.Join(parked, ".git")) {
				wrote = filepath.Join(parked, "late.txt")
				write(t, wrote, "written after parking\n")
			}
		}
		return nil
	}
	if got := f.one(w); got.Outcome == OutcomeRemoved {
		t.Fatalf("removed a checkout that changed after parking: %+v", got)
	}
	if wrote == "" {
		t.Fatal("fixture never saw the parked checkout")
	}
	if b, err := os.ReadFile(filepath.Join(w.Path, "late.txt")); err != nil || string(b) != "written after parking\n" {
		t.Fatalf("late work not back at the original path: %q %v", b, err)
	}
}
