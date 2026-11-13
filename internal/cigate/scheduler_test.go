package cigate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder is an ExecFunc that logs each command's start and end on one
// sequence, so a test can assert what overlapped, and lets a test hold a
// command open (gate) or fail it.
type recorder struct {
	mu     sync.Mutex
	seq    int
	start  map[string]int
	end    map[string]int
	env    map[string][]string
	active int
	peak   int
	fail   map[string]bool
	exit   map[string]error
	output map[string]string
	hold   time.Duration
	gate   map[string]chan struct{} // closed to let the command finish
}

func newRecorder() *recorder {
	return &recorder{start: map[string]int{}, end: map[string]int{}, env: map[string][]string{}, fail: map[string]bool{}, exit: map[string]error{}, output: map[string]string{}, gate: map[string]chan struct{}{}}
}

func (r *recorder) run(ctx context.Context, _ string, env []string, argv []string, out io.Writer) error {
	name := argv[0]
	r.mu.Lock()
	r.seq++
	r.start[name] = r.seq
	r.env[name] = env
	r.active++
	r.peak = max(r.peak, r.active)
	g := r.gate[name]
	r.mu.Unlock()
	if s := r.output[name]; s != "" {
		fmt.Fprint(out, s)
	}
	if g != nil {
		select {
		case <-g:
		case <-ctx.Done():
		}
	}
	if r.hold > 0 {
		time.Sleep(r.hold)
	}
	r.mu.Lock()
	r.seq++
	r.end[name] = r.seq
	r.active--
	r.mu.Unlock()
	if err := r.exit[name]; err != nil {
		return err
	}
	if r.fail[name] {
		return errors.New("boom")
	}
	return ctx.Err()
}

func (r *recorder) overlap(a, b string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.start[a] < r.end[b] && r.start[b] < r.end[a]
}

func (r *recorder) before(a, b string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.end[a] != 0 && r.end[a] < r.start[b]
}

func runChecks(t *testing.T, checks []Check, opt Options) Report {
	t.Helper()
	if opt.Out == nil {
		opt.Out = io.Discard
	}
	done := make(chan Report, 1)
	go func() { done <- Run(context.Background(), "checks", checks, opt) }()
	select {
	case rep := <-done:
		return rep
	case <-time.After(20 * time.Second):
		t.Fatal("the gate did not finish: a scheduling deadlock")
		return Report{}
	}
}

// `after` orders checks; a failed dependency still lets its dependents run
// (rule 1: every check runs).
func TestScheduler_AfterOrdersAndAFailureDoesNotStopDependents(t *testing.T) {
	r := newRecorder()
	r.fail["install"] = true
	r.hold = 5 * time.Millisecond
	b := check("build", "build")
	b.After = []string{"install"}
	l := check("lint", "lint")
	l.After = []string{"build"}
	rep := runChecks(t, []Check{l, b, check("install", "install")}, Options{Exec: r.run, Jobs: 4})
	if !r.before("install", "build") || !r.before("build", "lint") {
		t.Fatalf("after was not honored: start=%v end=%v", r.start, r.end)
	}
	st := statuses(rep)
	if st["install"] != Fail || st["build"] != Pass || st["lint"] != Pass {
		t.Fatalf("statuses %v: a failed dependency must not stop its dependents", st)
	}
}

// A tree-mutating check runs alone, ahead of every check that reads the tree,
// even when the plan lists a reader first; its own dependencies run before it.
func TestScheduler_MutatingCheckRunsAloneAndFirst(t *testing.T) {
	r := newRecorder()
	r.hold = 10 * time.Millisecond
	vg := check("regen", "regen")
	vg.MutatesTree = true
	vg.After = []string{"deps"}
	checks := []Check{check("lint", "lint"), check("deps", "deps"), vg, check("build", "build"), check("vet", "vet")}
	rep := runChecks(t, checks, Options{Exec: r.run, Jobs: 8})
	if rep.Failed() {
		t.Fatalf("rows: %+v", rep.Results)
	}
	for _, other := range []string{"lint", "deps", "build", "vet"} {
		if r.overlap("regen", other) {
			t.Errorf("regen overlapped %s: a check read a tree being rewritten", other)
		}
	}
	if !r.before("deps", "regen") {
		t.Error("regen's dependency must run before it")
	}
	for _, reader := range []string{"lint", "build", "vet"} {
		if !r.before("regen", reader) {
			t.Errorf("%s ran before the tree-mutating check finished", reader)
		}
	}
	// The readers, once released, run concurrently.
	if r.peak < 2 {
		t.Errorf("peak concurrency %d: independent checks should overlap", r.peak)
	}
}

// Light checks overlap up to the job cap and no further.
func TestScheduler_LightChecksBoundedByJobs(t *testing.T) {
	r := newRecorder()
	r.hold = 20 * time.Millisecond
	var checks []Check
	for i := 0; i < 8; i++ {
		checks = append(checks, check(fmt.Sprintf("c%d", i), fmt.Sprintf("c%d", i)))
	}
	runChecks(t, checks, Options{Exec: r.run, Jobs: 3})
	if r.peak > 3 || r.peak < 2 {
		t.Fatalf("peak %d with --jobs 3: want 2-3 concurrent light checks", r.peak)
	}
}

// fakeSlots is a host with n heavy slots.
type fakeSlots struct {
	sem    chan struct{}
	mu     sync.Mutex
	labels []string
}

type fakeSlot struct {
	s    *fakeSlots
	once sync.Once
}

func (f *fakeSlot) Env() []string { return []string{"FORGE_SLOT_HELD=heavy:0:test"} }
func (f *fakeSlot) SetLabel(l string) {
	f.s.mu.Lock()
	f.s.labels = append(f.s.labels, l)
	f.s.mu.Unlock()
}
func (f *fakeSlot) Release() error { f.once.Do(func() { <-f.s.sem }); return nil }

func (s *fakeSlots) take(ctx context.Context) (HeldSlot, error) {
	select {
	case s.sem <- struct{}{}:
		return &fakeSlot{s: s}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Heavy checks hold host slots — never more at once than the host has — and
// pass the slot to their command; light checks do not queue for one.
func TestScheduler_HeavyChecksHoldHostSlots(t *testing.T) {
	r := newRecorder()
	r.hold = 15 * time.Millisecond
	slots := &fakeSlots{sem: make(chan struct{}, 1)}
	var checks []Check
	for i := 0; i < 4; i++ {
		c := check(fmt.Sprintf("h%d", i), fmt.Sprintf("h%d", i))
		c.Heavy = true
		checks = append(checks, c)
	}
	// A light check runs beside a heavy one even with every slot taken:
	// every heavy command is held open until the light check has finished,
	// so a light check that queued for a slot would deadlock the gate.
	release := make(chan struct{})
	for i := 0; i < 4; i++ {
		r.gate[fmt.Sprintf("h%d", i)] = release
	}
	checks = append(checks, check("light", "light"))
	go func() {
		for {
			r.mu.Lock()
			lightDone := r.end["light"] != 0
			r.mu.Unlock()
			if lightDone {
				close(release)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	rep := runChecks(t, checks, Options{Exec: r.run, Jobs: 8, Slot: slots.take})
	if rep.Failed() {
		t.Fatalf("rows: %+v", rep.Results)
	}
	for i := 0; i < 4; i++ {
		for j := i + 1; j < 4; j++ {
			if r.overlap(fmt.Sprintf("h%d", i), fmt.Sprintf("h%d", j)) {
				t.Errorf("h%d and h%d overlapped on a one-slot host", i, j)
			}
		}
	}
	if got := strings.Join(r.env["h1"], " "); !strings.Contains(got, "FORGE_SLOT_HELD=heavy:0:test") {
		t.Errorf("the slot did not reach the check's command: %s", got)
	}
	if len(slots.labels) != 4 || !strings.HasPrefix(slots.labels[0], "ci gate: h") {
		t.Errorf("slot labels %v: waiters should see which check holds a slot", slots.labels)
	}
}

// A heavy check that holds a slot of its own runs its SlotArgv; without one
// (no limiter) it runs Argv. golangci-lint drops its machine-global lock this
// way only where the slot bounds it.
func TestScheduler_SlotArgvRunsOnlyInsideAnOwnSlot(t *testing.T) {
	lint := check("golangci-lint", "golangci-lint", "run", "--allow-serial-runners")
	lint.Heavy = true
	lint.SlotArgv = []string{"golangci-lint", "run", "--allow-parallel-runners"}
	for _, tc := range []struct {
		name string
		slot SlotFunc
		want string
	}{
		{"own slot", (&fakeSlots{sem: make(chan struct{}, 1)}).take, "golangci-lint run --allow-parallel-runners"},
		{"no limiter", nil, "golangci-lint run --allow-serial-runners"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var ran []string
			exec := func(_ context.Context, _ string, _ []string, argv []string, _ io.Writer) error {
				mu.Lock()
				ran = append(ran, strings.Join(argv, " "))
				mu.Unlock()
				return nil
			}
			rep := runChecks(t, []Check{lint}, Options{Exec: exec, Jobs: 2, Slot: tc.slot})
			if rep.Failed() {
				t.Fatalf("rows: %+v", rep.Results)
			}
			if len(ran) != 1 || ran[0] != tc.want {
				t.Fatalf("ran %q, want %q", ran, tc.want)
			}
		})
	}
}

// The gate files one slot request at a time and gives each freed slot to the
// waiting check earliest in execution order, not to whichever asked first.
func TestHeavyQueue_EarliestPositionFirst(t *testing.T) {
	free := make(chan struct{})
	slots := &fakeSlots{sem: make(chan struct{}, 8)}
	q := newHeavyQueue(context.Background(), func(ctx context.Context) (HeldSlot, error) {
		<-free // the host has no slot until the test frees one
		return slots.take(ctx)
	})
	defer q.close()
	got := make(chan int, 3)
	for _, pos := range []int{3, 1, 2} {
		go func() {
			s, err := q.take(pos, fmt.Sprint(pos))
			if err != nil {
				t.Error(err)
				return
			}
			got <- pos
			_ = s
		}()
	}
	waitUntil(t, func() bool { q.mu.Lock(); defer q.mu.Unlock(); return len(q.waiting) == 3 })
	var order []int
	for i := 0; i < 3; i++ {
		free <- struct{}{}
		order = append(order, <-got)
	}
	if fmt.Sprint(order) != "[1 2 3]" {
		t.Fatalf("grant order %v, want earliest position first", order)
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

// Interrupting the gate reports every check that never ran as SKIPPED, and
// the gate fails — it never reads as green.
func TestScheduler_InterruptSkipsWhatDidNotRun(t *testing.T) {
	r := newRecorder()
	r.gate["slow"] = make(chan struct{})
	after := check("after-slow", "after-slow")
	after.After = []string{"slow"}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			r.mu.Lock()
			started := r.start["slow"] != 0
			r.mu.Unlock()
			if started {
				cancel()
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	rep := Run(ctx, "checks", []Check{check("slow", "slow"), after}, Options{Out: io.Discard, Exec: r.run})
	st := statuses(rep)
	if st["slow"] != Skip || st["after-slow"] != Skip || !rep.Failed() {
		t.Fatalf("statuses %v failed=%v", st, rep.Failed())
	}
	if r.start["after-slow"] != 0 {
		t.Error("a check started after the gate was interrupted")
	}
}

// fakeVerdicts is an in-memory verdict cache.
type fakeVerdicts struct {
	mu       sync.Mutex
	have     map[string]bool
	recorded []string
}

func (f *fakeVerdicts) Lookup(c Check) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return "abc1234", f.have[c.Name]
}

func (f *fakeVerdicts) Record(c Check) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recorded = append(f.recorded, c.Name)
}

// A cached PASS answers without running; a fresh PASS is recorded; a FAIL, a
// Done short-circuit, a skip and a no-cache check never are.
func TestScheduler_VerdictCache(t *testing.T) {
	r := newRecorder()
	r.fail["broken"] = true
	v := &fakeVerdicts{have: map[string]bool{"cached": true, "uncacheable": true}}
	done := check("deps", "deps")
	done.Done = func() string { return "already installed" }
	nc := check("uncacheable", "uncacheable")
	nc.NoCache = true
	skipped := check("needs-tool", "needs-tool")
	skipped.Requires = []string{"definitely-not-installed-xyz"}
	dependent := check("after-cached", "after-cached")
	dependent.After = []string{"cached"}
	rep := runChecks(t, []Check{check("cached", "cached"), check("fresh", "fresh"), check("broken", "broken"), done, nc, skipped, dependent},
		Options{Exec: r.run, Verdicts: v, Env: []string{"PATH=" + t.TempDir()}})

	if _, ran := r.start["cached"]; ran {
		t.Error("a cached PASS ran again")
	}
	for _, res := range rep.Results {
		switch res.Name {
		case "cached":
			if res.Status != Pass || res.Cached != "abc1234" {
				t.Errorf("cached row = %+v", res)
			}
		case "uncacheable":
			if res.Cached != "" {
				t.Error("a no-cache check was answered from the cache")
			}
		}
	}
	if got := strings.Join(v.recorded, ","); !strings.Contains(got, "fresh") || !strings.Contains(got, "after-cached") ||
		strings.Contains(got, "broken") || strings.Contains(got, "deps") || strings.Contains(got, "uncacheable") || strings.Contains(got, "needs-tool") {
		t.Errorf("recorded %q: only PASSes a command earned, on cacheable checks", got)
	}
	var table bytes.Buffer
	rep.RenderText(&table)
	if !strings.Contains(table.String(), "PASS (cached abc1234)") || !strings.Contains(table.String(), "1 cached") {
		t.Errorf("the table must say which rows were cached:\n%s", table.String())
	}
}

// A command that exits with the check's skip code is SKIPPED with its last
// line as the reason; a failing probe skips without running the command.
func TestScheduler_SkipExitCodeAndProbe(t *testing.T) {
	r := newRecorder()
	r.exit["sibling"] = exitErr(77)
	r.output["sibling"] = "checking…\nno reliant checkout (set RELIANT_DIR)\n"
	r.fail["docker-info"] = true
	sib := check("needs-sibling", "sibling")
	sib.SkipExitCode = 77
	dock := check("needs-docker", "docker-build")
	dock.Probe, dock.ProbeReason = []string{"docker-info"}, "docker daemon not reachable"
	rep := runChecks(t, []Check{sib, dock}, Options{Exec: r.run})
	for _, res := range rep.Results {
		switch res.Name {
		case "needs-sibling":
			if res.Status != Skip || res.Note != "no reliant checkout (set RELIANT_DIR)" {
				t.Errorf("skip exit code: %+v", res)
			}
		case "needs-docker":
			if res.Status != Skip || res.Note != "docker daemon not reachable" {
				t.Errorf("probe: %+v", res)
			}
		}
	}
	if _, ran := r.start["docker-build"]; ran {
		t.Error("the command ran although its probe failed")
	}
	if !rep.Failed() {
		t.Error("an unwaived skip must fail the gate")
	}
}

type codedErr int

func (c codedErr) Error() string { return fmt.Sprintf("exit status %d", int(c)) }
func (c codedErr) ExitCode() int { return int(c) }

func exitErr(code int) error { return codedErr(code) }

// requires resolves against the check's own PATH, which ci.gate.path heads.
func TestScheduler_RequiresSeesGatePath(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("executable-bit fixture")
	}
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "yq-xyz"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := check("kcl", "kcl")
	c.Requires = []string{"yq-xyz"}
	r := newRecorder()
	rep := runChecks(t, []Check{c}, Options{Exec: r.run, Env: []string{"PATH=/nonexistent"}, Path: []string{tools}})
	if st := statuses(rep)["kcl"]; st != Pass {
		t.Fatalf("kcl = %s: a tool in ci.gate.path must satisfy requires", st)
	}
	if path := strings.Join(r.env["kcl"], " "); !strings.Contains(path, "PATH="+tools+string(os.PathListSeparator)+"/nonexistent") {
		t.Errorf("ci.gate.path must come first on PATH: %s", path)
	}
}

// Each check's output lands in its own log; the console carries one line per
// start and finish and a failure's tail, never two checks' output interleaved.
func TestScheduler_LogsAndConsole(t *testing.T) {
	r := newRecorder()
	r.fail["b"] = true
	r.output["a"] = "alpha output\n"
	r.output["b"] = "beta failed here\n"
	dir := t.TempDir()
	var console bytes.Buffer
	rep := runChecks(t, []Check{check("a", "a"), check("b", "b")}, Options{Exec: r.run, LogDir: filepath.Join(dir, "logs"), Root: dir, Out: &console})
	if b, _ := os.ReadFile(filepath.Join(dir, "logs", "a.log")); string(b) != "alpha output\n" {
		t.Errorf("a.log = %q", b)
	}
	out := console.String()
	if strings.Contains(out, "alpha output") {
		t.Errorf("a passing check's output belongs in its log, not the console:\n%s", out)
	}
	for _, want := range []string{"▶ a", "✓ a  PASS", "✗ b  FAIL", "    | beta failed here", "log: logs/b.log"} {
		if !strings.Contains(out, want) {
			t.Errorf("console lacks %q:\n%s", want, out)
		}
	}
	if res := rep.Results[1]; res.Log != "logs/b.log" {
		t.Errorf("result log = %q", res.Log)
	}
}

// CI_ONLY keeps the named checks and groups plus what they depend on.
func TestOnly(t *testing.T) {
	a := check("deps", "x")
	b := check("render", "x")
	b.Group, b.After = "kcl", []string{"deps"}
	c := check("lint", "x")
	c.Group = "static"
	got := names(Only([]Check{a, b, c}, []string{"kcl"}))
	if strings.Join(got, ",") != "deps,render" {
		t.Errorf("Only(kcl) = %v, want the group and its dependency", got)
	}
	if bad := UnknownOnly([]string{"kcl", "kcll"}, []Check{a, b, c}); len(bad) != 1 || bad[0] != "kcll" {
		t.Errorf("UnknownOnly = %v", bad)
	}
}

// A dependency cycle that slipped past the planner degrades to plan order
// rather than hanging the gate.
func TestExecutionOrder_CycleDoesNotHang(t *testing.T) {
	a, b := check("a", "a"), check("b", "b")
	a.After, b.After = []string{"b"}, []string{"a"}
	r := newRecorder()
	rep := runChecks(t, []Check{a, b}, Options{Exec: r.run})
	if rep.Failed() {
		t.Fatalf("rows: %+v", rep.Results)
	}
}
