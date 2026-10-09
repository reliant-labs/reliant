// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// machineStateLogWindow is how often the router writes one user's
// machine-state line for the same state.
const machineStateLogWindow = time.Minute

// machineStateLogMaxKeys bounds the throttle's memory independently of entry
// expiry. The oldest entry is evicted whenever an unseen key arrives at the
// cap, so fresh one-off users cannot grow the map without limit.
const machineStateLogMaxKeys = 4096

// machineStateLog throttles the router's per-request machine-state lines
// ("still starting", "suspended", "no daemon") per user and state.
type machineStateLog struct {
	mu      sync.Mutex
	window  time.Duration
	now     func() time.Time
	entries map[string]*machineStateLogEntry
}

type machineStateLogEntry struct {
	written    time.Time
	suppressed int
}

func newMachineStateLog(window time.Duration) *machineStateLog {
	return &machineStateLog{
		window:  window,
		now:     time.Now,
		entries: make(map[string]*machineStateLogEntry),
	}
}

// routerStateLog is the router's one instance. routableDaemonID is a free
// function called from both resolution and the wake step, so the throttle is
// package-scoped rather than per router.
var routerStateLog = newMachineStateLog(machineStateLogWindow)

// admit reports whether the line for key should be written now and, when it
// should, how many lines for key were dropped since the last one written.
func (l *machineStateLog) admit(key string) (ok bool, suppressed int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if entry, seen := l.entries[key]; seen {
		if now.Sub(entry.written) < l.window {
			entry.suppressed++
			return false, 0
		}
		suppressed = entry.suppressed
		entry.written = now
		entry.suppressed = 0
		return true, suppressed
	}

	if len(l.entries) >= machineStateLogMaxKeys {
		l.evictOldest()
	}
	l.entries[key] = &machineStateLogEntry{written: now}
	return true, 0
}

func (l *machineStateLog) evictOldest() {
	var oldestKey string
	var oldest time.Time
	for key, entry := range l.entries {
		if oldestKey == "" || entry.written.Before(oldest) {
			oldestKey, oldest = key, entry.written
		}
	}
	delete(l.entries, oldestKey)
}

// info writes msg at INFO when the (user, state) key admits it, carrying the
// count of lines it stands for. INFO, not WARN: a machine that is starting,
// asleep or absent is a state the product handles, and the request's own
// outcome is already on the RPC's "rpc failed" line.
func (l *machineStateLog) info(state, userID, msg string, args ...any) {
	ok, suppressed := l.admit(state + "|" + userID)
	if !ok {
		return
	}
	if suppressed > 0 {
		args = append(args, "suppressed", suppressed)
	}
	logging.Info(msg, args...)
}
