// Copyright (c) 2026 Reliant Labs
//go:build linux

package heavyjob

import (
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This helper holds a real kernel lease until killed by the parent test.
func TestBoundsLeaseHelper(t *testing.T) {
	root := os.Getenv("RELIANT_TEST_BOUNDS_ROOT")
	if root == "" {
		return
	}
	c := lowerBoundsAt(os.Getenv("RELIANT_TEST_BOUNDS_SELF"), root, bounds{weight: 25, memoryHigh: 12 << 30})
	if c.lease == nil {
		t.Fatal("no holder registered")
	}
	_, _ = os.Stdout.Write([]byte("ready\n"))
	_, _ = io.Copy(io.Discard, os.Stdin)
	c.release()
}

func TestBoundsLeasePrunesKilledHolder(t *testing.T) {
	root := t.TempDir()
	dir := boundsCgroup(t, root, cmdCgroup, "100", "max")
	self := selfIn(t, cmdCgroup)
	cmd := exec.Command(os.Args[0], "-test.run=^TestBoundsLeaseHelper$")
	cmd.Env = append(os.Environ(), "RELIANT_TEST_BOUNDS_ROOT="+root, "RELIANT_TEST_BOUNDS_SELF="+self)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	ready := make([]byte, len("ready\n"))
	if _, err := io.ReadFull(out, ready); err != nil || string(ready) != "ready\n" {
		t.Fatalf("helper not ready: %q, %v", ready, err)
	}
	c := lowerBoundsAt(self, root, bounds{weight: 25, memoryHigh: 12 << 30})
	defer c.release()
	if c.lease == nil {
		t.Fatal("surviving holder not registered")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait() // the kernel has closed the dead holder's lease
	c.release()
	if got := readTrim(t, filepath.Join(dir, "cpu.weight")); got != "100" {
		t.Fatalf("weight after survivor releases = %s, want 100", got)
	}
	if got := readTrim(t, filepath.Join(dir, "memory.high")); got != "max" {
		t.Fatalf("memory.high after survivor releases = %s, want max", got)
	}
}

func TestBoundsStateRejectsUntrustedDirectory(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := filepath.Join(os.TempDir(), fmt.Sprintf("reliant-heavy-bounds-%d", os.Geteuid()))
	target := t.TempDir()
	if err := os.Symlink(target, root); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readBoundsState("unused"); err == nil {
		t.Fatal("accepted symlink directory")
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readBoundsState("unused"); err == nil {
		t.Fatal("accepted non-private directory")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	state, path, err := readBoundsState("unused")
	if err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(target, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path+".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := writeBoundsState(path, state); err != nil {
		t.Fatal(err)
	}
	if got := readTrim(t, victim); got != "keep" {
		t.Fatalf("temporary symlink target overwritten: %q", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readBoundsState("unused"); err == nil {
		t.Fatal("followed ledger symlink")
	}
}

// boundsCgroup is a fake daemon command cgroup with a cpu.weight and a
// memory.high reading weight and high ("" for a file the kernel did not make:
// no cpu or memory controller).
func boundsCgroup(t *testing.T, root, path, weight, high string) string {
	t.Helper()
	dir := fakeCgroup(t, root, path, "max 100000")
	for name, content := range map[string]string{"cpu.weight": weight, "memory.high": high} {
		if content == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func readTrim(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

// The command line's cgroup is lowered while the command runs — the weight
// below the kernel's default its siblings have, memory.high from max to the
// bound — and release puts both back as they were.
func TestLowerBounds_LowersTheCommandCgroupAndRestoresIt(t *testing.T) {
	root := t.TempDir()
	fakeCgroup(t, root, "/daemon/commands", "max 100000")
	dir := boundsCgroup(t, root, cmdCgroup, "100", "max")

	c := lowerBoundsAt(selfIn(t, cmdCgroup), root, bounds{weight: 25, memoryHigh: 12 << 30})
	if got := readTrim(t, filepath.Join(dir, "cpu.weight")); got != "25" {
		t.Errorf("cpu.weight %q while the command runs, want 25", got)
	}
	if got := readTrim(t, filepath.Join(dir, "memory.high")); got != "12884901888" {
		t.Errorf("memory.high %q while the command runs, want 12 GiB", got)
	}
	c.release()
	if got := readTrim(t, filepath.Join(dir, "cpu.weight")); got != "100" {
		t.Errorf("cpu.weight %q after release, want 100 back", got)
	}
	if got := readTrim(t, filepath.Join(dir, "memory.high")); got != "max" {
		t.Errorf("memory.high %q after release, want max back", got)
	}
	c.release() // twice is harmless
}

// A value already at or below the bound — one the machine's operator set, or
// a nested wrapper's — is never raised, and release leaves it alone.
func TestLowerBounds_NeverRaisesAValue(t *testing.T) {
	root := t.TempDir()
	boundsCgroup(t, root, cmdCgroup, "10", "4294967296")
	before := snapshot(t, root)
	c := lowerBoundsAt(selfIn(t, cmdCgroup), root, bounds{weight: 25, memoryHigh: 12 << 30})
	c.release()
	if after := snapshot(t, root); !maps.Equal(before, after) {
		t.Fatalf("the cgroup changed:\nbefore %v\nafter  %v", before, after)
	}
}

// A value something else changed while the command ran is its to keep:
// release does not overwrite it with what it found.
func TestLowerBounds_ReleaseKeepsAValueChangedSince(t *testing.T) {
	root := t.TempDir()
	dir := boundsCgroup(t, root, cmdCgroup, "100", "max")
	c := lowerBoundsAt(selfIn(t, cmdCgroup), root, bounds{weight: 25, memoryHigh: 12 << 30})
	if err := os.WriteFile(filepath.Join(dir, "cpu.weight"), []byte("5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.release()
	if got := readTrim(t, filepath.Join(dir, "cpu.weight")); got != "5" {
		t.Errorf("cpu.weight %q after release, want the 5 set meanwhile", got)
	}
	if got := readTrim(t, filepath.Join(dir, "memory.high")); got != "max" {
		t.Errorf("memory.high %q after release, want max back", got)
	}
}

// Each bound is set where it can be and skipped where it cannot, silently,
// and the tree is otherwise left alone: no memory controller (no
// memory.high), no cpu controller (no cpu.weight), a file it may not write,
// a cgroup that is not a daemon command cgroup, bounds switched off.
func TestLowerBounds_SkipsWhatItCannotSet(t *testing.T) {
	t.Run("no memory controller", func(t *testing.T) {
		root := t.TempDir()
		dir := boundsCgroup(t, root, cmdCgroup, "100", "")
		c := lowerBoundsAt(selfIn(t, cmdCgroup), root, bounds{weight: 25, memoryHigh: 12 << 30})
		defer c.release()
		if got := readTrim(t, filepath.Join(dir, "cpu.weight")); got != "25" {
			t.Errorf("cpu.weight %q, want 25 whatever memory.high does", got)
		}
		if _, err := os.Stat(filepath.Join(dir, "memory.high")); err == nil {
			t.Error("made a memory.high the kernel did not")
		}
	})
	t.Run("no cpu controller", func(t *testing.T) {
		root := t.TempDir()
		dir := boundsCgroup(t, root, cmdCgroup, "", "max")
		c := lowerBoundsAt(selfIn(t, cmdCgroup), root, bounds{weight: 25, memoryHigh: 12 << 30})
		defer c.release()
		if got := readTrim(t, filepath.Join(dir, "memory.high")); got != "12884901888" {
			t.Errorf("memory.high %q, want 12 GiB whatever cpu.weight does", got)
		}
		if _, err := os.Stat(filepath.Join(dir, "cpu.weight")); err == nil {
			t.Error("made a cpu.weight the kernel did not")
		}
	})
	for _, tc := range []struct {
		name, self string
		b          bounds
		setup      func(t *testing.T, dir string)
	}{
		{"not writable", cmdCgroup, bounds{weight: 25, memoryHigh: 1 << 30}, func(t *testing.T, dir string) {
			if os.Geteuid() == 0 {
				t.Skip("root writes a read-only file")
			}
			for _, f := range []string{"cpu.weight", "memory.high"} {
				if err := os.Chmod(filepath.Join(dir, f), 0o444); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"not a daemon command cgroup", "/daemon/self", bounds{weight: 25, memoryHigh: 1 << 30}, nil},
		{"a session", "/user.slice/session-1.scope", bounds{weight: 25, memoryHigh: 1 << 30}, nil},
		{"switched off", cmdCgroup, bounds{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, p := range []string{"/daemon/self", "/user.slice/session-1.scope", cmdCgroup} {
				boundsCgroup(t, root, p, "100", "max")
			}
			if tc.setup != nil {
				tc.setup(t, filepath.Join(root, cmdCgroup))
			}
			before := snapshot(t, root)
			c := lowerBoundsAt(selfIn(t, tc.self), root, tc.b)
			if after := snapshot(t, root); !maps.Equal(before, after) {
				t.Fatalf("the cgroup tree changed:\nbefore %v\nafter  %v", before, after)
			}
			c.release()
		})
	}
}

// The wrapper's two cgroup steps compose: the cap's split moves the wrapper
// into a child of the command cgroup, which must not stop release from
// restoring the command cgroup's own weight and memory.high, and the
// command cgroup keeps its own cpu.max throughout.
func TestLowerBounds_ComposesWithTheCPUCap(t *testing.T) {
	rmdirReady := fakeKernel(t)
	root := t.TempDir()
	fakeCgroup(t, root, "/daemon/commands", "max 100000")
	dir := boundsCgroup(t, root, cmdCgroup, "100", "max")
	self := selfIn(t, cmdCgroup)

	b := lowerBoundsAt(self, root, bounds{weight: 25, memoryHigh: 12 << 30})
	c := limitCPUAt(self, root, 4)
	if c.why != "" {
		t.Fatalf("no cap: %s", c.why)
	}
	if got := readTrim(t, filepath.Join(dir, "cpu.weight")); got != "25" {
		t.Errorf("cpu.weight %q under the cap, want 25", got)
	}
	rmdirReady(dir)
	c.release()
	b.release()
	if got := readTrim(t, filepath.Join(dir, "cpu.weight")); got != "100" {
		t.Errorf("cpu.weight %q after release, want 100", got)
	}
	if got := readTrim(t, filepath.Join(dir, "memory.high")); got != "max" {
		t.Errorf("memory.high %q after release, want max", got)
	}
	if got := readTrim(t, filepath.Join(dir, "cpu.max")); got != "max 100000" {
		t.Errorf("the command cgroup's cpu.max became %q", got)
	}
}

// On this machine's own cgroup, when the test runs in a daemon command cgroup
// whose cpu.weight (and, with the memory controller, memory.high) it may
// write: both are lowered and put back. Elsewhere it skips.
func TestLowerBounds_OnARealCommandCgroup(t *testing.T) {
	rel, why := ownCgroup("/proc/self/cgroup")
	if why != "" || !isCommandCgroupName(filepath.Base(rel)) {
		t.Skip("not in a daemon command cgroup")
	}
	dir := filepath.Join(cgroupFS, rel)
	weight, err := os.ReadFile(filepath.Join(dir, "cpu.weight"))
	if err != nil {
		t.Skip("no cpu.weight: the cpu controller is not enabled for command cgroups")
	}
	high, highErr := os.ReadFile(filepath.Join(dir, "memory.high"))
	for name, value := range map[string][]byte{"cpu.weight": weight, "memory.high": high} {
		if value != nil {
			if err := writeCgroupFile(dir, name, strings.TrimSpace(string(value))); err != nil {
				t.Skipf("%s is not writable: %v", name, err)
			}
		}
	}
	// A bound far above what this test uses, so lowering it slows nothing.
	c := lowerBounds(bounds{weight: 1, memoryHigh: 1 << 40})
	t.Cleanup(c.release)
	if got := readTrim(t, filepath.Join(dir, "cpu.weight")); got != "1" && strings.TrimSpace(string(weight)) != "1" {
		t.Errorf("cpu.weight %q, want 1", got)
	}
	if highErr == nil {
		want := "1099511627776"
		original := strings.TrimSpace(string(high))
		if n, ok := cgroupValue(original); ok && n < 1<<40 {
			want = original
		}
		if got := readTrim(t, filepath.Join(dir, "memory.high")); got != want {
			t.Errorf("memory.high %q, want %s", got, want)
		}
	}
	c.release()
	if got := readTrim(t, filepath.Join(dir, "cpu.weight")); got != strings.TrimSpace(string(weight)) {
		t.Errorf("cpu.weight %q after release, want %q back", got, weight)
	}
	if highErr == nil {
		if got := readTrim(t, filepath.Join(dir, "memory.high")); got != strings.TrimSpace(string(high)) {
			t.Errorf("memory.high %q after release, want %q back", got, high)
		}
	}
}
