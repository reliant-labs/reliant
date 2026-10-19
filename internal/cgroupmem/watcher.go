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

	// PressureHighWatermark is the working-set/limit fraction at which memory
	// pressure is asserted (early warning before the OOM killer fires).
	PressureHighWatermark = 0.85

	// PressureClearWatermark is the working-set/limit fraction below which an
	// asserted pressure state clears. The 10-point gap (hysteresis) prevents
	// flapping when usage hovers around a single threshold.
	PressureClearWatermark = 0.75
)

// Sample is the watcher's latest memory observation, shaped for the
// daemon->gateway heartbeat (used_bytes, limit_bytes, pressure).
type Sample struct {
	// WorkingSetBytes is Stats.WorkingSetBytes: what pressure is judged on and
	// what the heartbeat reports as memory used. Compare it only to
	// LimitBytes, never to the cache-inclusive CurrentBytes.
	WorkingSetBytes uint64
	// CurrentBytes is memory.current (reclaimable cache included), for
	// diagnostics only.
	CurrentBytes uint64
	LimitBytes   uint64
	Pressure     bool
	// OOMKills counts the kernel OOM kills in the workspace cgroup since the
	// watcher started (memory.events oom_kill growth; the counter's value at
	// start is the baseline). It covers every process in the workspace, the
	// docker subtree included, not only the commands a tool call ran.
	OOMKills uint64
	// LastOOMKillAt is when the watcher first saw the newest of those kills,
	// so within one poll interval of it; zero when there has been none.
	LastOOMKillAt time.Time
}

// Watcher polls cgroup memory usage and tracks a hysteresis-smoothed
// pressure bit. It is a no-op on hosts without cgroup v2 memory accounting.
type Watcher struct {
	reader   *Reader
	interval time.Duration

	mu     sync.Mutex
	latest Sample
	valid  bool

	// events is the previous memory.events reading, so a poll can report what
	// grew. eventsSeen is false until the first reading, which is the baseline
	// and never reported: counters started before the daemon did.
	events      Events
	eventsSeen  bool
	lastSqueeze time.Time

	// oomKills / lastOOMKillAt accumulate OOM kills seen since the baseline,
	// carried into every Sample.
	oomKills      uint64
	lastOOMKillAt time.Time

	// now is time.Now; tests pin it.
	now func() time.Time

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
		now:      time.Now,
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

	newKill := w.logEvents(stats)

	w.mu.Lock()
	prev := w.latest.Pressure
	next := nextPressure(prev, stats.WorkingSetBytes, stats.LimitBytes)
	w.latest = Sample{
		WorkingSetBytes: stats.WorkingSetBytes, CurrentBytes: stats.CurrentBytes,
		LimitBytes: stats.LimitBytes, Pressure: next,
		OOMKills: w.oomKills, LastOOMKillAt: w.lastOOMKillAt,
	}
	w.valid = true
	w.mu.Unlock()

	if next != prev {
		if next {
			logging.Warn("[cgroupmem] Workspace memory pressure",
				"working_set", FormatBytes(stats.WorkingSetBytes), "limit", FormatBytes(stats.LimitBytes),
				"memory_working_set_bytes", stats.WorkingSetBytes, "memory_current_bytes", stats.CurrentBytes,
				"memory_inactive_file_bytes", stats.InactiveFileBytes,
				"memory_slab_reclaimable_bytes", stats.SlabReclaimableBytes,
				"memory_limit_bytes", stats.LimitBytes)
		} else {
			logging.Info("[cgroupmem] Workspace memory pressure cleared",
				"working_set", FormatBytes(stats.WorkingSetBytes), "limit", FormatBytes(stats.LimitBytes))
		}
	}
	// A new OOM kill is news as much as a pressure flip: the user should hear
	// about it on the next heartbeat, not a tick later.
	if next != prev || newKill {
		select {
		case w.changed <- struct{}{}:
		default:
		}
	}
}

// squeezeLogEvery bounds how often growth in the high/max counters is logged.
// Under sustained pressure they climb on every poll, and the pressure
// transition line already says it started.
const squeezeLogEvery = time.Minute

// logEvents reports growth in memory.events since the previous poll: an OOM
// kill is an ERROR the moment it is seen (the exec path only names kills it
// can attribute to one of its own commands), and growth in high/max is a WARN,
// at most once a minute, carrying the counts since the last line. It reports
// whether it saw a new OOM kill.
func (w *Watcher) logEvents(stats Stats) (newKill bool) {
	ev, ok := w.reader.Events()
	if !ok {
		return false
	}
	w.mu.Lock()
	prev, seen := w.events, w.eventsSeen
	w.events, w.eventsSeen = ev, true
	logSqueeze := false
	if seen && (ev.High > prev.High || ev.Max > prev.Max) && time.Since(w.lastSqueeze) >= squeezeLogEvery {
		logSqueeze = true
		w.lastSqueeze = time.Now()
	}
	newKill = seen && ev.OOMKill > prev.OOMKill
	if newKill {
		w.oomKills += ev.OOMKill - prev.OOMKill
		w.lastOOMKillAt = w.now()
	}
	w.mu.Unlock()
	if !seen {
		return false
	}

	if newKill {
		logging.Error("[cgroupmem] Workspace cgroup OOM kill",
			"oom_kills", ev.OOMKill-prev.OOMKill,
			"oom_kills_total", ev.OOMKill,
			"memory_limit_bytes", stats.LimitBytes,
			"memory_working_set_bytes", stats.WorkingSetBytes,
			"memory_current_bytes", stats.CurrentBytes,
			"memory_peak_bytes", stats.PeakBytes)
	}
	if logSqueeze {
		logging.Warn("[cgroupmem] Workspace cgroup under memory pressure",
			"high_events", ev.High-prev.High,
			"max_events", ev.Max-prev.Max,
			"memory_limit_bytes", stats.LimitBytes,
			"memory_working_set_bytes", stats.WorkingSetBytes,
			"memory_current_bytes", stats.CurrentBytes)
	}
	return newKill
}

// nextPressure is the pure hysteresis transition on the working set: assert
// at >= 85% of the limit, clear only below 75%. A cgroup without a limit is never under
// pressure.
func nextPressure(current bool, workingSet, limit uint64) bool {
	if limit == 0 {
		return false
	}
	frac := float64(workingSet) / float64(limit)
	if current {
		return frac >= PressureClearWatermark
	}
	return frac >= PressureHighWatermark
}
