package inputoptions

import (
	"sync"
	"time"
)

// maxCacheEntries bounds the memory a burst of distinct listings can hold. A
// listing is whatever the action's output.select kept, which for the shipped
// pickers is a few hundred bytes per item.
const maxCacheEntries = 128

// listing is one action call's output, before any projection: the acting
// identity, the page and the search all project from the same cached call.
type listing struct {
	data      map[string]any
	truncated bool
	expires   time.Time
	stored    time.Time
}

// cache is a small TTL cache of listings, keyed per user, connection, type,
// scope, query and upstream page. Only successful calls are stored: a
// failure, or a user with no credential, is asked again.
type cache struct {
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	entries map[string]*listing
}

func newCache(ttl time.Duration, now func() time.Time) *cache {
	return &cache{ttl: ttl, now: now, entries: map[string]*listing{}}
}

func (c *cache) enabled() bool { return c.ttl > 0 }

func (c *cache) get(key string) (*listing, bool) {
	if !c.enabled() {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	l, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if !c.now().Before(l.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return l, true
}

func (c *cache) put(key string, data map[string]any, truncated bool) *listing {
	now := c.now()
	l := &listing{data: data, truncated: truncated, stored: now, expires: now.Add(c.ttl)}
	if !c.enabled() {
		return l
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= maxCacheEntries {
		c.evictLocked(now)
	}
	c.entries[key] = l
	return l
}

// evictLocked drops every expired entry, and the oldest one if that freed
// nothing.
func (c *cache) evictLocked(now time.Time) {
	var oldestKey string
	var oldest time.Time
	freed := false
	for k, l := range c.entries {
		if !now.Before(l.expires) {
			delete(c.entries, k)
			freed = true
			continue
		}
		if oldestKey == "" || l.stored.Before(oldest) {
			oldestKey, oldest = k, l.stored
		}
	}
	if !freed && oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}
