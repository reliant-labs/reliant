// Cache eviction for the machine-local source cache.
//
// # Why the cache needed a reclaimer at all
//
// Resolve writes one FULL clone per repo+ref (CacheKey), and the only
// removal anywhere in this package is the explicit Refresh. A project
// that bumps its pin every few days therefore accumulates one complete
// checkout per bump, forever. Measured on a developer machine:
// <UserCacheDir>/forge/sources held 16 clones of one repository totalling
// 9.2 GB, because each entry also carries whatever the frontend install
// wrote into it (web/node_modules was 0.54 GB of a 0.67 GB entry).
//
// Nothing about that is a correctness bug — every entry is a valid,
// re-fetchable materialization of a reviewable pin — which is exactly why
// it went unnoticed until the disk filled.
//
// # Recording last use: why the metadata file's MTIME
//
// Eviction needs to know when an entry was last USED, which is not when
// it was fetched. Three candidates, and the choice matters:
//
//   - Rewriting a timestamp INTO .forge-source.json. Rejected. That file
//     is the completion marker: its presence is what makes an entry a
//     cache hit (see Resolve). Rewriting it on every hit puts the one
//     file that must never be half-written on the hot path, and a crash
//     mid-write would downgrade a complete 700 MB entry to a miss and
//     force a full re-clone. The cost of being wrong is far higher than
//     the information is worth.
//   - The entry DIRECTORY's mtime. Rejected. A directory's mtime tracks
//     changes to its own direct children, not reads, so it records the
//     fetch and not the use. It is also perturbed by anything that writes
//     into the tree — an `npm install` in a subdir — which conflates
//     "something wrote here" with "forge resolved this pin".
//   - The metadata file's MTIME, touched with os.Chtimes. CHOSEN.
//
// Chtimes writes no content, so an interrupted touch cannot corrupt the
// completion marker; the worst case is a missed touch, which costs one
// re-fetch of a re-fetchable pin. The file already exists on every
// complete entry, so caches that predate this code need no backfill:
// their mtime is the fetch time, which is a correct lower bound for last
// use. And there is no second marker file to keep in sync with the first.
//
// # Why an entry in use cannot be evicted
//
// The recency guard is the first protection. Resolve touches the metadata
// on every cache HIT, so any process currently building from an entry
// touched it moments ago and cannot be in a set whose youngest member is
// MaxAge (default 14 days) old. The open-file scan covers the case the
// recency guard cannot — a process that resolved an entry and then held it
// (an open file, or its cwd) for longer than MaxAge, such as a dev server
// left running from a cached frontend source.
//
// When the scan is unavailable (no lsof, Windows) or does not finish, every
// candidate is retained. It used to proceed on recency alone, and it also
// accepted a killed lsof's partial listing as complete; both decided a
// deletion on evidence that could not see the process using the entry.
// Retaining costs disk until the next pass; deleting a live checkout costs
// the process using it.
//
// Nothing persists a resolved cache path into a project's .forge/ state;
// the resolved directory is rewritten into an in-memory FrontendConfig
// and never serialized (see internal/cli/frontend_source.go and
// BuildState, which records image/tag/digest and no source paths). So
// there is no stored reference an eviction could invalidate.
package gitsource

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/openfiles"
)

// DefaultEvictMaxAge is how long an entry may go unresolved before it
// becomes a candidate. Two weeks is long enough that a pin a developer
// returns to between sprints survives, and short enough that abandoned
// bumps do not accumulate for a quarter.
const DefaultEvictMaxAge = 14 * 24 * time.Hour

// DefaultEvictKeepPerSlug is how many of the most recently used entries
// for one repository are retained REGARDLESS of age.
//
// This is the floor that makes eviction safe for an idle project. A
// repository pinned to one ref and not built for a month has exactly one
// entry, and it is the entry the next build needs; evicting it on age
// alone would turn the first build after a holiday into a full re-clone.
// Two, not one, so that a developer flipping between a release pin and a
// main pin keeps both warm.
const DefaultEvictKeepPerSlug = 2

// EvictPolicy bounds what Evict is allowed to remove.
type EvictPolicy struct {
	// MaxAge is the unused age beyond which an entry is a candidate.
	// Zero means DefaultEvictMaxAge.
	MaxAge time.Duration

	// KeepPerSlug is the number of most-recently-used entries per
	// repository retained regardless of age. Zero means
	// DefaultEvictKeepPerSlug; it is never treated as "keep none",
	// because a policy that can empty the cache of a repository's only
	// pin is a policy nobody wants by accident.
	KeepPerSlug int

	// InUse reports whether a live process holds the entry directory as
	// its cwd or has a file open under it. nil installs the default
	// open-file scan (one lsof snapshot, taken once, only when there are
	// candidates, and retaining everything when it cannot be taken). Tests
	// inject a stub so they never shell out.
	InUse func(entry string) bool

	// Ctx bounds the eviction: the open-file snapshot, and the removals,
	// which stop at the deadline with everything not yet removed retained.
	// Nil means unbounded.
	Ctx context.Context

	// RemoveTree deletes one evicted entry's tree. Machine maintenance passes
	// its paced, capped deleter, which may stop partway through a tree; that
	// is safe because the entry is renamed out of the cache first (see
	// evictingPrefix) and a later Evict finishes the leftover. Nil means an
	// unpaced removal of the whole tree.
	RemoveTree func(path string) error
}

// evictingPrefix names an entry being evicted. The entry is renamed to it
// before its tree is deleted, so Resolve can never see a half-deleted clone
// under the cache key, and a removal stopped partway (a delete cap, a
// back-off, a crash) leaves a name every later Evict recognises and finishes.
const evictingPrefix = ".forge-evicting-"

func (p EvictPolicy) removeTree(path string) error {
	if p.RemoveTree != nil {
		return p.RemoveTree(path)
	}
	return removeAllWritable(path)
}

func (p EvictPolicy) ctx() context.Context {
	if p.Ctx != nil {
		return p.Ctx
	}
	return context.Background()
}

func (p EvictPolicy) maxAge() time.Duration {
	if p.MaxAge <= 0 {
		return DefaultEvictMaxAge
	}
	return p.MaxAge
}

func (p EvictPolicy) keepPerSlug() int {
	if p.KeepPerSlug <= 0 {
		return DefaultEvictKeepPerSlug
	}
	return p.KeepPerSlug
}

// Eviction reports what Evict did, or would do under apply=false. The
// counts are returned rather than only printed so a test can assert on
// the decision instead of parsing prose.
type Eviction struct {
	// Removed names the cache entries evicted (or that would be).
	Removed []string
	// Bytes is the total size of Removed, as measured before removal.
	Bytes int64
	// Kept is the number of entries retained for any reason.
	Kept int
	// Held names entries a live process was using, which are always
	// retained. Reported separately from Kept's reasons because it is
	// the one retention a caller may want to re-run for later.
	Held []string
}

// Evict removes source-cache entries under root that no project has
// resolved for longer than the policy's MaxAge, keeping the newest
// KeepPerSlug entries for every repository regardless of age.
//
// It is deliberately a package-level function over a root rather than a
// Resolver method: the caller that needs it is machine-wide maintenance
// (forge storage gc), which has no project and no override file, and
// making it a method would force that caller to construct a Resolver
// whose other fields are meaningless to it.
//
// apply=false previews. A missing root is not an error — a machine that
// has never fetched a source has nothing to reclaim.
//
// Evict FAILS CLOSED on anything it cannot read: an entry whose metadata
// is missing, unparseable, or unstattable is retained and reported, never
// removed. Debris from an interrupted fetch is in that set; Resolve
// clears it the next time that exact pin is resolved, which is a cheaper
// place to handle it than a reclaimer that would have to guess.
func Evict(root string, now time.Time, policy EvictPolicy, apply bool, out io.Writer) (Eviction, error) {
	var result Eviction

	dirEntries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("read source cache %s: %w", root, err)
	}

	type candidate struct {
		name    string
		slug    string
		lastUse time.Time
	}
	var usable []candidate
	var leftovers []string

	for _, de := range dirEntries {
		if !de.IsDir() {
			continue
		}
		if strings.HasPrefix(de.Name(), evictingPrefix) {
			leftovers = append(leftovers, de.Name())
			continue
		}
		entry := filepath.Join(root, de.Name())
		if _, ok := readMetadata(entry); !ok {
			result.Kept++
			printf(out, "keep source cache %s (no readable %s — not a complete entry)\n", entry, MetadataFile)
			continue
		}
		info, statErr := os.Stat(filepath.Join(entry, MetadataFile))
		if statErr != nil {
			result.Kept++
			printf(out, "keep source cache %s (cannot read last-use time: %v)\n", entry, statErr)
			continue
		}
		usable = append(usable, candidate{name: de.Name(), slug: entrySlug(de.Name()), lastUse: info.ModTime()})
	}

	bySlug := map[string][]candidate{}
	for _, c := range usable {
		bySlug[c.slug] = append(bySlug[c.slug], c)
	}

	maxAge, keep := policy.maxAge(), policy.keepPerSlug()
	var doomed []candidate
	for _, group := range bySlug {
		sort.Slice(group, func(i, j int) bool { return group[i].lastUse.After(group[j].lastUse) })
		for rank, c := range group {
			if rank < keep {
				result.Kept++
				continue
			}
			if now.Sub(c.lastUse) <= maxAge {
				result.Kept++
				continue
			}
			doomed = append(doomed, c)
		}
	}
	sort.Slice(doomed, func(i, j int) bool { return doomed[i].name < doomed[j].name })

	if err := finishInterruptedEvictions(root, leftovers, policy, apply, out); err != nil {
		return result, err
	}

	inUse := policy.InUse
	if inUse == nil && len(doomed) > 0 {
		snap, err := openfiles.Take(policy.ctx())
		if ctxErr := policy.ctx().Err(); ctxErr != nil {
			result.Kept += len(doomed)
			return result, fmt.Errorf("source cache eviction stopped before removing anything: %w", ctxErr)
		}
		if err != nil {
			// Fail closed: see the package comment.
			result.Kept += len(doomed)
			printf(out, "keep %d source cache entries: cannot determine which are in use (%v)\n", len(doomed), err)
			return result, nil
		}
		inUse = snap.Holds
	}

	for i, c := range doomed {
		if err := policy.ctx().Err(); err != nil {
			result.Kept += len(doomed) - i
			return result, fmt.Errorf("source cache eviction stopped after %d of %d entries: %w", len(result.Removed), len(doomed), err)
		}
		entry := filepath.Join(root, c.name)
		if inUse(entry) {
			result.Kept++
			result.Held = append(result.Held, c.name)
			printf(out, "keep source cache %s (in use by a running process)\n", entry)
			continue
		}
		size := treeSize(entry)
		printf(out, "evict source cache %s (%d bytes, unused %s)\n", entry, size, now.Sub(c.lastUse).Round(time.Hour))
		if apply {
			trash := filepath.Join(root, fmt.Sprintf("%s%s-%d", evictingPrefix, c.name, now.UnixNano()))
			if err := os.Rename(entry, trash); err != nil {
				return result, fmt.Errorf("evict source cache %s: %w", entry, err)
			}
			if err := policy.removeTree(trash); err != nil {
				result.Removed = append(result.Removed, c.name) // out of the cache; the leftover is finished next pass
				return result, fmt.Errorf("evict source cache %s: %w", entry, err)
			}
		}
		result.Removed = append(result.Removed, c.name)
		result.Bytes += size
	}
	return result, nil
}

// finishInterruptedEvictions deletes what earlier evictions renamed out of the
// cache and did not finish. Their decision was already made and their names
// are no cache key, so nothing can be using them by name.
func finishInterruptedEvictions(root string, leftovers []string, policy EvictPolicy, apply bool, out io.Writer) error {
	sort.Strings(leftovers)
	for _, name := range leftovers {
		if err := policy.ctx().Err(); err != nil {
			return fmt.Errorf("source cache eviction stopped before finishing interrupted evictions: %w", err)
		}
		trash := filepath.Join(root, name)
		printf(out, "finish interrupted eviction %s\n", trash)
		if apply {
			if err := policy.removeTree(trash); err != nil {
				return fmt.Errorf("finish interrupted eviction %s: %w", trash, err)
			}
		}
	}
	return nil
}

// entrySlug recovers the repository part of a CacheKey ("<slug>-<12 hex>").
// A directory that does not have that shape is its own group, so an
// unrecognized name can never be counted against another repository's
// keep floor.
func entrySlug(name string) string {
	i := strings.LastIndex(name, "-")
	if i <= 0 || len(name)-i-1 != 12 {
		return name
	}
	for _, r := range name[i+1:] {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return name
		}
	}
	return name[:i]
}

// touchUsed records that an entry was just resolved. Failure is reported
// to the resolver's log and never to the caller: a cache that cannot
// record its own last-use time still serves the build correctly, and the
// consequence of a missed touch is one re-fetch of a re-fetchable pin.
func touchUsed(entry string, now time.Time) error {
	path := filepath.Join(entry, MetadataFile)
	return os.Chtimes(path, now, now)
}

// treeSize sums the apparent size of every regular file under path.
// Unreadable subtrees contribute nothing rather than failing the sweep —
// this number is reporting, not a decision input.
func treeSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable subtree is reported as zero bytes, not a failure
		}
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// removeAllWritable deletes a tree that may contain read-only files.
// A source entry can hold an installed node_modules and a Go module
// cache, both of which write mode-0444 files and 0555 directories, and
// a plain RemoveAll fails on those with EACCES partway through — leaving
// a half-deleted entry that is worse than the one it replaced.
func removeAllWritable(path string) error {
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // best-effort widening; RemoveAll reports the real failure
		}
		mode := fs.FileMode(0o600)
		if d.IsDir() {
			mode = 0o700
		}
		if info, err := d.Info(); err == nil {
			mode |= info.Mode().Perm()
		}
		_ = os.Chmod(p, mode)
		return nil
	})
	return os.RemoveAll(path)
}

func printf(out io.Writer, format string, args ...any) {
	if out == nil {
		return
	}
	fmt.Fprintf(out, format, args...)
}
