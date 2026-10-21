package tools

import (
	"container/list"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/mcp"
)

// A provider's prompt cache is a prefix match that starts with the tools array,
// so a tool list that flickers between two requests re-writes the whole
// conversation. The MCP part of that list comes from the user's machine, which
// is routinely suspended or still starting: discovery then fails, and a registry
// built per call has nothing to fall back on. This cache is the memory those
// registries lack. It holds the last listing that SUCCEEDED, per (scope,
// identity), and serves it only when a fresh listing cannot be had.

const (
	// DefaultMCPToolCacheTTL is how long after its last successful listing an
	// entry may still be served. Every success refreshes it, so it bounds only a
	// CONTINUOUS outage. It is hours because the provider's prompt cache lives at
	// most an hour: a shorter TTL would flip the list while the cache it protects
	// is still warm, and a machine that sleeps through lunch would come back to a
	// different tools array for no reason.
	DefaultMCPToolCacheTTL = 6 * time.Hour

	// DefaultMCPToolCacheSize bounds the entries held. One entry is one project
	// scope for one user on one daemon: a few dozen tool schemas, tens of KB.
	DefaultMCPToolCacheSize = 256
)

// MCPToolsSource says where a request's MCP tools came from.
type MCPToolsSource string

const (
	// MCPToolsLive: a listing succeeded for this request.
	MCPToolsLive MCPToolsSource = "live"
	// MCPToolsStale: the listing failed and the last successful one was served.
	MCPToolsStale MCPToolsSource = "stale-last-known-good"
	// MCPToolsNone: the listing failed and there was nothing to fall back on.
	MCPToolsNone MCPToolsSource = "none"
	// MCPToolsUnrequested: this request never asked for MCP tools.
	MCPToolsUnrequested MCPToolsSource = "unrequested"
)

// MCPListIdentity is implemented by a runtime whose tool listing depends on who
// is asking and which daemon answers. The returned string must differ whenever
// the listing could. A runtime without it is never cached: sharing a list
// between callers that might see different servers is worse than not caching.
type MCPListIdentity interface {
	MCPListIdentity() string
}

type mcpToolCacheKey struct {
	scope    string
	identity string
}

type mcpToolCacheEntry struct {
	key   mcpToolCacheKey
	tools map[string][]mcp.Tool
	at    time.Time
}

// MCPToolCache is safe for concurrent use. It stores the raw per-server
// listing, not adapters, so every request builds its own adapters from it.
type MCPToolCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	limit int
	now   func() time.Time
	index map[mcpToolCacheKey]*list.Element
	lru   *list.List // front = most recently used
}

// NewMCPToolCache returns a cache with the default TTL and size bound.
func NewMCPToolCache() *MCPToolCache {
	return newMCPToolCache(DefaultMCPToolCacheTTL, DefaultMCPToolCacheSize, time.Now)
}

func newMCPToolCache(ttl time.Duration, limit int, now func() time.Time) *MCPToolCache {
	return &MCPToolCache{
		ttl:   ttl,
		limit: limit,
		now:   now,
		index: make(map[mcpToolCacheKey]*list.Element),
		lru:   list.New(),
	}
}

// put records a successful listing, replacing whatever the key held.
func (c *MCPToolCache) put(key mcpToolCacheKey, serverTools map[string][]mcp.Tool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.index[key]; ok {
		entry := el.Value.(*mcpToolCacheEntry)
		entry.tools, entry.at = serverTools, c.now()
		c.lru.MoveToFront(el)
		return
	}
	c.index[key] = c.lru.PushFront(&mcpToolCacheEntry{key: key, tools: serverTools, at: c.now()})
	for c.lru.Len() > c.limit {
		oldest := c.lru.Back()
		c.lru.Remove(oldest)
		delete(c.index, oldest.Value.(*mcpToolCacheEntry).key)
	}
}

// get returns the last successful listing for key and how old it is. An entry
// past the TTL is dropped.
func (c *MCPToolCache) get(key mcpToolCacheKey) (map[string][]mcp.Tool, time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.index[key]
	if !ok {
		return nil, 0, false
	}
	entry := el.Value.(*mcpToolCacheEntry)
	age := c.now().Sub(entry.at)
	if age > c.ttl {
		c.lru.Remove(el)
		delete(c.index, key)
		return nil, 0, false
	}
	c.lru.MoveToFront(el)
	return entry.tools, age, true
}

func (c *MCPToolCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len()
}

// MCPToolSet is a request's MCP tools and where they came from.
type MCPToolSet struct {
	Tools  []Tool
	Source MCPToolsSource
	// StaleAge is how old the served listing is. Zero unless Source is stale.
	StaleAge time.Duration
}
