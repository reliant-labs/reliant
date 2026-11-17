package middleware

import "container/list"

// lruCache is a small, non-thread-safe LRU map used by the rate-limit
// and RPC-idempotency interceptors to bound memory under key-cardinality
// attacks (e.g. spoofed IPs, random idempotency keys). Callers guard it
// with their own mutex — both interceptors already serialize cache
// access, so embedding locking here would just double the cost.
//
// Hand-rolled (container/list + map) instead of importing an LRU
// dependency: forge/pkg keeps its dependency surface minimal because
// every downstream project inherits it.
type lruCache[V any] struct {
	max   int
	ll    *list.List // front = most recently used
	items map[string]*list.Element
}

type lruEntry[V any] struct {
	key   string
	value V
}

// newLRUCache returns an LRU holding at most max entries. max must be
// positive; callers validate (both interceptors disable themselves on
// non-positive sizes before constructing the cache).
func newLRUCache[V any](max int) *lruCache[V] {
	return &lruCache[V]{
		max:   max,
		ll:    list.New(),
		items: make(map[string]*list.Element, max),
	}
}

// get returns the value for key and marks it most-recently-used.
func (c *lruCache[V]) get(key string) (V, bool) {
	if el, ok := c.items[key]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*lruEntry[V]).value, true
	}
	var zero V
	return zero, false
}

// add inserts (or updates) key, evicting the least-recently-used entry
// when the cache is full.
func (c *lruCache[V]) add(key string, value V) {
	if el, ok := c.items[key]; ok {
		c.ll.MoveToFront(el)
		el.Value.(*lruEntry[V]).value = value
		return
	}
	el := c.ll.PushFront(&lruEntry[V]{key: key, value: value})
	c.items[key] = el
	if c.ll.Len() > c.max {
		oldest := c.ll.Back()
		if oldest != nil {
			c.ll.Remove(oldest)
			delete(c.items, oldest.Value.(*lruEntry[V]).key)
		}
	}
}

// len returns the number of cached entries.
func (c *lruCache[V]) len() int { return c.ll.Len() }
