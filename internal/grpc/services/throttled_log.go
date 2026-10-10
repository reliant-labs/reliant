// Copyright (c) 2025 Reliant Labs
package services

import (
	"sync"
	"time"
)

// throttledLogMaxKeys bounds the keys a throttledLog remembers. Past it, keys
// whose window has closed are forgotten (they would log again anyway).
const throttledLogMaxKeys = 4096

// throttledLog decides when an expected, repeating condition is worth a log
// line: at most once per window per key, reporting how many were suppressed
// since the last one. For states a client retries on a timer — a machine that
// is not up yet — where every retry is the same news.
type throttledLog struct {
	mu     sync.Mutex
	window time.Duration
	now    func() time.Time
	keys   map[string]throttledLogKey
}

type throttledLogKey struct {
	loggedAt   time.Time
	suppressed int
}

func newThrottledLog(window time.Duration, now func() time.Time) *throttledLog {
	return &throttledLog{window: window, now: now, keys: map[string]throttledLogKey{}}
}

// allow reports whether key may log now and, when it may, how many times it
// was suppressed since it last did.
func (t *throttledLog) allow(key string) (suppressed int, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	prev, seen := t.keys[key]
	if seen && now.Sub(prev.loggedAt) < t.window {
		prev.suppressed++
		t.keys[key] = prev
		return 0, false
	}
	if !seen && len(t.keys) >= throttledLogMaxKeys {
		for k, v := range t.keys {
			if now.Sub(v.loggedAt) >= t.window {
				delete(t.keys, k)
			}
		}
	}
	t.keys[key] = throttledLogKey{loggedAt: now}
	return prev.suppressed, true
}
