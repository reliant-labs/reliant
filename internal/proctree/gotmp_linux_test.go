// Copyright (c) 2026 Reliant Labs
//go:build linux

package proctree

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

func goTmpOf(env []string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GOTMPDIR="); ok {
			return v
		}
	}
	return ""
}

// A command gets a GOTMPDIR of its own, and two commands never share one.
func TestPrepare_GivesEachCommandItsOwnGoTmpDir(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	l := &Limiter{}
	cmd := exec.Command("sh", "-c", `test -d "$GOTMPDIR" && echo ok > "$GOTMPDIR/f"`)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	tree := l.Prepare(cmd, Owner{})
	dir := goTmpOf(cmd.Env)
	if dir == "" || !strings.HasPrefix(filepath.Base(dir), GoTmpPrefix) || !strings.HasPrefix(filepath.Base(filepath.Dir(dir)), GoTmpBasePrefix) {
		t.Fatalf("GOTMPDIR = %q, want a per-command root", dir)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	tree.Started(cmd.Process.Pid)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the command could not use its GOTMPDIR: %v", err)
	}
	tree.Release()

	a, b := exec.Command("true"), exec.Command("true")
	a.Env, b.Env = []string{}, []string{}
	ta, tb := l.Prepare(a, Owner{}), l.Prepare(b, Owner{})
	defer ta.Started(0)
	defer tb.Started(0)
	if goTmpOf(a.Env) == goTmpOf(b.Env) {
		t.Fatalf("two commands share %s", goTmpOf(a.Env))
	}
}

// Without a cgroup nothing proves a command's processes gone: a descendant
// that left its process group (setsid) keeps using the GOTMPDIR it
// inherited after the command exits, so Release leaves the root to the
// storage reaper.
func TestRelease_KeepsGoTmpDirWithoutACgroup(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("no setsid: ", err)
	}
	cmd := exec.Command("sh", "-c", `setsid sleep 300 </dev/null >/dev/null 2>&1 & echo $!`)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	var out bytes.Buffer
	cmd.Stdout = &out
	tree := (&Limiter{}).Prepare(cmd, Owner{})
	dir := goTmpOf(cmd.Env)
	if dir == "" {
		t.Fatal("no per-command GOTMPDIR")
	}
	if err := cmd.Start(); err != nil {
		tree.Started(0)
		t.Fatal(err)
	}
	tree.Started(cmd.Process.Pid)
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if pid, err := strconv.Atoi(strings.TrimSpace(out.String())); err == nil {
		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	}
	tree.Release()
	if tree.goTmp != dir {
		t.Fatalf("Release gave up %s with no cgroup to prove its processes gone", dir)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the GOTMPDIR a setsid descendant still holds is gone: %v", err)
	}
}

// With a cgroup, the root goes once the cgroup is empty.
func TestRelease_RemovesGoTmpDirOnceTheCgroupIsEmpty(t *testing.T) {
	testkit.Slow(t, "starts a command in a cgroup")
	t.Setenv("TMPDIR", t.TempDir())
	l := cgroupLimiter(t, 100, 1000)
	cmd := exec.Command("true")
	cmd.Env = []string{}
	tree := l.Prepare(cmd, Owner{})
	dir := goTmpOf(cmd.Env)
	if dir == "" {
		t.Fatal("no per-command GOTMPDIR")
	}
	if err := cmd.Start(); err != nil {
		tree.Started(0)
		t.Fatal(err)
	}
	tree.Started(cmd.Process.Pid)
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	tree.Release()
	if tree.goTmp != "" {
		t.Fatalf("Release kept %s after the cgroup emptied", dir)
	}
}

// fakeCgroup is a directory that reads as a cgroup populated or not.
func fakeCgroup(t *testing.T, populated bool) string {
	t.Helper()
	dir := t.TempDir()
	setPopulated(t, dir, populated)
	return dir
}

func setPopulated(t *testing.T, cgroup string, populated bool) {
	t.Helper()
	v := "0"
	if populated {
		v = "1"
	}
	if err := os.WriteFile(filepath.Join(cgroup, "cgroup.events"), []byte("populated "+v+"\nfrozen 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeGoTmp is a per-command root holding a killed build's work directory.
func fakeGoTmp(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gotmp-1-1")
	if err := os.MkdirAll(filepath.Join(root, "go-build1", "b001"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

// watchFast makes watchers poll every millisecond for at most d, and gives
// them a pool of n, for the test.
func watchFast(t *testing.T, d time.Duration, n int) {
	t.Helper()
	poll, wait, pool := goTmpWatchPoll, goTmpWatchFor, goTmpWatchers
	goTmpWatchPoll, goTmpWatchFor, goTmpWatchers = time.Millisecond, d, make(chan struct{}, n)
	t.Cleanup(func() { goTmpWatchPoll, goTmpWatchFor, goTmpWatchers = poll, wait, pool })
}

// reliant#3522: a killed command's processes are still exiting when it
// returns, so its cgroup is still populated and its root used to stay for the
// go_work_dirs pass (2,800 roots, 20 GiB, on one host). It is now watched, and
// goes as soon as the cgroup empties.
func TestRelease_WatchesAPopulatedCgroupUntilItEmpties(t *testing.T) {
	watchFast(t, time.Minute, 4)
	cg, root := fakeCgroup(t, true), fakeGoTmp(t)
	tree := &Tree{dir: cg, goTmp: root, fd: -1}
	done := tree.releaseGoTmp()
	if done == nil || tree.goTmp != "" {
		t.Fatalf("a populated cgroup's root was not handed to a watcher (goTmp %q)", tree.goTmp)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("the root went while its cgroup was populated: %v", err)
	}
	setPopulated(t, cg, false)
	<-done
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the root outlived its cgroup emptying: %v", err)
	}
	if len(goTmpWatchers) != 0 {
		t.Error("the watcher did not give back its slot")
	}
}

// A cgroup removed outright (Release's drop, or a later sweep, once empty) is
// as proven empty as one reporting populated 0.
func TestWatchGoTmp_RemovedCgroupIsEmpty(t *testing.T) {
	watchFast(t, time.Minute, 4)
	cg, root := fakeCgroup(t, true), fakeGoTmp(t)
	done, ok := watchGoTmp(cg, root)
	if !ok {
		t.Fatal("no watcher")
	}
	if err := os.RemoveAll(cg); err != nil {
		t.Fatal(err)
	}
	<-done
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the root outlived its cgroup's removal: %v", err)
	}
}

// A cgroup that stays populated (a daemon the command left running) or
// cannot be read proves nothing: the watcher gives up after goTmpWatchFor and
// leaves the root to the go_work_dirs pass.
func TestWatchGoTmp_GivesUpAndKeepsTheRoot(t *testing.T) {
	for name, cg := range map[string]func(t *testing.T) string{
		"populated": func(t *testing.T) string { return fakeCgroup(t, true) },
		"unreadable": func(t *testing.T) string {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "cgroup.events"), 0o755); err != nil {
				t.Fatal(err)
			}
			return dir
		},
	} {
		t.Run(name, func(t *testing.T) {
			watchFast(t, 20*time.Millisecond, 4)
			root := fakeGoTmp(t)
			done, ok := watchGoTmp(cg(t), root)
			if !ok {
				t.Fatal("no watcher")
			}
			<-done
			if _, err := os.Stat(root); err != nil {
				t.Fatalf("the root went with nothing proving its cgroup empty: %v", err)
			}
			if len(goTmpWatchers) != 0 {
				t.Error("the watcher did not give back its slot")
			}
		})
	}
}

// Watchers are bounded: with every one busy, a root stays with its tree and
// is left to the go_work_dirs pass rather than start another goroutine.
func TestRelease_NoFreeWatcherKeepsTheRoot(t *testing.T) {
	watchFast(t, time.Minute, 1)
	goTmpWatchers <- struct{}{}
	defer func() { <-goTmpWatchers }()
	root := fakeGoTmp(t)
	tree := &Tree{dir: fakeCgroup(t, true), goTmp: root, fd: -1}
	if done := tree.releaseGoTmp(); done != nil || tree.goTmp != root {
		t.Fatalf("with no free watcher: watched %v, goTmp %q; want the root kept", done != nil, tree.goTmp)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal(err)
	}
}

// A command that names its own GOTMPDIR keeps it.
func TestPrepare_KeepsACommandsOwnGoTmpDir(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	cmd := exec.Command("true")
	cmd.Env = []string{"GOTMPDIR=/mine"}
	tree := (&Limiter{}).Prepare(cmd, Owner{})
	defer tree.Started(0)
	if len(cmd.Env) != 1 || goTmpOf(cmd.Env) != "/mine" || tree.goTmp != "" {
		t.Fatalf("env = %v, goTmp = %q; want the command's own GOTMPDIR alone", cmd.Env, tree.goTmp)
	}
}
