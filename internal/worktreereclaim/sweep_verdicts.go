// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"
)

// The machine sweep's verdict cache (verdicts.json in the state directory).
//
// A pass re-judged every idle checkout from scratch: the ignored-data walk,
// the capture envelope, `git for-each-ref --contains` over every remote ref
// (2.7s with 3,900 of them), the squash/patch checks and, for one with work,
// a hash of every changed file (up to 3s). On a machine with ~600 idle
// checkouts the 30-minute budget reached about 190 of them, the same ones
// first every time, and deferred the rest "out of time this pass" pass after
// pass (357 of 1223 on 2026-11-25), among them clean pushed checkouts that
// were never judged at all.
//
// So a verdict that keeps the checkout is remembered with the state it was
// judged on, and reused while that state is unchanged and the verdict is
// younger than verdictTTL: same HEAD, branch, index, reflog and newest file
// time. Only verdicts that keep the checkout are cached (work only it holds,
// ignored data, too large, hidden index flags); a verdict that would let it
// go (auto-safe) is never cached, and reclaimEntry judges every checkout
// again from scratch under its locks before it captures anything, so a stale
// entry can only delay a reclaim, never cause one. Ref and effective config
// changes invalidate the stamp too; the TTL bounds changes only visible on
// the network (such as a merge not yet fetched).

const (
	verdictsFile = "verdicts.json"
	// verdictTTL bounds how long a cached verdict stands without a fresh
	// look, for the changes its stamp cannot see.
	verdictTTL = 6 * time.Hour
)

// cachedVerdict is what evaluateDeep found about one checkout in one state.
type cachedVerdict struct {
	Stamp string    `json:"stamp"`
	At    time.Time `json:"at"`
	// Reason/Detail are the verdict: the data hold, the envelope's or the
	// hidden flags' hold, or the git classification.
	Reason        HeldReason `json:"reason"`
	Detail        string     `json:"detail,omitempty"`
	Base          string     `json:"base,omitempty"`
	Fingerprint   string     `json:"fingerprint,omitempty"`
	Work          string     `json:"work,omitempty"`
	UnpushedCount int        `json:"unpushed_commits,omitempty"`
	Bytes         int64      `json:"bytes,omitempty"`
}

type verdictsDoc struct {
	Version  int                      `json:"version"`
	Verdicts map[string]cachedVerdict `json:"verdicts"`
}

// verdictCache is the cache one pass reads and rewrites. Nil-safe: a nil
// cache remembers nothing.
type verdictCache struct {
	mu   sync.Mutex
	path string
	old  map[string]cachedVerdict
	new  map[string]cachedVerdict
}

func loadVerdicts(dir string) *verdictCache {
	c := &verdictCache{path: filepath.Join(dir, verdictsFile), old: map[string]cachedVerdict{}, new: map[string]cachedVerdict{}}
	if b, err := os.ReadFile(c.path); err == nil {
		var doc verdictsDoc
		if json.Unmarshal(b, &doc) == nil && doc.Version == 1 && doc.Verdicts != nil {
			c.old = doc.Verdicts
		}
	}
	return c
}

func verdictKey(e *SweepEntry) string { return e.Key + "\x00" + resolvePath(e.Path) }

// get returns the cached verdict for e in exactly this state, still fresh.
func (c *verdictCache) get(e *SweepEntry, stamp string, now time.Time) (cachedVerdict, bool) {
	if c == nil || stamp == "" {
		return cachedVerdict{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.old[verdictKey(e)]
	if !ok || v.Stamp != stamp || now.Sub(v.At) >= verdictTTL || v.At.After(now) {
		return cachedVerdict{}, false
	}
	c.new[verdictKey(e)] = v
	return v, true
}

// put remembers a verdict that keeps the checkout.
func (c *verdictCache) put(e *SweepEntry, v cachedVerdict) {
	if c == nil || v.Stamp == "" || !cacheable(v.Reason) {
		return
	}
	c.mu.Lock()
	c.new[verdictKey(e)] = v
	c.mu.Unlock()
}

// lastByKey indexes the last report's entries as orderDeep reads them.
func lastByKey(dir string) map[string]SweepEntry {
	out := map[string]SweepEntry{}
	if rep, err := ReadSweepReport(dir); err == nil {
		for i := range rep.Entries {
			out[verdictKey(&rep.Entries[i])] = rep.Entries[i]
		}
	}
	return out
}

// keepUnvisited carries an entry this pass did not look at (deferred, held by a
// cheaper check) into the next file, so a pass that runs out of time does
// not forget what the last one learned.
func (c *verdictCache) keepUnvisited(now time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range c.old {
		if _, ok := c.new[k]; !ok && now.Sub(v.At) < verdictTTL {
			c.new[k] = v
		}
	}
}

func (c *verdictCache) save() {
	if c == nil {
		return
	}
	c.mu.Lock()
	b, err := json.Marshal(verdictsDoc{Version: 1, Verdicts: c.new})
	c.mu.Unlock()
	if err == nil {
		_ = writeFileAtomic(c.path, b)
	}
}

// bytesOf is the size last recorded for e, 0 when none is.
func (c *verdictCache) bytesOf(e *SweepEntry) int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.old[verdictKey(e)].Bytes
}

// cacheable says which verdicts may be reused: only those that keep the
// checkout and come from its own state. Unverified is a failure to look,
// never cached.
func cacheable(r HeldReason) bool {
	switch r {
	case ReasonDirty, ReasonUnpushed, ReasonUnreachable, ReasonData, ReasonTooLarge:
		return true
	}
	return false
}

// verdictStamp names the state a verdict is about: HEAD, branch, index,
// reflog, newest file time, refs (including packed and per-worktree refs),
// pseudo-refs and effective git configuration. A failed read caches nothing.
func verdictStamp(ctx context.Context, e *SweepEntry, newest time.Time) string {
	admin := adminDir(e.Path)
	if admin == "" {
		return ""
	}
	head, err := os.ReadFile(filepath.Join(admin, "HEAD"))
	if err != nil {
		return ""
	}
	stamp := fmt.Sprintf("%q|%d", head, newest.UnixNano())
	for _, f := range []string{"index", filepath.Join("logs", "HEAD")} {
		fi, err := os.Stat(filepath.Join(admin, f))
		switch {
		case err == nil:
			stamp += fmt.Sprintf("|%s:%d:%d", f, fi.Size(), fi.ModTime().UnixNano())
		case os.IsNotExist(err):
			stamp += "|" + f + ":-"
		default:
			return ""
		}
	}
	// Ask git rather than walking refs/: packed refs and alternate ref
	// backends must invalidate exactly as loose refs do. This enumerates tips,
	// not reachability (the expensive classification this cache avoids).
	refs, err := git(ctx, e.Path, nil, "for-each-ref", "--format=%(refname) %(objectname) %(symref)")
	if err != nil {
		return ""
	}
	config, err := git(ctx, e.Path, nil, "config", "--null", "--list", "--includes")
	if err != nil {
		return ""
	}
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%q|%q", refs, config)
	for _, name := range []string{"ORIG_HEAD", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD"} {
		b, err := os.ReadFile(filepath.Join(admin, name))
		if err != nil && !os.IsNotExist(err) {
			return ""
		}
		_, _ = fmt.Fprintf(h, "|%s:%q", name, b)
	}
	return fmt.Sprintf("%s|%s|%x", stamp, e.Head, h.Sum(nil))
}

// sweepWorkerCount is how many worktrees a pass inspects at once: four, or
// under disk pressure a quarter of the CPUs (at most 16), because then what a
// pass does not reach stays on disk.
func sweepWorkerCount(pressure bool) int {
	if !pressure {
		return sweepWorkers
	}
	return min(16, max(sweepWorkers, runtime.NumCPU()/4))
}

// orderDeep orders the worktrees a pass inspects in depth. First the ones the
// last pass did not reach, or not report at all (new), oldest activity first:
// no worktree waits more than one pass for its first look. Then, under
// pressure, the largest first (the size the last look measured); otherwise
// the oldest activity first.
func orderDeep(deep []int, entries []SweepEntry, activity []time.Time, last map[string]SweepEntry, cache *verdictCache, pressure bool) {
	unreached := func(i int) bool {
		p, ok := last[verdictKey(&entries[i])]
		return !ok || p.Outcome == OutcomeSweepDeferred
	}
	size := func(i int) int64 {
		if p, ok := last[verdictKey(&entries[i])]; ok && p.Bytes > 0 {
			return p.Bytes
		}
		return cache.bytesOf(&entries[i])
	}
	sort.SliceStable(deep, func(a, b int) bool {
		x, y := deep[a], deep[b]
		if ux, uy := unreached(x), unreached(y); ux != uy {
			return ux
		}
		if pressure {
			if sx, sy := size(x), size(y); sx != sy {
				return sx > sy
			}
		}
		return activity[x].Before(activity[y])
	})
}
