package config

import (
	"container/list"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// StoredConfigVersions reports a cheap version token for a project's stored
// config record, read without the record's payload (db.Repo
// GetProjectConfigVersion). The token must change whenever the record is
// rewritten; it is compared for equality only. A project with no record
// returns sql.ErrNoRows.
type StoredConfigVersions interface {
	GetProjectConfigVersion(ctx context.Context, projectID string) (string, error)
}

// DefaultParsedConfigCacheBytes bounds the parsed configs a cached provider
// retains, measured as the payload bytes of the records they were parsed
// from. 128 MiB holds ~7 of the largest prod records (18 MB, a workspace of
// many nested checkouts) on a 2 GiB worker; typical records are far smaller.
const DefaultParsedConfigCacheBytes int64 = 128 << 20

// maxParsedConfigCacheEntries bounds the entry count independently of bytes,
// so many tiny projects cannot grow the index without limit.
const maxParsedConfigCacheEntries = 512

// parsedConfigLoadTimeout bounds a shared cache fill. The fill is detached
// from any one caller's context (see get), so it needs its own deadline.
const parsedConfigLoadTimeout = 30 * time.Second

// NewCachedStoredConfigProvider returns a provider that parses each project's
// record once per version and serves the parsed Config from memory until the
// record changes.
//
// Every call still makes one tiny read — the version token — so a record
// rewritten by any replica (a daemon push lands on whichever server holds its
// connection) is seen on the very next call, on every worker. Staleness is
// bounded by that check, not by a TTL.
//
// Why it exists: the temporal worker resolved the project config on every LLM
// call, and for a workspace of ~30 nested checkouts the record is ~18 MB of
// skills. In 22 minutes of prod that was 8 GB of row reads and 18.45 GB of
// skill JSON parsing — over half of all worker allocation, and the GC
// pressure behind its memory ceiling.
//
// maxBytes <= 0 selects DefaultParsedConfigCacheBytes. A record larger than
// the budget is parsed and returned but not retained.
func NewCachedStoredConfigProvider(store StoredConfigStore, versions StoredConfigVersions, maxBytes int64) *StoredConfigProvider {
	if maxBytes <= 0 {
		maxBytes = DefaultParsedConfigCacheBytes
	}
	return &StoredConfigProvider{
		store: store,
		cache: &parsedConfigCache{
			versions:   versions,
			maxBytes:   maxBytes,
			maxEntries: maxParsedConfigCacheEntries,
			lru:        list.New(),
			byProject:  map[string]*list.Element{},
		},
	}
}

// parsedConfigCache is an LRU of parsed project configs, keyed by project and
// validated by the record's version token.
type parsedConfigCache struct {
	versions   StoredConfigVersions
	maxBytes   int64
	maxEntries int

	mu        sync.Mutex
	lru       *list.List // of *parsedConfigEntry; front is most recently used
	byProject map[string]*list.Element
	bytes     int64

	// flight collapses concurrent misses for one (project, version) into a
	// single read and parse: a fan-out of agents on one project all miss
	// together right after a daemon push.
	flight singleflight.Group
}

type parsedConfigEntry struct {
	projectID string
	version   string
	cfg       *Config
	size      int64
}

type loadFunc func(ctx context.Context, projectID string) (*Config, int64, error)

func (c *parsedConfigCache) get(ctx context.Context, projectID string, load loadFunc) (*Config, error) {
	version, err := c.versions.GetProjectConfigVersion(ctx, projectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.evict(projectID)
			return notSyncedConfig(projectID), nil
		}
		return nil, fmt.Errorf("failed to load stored config version for project %s: %w", projectID, err)
	}

	if cfg, ok := c.lookup(projectID, version); ok {
		return cfg.shareableCopy(), nil
	}

	// The fill is shared by every caller that missed on this version, so it
	// must not die with whichever caller happened to start it. Each caller
	// still stops waiting when its own context ends.
	ch := c.flight.DoChan(projectID+"\x00"+version, func() (any, error) {
		if cfg, ok := c.lookup(projectID, version); ok {
			return cfg, nil
		}
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), parsedConfigLoadTimeout)
		defer cancel()
		cfg, size, err := load(loadCtx, projectID)
		if err != nil {
			return nil, err
		}
		// Labelled with the version read BEFORE the record. If a write landed
		// in between, the record is newer than its label, so the next call
		// sees a different version and re-reads: an extra read, never stale
		// content served under a newer label.
		c.store(projectID, version, cfg, size)
		return cfg, nil
	})

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			if errors.Is(res.Err, sql.ErrNoRows) {
				// Deleted between the version read and the record read.
				c.evict(projectID)
				return notSyncedConfig(projectID), nil
			}
			return nil, res.Err
		}
		return res.Val.(*Config).shareableCopy(), nil
	}
}

func (c *parsedConfigCache) lookup(projectID, version string) (*Config, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.byProject[projectID]
	if !ok {
		return nil, false
	}
	entry := el.Value.(*parsedConfigEntry)
	if entry.version != version {
		return nil, false
	}
	c.lru.MoveToFront(el)
	return entry.cfg, true
}

func (c *parsedConfigCache) store(projectID, version string, cfg *Config, size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeLocked(projectID)
	if size > c.maxBytes {
		return
	}
	c.byProject[projectID] = c.lru.PushFront(&parsedConfigEntry{
		projectID: projectID, version: version, cfg: cfg, size: size,
	})
	c.bytes += size
	for c.bytes > c.maxBytes || c.lru.Len() > c.maxEntries {
		oldest := c.lru.Back()
		c.removeLocked(oldest.Value.(*parsedConfigEntry).projectID)
	}
}

func (c *parsedConfigCache) evict(projectID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeLocked(projectID)
}

func (c *parsedConfigCache) removeLocked(projectID string) {
	el, ok := c.byProject[projectID]
	if !ok {
		return
	}
	c.bytes -= el.Value.(*parsedConfigEntry).size
	c.lru.Remove(el)
	delete(c.byProject, projectID)
}

// shareableCopy returns a Config a caller may treat as its own at the top
// level — append to or sort Skills, add MCP servers — without racing another
// caller holding the same cached Config. It copies the slices and maps, not
// what they hold: a skill's Body, Metadata and AllowedTools stay shared and
// are read-only. A few hundred skill headers cost ~100 KB, against the tens
// of MB a re-parse costs.
func (c *Config) shareableCopy() *Config {
	out := *c
	out.ContextPaths = slices.Clone(c.ContextPaths)
	// An MCP server's own slices and map are what a caller edits (append an
	// arg, set a header), so they are copied too, not just the outer map.
	if c.MCPServers != nil {
		out.MCPServers = make(map[string]MCPServer, len(c.MCPServers))
		for name, server := range c.MCPServers {
			server.Args = slices.Clone(server.Args)
			server.Env = slices.Clone(server.Env)
			server.Headers = maps.Clone(server.Headers)
			server.RequiresFiles = slices.Clone(server.RequiresFiles)
			out.MCPServers[name] = server
		}
	}
	out.Skills = slices.Clone(c.Skills)
	out.RepoMemories = maps.Clone(c.RepoMemories)
	return &out
}
