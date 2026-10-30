// Copyright (c) 2026 Reliant Labs
//go:build linux

package procguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/proctree"
)

// fakeProc writes a /proc with one stat (and cmdline) per process.
type fakeProc struct {
	t    *testing.T
	root string
}

func newFakeProc(t *testing.T) *fakeProc {
	return &fakeProc{t: t, root: t.TempDir()}
}

// add writes process pid in group pgid with threads threads. Every fake
// process is owned by this test's user, which is the uid the guard counts.
func (f *fakeProc) add(pid, pgid int, threads int, cmd string) {
	dir := filepath.Join(f.root, fmt.Sprint(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	// Fields 3..20: state ppid pgrp session tty tpgid flags minflt cminflt
	// majflt cmajflt utime stime cutime cstime priority nice num_threads.
	stat := fmt.Sprintf("%d (%s) S 1 %d %d 0 -1 0 0 0 0 0 0 0 0 0 20 0 %d 0 0\n", pid, filepath.Base(strings.Fields(cmd)[0]), pgid, pgid, threads)
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.ReplaceAll(cmd, " ", "\x00")), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeProc) remove(pid int) {
	_ = os.RemoveAll(filepath.Join(f.root, fmt.Sprint(pid)))
}

// The guard refuses background heavy commands from 70% of the limit and
// accepts them again only below 60%, naming the largest trees by the agent
// that started each; a group the daemon did not start is named by its
// command line.
func TestGuardRefusesNearTheLimitAndNamesTheTrees(t *testing.T) {
	f := newFakeProc(t)
	owners := []proctree.Info{{Pgid: 100, Owner: proctree.Owner{ChatID: "a32b6a3d-1111-2222", Command: "go test ./tools/ci-evidence/"}}}
	g := New(Config{ProcRoot: f.root, UID: os.Getuid(), NprocLimit: 1000, ThreadsMax: 1 << 30, Owners: func() []proctree.Info { return owners }})

	// 690 of 1000: below the line.
	for i := range 600 {
		f.add(1000+i, 100, 1, "bash scripts/ci-gate.sh all")
	}
	f.add(100, 100, 10, "go test ./tools/ci-evidence/")
	f.add(200, 200, 80, "nohup /tmp/blitz/gate.sh 1631")
	if !g.Check() {
		t.Fatal("the guard could not count the fake /proc")
	}
	if why := g.HeavyRefusal(); why != "" {
		t.Fatalf("refused at 69%%: %s", why)
	}

	// 700 of 1000: refusing.
	f.add(300, 300, 10, "sleep 9")
	g.Check()
	select {
	case <-g.Changed():
	default:
		t.Error("crossing the line did not signal the heartbeat")
	}
	why := g.HeavyRefusal()
	for _, want := range []string{"700 of 1000 tasks", "610 tasks: `go test ./tools/ci-evidence/` (agent a32b6a3d)", "80 tasks: `nohup /tmp/blitz/gate.sh 1631` (not started by an agent's tracked command)", "run_in_background"} {
		if !strings.Contains(why, want) {
			t.Errorf("refusal lacks %q:\n%s", want, why)
		}
	}
	rep := g.Report()
	if !rep.RefusingHeavy || rep.Count != 700 || rep.Limit != 1000 || len(rep.Top) != 3 ||
		rep.Top[0].ChatId != "a32b6a3d-1111-2222" || !rep.Top[0].Tracked || rep.Top[1].Tracked {
		t.Fatalf("report = %v", rep)
	}

	// 650: between the lines — still refusing (hysteresis).
	for i := range 50 {
		f.remove(1000 + i)
	}
	g.Check()
	if g.HeavyRefusal() == "" {
		t.Error("stopped refusing at 65%; refusal should hold until below 60%")
	}

	// 590: accepted again.
	for i := 50; i < 110; i++ {
		f.remove(1000 + i)
	}
	g.Check()
	if why := g.HeavyRefusal(); why != "" {
		t.Errorf("still refusing at 59%%: %s", why)
	}
	if g.Report().RefusingHeavy {
		t.Error("the report still says refusing")
	}
}

// Whichever limit is nearer counts: here the kernel's threads-max, against
// every user's tasks.
func TestGuardUsesTheTighterLimit(t *testing.T) {
	f := newFakeProc(t)
	f.add(10, 10, 50, "a")
	g := New(Config{ProcRoot: f.root, UID: os.Getuid(), NprocLimit: 1000, ThreadsMax: 60, Owners: func() []proctree.Info { return nil }})
	g.Check()
	if rep := g.Report(); rep.Limit != 60 || !rep.RefusingHeavy {
		t.Fatalf("report = %v, want the threads-max limit and a refusal at 83%%", rep)
	}
}

// A nil guard, and a guard that never counted, refuse nothing and report
// nothing.
func TestGuardWithoutACountRefusesNothing(t *testing.T) {
	var nilGuard *Guard
	if nilGuard.HeavyRefusal() != "" || nilGuard.Report() != nil || nilGuard.Changed() != nil {
		t.Fatal("a nil guard acted")
	}
	g := New(Config{ProcRoot: filepath.Join(t.TempDir(), "missing")})
	if g.Check() || g.HeavyRefusal() != "" || g.Report() != nil {
		t.Fatal("a guard with no /proc acted")
	}
}

func TestParseStatSurvivesParenthesesInTheName(t *testing.T) {
	pgid, threads, ok := parseStat("42 (a) b (c)) S 1 7 7 0 -1 0 0 0 0 0 0 0 0 0 20 0 3 0 0")
	if !ok || pgid != 7 || threads != 3 {
		t.Fatalf("parseStat = %d %d %t", pgid, threads, ok)
	}
	if _, _, ok := parseStat("42 (zombie) Z 1 7 7 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 0"); ok {
		t.Error("a zombie was counted")
	}
}
