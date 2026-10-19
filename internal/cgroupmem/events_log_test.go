// Copyright (c) 2025 Reliant Labs
package cgroupmem

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writeEvents(t *testing.T, dir string, high, max, oomKill int) {
	t.Helper()
	content := "low 0\nhigh " + itoa(high) + "\nmax " + itoa(max) + "\noom 0\noom_kill " + itoa(oomKill) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "memory.events"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestParseMemoryEvents(t *testing.T) {
	ev, ok := parseMemoryEvents([]byte(eventsFixture))
	if !ok || ev.High != 42 || ev.Max != 12 || ev.OOMKill != 3 {
		t.Fatalf("parseMemoryEvents = %+v ok=%v, want high=42 max=12 oom_kill=3", ev, ok)
	}
	if _, ok := parseMemoryEvents([]byte("junk\n")); ok {
		t.Error("a file with none of the counters must report not-ok")
	}
	if _, ok := parseMemoryEventsOOMKill([]byte("high 1\nmax 2\n")); ok {
		t.Error("oom_kill absent must stay not-ok for the existing OOM classifier")
	}
}

// The first reading is a baseline: counters that started before the daemon did
// are history, not news.
func TestWatcherEventsBaselineIsSilent(t *testing.T) {
	out := captureLogs(t)
	dir := writeCgroupFixture(t, "100\n", "1000\n", "", eventsFixture)
	w := NewWatcher(NewReader(dir), time.Hour)
	w.poll()
	if s := out.String(); strings.Contains(s, "OOM kill") || strings.Contains(s, "under memory pressure") {
		t.Fatalf("baseline reading logged: %s", s)
	}
}

func TestWatcherLogsOOMKillAsErrorWithMemoryFigures(t *testing.T) {
	out := captureLogs(t)
	dir := writeCgroupFixture(t, "900\n", "1000\n", "950\n", eventsFixture)
	w := NewWatcher(NewReader(dir), time.Hour)
	w.poll()

	writeEvents(t, dir, 42, 12, 4) // one more kill
	w.poll()

	var line string
	for _, l := range strings.Split(out.String(), "\n") {
		if strings.Contains(l, "cgroup OOM kill") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no OOM kill line:\n%s", out.String())
	}
	for _, want := range []string{"level=ERROR", "oom_kills=1", "oom_kills_total=4", "memory_limit_bytes=1000", "memory_peak_bytes=950"} {
		if !strings.Contains(line, want) {
			t.Errorf("line missing %q: %s", want, line)
		}
	}
}

func TestWatcherLogsMemoryEventGrowthAtWarnAndThrottles(t *testing.T) {
	out := captureLogs(t)
	// 80% unreclaimable (no memory.stat, no PSI): a squeeze that matters.
	dir := writeCgroupFixture(t, "800\n", "1000\n", "", eventsFixture)
	w := NewWatcher(NewReader(dir), time.Hour)
	w.poll() // baseline

	writeEvents(t, dir, 50, 12, 3) // memory.high crossed 8 more times
	w.poll()
	writeEvents(t, dir, 60, 15, 3) // still climbing, inside the minute
	w.poll()

	got := strings.Count(out.String(), "under memory pressure")
	if got != 1 {
		t.Fatalf("want 1 throttled WARN, got %d:\n%s", got, out.String())
	}
	if !strings.Contains(out.String(), "level=WARN") || !strings.Contains(out.String(), "high_events=8") {
		t.Fatalf("WARN missing level or count:\n%s", out.String())
	}

	// A static counter is silent, and so is a new window with no growth.
	w.lastSqueeze = time.Now().Add(-2 * squeezeLogEvery)
	w.poll()
	if strings.Count(out.String(), "under memory pressure") != 1 {
		t.Fatalf("no growth must log nothing:\n%s", out.String())
	}
}

// TestWatcherSqueezeOnCacheIsNotAWarning is the regression test for the 1426
// "under memory pressure" WARNs: the high/max counters climb whenever the
// kernel drops cache at a limit, which on a machine with 10% of its memory
// unreclaimable and nothing stalling is routine, not a warning.
func TestWatcherSqueezeOnCacheIsNotAWarning(t *testing.T) {
	out := captureLogs(t)
	dir := writeCgroupFixture(t, "100\n", "1000\n", "", eventsFixture)
	writePSI(t, dir, psiFixture("0.40", "0.00"))
	w := NewWatcher(NewReader(dir), time.Hour)
	w.poll() // baseline

	writeEvents(t, dir, 50, 20, 3)
	w.poll()

	if strings.Contains(out.String(), "level=WARN") || strings.Contains(out.String(), "under memory pressure") {
		t.Fatalf("cache reclaim at a limit logged as pressure:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "without stalling") || !strings.Contains(out.String(), "max_events=8") {
		t.Fatalf("want the DEBUG line with its counts:\n%s", out.String())
	}

	// Tasks stalling: the same growth is a WARN.
	writePSI(t, dir, psiFixture("25.00", "1.00"))
	w.lastSqueeze = time.Time{}
	writeEvents(t, dir, 60, 30, 3)
	w.poll()
	if !strings.Contains(out.String(), "level=WARN") || !strings.Contains(out.String(), "under memory pressure") {
		t.Fatalf("stalling squeeze must WARN:\n%s", out.String())
	}
}

// TestWatcherAttributesSqueezeToChildCgroup: memory.events is hierarchical,
// so dockerd hitting its own container cap shows up in the workspace's
// counters. The line must say where it came from.
func TestWatcherAttributesSqueezeToChildCgroup(t *testing.T) {
	out := captureLogs(t)
	dir := writeCgroupFixture(t, "900\n", "1000\n", "", eventsFixture)
	for _, child := range []string{"dockerd", "daemon"} {
		if err := os.Mkdir(filepath.Join(dir, child), 0o755); err != nil {
			t.Fatal(err)
		}
		writeEvents(t, filepath.Join(dir, child), 0, 0, 0)
	}
	w := NewWatcher(NewReader(dir), time.Hour)
	w.poll() // baseline, children included

	writeEvents(t, filepath.Join(dir, "dockerd"), 0, 7, 0)
	writeEvents(t, dir, 42, 19, 3)
	w.poll()

	if !strings.Contains(out.String(), `by_child="dockerd: max +7"`) {
		t.Fatalf("squeeze not attributed to dockerd:\n%s", out.String())
	}
}
