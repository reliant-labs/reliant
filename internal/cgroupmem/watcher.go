// Copyright (c) 2025 Reliant Labs
package cgroupmem

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	// DefaultPollInterval is how often the watcher samples the cgroup files.
	// Reading two small kernfs files is effectively free.
	DefaultPollInterval = 5 * time.Second

	// PressureHighWatermark is the unreclaimable/limit fraction at which
	// memory pressure is asserted (early warning before the OOM killer fires).
	PressureHighWatermark = 0.85

	// PressureClearWatermark is the unreclaimable/limit fraction below which
	// an asserted pressure state clears. The 10-point gap (hysteresis) prevents
	// flapping when usage hovers around a single threshold.
	PressureClearWatermark = 0.75

	// PSIFullHighPercent asserts pressure on stalls alone: every non-idle task
	// in the cgroup waited on memory for at least this share of the last 10s.
	// That is thrashing — the case an occupancy figure that leaves out the
	// file cache cannot see — and it is never caused by idle cache.
	PSIFullHighPercent = 10.0
	// PSIFullClearPercent is the stall share below which a PSI-asserted
	// pressure state may clear (with the byte figure below its own clear mark).
	PSIFullClearPercent = 2.0
	// PSISomeWarnPercent is the "some" stall share at which growth in the
	// high/max counters is worth a WARN. Below it the kernel reclaimed cache at
	// a limit without anyone waiting long — routine on a busy machine.
	PSISomeWarnPercent = 10.0
)

// Sample is the watcher's latest memory observation, shaped for the
// daemon->gateway heartbeat (used_bytes, limit_bytes, pressure).
type Sample struct {
	// UnreclaimableBytes is Stats.UnreclaimableBytes: what pressure is judged
	// on and what the heartbeat reports as memory used. Compare it only to
	// LimitBytes, never to the cache-inclusive CurrentBytes.
	UnreclaimableBytes uint64
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
	// children is the previous ChildEvents reading, for attributing growth.
	children map[string]Events

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

	w.mu.Lock()
	prev := w.latest.Pressure
	w.mu.Unlock()
	next := nextPressure(prev, stats)
	newKill := w.logEvents(stats, next)

	w.mu.Lock()
	w.latest = Sample{
		UnreclaimableBytes: stats.UnreclaimableBytes, CurrentBytes: stats.CurrentBytes,
		LimitBytes: stats.LimitBytes, Pressure: next,
		OOMKills: w.oomKills, LastOOMKillAt: w.lastOOMKillAt,
	}
	w.valid = true
	w.mu.Unlock()

	if next != prev {
		if next {
			logging.Warn("[cgroupmem] Workspace memory pressure",
				append([]any{"unreclaimable", FormatBytes(stats.UnreclaimableBytes), "limit", FormatBytes(stats.LimitBytes)},
					memoryAttrs(stats)...)...)
		} else {
			logging.Info("[cgroupmem] Workspace memory pressure cleared",
				"unreclaimable", FormatBytes(stats.UnreclaimableBytes), "limit", FormatBytes(stats.LimitBytes))
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

// memoryAttrs are the structured fields every memory log line carries: the
// figure pressure is judged on, what it was derived from, and the stall share.
func memoryAttrs(stats Stats) []any {
	attrs := []any{
		"memory_unreclaimable_bytes", stats.UnreclaimableBytes,
		"memory_current_bytes", stats.CurrentBytes,
		"memory_active_file_bytes", stats.ActiveFileBytes,
		"memory_inactive_file_bytes", stats.InactiveFileBytes,
		"memory_slab_reclaimable_bytes", stats.SlabReclaimableBytes,
		"memory_limit_bytes", stats.LimitBytes,
	}
	if stats.PSI.Valid {
		attrs = append(attrs, "memory_psi_some_avg10", stats.PSI.SomeAvg10, "memory_psi_full_avg10", stats.PSI.FullAvg10)
	}
	return attrs
}

// squeezeAlarming reports whether growth in the high/max counters means the
// workspace is short of memory, rather than that the kernel dropped cache at a
// limit — which on a machine that reads a lot of files happens continuously
// and costs nothing. It is alarming when the pressure bit is set, when PSI
// shows tasks stalling, or (no PSI to ask) when the unreclaimable share is
// already past the clear watermark.
func squeezeAlarming(stats Stats, pressure bool) bool {
	if pressure {
		return true
	}
	if stats.PSI.Valid {
		return stats.PSI.SomeAvg10 >= PSISomeWarnPercent
	}
	return stats.LimitBytes > 0 && float64(stats.UnreclaimableBytes)/float64(stats.LimitBytes) >= PressureClearWatermark
}

// childGrowth names the child cgroups whose high/max counters grew since the
// previous reading, e.g. "dockerd: max +12". It updates the stored reading.
func (w *Watcher) childGrowth() string {
	cur := w.reader.ChildEvents()
	w.mu.Lock()
	prev := w.children
	w.children = cur
	w.mu.Unlock()
	if prev == nil {
		return ""
	}
	names := make([]string, 0, len(cur))
	for name := range cur {
		names = append(names, name)
	}
	sort.Strings(names)
	var parts []string
	for _, name := range names {
		c, p := cur[name], prev[name]
		var grew []string
		if c.Max > p.Max {
			grew = append(grew, fmt.Sprintf("max +%d", c.Max-p.Max))
		}
		if c.High > p.High {
			grew = append(grew, fmt.Sprintf("high +%d", c.High-p.High))
		}
		if len(grew) > 0 {
			parts = append(parts, name+": "+strings.Join(grew, " "))
		}
	}
	return strings.Join(parts, "; ")
}

// logEvents reports growth in memory.events since the previous poll: an OOM
// kill is an ERROR the moment it is seen (the exec path only names kills it
// can attribute to one of its own commands), and growth in high/max is logged
// at most once a minute, carrying the counts since the last line and the child
// cgroups they came from. That line is a WARN only when squeezeAlarming says
// the squeeze cost something; reclaiming cache at a limit is DEBUG. It reports
// whether it saw a new OOM kill.
func (w *Watcher) logEvents(stats Stats, pressure bool) (newKill bool) {
	ev, ok := w.reader.Events()
	if !ok {
		return false
	}
	w.mu.Lock()
	prev, seen := w.events, w.eventsSeen
	w.events, w.eventsSeen = ev, true
	logSqueeze := false
	grew := seen && (ev.High > prev.High || ev.Max > prev.Max)
	if grew && time.Since(w.lastSqueeze) >= squeezeLogEvery {
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
		_ = w.childGrowth() // the children's baseline
		return false
	}

	if newKill {
		logging.Error("[cgroupmem] Workspace cgroup OOM kill",
			append([]any{"oom_kills", ev.OOMKill - prev.OOMKill, "oom_kills_total", ev.OOMKill,
				"memory_peak_bytes", stats.PeakBytes}, memoryAttrs(stats)...)...)
	}
	if logSqueeze {
		attrs := []any{"high_events", ev.High - prev.High, "max_events", ev.Max - prev.Max}
		if by := w.childGrowth(); by != "" {
			attrs = append(attrs, "by_child", by)
		}
		attrs = append(attrs, memoryAttrs(stats)...)
		if squeezeAlarming(stats, pressure) {
			logging.Warn("[cgroupmem] Workspace cgroup under memory pressure", attrs...)
		} else {
			logging.Debug("[cgroupmem] Cgroup reclaimed cache at a memory limit without stalling", attrs...)
		}
	} else if w.childrenUnread() {
		// The first reading is the baseline: the next logged line attributes
		// what grew since then, and each later line what grew since the last.
		_ = w.childGrowth()
	}
	return newKill
}

// childrenUnread reports whether no child baseline has been taken yet.
func (w *Watcher) childrenUnread() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.children == nil
}

// nextPressure is the pure hysteresis transition. Pressure asserts when the
// unreclaimable memory reaches 85% of the limit OR every task has been
// stalled on memory for 10% of the last 10s (PSI full); it clears only when
// the unreclaimable share is below 75% AND the stall share below 2%. Page
// cache moves neither signal. Without a limit only PSI can assert.
func nextPressure(current bool, stats Stats) bool {
	frac := 0.0
	if stats.LimitBytes > 0 {
		frac = float64(stats.UnreclaimableBytes) / float64(stats.LimitBytes)
	}
	stall := 0.0
	if stats.PSI.Valid {
		stall = stats.PSI.FullAvg10
	}
	if current {
		return (stats.LimitBytes > 0 && frac >= PressureClearWatermark) || stall >= PSIFullClearPercent
	}
	return (stats.LimitBytes > 0 && frac >= PressureHighWatermark) || stall >= PSIFullHighPercent
}
