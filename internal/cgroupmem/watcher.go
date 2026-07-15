// Copyright (c) 2025 Reliant Labs
package cgroupmem

import (
	"context"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	// DefaultPollInterval is how often the watcher samples the cgroup files.
	// Reading two small kernfs files is effectively free.
	DefaultPollInterval = 5 * time.Second

	// PressureHighWatermark is the used/limit fraction at which memory
	// pressure is asserted (early warning before the OOM killer fires).
	PressureHighWatermark = 0.85

	// PressureClearWatermark is the used/limit fraction below which an
	// asserted pressure state clears. The 10-point gap (hysteresis) prevents
	// flapping when usage hovers around a single threshold.
	PressureClearWatermark = 0.75
)

// Sample is the watcher's latest memory observation, shaped for the
// daemon->gateway heartbeat (used_bytes, limit_bytes, pressure).
type Sample struct {
	UsedBytes  uint64
	LimitBytes uint64
	Pressure   bool
}

// Watcher polls cgroup memory usage and tracks a hysteresis-smoothed
// pressure bit. It is a no-op on hosts without cgroup v2 memory accounting.
type Watcher struct {
	reader   *Reader
	interval time.Duration

	mu     sync.Mutex
	latest Sample
	valid  bool

	// changed receives one token when the pressure bit flips, letting the
	// heartbeat loop emit immediately instead of waiting for the next tick.
	// Buffered(1) + non-blocking send: transitions coalesce, never block.
	changed chan struct{}
}

// NewWatcher creates a Watcher over the given reader, polling at
// DefaultPollInterval. interval <= 0 uses the default.
func NewWatcher(reader *Reader, interval time.Duration) *Watcher {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	return &Watcher{
		reader:   reader,
		interval: interval,
		changed:  make(chan struct{}, 1),
	}
}

// Latest returns the most recent sample. ok is false until the first
// successful poll — and forever on hosts without cgroup accounting, so
// consumers naturally omit memory fields there.
func (w *Watcher) Latest() (Sample, bool) {
	if w == nil {
		return Sample{}, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.latest, w.valid
}

// Changed returns a channel that receives a token whenever the pressure bit
// flips. Nil-safe for consumers: a nil Watcher yields a nil channel, which
// simply never fires in a select.
func (w *Watcher) Changed() <-chan struct{} {
	if w == nil {
		return nil
	}
	return w.changed
}

// Run polls until ctx is done. It returns immediately when the cgroup files
// are absent (macOS / uncontained daemons) — no goroutine churn, no errors.
func (w *Watcher) Run(ctx context.Context) {
	if w == nil || !w.reader.Available() {
		return
	}

	w.poll() // prime immediately so the first heartbeat already has data

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.poll()
		}
	}
}

// poll takes one sample and applies the hysteresis transition.
func (w *Watcher) poll() {
	stats, ok := w.reader.Stats()
	if !ok {
		return
	}

	w.mu.Lock()
	prev := w.latest.Pressure
	next := nextPressure(prev, stats.UsedBytes, stats.LimitBytes)
	w.latest = Sample{UsedBytes: stats.UsedBytes, LimitBytes: stats.LimitBytes, Pressure: next}
	w.valid = true
	w.mu.Unlock()

	if next != prev {
		if next {
			logging.Warn("[cgroupmem] Workspace memory pressure",
				"used", FormatBytes(stats.UsedBytes), "limit", FormatBytes(stats.LimitBytes))
		} else {
			logging.Info("[cgroupmem] Workspace memory pressure cleared",
				"used", FormatBytes(stats.UsedBytes), "limit", FormatBytes(stats.LimitBytes))
		}
		select {
		case w.changed <- struct{}{}:
		default:
		}
	}
}

// nextPressure is the pure hysteresis transition: assert at >= 85% of the
// limit, clear only below 75%. A cgroup without a limit is never under
// pressure.
func nextPressure(current bool, used, limit uint64) bool {
	if limit == 0 {
		return false
	}
	frac := float64(used) / float64(limit)
	if current {
		return frac >= PressureClearWatermark
	}
	return frac >= PressureHighWatermark
}
