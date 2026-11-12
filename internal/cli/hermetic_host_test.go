//go:build !e2e && !integration

package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// hostTools are the binaries whose real invocation reads or mutates state
// shared with the rest of the machine — the kubeconfig, k3d clusters, docker
// builders and images, cloud credentials — or reaches the network. Unit tests
// run on a box where other agents and the developer's own stack are using all
// of it, so none of them may run one for real.
//
// The e2e and integration lanes drive real clusters and registries on purpose,
// which is why this file is excluded from those builds.
var hostTools = []string{"kubectl", "k3d", "docker", "gcloud", "helm", "flux"}

func init() { hostToolTripwireSetup = installHostToolTripwire }

// tripwireDirEnv hands the tripwire directory to every process this test binary
// spawns. A helper process — this binary re-executed by a test with
// -test.run=TestHelper… — runs TestMain too, and reuses the parent's directory
// instead of making its own (see installHostToolTripwire).
const tripwireDirEnv = "FORGE_CLI_TEST_TRIPWIRE_DIR"

// tripwireLockFile is the file whose exclusive OS lock (flock(2); LockFileEx on
// Windows) marks a tripwire directory as owned by a running suite. The suite
// takes it before the file has its name and holds it for its whole life; the
// kernel drops it when the suite exits however it exits. A later run reaps a
// directory only when it can take that lock itself.
//
// Ownership used to be a pid in an owner.pid file, probed for liveness. That
// is wrong on any machine whose sandboxes share /tmp but not a PID namespace:
// another sandbox's live suite has a pid that is "not running" here, so
// concurrent internal/cli suites deleted each other's live directories and
// failed TestHostToolTripwire_ShadowsRealBinaries and the env-parity tests. A
// file lock is held by the kernel, not named by a pid, so it is seen across
// namespaces. No owner.pid is written any more either: a forge that still
// reaps by pid treats a directory without one as legacy and waits out its age
// floor instead of deleting a live suite's directory.
const tripwireLockFile = "owner.lock"

// tripwireOrphanAge is how old a tripwire directory with no lock file (made by
// a forge that predates it, with or without an owner.pid) must be before it is
// reaped. Its owner cannot be proven gone, so only an age no run of this
// package approaches (its longest budget is 45 minutes) makes it safe.
const tripwireOrphanAge = 24 * time.Hour

// tripwireLock holds this suite's tripwire lock open for the life of the
// process. It must stay reachable: a collected *os.File is closed by its
// finalizer, which would release the lock mid-run.
var tripwireLock *os.File

// liveTripwireDir is the directory this process's tripwire runs from.
var liveTripwireDir string

// installHostToolTripwire puts a directory of stand-ins for hostTools at the
// front of PATH for the whole test process and everything it spawns. Each
// stand-in records its argv and exits 97, so a test that reaches a real tool
// gets a failure instead of a side effect, and the returned check fails the
// suite even when the code under test swallowed that failure.
//
// Fixing a hit means giving the call site a seam with a hermetic default in
// main_test.go's TestMain (as listK3dClustersFn, pinKubectlContextFn,
// imagetoolsInspect and buildxAvailable have), not deleting a tool from the
// list. A test that drives a fake tool of its own puts it on PATH ahead of
// these with t.Setenv.
//
// ONE DIRECTORY PER SUITE, NOT PER PROCESS. A helper process inherits the
// parent's PATH, tripwire first, and tripwireDirEnv naming it, so it reuses
// that directory: a host tool it reaches is appended to the parent's calls.log,
// which the parent's check reads, and it creates nothing to remove. Helpers
// used to make a directory of their own, and most of them end by signal or
// os.Exit, so TestMain never removed it: one helper-heavy run left 25 behind,
// one afternoon on a shared box 498. The cleanup that invites — `rm -rf
// /tmp/forge-cli-tripwire-*` — deleted a LIVE suite's directory too, and
// TestHostToolTripwire_ShadowsRealBinaries then failed with every tool
// resolving to the real binary: the "flake" seen in full-mode gates.
// reapStaleTripwires removes the leftovers instead, and never a live one.
//
// Cache-safe: PATH is changed in-process, and `go test` hashes the env vars a
// test reads from its OWN environment, not this one; files outside the module
// are not part of the cache key.
func installHostToolTripwire() (func() error, error) {
	// A k3d create watches its server containers through docker; unit tests
	// see no restarts unless they stub one (the e2e lanes keep the real one).
	k3dServerBootCrashFn = func(context.Context, string) string { return "" }
	// Creating the /dev/kmsg device file runs sudo mknod; unit tests must never.
	addK3dKmsgArgsFn = func(args []string) ([]string, error) { return args, nil }
	if runtime.GOOS == "windows" {
		return func() error { return nil }, nil
	}
	if dir := os.Getenv(tripwireDirEnv); dir != "" && tripwireLeadsPATH(dir, os.Getenv("PATH")) {
		// A helper process: the parent owns the directory and checks it.
		liveTripwireDir = dir
		return func() error { return nil }, nil
	}
	reapStaleTripwires(os.TempDir(), time.Now())
	dir, err := os.MkdirTemp("", "forge-cli-tripwire-")
	if err != nil {
		return nil, err
	}
	registerSharedTempDir(dir)
	if tripwireLock, err = holdTripwireLock(dir); err != nil {
		return nil, err
	}
	calls := filepath.Join(dir, "calls.log")
	for _, tool := range hostTools {
		script := fmt.Sprintf("#!/bin/sh\n"+
			"echo \"%[1]s $*\" >> %[2]q\n"+
			"echo \"forge unit test reached the real %[1]s; seam the call site (see main_test.go TestMain)\" >&2\n"+
			"exit 97\n", tool, calls)
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o755); err != nil {
			return nil, err
		}
	}
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		return nil, err
	}
	if err := os.Setenv(tripwireDirEnv, dir); err != nil {
		return nil, err
	}
	liveTripwireDir = dir
	return func() error {
		data, err := os.ReadFile(calls)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("tests ran real host binaries (intercepted; each exited 97). "+
			"Bisect with -run to find the test, then seam the call site:\n%s", data)
	}, nil
}

// tripwireLeadsPATH reports whether dir is a tripwire directory that still
// exists and is the first PATH entry: only then is the inherited one in force.
// A helper started with a PATH of its own makes its own tripwire.
func tripwireLeadsPATH(dir, path string) bool {
	first, _, _ := strings.Cut(path, string(os.PathListSeparator))
	if first != dir || !isTripwirePath(filepath.Join(dir, hostTools[0])) {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, hostTools[0]))
	return err == nil
}

// holdTripwireLock takes the exclusive lock that marks dir as this suite's and
// returns the file holding it. The lock is taken on a temporary name and only
// then renamed to tripwireLockFile (the lock follows the inode), so no reaper
// can ever find the file under its name and unlocked while its owner is alive.
func holdTripwireLock(dir string) (*os.File, error) {
	f, err := os.CreateTemp(dir, tripwireLockFile+".*")
	if err != nil {
		return nil, err
	}
	locked, err := tryLockFile(f) // lockfile_*.go
	if err == nil && !locked {
		err = errors.New("a fresh lock file is already locked")
	}
	if err == nil {
		err = os.Rename(f.Name(), filepath.Join(dir, tripwireLockFile))
	}
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock tripwire directory %s: %w", dir, err)
	}
	return f, nil
}

// reapStaleTripwires removes the forge-cli-tripwire-* directories in root
// whose owner is gone: a lock file nobody holds (this process takes the lock
// itself to prove it), or, for a directory from a forge that wrote no lock
// file, an age past tripwireOrphanAge. A directory whose lock is held, or
// whose lock file cannot be opened, is left alone: deleting a live suite's
// tripwire is the failure this exists to prevent. This suite's own directory
// is safe too: a second open file description contends for the lock like any
// other process would.
//
// No pid is consulted. A pid names a process only inside one PID namespace,
// and sandboxes sharing this /tmp do not share one.
func reapStaleTripwires(root string, now time.Time) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "forge-cli-tripwire-") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		f, err := os.OpenFile(filepath.Join(dir, tripwireLockFile), os.O_RDWR, 0)
		switch {
		case err == nil:
			locked, lockErr := tryLockFile(f)
			if locked {
				_ = unlockFile(f)
			}
			// Close before removing: Windows cannot delete an open file. A
			// released lock is never retaken (a new suite makes a new
			// directory), so letting go of it first opens no window.
			_ = f.Close()
			if lockErr != nil || !locked {
				continue
			}
		case errors.Is(err, fs.ErrNotExist):
			info, statErr := e.Info()
			if statErr != nil || now.Sub(info.ModTime()) < tripwireOrphanAge {
				continue
			}
		default:
			continue
		}
		_ = os.RemoveAll(dir)
	}
}

// TestHostToolTripwire_ShadowsRealBinaries pins that the tripwire is actually
// in front: if PATH resolution reached a real kubectl/k3d/docker, every
// "hermetic" claim above would be unchecked.
func TestHostToolTripwire_ShadowsRealBinaries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tripwire stand-ins are shell scripts")
	}
	if _, err := os.Stat(liveTripwireDir); err != nil {
		t.Fatalf("this suite's tripwire directory %s is gone (%v): something removed it while the suite ran "+
			"(a hand cleanup of %s/forge-cli-tripwire-*?). Leftovers are reaped at the next run's start; never remove them by hand",
			liveTripwireDir, err, os.TempDir())
	}
	for _, tool := range hostTools {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("LookPath(%s): %v; want the tripwire stand-in", tool, err)
		}
		if !isTripwirePath(path) {
			t.Errorf("%s resolves to %s; want the forge-cli-tripwire stand-in", tool, path)
		}
	}
}

func isTripwirePath(path string) bool {
	matched, _ := filepath.Match("forge-cli-tripwire-*", filepath.Base(filepath.Dir(path)))
	return matched
}

// TestTripwireHelperProbe is the helper process the next test spawns: it
// reports which tripwire directory its TestMain left in force.
func TestTripwireHelperProbe(t *testing.T) {
	if os.Getenv("FORGE_TRIPWIRE_PROBE") != "1" {
		return
	}
	fmt.Printf("TRIPWIRE=%s\n", liveTripwireDir)
}

// A helper process runs under the suite's tripwire and leaves nothing behind;
// one started with a PATH of its own gets a tripwire of its own.
func TestTripwireHelperProcessReusesTheSuiteDirectory(t *testing.T) {
	testkit.Slow(t, "execs the forge binary as a child process")
	if runtime.GOOS == "windows" {
		t.Skip("tripwire stand-ins are shell scripts")
	}
	probe := func(t *testing.T, env ...string) (dir string, made []string) {
		t.Helper()
		tmp := t.TempDir()
		cmd := exec.Command(os.Args[0], "-test.run=^TestTripwireHelperProbe$")
		cmd.Env = append(os.Environ(), append([]string{"FORGE_TRIPWIRE_PROBE=1", "TMPDIR=" + tmp}, env...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("probe: %v\n%s", err, out)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if v, ok := strings.CutPrefix(line, "TRIPWIRE="); ok {
				dir = v
			}
		}
		made, _ = filepath.Glob(filepath.Join(tmp, "forge-cli-tripwire-*"))
		return dir, made
	}

	dir, made := probe(t)
	if dir != liveTripwireDir || len(made) != 0 {
		t.Fatalf("helper ran under %q and made %v; want the suite's %s and nothing new", dir, made, liveTripwireDir)
	}

	own, _ := probe(t, "PATH="+strings.SplitN(os.Getenv("PATH"), string(os.PathListSeparator), 2)[1])
	if own == "" || own == liveTripwireDir || !isTripwirePath(filepath.Join(own, "kubectl")) {
		t.Fatalf("a helper whose PATH lacks the suite's tripwire ran under %q; want a tripwire of its own", own)
	}
}

func TestReapStaleTripwires(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the tripwire, and so its reaper, is not installed on Windows")
	}
	root := t.TempDir()
	now := time.Now()
	// mk makes a tripwire-shaped directory aged by age. lock is "" for a
	// legacy directory with no lock file, "held" for one whose lock another
	// owner holds, and "free" for one whose owner has exited. pid, when set,
	// is written as a legacy owner.pid: one that names no process here, as a
	// suite in another PID namespace's does.
	var held []*os.File
	t.Cleanup(func() {
		for _, f := range held {
			_ = f.Close()
		}
	})
	mk := func(name, lock, pid string, age time.Duration) string {
		t.Helper()
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if pid != "" {
			if err := os.WriteFile(filepath.Join(dir, "owner.pid"), []byte(pid), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		switch lock {
		case "held":
			f, err := holdTripwireLock(dir)
			if err != nil {
				t.Fatal(err)
			}
			held = append(held, f)
		case "free":
			f, err := holdTripwireLock(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
		}
		at := now.Add(-age)
		if err := os.Chtimes(dir, at, at); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	const deadPID = "2147483646" // running nowhere this test can see
	gone := []string{
		mk("forge-cli-tripwire-1", "free", "", 0),                            // owner exited
		mk("forge-cli-tripwire-2", "", "", tripwireOrphanAge+time.Hour),      // pre-lock leftover, old
		mk("forge-cli-tripwire-3", "", deadPID, tripwireOrphanAge+time.Hour), // pid-era leftover, old
	}
	kept := []string{
		mk("forge-cli-tripwire-4", "held", deadPID, 48*time.Hour), // live owner whose pid is foreign here, however old
		mk("forge-cli-tripwire-5", "held", "", 0),                 // live owner
		mk("forge-cli-tripwire-6", "", deadPID, time.Hour),        // pid-era, recent: maybe a live old-forge suite elsewhere
		mk("forge-cli-tripwire-7", "", "", time.Minute),           // no owner record, recent
		mk("forge-cli-other-1", "free", "", 48*time.Hour),         // not a tripwire
		mk("unrelated", "", "", 48*time.Hour),
	}
	reapStaleTripwires(root, now)
	for _, d := range gone {
		if _, err := os.Stat(d); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s survived the reap (%v)", filepath.Base(d), err)
		}
	}
	for _, d := range kept {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s was reaped: %v", filepath.Base(d), err)
		}
	}

	// A held directory goes once its owner lets go.
	for _, f := range held {
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	held = nil
	reapStaleTripwires(root, now)
	for _, d := range kept[:2] {
		if _, err := os.Stat(d); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s survived the reap after its owner released it (%v)", filepath.Base(d), err)
		}
	}
}
