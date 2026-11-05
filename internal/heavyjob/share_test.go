// Copyright (c) 2025 Reliant Labs
package heavyjob

import (
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/hostslot"
)

func TestDefaultShare(t *testing.T) {
	for _, tc := range []struct{ cpus, slots, want int }{
		{32, 8, 6},
		{31, 8, 5}, // the 2026-11-03 machine: 8 x 5 = 40 CPUs, 1.3x
		{4, 8, 2},  // never below two
		{64, 4, 24},
		{30, 8, 5}, // rounds down
		{1, 1, 2},
		{16, 0, 24}, // no slot count reads as one slot
	} {
		if got := DefaultShare(tc.cpus, tc.slots); got != tc.want {
			t.Errorf("DefaultShare(%d CPUs, %d slots) = %d, want %d", tc.cpus, tc.slots, got, tc.want)
		}
	}
}

func TestShareEnvKeepsWhatTheCommandSet(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
		want []string
	}{
		{"unset", []string{"PATH=/bin"}, []string{"GOMAXPROCS=8", "UV_THREADPOOL_SIZE=8"}},
		{"own GOMAXPROCS", []string{"GOMAXPROCS=16"}, []string{"UV_THREADPOOL_SIZE=8"}},
		{"own pool", []string{"UV_THREADPOOL_SIZE=2", "GOMAXPROCS=3"}, nil},
		{"empty reads as unset", []string{"GOMAXPROCS="}, []string{"GOMAXPROCS=8", "UV_THREADPOOL_SIZE=8"}},
		// GOFLAGS is never written: -p defaults to the go command's
		// GOMAXPROCS, so the share already bounds it, and an explicit -p
		// stays exactly as the command set it.
		{"own GOFLAGS -p", []string{"GOFLAGS=-p=2 -mod=mod"}, []string{"GOMAXPROCS=8", "UV_THREADPOOL_SIZE=8"}},
	} {
		if got := shareEnv(tc.env, 8); !slices.Equal(got, tc.want) {
			t.Errorf("%s: shareEnv(%q) = %q, want %q", tc.name, tc.env, got, tc.want)
		}
	}
	if got := shareEnv(nil, 0); got != nil {
		t.Errorf("no share exported %q", got)
	}
}

func TestPreparePassesAConfiguredShare(t *testing.T) {
	g, err := New(Config{Env: []string{"FORGE_SLOTS_DIR=" + t.TempDir(), "FORGE_HEAVY_SLOTS=3"}, Self: testSelf(t), Share: 5})
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command("sh", "-c", "go test ./...")
	if ok, err := g.Prepare(c, Job{Command: "go test ./..."}); !ok || err != nil {
		t.Fatalf("not rewritten: %v", err)
	}
	if i := slices.Index(c.Args, "--share"); i < 0 || c.Args[i+1] != "5" || i > slices.Index(c.Args, "--") {
		t.Fatalf("wrapper args %q carry no --share 5", c.Args)
	}
}

func TestParseWrapperArgsShare(t *testing.T) {
	w, err := parseWrapperArgs([]string{"--dir", "d", "--slots", "2", "--share", "6", "--child-gomaxprocs", "", "--", "go", "test"})
	if err != nil || w.share != 6 || w.opt.Slots != 2 || !slices.Equal(w.argv, []string{"go", "test"}) {
		t.Fatalf("parsed %+v err %v", w, err)
	}
	if w.childGOMAXPROCS == nil || *w.childGOMAXPROCS != "" {
		t.Fatalf("--child-gomaxprocs \"\" parsed as %v", w.childGOMAXPROCS)
	}
	if w, _ := parseWrapperArgs([]string{"--", "x"}); w.childGOMAXPROCS != nil {
		t.Fatal("no --child-gomaxprocs parsed as one")
	}
	if _, err := parseWrapperArgs([]string{"--share", "0", "--", "x"}); err == nil {
		t.Fatal("--share 0 accepted")
	}
}

func TestChildEnvRestoresTheCommandsGOMAXPROCS(t *testing.T) {
	wrapper := []string{"A=1", "GOMAXPROCS=6", "GOMAXPROCS=1"}
	for _, tc := range []struct {
		name  string
		child *string
		want  []string
	}{
		{"not started on one P by the gate", nil, wrapper},
		{"command had none", ptr(""), []string{"A=1"}},
		{"command had its own", ptr("6"), []string{"A=1", "GOMAXPROCS=6"}},
	} {
		if got := childEnv(wrapper, tc.child); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func ptr(s string) *string { return &s }

// childReport is a heavy command that outlives a few of the wrapper's
// schedtrace lines, then prints what its environment says.
const childReport = `sleep 0.3; echo "child GOMAXPROCS=[$GOMAXPROCS] UV=[$UV_THREADPOOL_SIZE] GOFLAGS=[$GOFLAGS]"`

var schedLine = regexp.MustCompile(`SCHED \d+ms: gomaxprocs=(\d+)`)

// The wrapper runs on one P — GODEBUG=schedtrace makes the runtime report
// its own gomaxprocs — while the command it runs gets the pool's share, not
// the wrapper's one.
func TestWrapperRunsLeanAndItsCommandGetsItsShare(t *testing.T) {
	g, _ := oneSlotGate(t)
	s := start(t, g, childReport, true, "GODEBUG=schedtrace=20")
	s.wait(t, 10*time.Second)
	out := s.out.String()
	if s.err != nil {
		t.Fatalf("command failed: %v\n%s", s.err, out)
	}
	sched := schedLine.FindAllStringSubmatch(out, -1)
	if len(sched) == 0 {
		t.Fatalf("no schedtrace from the wrapper:\n%s", out)
	}
	// Every line, the first included: the wrapper is on one P from its
	// start, before package init could grow threads.
	for _, m := range sched {
		if m[1] != "1" {
			t.Fatalf("wrapper ran at gomaxprocs=%s, want 1 throughout:\n%s", m[1], out)
		}
	}
	cpus := max(1, int(math.Floor(hostslot.DetectMachine().CPUs)))
	want := strconv.Itoa(DefaultShare(cpus, 1))
	if line := fmt.Sprintf("child GOMAXPROCS=[%s] UV=[%s] GOFLAGS=[]", want, want); !strings.Contains(out, line) {
		t.Fatalf("want %q (%d CPUs, 1 slot) in:\n%s", line, cpus, out)
	}
}

func TestWrapperKeepsTheCommandsOwnSettings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("drives sh")
	}
	g, err := New(Config{Env: []string{"FORGE_SLOTS_DIR=" + t.TempDir(), "FORGE_HEAVY_SLOTS=1"}, Self: testSelf(t), Share: 3})
	if err != nil {
		t.Fatal(err)
	}
	s := start(t, g, childReport, true)
	s.wait(t, 10*time.Second)
	if want := "child GOMAXPROCS=[3] UV=[3] GOFLAGS=[]"; !strings.Contains(s.out.String(), want) {
		t.Fatalf("configured share: want %q in:\n%s", want, s.out)
	}

	s = start(t, g, childReport, true, "GOMAXPROCS=5", "GOFLAGS=-p=2")
	s.wait(t, 10*time.Second)
	if want := "child GOMAXPROCS=[5] UV=[3] GOFLAGS=[-p=2]"; !strings.Contains(s.out.String(), want) {
		t.Fatalf("own settings: want %q in:\n%s", want, s.out)
	}
}
