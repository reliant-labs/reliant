// Copyright (c) 2025 Reliant Labs
package candidate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/mergequeue/gitport"
)

// Pruning the job caches. Host keeps one bare git cache per repository under
// Root so a repository is fetched in full once; nothing else removes them, so
// [Host.Prune] holds them to an age and a size budget, in the style of forge's
// pkg/gocache (which internal/diskwatch applies to the Go build caches): a
// cache unused for MaxAge goes, and while all of them together are over
// MaxBytes the least recently used go, down to pruneLowWater of it, never one
// used within MinIdle. A removed cache costs only a full fetch on the
// repository's next job.
//
// Last use. Every job takes its cache's lease, a sidecar file
// (<Root>/<owner>/<name>.git.lease), exclusively for as long as it runs, and
// stamps its mtime when it starts and when it ends; Prune reads that mtime,
// never atime (relatime, noatime). A cache from before leases existed is
// judged by the newest mtime of the files a fetch writes.
//
// In use. A cache is removed, or gc'd, only under both of the locks a job
// holds: the Host's per-repository mutex (jobs of this process) and the
// lease's advisory file lock (jobs of any daemon process on the machine).
// Prune takes them without waiting, so a running job is skipped, never waited
// for, and re-reads the last use under them before it removes anything. The
// cache is renamed aside before it is deleted, so a job never opens a
// half-deleted one; a pass interrupted mid-delete leaves only trash, which
// the next pass removes.

const (
	// DefaultPruneMaxAge removes a cache no job used for a week.
	DefaultPruneMaxAge = 7 * 24 * time.Hour
	// DefaultPruneMaxBytes is the budget for every job cache together.
	DefaultPruneMaxBytes = uint64(5) << 30
	// DefaultPruneMinIdle keeps a cache a job used within the hour out of
	// the size eviction: a repository with a steady stream of batches would
	// otherwise be refetched in full after every pass.
	DefaultPruneMinIdle = time.Hour
	// DefaultPruneGCTimeout bounds one cache's `git gc --auto`.
	DefaultPruneGCTimeout = 10 * time.Minute
	// pruneLowWater is the fraction of MaxBytes a size eviction brings the
	// caches down to, so one pass buys headroom (gocache.LowWater).
	pruneLowWater = 0.8
	// leasePoll is how often a job retries a lease another process holds.
	leasePoll = 200 * time.Millisecond
	// leaseSuffix names a cache's lease, beside it.
	leaseSuffix = ".lease"
	// trashMarker names a cache renamed aside for deletion.
	trashMarker = ".trash-"
)

// PrunePolicy is [Host.Prune]'s budget. The zero value of each field is its
// default.
type PrunePolicy struct {
	// MaxAge removes a cache unused for longer.
	MaxAge time.Duration
	// MaxBytes is the budget for every cache together.
	MaxBytes uint64
	// MinIdle keeps a cache used more recently out of the size eviction.
	MinIdle time.Duration
	// GC runs `git gc --auto` in every cache the pass keeps and no job is
	// using, which packs loose objects once git's own thresholds say so.
	GC bool
	// GCTimeout bounds one cache's gc.
	GCTimeout time.Duration
}

func (p PrunePolicy) withDefaults() PrunePolicy {
	if p.MaxAge <= 0 {
		p.MaxAge = DefaultPruneMaxAge
	}
	if p.MaxBytes == 0 {
		p.MaxBytes = DefaultPruneMaxBytes
	}
	if p.MinIdle <= 0 {
		p.MinIdle = DefaultPruneMinIdle
	}
	if p.GCTimeout <= 0 {
		p.GCTimeout = DefaultPruneGCTimeout
	}
	return p
}

// What Prune did with one cache.
const (
	PruneKept        = "kept"
	PruneInUse       = "in_use"
	PruneRemovedAge  = "removed_age"
	PruneRemovedSize = "removed_size"
	PruneFailed      = "failed"
)

// PrunedCache is one cache as Prune found it and what it did.
type PrunedCache struct {
	// Repository is "<owner>/<name>", lower-cased as the cache's path.
	Repository string
	Bytes      uint64
	LastUsed   time.Time
	Outcome    string
	// Err says why a removal or a gc failed.
	Err string
}

// PruneResult is what one Prune pass did.
type PruneResult struct {
	Caches []PrunedCache
	// Bytes is the size of every cache when the pass looked; Freed what it
	// removed.
	Bytes, Freed uint64
	// Budget is the policy's MaxBytes.
	Budget uint64
}

// Removed is how many caches the pass removed.
func (r PruneResult) Removed() int {
	n := 0
	for _, c := range r.Caches {
		if c.Outcome == PruneRemovedAge || c.Outcome == PruneRemovedSize {
			n++
		}
	}
	return n
}

// jobCache is one cache on disk.
type jobCache struct {
	repo     string
	dir      string
	bytes    uint64
	lastUsed time.Time
	result   *PrunedCache
}

// Prune holds the job caches under Root to policy: the ones no job used for
// MaxAge go, then, while all of them together are over MaxBytes, the least
// recently used, never one a job is using. With policy.GC it then runs
// `git gc --auto` in each cache it kept that no job is using. It never
// returns an error: what it could not do is in the result.
func (h *Host) Prune(ctx context.Context, policy PrunePolicy) PruneResult {
	policy = policy.withDefaults()
	res := PruneResult{Budget: policy.MaxBytes}
	if h.Root == "" {
		return res
	}
	now := h.clock()
	caches := h.discoverCaches(ctx, now, policy.MaxAge)
	res.Caches = make([]PrunedCache, len(caches))
	for i, c := range caches {
		res.Caches[i] = PrunedCache{Repository: c.repo, Bytes: c.bytes, LastUsed: c.lastUsed, Outcome: PruneKept}
		c.result = &res.Caches[i]
		res.Bytes += c.bytes
	}

	// Age first: a cache unused for MaxAge goes whatever the total.
	total := res.Bytes
	ageCutoff := now.Add(-policy.MaxAge)
	var rest []*jobCache
	for _, c := range caches {
		if ctx.Err() == nil && c.lastUsed.Before(ageCutoff) {
			if h.removeCache(c, func(lastUsed time.Time) bool { return lastUsed.Before(ageCutoff) }, PruneRemovedAge) {
				res.Freed += c.bytes
				total -= c.bytes
				continue
			}
		}
		rest = append(rest, c)
	}

	// Then size: least recently used first, down to the low-water mark.
	if total > policy.MaxBytes {
		target := uint64(float64(policy.MaxBytes) * pruneLowWater)
		idleCutoff := now.Add(-policy.MinIdle)
		sort.SliceStable(rest, func(i, j int) bool { return rest[i].lastUsed.Before(rest[j].lastUsed) })
		for _, c := range rest {
			if total <= target || ctx.Err() != nil || !c.lastUsed.Before(idleCutoff) {
				break // sorted: nothing later is idle long enough either
			}
			if h.removeCache(c, func(lastUsed time.Time) bool { return lastUsed.Before(idleCutoff) }, PruneRemovedSize) {
				res.Freed += c.bytes
				total -= c.bytes
			}
		}
	}

	if policy.GC {
		for _, c := range caches {
			if ctx.Err() != nil {
				break
			}
			if c.result.Outcome == PruneKept {
				h.gcCache(ctx, c, policy.GCTimeout)
			}
		}
	}
	return res
}

// discoverCaches lists the caches under Root with their size and last use,
// and removes what earlier passes left: trash, and the leases of caches gone
// for longer than maxAge.
func (h *Host) discoverCaches(ctx context.Context, now time.Time, maxAge time.Duration) []*jobCache {
	owners, err := os.ReadDir(h.Root)
	if err != nil {
		return nil
	}
	var out []*jobCache
	for _, o := range owners {
		if !o.IsDir() || ctx.Err() != nil {
			continue
		}
		ownerDir := filepath.Join(h.Root, o.Name())
		entries, err := os.ReadDir(ownerDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name, path := e.Name(), filepath.Join(ownerDir, e.Name())
			switch {
			case strings.Contains(name, trashMarker):
				// Renamed aside by a pass that did not finish deleting it:
				// no job opens it.
				_ = os.RemoveAll(path)
			case strings.HasSuffix(name, ".git"+leaseSuffix):
				dir := strings.TrimSuffix(path, leaseSuffix)
				if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
					h.removeOrphanLease(o.Name()+"/"+strings.TrimSuffix(strings.TrimSuffix(name, leaseSuffix), ".git"), dir, now.Add(-maxAge))
				}
			case e.IsDir() && strings.HasSuffix(name, ".git"):
				c := &jobCache{repo: o.Name() + "/" + strings.TrimSuffix(name, ".git"), dir: path}
				c.bytes = dirBytes(path)
				c.lastUsed = cacheLastUsed(path)
				out = append(out, c)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].repo < out[j].repo })
	return out
}

// removeCache removes c when no job holds it and, re-read under its locks,
// still eligible says so. It records what happened in c.result and reports
// whether c is gone.
func (h *Host) removeCache(c *jobCache, eligible func(lastUsed time.Time) bool, outcome string) bool {
	release, ok := h.tryHold(c)
	if !ok {
		c.result.Outcome = PruneInUse
		return false
	}
	defer release()
	// A job may have finished between the measure and the lock.
	if lastUsed := cacheLastUsed(c.dir); !eligible(lastUsed) {
		c.result.LastUsed = lastUsed
		return false
	}
	trash := fmt.Sprintf("%s%s%d", c.dir, trashMarker, h.clock().UnixNano())
	if err := os.Rename(c.dir, trash); err != nil {
		c.result.Outcome, c.result.Err = PruneFailed, err.Error()
		return false
	}
	c.result.Outcome = outcome
	if err := os.RemoveAll(trash); err != nil {
		// Out of the way already: the next pass removes the rest.
		c.result.Err = err.Error()
	}
	// The lease goes too, under its own lock: a job that opened it before
	// this unlink sees a lease no longer at its path once it gets the lock,
	// and takes the new one (acquireLease).
	_ = os.Remove(leasePath(c.dir))
	return true
}

// removeOrphanLease removes the lease of a cache already gone, once it is
// older than cutoff and no job holds it.
func (h *Host) removeOrphanLease(repo, dir string, cutoff time.Time) {
	info, err := os.Stat(leasePath(dir))
	if err != nil || !info.ModTime().Before(cutoff) {
		return
	}
	release, ok := h.tryHold(&jobCache{repo: repo, dir: dir})
	if !ok {
		return
	}
	defer release()
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		_ = os.Remove(leasePath(dir))
	}
}

// tryHold takes both of a job's locks on c without waiting: the Host's
// per-repository mutex, then the lease. ok is false when a job holds either.
func (h *Host) tryHold(c *jobCache) (release func(), ok bool) {
	lock := h.repoLock(c.repo)
	if !lock.TryLock() {
		return nil, false
	}
	lease, ok, err := tryLease(c.dir)
	if err != nil || !ok {
		lock.Unlock()
		return nil, false
	}
	return func() {
		releaseLease(lease)
		lock.Unlock()
	}, true
}

// gcCache runs `git gc --auto` in c under its locks, when no job holds it.
func (h *Host) gcCache(ctx context.Context, c *jobCache, timeout time.Duration) {
	release, ok := h.tryHold(c)
	if !ok {
		c.result.Outcome = PruneInUse
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// gc.autoDetach=false keeps gc in the foreground, under the locks: a
	// detached gc would still be running when the next job starts.
	cmd := exec.CommandContext(ctx, "git", "--git-dir="+c.dir, "-c", "gc.autoDetach=false", "gc", "--auto", "--quiet")
	cmd.Env = gitport.HermeticEnv(gitport.Identity{})
	cmd.WaitDelay = 5 * time.Second
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 500 {
			msg = msg[len(msg)-500:]
		}
		c.result.Err = fmt.Sprintf("git gc --auto: %v: %s", err, msg)
		return
	}
	c.bytes = dirBytes(c.dir)
	c.result.Bytes = c.bytes
}

// ---- leases ---------------------------------------------------------------

// leasePath is the lease of the cache at dir.
func leasePath(dir string) string { return dir + leaseSuffix }

// acquireLease takes the lease of the cache at dir, waiting while another
// process's job holds it, until ctx is done.
func acquireLease(ctx context.Context, dir string) (*os.File, error) {
	for {
		f, ok, err := tryLease(dir)
		if err != nil {
			return nil, err
		}
		if ok {
			return f, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(leasePoll):
		}
	}
}

// tryLease takes the lease of the cache at dir without waiting. ok is false
// when another holder has it. A lease Prune unlinked while this one waited
// for its lock is not held: the file at the path is, so it tries that one.
func tryLease(dir string) (f *os.File, ok bool, err error) {
	path := leasePath(dir)
	for range 3 {
		_, statErr := os.Stat(path)
		created := errors.Is(statErr, fs.ErrNotExist)
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil, false, fmt.Errorf("candidate: open the job cache's lease: %w", err)
		}
		locked, err := tryFlock(f)
		if err != nil || !locked {
			_ = f.Close()
			if err != nil {
				return nil, false, fmt.Errorf("candidate: lock the job cache's lease: %w", err)
			}
			return nil, false, nil
		}
		held, herr := f.Stat()
		cur, cerr := os.Stat(path)
		if herr == nil && cerr == nil && os.SameFile(held, cur) {
			if created {
				// A new lease must not make the cache look used now: it
				// starts at the cache's last use from before leases (a job
				// stamps it at once anyway).
				if t := fetchLastUsed(dir); !t.IsZero() {
					_ = os.Chtimes(path, t, t)
				}
			}
			return f, true, nil
		}
		releaseLease(f)
	}
	return nil, false, nil
}

func releaseLease(f *os.File) {
	unflock(f)
	_ = f.Close()
}

// stampLease records a use of the cache at dir at t.
func stampLease(dir string, t time.Time) {
	_ = os.Chtimes(leasePath(dir), t, t)
}

// cacheLastUsed is when a job last used the cache at dir: its lease's mtime,
// or, for a cache no leased job has used yet, the newest mtime of what a
// fetch writes.
func cacheLastUsed(dir string) time.Time {
	if info, err := os.Stat(leasePath(dir)); err == nil {
		return info.ModTime()
	}
	return fetchLastUsed(dir)
}

// fetchLastUsed is the newest mtime of what a fetch into the cache at dir
// writes; zero when there is no cache.
func fetchLastUsed(dir string) time.Time {
	var last time.Time
	for _, p := range []string{dir, filepath.Join(dir, "FETCH_HEAD"), filepath.Join(dir, "packed-refs"), filepath.Join(dir, "refs")} {
		if info, err := os.Stat(p); err == nil && info.ModTime().After(last) {
			last = info.ModTime()
		}
	}
	return last
}

// dirBytes is the apparent size of every regular file under dir.
func dirBytes(dir string) uint64 {
	var n uint64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			n += uint64(info.Size()) //nolint:gosec // G115: a file size is never negative
		}
		return nil
	})
	return n
}
