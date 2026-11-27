package middleware

import (
	"sync"
	"time"
)

// DedupeConfig configures a DedupeStore. The zero value is a working
// default: 1000 keys, 1h TTL.
type DedupeConfig struct {
	// MaxKeys bounds how many keys are remembered at once. The oldest
	// (least recently touched) key is dropped when the bound is reached,
	// so a flood of unique keys can never grow the process's memory.
	// Default: 1000.
	MaxKeys int

	// TTL is how long a marked key stays marked. Size it to the sender's
	// retry window: a provider that retries a failed delivery for 24h
	// needs a 24h TTL to still recognise the redelivery as a duplicate.
	// Default: 1 hour.
	TTL time.Duration
}

// DedupeStore answers "have I already handled this?" for a stream of
// opaque keys. It is the boolean-shaped counterpart to
// [IdempotencyMiddleware]: the middleware replays a cached RESPONSE from
// in front of a handler, a DedupeStore lets the handler itself decide,
// which is what you want when the check has to happen after some other
// gate (verifying a webhook signature) or when the answer must be marked
// only once processing actually succeeded.
//
//	if store.Seen(eventID) { return }   // already handled — ack and stop
//	if err := process(evt); err != nil { return err }
//	store.Mark(eventID)                 // only after success, so a retry
//	                                    // of a FAILED delivery re-runs
//
// The store is bounded and PER-PROCESS. Two replicas each keep their own,
// so a duplicate delivery routed to the other replica is processed again.
// When at-most-once has to hold across replicas, keep the same call shape
// and back it with shared storage — a table with a unique constraint on
// the key, where Seen is a SELECT and Mark an INSERT.
//
// Safe for concurrent use. Seen and Mark are separate operations, so two
// deliveries of the same key arriving at once can both observe "not seen"
// and both process; a unique constraint in shared storage is what closes
// that window for good.
type DedupeStore struct {
	mu    sync.Mutex
	cache *lruCache[time.Time] // value = expiry
	ttl   time.Duration
}

// NewDedupeStore returns a bounded, TTL'd key set. See DedupeConfig for
// the defaults applied to zero fields.
func NewDedupeStore(cfg DedupeConfig) *DedupeStore {
	maxKeys := cfg.MaxKeys
	if maxKeys <= 0 {
		maxKeys = 1000
	}
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &DedupeStore{cache: newLRUCache[time.Time](maxKeys), ttl: ttl}
}

// Seen reports whether key was marked within the TTL window. An empty key
// is never seen — callers that cannot derive a key must fall through to
// processing rather than silently dropping the work.
func (s *DedupeStore) Seen(key string) bool {
	if key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expiresAt, ok := s.cache.get(key)
	return ok && time.Now().Before(expiresAt)
}

// Mark records key as handled for the configured TTL. Marking a key that
// is already present refreshes its expiry. An empty key is ignored.
func (s *DedupeStore) Mark(key string) {
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache.add(key, time.Now().Add(s.ttl))
}

// Len returns how many keys are currently tracked, including any whose
// TTL has lapsed but that eviction has not yet reclaimed. Exposed for
// tests and diagnostics that assert the bound holds.
func (s *DedupeStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cache.len()
}
