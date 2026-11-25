// Copyright (c) 2025 Reliant Labs
package db

import (
	"container/list"
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// pinnedCatalogCacheBytes bounds the pinned catalogs one Repo keeps in
// memory. A machine's catalog runs to hundreds of KB, so this holds the
// catalogs of a few hundred distinct pins: every run and fire active in a
// process at once, many times over.
const pinnedCatalogCacheBytes = 64 << 20

// pinnedCatalogTouchEvery is how stale a cached pin's last_used_at may get
// before a read bumps it again. The retention prune deletes a pin unused for
// CatalogSnapshotRetention (90 days), so an hour costs it nothing.
const pinnedCatalogTouchEvery = time.Hour

// pinnedCatalogCache keeps the catalogs GetPinnedProjectCatalog read, by pin,
// least recently used first out, bounded by their size in bytes.
//
// A pin is the sha256 of its catalog's content (catalogPin), so the row of a
// pin never changes: a cached catalog is the stored one, byte for byte. What
// can change is whether the row is still stored (the retention prune, a
// deleted project), so a hit still bumps last_used_at once every touchEvery,
// and a bump that finds no row evicts the pin and answers not found.
//
// Prod read each pinned row through UPDATE … RETURNING on every load of a
// run and every resolution of a fire: ~100ms each (decompressing hundreds of
// KB of workflow JSON), 68 a minute, and a row lock taken in every
// transaction that read it.
type pinnedCatalogCache struct {
	mu         sync.Mutex
	maxBytes   int64
	touchEvery time.Duration
	bytes      int64
	order      *list.List // of *pinnedCatalogEntry, most recently used first
	entries    map[string]*list.Element
}

type pinnedCatalogEntry struct {
	pin     string
	catalog *PinnedCatalog
	size    int64
	// touched is when last_used_at was last set, by this process, at or
	// after.
	touched time.Time
}

func newPinnedCatalogCache(maxBytes int64, touchEvery time.Duration) *pinnedCatalogCache {
	return &pinnedCatalogCache{maxBytes: maxBytes, touchEvery: touchEvery, order: list.New(), entries: map[string]*list.Element{}}
}

// get is pin's cached catalog and whether its last_used_at is due a bump at
// now.
func (c *pinnedCatalogCache) get(pin string, now time.Time) (catalog *PinnedCatalog, touchDue, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[pin]
	if !ok {
		return nil, false, false
	}
	c.order.MoveToFront(el)
	e := el.Value.(*pinnedCatalogEntry)
	return e.catalog, now.Sub(e.touched) >= c.touchEvery, true
}

// add keeps catalog as pin's, its last_used_at set at touched. A catalog
// larger than the whole cache is not kept.
func (c *pinnedCatalogCache) add(pin string, catalog *PinnedCatalog, touched time.Time) {
	size := pinnedCatalogSize(catalog)
	c.mu.Lock()
	defer c.mu.Unlock()
	if size > c.maxBytes {
		return
	}
	if el, ok := c.entries[pin]; ok {
		c.order.MoveToFront(el)
		if e := el.Value.(*pinnedCatalogEntry); touched.After(e.touched) {
			e.touched = touched
		}
		return
	}
	c.entries[pin] = c.order.PushFront(&pinnedCatalogEntry{pin: pin, catalog: catalog, size: size, touched: touched})
	c.bytes += size
	for c.bytes > c.maxBytes {
		c.removeElement(c.order.Back())
	}
}

// touched records that pin's last_used_at was set at t.
func (c *pinnedCatalogCache) touched(pin string, t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[pin]; ok {
		if e := el.Value.(*pinnedCatalogEntry); t.After(e.touched) {
			e.touched = t
		}
	}
}

// remove forgets pin.
func (c *pinnedCatalogCache) remove(pin string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[pin]; ok {
		c.removeElement(el)
	}
}

func (c *pinnedCatalogCache) removeElement(el *list.Element) {
	e := c.order.Remove(el).(*pinnedCatalogEntry)
	delete(c.entries, e.pin)
	c.bytes -= e.size
}

// pinnedCatalogSize is roughly the memory a decoded catalog holds: its JSON,
// and each account workflow's definition.
func pinnedCatalogSize(c *PinnedCatalog) int64 {
	size := int64(256)
	if c.Workflows != nil {
		size += int64(len(*c.Workflows))
	}
	if c.Presets != nil {
		size += int64(len(*c.Presets))
	}
	for slug, w := range c.UserWorkflows {
		size += int64(len(slug)+len(w.Definition)) + 64
	}
	for _, s := range c.RepoStates {
		size += int64(len(s.Repo)+len(s.Branch)+len(s.Commit)) + 64
	}
	return size
}

// sharedPinnedCatalog is the catalog a cache hit returns: a copy of the
// cached struct, so a caller that sets a field cannot change another's. Its
// strings, map and slice are shared and read-only, as every reader treats
// them.
func sharedPinnedCatalog(c *PinnedCatalog) *PinnedCatalog {
	out := *c
	return &out
}

// pinnedCatalogs is r's pinned catalog cache, built on first use.
func (r *Repo) pinnedCatalogs() *pinnedCatalogCache {
	r.pinnedOnce.Do(func() {
		r.pinned = newPinnedCatalogCache(pinnedCatalogCacheBytes, pinnedCatalogTouchEvery)
	})
	return r.pinned
}

var (
	pinnedReadsOnce sync.Once
	pinnedReadsCtr  metric.Int64Counter
)

// countPinnedRead counts one GetPinnedProjectCatalog by where it was
// answered: "hit" from the cache, "miss" from the database.
func countPinnedRead(ctx context.Context, result string) {
	pinnedReadsOnce.Do(func() {
		var err error
		pinnedReadsCtr, err = otel.GetMeterProvider().Meter("github.com/reliant-labs/reliant/internal/db").
			Int64Counter("reliant.workflow_catalog.pinned_reads",
				metric.WithUnit("{read}"),
				metric.WithDescription("Reads of a pinned workflow catalog, by result: hit (served from the process's cache) or miss (read from the database)."))
		// An instrument the SDK refuses still comes back usable (a no-op).
		otel.Handle(err)
	})
	pinnedReadsCtr.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}
