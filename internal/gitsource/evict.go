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
	"slices"
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

	// SlimAfter is how long an entry beyond the newest KeepPerSlug of its
	// repository may sit unused before its installed dependencies are removed
	// (SlimDirs). Zero turns slimming off.
	//
	// Measured on the machine this was written for: of 17 GB across 37 clones
	// of one repository, 14 GB was an installed node_modules (650 MB in 20
	// clones) and 1.7 GB was git objects (46 MB per clone). A shared object
	// store would save a tenth; the dependency trees are the bytes, and they
	// are rebuildable by definition: every consumer of a cached source runs
	// the package manager when node_modules is missing (frontenddeps.Ensure),
	// so a slimmed clone is a cache hit that costs one install, not a re-fetch.
	SlimAfter time.Duration

	// BudgetBytes bounds the cache's size. Past it, entries beyond the newest
	// KeepPerSlug are evicted least recently used first, down to
	// budgetLowWater of it, whatever their age. Zero means no size bound.
	//
	// The age rule alone cannot bound the cache: a project that bumps its pin
	// every few hours produces more clones inside MaxAge than any disk holds.
	BudgetBytes int64

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
	// Slimmed names the entries whose installed dependencies were removed
	// (or would be), and SlimBytes is what that frees. The entry itself stays
	// a cache hit.
	Slimmed   []string
	SlimBytes int64
	// OverBudget is set when the cache is still larger than BudgetBytes after
	// everything evictable was: the newest KeepPerSlug clones of each
	// repository, and entries a live process holds, are never removed.
	OverBudget bool
	// TotalBytes is the cache's size before this pass, measured only when a
	// budget is set.
	TotalBytes int64
}

// budgetLowWater is the share of the budget an over-budget pass evicts down
// to, so the next pass does not evict one clone each time.
const budgetLowWater = 0.8

// SlimDirs are the directories an entry's slimming removes: installed
// dependencies a package manager recreates from the lockfile in the clone.
var SlimDirs = []string{"node_modules"}

// slimDepth is how far below an entry's root slimming looks for SlimDirs
// (frontends/web/node_modules is 2 below it).
const slimDepth = 4

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
	var all []ranked
	for _, group := range bySlug {
		sort.Slice(group, func(i, j int) bool { return group[i].lastUse.After(group[j].lastUse) })
		for rank, c := range group {
			all = append(all, ranked{c, rank})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].name < all[j].name })

	// Sizes are measured once per entry, and only when a decision needs one.
	sizes := map[string]int64{}
	sizeOf := func(name string) int64 {
		if n, ok := sizes[name]; ok {
			return n
		}
		n := treeSize(filepath.Join(root, name))
		sizes[name] = n
		return n
	}

	var doomed []candidate
	isDoomed := map[string]bool{}
	for _, c := range all {
		switch {
		case c.rank < keep:
			result.Kept++
		case now.Sub(c.lastUse) <= maxAge:
			result.Kept++
		default:
			doomed = append(doomed, c.candidate)
			isDoomed[c.name] = true
		}
	}

	// Slimming comes before the size budget: it frees the bytes that cost
	// least to get back, so it can make eviction unnecessary. An entry's
	// installed dependencies are found now and removed after the in-use check.
	slims, slimmed, err := planSlims(root, now, policy, all, keep, isDoomed)
	if err != nil {
		return result, err
	}

	// The size budget, judged on what the cache will weigh once the entries
	// above are slimmed and evicted. A pass that cannot measure everything
	// (its deadline ran out) decides nothing on size: it retains.
	if policy.BudgetBytes > 0 {
		var budgeted []candidate
		budgeted, result = evictForBudget(policy, all, keep, isDoomed, slimmed, result, sizeOf)
		doomed = append(doomed, budgeted...)
	}
	sort.Slice(doomed, func(i, j int) bool { return doomed[i].name < doomed[j].name })

	if err := finishInterruptedEvictions(root, leftovers, policy, apply, out); err != nil {
		return result, err
	}

	inUse := policy.InUse
	if inUse == nil && len(doomed)+len(slims) > 0 {
		snap, err := openfiles.Take(policy.ctx())
		if ctxErr := policy.ctx().Err(); ctxErr != nil {
			result.Kept += len(doomed)
			return result, fmt.Errorf("source cache eviction stopped before removing anything: %w", ctxErr)
		}
		if err != nil {
			// Fail closed: see the package comment.
			result.Kept += len(doomed)
			printf(out, "keep %d source cache entries: cannot determine which are in use (%v)\n", len(doomed)+len(slims), err)
			return result, nil
		}
		inUse = snap.Holds
	}

	ep := evictPass{root: root, now: now, policy: policy, apply: apply, out: out, inUse: inUse, result: &result}
	if err := ep.slim(slims, isDoomed); err != nil {
		return result, err
	}
	err = ep.evict(doomed, sizeOf)
	return result, err
}

// evictPass is what removing entries shares: where, when, whether to really
// delete, how to tell an entry is held, and the result being filled in.
type evictPass struct {
	root   string
	now    time.Time
	policy EvictPolicy
	apply  bool
	out    io.Writer
	inUse  func(string) bool
	result *Eviction
}

// ranked is a cache entry with its place in its repository's recency order.
type ranked struct {
	candidate
	rank int
}

type candidate struct {
	name    string
	slug    string
	lastUse time.Time
}

// slim is an entry whose installed dependencies are to be removed.
type slim struct {
	name  string
	dirs  []string
	bytes int64
}

// planSlims finds the entries beyond the keep floor, idle past SlimAfter, that
// hold installed dependencies, and what each would free.
func planSlims(root string, now time.Time, policy EvictPolicy, all []ranked, keep int, isDoomed map[string]bool) ([]slim, map[string]int64, error) {
	var slims []slim
	slimmed := map[string]int64{}
	if policy.SlimAfter <= 0 {
		return nil, slimmed, nil
	}
	for _, c := range all {
		if c.rank < keep || isDoomed[c.name] || now.Sub(c.lastUse) <= policy.SlimAfter {
			continue
		}
		if err := policy.ctx().Err(); err != nil {
			return nil, nil, fmt.Errorf("source cache eviction stopped while looking for installed dependencies: %w", err)
		}
		dirs := slimDirsIn(filepath.Join(root, c.name))
		if len(dirs) == 0 {
			continue
		}
		var bytes int64
		for _, d := range dirs {
			bytes += treeSize(d)
		}
		slims = append(slims, slim{name: c.name, dirs: dirs, bytes: bytes})
		slimmed[c.name] = bytes
	}
	return slims, slimmed, nil
}

// slim removes the installed dependencies of the planned entries that no
// process holds. Each tree is renamed out of the entry before it is deleted, so
// a stopped removal leaves a name every later pass finishes.
func (ep evictPass) slim(slims []slim, isDoomed map[string]bool) error {
	root, now, policy, apply, out, inUse, result := ep.root, ep.now, ep.policy, ep.apply, ep.out, ep.inUse, ep.result
	for _, sl := range slims {
		if isDoomed[sl.name] {
			continue // evicted whole
		}
		if err := policy.ctx().Err(); err != nil {
			return fmt.Errorf("source cache slimming stopped after %d of %d entries: %w", len(result.Slimmed), len(slims), err)
		}
		entry := filepath.Join(root, sl.name)
		if inUse(entry) {
			result.Held = append(result.Held, sl.name)
			printf(out, "keep source cache %s (in use by a running process)\n", entry)
			continue
		}
		printf(out, "slim source cache %s (%d bytes of installed dependencies, unused beyond %s)\n", entry, sl.bytes, policy.SlimAfter)
		if apply {
			for i, dir := range sl.dirs {
				trash := filepath.Join(root, fmt.Sprintf("%s%s-dep%d-%d", evictingPrefix, sl.name, i, now.UnixNano()))
				_ = os.Chmod(dir, 0o755) // a dependency tree can be read-only; moving a directory needs write on it
				if err := os.Rename(dir, trash); err != nil {
					return fmt.Errorf("slim source cache %s: %w", entry, err)
				}
				if err := policy.removeTree(trash); err != nil {
					result.Slimmed = append(result.Slimmed, sl.name)
					result.SlimBytes += sl.bytes
					return fmt.Errorf("slim source cache %s: %w", entry, err)
				}
			}
		}
		result.Slimmed = append(result.Slimmed, sl.name)
		result.SlimBytes += sl.bytes
	}
	return nil
}

// evict removes the doomed entries that no process holds.
func (ep evictPass) evict(doomed []candidate, sizeOf func(string) int64) error {
	root, now, policy, apply, out, inUse, result := ep.root, ep.now, ep.policy, ep.apply, ep.out, ep.inUse, ep.result
	for i, c := range doomed {
		if err := policy.ctx().Err(); err != nil {
			result.Kept += len(doomed) - i
			return fmt.Errorf("source cache eviction stopped after %d of %d entries: %w", len(result.Removed), len(doomed), err)
		}
		entry := filepath.Join(root, c.name)
		if inUse(entry) {
			result.Kept++
			result.Held = append(result.Held, c.name)
			printf(out, "keep source cache %s (in use by a running process)\n", entry)
			continue
		}
		size := sizeOf(c.name)
		printf(out, "evict source cache %s (%d bytes, unused %s)\n", entry, size, now.Sub(c.lastUse).Round(time.Hour))
		if apply {
			trash := filepath.Join(root, fmt.Sprintf("%s%s-%d", evictingPrefix, c.name, now.UnixNano()))
			if err := os.Rename(entry, trash); err != nil {
				return fmt.Errorf("evict source cache %s: %w", entry, err)
			}
			if err := policy.removeTree(trash); err != nil {
				result.Removed = append(result.Removed, c.name) // out of the cache; the leftover is finished next pass
				return fmt.Errorf("evict source cache %s: %w", entry, err)
			}
		}
		result.Removed = append(result.Removed, c.name)
		result.Bytes += size
	}
	return nil
}

// evictForBudget picks the entries to evict, least recently used first and never
// within the keep floor, to bring a cache over policy.BudgetBytes down to
// budgetLowWater of it. isDoomed is updated; the entries chosen are returned.
func evictForBudget(policy EvictPolicy, all []ranked, keep int, isDoomed map[string]bool, slimmed map[string]int64, result Eviction, sizeOf func(string) int64) ([]candidate, Eviction) {
	var total int64
	for _, c := range all {
		if policy.ctx().Err() != nil {
			return nil, result // not everything was measured: decide nothing on size
		}
		total += sizeOf(c.name)
	}
	result.TotalBytes = total
	after := total
	for _, c := range all {
		if isDoomed[c.name] {
			after -= sizeOf(c.name)
		} else {
			after -= slimmed[c.name]
		}
	}
	var chosen []candidate
	if after > policy.BudgetBytes {
		target := int64(float64(policy.BudgetBytes) * budgetLowWater)
		var lru []ranked
		for _, c := range all {
			if c.rank >= keep && !isDoomed[c.name] {
				lru = append(lru, c)
			}
		}
		sort.Slice(lru, func(i, j int) bool { return lru[i].lastUse.Before(lru[j].lastUse) })
		for _, c := range lru {
			if after <= target {
				break
			}
			after -= sizeOf(c.name) - slimmed[c.name]
			chosen = append(chosen, c.candidate)
			isDoomed[c.name] = true
			result.Kept-- // the age rule had spared it and counted it kept
		}
	}
	result.OverBudget = after > policy.BudgetBytes
	return chosen, result
}

// slimDirsIn lists the SlimDirs inside an entry, to slimDepth levels, without
// descending into .git or into a directory it is about to list.
func slimDirsIn(entry string) []string {
	var found []string
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		des, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, de := range des {
			if !de.IsDir() || de.Name() == ".git" {
				continue
			}
			full := filepath.Join(dir, de.Name())
			if slices.Contains(SlimDirs, de.Name()) {
				found = append(found, full)
				continue
			}
			if depth < slimDepth {
				walk(full, depth+1)
			}
		}
	}
	walk(entry, 1)
	return found
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
