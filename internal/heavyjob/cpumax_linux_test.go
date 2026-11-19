// Copyright (c) 2026 Reliant Labs
//go:build linux

package heavyjob

import (
	"io/fs"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeKernel stands in for the kernel's part of enabling the cpu controller
// for a fake cgroup's children: each child gets a cpu.max. It returns a
// function that plays the part of rmdir's, which removes a cgroup's interface
// files with it.
func fakeKernel(t *testing.T) (rmdirReady func(dir string)) {
	t.Helper()
	orig := enableCPU
	t.Cleanup(func() { enableCPU = orig })
	enableCPU = func(dir string) error {
		if err := writeCgroupFile(dir, "cgroup.subtree_control", "+cpu"); err != nil {
			return err
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() {
				if err := os.WriteFile(filepath.Join(dir, e.Name(), "cpu.max"), []byte("max 100000"), 0o644); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return func(dir string) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() {
				_ = os.Remove(filepath.Join(dir, e.Name(), "cpu.max"))
			}
		}
	}
}

// fakeCgroup makes the cgroup path under root with the interface files the
// wrapper reads: cpu.max (none when ""), the cpu and pids controllers, none
// enabled for children, no process of its own.
func fakeCgroup(t *testing.T, root, path, cpuMax string) string {
	t.Helper()
	dir := filepath.Join(root, path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"cgroup.controllers": "cpu pids\n", "cgroup.subtree_control": "", "cgroup.procs": ""}
	if cpuMax != "" {
		files["cpu.max"] = cpuMax
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// selfIn writes a /proc/self/cgroup naming cgroup.
func selfIn(t *testing.T, cgroup string) string {
	t.Helper()
	self := filepath.Join(t.TempDir(), "cgroup")
	if err := os.WriteFile(self, []byte("0::"+cgroup+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return self
}

// snapshot is every directory and file under root, with each file's content.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			snap[p] = "<dir>"
			return nil
		}
		data, err := os.ReadFile(p)
		snap[p] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// cpuMaxesAbove lists every cpu.max under root that allows more than cpus.
func cpuMaxesAbove(t *testing.T, root string, cpus float64) []string {
	t.Helper()
	var above []string
	for p, content := range snapshot(t, root) {
		if filepath.Base(p) != "cpu.max" {
			continue
		}
		if n, capped := readCPUMax(p); !capped || n > cpus {
			above = append(above, p+": "+content)
		}
	}
	return above
}

const cmdCgroup = "/daemon/commands/cmd-268-7"

// The regression: in a command cgroup capped at 2 CPUs, a wrapper whose share
// is 5 left the cgroup at 5 — it overwrote the cap it inherited, raising it
// (5 became 7 on a workspace). Its cgroup must stay byte-for-byte as it was,
// nothing it makes may allow more than 2 CPUs, and the share it exports is 2.
func TestLimitCPU_NeverRaisesTheCapItInherits(t *testing.T) {
	fakeKernel(t)
	root := t.TempDir()
	fakeCgroup(t, root, "/daemon/commands", "max 100000")
	dir := fakeCgroup(t, root, cmdCgroup, "200000 100000")
	before := snapshot(t, root)

	c := limitCPUAt(selfIn(t, cmdCgroup), root, 5)
	defer c.release()
	if got, _ := os.ReadFile(filepath.Join(dir, "cpu.max")); string(got) != "200000 100000" {
		t.Fatalf("the wrapper rewrote the cpu.max of the cgroup it runs in to %q, want it unchanged at %q", got, "200000 100000")
	}
	if above := cpuMaxesAbove(t, dir, 2); len(above) > 0 {
		t.Fatalf("cpu.max above the 2 CPUs inherited: %v", above)
	}
	if c.share != 2 {
		t.Fatalf("share %d exported as GOMAXPROCS, want it clamped to the 2 CPUs inherited", c.share)
	}
	// The inherited cap already binds the command: nothing to make.
	if c.why == "" || c.into(exec.Command("true")) {
		t.Fatalf("made a cgroup no tighter than the one it inherited (why %q)", c.why)
	}
	if after := snapshot(t, root); !maps.Equal(before, after) {
		t.Fatalf("the cgroup tree changed:\nbefore %v\nafter  %v", before, after)
	}
}

// An ancestor's cap counts as much as the cgroup's own: the tightest on the
// path to the root is what the wrapper inherited.
func TestLimitCPU_ClampsToTheTightestAncestor(t *testing.T) {
	rmdirReady := fakeKernel(t)
	for _, tc := range []struct {
		name      string
		daemon    string // the cpu.max two levels up
		share     int
		wantShare int
		wantCap   string // the command cgroup's cpu.max; "" for none made
	}{
		{"ancestor below the share", "300000 100000", 5, 3, ""},
		{"ancestor equal to the share", "300000 100000", 3, 3, ""},
		{"fractional ancestor", "250000 50000", 9, 5, ""},
		{"ancestor above the share", "300000 100000", 2, 2, "200000 100000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			fakeCgroup(t, root, "/daemon", tc.daemon)
			fakeCgroup(t, root, "/daemon/commands", "max 100000")
			dir := fakeCgroup(t, root, cmdCgroup, "max 100000")
			c := limitCPUAt(selfIn(t, cmdCgroup), root, tc.share)
			defer c.release()
			if c.share != tc.wantShare {
				t.Errorf("share %d, want %d", c.share, tc.wantShare)
			}
			if got, _ := os.ReadFile(filepath.Join(dir, "cpu.max")); string(got) != "max 100000" {
				t.Errorf("the wrapper's own cgroup's cpu.max became %q", got)
			}
			got := ""
			if c.command != "" {
				data, _ := os.ReadFile(filepath.Join(c.command, "cpu.max"))
				got = string(data)
			}
			if got != tc.wantCap {
				t.Errorf("command cgroup cpu.max %q, want %q (why %q)", got, tc.wantCap, c.why)
			}
			rmdirReady(dir)
		})
	}
}

// Under an uncapped cgroup the command gets its share as a cap of its own: in
// a cgroup the wrapper made, which it starts in and which release removes,
// while the cgroup the wrapper runs in keeps its own cpu.max.
func TestLimitCPU_CapsAChildOfItsOwnAtTheShare(t *testing.T) {
	rmdirReady := fakeKernel(t)
	root := t.TempDir()
	fakeCgroup(t, root, "/daemon/commands", "max 100000")
	dir := fakeCgroup(t, root, cmdCgroup, "max 100000")

	c := limitCPUAt(selfIn(t, cmdCgroup), root, 5)
	if c.why != "" {
		t.Fatalf("no cap: %s", c.why)
	}
	if c.share != 5 {
		t.Errorf("share %d, want 5", c.share)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "cpu.max")); string(got) != "max 100000" {
		t.Errorf("the wrapper's own cgroup's cpu.max became %q", got)
	}
	if filepath.Dir(c.command) != dir || filepath.Dir(c.caller) != dir {
		t.Fatalf("children %s and %s, want both in %s", c.command, c.caller, dir)
	}
	if got, _ := os.ReadFile(filepath.Join(c.command, "cpu.max")); string(got) != "500000 100000" {
		t.Errorf("command cgroup cpu.max %q, want a 5-CPU cap", got)
	}
	cmd := exec.Command("true")
	if !c.into(cmd) || !cmd.SysProcAttr.UseCgroupFD {
		t.Fatal("the command does not start in its capped cgroup")
	}

	rmdirReady(dir)
	c.release()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("release left %s in the wrapper's cgroup", e.Name())
		}
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "cpu.max")); string(got) != "max 100000" {
		t.Errorf("the wrapper's own cgroup's cpu.max became %q", got)
	}
	c.release() // twice is harmless
}

// Where it cannot make a cgroup of its own — or must not — the wrapper changes
// nothing in the cgroup tree, and the command runs uncapped by it.
func TestLimitCPU_LeavesTheTreeAloneWhereItCannotMakeACgroup(t *testing.T) {
	fakeKernel(t)
	for _, tc := range []struct {
		name, self string
		setup      func(t *testing.T, dir string)
	}{
		{"no cpu controller", "0::" + cmdCgroup, func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "cgroup.controllers"), []byte("pids\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"controllers already enabled for its children", "0::" + cmdCgroup, func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), []byte("pids\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"cannot make a child", "0::" + cmdCgroup, func(t *testing.T, dir string) {
			if os.Geteuid() == 0 {
				t.Skip("root writes a read-only directory")
			}
			if err := os.Chmod(dir, 0o555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		}},
		{"not a daemon command cgroup", "0::/daemon/self", nil},
		{"a session", "0::/user.slice/session-1.scope", nil},
		{"almost a command cgroup", "0::/daemon/commands/cmd-x", nil},
		{"cgroup v1", "12:cpu,cpuacct:" + cmdCgroup, nil},
		{"the root", "0::/", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, p := range []string{"/daemon/self", "/user.slice/session-1.scope", "/daemon/commands/cmd-x"} {
				fakeCgroup(t, root, p, "max 100000")
			}
			dir := fakeCgroup(t, root, cmdCgroup, "max 100000")
			if tc.setup != nil {
				tc.setup(t, dir)
			}
			before := snapshot(t, root)
			self := filepath.Join(t.TempDir(), "cgroup")
			if err := os.WriteFile(self, []byte(tc.self+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			c := limitCPUAt(self, root, 5)
			defer c.release()
			if c.why == "" || c.into(exec.Command("true")) {
				t.Fatalf("reported a cap")
			}
			if c.share != 5 {
				t.Errorf("share %d, want 5: nothing caps it lower", c.share)
			}
			if after := snapshot(t, root); !maps.Equal(before, after) {
				t.Fatalf("the cgroup tree changed:\nbefore %v\nafter  %v", before, after)
			}
		})
	}
}

// A cgroup path that would leave the cgroup root is never written to.
func TestLimitCPU_RefusesAPathOutsideTheCgroupRoot(t *testing.T) {
	fakeKernel(t)
	base := t.TempDir()
	root := filepath.Join(base, "cgroup")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeCgroup(t, base, "cmd-1-1", "max 100000")
	before := snapshot(t, base)
	for _, cg := range []string{"/../cmd-1-1", "../cmd-1-1", "/a/../../cmd-1-1"} {
		c := limitCPUAt(selfIn(t, cg), root, 5)
		c.release()
		if c.why == "" {
			t.Errorf("%s: reported a cap", cg)
		}
		if after := snapshot(t, base); !maps.Equal(before, after) {
			t.Fatalf("%s: the tree outside the root changed", cg)
		}
	}
}

// On this machine's own cgroup, when the test runs in a daemon command cgroup
// with the cpu controller (an agent's command): the command starts in a
// capped cgroup of the wrapper's own, the cgroup it ran in keeps its cpu.max,
// and release puts everything back as it was. Elsewhere it skips.
func TestLimitCPU_OnARealCommandCgroup(t *testing.T) {
	rel, why := ownCgroup("/proc/self/cgroup")
	if why != "" || !isCommandCgroupName(filepath.Base(rel)) {
		t.Skip("not in a daemon command cgroup")
	}
	dir := filepath.Join(cgroupFS, rel)
	if ctl, err := os.ReadFile(filepath.Join(dir, "cgroup.controllers")); err != nil || !hasWord(string(ctl), "cpu") {
		t.Skip("no cpu controller for this cgroup")
	}
	if inherited, capped := inheritedCPUs(cgroupFS, rel); capped && inherited <= 1 {
		t.Skip("capped at 1 CPU or less already")
	}
	cpuMax, err := os.ReadFile(filepath.Join(dir, "cpu.max"))
	if err != nil {
		t.Fatal(err)
	}
	c := limitCPU(1)
	defer c.release()
	if c.why != "" {
		t.Fatalf("no cap in a command cgroup with the cpu controller: %s", c.why)
	}
	cmd := exec.Command("cat", "/proc/self/cgroup")
	if !c.into(cmd) {
		t.Fatal("no capped cgroup to start in")
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("starting in the capped cgroup: %v", err)
	}
	if want := "0::/" + rel + "/" + filepath.Base(c.command); strings.TrimSpace(string(out)) != want {
		t.Errorf("the command ran in %q, want %q", strings.TrimSpace(string(out)), want)
	}
	if got, _ := os.ReadFile(filepath.Join(c.command, "cpu.max")); strings.TrimSpace(string(got)) != "100000 100000" {
		t.Errorf("command cgroup cpu.max %q, want a 1-CPU cap", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "cpu.max")); string(got) != string(cpuMax) {
		t.Errorf("the cgroup it ran in changed its cpu.max from %q to %q", cpuMax, got)
	}

	c.release()
	if now, _ := ownCgroup("/proc/self/cgroup"); now != rel {
		t.Errorf("release left this process in %s, not %s", now, rel)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("release left %s in the cgroup", e.Name())
		}
	}
	if sc, _ := os.ReadFile(filepath.Join(dir, "cgroup.subtree_control")); strings.TrimSpace(string(sc)) != "" {
		t.Errorf("release left controllers enabled for children: %q", sc)
	}
}

// inheritedShare is share clamped as the wrapper clamps it: to the cpu.max
// this process inherited.
func inheritedShare(share int) int {
	rel, why := ownCgroup("/proc/self/cgroup")
	if why != "" {
		return share
	}
	if n, capped := inheritedCPUs(cgroupFS, rel); capped {
		return min(share, max(1, int(math.Floor(n))))
	}
	return share
}
