package coverage_source

import (
	"container/list"
	"sync"
	"time"
)

const (
	// cacheTTL is how long one viewer's read of a blob is reused. A blob
	// never changes, so the bound is about the viewer's access: a revoked
	// GitHub connection stops serving them within it.
	cacheTTL = 5 * time.Minute
	// cacheMaxBytes bounds the cache's memory, across viewers.
	cacheMaxBytes = 64 << 20
)

// blobCache keeps blob bytes read from GitHub, in memory only, keyed by
// (viewer, blob id): one viewer's read never answers another, whose GitHub
// access may differ. Least recently used entries go first once the byte
// bound is reached.
type blobCache struct {
	mu       sync.Mutex
	ttl      time.Duration
	maxBytes int
	now      func() time.Time
	bytes    int
	order    *list.List // front: most recently used
	entries  map[blobKey]*list.Element
}

type blobKey struct{ viewer, blobID string }

type blobEntry struct {
	key     blobKey
	content []byte
	expires time.Time
}

func newBlobCache(ttl time.Duration, maxBytes int, now func() time.Time) *blobCache {
	return &blobCache{ttl: ttl, maxBytes: maxBytes, now: now, order: list.New(), entries: map[blobKey]*list.Element{}}
}

func (c *blobCache) get(viewer, blobID string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[blobKey{viewer, blobID}]
	if !ok {
		return nil, false
	}
	e := el.Value.(*blobEntry)
	if !c.now().Before(e.expires) {
		c.remove(el)
		return nil, false
	}
	c.order.MoveToFront(el)
	return e.content, true
}

func (c *blobCache) put(viewer, blobID string, content []byte) {
	if len(content) > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := blobKey{viewer, blobID}
	if el, ok := c.entries[key]; ok {
		c.remove(el)
	}
	c.entries[key] = c.order.PushFront(&blobEntry{key: key, content: content, expires: c.now().Add(c.ttl)})
	c.bytes += len(content)
	for c.bytes > c.maxBytes {
		c.remove(c.order.Back())
	}
}

func (c *blobCache) remove(el *list.Element) {
	e := c.order.Remove(el).(*blobEntry)
	delete(c.entries, e.key)
	c.bytes -= len(e.content)
}
