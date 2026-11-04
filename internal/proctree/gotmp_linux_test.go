// Copyright (c) 2026 Reliant Labs
//go:build linux

package proctree

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

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
