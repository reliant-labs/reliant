//go:build unix

package selfupdate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// swapped performs a real swap to 2.0.0 (with a fake exec) and returns what
// Boot needs to see it.
func swapped(t *testing.T, confirmWithin time.Duration) (u *Updater, exe string, oldBin, newBin []byte) {
	t.Helper()
	u, s, exe, oldBin, newBin := stagedUpdater(t, func(c *Config) { c.Shutdown = func() {}; c.ConfirmWithin = confirmWithin })
	if err := u.Request(context.Background(), s, Decision{Action: Apply}); err != nil {
		t.Fatal(err)
	}
	u.execFn = func(string, []string, []string) error { return nil }
	if err := u.ExecPending(); err != nil {
		t.Fatal(err)
	}
	return u, exe, oldBin, newBin
}

// execRecorder stands in for exec(2).
type execRecorder struct {
	mu    sync.Mutex
	paths []string
	ch    chan string
}

func newExecRecorder() *execRecorder { return &execRecorder{ch: make(chan string, 4)} }

func (r *execRecorder) exec(path string, _, _ []string) error {
	r.mu.Lock()
	r.paths = append(r.paths, path)
	r.mu.Unlock()
	r.ch <- path
	return nil
}

func (r *execRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.paths)
}

func bootAs(u *Updater, exe string, pid int, rec *execRecorder, alive func(int) bool) *Pending {
	return boot(BootOptions{StateDir: u.cfg.StateDir, Logger: testkit.DiscardLogger()}, bootEnv{exe: exe, pid: pid, exec: rec.exec, alive: alive})
}

func allAlive(int) bool { return true }

func TestBootWithNoSwapIsInert(t *testing.T) {
	t.Parallel()
	rec := newExecRecorder()
	p := boot(BootOptions{StateDir: t.TempDir(), Logger: testkit.DiscardLogger()}, bootEnv{exe: installed(t, script("echo 1")), pid: 1, exec: rec.exec, alive: allAlive})
	if p.Swapped || p.Reverted != "" {
		t.Fatalf("p = %+v", p)
	}
	if err := p.Confirm(); err != nil {
		t.Fatal(err)
	}
	var nilP *Pending
	if err := nilP.Confirm(); err != nil {
		t.Fatal(err)
	}
}

func TestBootThenConfirm(t *testing.T) {
	t.Parallel()
	u, exe, _, _ := swapped(t, time.Minute)
	rec := newExecRecorder()
	p := bootAs(u, exe, 4242, rec, allAlive)
	if !p.Swapped || p.From != "1.0.0" || p.To != "2.0.0" {
		t.Fatalf("p = %+v, want an unconfirmed 1.0.0 -> 2.0.0", p)
	}
	st := readJSONState(t, u.cfg.StateDir)
	if len(st.Swap.Boots) != 1 || st.Swap.Boots[0].PID != 4242 {
		t.Fatalf("boots = %+v", st.Swap.Boots)
	}
	if err := p.Confirm(); err != nil {
		t.Fatal(err)
	}
	st = readJSONState(t, u.cfg.StateDir)
	if st.Swap.Phase != phaseConfirmed {
		t.Fatalf("phase = %s, want confirmed", st.Swap.Phase)
	}
	if _, err := os.Stat(filepath.Join(u.cfg.StateDir, "staged", "2.0.0")); !os.IsNotExist(err) {
		t.Error("the staged release outlived its confirmation")
	}
	if rec.count() != 0 {
		t.Fatal("a confirmed binary must not be reverted")
	}
	// A later start of the confirmed binary is inert.
	if p2 := bootAs(u, exe, 4343, rec, allAlive); p2.Swapped {
		t.Fatalf("a confirmed swap booted as pending: %+v", p2)
	}
}

func TestWatchdogRevertsAnUnconfirmedBinary(t *testing.T) {
	t.Parallel()
	u, exe, oldBin, _ := swapped(t, 50*time.Millisecond)
	rec := newExecRecorder()
	p := bootAs(u, exe, 4242, rec, allAlive)
	if !p.Swapped {
		t.Fatal("want a pending swap")
	}
	path := testkit.Receive(t, "the watchdog's exec of the previous binary", rec.ch)
	if resolved, _ := filepath.EvalSymlinks(exe); path != resolved {
		t.Fatalf("exec'd %s, want %s", path, resolved)
	}
	if got, _ := os.ReadFile(exe); !bytes.Equal(got, oldBin) {
		t.Fatal("the previous binary is not back in place")
	}
	st := readJSONState(t, u.cfg.StateDir)
	if st.Swap.Phase != phaseReverted || !strings.Contains(st.Swap.Reason, "did not confirm") {
		t.Fatalf("swap = %+v", st.Swap)
	}
	if _, failed := st.failed("2.0.0"); !failed {
		t.Fatal("the reverted version must be recorded as failed")
	}
	if err := p.Confirm(); err == nil {
		t.Fatal("Confirm after the watchdog fired must say so")
	}
	// The previous binary's next start reports the revert.
	if p2 := bootAs(u, exe, 4242, rec, allAlive); p2.Swapped || p2.Reverted != "2.0.0" {
		t.Fatalf("p2 = %+v, want Reverted 2.0.0", p2)
	}
	// And Check never offers 2.0.0 again.
	u.cfg.Policy = &countingPolicy{d: Decision{Action: Apply}}
	if _, d, err := u.Check(context.Background()); err != nil || d.Action != Skip {
		t.Fatalf("Check after a revert: %+v %v", d, err)
	}
}

// holdLock takes the state lock as another instance would, so Confirm cannot.
func holdLock(t *testing.T, dir string) *flock.Flock {
	t.Helper()
	l := flock.New(filepath.Join(dir, "lock"))
	if err := l.Lock(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Unlock() })
	return l
}

func TestAFailedConfirmCanBeRetried(t *testing.T) {
	t.Parallel()
	u, exe, _, _ := swapped(t, time.Hour)
	rec := newExecRecorder()
	p := bootAs(u, exe, 4242, rec, allAlive)
	p.lockWait = 50 * time.Millisecond
	l := holdLock(t, u.cfg.StateDir)
	if err := p.Confirm(); err == nil {
		t.Fatal("Confirm must report that it could not record the confirmation")
	}
	if st := readJSONState(t, u.cfg.StateDir); st.Swap.Phase != phaseSwapped {
		t.Fatalf("phase = %s, want still swapped", st.Swap.Phase)
	}
	if err := l.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := p.Confirm(); err != nil {
		t.Fatalf("a retried Confirm: %v", err)
	}
	if st := readJSONState(t, u.cfg.StateDir); st.Swap.Phase != phaseConfirmed {
		t.Fatalf("phase = %s, want confirmed", st.Swap.Phase)
	}
	if rec.count() != 0 {
		t.Fatal("a confirmed binary must not be reverted")
	}
}

func TestAFailedConfirmLeavesTheWatchdogArmed(t *testing.T) {
	t.Parallel()
	u, exe, oldBin, _ := swapped(t, time.Hour)
	rec := newExecRecorder()
	p := bootAs(u, exe, 4242, rec, allAlive)
	p.lockWait = 50 * time.Millisecond
	l := holdLock(t, u.cfg.StateDir)
	p.deadline = time.Now() // the deadline passes while Confirm is failing
	if err := p.Confirm(); err == nil {
		t.Fatal("Confirm must report that it could not record the confirmation")
	}
	// The re-armed watchdog waits for the lock (revert's own 30s bound).
	if err := l.Unlock(); err != nil {
		t.Fatal(err)
	}
	testkit.Receive(t, "the re-armed watchdog's exec of the previous binary", rec.ch)
	if got, _ := os.ReadFile(exe); !bytes.Equal(got, oldBin) {
		t.Fatal("the previous binary is not back in place")
	}
	if st := readJSONState(t, u.cfg.StateDir); st.Swap.Phase != phaseReverted {
		t.Fatalf("phase = %s, want reverted", st.Swap.Phase)
	}
}

func TestBootLoopGuard(t *testing.T) {
	t.Parallel()
	u, exe, oldBin, newBin := swapped(t, time.Hour)
	rec := newExecRecorder()
	dead := map[int]bool{}
	alive := func(pid int) bool { return !dead[pid] }

	// First start, then it dies without confirming.
	p1 := bootAs(u, exe, 100, rec, alive)
	p1.timer.Stop()
	dead[100] = true
	// A concurrent, live instance does not count as a crash.
	p2 := bootAs(u, exe, 200, rec, alive)
	p2.timer.Stop()
	if !p2.Swapped || rec.count() != 0 {
		t.Fatal("one crash must not revert yet")
	}
	// Second death.
	dead[200] = true
	p3 := bootAs(u, exe, 300, rec, alive)
	if p3.Swapped || rec.count() != 1 {
		t.Fatalf("two unconfirmed crashes must revert at the next start: p3=%+v execs=%d", p3, rec.count())
	}
	if got, _ := os.ReadFile(exe); !bytes.Equal(got, oldBin) || bytes.Equal(got, newBin) {
		t.Fatal("the boot-loop guard did not put the previous binary back")
	}
	st := readJSONState(t, u.cfg.StateDir)
	if _, failed := st.failed("2.0.0"); !failed || !strings.Contains(st.Swap.Reason, "without confirming") {
		t.Fatalf("state = %+v", st)
	}
}

func TestBootIgnoresAnotherBinaryAndNoticesAReplacement(t *testing.T) {
	t.Parallel()
	u, exe, _, _ := swapped(t, time.Hour)
	rec := newExecRecorder()

	other := installed(t, script("echo other"))
	if p := bootAs(u, other, 1, rec, allAlive); p.Swapped {
		t.Fatal("a different binary sharing the state dir must not adopt the swap")
	}

	// Someone reinstalled the binary by hand after the swap.
	if err := replaceWithCopy(other, exe); err != nil {
		t.Fatal(err)
	}
	if p := bootAs(u, exe, 1, rec, allAlive); p.Swapped || rec.count() != 0 {
		t.Fatal("a replaced binary must be neither confirmed nor reverted")
	}
	if st := readJSONState(t, u.cfg.StateDir); st.Swap.Phase != phaseSuperseded {
		t.Fatalf("phase = %s, want superseded", st.Swap.Phase)
	}
}

func TestRevertDefersToAnotherInstancesConfirmation(t *testing.T) {
	t.Parallel()
	u, exe, _, newBin := swapped(t, time.Hour)
	rec := newExecRecorder()
	a := bootAs(u, exe, 1, rec, allAlive)
	a.timer.Stop()
	b := bootAs(u, exe, 2, rec, allAlive)
	b.timer.Stop()
	if err := b.Confirm(); err != nil {
		t.Fatal(err)
	}
	a.watchdog() // a's deadline passes after b confirmed
	if rec.count() != 0 {
		t.Fatal("a binary another instance confirmed must not be reverted")
	}
	if got, _ := os.ReadFile(exe); !bytes.Equal(got, newBin) {
		t.Fatal("the confirmed binary was replaced")
	}
}
