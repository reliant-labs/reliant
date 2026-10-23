package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Go-toolchain cache layer: the shared build cache, the golangci-lint cache,
// the goimports module index, and orphaned private caches (gocache_orphans.go).
//
// Why age-based trim and not `go clean -cache`: without -trimpath Go keys every
// main-module package by its absolute directory, so each worktree gets its own
// full copy of the cache. Go's built-in trim (entries unused for 5 days) cannot
// keep up. Wiping the cache under running builds produces
// `link: cannot open file …/go-build/…-d`. Trimming by mtime is safe while
// builds run because Go bumps an entry's mtime on use whenever it is more than
// an hour stale: an entry older than X has been unused for at least X-1h, so
// with a floor of 2h a live build never loses an entry it is using.
//
// The floor bounds what the age rule may delete, and by itself it bounds the
// size budget too: everything written in the last goCacheFloor stays. On a
// machine filling faster than its budget per floor (2026-10-22: ~60 GB/h
// against a 20 GiB budget, the cache at 74 GB with every entry under 4h old)
// that never converges. So a cache still over budget after the safe pass is
// trimmed further, least recently used first, down to a low-water mark
// (goCacheLowWater of the budget) and no further than the policy's
// go_cache_min_age (default 20m).
//
// Why that is acceptable, from cmd/go/internal/cache:
//   - A missing entry is a MISS, not an error. Get reads the action file and
//     GetFile stats the output; either absent (or the wrong size) is
//     entryNotFoundError and the action is simply run again.
//   - The one hazard is an entry a RUNNING build has already resolved and
//     not yet consumed (an archive waiting for the link, an executable
//     between GetFile and exec): deleting it fails that build with "could not
//     import … no such file" or "cannot open file", and the build passes on
//     retry. Go restamps an entry's mtime on use only when it is more than an
//     hour stale, so an mtime younger than 1h says nothing about use: only an
//     entry with mtime older than 1h+D is certainly idle for D. The 2h floor
//     is D=1h and is why the age rule is safe. The size rule trades that
//     guarantee for the budget, oldest mtime first, so the entries it takes
//     are the least likely to be in use; the risk it accepts is one retryable
//     build, against a full disk that refuses every heavy command on the
//     machine. Measured: 1 of 25 builds failed while a loop deleted 150 random
//     entries (any age) every 50ms under them; a trim takes the oldest first.
//   - A write in flight is not at risk: copyFile creates the output in place
//     and commits its last byte at the end, so its mtime is now, and the
//     minimum age (>= 5m) leaves it alone.
//   - trim.txt and README are top-level files, which the trim never touches;
//     cmd/go's own trim (entries unused 5 days) is independent of this one.

// goCacheFloor is the youngest entry the trim may ever delete, whatever the
// policy says. It is what makes the trim safe under live builds.
const goCacheFloor = 2 * time.Hour

// goCacheWorkers bounds how many shards are read and trimmed at once. The
// unlinks themselves are paced by the pass's deletion governor (deletes.go),
// so more workers only means more directory reads in flight — each a burst of
// lookups that a network or virtiofs filesystem has to serve.
const goCacheWorkers = 4

var goCacheEntryName = regexp.MustCompile(`^[0-9a-f]{64}-[ad]$`)

// goBuildCacheSentinelPrefix is the first line shared by Go's cache README and
// golangci-lint's fork of it.
const goBuildCacheSentinelPrefix = "This directory holds cached build artifacts from"

type goCacheEntry struct {
	sub   string
	name  string
	size  int64
	mtime time.Time
	// dir marks an executable entry: cmd/go caches the output of `go run` and
	// `go tool` as a directory holding the one binary. Those are the largest
	// entries in the cache (270 MB for a CLI) and the size accounting and the
	// trim must see them like any other.
	dir bool
}

func (e goCacheEntry) path(root string) string { return filepath.Join(root, e.sub, e.name) }

// remove deletes the entry through the pass governor.
func (e goCacheEntry) remove(ctx context.Context, del deleter, root string) error {
	if e.dir {
		return del.removeTree(ctx, e.path(root))
	}
	return del.unlink(ctx, e.path(root))
}

// GoCaches trims every Go-toolchain cache. Dry-run unless apply is set.
// Layers run independently; one failure does not stop the rest.
//
// The shared build cache is the layer that matters most and it grows ~40 GB/h
// under agent load, so it runs FIRST and each step gets a guaranteed slice of
// what remains: a slow step cannot starve the next. A step cut off by its
// slice has still reclaimed what it covered (see trimGoBuildCache); that is a
// partial success, never a failure. Only real errors are returned.
//
// The delete budget is shared the same way: each step may spend a share of
// what the steps before it left (deletes.go), so the shared cache cannot spend
// the whole pass on itself. A step that reaches its share stops and is
// reported as cut off; a back-off ends every step after it.
func (r Runner) GoCaches(apply bool) error {
	parent := r.hostCtx()
	budget := r.deletes()
	var failures []error
	steps := []struct {
		name    string
		frac    float64 // share of the time still left in the slice
		deletes float64 // share of the delete budget still left
		run     func(Runner, bool) error
	}{
		{"shared build cache", 0.6, 0.75, Runner.SharedGoCache},
		{"golangci-lint cache", 0.5, 0.5, Runner.GolangciLintCache},
		{"orphaned private caches", 0.5, 0.5, Runner.OrphanedGoCaches},
		{"goimports index", 1, 1, Runner.GoimportsIndex},
	}
	for _, step := range steps {
		// A step the slice left no time for is recorded as cut off, never
		// dropped: a record that omits it reads as a step that finished.
		if err := parent.Err(); err != nil {
			failures = append(failures, layerErr("go caches: "+step.name, &CutOffError{Err: err}))
			continue
		}
		if err := budget.backedOff(); err != nil {
			failures = append(failures, layerErr("go caches: "+step.name, err))
			continue
		}
		ctx, cancel := sliceContext(parent, step.frac)
		sub := r
		sub.Ctx = ctx
		sub.governor = budget.share(step.deletes)
		err := step.run(sub, apply)
		cancel()
		// A step that ended on its deadline did what it reached and resumes
		// next pass: a cut-off, which the record keeps apart from a failure.
		// It used to be dropped here, so a trim stopped by its time slice
		// under load recorded a clean pass while past-floor entries piled up.
		if err != nil && onlyContextErrors(err) {
			err = &CutOffError{Err: err}
		}
		if err != nil {
			failures = append(failures, layerErr("go caches: "+step.name, err))
		}
	}
	return errors.Join(failures...)
}

// sliceContext bounds a step to frac of the time its parent has left. A parent
// without a deadline is passed through.
func sliceContext(parent context.Context, frac float64) (context.Context, context.CancelFunc) {
	deadline, ok := parent.Deadline()
	if !ok || frac >= 1 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, time.Duration(float64(time.Until(deadline))*frac))
}

func refuseUnderTest(field string) error {
	return fmt.Errorf("storage.Runner.%s is unset under test: set it to a t.TempDir() "+
		"(an unset root would trim the developer's real Go cache)", field)
}

// goEnv resolves the shared GOCACHE and GOMODCACHE.
func (r Runner) goEnv(ctx context.Context) (gocache, gomodcache string, err error) {
	gocache, gomodcache = r.GoCacheRoot, r.GoModCacheRoot
	if gocache != "" && gomodcache != "" {
		return gocache, gomodcache, nil
	}
	if testing.Testing() {
		if gocache == "" {
			return "", "", refuseUnderTest("GoCacheRoot")
		}
		return gocache, "", nil
	}
	out, err := r.command(ctx, "go", "env", "GOCACHE", "GOMODCACHE")
	if err != nil {
		return "", "", fmt.Errorf("go env: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 || strings.TrimSpace(lines[0]) == "" || strings.TrimSpace(lines[1]) == "" {
		return "", "", fmt.Errorf("unexpected `go env` output %q", out)
	}
	if gocache == "" {
		gocache = strings.TrimSpace(lines[0])
	}
	if gomodcache == "" {
		gomodcache = strings.TrimSpace(lines[1])
	}
	return gocache, gomodcache, nil
}

// SharedGoCache trims the shared `go env GOCACHE`.
func (r Runner) SharedGoCache(apply bool) error {
	root := r.GoCacheRoot
	if root == "" {
		var err error
		if root, _, err = r.goEnv(r.hostCtx()); err != nil {
			r.print("skip shared go build cache (%v)\n", err)
			// No Go toolchain at all: there is no shared cache to trim. Any
			// other failure to locate it is a failure, never a clean pass —
			// it is the one cache that fills the disk.
			if testing.Testing() || errors.Is(err, exec.ErrNotFound) {
				return nil
			}
			return fmt.Errorf("locate the shared Go build cache: %w", err)
		}
	}
	return r.trimGoBuildCache("shared go build cache", root, apply)
}

// GolangciLintCache trims golangci-lint's fork of the Go cache format.
func (r Runner) GolangciLintCache(apply bool) error {
	root := r.GolangciCacheRoot
	if root == "" {
		if testing.Testing() {
			r.print("skip golangci-lint cache (%v)\n", refuseUnderTest("GolangciCacheRoot"))
			return nil
		}
		if env := os.Getenv("GOLANGCI_LINT_CACHE"); env != "" {
			root = env
		} else if dir, err := os.UserCacheDir(); err == nil {
			root = filepath.Join(dir, "golangci-lint")
		} else {
			return nil
		}
	}
	return r.trimGoBuildCache("golangci-lint cache", root, apply)
}

// hasCacheSentinel reports whether root's README is a Go-style cache marker.
func hasCacheSentinel(root string) bool {
	f, err := os.Open(filepath.Join(root, "README"))
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, len(goBuildCacheSentinelPrefix))
	n, _ := f.Read(buf)
	return string(buf[:n]) == goBuildCacheSentinelPrefix
}

const (
	goCacheShards     = 256
	goCacheSampleSize = 16
)

// goCachePass is what one trim pass did. It is reported, never an error: a pass
// cut off by its deadline that freed space is a partial success.
type goCachePass struct {
	shardsTotal, shardsCovered int
	removed                    int
	freed                      uint64
	finished                   bool
	samples                    []string
	// stop is why the pass ended early, when it was not its deadline: the
	// delete cap (a CutOffError) or a back-off (a BackedOffError).
	stop error
}

type goCacheSample struct {
	bytes   uint64
	shards  int
	entries []goCacheEntry // sampled entries older than the minimum age, oldest first
	err     error          // filesystem distress that stopped the sample
}

// goCacheShardList returns the cache's shard directories, sorted.
func goCacheShardList(root string) ([]string, error) {
	subs, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var shards []string
	for _, sub := range subs {
		if sub.IsDir() && len(sub.Name()) == 2 { // README, trim.txt and the rest are never touched
			shards = append(shards, sub.Name())
		}
	}
	sort.Strings(shards)
	return shards, nil
}

// readGoCacheShard lists one shard's entries. Only stats; never opens a file.
// An unreadable shard or entry is skipped, except that filesystem distress is
// returned: the caller must stop, not carry on to the next shard.
func readGoCacheShard(root, shard string) ([]goCacheEntry, error) {
	dir := filepath.Join(root, shard)
	files, err := os.ReadDir(dir)
	if err != nil {
		if fsDistress(err) {
			return nil, err
		}
		return nil, nil
	}
	out := make([]goCacheEntry, 0, len(files))
	for _, f := range files {
		if !goCacheEntryName.MatchString(f.Name()) {
			continue
		}
		info, err := f.Info()
		if err != nil {
			if fsDistress(err) {
				return out, err
			}
			continue
		}
		switch {
		case info.Mode().IsRegular():
			out = append(out, goCacheEntry{sub: shard, name: f.Name(), size: info.Size(), mtime: info.ModTime()})
		case info.IsDir() && strings.HasSuffix(f.Name(), "-d"):
			size, err := goCacheDirSize(filepath.Join(dir, f.Name()))
			if err != nil {
				if fsDistress(err) {
					return out, err
				}
				continue // removed since the listing
			}
			out = append(out, goCacheEntry{sub: shard, name: f.Name(), size: size, mtime: info.ModTime(), dir: true})
		}
	}
	return out, nil
}

// goCacheDirSize is the bytes of the regular files directly inside an
// executable entry.
func goCacheDirSize(path string) (int64, error) {
	files, err := os.ReadDir(path)
	if err != nil {
		return 0, err
	}
	var n int64
	for _, f := range files {
		if info, err := f.Info(); err == nil && info.Mode().IsRegular() {
			n += info.Size()
		}
	}
	return n, nil
}

// sampleGoCache reads a bounded, evenly spaced subset of shards (starting at
// offset so successive passes sample different ones). Entry hashes are uniform,
// so a shard is a fair 1/256 slice of both size and age distribution.
func sampleGoCache(ctx context.Context, root string, shards []string, offset int, now time.Time, minAge time.Duration) goCacheSample {
	n := goCacheSampleSize
	if n > len(shards) {
		n = len(shards)
	}
	var out goCacheSample
	floor := now.Add(-minAge)
	for i := 0; i < n && ctx.Err() == nil; i++ {
		shard := shards[(offset+i*len(shards)/n)%len(shards)]
		entries, err := readGoCacheShard(root, shard)
		if err != nil {
			out.err = err
			break
		}
		out.shards++
		for _, e := range entries {
			out.bytes += uint64(e.size)
			if e.mtime.Before(floor) {
				out.entries = append(out.entries, e)
			}
		}
	}
	sort.Slice(out.entries, func(i, j int) bool { return out.entries[i].mtime.Before(out.entries[j].mtime) })
	return out
}

// goCacheLowWater is the share of the budget an over-budget pass trims to. The
// slack is what keeps the next pass from trimming a handful of entries every
// time, and absorbs the sample's error in the size estimate.
const goCacheLowWater = 0.8

// goCacheCutoff picks the mtime below which entries are deleted: the older of
// "unused longer than the age rule" and "what must go to reach the low-water
// mark", whichever deletes more. The age rule never reaches into the safety
// floor; the size rule may, but never past minAge (itself at most the floor).
// No global sort and no complete scan: the budget cutoff is read off the
// sample's mtime distribution scaled to the whole cache, so it takes the
// oldest entries first and reaches into the window only as far as it must.
func goCacheCutoff(sample goCacheSample, nShards int, now time.Time, unused time.Duration, budget uint64, minAge time.Duration) (cutoff time.Time, estTotal uint64) {
	floorCut := now.Add(-goCacheFloor)
	minCut := now.Add(-min(minAge, goCacheFloor))
	cutoff = now.Add(-unused)
	if cutoff.After(floorCut) {
		cutoff = floorCut
	}
	if sample.shards == 0 {
		return cutoff, 0
	}
	scale := float64(nShards) / float64(sample.shards)
	estTotal = uint64(float64(sample.bytes) * scale)
	if estTotal <= budget {
		return cutoff, estTotal
	}
	need := float64(estTotal-uint64(float64(budget)*goCacheLowWater)) / scale // bytes to drop from the sample
	var acc float64
	budgetCut := minCut // the sample cannot cover the excess: take everything past the minimum age
	for _, e := range sample.entries {
		acc += float64(e.size)
		if acc >= need {
			budgetCut = e.mtime.Add(time.Second) // include this entry itself
			break
		}
	}
	if budgetCut.After(minCut) {
		budgetCut = minCut
	}
	if budgetCut.After(cutoff) {
		cutoff = budgetCut
	}
	return cutoff, estTotal
}

// goCacheNeed estimates how many entries are last used before cutoff across
// the whole cache: the sample's count scaled to every shard. The sample's
// entries are those past the minimum age, which every cutoff is.
func goCacheNeed(sample goCacheSample, nShards int, cutoff time.Time) int {
	if sample.shards == 0 {
		return 0
	}
	n := 0
	for _, e := range sample.entries {
		if e.mtime.Before(cutoff) {
			n++
		}
	}
	return int(float64(n) * float64(nShards) / float64(sample.shards))
}

// defaultGoCacheVolumePercent is GoCacheVolumePercent's default.
const defaultGoCacheVolumePercent = 15

// goCacheDiskSpace is DiskSpace; tests replace it.
var goCacheDiskSpace = DiskSpace

// goCacheBudget is the size the cache at root is trimmed to: go_cache_gib,
// capped at go_cache_volume_percent of the volume it lives on. A volume that
// cannot be read leaves go_cache_gib alone.
func (r Runner) goCacheBudget(root string) uint64 {
	budget := r.Policy.GoCacheGiB * GiB
	pct := r.Policy.GoCacheVolumePercent
	if pct == 0 {
		pct = defaultGoCacheVolumePercent
	}
	if pct >= 100 {
		return budget
	}
	if disk, err := goCacheDiskSpace(root); err == nil && disk.Capacity > 0 {
		if v := disk.Capacity / 100 * uint64(pct); v < budget {
			budget = v
		}
	}
	return budget
}

// cursorFile persists, per cache, the shard the next pass starts at.
func (r Runner) goCacheCursorPath() string {
	if r.PolicyPath == "" || guardMachinePolicy(r.PolicyPath) != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(r.PolicyPath), "go-cache-cursor.json")
}

func (r Runner) loadGoCacheCursor(label string) int {
	path := r.goCacheCursorPath()
	if path == "" {
		return int(time.Now().Unix()/3600) * 37 // no persistence: rotate by the hour
	}
	var m map[string]int
	if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &m) == nil {
		return m[label]
	}
	return 0
}

func (r Runner) saveGoCacheCursor(label string, next int) {
	path := r.goCacheCursorPath()
	if path == "" {
		return
	}
	m := map[string]int{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	m[label] = next
	b, _ := json.Marshal(m)
	tmp := path + ".tmp"
	if atomicfile.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

func (r Runner) trimGoBuildCache(label, root string, apply bool) error {
	if _, err := os.Stat(root); err != nil {
		return nil // absent: nothing to trim
	}
	if !hasCacheSentinel(root) {
		r.print("skip %s %s: no cache README, not recognised as a Go cache\n", label, root)
		return nil
	}
	unused, err := time.ParseDuration(r.Policy.GoCacheUnused)
	if err != nil {
		return fmt.Errorf("go_cache_unused: %w", err)
	}
	minAge, err := r.Policy.goCacheMinAge()
	if err != nil {
		return err
	}
	shards, err := goCacheShardList(root)
	if err != nil {
		return err
	}
	if len(shards) == 0 {
		return nil
	}
	ctx := r.hostCtx()
	del := r.deletes()
	now := time.Now()
	start := r.loadGoCacheCursor(label) % len(shards)
	sample := sampleGoCache(ctx, root, shards, start, now, minAge)
	if sample.err != nil {
		err := del.observe(root, sample.err)
		r.print("%s %s: %v\n", label, root, err)
		return err
	}
	budget := r.goCacheBudget(root)
	cutoff, estTotal := goCacheCutoff(sample, len(shards), now, unused, budget, minAge)
	overBudget := ""
	if estTotal > budget {
		// Over its size budget: the per-pass cap must not stop the trim short
		// of the budget, or a cache that grows faster than one capped pass
		// can trim never converges. Allow what the sample says it takes, with
		// a margin; pacing, back-off and the time slice still apply.
		need := goCacheNeed(sample, len(shards), cutoff)
		del = del.allowOver(need + need/10 + deleteBatch)
		overBudget = fmt.Sprintf("; over budget, per-pass cap lifted for ~%d entries", need)
	}

	pass := streamTrimGoCache(ctx, root, shards, start, cutoff, apply, del)
	if apply {
		r.saveGoCacheCursor(label, (start+pass.shardsCovered)%len(shards))
	}

	r.print("%s %s: ~%.1f GiB estimated (budget %.1f GiB, trimmed to %.1f GiB when over); deleting entries last used before %s (age rule %s, floor %s, never newer than %s when over budget)%s\n",
		label, root, float64(estTotal)/float64(GiB), float64(budget)/float64(GiB), float64(budget)*goCacheLowWater/float64(GiB), cutoff.Format(time.RFC3339), unused, goCacheFloor, minAge, overBudget)
	for _, p := range pass.samples {
		r.print("  %s %s\n", verb(apply), p)
	}
	status := "finished"
	if !pass.finished {
		why := "cut off"
		var backoff *BackedOffError
		switch {
		case errors.As(pass.stop, &backoff):
			why = "BACKED OFF on filesystem distress"
		case errors.Is(pass.stop, errDeleteCap):
			why = "stopped at this pass's delete cap"
		}
		status = fmt.Sprintf("%s after %d/%d shards; next pass resumes at shard %02x", why, pass.shardsCovered, pass.shardsTotal, shards[(start+pass.shardsCovered)%len(shards)])
	}
	r.print("%s: %s %d entries, %.1f GiB (%s)\n", label, map[bool]string{true: "removed", false: "would remove"}[apply], pass.removed, float64(pass.freed)/float64(GiB), status)
	young := goCacheYoungBytes(sample, len(shards))
	if r.OnGoCacheTrim != nil {
		r.OnGoCacheTrim(GoCacheTrim{
			Cache: label, Root: root, Applied: apply,
			EstimatedBytes: estTotal, BudgetBytes: budget, YoungBytes: young,
			Cutoff: cutoff, Removed: pass.removed, FreedBytes: pass.freed,
			ShardsCovered: pass.shardsCovered, ShardsTotal: pass.shardsTotal, Finished: pass.finished,
		})
	}
	// A walk the deadline stopped is a cut-off. streamTrimGoCache records
	// only the stops it causes itself (cap, back-off), so without this a trim
	// that ran out of time returned nil and its pass recorded a clean finish.
	if !pass.finished && pass.stop == nil {
		if err := ctx.Err(); err != nil {
			pass.stop = &CutOffError{Err: fmt.Errorf("stopped after %d/%d shards: %w", pass.shardsCovered, pass.shardsTotal, err)}
		}
	}
	if young > budget {
		unreachable := &BudgetUnreachableError{Cache: label, Estimated: estTotal, Budget: budget, Young: young, Floor: minAge}
		r.print("WARNING %v\n", unreachable)
		return errors.Join(pass.stop, unreachable)
	}
	return pass.stop
}

// goCacheYoungBytes estimates the bytes of the whole cache last used inside the
// minimum age — the part no pass may delete. The sample's entries are those
// past the minimum age, so the rest of its bytes are the young ones.
func goCacheYoungBytes(sample goCacheSample, nShards int) uint64 {
	if sample.shards == 0 {
		return 0
	}
	var old uint64
	for _, e := range sample.entries {
		old += uint64(e.size)
	}
	if old >= sample.bytes {
		return 0
	}
	return uint64(float64(sample.bytes-old) * float64(nShards) / float64(sample.shards))
}

// GoCacheTrim is what one trim of one Go-format build cache measured and did.
// It is recorded in the pass's GCResult (GoCaches), so whoever schedules the
// pass can see the cache's size, the part no trim may touch, and whether the
// walk covered every shard, without parsing human output.
type GoCacheTrim struct {
	// Cache names the cache ("shared go build cache"); Root is its directory.
	Cache string `json:"cache"`
	Root  string `json:"root"`
	// Applied is false for a dry run, which removes nothing.
	Applied bool `json:"applied"`
	// EstimatedBytes is the cache's estimated size before the trim,
	// BudgetBytes its size budget, and YoungBytes the estimated bytes last
	// used inside the safety floor, which no trim deletes.
	EstimatedBytes uint64 `json:"estimated_bytes"`
	BudgetBytes    uint64 `json:"budget_bytes"`
	YoungBytes     uint64 `json:"young_bytes"`
	// Cutoff is the last-use time below which entries were removed.
	Cutoff time.Time `json:"cutoff"`
	// Removed and FreedBytes are what the trim removed (or, dry, would have).
	Removed    int    `json:"removed"`
	FreedBytes uint64 `json:"freed_bytes"`
	// ShardsCovered of ShardsTotal were walked to the end; Finished is true
	// when that is all of them. A trim that did not finish resumes at the
	// first uncovered shard next pass.
	ShardsCovered int  `json:"shards_covered"`
	ShardsTotal   int  `json:"shards_total"`
	Finished      bool `json:"finished"`
}

// BudgetUnreachableError marks a cache no trim can bring under its size budget,
// because more than the budget was used inside the minimum age (Floor: the
// policy's go_cache_min_age, 20m by default). Entries that young are
// indistinguishable from a build's work in progress, so they are never
// deleted: the pass removed everything older and the cache still sits at
// roughly Young. Only a slower fill, or a larger budget, fixes it.
//
// It is not a failure — NewGCResult records it apart, in OverBudget — but it is
// never silent either. Before it existed a 20 GiB budget held a 152 GiB cache
// on a machine filling at ~70 GiB/h, and every pass reported itself finished.
type BudgetUnreachableError struct {
	// Cache names the cache ("shared go build cache").
	Cache string
	// Estimated is the cache's estimated size, Budget its size budget, and
	// Young the estimated bytes last used inside Floor, all in bytes.
	Estimated, Budget, Young uint64
	Floor                    time.Duration
}

func (e *BudgetUnreachableError) Error() string {
	gib := func(b uint64) float64 { return float64(b) / float64(GiB) }
	return fmt.Sprintf("%s is over its budget and no trim can reach it: ~%.1f GiB against %.1f GiB, of which ~%.1f GiB "+
		"was used in the last %s (~%.0f GiB/h). Entries that young may belong to a running build, so they are never "+
		"trimmed; raise go_cache_gib, or slow the fill. A common cause is builds without -trimpath, which compile every "+
		"worktree's packages again: run `go env GOFLAGS` in a worktree (a go.mod toolchain line that switches to a "+
		"downloaded toolchain drops a -trimpath set in $GOROOT/go.env)",
		e.Cache, gib(e.Estimated), gib(e.Budget), gib(e.Young), e.Floor, gib(e.Young)/e.Floor.Hours())
}

// streamTrimGoCache walks the shards in rotated order from start, deleting
// entries older than cutoff as it meets them, so every shard finished has
// already reclaimed its space when a deadline cuts the pass off. Shards are
// worked in parallel; shardsCovered counts the contiguous prefix completed, the
// only part the next pass may skip.
//
// Every unlink goes through del, which paces them, caps the pass and backs it
// off at the first sign of filesystem distress. Any of those stops every
// worker: a capped or backed-off pass does not read on to the next shard.
func streamTrimGoCache(ctx context.Context, root string, shards []string, start int, cutoff time.Time, apply bool, del deleter) goCachePass {
	n := len(shards)
	pass := goCachePass{shardsTotal: n}
	done := make([]bool, n)
	var mu sync.Mutex
	var next, removed, freed atomic.Int64
	var halted atomic.Bool
	halt := func(err error) {
		mu.Lock()
		if pass.stop == nil {
			pass.stop = err
		}
		mu.Unlock()
		halted.Store(true)
	}
	var wg sync.WaitGroup
	for w := 0; w < goCacheWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil && !halted.Load() {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				shard := shards[(start+i)%n]
				entries, err := readGoCacheShard(root, shard)
				if err != nil {
					halt(del.observe(filepath.Join(root, shard), err))
					return
				}
				complete := true
				for _, e := range entries {
					if !e.mtime.Before(cutoff) {
						continue
					}
					if ctx.Err() != nil || halted.Load() {
						complete = false
						break
					}
					if apply {
						if err := e.remove(ctx, del, root); err != nil {
							if stopsPass(err) {
								if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
									halt(err)
								}
								complete = false
								break
							}
							continue // this entry could not be removed; the rest can
						}
					}
					removed.Add(1)
					freed.Add(e.size)
					mu.Lock()
					if len(pass.samples) < 5 {
						pass.samples = append(pass.samples, fmt.Sprintf("%s (%d bytes)", e.path(root), e.size))
					}
					mu.Unlock()
				}
				if complete {
					mu.Lock()
					done[i] = true
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	for pass.shardsCovered < n && done[pass.shardsCovered] {
		pass.shardsCovered++
	}
	pass.finished = pass.shardsCovered == n
	pass.removed, pass.freed = int(removed.Load()), uint64(freed.Load())
	return pass
}

func verb(apply bool) string {
	if apply {
		return "remove"
	}
	return "would remove"
}

// ---- goimports module index -------------------------------------------------

var goimportsPayload = regexp.MustCompile(`^index-[0-9]+-[0-9]+$`)

const goimportsKeepRecent = 24 * time.Hour

// GoimportsIndex prunes stale goimports/gopls module-index generations.
//
// x/tools/internal/modindex writes each index to a fresh randomly named
// payload (index-<ver>-<rand>) and then points a predictable link file
// (index-name-<ver>-<sha256 of GOMODCACHE>) at it by name; Read follows the
// link and nothing prunes old payloads. So the current generations are exactly
// the payload names held by link files. Those, and anything touched in the
// last 24h (a writer between "payload written" and "link updated"), are kept.
// If no link can be read the current generation is not certain, so the newest
// two payloads are kept as well.
func (r Runner) GoimportsIndex(apply bool) error {
	root := r.GoimportsRoot
	if root == "" {
		if testing.Testing() {
			r.print("skip goimports index (%v)\n", refuseUnderTest("GoimportsRoot"))
			return nil
		}
		dir, err := os.UserCacheDir()
		if err != nil {
			return nil
		}
		root = filepath.Join(dir, "goimports")
	}
	files, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	type payload struct {
		name  string
		size  int64
		mtime time.Time
	}
	var payloads []payload
	keep := map[string]bool{}
	linksOK := true
	links := 0
	for _, f := range files {
		switch {
		case strings.HasPrefix(f.Name(), "index-name-"):
			links++
			b, err := os.ReadFile(filepath.Join(root, f.Name()))
			if err != nil || len(b) == 0 {
				linksOK = false
				continue
			}
			keep[strings.TrimSpace(string(b))] = true
		case goimportsPayload.MatchString(f.Name()):
			info, err := f.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			payloads = append(payloads, payload{f.Name(), info.Size(), info.ModTime()})
		}
	}
	sort.Slice(payloads, func(i, j int) bool { return payloads[i].mtime.After(payloads[j].mtime) })
	if !linksOK || links == 0 {
		for i := 0; i < len(payloads) && i < 2; i++ {
			keep[payloads[i].name] = true
		}
	}
	now := time.Now()
	var reclaim int64
	var victims []payload
	for i := len(payloads) - 1; i >= 0; i-- { // oldest first
		p := payloads[i]
		if keep[p.name] || now.Sub(p.mtime) < goimportsKeepRecent {
			continue
		}
		victims = append(victims, p)
		reclaim += p.size
	}
	r.print("goimports index %s: %d generations, %d stale (%.1f GiB)\n", root, len(payloads), len(victims), float64(reclaim)/float64(GiB))
	ctx := r.hostCtx()
	del := r.deletes()
	removed := 0
	for _, p := range victims {
		if ctx.Err() != nil {
			r.print("goimports index: stopped early at the deadline; %d stale generations retained\n", len(victims)-removed)
			return nil
		}
		r.print("  %s %s (%d bytes)\n", verb(apply), filepath.Join(root, p.name), p.size)
		if apply {
			if err := del.unlink(ctx, filepath.Join(root, p.name)); err != nil {
				if stopsPass(err) {
					r.print("goimports index: stopped (%v); %d stale generations retained\n", err, len(victims)-removed)
				}
				return err
			}
		}
		removed++
	}
	return nil
}

// goCacheStatus reports the shared cache's estimated size from a sample of
// shards (a full scan of a million-entry cache is minutes under load).
func (r Runner) goCacheStatus(ctx context.Context) {
	root, _, err := r.goEnv(ctx)
	if err != nil {
		return
	}
	shards, err := goCacheShardList(root)
	if err != nil || len(shards) == 0 {
		return
	}
	minAge, err := r.Policy.goCacheMinAge()
	if err != nil {
		return
	}
	sample := sampleGoCache(ctx, root, shards, 0, time.Now(), minAge)
	if sample.shards == 0 {
		return
	}
	est := float64(sample.bytes) * float64(len(shards)) / float64(sample.shards)
	budget := r.goCacheBudget(root)
	young := goCacheYoungBytes(sample, len(shards))
	r.print("go build cache %s: ~%.1f GiB estimated from %d/%d shards (budget %.1f GiB, unused %s; ~%.1f GiB used in the last %s, which no trim deletes)\n", root,
		est/float64(GiB), sample.shards, len(shards), float64(budget)/float64(GiB), r.Policy.GoCacheUnused, float64(young)/float64(GiB), minAge)
	if young > budget {
		r.print("WARNING %v\n", &BudgetUnreachableError{Cache: "go build cache", Estimated: uint64(est), Budget: budget, Young: young, Floor: minAge})
	}
}

// ProductionGoCacheRoots resolves the machine's Go-toolchain cache locations
// for a Runner: the shared GOCACHE/GOMODCACHE (from `go env`), the
// golangci-lint cache and the goimports index. Anything that cannot be located
// is left empty, which the layers skip rather than guess.
func ProductionGoCacheRoots(ctx context.Context, command func(context.Context, string, ...string) ([]byte, error)) (gocache, gomodcache, golangci, goimports string) {
	if out, err := command(ctx, "go", "env", "GOCACHE", "GOMODCACHE"); err == nil {
		if lines := strings.Split(strings.TrimSpace(string(out)), "\n"); len(lines) == 2 {
			gocache, gomodcache = strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
		}
	}
	if env := os.Getenv("GOLANGCI_LINT_CACHE"); env != "" {
		golangci = env
	}
	if dir, err := os.UserCacheDir(); err == nil {
		if golangci == "" {
			golangci = filepath.Join(dir, "golangci-lint")
		}
		goimports = filepath.Join(dir, "goimports")
	}
	return
}
