// Copyright (c) 2026 Reliant Labs
//go:build linux

package proctree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func goTmpOf(env []string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GOTMPDIR="); ok {
			return v
		}
	}
	return ""
}

// A command gets a GOTMPDIR of its own, and it is removed once the command's
// processes are gone.
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
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the command's GOTMPDIR survived it")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Two commands never share one.
	a, b := exec.Command("true"), exec.Command("true")
	a.Env, b.Env = []string{}, []string{}
	ta, tb := l.Prepare(a, Owner{}), l.Prepare(b, Owner{})
	defer ta.Started(0)
	defer tb.Started(0)
	if goTmpOf(a.Env) == goTmpOf(b.Env) {
		t.Fatalf("two commands share %s", goTmpOf(a.Env))
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
