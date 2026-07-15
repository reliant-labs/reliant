// Copyright (c) 2025 Reliant Labs
package netports

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// DefaultPollInterval is how often the watcher rescans the proc tables.
// Reading two small procfs files is effectively free.
const DefaultPollInterval = 3 * time.Second

// Watcher periodically scans for listening loopback/wildcard TCP ports and
// signals when the set changes, mirroring cgroupmem.Watcher so the daemon
// heartbeat loop can piggyback detected ports and emit immediately on change.
// Inert on hosts without /proc/net/tcp (macOS, non-Linux).
type Watcher struct {
	interval time.Duration
	exclude  map[int]bool

	mu     sync.Mutex
	latest []uint32
	valid  bool

	// changed receives one token when the port set changes, letting the
	// heartbeat loop emit immediately instead of waiting for the next tick.
	// Buffered(1) + non-blocking send: transitions coalesce, never block.
	changed chan struct{}
}

// NewWatcher creates a Watcher. excludePorts are the daemon's own listeners
// (RPC, preview forwarder) — never reported. interval <= 0 uses the default.
func NewWatcher(interval time.Duration, excludePorts ...int) *Watcher {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	exclude := make(map[int]bool, len(excludePorts))
	for _, p := range excludePorts {
		if p > 0 {
			exclude[p] = true
		}
	}
	return &Watcher{
		interval: interval,
		exclude:  exclude,
		changed:  make(chan struct{}, 1),
	}
}

// Latest returns the most recent port set. ok is false until the first
// successful scan — and forever on hosts without proc tables, so consumers
// naturally omit the field there.
func (w *Watcher) Latest() ([]uint32, bool) {
	if w == nil {
		return nil, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.latest, w.valid
}

// Changed returns a channel that receives a token whenever the detected port
// set changes. Nil-safe for consumers: a nil Watcher yields a nil channel,
// which simply never fires in a select.
func (w *Watcher) Changed() <-chan struct{} {
	if w == nil {
		return nil
	}
	return w.changed
}

// Run polls until ctx is done. It returns immediately when the proc tables
// are absent (macOS / non-Linux) — no goroutine churn, no errors.
func (w *Watcher) Run(ctx context.Context) {
	if w == nil {
		return
	}
	if _, ok := ListeningLoopbackPorts(nil); !ok {
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

func (w *Watcher) poll() {
	ports, ok := ListeningLoopbackPorts(w.exclude)
	if !ok {
		return
	}

	w.mu.Lock()
	// First scan only counts as a change when it found something — an empty
	// initial set is the steady state, not news.
	changed := (w.valid && !slices.Equal(w.latest, ports)) || (!w.valid && len(ports) > 0)
	w.latest = ports
	w.valid = true
	w.mu.Unlock()

	if changed {
		logging.Info("[netports] Detected listener set changed", "ports", ports)
		select {
		case w.changed <- struct{}{}:
		default:
		}
	}
}
