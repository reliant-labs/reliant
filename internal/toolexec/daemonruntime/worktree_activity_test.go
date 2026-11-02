package daemonruntime

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

func newTestRecoveryGate() *recoveryAdmission {
	return &recoveryAdmission{work: map[uint64][]string{}, leases: map[uint64]*WorktreeRecoveryLease{}}
}
func TestRecoveryAdmissionUnknownManagedLifetimeSurvivesStartReturn(t *testing.T) {
	for _, kind := range []string{"background", "terminal"} {
		t.Run(kind, func(t *testing.T) {
			g := newTestRecoveryGate()
			workspace, outside := t.TempDir(), t.TempDir()
			var running atomic.Bool
			// This injected registry models an arbitrary shell started outside the
			// workspace: no cwd/fd/exe mapping can disclose its future delayed write.
			// Explicit completion is the only release, not returning its start result.
			g.managedUse = func() error {
				if running.Load() {
					return errRecoveryAdmission
				}
				return nil
			}
			finishStart, err := g.begin(nil)
			if err != nil {
				t.Fatal(err)
			}
			initialCwd := outside
			if admissionOverlap(initialCwd, workspace) {
				t.Fatal("fixture cwd must be outside")
			}
			running.Store(true) // manager publishes process/session before returning
			finishStart()
			delayedWrite := func() {
				if err := os.WriteFile(filepath.Join(workspace, "delayed"), []byte("written"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, root := range []string{workspace, t.TempDir()} {
				if lease, err := g.acquire([]string{root}); err == nil {
					lease.Release()
					t.Fatal("unknown managed lifetime must veto ANY finalizer")
				}
			}
			delayedWrite() // no sleeps: advance the injected managed lifetime
			running.Store(false)
			lease, err := g.acquire([]string{workspace})
			if err != nil {
				t.Fatal("completion must release veto", err)
			}
			lease.Release()
		})
	}
}

func TestRecoveryAdmissionFreshCheckRetainsUnknownManagedVeto(t *testing.T) {
	g := newTestRecoveryGate()
	var running atomic.Bool
	g.managedUse = func() error {
		if running.Load() {
			return errRecoveryAdmission
		}
		return nil
	}
	lease, err := g.acquire([]string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	// A registry recovered after initial admission is checked again before
	// removal. Unknown scope must veto even without matching cwd/open files.
	running.Store(true)
	if err := lease.Check(context.Background()); err != errRecoveryAdmission {
		t.Fatalf("fresh lifetime veto missing: %v", err)
	}
}

func TestRecoveryAdmissionScopedOverlapAndUnknown(t *testing.T) {
	g := newTestRecoveryGate()
	root := t.TempDir()
	other := t.TempDir()
	lease, err := g.acquire([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	for _, paths := range [][]string{{root}, {filepath.Join(root, "child")}, {filepath.Dir(root)}, nil} {
		if done, err := g.begin(paths); err == nil {
			done()
			t.Fatalf("admitted overlapping/unknown paths %v", paths)
		}
	}
	done, err := g.begin([]string{other})
	if err != nil {
		t.Fatal("unrelated work must remain available", err)
	}
	done()
	lease.Release()
	lease.Release()
	done, err = g.begin(nil)
	if err != nil {
		t.Fatal(err)
	}
	done()
}
func TestRecoveryAdmissionActiveWorkAndLifetime(t *testing.T) {
	g := newTestRecoveryGate()
	root := t.TempDir()
	done, err := g.begin([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if lease, err := g.acquire([]string{root}); err == nil {
		lease.Release()
		t.Fatal("cannot acquire while admitted work lives")
	}
	done()
	lease, err := g.acquire([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	lease.deadline = time.Now().Add(-time.Second)
	if err := lease.Check(context.Background()); err == nil {
		t.Fatal("expired lease must not authorize")
	}
	// Expiry invalidates authority but keeps admissions closed until explicit
	// release, so a slow finalizer cannot accidentally write beside new work.
	if done, err := g.begin(nil); err == nil {
		done()
		t.Fatal("expired lease still excludes work")
	}
	lease.Release()
	if err := lease.Check(context.Background()); err == nil {
		t.Fatal("released lease must not authorize")
	}
}
func TestRecoveryAdmissionSymlinkAndFutureChild(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlink privilege platform")
	}
	g := newTestRecoveryGate()
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	lease, err := g.acquire([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if done, err := g.begin([]string{filepath.Join(alias, "future", "child")}); err == nil {
		done()
		t.Fatal("symlink alias escaped lease")
	}
}
func TestRecoveryAdmissionRaceNoConcurrentAuthority(t *testing.T) {
	g := newTestRecoveryGate()
	root := t.TempDir()
	var work atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				done, err := g.begin([]string{root})
				if err == nil {
					work.Add(1)
					work.Add(-1)
					done()
				}
				lease, err := g.acquire([]string{root})
				if err == nil {
					if work.Load() != 0 {
						t.Error("work entered while recovery held")
					}
					lease.Release()
				}
			}
		}()
	}
	wg.Wait()
}
func TestRecoveryAggregateSiblingCheckoutsFencesSetAndLatestActivity(t *testing.T) {
	now := time.Date(2026, 1, 8, 0, 0, 0, 0, time.UTC)
	old := now.Add(-worktreereclaim.RecoveryIdlePeriod)
	a := worktreereclaim.RecoveryActivity{Generation: 2, ObservationID: "first", ObservedSince: old, LastActivity: old, ObservedThrough: now, Complete: true}
	b := a
	b.ObservationID = "second"
	b.Generation = 3
	combined := aggregateRecoveryActivity([]string{"/a", "/b"}, []worktreereclaim.RecoveryActivity{a, b})
	if !worktreereclaim.RecoveryInactive(combined, now) || combined.Generation != 5 {
		t.Fatal("complete sibling workspace evidence should qualify")
	}
	b.LastActivity = now
	changed := aggregateRecoveryActivity([]string{"/a", "/b"}, []worktreereclaim.RecoveryActivity{a, b})
	if worktreereclaim.RecoveryInactive(changed, now) {
		t.Fatal("one checkout edit resets whole workspace age")
	}
	b.ObservationID = "restart"
	restarted := aggregateRecoveryActivity([]string{"/a", "/b"}, []worktreereclaim.RecoveryActivity{a, b})
	if restarted.ObservationID == combined.ObservationID {
		t.Fatal("one checkout restart must fence workspace")
	}
	removed := aggregateRecoveryActivity([]string{"/a"}, []worktreereclaim.RecoveryActivity{a})
	if removed.ObservationID == combined.ObservationID {
		t.Fatal("checkout set must be fenced")
	}
	b.Complete = false
	if aggregateRecoveryActivity([]string{"/a", "/b"}, []worktreereclaim.RecoveryActivity{a, b}).Complete {
		t.Fatal("unknown checkout holds group")
	}
}

func TestRecoveryAdmissionOnlyExactLifecycleExemptions(t *testing.T) {
	for _, name := range []string{"worktree.recovery_finalize", "worktree.reconcile"} {
		if !recoveryLifecycleCommand(name) {
			t.Fatal(name)
		}
	}
	for _, name := range []string{"git.recovery_publish", "git.recovery_verify", "git.recovery_delete", "worktree.recovery_observe", "worktree.recovery_capture", "worktree.recovery_restore", "git.recovery_publish.evil", "worktree.recovery_finalize_extra", "exec.run", "worktree.delete_directory"} {
		if recoveryLifecycleCommand(name) {
			t.Fatal("unexpected exemption", name)
		}
	}
}
