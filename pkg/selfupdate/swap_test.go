package selfupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// The Windows swap, tested on every platform: lockingFS models Windows'
// lock on a running image, and the Updater and Boot are handed it in place of
// the host's file operations.

// lockingFS is a [swapFS] in which a running image (a locked path) may be
// renamed, and its lock moves with it, but neither replaced nor removed.
type lockingFS struct {
	mu     sync.Mutex
	locked map[string]bool
	// failRename, when it returns an error, fails that rename first.
	failRename func(oldpath, newpath string) error
}

func newLockingFS(running ...string) *lockingFS {
	l := &lockingFS{locked: map[string]bool{}}
	for _, p := range running {
		l.locked[p] = true
	}
	return l
}

func (l *lockingFS) fs() swapFS {
	return swapFS{rename: l.rename, remove: l.remove, moveAside: true}
}

func (l *lockingFS) rename(oldpath, newpath string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failRename != nil {
		if err := l.failRename(oldpath, newpath); err != nil {
			return err
		}
	}
	if l.locked[newpath] {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: fs.ErrPermission}
	}
	if err := os.Rename(oldpath, newpath); err != nil {
		return err
	}
	if l.locked[oldpath] {
		delete(l.locked, oldpath)
		l.locked[newpath] = true
	}
	return nil
}

func (l *lockingFS) remove(path string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.locked[path] {
		return &os.PathError{Op: "remove", Path: path, Err: fs.ErrPermission}
	}
	return os.Remove(path)
}

// run marks path as a running image; exit releases every lock, as a process
// exiting does.
func (l *lockingFS) run(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.locked[path] = true
}

func (l *lockingFS) exitAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	clear(l.locked)
}

// asides lists the images set aside beside exe.
func asides(t *testing.T, exe string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(exe))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if isAsideOf(filepath.Base(exe), e.Name()) {
			out = append(out, filepath.Join(filepath.Dir(exe), e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// windowsSwap arms a swap of exe (holding oldBin, a running image in lfs) to
// newBin, with no release server: the staged binary is written directly.
func windowsSwap(t *testing.T, lfs *lockingFS, confirmWithin time.Duration) (u *Updater, exe string, oldBin, newBin []byte) {
	t.Helper()
	oldBin, newBin = []byte("binary 1.0.0"), []byte("binary 2.0.0")
	exe = installed(t, oldBin)
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	lfs.run(exe)
	u = newTestUpdater(t, exe, nil, func(c *Config) { c.Shutdown = func() {}; c.ConfirmWithin = confirmWithin })
	u.fs = lfs.fs()
	u.now = func() time.Time { return fixedSwapTime }
	dir := filepath.Join(u.cfg.StateDir, "staged", "2.0.0")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, "app")
	if err := os.WriteFile(staged, newBin, 0o755); err != nil {
		t.Fatal(err)
	}
	s := Staged{Release: Release{Version: "2.0.0", Tag: "v2.0.0"}, Path: staged, SHA256: sumHex(newBin)}
	if err := u.Request(context.Background(), s, Decision{Action: Apply}); err != nil {
		t.Fatal(err)
	}
	return u, exe, oldBin, newBin
}

var fixedSwapTime = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestWindowsSwapMovesTheRunningImageAside(t *testing.T) {
	t.Parallel()
	lfs := newLockingFS()
	u, exe, oldBin, newBin := windowsSwap(t, lfs, time.Hour)
	rec := newExecRecorder()
	u.execFn = rec.exec
	if err := u.ExecPending(); err != nil {
		t.Fatal(err)
	}
	if got := testkit.Receive(t, "the start of the new binary", rec.ch); got != exe {
		t.Fatalf("started %q, want the install %q", got, exe)
	}
	if !bytes.Equal(readFile(t, exe), newBin) {
		t.Fatal("the install does not hold the new binary")
	}
	// The old process still runs its image, now under a new name, which
	// could not be removed yet.
	aside := asides(t, exe)
	if want := asideName(exe, fixedSwapTime.UnixNano()); len(aside) != 1 || aside[0] != want {
		t.Fatalf("asides = %v, want [%s]", aside, want)
	}
	if !bytes.Equal(readFile(t, aside[0]), oldBin) {
		t.Fatal("the image set aside is not the old binary")
	}
	st := readJSONState(t, u.cfg.StateDir)
	if st.Swap == nil || st.Swap.Phase != phaseSwapped || st.Swap.Path != exe {
		t.Fatalf("swap = %+v", st.Swap)
	}
	if _, err := os.Stat(journalPath(u.cfg.StateDir)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a completed move left its journal: %v", err)
	}

	// The old process exited; the new one starts, sweeps the image set aside
	// and confirms.
	lfs.exitAll()
	lfs.run(exe)
	p := boot(BootOptions{StateDir: u.cfg.StateDir, Logger: testkit.DiscardLogger()},
		bootEnv{exe: exe, pid: 2, exec: rec.exec, alive: allAlive, fs: lfs.fs()})
	if !p.Swapped || p.To != "2.0.0" {
		t.Fatalf("p = %+v, want a pending 2.0.0", p)
	}
	if left := asides(t, exe); len(left) != 0 {
		t.Fatalf("the next start left %v", left)
	}
	if err := p.Confirm(); err != nil {
		t.Fatal(err)
	}
	if st := readJSONState(t, u.cfg.StateDir); st.Swap.Phase != phaseConfirmed {
		t.Fatalf("phase = %s", st.Swap.Phase)
	}
}

func TestWindowsConfirmSweepsAnImageTheBootFoundStillRunning(t *testing.T) {
	t.Parallel()
	lfs := newLockingFS()
	u, exe, _, _ := windowsSwap(t, lfs, time.Hour)
	rec := newExecRecorder()
	u.execFn = rec.exec
	if err := u.ExecPending(); err != nil {
		t.Fatal(err)
	}
	// The new process boots before the old one has exited.
	p := boot(BootOptions{StateDir: u.cfg.StateDir, Logger: testkit.DiscardLogger()},
		bootEnv{exe: exe, pid: 2, exec: rec.exec, alive: allAlive, fs: lfs.fs()})
	p.timer.Stop()
	if len(asides(t, exe)) != 1 {
		t.Fatal("a running image set aside must survive the sweep")
	}
	lfs.exitAll()
	if err := p.Confirm(); err != nil {
		t.Fatal(err)
	}
	if left := asides(t, exe); len(left) != 0 {
		t.Fatalf("Confirm left %v", left)
	}
}

func TestWindowsWatchdogRevertGoesThroughTheSameRename(t *testing.T) {
	t.Parallel()
	lfs := newLockingFS()
	u, exe, oldBin, newBin := windowsSwap(t, lfs, 50*time.Millisecond)
	rec := newExecRecorder()
	u.execFn = rec.exec
	if err := u.ExecPending(); err != nil {
		t.Fatal(err)
	}
	testkit.Receive(t, "the start of the new binary", rec.ch)
	// The new binary runs, and never confirms.
	lfs.exitAll()
	lfs.run(exe)
	p := boot(BootOptions{StateDir: u.cfg.StateDir, Logger: testkit.DiscardLogger()},
		bootEnv{exe: exe, pid: 2, exec: rec.exec, alive: allAlive, fs: lfs.fs()})
	if !p.Swapped {
		t.Fatal("want a pending swap")
	}
	if got := testkit.Receive(t, "the watchdog's start of the previous binary", rec.ch); got != exe {
		t.Fatalf("started %q, want %q", got, exe)
	}
	if !bytes.Equal(readFile(t, exe), oldBin) {
		t.Fatal("the previous binary is not back in place")
	}
	aside := asides(t, exe)
	if len(aside) != 1 || !bytes.Equal(readFile(t, aside[0]), newBin) {
		t.Fatalf("asides = %v, want the running new binary set aside", aside)
	}
	st := readJSONState(t, u.cfg.StateDir)
	if _, failed := st.failed("2.0.0"); !failed || st.Swap.Phase != phaseReverted {
		t.Fatalf("state = %+v", st)
	}
}

func TestWindowsBootLoopGuardRevertsThroughTheSameRename(t *testing.T) {
	t.Parallel()
	lfs := newLockingFS()
	u, exe, oldBin, _ := windowsSwap(t, lfs, time.Hour)
	rec := newExecRecorder()
	u.execFn = rec.exec
	if err := u.ExecPending(); err != nil {
		t.Fatal(err)
	}
	testkit.Receive(t, "the start of the new binary", rec.ch)
	dead := map[int]bool{}
	alive := func(pid int) bool { return !dead[pid] }
	bootAt := func(pid int) *Pending {
		return boot(BootOptions{StateDir: u.cfg.StateDir, Logger: testkit.DiscardLogger()},
			bootEnv{exe: exe, pid: pid, exec: rec.exec, alive: alive, fs: lfs.fs()})
	}
	// Two starts of the new binary die unconfirmed; a new pid each time, as a
	// Windows start always has.
	for _, pid := range []int{10, 11} {
		lfs.exitAll()
		lfs.run(exe)
		bootAt(pid).timer.Stop()
		dead[pid] = true
	}
	lfs.exitAll()
	lfs.run(exe)
	if p := bootAt(12); p.Swapped {
		t.Fatalf("two unconfirmed crashes must revert at the next start: %+v", p)
	}
	testkit.Receive(t, "the guard's start of the previous binary", rec.ch)
	if !bytes.Equal(readFile(t, exe), oldBin) {
		t.Fatal("the boot-loop guard did not put the previous binary back")
	}
}

func TestWindowsFailedStartPutsTheOldBinaryBack(t *testing.T) {
	t.Parallel()
	lfs := newLockingFS()
	u, exe, oldBin, _ := windowsSwap(t, lfs, time.Hour)
	u.execFn = func(string, []string, []string) error { return errors.New("not a valid Win32 application") }
	if err := u.ExecPending(); err == nil || !strings.Contains(err.Error(), "Win32") {
		t.Fatalf("err = %v", err)
	}
	if !bytes.Equal(readFile(t, exe), oldBin) {
		t.Fatal("a failed start must put the old binary back")
	}
	// The new binary, never run, was removed; the old image still runs under
	// its aside name.
	if aside := asides(t, exe); len(aside) != 1 || !bytes.Equal(readFile(t, aside[0]), oldBin) {
		t.Fatalf("asides = %v", aside)
	}
	st := readJSONState(t, u.cfg.StateDir)
	if _, failed := st.failed("2.0.0"); !failed || st.Swap.Phase != phaseReverted {
		t.Fatalf("state = %+v", st)
	}
}

func TestWindowsSwapNeverLeavesTheInstallPathEmpty(t *testing.T) {
	t.Parallel()
	lfs := newLockingFS()
	u, exe, oldBin, _ := windowsSwap(t, lfs, time.Hour)
	lfs.failRename = func(oldpath, newpath string) error {
		if newpath == exe && strings.Contains(filepath.Base(oldpath), ".new-") {
			return errors.New("disk full")
		}
		return nil
	}
	u.execFn = func(string, []string, []string) error { t.Error("a failed swap must not start anything"); return nil }
	if err := u.ExecPending(); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v", err)
	}
	if !bytes.Equal(readFile(t, exe), oldBin) {
		t.Fatal("the running image was not renamed back")
	}
	if aside := asides(t, exe); len(aside) != 0 {
		t.Fatalf("asides = %v", aside)
	}
	if entries, _ := os.ReadDir(filepath.Dir(exe)); len(entries) != 1 {
		t.Fatalf("temp files left beside the install: %v", entries)
	}
}

func TestSetAsideNeverReplacesAnEarlierImage(t *testing.T) {
	t.Parallel()
	exe := installed(t, []byte("current"))
	earlier := asideName(exe, fixedSwapTime.UnixNano())
	if err := os.WriteFile(earlier, []byte("still running"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := newLockingFS().fs().setAside(exe, fixedSwapTime, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got == earlier || got != asideName(exe, fixedSwapTime.UnixNano()+1) {
		t.Fatalf("set aside at %s", got)
	}
	if !bytes.Equal(readFile(t, earlier), []byte("still running")) || !bytes.Equal(readFile(t, got), []byte("current")) {
		t.Fatal("an earlier image was overwritten")
	}
	if got, err := newLockingFS().fs().setAside(exe, fixedSwapTime, nil); err != nil || got != "" {
		t.Fatalf("a missing file: %q %v, want nothing set aside", got, err)
	}
}

func TestAsideNames(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("d", "bin")
	if got, want := asideName(filepath.Join(dir, "reliant.exe"), 42), filepath.Join(dir, "reliant.old-42.exe"); got != want {
		t.Fatalf("asideName = %s, want %s", got, want)
	}
	if got, want := asideName(filepath.Join(dir, "reliant"), 42), filepath.Join(dir, "reliant.old-42"); got != want {
		t.Fatalf("asideName = %s, want %s", got, want)
	}
	for _, tc := range []struct {
		base, name string
		want       bool
	}{
		{"reliant.exe", "reliant.old-42.exe", true},
		{"reliant", "reliant.old-42", true},
		{"reliant.exe", "reliant.exe", false},
		{"reliant.exe", "reliant.old-.exe", false},
		{"reliant.exe", "reliant.old-4x2.exe", false},
		{"reliant.exe", "reliant.old-42", false},
		{"reliant.exe", "other.old-42.exe", false},
		{"reliant.exe", "reliant-cli.old-42.exe", false},
	} {
		if got := isAsideOf(tc.base, tc.name); got != tc.want {
			t.Errorf("isAsideOf(%q, %q) = %v, want %v", tc.base, tc.name, got, tc.want)
		}
	}
}

func TestSweepAsideKeepsRunningImagesAndOtherFiles(t *testing.T) {
	t.Parallel()
	exe := installed(t, []byte("current"))
	stale, running := asideName(exe, 1), asideName(exe, 2)
	other := filepath.Join(filepath.Dir(exe), "notes.old-3.txt")
	for _, p := range []string{stale, running, other} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sweepAside(newLockingFS(running).fs(), exe)
	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Error("a stale image set aside was not removed")
	}
	for _, p := range []string{exe, running, other} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	// Where a running image is renamed over (unix), nothing is ever set
	// aside, so nothing is swept.
	sweepAside(swapFS{rename: os.Rename, remove: os.Remove}, exe)
	if _, err := os.Stat(running); err != nil {
		t.Errorf("a sweep without moveAside removed %s", running)
	}
}

func TestRespawnStartsTheSameCommandThenExits(t *testing.T) {
	t.Parallel()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	argv, env := []string{"app", "daemon", "--flag"}, []string{"A=1", "B=2"}
	var got *exec.Cmd
	exited := -1
	err = respawn("/install/app", argv, env, func(c *exec.Cmd) error { got = c; return nil }, func(code int) { exited = code })
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Path != "/install/app" || fmt.Sprint(got.Args) != fmt.Sprint(argv) || fmt.Sprint(got.Env) != fmt.Sprint(env) || got.Dir != wd {
		t.Fatalf("started %+v, want the same argv, environment and working directory", got)
	}
	if got.Stdout != os.Stdout || got.Stderr != os.Stderr || got.Stdin != os.Stdin {
		t.Fatal("the new process must inherit stdio")
	}
	if exited != 0 {
		t.Fatalf("exit(%d) after starting, want exit(0)", exited)
	}

	exited = -1
	err = respawn("/install/app", argv, env, func(*exec.Cmd) error { return errors.New("file not found") }, func(code int) { exited = code })
	if err == nil || !strings.Contains(err.Error(), "file not found") || exited != -1 {
		t.Fatalf("a failed start: err=%v exit=%d, want the error and no exit", err, exited)
	}
}

// errKilled stands in for the process dying mid-move: the fake rename panics
// with it, so nothing after the rename (the in-process restore included)
// runs, exactly as when the process is killed.
var errKilled = errors.New("killed")

// killBeforeInstall makes lfs "kill the process" when the staged copy is
// about to be renamed onto exe, after exe was renamed aside.
func killBeforeInstall(lfs *lockingFS, exe string) {
	lfs.mu.Lock()
	defer lfs.mu.Unlock()
	lfs.failRename = func(oldpath, newpath string) error {
		if newpath == exe && strings.Contains(filepath.Base(oldpath), ".new-") {
			panic(errKilled)
		}
		return nil
	}
}

func (l *lockingFS) revive() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failRename = nil
	clear(l.locked)
}

// dies runs fn, which must be killed by errKilled.
func dies(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != errKilled {
			t.Fatalf("want the process killed mid-move, got %v", r)
		}
	}()
	fn()
}

// leftovers lists the staged copies left beside exe.
func leftovers(t *testing.T, exe string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(exe))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.Contains(e.Name(), ".new-") {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestWindowsSwapKilledBetweenTheRenamesIsRecoveredAtTheNextBoot(t *testing.T) {
	t.Parallel()
	lfs := newLockingFS()
	u, exe, oldBin, _ := windowsSwap(t, lfs, time.Hour)
	u.execFn = func(string, []string, []string) error { t.Error("a killed swap must not start anything"); return nil }
	killBeforeInstall(lfs, exe)
	dies(t, func() { _ = u.ExecPending() })

	// The window: the install path is gone, and only the journal says where
	// its binary went.
	if _, err := os.Lstat(exe); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the fault was not injected between the renames: %v", err)
	}
	aside := asides(t, exe)
	if len(aside) != 1 {
		t.Fatalf("asides = %v", aside)
	}
	var j moveJournal
	if err := json.Unmarshal(readFile(t, journalPath(u.cfg.StateDir)), &j); err != nil || j.Path != exe || j.Aside != aside[0] {
		t.Fatalf("journal = %+v (%v), want %s set aside as %s", j, err, exe, aside[0])
	}

	// The process died, so its locks are gone. The image set aside, started
	// by hand, is the binary that can still run: its Boot puts the install
	// back.
	lfs.revive()
	lfs.run(aside[0])
	p := boot(BootOptions{StateDir: u.cfg.StateDir, Logger: testkit.DiscardLogger()},
		bootEnv{exe: aside[0], pid: 3, exec: newExecRecorder().exec, alive: allAlive, fs: lfs.fs()})
	if p.Swapped || p.Reverted != "" {
		t.Fatalf("p = %+v: an interrupted swap is neither pending nor reverted", p)
	}
	if !bytes.Equal(readFile(t, exe), oldBin) {
		t.Fatal("the install was not restored")
	}
	if _, err := os.Stat(journalPath(u.cfg.StateDir)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the journal outlived the recovery: %v", err)
	}
	if left := leftovers(t, exe); len(left) != 0 {
		t.Fatalf("staged copies left beside the install: %v", left)
	}
	// state.json is written only after a move completes, so it never
	// recorded this swap: the restored binary boots as if none happened.
	if st := readJSONState(t, u.cfg.StateDir); st.Swap != nil {
		t.Fatalf("swap = %+v, want none recorded", st.Swap)
	}
	if p := boot(BootOptions{StateDir: u.cfg.StateDir, Logger: testkit.DiscardLogger()},
		bootEnv{exe: exe, pid: 4, exec: newExecRecorder().exec, alive: allAlive, fs: lfs.fs()}); p.Swapped || p.Reverted != "" {
		t.Fatalf("the restored install booted as %+v", p)
	}
}

func TestWindowsRevertKilledBetweenTheRenamesIsRecoveredByItsAside(t *testing.T) {
	t.Parallel()
	lfs := newLockingFS()
	u, exe, _, newBin := windowsSwap(t, lfs, time.Hour)
	rec := newExecRecorder()
	u.execFn = rec.exec
	if err := u.ExecPending(); err != nil {
		t.Fatal(err)
	}
	lfs.exitAll()
	lfs.run(exe)
	p := boot(BootOptions{StateDir: u.cfg.StateDir, Logger: testkit.DiscardLogger()},
		bootEnv{exe: exe, pid: 2, exec: rec.exec, alive: allAlive, fs: lfs.fs()})
	p.timer.Stop()
	killBeforeInstall(lfs, exe)
	dies(t, func() { p.revert("test: reverting") })
	if _, err := os.Lstat(exe); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the fault was not injected between the renames: %v", err)
	}

	// Start the image set aside, restoring only its own install. An unrelated
	// binary sharing writable state must not authorize journal operations.
	lfs.revive()
	var interrupted moveJournal
	if err := json.Unmarshal(readFile(t, journalPath(u.cfg.StateDir)), &interrupted); err != nil {
		t.Fatal(err)
	}
	other := interrupted.Aside
	boot(BootOptions{StateDir: u.cfg.StateDir, Logger: testkit.DiscardLogger()},
		bootEnv{exe: other, pid: 5, exec: rec.exec, alive: allAlive, fs: lfs.fs()})
	if !bytes.Equal(readFile(t, exe), newBin) {
		t.Fatal("the install was not restored to the binary the revert was replacing")
	}
	// The revert was never recorded, so the swap is still pending and the
	// restored binary's next start re-arms its watchdog.
	if st := readJSONState(t, u.cfg.StateDir); st.Swap.Phase != phaseSwapped {
		t.Fatalf("phase = %s, want swapped", st.Swap.Phase)
	}
	if p := boot(BootOptions{StateDir: u.cfg.StateDir, Logger: testkit.DiscardLogger()},
		bootEnv{exe: exe, pid: 6, exec: rec.exec, alive: allAlive, fs: lfs.fs()}); !p.Swapped {
		t.Fatalf("p = %+v, want the swap still pending", p)
	} else {
		p.timer.Stop()
	}
}

func TestRecoverMove(t *testing.T) {
	t.Parallel()
	t.Run("nothing to recover", func(t *testing.T) {
		t.Parallel()
		if got, err := recoverMove(newLockingFS().fs(), t.TempDir(), installed(t, []byte("running"))); err != nil || got != "" {
			t.Fatalf("%q %v", got, err)
		}
	})
	t.Run("the move completed before the journal was removed", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		exe := installed(t, []byte("new"))
		staged := filepath.Join(filepath.Dir(exe), ".app.new-1")
		aside := asideName(exe, 1)
		for p, b := range map[string]string{staged: "copy", aside: "old"} {
			if err := os.WriteFile(p, []byte(b), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		data, _ := json.Marshal(moveJournal{Path: exe, Aside: aside, Staged: staged})
		if err := os.WriteFile(journalPath(dir), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := recoverMove(newLockingFS().fs(), dir, exe); err != nil || got != "" {
			t.Fatalf("%q %v, want nothing restored", got, err)
		}
		if !bytes.Equal(readFile(t, exe), []byte("new")) {
			t.Fatal("a completed move was undone")
		}
		for _, p := range []string{journalPath(dir), staged} {
			if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("%s survived: %v", p, err)
			}
		}
	})
	t.Run("a torn journal", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		exe := installed(t, []byte("running"))
		if err := os.WriteFile(journalPath(dir), []byte("{\"pa"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := recoverMove(newLockingFS().fs(), dir, exe); err != nil || got != "" {
			t.Fatalf("%q %v", got, err)
		}
		if _, err := os.Stat(journalPath(dir)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal("a torn journal must be dropped")
		}
	})
	t.Run("the image set aside is gone too", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		exe := filepath.Join(t.TempDir(), "app.exe")
		data, _ := json.Marshal(moveJournal{Path: exe, Aside: asideName(exe, 1), Staged: filepath.Join(filepath.Dir(exe), ".app.exe.new-1")})
		if err := os.WriteFile(journalPath(dir), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := recoverMove(newLockingFS().fs(), dir, exe); err == nil {
			t.Fatal("want an error naming what could not be restored")
		}
		if _, err := os.Stat(journalPath(dir)); err != nil {
			t.Fatal("an unrecovered journal must stay for a later start")
		}
	})
}
