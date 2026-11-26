// Copyright (c) 2025 Reliant Labs
package db

import (
	"container/list"
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// catalogColumnCacheBytes bounds the live catalog columns one Repo keeps in
// memory: each machine's synced workflows and presets of a project, and each
// project's union of them. A machine's workflows run to hundreds of KB
// compressed in storage (325 kB on average in prod, 2026-11-18), a few MB as
// a string, so this holds the catalogs a process resolves against at once.
const catalogColumnCacheBytes = 64 << 20

// catalogColumnCache keeps the catalog JSON columns the Repo read, each with
// the version of the row it was read from (store's rowVersionSQL), least
// recently used first out, bounded by their size in bytes.
//
// Unlike a pinned catalog's, a live catalog's row changes on every sync, so a
// hit is never served on faith: every read first reads the row's version, a
// few bytes, and the kept value answers only when that version is the one it
// was read at. Any write to the row changes the version, so a hit is the
// stored value byte for byte, in the caller's own transaction view; a changed
// or missing row is read again (or answered missing) and replaces the entry.
//
// The value is the column's string, shared: a hit hands out a pointer of its
// own to it, so a caller that sets *p cannot change another's, and strings do
// not change. Serving the same string each time also lets
// workflowsource's catalog index find it by identity rather than by hashing
// it again.
type catalogColumnCache struct {
	mu       sync.Mutex
	maxBytes int64
	bytes    int64
	order    *list.List // of *catalogColumnEntry, most recently used first
	entries  map[catalogColumnKey]*list.Element
}

// catalogColumnKey names one column of one catalog row.
type catalogColumnKey struct {
	column    string
	projectID string
	daemonID  string // "" for the project's own record (project_configs)
}

type catalogColumnEntry struct {
	key     catalogColumnKey
	version string
	value   *string
	size    int64
}

func newCatalogColumnCache(maxBytes int64) *catalogColumnCache {
	return &catalogColumnCache{maxBytes: maxBytes, order: list.New(), entries: map[catalogColumnKey]*list.Element{}}
}

// catalogColumnReads is how one cached column is read: the version of its
// row (found false when there is no row), and the column with that version in
// one statement.
type catalogColumnReads struct {
	version   func(ctx context.Context) (version string, found bool, err error)
	versioned func(ctx context.Context) (value *string, version string, found bool, err error)
}

// read is key's value, from the cache when its row is still the version it
// was read at, and found false when there is no row. A read from the
// database is kept only when fill is set: a read inside a transaction never
// fills the cache (the transaction may be reading a row it wrote itself, and
// may roll back), though it is served from it, version checked in its own
// view, like any other.
func (c *catalogColumnCache) read(ctx context.Context, key catalogColumnKey, reads catalogColumnReads, fill bool) (*string, bool, error) {
	if kept, ok := c.version(key); ok {
		current, found, err := reads.version(ctx)
		if err != nil {
			return nil, false, err
		}
		if !found {
			if fill {
				c.remove(key)
			}
			countCatalogColumnRead(ctx, key.column, "missing")
			return nil, false, nil
		}
		if current == kept {
			if value, ok := c.get(key, current); ok {
				countCatalogColumnRead(ctx, key.column, "hit")
				return value, true, nil
			}
		}
	}
	value, version, found, err := reads.versioned(ctx)
	if err != nil {
		return nil, false, err
	}
	if !found {
		if fill {
			c.remove(key)
		}
		countCatalogColumnRead(ctx, key.column, "missing")
		return nil, false, nil
	}
	if fill {
		c.add(key, version, value)
	}
	countCatalogColumnRead(ctx, key.column, "miss")
	return ownPointer(value), true, nil
}

// version is the row version key's kept value was read at.
func (c *catalogColumnCache) version(key catalogColumnKey) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return "", false
	}
	return el.Value.(*catalogColumnEntry).version, true
}

// get is key's kept value when it was read at version.
func (c *catalogColumnCache) get(key catalogColumnKey, version string) (*string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	e := el.Value.(*catalogColumnEntry)
	if e.version != version {
		return nil, false
	}
	c.order.MoveToFront(el)
	return ownPointer(e.value), true
}

// add keeps value as key's, read at version, replacing what was kept. A
// value larger than a quarter of the cache is not kept: it would evict
// most of the others.
func (c *catalogColumnCache) add(key catalogColumnKey, version string, value *string) {
	size := int64(128 + len(version))
	if value != nil {
		size += int64(len(*value))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.removeElement(el)
	}
	if size > c.maxBytes/4 {
		return
	}
	c.entries[key] = c.order.PushFront(&catalogColumnEntry{key: key, version: version, value: ownPointer(value), size: size})
	c.bytes += size
	for c.bytes > c.maxBytes {
		c.removeElement(c.order.Back())
	}
}

// remove forgets key.
func (c *catalogColumnCache) remove(key catalogColumnKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.removeElement(el)
	}
}

func (c *catalogColumnCache) removeElement(el *list.Element) {
	e := c.order.Remove(el).(*catalogColumnEntry)
	delete(c.entries, e.key)
	c.bytes -= e.size
}

// ownPointer is a pointer of the caller's own to v's string (nil for NULL).
func ownPointer(v *string) *string {
	if v == nil {
		return nil
	}
	s := *v
	return &s
}

// catalogColumns is r's catalog column cache, built on first use.
func (r *Repo) catalogColumns() *catalogColumnCache {
	r.catalogColumnsOnce.Do(func() {
		r.catalogColumnsCache = newCatalogColumnCache(catalogColumnCacheBytes)
	})
	return r.catalogColumnsCache
}

var (
	catalogColumnReadsOnce sync.Once
	catalogColumnReadsCtr  metric.Int64Counter
)

// countCatalogColumnRead counts one read of a catalog column by where it was
// answered: "hit" from the cache after a version check, "miss" from the
// database, "missing" when there is no row.
func countCatalogColumnRead(ctx context.Context, column, result string) {
	catalogColumnReadsOnce.Do(func() {
		var err error
		catalogColumnReadsCtr, err = otel.GetMeterProvider().Meter("github.com/reliant-labs/reliant/internal/db").
			Int64Counter("reliant.workflow_catalog.column_reads",
				metric.WithUnit("{read}"),
				metric.WithDescription("Reads of a live workflow catalog column (a machine's or a project's synced workflows or presets), by column and result: hit (served from the process's cache after a row-version check), miss (read from the database) or missing (no row)."))
		// An instrument the SDK refuses still comes back usable (a no-op).
		otel.Handle(err)
	})
	catalogColumnReadsCtr.Add(ctx, 1, metric.WithAttributes(attribute.String("column", column), attribute.String("result", result)))
}

// fillsCatalogColumns reports whether a catalog column r reads under ctx may
// be kept: not inside a transaction (catalogColumnCache.read).
func (r *Repo) fillsCatalogColumns(ctx context.Context) bool {
	return r.DB != nil && !r.DB.inTx(ctx)
}
