// Copyright (c) 2025 Reliant Labs
package workflowref

import (
	"crypto/sha256"
	"sync"

	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// parseCacheEntries and parseCacheBytes bound the parsed workflows kept: one
// per distinct definition (by content), so a few per activated workflow and
// its children. Bytes counts the YAML each entry was parsed from, a stand-in
// for the parsed tree's size (each entry holds that source too).
const (
	parseCacheEntries = 512
	parseCacheBytes   = 16 << 20
)

// parses memoizes wfyaml.ParseWorkflow by the definition's content.
// Resolution parses every workflow it resolves, and the readers that list
// automations resolve each activated workflow, and each workflow it calls, on
// every request: ListInbox (`inbox.declarationItems`, p50 1.77s of a 1.91s p50
// request in prod, 2026-11-25) and ListTriggers. Parsing a large definition
// is tens of milliseconds (about 38ms for an 800KB pr-review.yaml); hashing
// it and cloning the parsed tree is a small fraction of that
// (BenchmarkParseWorkflowCached).
//
// ParseWorkflow is a pure function of the bytes, so a hit is
// indistinguishable from a parse, and a sync that changes one byte is a
// miss. The digest only finds the entry: a hit is decided by comparing the
// bytes the entry was parsed from, never by the digest alone. Each caller gets its own copy (proto.Clone), exactly as it got its
// own fresh parse before: launch and the runtime mutate the workflow they
// resolved (inputs, overrides, bound secrets), and a shared tree would let
// one request's edits reach another's. A definition that does not parse is
// not cached: it is rare, and its error stays the parser's own.
var parses = &parseCache{items: make(map[[sha256.Size]byte]*parsedEntry, parseCacheEntries)}

// parseWorkflow is ParseWorkflow through parses; a test counts the parses it
// saves.
var parseWorkflow = wfyaml.ParseWorkflow

type parseCache struct {
	mu    sync.Mutex
	items map[[sha256.Size]byte]*parsedEntry
	order [][sha256.Size]byte // insertion order, oldest first
	bytes int
}

type parsedEntry struct {
	src  string              // the bytes wf was parsed from
	wf   *reliantv1.Workflow // never handed out: callers get clones
	size int
}

// parse returns a workflow of its own parsed from data.
func (c *parseCache) parse(data []byte) (*reliantv1.Workflow, error) {
	key := sha256.Sum256(data)
	c.mu.Lock()
	hit := c.items[key]
	c.mu.Unlock()
	if hit != nil && hit.src == string(data) {
		return proto.CloneOf(hit.wf), nil
	}
	wf, err := parseWorkflow(data)
	if err != nil {
		return nil, err
	}
	c.add(key, &parsedEntry{src: string(data), wf: proto.CloneOf(wf), size: len(data)})
	return wf, nil
}

func (c *parseCache) add(key [sha256.Size]byte, e *parsedEntry) {
	if e.size > parseCacheBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[key]; ok {
		return
	}
	c.items[key] = e
	c.order = append(c.order, key)
	c.bytes += e.size
	for len(c.order) > parseCacheEntries || c.bytes > parseCacheBytes {
		oldest := c.order[0]
		c.bytes -= c.items[oldest].size
		delete(c.items, oldest)
		c.order = c.order[1:]
	}
}
