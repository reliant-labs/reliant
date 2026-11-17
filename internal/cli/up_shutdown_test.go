package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

func runMarker(proj, env, svc, run string) []string {
	return append(markerProj(proj, env, svc), forgeUpRunVar+"="+run)
}

// fakeStopTable models a process table for the shutdown sweep: rows, the
// environments that can be read (a pid absent from env is unreadable), and
// liveness (every row is alive unless listed in dead).
func fakeStopTable(rows map[int]stopProc, env map[int][]string, dead ...int) *stopTable {
	gone := map[int]bool{}
	for _, pid := range dead {
		gone[pid] = true
	}
	return &stopTable{
		procs: rows,
		readEnv: func(pid int) ([]string, bool) {
			e, ok := env[pid]
			return e, ok
		},
		isAlive: func(pid int) bool {
			_, ok := rows[pid]
			return ok && !gone[pid]
		},
	}
}

// TestSweepRun pins the shutdown sweep's selection: a process this run
// started is found wherever it now sits in the process table; nothing another
// run, project or env started — nor this command's own lineage — ever is; and
// a process whose environment cannot be read is reported as undetermined
// unless it provably cannot be this run's.
//
//	1    init
//	├─ 100 forge env up         (this command)
//	│  └─ 110 air               (run R, api)
//	│     └─ 111 server         (run R, api)    ← inside 110's tree: not a root
//	├─ 200 server               (run R, api)    ← detached: reparented to init
//	│  └─ 201 worker child      (run R, api)
//	├─ 300 server               (run OTHER, api) ← another run of the same stack
//	├─ 400 server               (run R, other project)
//	├─ 500 server               (run R, other env)
//	├─ 600 unreadable, started during the run    ← undetermined
//	├─ 601 unreadable, another user's            ← provably not ours
//	├─ 602 unreadable, started before the run    ← provably not ours
//	├─ 603 unreadable zombie                     ← dead
//	└─ 604 unreadable, start time unknown        ← undetermined
func TestSweepRun(t *testing.T) {
	since := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	during, before := since.Add(time.Minute), since.Add(-time.Minute)
	rows := map[int]stopProc{
		100: {ppid: 1, started: before},
		110: {ppid: 100, started: during},
		111: {ppid: 110, started: during},
		200: {ppid: 1, started: during},
		201: {ppid: 200, started: during},
		300: {ppid: 1, started: during},
		400: {ppid: 1, started: during},
		500: {ppid: 1, started: during},
		600: {ppid: 1, started: during},
		601: {ppid: 1, started: during, foreign: true},
		602: {ppid: 1, started: before},
		603: {ppid: 1, started: during, exited: true},
		604: {ppid: 1},
	}
	env := map[int][]string{
		100: {"PATH=/usr/bin"},
		110: runMarker(testProj, "dev", "api", "R"),
		111: runMarker(testProj, "dev", "api", "R"),
		200: runMarker(testProj, "dev", "api", "R"),
		201: runMarker(testProj, "dev", "api", "R"),
		300: runMarker(testProj, "dev", "api", "OTHER"),
		400: runMarker("projB", "dev", "api", "R"),
		500: runMarker(testProj, "staging", "api", "R"),
	}
	table := fakeStopTable(rows, env)
	lineage := lineageOf(fakeFacts{ppid: map[int]int{100: 1}}, 100)

	got := sweepRun(testProj, "dev", "R", since, table, lineage)
	if want := []int{110, 200}; !equalInts(got.roots, want) {
		t.Errorf("roots = %v, want %v: the runner and the process that left its tree", got.roots, want)
	}
	if want := []int{600, 604}; !equalInts(got.unknown, want) {
		t.Errorf("unknown = %v, want %v", got.unknown, want)
	}

	// The command's own lineage is never selected, even wearing the marker
	// (a forge run from a shell inside its own stack) or unreadable.
	env[100] = runMarker(testProj, "dev", "api", "R")
	if got := sweepRun(testProj, "dev", "R", since, table, lineage); !equalInts(got.roots, []int{110, 200}) {
		t.Errorf("with a marked lineage: roots = %v, want [110 200]", got.roots)
	}
	delete(env, 100)
	rows[100] = stopProc{ppid: 1, started: during}
	if got := sweepRun(testProj, "dev", "R", since, table, lineage); !equalInts(got.unknown, []int{600, 604}) {
		t.Errorf("with an unreadable lineage: unknown = %v, want [600 604]", got.unknown)
	}
}

// stopTestRegistry is a registry with one runner that has ALREADY exited —
// the case where only the sweep can find what it left behind — and a stack
// ledger on disk. The runner's pid is never signalled: an exited runner is
// skipped by every signalling pass.
func stopTestRegistry(t *testing.T, read func() (*stopTable, error)) (*procRegistry, string) {
	t.Helper()
	prevReap, prevStraggler := stopReapWait, stopStragglerWait
	stopReapWait, stopStragglerWait = 100*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { stopReapWait, stopStragglerWait = prevReap, prevStraggler })

	dir, projectID, env := testStack(t)
	reg := newProcRegistry(projectID, dir, env)
	reg.readStopTable = read
	done := make(chan struct{})
	close(done)
	reg.processes = []*managedProcess{{name: "api", pid: 4242, done: done}}
	ledger, err := upStatePath(projectID, env)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ledger), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledger, []byte("api\t4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return reg, ledger
}

// TestShutdownKeepsRecordsWhenTheProcessTableIsUnreadable is review blocker
// B1: a sweep that cannot read the process table has found nothing, which is
// not the same as finding that nothing is left. The records stay, and the
// command says why.
func TestShutdownKeepsRecordsWhenTheProcessTableIsUnreadable(t *testing.T) {
	reg, ledger := stopTestRegistry(t, func() (*stopTable, error) {
		return nil, errors.New("ps: exec: \"ps\": executable file not found in $PATH")
	})
	var err error
	out := captureStdout(t, func() { err = reg.shutdown() })
	if err == nil || !strings.Contains(err.Error(), "unverified") || !strings.Contains(err.Error(), "forge env down") {
		t.Fatalf("shutdown = %v, want an unverified-shutdown error naming the retry", err)
	}
	if !strings.Contains(out, "process table could not be read") || !strings.Contains(out, "records are kept") {
		t.Errorf("output does not explain the unverified stop:\n%s", out)
	}
	if _, statErr := os.Stat(ledger); statErr != nil {
		t.Errorf("ledger removed although the stack could not be checked: %v", statErr)
	}
}

// TestShutdownKeepsRecordsForAnUninspectableDescendant is review blocker B2:
// the runner already exited, its child was reparented to init, and the
// child's environment cannot be read — so whether it carries this run's
// marker is unknown. That is not proof it is gone.
func TestShutdownKeepsRecordsForAnUninspectableDescendant(t *testing.T) {
	var reg *procRegistry
	table := func(env map[int][]string) func() (*stopTable, error) {
		return func() (*stopTable, error) {
			return fakeStopTable(map[int]stopProc{
				1:    {},
				5000: {ppid: 1, started: reg.startedAt.Add(time.Minute)},
			}, env), nil
		}
	}

	reg, ledger := stopTestRegistry(t, nil)
	reg.readStopTable = table(nil) // pid 5000's environment is unreadable
	var err error
	out := captureStdout(t, func() { err = reg.shutdown() })
	if err == nil || !strings.Contains(err.Error(), "pid 5000") {
		t.Fatalf("shutdown = %v, want an unverified-shutdown error naming pid 5000", err)
	}
	if !strings.Contains(out, "could not be read") {
		t.Errorf("output does not explain the unverified stop:\n%s", out)
	}
	if _, statErr := os.Stat(ledger); statErr != nil {
		t.Errorf("ledger removed although pid 5000 may still be this stack's: %v", statErr)
	}

	// The control: the same process, readable and carrying no marker, is
	// provably not this stack's — the stack is down and the records go.
	reg, ledger = stopTestRegistry(t, nil)
	reg.readStopTable = table(map[int][]string{5000: {"PATH=/usr/bin"}})
	out = captureStdout(t, func() { err = reg.shutdown() })
	if err != nil {
		t.Fatalf("shutdown = %v, want nil\n%s", err, out)
	}
	if _, statErr := os.Stat(ledger); !os.IsNotExist(statErr) {
		t.Errorf("ledger kept although the stack is proven down: %v", statErr)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParseAirStopDrain(t *testing.T) {
	cases := []struct {
		name string
		toml string
		want time.Duration
	}{
		{"duration string", "[build]\nsend_interrupt = true\nkill_delay = \"5s\"\n", 5 * time.Second},
		{"bare integer is milliseconds, as air reads it", "[build]\nsend_interrupt = true\nkill_delay = 1500\n", 1500 * time.Millisecond},
		{"no interrupt: air kills at once", "[build]\nsend_interrupt = false\nkill_delay = \"20s\"\n", 0},
		{"no kill_delay", "[build]\nsend_interrupt = true\n", 0},
		{"unparseable", "[build\n", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseAirStopDrain([]byte(tc.toml)); got != tc.want {
				t.Errorf("parseAirStopDrain = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestRequireStopGrace pins how a runner's declared drain sets the shutdown
// grace: never below the default, the longest drain plus a margin, capped,
// and a drain past the cap reported where the user will see it.
func TestRequireStopGrace(t *testing.T) {
	p := newProcRegistry(testProj, t.TempDir(), "dev")
	out := captureStdout(t, func() {
		p.requireStopGrace("api", 5*time.Second, "runner's kill_delay")
	})
	if p.stopGrace != 0 || out != "" {
		t.Errorf("a drain inside the default grace changed it to %s (output %q)", p.stopGrace, out)
	}

	p.requireStopGrace("api", 12*time.Second, "runner's kill_delay")
	p.requireStopGrace("worker", 8*time.Second, "preStopSeconds")
	if want := 12*time.Second + stopGraceMargin; p.stopGrace != want || !strings.Contains(p.graceFor, "api") {
		t.Errorf("grace = %s for %q, want %s for api", p.stopGrace, p.graceFor, want)
	}

	out = captureStdout(t, func() {
		p.requireStopGrace("admin", time.Minute, "preStopSeconds")
	})
	if p.stopGrace != maxStopGrace {
		t.Errorf("grace = %s, want the cap %s", p.stopGrace, maxStopGrace)
	}
	if !strings.Contains(out, "admin") || !strings.Contains(out, "SIGKILL") {
		t.Errorf("a drain past the cap was not reported: %q", out)
	}
}

// TestHostPreStopExtendsTheStopGrace pins what preStopSeconds means on the
// host runtime: nothing pauses before SIGTERM (no load balancer to leave), and
// forge waits the declared time before SIGKILL.
func TestHostPreStopExtendsTheStopGrace(t *testing.T) {
	if got := hostPreStop(WorkloadEntity{}); got != 0 {
		t.Errorf("unset preStopSeconds = %s, want 0", got)
	}
	zero := int32(0)
	if got := hostPreStop(WorkloadEntity{Spec: deployv1alpha1.WorkloadSpec{PreStopSeconds: &zero}}); got != 0 {
		t.Errorf("preStopSeconds 0 = %s, want 0", got)
	}
	n := int32(20)
	w := WorkloadEntity{Name: "proxy", Spec: deployv1alpha1.WorkloadSpec{PreStopSeconds: &n}}
	if got := hostPreStop(w); got != 20*time.Second {
		t.Fatalf("preStopSeconds 20 = %s, want 20s", got)
	}
	p := newProcRegistry(testProj, t.TempDir(), "dev")
	captureStdout(t, func() { p.requireStopGrace(w.Name, hostPreStop(w), "preStopSeconds") })
	if want := 20*time.Second + stopGraceMargin; p.stopGrace != want || !strings.Contains(p.graceFor, "preStopSeconds") {
		t.Errorf("grace = %s for %q, want %s for preStopSeconds", p.stopGrace, p.graceFor, want)
	}
}

// A teardown from outside the stack (`forge env down`, the next up's
// pre-flight) honours preStopSeconds too: the launch records the host
// workloads' stop grace, and stackTreeStop waits it out before SIGKILL.
func TestEnvDownHonoursTheRecordedPreStopGrace(t *testing.T) {
	n20, n60 := int32(20), int32(60)
	host := func(name string, n *int32) WorkloadEntity {
		w := WorkloadEntity{Name: name, Spec: deployv1alpha1.WorkloadSpec{PreStopSeconds: n}}
		w.Runtime.Type = RuntimeHost
		return w
	}
	if got := hostStopGrace(&KCLEntities{Workloads: []WorkloadEntity{host("api", nil)}}); got != 0 {
		t.Errorf("no preStopSeconds: grace %s, want 0", got)
	}
	cluster := host("edge", &n60)
	cluster.Runtime.Type = RuntimeCluster
	ents := &KCLEntities{Workloads: []WorkloadEntity{host("api", nil), host("proxy", &n20), cluster}}
	if got, want := hostStopGrace(ents), 20*time.Second+stopGraceMargin; got != want {
		t.Errorf("host proxy at 20s (cluster edge ignored): grace %s, want %s", got, want)
	}
	if got := hostStopGrace(&KCLEntities{Workloads: []WorkloadEntity{host("slow", &n60)}}); got != maxStopGrace {
		t.Errorf("60s: grace %s, want the %s cap", got, maxStopGrace)
	}

	for _, tc := range []struct {
		st   *resolvedEnvState
		want time.Duration
	}{
		{nil, treeStopGrace},
		{&resolvedEnvState{}, treeStopGrace},
		{&resolvedEnvState{StopGraceSeconds: 3}, treeStopGrace},
		{&resolvedEnvState{StopGraceSeconds: 23}, 23 * time.Second},
		{&resolvedEnvState{StopGraceSeconds: 90}, maxStopGrace},
	} {
		if got := recordedStopGrace(tc.st); got != tc.want {
			t.Errorf("recordedStopGrace(%+v) = %s, want %s", tc.st, got, tc.want)
		}
	}

	// The record round-trips through the env state file stackTreeStop reads.
	t.Setenv("HOME", t.TempDir())
	path, err := upEnvStatePath(testProj, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(resolvedEnvState{Env: "dev", StopGraceSeconds: 23})
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := recordedStopGrace(loadResolvedEnv(testProj, "dev")); got != 23*time.Second {
		t.Fatalf("grace read back = %s, want 23s", got)
	}
}
