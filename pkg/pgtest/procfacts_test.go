package pgtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestShouldReap(t *testing.T) {
	mtime := time.Now().Add(-time.Hour)
	const want = "/rt/bin/postgres"
	cases := []struct {
		name  string
		facts procFacts
		reap  bool
	}{
		{"match", procFacts{exe: want, exeOK: true, start: mtime.Add(-time.Minute), startOK: true}, true},
		{"exe mismatch", procFacts{exe: "/usr/bin/other", exeOK: true, start: mtime.Add(-time.Minute), startOK: true}, false},
		{"created after pidfile", procFacts{exe: want, exeOK: true, start: mtime.Add(time.Minute), startOK: true}, false},
		{"exe unreadable", procFacts{start: mtime.Add(-time.Minute), startOK: true}, false},
		{"start unreadable", procFacts{exe: want, exeOK: true}, false},
		{"nothing readable", procFacts{}, false},
		{"mismatch beats unreadable start", procFacts{exe: "/x", exeOK: true}, false},
	}
	for _, c := range cases {
		if got := shouldReap(c.facts, mtime, want); got != c.reap {
			t.Errorf("%s: got %v want %v", c.name, got, c.reap)
		}
	}
}

func TestClassifyPidForeignVsUnknown(t *testing.T) {
	mtime := time.Now()
	if classifyPid(procFacts{exe: "/a", exeOK: true}, mtime, "/b") != verdictForeign {
		t.Fatal("readable mismatch must be foreign")
	}
	if classifyPid(procFacts{}, mtime, "/b") != verdictUnknown {
		t.Fatal("unreadable must be unknown")
	}
}

func TestLinuxStartTime(t *testing.T) {
	stat := "42 (a) b) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 5000 23"
	boot := time.Unix(1000, 0)
	got, ok := linuxStartTime(stat, boot, 100)
	if !ok || !got.Equal(boot.Add(50*time.Second)) {
		t.Fatalf("got %v %v", got, ok)
	}
	if _, ok := linuxStartTime("garbage", boot, 100); ok {
		t.Fatal("garbage accepted")
	}
}

func TestReadProcFactsSelfAndChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("covered by the windows-tagged test")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no fact reader on this OS")
	}
	self := readProcFacts(os.Getpid())
	exe, _ := os.Executable()
	if !self.exeOK || !samePath(self.exe, exe) {
		t.Fatalf("self exe %q (ok=%v), want %q", self.exe, self.exeOK, exe)
	}
	if !self.startOK || self.start.After(time.Now()) {
		t.Fatalf("self start %v ok=%v", self.start, self.startOK)
	}

	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	before := time.Now().Add(-2 * time.Second)
	cmd := exec.Command(sleep, "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	f := readProcFacts(cmd.Process.Pid)
	if !f.exeOK || filepath.Base(f.exe) != "sleep" {
		t.Fatalf("child exe %q ok=%v", f.exe, f.exeOK)
	}
	if !f.startOK || f.start.After(time.Now().Add(time.Second)) || f.start.Before(before) {
		t.Fatalf("child start %v outside [%v, now]", f.start, before)
	}
}

func TestReadProcFactsDeadPid(t *testing.T) {
	if f := readProcFacts(0); f.exeOK || f.startOK {
		t.Fatal("pid 0 must be unreadable")
	}
}
