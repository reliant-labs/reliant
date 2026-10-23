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
	"strconv"
	"strings"
	"testing"
	"time"
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

// tripwireOwnerFile records the pid of the process that made a tripwire
// directory, so a later run can tell a leftover from a live one.
const tripwireOwnerFile = "owner.pid"

// tripwireOrphanAge is how old a tripwire directory with no owner file (made
// by a forge that predates the file) must be before it is reaped. No run of
// this package lasts that long: its longest budget is 45 minutes.
const tripwireOrphanAge = 6 * time.Hour

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
	if runtime.GOOS == "windows" {
		return func() error { return nil }, nil
	}
	if dir := os.Getenv(tripwireDirEnv); dir != "" && tripwireLeadsPATH(dir, os.Getenv("PATH")) {
		// A helper process: the parent owns the directory and checks it.
		liveTripwireDir = dir
		return func() error { return nil }, nil
	}
	reapStaleTripwires(os.TempDir(), time.Now(), processAlive) // procgroup_*.go
	dir, err := os.MkdirTemp("", "forge-cli-tripwire-")
	if err != nil {
		return nil, err
	}
	registerSharedTempDir(dir)
	if err := os.WriteFile(filepath.Join(dir, tripwireOwnerFile), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
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

// reapStaleTripwires removes the forge-cli-tripwire-* directories in root
// whose owner is gone: an owner.pid naming a process that is not running, or,
// for a directory from a forge that wrote no owner file, an age past
// tripwireOrphanAge. A directory whose owner is alive, or whose owner cannot be
// read, is left alone: deleting a live suite's tripwire is the failure this
// exists to prevent.
func reapStaleTripwires(root string, now time.Time, alive func(pid int) bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "forge-cli-tripwire-") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, tripwireOwnerFile))
		switch {
		case err == nil:
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if convErr != nil || pid <= 0 || pid == os.Getpid() || alive(pid) {
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
	root := t.TempDir()
	now := time.Now()
	mk := func(name, owner string, age time.Duration) string {
		t.Helper()
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if owner != "" {
			if err := os.WriteFile(filepath.Join(dir, tripwireOwnerFile), []byte(owner), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		at := now.Add(-age)
		if err := os.Chtimes(dir, at, at); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	alive := func(pid int) bool { return pid == 100 }
	gone := []string{
		mk("forge-cli-tripwire-1", "200", 0),                        // owner exited
		mk("forge-cli-tripwire-2", "", tripwireOrphanAge+time.Hour), // pre-owner-file leftover, old
	}
	kept := []string{
		mk("forge-cli-tripwire-3", "100", 48*time.Hour),          // owner alive, however old
		mk("forge-cli-tripwire-4", strconv.Itoa(os.Getpid()), 0), // this process
		mk("forge-cli-tripwire-5", "", time.Minute),              // no owner file, recent: maybe a live old-forge suite
		mk("forge-cli-tripwire-6", "not-a-pid", 48*time.Hour),    // unreadable owner
		mk("forge-cli-other-1", "200", 48*time.Hour),             // not a tripwire
		mk("unrelated", "", 48*time.Hour),
	}
	reapStaleTripwires(root, now, alive)
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
}
