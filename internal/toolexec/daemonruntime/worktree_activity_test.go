package daemonruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

func newTestRecoveryGate() *recoveryAdmission {
	return &recoveryAdmission{work: map[uint64]admissionScope{}, leases: map[uint64]*WorktreeRecoveryLease{}}
}

// managedRegistry stands in for the background-process and terminal
// registries: what managedWorkSnapshot would report right now.
type managedRegistry struct {
	mu   sync.Mutex
	work []managedWork
}

func (m *managedRegistry) set(work ...managedWork) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.work = work
}

func (m *managedRegistry) use(leased []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return managedUseIn(leased, m.work)
}

// gateWithRegistry is a gate whose managed-use check is the production one
// over an injected registry, and whose process inspection finds nothing.
func gateWithRegistry() (*recoveryAdmission, *managedRegistry) {
	g, reg := newTestRecoveryGate(), &managedRegistry{}
	g.managedUse = reg.use
	g.processUse = func(context.Context, []string) error { return nil }
	return g, reg
}

// The owner decision of 2026-11-08 (bug c4a30cf1): on an agent machine some
// background process, terminal and tool call is always open, and holding
// every archived worktree for them meant none ever freed its disk. Work that
// runs anywhere other than inside the worktree does not hold it, including
// work started in an ancestor of it ($HOME).
func TestRecoveryAdmissionBusyMachineUnrelatedWorkDoesNotHold(t *testing.T) {
	g, reg := gateWithRegistry()
	home := t.TempDir()
	workspace := filepath.Join(home, ".reliant", "worktrees", "archived")
	sibling := filepath.Join(home, ".reliant", "worktrees", "live")
	elsewhere := t.TempDir()
	for _, dir := range []string{workspace, sibling} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	reg.set(
		managedWork{kind: "background process", dir: sibling},
		managedWork{kind: "background process", dir: elsewhere},
		managedWork{kind: "terminal session", dir: home},
		managedWork{kind: "terminal session", dir: filepath.Join(sibling, "sub")},
	)
	for _, dir := range []string{sibling, home, elsewhere} {
		done, err := g.beginIn(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer done()
	}
	lease, err := g.acquire([]string{workspace})
	if err != nil {
		t.Fatalf("unrelated work on the machine held the worktree: %v", err)
	}
	defer lease.Release()
	if err := lease.Check(context.Background()); err != nil {
		t.Fatalf("unrelated work on the machine held the worktree: %v", err)
	}
	// And the reverse: a worktree being retired refuses only work inside it.
	for _, dir := range []string{sibling, home, elsewhere} {
		done, err := g.beginIn(dir)
		if err != nil {
			t.Fatalf("work in %s refused while an unrelated worktree retires: %v", dir, err)
		}
		done()
	}
	for _, dir := range []string{workspace, filepath.Join(workspace, "sub", "dir")} {
		if done, err := g.beginIn(dir); err == nil {
			done()
			t.Fatalf("work in %s admitted into a worktree being retired", dir)
		}
	}
}

func TestRecoveryAdmissionManagedWorkStartedInWorktreeHolds(t *testing.T) {
	for _, kind := range []string{"background process", "terminal session"} {
		t.Run(kind, func(t *testing.T) {
			g, reg := gateWithRegistry()
			workspace := t.TempDir()
			for _, dir := range []string{workspace, filepath.Join(workspace, "frontends", "web")} {
				reg.set(managedWork{kind: "background process", dir: t.TempDir()}, managedWork{kind: kind, dir: dir})
				if lease, err := g.acquire([]string{workspace}); err == nil {
					lease.Release()
					t.Fatalf("a %s started in %s did not hold the worktree", kind, dir)
				}
			}
			// The registry, not the start command, carries the veto: once the
			// process exits or the session closes, the worktree is free.
			reg.set(managedWork{kind: "background process", dir: t.TempDir()})
			lease, err := g.acquire([]string{workspace})
			if err != nil {
				t.Fatal("completion must release the hold:", err)
			}
			defer lease.Release()
			// One that appears after admission is seen by the next Check.
			reg.set(managedWork{kind: kind, dir: workspace})
			if err := lease.Check(context.Background()); err == nil {
				t.Fatalf("a %s started after admission was not seen before removal", kind)
			}
		})
	}
}

// A managed process whose working directory cannot be placed may be running in
// the worktree, so it holds it.
func TestRecoveryAdmissionManagedWorkWithUnknownDirHolds(t *testing.T) {
	workspace := t.TempDir()
	// A name longer than NAME_MAX: resolving it fails with ENAMETOOLONG.
	unresolvable := filepath.Join(t.TempDir(), strings.Repeat("a", 300), "shell")
	if err := managedUseIn([]string{workspace}, []managedWork{{kind: "terminal session", dir: unresolvable}}); err == nil {
		t.Fatal("a session whose directory cannot be resolved did not hold the worktree")
	}
}

// A live process with its cwd, an open file or a mapping under the worktree
// holds it, and so does inspection that cannot run: an unreadable /proc is
// never read as "unused".
func TestRecoveryAdmissionProcessInspectionHoldsAndFailsClosed(t *testing.T) {
	for name, inspectErr := range map[string]error{
		"process with cwd in the worktree": errors.New("process 123 has its working directory in the workspace"),
		"unreadable /proc":                 errors.New("process enumeration unavailable: permission denied"),
	} {
		t.Run(name, func(t *testing.T) {
			g, _ := gateWithRegistry()
			// The gate canonicalizes what it is given (macOS: /var is /private/var).
			workspace, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			var seen []string
			g.processUse = func(_ context.Context, paths []string) error { seen = paths; return inspectErr }
			lease, err := g.acquire([]string{workspace})
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			if err := lease.Check(context.Background()); err == nil {
				t.Fatal("removal authorized despite", inspectErr)
			}
			if len(seen) != 1 || !admissionWithin(seen[0], workspace) {
				t.Fatalf("inspection was not scoped to the worktree: %v", seen)
			}
		})
	}
}

func TestRecoveryAdmissionFreshCheckRetainsManagedVeto(t *testing.T) {
	g := newTestRecoveryGate()
	var running atomic.Bool
	g.managedUse = func([]string) error {
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
	// removal.
	running.Store(true)
	if err := lease.Check(context.Background()); err != errRecoveryAdmission {
		t.Fatalf("fresh lifetime veto missing: %v", err)
	}
}

func TestRecoveryCommandExecAndTerminalAreScopedByTheirHandlers(t *testing.T) {
	for _, name := range []string{"exec.run", "exec.bg_start", "terminal.create"} {
		if !selfAdmittedCommand(name) {
			t.Fatalf("%s would be admitted with unknown scope by the dispatcher", name)
		}
	}
	for _, name := range []string{"exec.bg_list", "terminal.close", "fs.read_file", "worktree.recovery_finalize", "exec.run.evil"} {
		if selfAdmittedCommand(name) {
			t.Fatal("unexpected self-admission", name)
		}
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
