// Copyright (c) 2025 Reliant Labs
package workflowsource

import (
	"crypto/sha256"
	"sync"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
)

// indexCacheSize bounds the catalog indexes kept: one per distinct catalog
// content, so a few per active project machine.
const indexCacheSize = 32

// indexes memoizes catalogIndex by the catalog's content. Indexing a catalog
// parses every workflow file in it (each file's name: is read from its YAML),
// and a machine's catalog runs to hundreds of KB: rebuilding it on every
// resolution was 15% of ListInbox's CPU, which resolves each activated
// workflow, and each of its children, against the same catalog. The key is
// the content itself, so a hit is indistinguishable from a rebuild and a sync
// that changes a byte is a miss. A cached *workflowref.Index is shared and
// must be treated as read-only, as every caller already does.
var indexes = &indexCache{items: make(map[[sha256.Size]byte]*workflowref.Index, indexCacheSize)}

type indexCache struct {
	mu    sync.Mutex
	items map[[sha256.Size]byte]*workflowref.Index
	order [][sha256.Size]byte // insertion order, oldest first
	// recent is the last few catalog strings hashed and their keys, newest
	// first (keyOf).
	recent []hashedCatalog
}

// recentCatalogs bounds indexCache.recent. Each entry keeps its string alive,
// so it is a few active catalogs, not the whole index cache.
const recentCatalogs = 8

type hashedCatalog struct {
	json string
	key  [sha256.Size]byte
}

// keyOf is the content key of a catalog's JSON. Hashing hundreds of KB on
// every resolution was ~40% of prod ListTriggers' CPU and a quarter of the
// trigger reconciler's (2026-11-24): a request, a fire or a pass reads one
// catalog once (launch.NewRequestResolver) and resolves many workflows
// against that one string. A string just hashed answers from recent: Go's
// string comparison returns at once when both share their bytes, the case
// here, and compares the bytes otherwise, so a hit is the same content
// either way and is never decided by an address alone.
func (c *indexCache) keyOf(json string) [sha256.Size]byte {
	c.mu.Lock()
	for i, h := range c.recent {
		if len(h.json) == len(json) && h.json == json {
			if i > 0 {
				copy(c.recent[1:i+1], c.recent[:i])
				c.recent[0] = h
			}
			c.mu.Unlock()
			return h.key
		}
	}
	c.mu.Unlock()
	key := sha256.Sum256([]byte(json))
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recent = append([]hashedCatalog{{json: json, key: key}}, c.recent...)
	if len(c.recent) > recentCatalogs {
		c.recent[recentCatalogs] = hashedCatalog{}
		c.recent = c.recent[:recentCatalogs]
	}
	return key
}

func (c *indexCache) get(key [sha256.Size]byte) (*workflowref.Index, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ix, ok := c.items[key]
	return ix, ok
}

func (c *indexCache) add(key [sha256.Size]byte, ix *workflowref.Index) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[key]; ok {
		return
	}
	c.items[key] = ix
	c.order = append(c.order, key)
	for len(c.order) > indexCacheSize {
		delete(c.items, c.order[0])
		c.order = c.order[1:]
	}
}

// catalogIndex indexes a stored catalog's workflows JSON, from the cache when
// the same content was indexed before.
func catalogIndex(workflowsJSON *string) (*workflowref.Index, error) {
	if workflowsJSON == nil || *workflowsJSON == "" {
		return config.ProjectWorkflowIndex(nil), nil
	}
	key := indexes.keyOf(*workflowsJSON)
	if ix, ok := indexes.get(key); ok {
		return ix, nil
	}
	workflows, err := config.ParseStoredWorkflows(workflowsJSON)
	if err != nil {
		return nil, err
	}
	ix := config.ProjectWorkflowIndex(workflows)
	indexes.add(key, ix)
	return ix, nil
}
