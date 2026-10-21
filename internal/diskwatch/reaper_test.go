// Copyright (c) 2025 Reliant Labs
package diskwatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testBlocks    = 10 << 20 // tempCacheMachine's 40 GiB of 4 KiB blocks
	plentyOfSpace = testBlocks * 3 / 4
)

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// reaperHarness is a Watcher over a fake filesystem with an injected reaper
// runner and a clock the test moves.
type reaperHarness struct {
	t      *testing.T
	w      *Watcher
	fs     *fakeFS
	cache  string
	policy string
	now    time.Time
	logs   *bytes.Buffer

	mu    sync.Mutex
	calls []string // policy path of each run
	run   func(ctx context.Context, policy string) (reaperRun, error)
}

func newReaperHarness(t *testing.T, bavail uint64) *reaperHarness {
	t.Helper()
	h := &reaperHarness{t: t, now: time.Now()}
	h.logs = captureLogs(t)
	fs, env, cache := tempCacheMachine(t, bavail)
	h.fs, h.cache = fs, cache
	h.policy = filepath.Join(t.TempDir(), "forge", "storage.json")
	h.w = watcherOn(t, fs, env, &h.now)
	h.w.reaper.run = func(ctx context.Context, policy string) (reaperRun, error) {
		h.mu.Lock()
		h.calls = append(h.calls, policy)
		run := h.run
		h.mu.Unlock()
		if run == nil {
			return reaperRun{}, nil
		}
		return run(ctx, policy)
	}
	h.w.ConfigureReaper(ReaperConfig{Enabled: true, Interval: time.Hour, PressureInterval: 10 * time.Minute, PolicyPath: h.policy})
	// Sample the volumes so the pressure bit is current, and let the consumer
	// scan a pressured volume starts finish before the test moves the clock.
	h.w.poll(context.Background())
	if bavail == 0 {
		waitScanned(t, h.w)
	}
	return h
}

func (h *reaperHarness) runs() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

func (h *reaperHarness) advance(d time.Duration) { h.now = h.now.Add(d) }

func (h *reaperHarness) setFree(bavail uint64) {
	m := h.fs.mounts[h.cache]
	m.Bfree, m.Bavail = bavail, bavail
	h.fs.mounts[h.cache] = m
	h.w.poll(context.Background())
}

// writeForgeRecord writes forge's last-auto-gc.json beside the policy.
func (h *reaperHarness) writeForgeRecord(rec autoGCRecord) {
	h.t.Helper()
	b, err := json.Marshal(rec)
	if err != nil {
		h.t.Fatal(err)
	}
	h.writeForgeRecordRaw(b)
}

func (h *reaperHarness) writeForgeRecordRaw(b []byte) {
	h.t.Helper()
	if err := os.MkdirAll(filepath.Dir(h.policy), 0o700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(h.policy), autoGCRecordName), b, 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *reaperHarness) tick() bool { return h.w.reaperTick(context.Background()) }

func (h *reaperHarness) logged(want string) bool { return strings.Contains(h.logs.String(), want) }

func TestReaper_RunsUnderPressureAtThePressureInterval(t *testing.T) {
	h := newReaperHarness(t, 0)

	if !h.tick() || h.runs() != 1 {
		t.Fatalf("a volume the reaper can relieve is under pressure and nothing has run: want a pass, runs=%d", h.runs())
	}
	if h.calls[0] != h.policy {
		t.Errorf("reaper was handed policy %q, want %q", h.calls[0], h.policy)
	}
	h.advance(5 * time.Minute)
	if h.tick() {
		t.Fatal("5m after a pass, inside the 10m pressure interval: must not run again")
	}
	h.advance(5 * time.Minute)
	if !h.tick() || h.runs() != 2 {
		t.Fatalf("10m after the last pass, still under pressure: want a second pass, runs=%d", h.runs())
	}
	if !h.logged("trigger=pressure") {
		t.Errorf("the pass should be logged as pressure-triggered:\n%s", h.logs)
	}
}

func TestReaper_RunsOnTheSlowIntervalWithoutPressure(t *testing.T) {
	h := newReaperHarness(t, plentyOfSpace)

	if !h.tick() {
		t.Fatal("first tick on a machine that has never run the reaper: the budget pass is due")
	}
	h.advance(30 * time.Minute)
	if h.tick() {
		t.Fatal("plenty of space and 30m since the last pass: nothing to do, must be a no-op")
	}
	h.advance(31 * time.Minute)
	if !h.tick() || h.runs() != 2 {
		t.Fatalf("61m since the last pass, inside no pressure: the hourly budget pass is due, runs=%d", h.runs())
	}
	if !h.logged("trigger=scheduled") {
		t.Errorf("the pass should be logged as scheduled:\n%s", h.logs)
	}
}

func TestReaper_PlentyOfSpaceNeverUsesThePressureInterval(t *testing.T) {
	h := newReaperHarness(t, plentyOfSpace)
	h.tick()
	for i := 0; i < 5; i++ {
		h.advance(11 * time.Minute) // past the pressure interval, inside the slow one
		if h.tick() {
			t.Fatalf("after %dm with plenty of space the pressure interval must not apply", 11*(i+1))
		}
	}
	if h.runs() != 1 {
		t.Fatalf("runs = %d, want 1", h.runs())
	}
}

func TestReaper_PressureOnASystemOnlyVolumeIsNotRelievable(t *testing.T) {
	h := newReaperHarness(t, plentyOfSpace)
	h.w.mu.Lock()
	h.w.vols = []*volume{{key: 99, members: []member{{Target: Target{Path: "/usr", Role: RoleSystem}}}}}
	h.w.pressure = map[uint64]bool{99: true}
	h.w.mu.Unlock()
	if h.w.reclaimablePressure() {
		t.Fatal("the /etc /usr /opt /var overlays hold nothing the reaper removes: their pressure is not a reason to run it")
	}
}

func TestReaper_AnchoredOnForgesOwnRecord(t *testing.T) {
	h := newReaperHarness(t, 0)
	// A pass forge ran 3m ago (a `forge env up`, another daemon, a restart of
	// this one): this process has run nothing, but the machine has.
	h.writeForgeRecord(autoGCRecord{At: h.now.Add(-3 * time.Minute), OK: true})
	if h.tick() {
		t.Fatal("forge's own record says a pass ran 3m ago: must not start another under the 10m pressure interval")
	}
	h.advance(8 * time.Minute)
	if !h.tick() {
		t.Fatal("11m after forge's recorded pass: due")
	}
}

func TestReaper_SingleFlight(t *testing.T) {
	h := newReaperHarness(t, 0)
	started, release := make(chan struct{}), make(chan struct{})
	h.run = func(ctx context.Context, _ string) (reaperRun, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return reaperRun{}, nil
	}

	done := make(chan bool)
	go func() { done <- h.tick() }()
	<-started

	if h.tick() {
		t.Fatal("a second tick while a pass is running must not start another")
	}
	// A user's confirmed clean-up queues behind the running pass instead of
	// running a second one beside it.
	if started, _ := h.w.ClearCaches([]string{KindGoBuildCache}); len(started) != 1 {
		t.Fatalf("ClearCaches started %v, want the Go build cache", started)
	}
	time.Sleep(50 * time.Millisecond)
	if h.runs() != 1 {
		t.Fatalf("runs = %d while the first pass is still running, want 1", h.runs())
	}
	h.mu.Lock()
	h.run = nil
	h.mu.Unlock()
	close(release)
	if !<-done {
		t.Fatal("the first tick ran a pass")
	}
	waitCleared(t, h.w)
	if h.runs() != 2 {
		t.Fatalf("after release the queued clean-up should have run once: runs=%d", h.runs())
	}
}

func TestReaper_TimeoutStopsThePassAndStillBacksOff(t *testing.T) {
	h := newReaperHarness(t, 0)
	h.w.reaper.budget = 40 * time.Millisecond
	h.run = func(ctx context.Context, _ string) (reaperRun, error) {
		<-ctx.Done()
		return reaperRun{}, ctx.Err()
	}
	begin := time.Now()
	if !h.tick() {
		t.Fatal("tick should have started a pass")
	}
	if took := time.Since(begin); took > 5*time.Second {
		t.Fatalf("the pass outlived its budget: %s", took)
	}
	if !h.logged("Storage reaper pass stopped") || !h.logged("level=WARN") {
		t.Errorf("a timed-out pass is a WARN:\n%s", h.logs)
	}
	// A wedged reaper must be retried at the interval, not on every poll.
	if h.tick() {
		t.Fatal("a pass that timed out must still count as an attempt")
	}
}

func TestReaper_DisabledDoesNothing(t *testing.T) {
	h := newReaperHarness(t, 0)
	h.w.ConfigureReaper(ReaperConfig{Enabled: false, Interval: time.Hour, PressureInterval: time.Minute, PolicyPath: h.policy})
	if h.tick() {
		t.Fatal("a disabled routine must not run a pass, even under pressure")
	}
	done := make(chan struct{})
	go func() { h.w.RunReaper(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunReaper must return at once when disabled")
	}
	if h.runs() != 0 {
		t.Fatalf("runs = %d, want 0", h.runs())
	}
	// A user's confirmed clean-up is explicit, so it runs whatever the schedule says.
	h.w.ClearCaches([]string{KindGoBuildCache})
	waitCleared(t, h.w)
	if h.runs() != 1 {
		t.Fatalf("ClearCaches must run the reaper even when the schedule is off: runs=%d", h.runs())
	}
}

func TestReaper_RunReaperTicksUntilCancelled(t *testing.T) {
	h := newReaperHarness(t, plentyOfSpace)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.w.RunReaper(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for h.runs() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunReaper did not stop on cancel")
	}
	if h.runs() != 1 {
		t.Fatalf("runs = %d, want exactly the first scheduled pass", h.runs())
	}
}

func TestReaper_MalformedSummaryIsWarnedNotTrusted(t *testing.T) {
	cases := map[string]func(h *reaperHarness){
		"not json":      func(h *reaperHarness) { h.writeForgeRecordRaw([]byte("{not json")) },
		"no record":     func(h *reaperHarness) {},
		"stale record":  func(h *reaperHarness) { h.writeForgeRecord(autoGCRecord{At: h.now.Add(-48 * time.Hour), OK: true}) },
		"wrong shape":   func(h *reaperHarness) { h.writeForgeRecordRaw([]byte(`["a","b"]`)) },
		"empty file":    func(h *reaperHarness) { h.writeForgeRecordRaw(nil) },
		"policy absent": func(h *reaperHarness) { h.policy = "" },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			h := newReaperHarness(t, plentyOfSpace)
			setup(h)
			res := h.w.reaperPass(context.Background(), h.policy, "scheduled")
			if !res.Ran {
				t.Fatalf("exit 0 means the pass ran: %+v", res)
			}
			if len(res.CutOff)+len(res.BackedOff)+len(res.Failed) != 0 {
				t.Errorf("nothing may be read out of a summary that is not this pass's: %+v", res)
			}
			if !h.logged("summary is unreadable") || !h.logged("level=WARN") {
				t.Errorf("an unreadable summary must be a WARN, not a clean INFO:\n%s", h.logs)
			}
			if h.logged("Storage reaper pass finished level") || strings.Contains(h.logs.String(), "level=INFO msg=\"[diskwatch] Storage reaper pass finished\"") {
				t.Errorf("must not report success without a summary:\n%s", h.logs)
			}
		})
	}
}

func TestReaper_LogsWhatForgeSkippedAndWhatItFreed(t *testing.T) {
	h := newReaperHarness(t, plentyOfSpace)
	h.run = func(_ context.Context, _ string) (reaperRun, error) {
		h.setFree(plentyOfSpace + 25000) // 25000 blocks of 4 KiB freed
		h.writeForgeRecord(autoGCRecord{At: h.now, OK: true, CutOffLayers: []string{"go caches: shared build cache"}, BackedOffLayers: []string{"temp sweep"}})
		return reaperRun{}, nil
	}
	res := h.w.reaperPass(context.Background(), h.policy, "scheduled")
	if want := uint64(25000 * 4096); res.Freed != want {
		t.Errorf("Freed = %d, want %d (the growth of free space on the volumes the reaper relieves)", res.Freed, want)
	}
	if !reflect.DeepEqual(res.CutOff, []string{"go caches: shared build cache"}) || !reflect.DeepEqual(res.BackedOff, []string{"temp sweep"}) {
		t.Errorf("skipped layers = cutOff %v backedOff %v", res.CutOff, res.BackedOff)
	}
	for _, want := range []string{"level=INFO", "Storage reaper pass finished", `freed="102 MB"`, "go caches: shared build cache", "temp sweep"} {
		if !h.logged(want) {
			t.Errorf("INFO line missing %q:\n%s", want, h.logs)
		}
	}
}

// forge records a Go cache it cannot trim to budget — more than the budget is
// younger than its 2h safety floor — under over_budget, and the pass is still
// OK. The daemon must say so at WARN: that finding is why a 20 GiB budget held
// a 152 GiB cache with every pass logged as a clean INFO.
func TestReaper_WarnsOnABudgetNoTrimCanReach(t *testing.T) {
	h := newReaperHarness(t, plentyOfSpace)
	finding := "shared go build cache is over its budget and no trim can reach it: ~152.0 GiB against 20.0 GiB"
	h.run = func(context.Context, string) (reaperRun, error) {
		h.writeForgeRecord(autoGCRecord{At: h.now, OK: true, OverBudget: []string{finding}})
		return reaperRun{}, nil
	}
	res := h.w.reaperPass(context.Background(), h.policy, "scheduled")
	if !reflect.DeepEqual(res.OverBudget, []string{finding}) {
		t.Errorf("OverBudget = %v", res.OverBudget)
	}
	for _, want := range []string{"level=WARN", "over a budget no trim can reach", "152.0 GiB", "Storage reaper pass finished"} {
		if !h.logged(want) {
			t.Errorf("log missing %q:\n%s", want, h.logs)
		}
	}
}

func TestReaper_FailureIsAWarnNotACrash(t *testing.T) {
	t.Run("non-zero exit with a record", func(t *testing.T) {
		h := newReaperHarness(t, plentyOfSpace)
		h.run = func(context.Context, string) (reaperRun, error) {
			h.writeForgeRecord(autoGCRecord{At: h.now, OK: false, FailedLayers: []string{"docker"}, Error: "docker: cannot connect"})
			return reaperRun{ExitCode: 1, Stderr: "boom"}, nil
		}
		res := h.w.reaperPass(context.Background(), h.policy, "scheduled")
		if !reflect.DeepEqual(res.Failed, []string{"docker"}) {
			t.Errorf("Failed = %v", res.Failed)
		}
		for _, want := range []string{"level=WARN", "Storage reaper pass failed", "exit=1", "docker", "boom"} {
			if !h.logged(want) {
				t.Errorf("WARN line missing %q:\n%s", want, h.logs)
			}
		}
	})
	t.Run("another pass holds forge's lock", func(t *testing.T) {
		h := newReaperHarness(t, plentyOfSpace)
		h.run = func(context.Context, string) (reaperRun, error) {
			return reaperRun{ExitCode: 1, Stderr: "storage maintenance is already running: resource temporarily unavailable"}, nil
		}
		h.w.reaperPass(context.Background(), h.policy, "scheduled")
		if !h.logged("level=INFO") || !h.logged("holds forge's lock") || h.logged("level=WARN") {
			t.Errorf("a busy lock is someone else doing the work: INFO, not a failure:\n%s", h.logs)
		}
	})
	t.Run("the runner panics", func(t *testing.T) {
		h := newReaperHarness(t, plentyOfSpace)
		h.run = func(context.Context, string) (reaperRun, error) { panic("kaboom") }
		h.w.reaperPass(context.Background(), h.policy, "scheduled")
		if !h.logged("panicked") || !h.logged("kaboom") {
			t.Errorf("a panic in the pass must be logged and contained:\n%s", h.logs)
		}
	})
}

func TestReaper_ForgeMissingIsAWarnAndRetriedAtTheInterval(t *testing.T) {
	h := newReaperHarness(t, 0)
	h.run = func(context.Context, string) (reaperRun, error) {
		return reaperRun{}, &exec.Error{Name: "reliant", Err: exec.ErrNotFound}
	}
	if !h.tick() {
		t.Fatal("tick should have attempted a pass")
	}
	if !h.logged("level=WARN") || !h.logged("could not run") {
		t.Errorf("a reaper that cannot be run is a WARN:\n%s", h.logs)
	}
	if h.tick() {
		t.Fatal("a failing reaper must not be retried on every poll")
	}
	h.advance(10 * time.Minute)
	if !h.tick() {
		t.Fatal("it is retried once the pressure interval has passed")
	}
}

func TestExecReaperRefusesToRunTheTestBinary(t *testing.T) {
	_, err := execReaper(context.Background(), filepath.Join(t.TempDir(), "storage.json"))
	if !errors.Is(err, errReaperUnderTest) {
		t.Fatalf("execReaper under go test = %v, want errReaperUnderTest (os.Executable is the test binary)", err)
	}
}

func TestReaperCommandArgs(t *testing.T) {
	got := reaperCommandArgs("/var/cache/workspace/forge-storage.json")
	want := []string{"forge", "storage", "auto-gc", "--policy", "/var/cache/workspace/forge-storage.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
}

func TestRunReaperProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	t.Run("captures output and the exit code", func(t *testing.T) {
		run, err := runReaperProcess(context.Background(), "/bin/sh", []string{"-c", "echo out; echo err >&2; exit 3"})
		if err != nil {
			t.Fatal(err)
		}
		if run.ExitCode != 3 || run.Stdout != "out" || run.Stderr != "err" {
			t.Fatalf("run = %+v", run)
		}
	})
	t.Run("a command that does not exist is an error", func(t *testing.T) {
		if _, err := runReaperProcess(context.Background(), filepath.Join(t.TempDir(), "no-such-binary"), nil); err == nil {
			t.Fatal("want an error")
		}
	})
	t.Run("the context stops the whole process group", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		begin := time.Now()
		_, err := runReaperProcess(ctx, "/bin/sh", []string{"-c", "sleep 60 & wait"})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want the deadline", err)
		}
		if took := time.Since(begin); took > 10*time.Second {
			t.Fatalf("took %s: the child outlived its budget", took)
		}
	})
}

func TestTailBufferKeepsTheEnd(t *testing.T) {
	b := &tailBuffer{max: 8}
	for _, s := range []string{"0123", "4567", "89ab"} {
		if n, err := b.Write([]byte(s)); err != nil || n != 4 {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	if got := b.String(); got != "456789ab" {
		t.Fatalf("tail = %q, want the last 8 bytes", got)
	}
}

func TestParseReaperConfig(t *testing.T) {
	cases := []struct {
		name                     string
		mode, interval, pressure string
		managed                  bool
		wantEnabled              bool
		wantInterval, wantPress  time.Duration
		wantWarnings             int
	}{
		{"auto on a managed daemon", "auto", "", "", true, true, time.Hour, 10 * time.Minute, 0},
		{"auto on a person's machine", "auto", "", "", false, false, time.Hour, 10 * time.Minute, 0},
		{"unset is auto", "", "", "", true, true, time.Hour, 10 * time.Minute, 0},
		{"on forces it", "on", "", "", false, true, time.Hour, 10 * time.Minute, 0},
		{"off wins on a managed daemon", "off", "", "", true, false, time.Hour, 10 * time.Minute, 0},
		{"mode is case and space tolerant", " OFF ", "", "", true, false, time.Hour, 10 * time.Minute, 0},
		{"an unknown mode warns and means auto", "sometimes", "", "", true, true, time.Hour, 10 * time.Minute, 1},
		{"interval override", "on", "2h", "30m", false, true, 2 * time.Hour, 30 * time.Minute, 0},
		{"intervals below the floor are raised", "on", "10s", "1s", false, true, MinReaperInterval, MinReaperPressureInterval, 2},
		{"an unparsable interval falls back with a warning", "on", "soon", "later", false, true, time.Hour, 10 * time.Minute, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, warnings := ParseReaperConfig(tc.mode, tc.interval, tc.pressure, "/p/storage.json", tc.managed)
			if cfg.Enabled != tc.wantEnabled || cfg.Interval != tc.wantInterval || cfg.PressureInterval != tc.wantPress {
				t.Errorf("cfg = %+v, want enabled=%t interval=%s pressure=%s", cfg, tc.wantEnabled, tc.wantInterval, tc.wantPress)
			}
			if len(warnings) != tc.wantWarnings {
				t.Errorf("warnings = %v, want %d", warnings, tc.wantWarnings)
			}
			if cfg.PolicyPath != "/p/storage.json" {
				t.Errorf("PolicyPath = %q", cfg.PolicyPath)
			}
		})
	}
}

func TestReaperPolicyPathDefaultsToForgesOwn(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	w := New(Options{Env: Environment{Getenv: func(string) string { return "" }}, noDocker: true})
	if runtime.GOOS == "linux" {
		if got, want := w.reaperPolicyPath(ReaperConfig{}), filepath.Join(dir, "forge", "storage.json"); got != want {
			t.Fatalf("policy = %q, want forge's default %q", got, want)
		}
	}
	if got := w.reaperPolicyPath(ReaperConfig{PolicyPath: "/x/storage.json"}); got != "/x/storage.json" {
		t.Fatalf("explicit policy = %q", got)
	}
}

// Reclaim is the disk guard's built-in reclaimer: one reaper pass at once,
// whatever the schedule says, reporting the space it measured freed.
func TestReclaim_RunsOneReaperPassAndSaysWhatItFreed(t *testing.T) {
	h := newReaperHarness(t, 0)
	h.writeForgeRecord(autoGCRecord{At: h.now, OK: true}) // the schedule would wait an hour
	h.run = func(context.Context, string) (reaperRun, error) {
		m := h.fs.mounts[h.cache]
		m.Bfree, m.Bavail = plentyOfSpace, plentyOfSpace
		h.fs.mounts[h.cache] = m
		return reaperRun{}, nil
	}
	freed, summary, err := h.w.Reclaim(context.Background())
	if err != nil || h.runs() != 1 {
		t.Fatalf("Reclaim: err %v, runs %d; want one pass", err, h.runs())
	}
	if freed == 0 || !strings.HasPrefix(summary, "build caches and docker ") {
		t.Fatalf("Reclaim = %d, %q; want the freed space measured and named", freed, summary)
	}
	if rep, ok := h.w.Report(); !ok || len(UnderPressure(rep)) > 0 {
		t.Fatalf("the report was not re-measured after the pass: %v", rep)
	}

	h.run = func(context.Context, string) (reaperRun, error) { return reaperRun{}, errors.New("no forge") }
	if _, _, err := h.w.Reclaim(context.Background()); err == nil {
		t.Fatal("a pass that could not run must say so")
	}
}
