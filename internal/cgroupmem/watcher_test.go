// Copyright (c) 2025 Reliant Labs
package cgroupmem

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestNextPressureHysteresis(t *testing.T) {
	tests := []struct {
		name    string
		current bool
		used    uint64
		limit   uint64
		want    bool
	}{
		{"idle stays clear", false, 100, 1000, false},
		{"just below high watermark stays clear", false, 849, 1000, false},
		{"at high watermark asserts", false, 850, 1000, true},
		{"above high watermark asserts", false, 990, 1000, true},
		{"asserted stays above clear watermark", true, 800, 1000, true},
		{"asserted stays at clear watermark", true, 750, 1000, true},
		{"asserted clears below clear watermark", true, 749, 1000, false},
		{"asserted stays at high usage", true, 990, 1000, true},
		{"no limit never asserts", false, 1 << 40, 0, false},
		{"no limit clears", true, 1 << 40, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nextPressure(tt.current, tt.used, tt.limit); got != tt.want {
				t.Errorf("nextPressure(%v, %d, %d) = %v, want %v", tt.current, tt.used, tt.limit, got, tt.want)
			}
		})
	}
}

// TestNextPressureNoFlapping walks a usage curve hovering between the two
// watermarks and asserts the state changes exactly twice (assert once, clear
// once) instead of flapping on every sample.
func TestNextPressureNoFlapping(t *testing.T) {
	const limit = uint64(100)
	curve := []uint64{70, 80, 86, 84, 80, 78, 76, 80, 84, 74, 70, 60}
	state := false
	transitions := 0
	for _, used := range curve {
		next := nextPressure(state, used, limit)
		if next != state {
			transitions++
		}
		state = next
	}
	if transitions != 2 {
		t.Errorf("expected exactly 2 transitions (assert at 86, clear at 74), got %d", transitions)
	}
	if state {
		t.Error("expected final state to be clear")
	}
}

func setUsage(t *testing.T, dir string, used uint64) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "memory.current"), []byte(strconv.FormatUint(used, 10)+"\n"), 0o644); err != nil {
		t.Fatalf("writing memory.current: %v", err)
	}
}

func TestWatcherPollAndChanged(t *testing.T) {
	dir := writeCgroupFixture(t, "100\n", "1000\n", "", eventsFixture)
	w := NewWatcher(NewReader(dir), time.Hour) // manual polls only

	w.poll()
	sample, ok := w.Latest()
	if !ok {
		t.Fatal("expected a valid sample after poll")
	}
	if sample.Pressure {
		t.Error("10% usage must not be pressure")
	}
	if sample.UsedBytes != 100 || sample.LimitBytes != 1000 {
		t.Errorf("sample = %+v, want used=100 limit=1000", sample)
	}

	// Cross the high watermark: pressure asserts and Changed fires once.
	setUsage(t, dir, 900)
	w.poll()
	if sample, _ := w.Latest(); !sample.Pressure {
		t.Fatal("90% usage must assert pressure")
	}
	select {
	case <-w.Changed():
	default:
		t.Fatal("expected Changed signal on pressure assert")
	}

	// Hovering above the clear watermark: no change, no signal.
	setUsage(t, dir, 800)
	w.poll()
	if sample, _ := w.Latest(); !sample.Pressure {
		t.Fatal("80% while asserted must stay asserted (hysteresis)")
	}
	select {
	case <-w.Changed():
		t.Fatal("no transition — Changed must not fire")
	default:
	}

	// Dropping below the clear watermark: clears and signals.
	setUsage(t, dir, 700)
	w.poll()
	if sample, _ := w.Latest(); sample.Pressure {
		t.Fatal("70% must clear pressure")
	}
	select {
	case <-w.Changed():
	default:
		t.Fatal("expected Changed signal on pressure clear")
	}
}

func TestWatcherRunNoCgroup(t *testing.T) {
	w := NewWatcher(NewReader(filepath.Join(t.TempDir(), "missing")), time.Millisecond)

	done := make(chan struct{})
	go func() {
		w.Run(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run must return immediately when the cgroup is absent")
	}
	if _, ok := w.Latest(); ok {
		t.Error("Latest must stay invalid without cgroup files")
	}
}

func TestWatcherNilSafety(t *testing.T) {
	var w *Watcher
	if _, ok := w.Latest(); ok {
		t.Error("nil watcher Latest must be invalid")
	}
	if w.Changed() != nil {
		t.Error("nil watcher Changed must be nil")
	}
	w.Run(context.Background()) // must not panic
}
