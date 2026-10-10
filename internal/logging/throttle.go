// Copyright (c) 2025 Reliant Labs
package logging

import (
	"sync"
	"time"
)

// throttleMaxKeys bounds the keys a Throttle remembers. Past it, keys whose
// window has closed are forgotten (they would log again anyway).
const throttleMaxKeys = 4096

// Throttle decides when an expected, repeating condition is worth a log line:
// at most once per window per key, reporting how many were suppressed since
// the last one. For states a client retries on a timer — a machine that is
// not up yet — where every retry is the same news.
//
// It is shared by every layer that logs such a state (the terminal socket,
// the daemon router), so each says it the same way: one line per window, with
// the count it stands for.
type Throttle struct {
	mu     sync.Mutex
	window time.Duration
	now    func() time.Time
	keys   map[string]throttleKey
}

type throttleKey struct {
	loggedAt   time.Time
	suppressed int
}

// NewThrottle returns a Throttle admitting one line per window per key. now
// is the clock (time.Now outside tests).
func NewThrottle(window time.Duration, now func() time.Time) *Throttle {
	return &Throttle{window: window, now: now, keys: map[string]throttleKey{}}
}

// Allow reports whether key may log now and, when it may, how many times it
// was suppressed since it last did.
func (t *Throttle) Allow(key string) (suppressed int, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	prev, seen := t.keys[key]
	if seen && now.Sub(prev.loggedAt) < t.window {
		prev.suppressed++
		t.keys[key] = prev
		return 0, false
	}
	if !seen && len(t.keys) >= throttleMaxKeys {
		for k, v := range t.keys {
			if now.Sub(v.loggedAt) >= t.window {
				delete(t.keys, k)
			}
		}
	}
	t.keys[key] = throttleKey{loggedAt: now}
	return prev.suppressed, true
}
