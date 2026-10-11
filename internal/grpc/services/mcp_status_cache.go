// Copyright (c) 2025 Reliant Labs
package services

import (
	"container/list"
	"context"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// How long a project's MCP server status is served without asking the daemon
// again, kept as a fallback, and waited for.
//
// The status lives on the user's machine, so the only way to learn it is a
// daemon round trip. ListServers used to make 2+2N of those in a row (status
// for the clients, their health and each server's last error, plus a
// tools/list per server), each allowed 10s and none tied to the request: in
// prod it took 4.5s and 8.7s (p90) to render a settings page whose
// configuration is in the database. Now a page load costs at most one round
// trip, shared by every read in flight, and a machine that is slow to answer
// costs it a bounded wait rather than the daemon's timeout.
const (
	mcpStatusFreshFor = 5 * time.Second
	mcpStatusStaleFor = 5 * time.Minute
	// mcpStatusWait bounds how long reads wait for a status that has never
	// been answered. It runs from when the fetch STARTED, so readers that
	// arrive later wait only the remainder, and once it has passed nobody
	// waits: they answer with the configuration alone (status unknown) while
	// the fetch carries on to fill the cache for the next read.
	mcpStatusWait = 2 * time.Second
	// mcpStatusRefreshWait is how long a read with a stale answer in hand
	// waits for the refresh it started before settling for the stale one. A
	// healthy machine answers well inside it, so the page shows the current
	// status; a slow or wedged one costs the page this much and no more.
	mcpStatusRefreshWait = 250 * time.Millisecond
	// mcpStatusMaxEntries bounds the cache. Past it the least recently used
	// entry is dropped, fresh or not, so one-off project visits cannot grow
	// it with every user and project ever seen.
	mcpStatusMaxEntries = 512
)

// mcpStatusFetch asks the user's daemon for a project's MCP server status.
type mcpStatusFetch func(ctx context.Context, userID, projectPath string) (*daemonMCPServerStatus, error)

// mcpStatusCache holds the last MCP server status each (user, project) got
// from the daemon, and the one fetch in flight for it, LRU-bounded at
// maxEntries.
type mcpStatusCache struct {
	fetch mcpStatusFetch
	now   func() time.Time

	freshFor, staleFor, wait, refreshWait, fetchTimeout time.Duration
	maxEntries                                          int

	mu      sync.Mutex
	entries map[string]*mcpStatusEntry
	lru     *list.List // of keys; front is most recently used
}

type mcpStatusEntry struct {
	// status is the last answer; nil when the last fetch failed. fetchedAt
	// is when it was answered, zero when it never was (or was invalidated).
	status    *daemonMCPServerStatus
	fetchedAt time.Time
	// inflight is closed when the fetch in progress finishes; nil when none
	// is. A fetch caches its answer only while it is still the entry's
	// inflight one, so a fetch an invalidation orphaned never lands.
	inflight      chan struct{}
	inflightSince time.Time
	element       *list.Element // this entry's key in the LRU
}

func newMCPStatusCache(fetch mcpStatusFetch) *mcpStatusCache {
	return &mcpStatusCache{
		fetch:        fetch,
		now:          time.Now,
		freshFor:     mcpStatusFreshFor,
		staleFor:     mcpStatusStaleFor,
		wait:         mcpStatusWait,
		refreshWait:  mcpStatusRefreshWait,
		fetchTimeout: mcpStatusTimeout,
		maxEntries:   mcpStatusMaxEntries,
		entries:      map[string]*mcpStatusEntry{},
		lru:          list.New(),
	}
}

func mcpStatusKey(userID, projectPath string) string {
	return userID + "\x00" + projectPath
}

// get returns the project's MCP server status, or nil when it is not known:
// the daemon is unreachable, failed, or has not answered in time.
//
// An answer younger than freshFor is returned as is. Past that, get starts a
// refresh (one per project, however many reads arrive) and waits for it —
// for refreshWait when it has an older answer, up to staleFor, to fall back
// on, and for `wait` when it has none. Both run from when the refresh
// STARTED, so later readers wait only the remainder, never past ctx. A
// failure is an answer too: a machine that is down or wedged costs a page
// load at most refreshWait once it has been asked. A refresh outlives the
// readers that stop waiting for it and fills the cache for the next one.
func (c *mcpStatusCache) get(ctx context.Context, userID, projectPath string) *daemonMCPServerStatus {
	key := mcpStatusKey(userID, projectPath)
	c.mu.Lock()
	now := c.now()
	entry := c.entries[key]
	if entry == nil {
		entry = &mcpStatusEntry{}
		entry.element = c.lru.PushFront(key)
		c.entries[key] = entry
		c.evictLocked()
	} else {
		c.lru.MoveToFront(entry.element)
	}
	answered := !entry.fetchedAt.IsZero()
	age := now.Sub(entry.fetchedAt)
	if answered && age < c.freshFor {
		status := entry.status
		c.mu.Unlock()
		return status
	}
	done := c.startFetchLocked(ctx, key, userID, projectPath, entry)
	fallback, budget := (*daemonMCPServerStatus)(nil), c.wait
	if answered && age < c.staleFor {
		fallback, budget = entry.status, c.refreshWait
	}
	remaining := budget - now.Sub(entry.inflightSince)
	c.mu.Unlock()
	if remaining <= 0 {
		return fallback
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		return fallback
	case <-ctx.Done():
		return fallback
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if current := c.entries[key]; current != nil && !current.fetchedAt.IsZero() {
		c.lru.MoveToFront(current.element)
		return current.status
	}
	return fallback
}

// startFetchLocked starts a fetch for the entry unless one is in flight, and
// returns the channel that closes when the fetch finishes. The fetch is not
// the request's: it outlives a caller that stops waiting, so the answer still
// lands in the cache.
func (c *mcpStatusCache) startFetchLocked(ctx context.Context, key, userID, projectPath string, entry *mcpStatusEntry) chan struct{} {
	if entry.inflight != nil {
		return entry.inflight
	}
	done := make(chan struct{})
	entry.inflight, entry.inflightSince = done, c.now()
	fetchCtx := context.WithoutCancel(ctx)
	go func() {
		defer close(done)
		fetchCtx, cancel := context.WithTimeout(fetchCtx, c.fetchTimeout)
		defer cancel()
		status, err := c.fetch(fetchCtx, userID, projectPath)
		if err != nil {
			logging.Debug("MCP server status unavailable from daemon", "user_id", userID, "project_path", projectPath, "error", err)
			status = nil
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		current := c.entries[key]
		if current == nil || current.inflight != done {
			return
		}
		current.inflight = nil
		current.status, current.fetchedAt = status, c.now()
		c.lru.MoveToFront(current.element)
	}()
	return done
}

// invalidateUser forgets every status cached for a user. MCPService calls it
// after anything it does that can change a server's status — start, stop,
// restart, a config write — so the read that follows asks the daemon.
func (c *mcpStatusCache) invalidateUser(userID string) {
	if c == nil {
		return
	}
	prefix := userID + "\x00"
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.entries {
		if strings.HasPrefix(key, prefix) {
			entry.status, entry.fetchedAt, entry.inflight = nil, time.Time{}, nil
		}
	}
}

// evictLocked drops least recently used entries past maxEntries.
func (c *mcpStatusCache) evictLocked() {
	for len(c.entries) > c.maxEntries {
		oldest := c.lru.Back()
		if oldest == nil {
			return
		}
		key := oldest.Value.(string)
		delete(c.entries, key)
		c.lru.Remove(oldest)
	}
}
