package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeUpClock is a clock the test advances by hand, so every offset and
// duration below is exact.
type fakeUpClock struct{ at time.Time }

func newFakeUpClock() *fakeUpClock {
	return &fakeUpClock{at: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
}
func (c *fakeUpClock) now() time.Time          { return c.at }
func (c *fakeUpClock) advance(d time.Duration) { c.at = c.at.Add(d) }

func phaseByName(t *testing.T, phases []upPhaseRecord, name string) upPhaseRecord {
	t.Helper()
	for _, p := range phases {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no phase %q in %+v", name, phases)
	return upPhaseRecord{}
}

// Overlapping spans keep their own start offsets and durations: the infra
// pre-warm beside the build is two spans that overlap, never a sum.
func TestUpTimingsRecordsOverlapAsOffsets(t *testing.T) {
	clk := newFakeUpClock()
	tm := newUpTimings(clk.now)
	ctx := withUpTimings(context.Background(), tm)

	clk.advance(1 * time.Second)
	_, build := startUpSpan(ctx, "build")
	clk.advance(500 * time.Millisecond)
	_, infra := startUpSpan(ctx, "infra")
	clk.advance(2 * time.Second)
	infra.end(nil)
	clk.advance(3 * time.Second)
	build.end(nil)
	clk.advance(1 * time.Second)

	total, phases := tm.finish()
	if total != 7500*time.Millisecond {
		t.Fatalf("total = %s, want 7.5s", total)
	}
	if len(phases) != 2 || phases[0].Name != "build" || phases[1].Name != "infra" {
		t.Fatalf("phases not in start order: %+v", phases)
	}
	if b := phases[0]; b.StartMS != 1000 || b.DurationMS != 5500 || b.Status != upSpanOK {
		t.Errorf("build = %+v, want start 1000 duration 5500 ok", b)
	}
	if i := phases[1]; i.StartMS != 1500 || i.DurationMS != 2000 {
		t.Errorf("infra = %+v, want start 1500 duration 2000", i)
	}
}

// Spans opened from a span's context nest under it, by full path, and a span
// recorded after the fact (a build dispatched in parallel) lands under the
// stage it ran in.
func TestUpTimingsNestsThroughContext(t *testing.T) {
	clk := newFakeUpClock()
	tm := newUpTimings(clk.now)
	ctx := withUpTimings(context.Background(), tm)

	buildCtx, build := startUpSpan(ctx, "build")
	goCtx, stage := startUpSpan(buildCtx, "go")
	started := upNow(goCtx)
	clk.advance(750 * time.Millisecond)
	recordUpSpan(goCtx, "api (docker)", started, "", nil)
	stage.end(nil)
	build.end(nil)

	_, phases := tm.finish()
	img := phaseByName(t, phases, "build/go/api (docker)")
	if img.Parent != "build/go" || img.DurationMS != 750 {
		t.Errorf("image span = %+v, want parent build/go, 750ms", img)
	}
	if p := phaseByName(t, phases, "build/go"); p.Parent != "build" {
		t.Errorf("stage parent = %q, want build", p.Parent)
	}
	if p := phaseByName(t, phases, "build"); p.Parent != "" {
		t.Errorf("top-level parent = %q, want empty", p.Parent)
	}
}

// A failed span says so with its error's first line; a span the run never
// closed is incomplete; skipped and discarded spans are honoured; and a
// second end does not overwrite the first.
func TestUpTimingsStatuses(t *testing.T) {
	clk := newFakeUpClock()
	tm := newUpTimings(clk.now)
	ctx := withUpTimings(context.Background(), tm)

	_, failed := startUpSpan(ctx, "deploy")
	failed.end(errors.New("rollout timed out\nsecond line"))
	failed.end(nil)
	_, open := startUpSpan(ctx, "readiness")
	_, skipped := startUpSpan(ctx, "shell")
	skipped.endWith(upSpanSkipped, nil)
	_, dropped := startUpSpan(ctx, "variants")
	dropped.end(nil)
	dropped.discard()
	clk.advance(time.Second)
	_ = open

	_, phases := tm.finish()
	if len(phases) != 3 {
		t.Fatalf("want 3 phases (variants discarded), got %+v", phases)
	}
	if p := phaseByName(t, phases, "deploy"); p.Status != upSpanFailed || p.Error != "rollout timed out" {
		t.Errorf("deploy = %+v, want failed with first error line", p)
	}
	if p := phaseByName(t, phases, "readiness"); p.Status != upSpanIncomplete || p.DurationMS != 1000 {
		t.Errorf("readiness = %+v, want incomplete closed at finish", p)
	}
	if p := phaseByName(t, phases, "shell"); p.Status != upSpanSkipped {
		t.Errorf("shell = %+v, want skipped", p)
	}
	if _, again := tm.finish(); again != nil {
		t.Errorf("second finish returned phases %+v, want nil", again)
	}
}

// A context with no recorder — `forge env build` — gets nil spans whose
// methods are no-ops, so the build path needs no "am I under up" checks.
func TestUpTimingsWithoutRecorderIsNoOp(t *testing.T) {
	ctx := context.Background()
	got, s := startUpSpan(ctx, "build")
	if s != nil || got != ctx {
		t.Fatalf("startUpSpan without a recorder = (%v, %v), want (ctx, nil)", got, s)
	}
	s.end(errors.New("x"))
	s.discard()
	recordUpSpan(ctx, "x", time.Time{}, "", nil)
	markUpLaunched(ctx, "api")
	r := timedBuild(ctx, func() buildResult { return buildResult{name: "api"} })
	if r.name != "api" {
		t.Fatalf("timedBuild changed the result: %+v", r)
	}
	newUpReadyTimer(ctx).observe([]hostReadyResult{{name: "api", state: portReadyOurs}})
}

// A stage that built nothing is dropped; one with a failed build is failed;
// each build is a child span named after its result.
func TestTimedBuildStage(t *testing.T) {
	clk := newFakeUpClock()
	tm := newUpTimings(clk.now)
	ctx := withUpTimings(context.Background(), tm)

	timedBuildStage(ctx, "variants", func(context.Context) []buildResult { return nil })
	timedBuildStage(ctx, "images", func(ctx context.Context) []buildResult {
		ok := timedBuild(ctx, func() buildResult { clk.advance(2 * time.Second); return buildResult{name: "web (docker)"} })
		bad := timedBuild(ctx, func() buildResult {
			clk.advance(time.Second)
			return buildResult{name: "api (docker)", err: errors.New("docker build failed")}
		})
		return []buildResult{ok, bad}
	})

	_, phases := tm.finish()
	for _, p := range phases {
		if p.Name == "variants" {
			t.Fatalf("empty stage was recorded: %+v", p)
		}
	}
	if p := phaseByName(t, phases, "images"); p.Status != upSpanFailed || p.DurationMS != 3000 {
		t.Errorf("images = %+v, want failed, 3000ms", p)
	}
	if p := phaseByName(t, phases, "images/web (docker)"); p.Status != upSpanOK || p.DurationMS != 2000 || p.StartMS != 0 {
		t.Errorf("web = %+v", p)
	}
	if p := phaseByName(t, phases, "images/api (docker)"); p.Status != upSpanFailed || p.StartMS != 2000 || p.DurationMS != 1000 {
		t.Errorf("api = %+v", p)
	}
}

// Time-to-ready runs from each workload's launch to the first poll that
// found all of its ports ours; whatever the gate gave up on is failed.
func TestUpReadyTimer(t *testing.T) {
	clk := newFakeUpClock()
	tm := newUpTimings(clk.now)
	ctx := withUpTimings(context.Background(), tm)

	markUpLaunched(ctx, "api")
	clk.advance(time.Second)
	markUpLaunched(ctx, "worker")
	clk.advance(time.Second)
	readyCtx, gate := startUpSpan(ctx, "readiness")
	rt := newUpReadyTimer(readyCtx)

	clk.advance(3 * time.Second)
	rt.observe([]hostReadyResult{
		{name: "api", port: 8080, state: portReadyOurs},
		{name: "api", port: 8081, state: portReadyNobody},
		{name: "worker", state: portReadyOurs},
	})
	clk.advance(2 * time.Second)
	rt.observe([]hostReadyResult{
		{name: "api", port: 8080, state: portReadyOurs},
		{name: "api", port: 8081, state: portReadyNobody},
		{name: "worker", state: portReadyOurs},
	})
	gateErr := errors.New("[up] host service(s) never came up")
	rt.close(gateErr)
	gate.end(gateErr)

	_, phases := tm.finish()
	if p := phaseByName(t, phases, "readiness/worker"); p.Status != upSpanOK || p.StartMS != 1000 || p.DurationMS != 4000 {
		t.Errorf("worker = %+v, want ok from its launch at 1000ms, 4000ms", p)
	}
	if p := phaseByName(t, phases, "readiness/api"); p.Status != upSpanFailed || p.StartMS != 0 || p.DurationMS != 7000 {
		t.Errorf("api = %+v, want failed, launched at 0, 7000ms", p)
	}
	n := 0
	for _, p := range phases {
		if p.Name == "readiness/worker" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("worker recorded %d times, want once", n)
	}
}

// A workload the gate saw but this run never launched (its build or launch
// failed before procs.start) is skipped, never credited as ready.
func TestUpReadyTimerUnlaunchedIsNotReady(t *testing.T) {
	clk := newFakeUpClock()
	tm := newUpTimings(clk.now)
	ctx := withUpTimings(context.Background(), tm)

	markUpLaunched(ctx, "api")
	readyCtx, gate := startUpSpan(ctx, "readiness")
	rt := newUpReadyTimer(readyCtx)
	clk.advance(time.Second)
	rt.observe([]hostReadyResult{
		{name: "api", state: portReadyOurs},
		{name: "worker", state: portReadyOurs},
	})
	rt.close(nil)
	gate.end(nil)

	_, phases := tm.finish()
	if p := phaseByName(t, phases, "readiness/api"); p.Status != upSpanOK {
		t.Errorf("api = %+v, want ok", p)
	}
	if p := phaseByName(t, phases, "readiness/worker"); p.Status != upSpanSkipped {
		t.Errorf("worker = %+v, want skipped: it was never launched", p)
	}
}

// Each run appends ONE line to .forge/logs/<env>/up-timings.jsonl, in the
// documented shape, and prints the table to stderr unless
// FORGE_UP_TIMINGS=0.
func TestFinishUpTimingsAppendsJSONLine(t *testing.T) {
	dir := t.TempDir()
	run := func(err error) string {
		clk := newFakeUpClock()
		tm := newUpTimings(clk.now)
		ctx := withUpTimings(context.Background(), tm)
		_, s := startUpSpan(ctx, "render")
		clk.advance(1200 * time.Millisecond)
		s.end(nil)
		var out bytes.Buffer
		finishUpTimings(tm, upTimingsRun{
			projectDir: dir, version: "v1.2.3", runID: newUpRunID(),
			opts: upOptions{env: "dev", targets: []string{"api"}, noBuild: true, renderOptions: []string{"region=secret-value"}},
		}, err, &out)
		// A second finish of the same run (the deferred one) writes nothing.
		finishUpTimings(tm, upTimingsRun{projectDir: dir, opts: upOptions{env: "dev"}}, nil, &out)
		return out.String()
	}
	table := run(nil)
	if !strings.Contains(table, "[up] timings — ok in 1.2s") || !strings.Contains(table, "render") {
		t.Errorf("table missing header or row:\n%s", table)
	}
	t.Setenv("FORGE_UP_TIMINGS", "0")
	if quiet := run(errors.New("deploy: boom\nmore")); quiet != "" {
		t.Errorf("FORGE_UP_TIMINGS=0 still printed:\n%s", quiet)
	}

	raw, err := os.ReadFile(filepath.Join(dir, ".forge", "logs", "dev", "up-timings.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("secret-value")) {
		t.Errorf("an option value leaked into the record:\n%s", raw)
	}
	var recs []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line is not JSON: %v: %s", err, sc.Text())
		}
		recs = append(recs, m)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 lines (one per run), got %d", len(recs))
	}
	first, second := recs[0], recs[1]
	for _, k := range []string{"schema", "run_id", "started_at", "env", "forge_version", "targets", "flags", "status", "total_ms", "phases"} {
		if _, ok := first[k]; !ok {
			t.Errorf("record lacks %q: %v", k, first)
		}
	}
	if first["schema"] != upTimingsSchema || first["status"] != "ok" || first["total_ms"] != float64(1200) {
		t.Errorf("first record = %v", first)
	}
	if second["status"] != "failed" || second["error"] != "deploy: boom" {
		t.Errorf("failed run recorded as %v / %v", second["status"], second["error"])
	}
	flags := first["flags"].(map[string]any)
	if flags["no_build"] != true || len(flags["options"].([]any)) != 1 || flags["options"].([]any)[0] != "region" {
		t.Errorf("flags = %v, want no_build and option NAMES only", flags)
	}
	ph := first["phases"].([]any)[0].(map[string]any)
	if ph["name"] != "render" || ph["start_ms"] != float64(0) || ph["duration_ms"] != float64(1200) || ph["status"] != "ok" {
		t.Errorf("phase = %v", ph)
	}
}

func TestUpTimingsPathRefusesNonFileEnv(t *testing.T) {
	for _, env := range []string{"", ".", "..", "a/b"} {
		if _, err := upTimingsPath(t.TempDir(), env); err == nil {
			t.Errorf("env %q accepted", env)
		}
	}
}

func TestFmtUpDuration(t *testing.T) {
	for ms, want := range map[int64]string{0: "0.0s", 1234: "1.2s", 59_949: "59.9s", 60_000: "1m00s", 83_400: "1m23s"} {
		if got := fmtUpDuration(ms); got != want {
			t.Errorf("fmtUpDuration(%d) = %q, want %q", ms, got, want)
		}
	}
}

// The table groups each phase's children under it even when a concurrent
// top-level phase started between them.
func TestUpPhasesTreeOrder(t *testing.T) {
	got := upPhasesTreeOrder([]upPhaseRecord{
		{Name: "build", StartMS: 0},
		{Name: "infra", StartMS: 0},
		{Name: "build/go", Parent: "build", StartMS: 10},
		{Name: "build/go/api", Parent: "build/go", StartMS: 10},
		{Name: "deploy", StartMS: 50},
		{Name: "host/jobs", Parent: "host", StartMS: 60},
	})
	var names []string
	for _, p := range got {
		names = append(names, p.Name)
	}
	want := "build build/go build/go/api infra deploy host/jobs"
	if strings.Join(names, " ") != want {
		t.Errorf("order = %v, want %s", names, want)
	}
	var out bytes.Buffer
	printUpTimings(&out, upTimingsRecord{Status: "ok", Phases: got}, "x")
	if !strings.Contains(out.String(), "\n      api ") || !strings.Contains(out.String(), "\n  host/jobs ") {
		t.Errorf("nesting/orphan labels wrong:\n%s", out.String())
	}
}
