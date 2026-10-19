// Copyright (c) 2025 Reliant Labs
package diskwatch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// Cache trim: the daemon's own size budgets for the tool caches forge's
// storage reaper (reaper.go) does not own.
//
// Who owns what. forge's reaper trims the Go-format caches — GOCACHE and
// golangci-lint, oldest first down to its go_cache_gib budget — plus the
// goimports index, docker's build cache and temp scratch. Nothing trims the
// npm cache (7 GB on the machine that motivated this, plus ~/.npm), and npm
// never evicts on its own; nothing bounds the Go module cache; and builds
// killed mid-link leave go-build* work directories in TMPDIR forever. This file
// keeps those inside budgets derived from the size of the volume each lives
// on, least recently used first. It runs in the same single-flight as the
// reaper, on the same schedule, and the disk-pressure guard reaches both
// through Watcher.TrimCaches.
//
// Least recently used. An entry's last use is the later of its mtime and atime.
// npm writes a cache entry once and only reads it after; Linux's default
// relatime updates atime on a read at most once a day, so npm's LRU order has
// day granularity, which is what CacheBudget.MinAge is sized for. The Go module
// cache is read-only and works the same way.
//
// Why the module cache only above a hard ceiling. Module downloads are
// content-addressed and every build resolves through them: evicting one a
// build is about to read fails it outright (or, offline or behind a private
// proxy, for good), and re-downloading is slower than any cache hit. It grows
// only as fast as new module versions appear, not with every worktree the way
// the build cache does. So it is left alone below GoModCeiling and, above it,
// trimmed by whole module versions untouched for a week.
//
// Removal is paced (RatePerSec unlinks a second in batches) and stops at the
// first sign of filesystem distress, like forge's deletion governor: a burst
// of unlinks on a virtiofs volume detached it on 2026-10-09. There is no
// per-pass cap: pacing is the protection, and a pass far over budget must be
// able to reach it.

// CacheBudget is one cache's size budget. The budget is Percent of the
// volume the cache lives on, clamped to [Min, Max]; a pass over it trims, least
// recently used first, down to Target of it, never removing anything used in
// the last MinAge.
type CacheBudget struct {
	Percent  float64
	Min, Max uint64
	// Target is the fraction of the budget a trim brings the cache down to,
	// so one pass buys headroom instead of trimming a few MB every hour.
	Target float64
	MinAge time.Duration
	// Off disables this cache's trim.
	Off bool
}

// bytesFor is the budget on a volume of the given size.
func (b CacheBudget) bytesFor(volume uint64) uint64 {
	v := uint64(float64(volume) * b.Percent / 100)
	if v < b.Min {
		v = b.Min
	}
	if b.Max > 0 && v > b.Max {
		v = b.Max
	}
	return v
}

// CacheTrimConfig configures the trim. The zero value of any field is its
// default (DefaultCacheTrimConfig).
type CacheTrimConfig struct {
	// Npm is the npm cache's budget.
	Npm CacheBudget
	// GoModCeiling is the Go module cache's HARD CEILING: below it the module
	// cache is never touched.
	GoModCeiling CacheBudget
	// GoModInterval is how often the module cache is measured: it is a walk
	// of every extracted file, and it grows slowly.
	GoModInterval time.Duration
	// GoWorkDirAge is the age past which a go-build* directory in TMPDIR (a
	// killed build's work dir) is removed; negative disables it.
	GoWorkDirAge time.Duration
	// UrgentFactor scales every budget for TrimCaches(urgent=true), the
	// disk-pressure guard's call: the caches are trimmed to UrgentFactor of
	// their budgets (the module cache's ceiling is never lowered).
	UrgentFactor float64
	// RatePerSec paces unlinks.
	RatePerSec int
	// PassBudget bounds one trim pass.
	PassBudget time.Duration
}

// DefaultCacheTrimConfig returns the working defaults. On the 413 GiB
// volume of the motivating machine: npm 8.3 GiB, module cache ceiling 41 GiB.
func DefaultCacheTrimConfig() CacheTrimConfig {
	return CacheTrimConfig{
		Npm:           CacheBudget{Percent: 2, Min: 2 * GiB, Max: 10 * GiB, Target: 0.8, MinAge: 24 * time.Hour},
		GoModCeiling:  CacheBudget{Percent: 10, Min: 10 * GiB, Max: 60 * GiB, Target: 0.8, MinAge: 7 * 24 * time.Hour},
		GoModInterval: 24 * time.Hour,
		GoWorkDirAge:  12 * time.Hour,
		UrgentFactor:  0.5,
		RatePerSec:    1000,
		PassBudget:    10 * time.Minute,
	}
}

func (c CacheTrimConfig) withDefaults() CacheTrimConfig {
	d := DefaultCacheTrimConfig()
	budget := func(b, def CacheBudget) CacheBudget {
		if b.Off {
			return b
		}
		if b.Percent == 0 && b.Min == 0 && b.Max == 0 {
			b.Percent, b.Min, b.Max = def.Percent, def.Min, def.Max
		}
		if b.Target <= 0 || b.Target > 1 {
			b.Target = def.Target
		}
		if b.MinAge <= 0 {
			b.MinAge = def.MinAge
		}
		return b
	}
	c.Npm = budget(c.Npm, d.Npm)
	c.GoModCeiling = budget(c.GoModCeiling, d.GoModCeiling)
	if c.GoModInterval <= 0 {
		c.GoModInterval = d.GoModInterval
	}
	if c.GoWorkDirAge == 0 {
		c.GoWorkDirAge = d.GoWorkDirAge
	}
	if c.UrgentFactor <= 0 || c.UrgentFactor > 1 {
		c.UrgentFactor = d.UrgentFactor
	}
	if c.RatePerSec <= 0 {
		c.RatePerSec = d.RatePerSec
	}
	if c.PassBudget <= 0 {
		c.PassBudget = d.PassBudget
	}
	return c
}

// CacheTrimResult is what one trim did to one cache.
type CacheTrimResult struct {
	Kind string
	Path string
	// Size is the cache's size when the pass looked (an estimate from a
	// sample of its shards for npm), Budget its budget, Target what a trim
	// aims for.
	Size, Budget, Target uint64
	Freed                uint64
	Removed              int
	// Stopped says why the trim ended before reaching Target: a deadline, a
	// filesystem in distress, or nothing old enough left to remove.
	Stopped string
}

// TrimResult is what Watcher.TrimCaches did.
type TrimResult struct {
	Caches []CacheTrimResult
	// Reaper is forge's pass over the Go-format caches and docker.
	Reaper ReaperResult
}

// Freed is the total freed by the native trims (the reaper's own measure is
// in Reaper.Freed).
func (r TrimResult) Freed() uint64 {
	var n uint64
	for _, c := range r.Caches {
		n += c.Freed
	}
	return n
}

// cacheTrimState is the trim's state on a Watcher, guarded by Watcher.mu.
type cacheTrimState struct {
	cfg       CacheTrimConfig
	lastGoMod time.Time
	cursor    map[string]int
	// remove overrides os.Remove (tests: distress).
	remove func(string) error
}

// ConfigureCacheTrim sets the trim's budgets. Zero fields take defaults.
func (w *Watcher) ConfigureCacheTrim(cfg CacheTrimConfig) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.trim.cfg = cfg.withDefaults()
	w.mu.Unlock()
}

func (w *Watcher) cacheTrimConfig() CacheTrimConfig {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.trim.cfg.withDefaults()
}

// TrimCaches is the entrypoint for a caller that needs disk space now — the
// disk-pressure guard at 90%. It waits for any storage pass already running
// (the schedule's, a user's clean-up), then trims the native caches and runs
// forge's reaper once, whatever the schedule's mode. urgent trims each native
// cache to UrgentFactor of its budget, measures the module cache whatever its
// interval (it is still only trimmed above its ceiling), and removes go-build
// work dirs older than two hours. It never returns an error: what it could not
// do is in the result and the log.
func (w *Watcher) TrimCaches(ctx context.Context, urgent bool) TrimResult {
	if w == nil {
		return TrimResult{}
	}
	if !w.acquireReaper(ctx) {
		return TrimResult{}
	}
	defer w.releaseReaper()
	trigger := "trim"
	if urgent {
		trigger = "trim_urgent"
	}
	res := TrimResult{Caches: w.trimNativeCaches(ctx, urgent, urgent, trigger)}
	res.Reaper = w.reaperPass(ctx, w.reaperPolicyPath(w.reaperConfig()), trigger)
	return res
}

// trimNativeCaches runs one pass over the caches this file owns. urgent
// applies UrgentFactor; measureMod measures the module cache whatever
// GoModInterval says. The caller holds the storage single-flight.
func (w *Watcher) trimNativeCaches(ctx context.Context, urgent, measureMod bool, trigger string) (out []CacheTrimResult) {
	defer func() {
		if r := recover(); r != nil {
			logging.Warn("[diskwatch] Cache trim panicked", "trigger", trigger, "panic", fmt.Sprint(r))
		}
	}()
	if w.systemEnv && testing.Testing() {
		logging.Warn("[diskwatch] Refusing to trim this machine's real caches under go test (give the Watcher an Environment)")
		return nil
	}
	cfg := w.cacheTrimConfig()
	ctx, cancel := context.WithTimeout(ctx, cfg.PassBudget)
	defer cancel()
	p := w.newPacer(cfg.RatePerSec)

	scale := 1.0
	if urgent {
		scale = cfg.UrgentFactor
	}
	for _, t := range w.targetList() {
		if ctx.Err() != nil || p.stopped != nil {
			break
		}
		var r *CacheTrimResult
		switch t.Kind {
		case KindNpmCache:
			if !cfg.Npm.Off {
				r = w.trimNpm(ctx, t.Path, cfg.Npm, scale, p)
			}
		case KindGoModCache:
			if !cfg.GoModCeiling.Off {
				r = w.trimGoMod(ctx, t.Path, cfg, measureMod, p)
			}
		}
		if r != nil {
			out = append(out, *r)
			logCacheTrim(trigger, *r)
		}
	}
	age := cfg.GoWorkDirAge
	if urgent && (age < 0 || age > 2*time.Hour) {
		age = 2 * time.Hour
	}
	if age > 0 && ctx.Err() == nil && p.stopped == nil {
		if r := w.trimGoWorkDirs(ctx, age, p); r != nil {
			out = append(out, *r)
			logCacheTrim(trigger, *r)
		}
	}
	return out
}

func logCacheTrim(trigger string, r CacheTrimResult) {
	fields := []any{"trigger", trigger, "kind", r.Kind, "path", r.Path, "size", FormatBytes(r.Size),
		"budget", FormatBytes(r.Budget), "freed", FormatBytes(r.Freed), "removed", r.Removed}
	switch {
	case r.Removed == 0 && r.Stopped == "":
		logging.Debug("[diskwatch] Cache within budget", fields...)
	case r.Stopped != "":
		logging.Warn("[diskwatch] Cache trim stopped before its target", append(fields, "target", FormatBytes(r.Target), "stopped", r.Stopped)...)
	default:
		logging.Info("[diskwatch] Cache trimmed", append(fields, "target", FormatBytes(r.Target))...)
	}
}

// volumeSize is the size of the filesystem holding path; 0 when unknown.
func (w *Watcher) volumeSize(path string) uint64 {
	st, _, err := w.stat(path)
	if err != nil {
		return 0
	}
	return st.usage().TotalBytes
}

// ---- pacing --------------------------------------------------------------

// trimBatch unlinks are granted between pacing pauses.
const trimBatch = 200

// pacer paces one pass's unlinks and stops it at the first distress error.
type pacer struct {
	rate      int
	inBatch   int
	batchFrom time.Time
	remove    func(string) error
	stopped   error
}

func (w *Watcher) newPacer(rate int) *pacer {
	w.mu.Lock()
	remove := w.trim.remove
	w.mu.Unlock()
	if remove == nil {
		remove = os.Remove
	}
	return &pacer{rate: rate, remove: remove}
}

// unlink removes one file or empty directory, pacing first. A path already
// gone is not an error. A distress error stops the pass.
func (p *pacer) unlink(ctx context.Context, path string) error {
	if p.stopped != nil {
		return p.stopped
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.inBatch >= trimBatch {
		if wait := time.Until(p.batchFrom.Add(time.Second * trimBatch / time.Duration(p.rate))); wait > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		p.inBatch = 0
	}
	if p.inBatch == 0 {
		p.batchFrom = time.Now()
	}
	p.inBatch++
	err := p.remove(path)
	if err != nil && errors.Is(err, fs.ErrPermission) {
		// Module caches write read-only files and directories.
		_ = os.Chmod(filepath.Dir(path), 0o755)
		_ = os.Chmod(path, 0o644)
		err = p.remove(path)
	}
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if distressed(err) {
		p.stopped = fmt.Errorf("filesystem distress at %s: %w", path, err)
		return p.stopped
	}
	return err
}

// removeTree removes a tree through the pacer, children first, never
// following a symlink; directories are made writable so a read-only module
// tree can be emptied. It returns the bytes it freed.
func (p *pacer) removeTree(ctx context.Context, path string) (freed uint64, err error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	if info.IsDir() {
		_ = os.Chmod(path, 0o755)
		entries, rerr := os.ReadDir(path)
		if rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			if distressed(rerr) {
				p.stopped = rerr
			}
			return 0, rerr
		}
		for _, e := range entries {
			n, err := p.removeTree(ctx, filepath.Join(path, e.Name()))
			freed += n
			if err != nil {
				return freed, err
			}
		}
	} else {
		freed = allocatedBytes(info)
	}
	if err := p.unlink(ctx, path); err != nil {
		return freed, err
	}
	return freed, nil
}

func distressed(err error) bool {
	return errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) ||
		errors.Is(err, syscall.EIO) || errors.Is(err, syscall.ENOTCONN)
}

func stopReason(ctx context.Context, p *pacer) string {
	switch {
	case p.stopped != nil:
		return p.stopped.Error()
	case ctx.Err() != nil:
		return "out of time this pass: " + ctx.Err().Error()
	}
	return ""
}

// ---- npm -----------------------------------------------------------------

// lruBucket is the resolution of the size-by-last-use histogram.
const lruBucket = 10 * time.Minute

// npmSampleShards bounds the shards read to estimate the cache's size and age
// distribution; entries are hash-named, so a shard is a fair sample.
const npmSampleShards = 64

// npmShards lists the hash-sharded directories of an npm cache: every
// _cacache/content-v2/<algo>/<xx> and _cacache/index-v5/<xx>. Anything else
// (tmp, _logs, _npx) is not a cache entry and never touched.
func npmShards(root string) []string {
	cc := filepath.Join(root, "_cacache")
	var out []string
	add := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() && isShard(e.Name()) {
				out = append(out, filepath.Join(dir, e.Name()))
			}
		}
	}
	if algos, err := os.ReadDir(filepath.Join(cc, "content-v2")); err == nil {
		for _, a := range algos {
			if a.IsDir() {
				add(filepath.Join(cc, "content-v2", a.Name()))
			}
		}
	}
	add(filepath.Join(cc, "index-v5"))
	sort.Strings(out)
	return out
}

// trimNpm keeps an npm cache within its budget. Removing a content file or an
// index bucket is a cache miss to npm, which fetches the package again
// (pacote retries a cache ENOENT from the network); npm never needs an entry
// to exist.
func (w *Watcher) trimNpm(ctx context.Context, root string, b CacheBudget, scale float64, p *pacer) *CacheTrimResult {
	shards := npmShards(root)
	if len(shards) == 0 {
		return nil // not an npm cache (or an empty one): nothing to own
	}
	budget := uint64(float64(b.bytesFor(w.volumeSize(root))) * scale)
	res := &CacheTrimResult{Kind: KindNpmCache, Path: root, Budget: budget, Target: uint64(float64(budget) * b.Target)}
	now := w.now()
	floor := now.Add(-b.MinAge)

	w.mu.Lock()
	if w.trim.cursor == nil {
		w.trim.cursor = map[string]int{}
	}
	start := w.trim.cursor[root] % len(shards)
	w.mu.Unlock()

	// Size and age distribution from a sample of shards, scaled.
	n := min(npmSampleShards, len(shards))
	hist := map[int64]uint64{}
	var sampled uint64
	for i := 0; i < n && ctx.Err() == nil; i++ {
		walkFiles(shards[(start+i*len(shards)/n)%len(shards)], func(_ string, info fs.FileInfo) {
			size := allocatedBytes(info)
			sampled += size
			if lu := lastUse(info); lu.Before(floor) {
				hist[lu.UnixNano()/int64(lruBucket)] += size
			}
		})
	}
	ratio := float64(len(shards)) / float64(n)
	res.Size = uint64(float64(sampled) * ratio)
	if res.Size <= budget {
		return res
	}
	excess := res.Size - res.Target
	cutoff := lruCutoff(hist, ratio, excess, floor)

	// Remove everything last used before the cutoff, walking the shards from
	// the cursor, until the excess is freed.
	covered := 0
	for i := 0; i < len(shards) && res.Freed < excess && ctx.Err() == nil && p.stopped == nil; i++ {
		walkFiles(shards[(start+i)%len(shards)], func(path string, info fs.FileInfo) {
			if res.Freed >= excess || p.stopped != nil || ctx.Err() != nil || !lastUse(info).Before(cutoff) {
				return
			}
			if err := p.unlink(ctx, path); err == nil {
				res.Freed += allocatedBytes(info)
				res.Removed++
			}
		})
		covered = i + 1
	}
	w.mu.Lock()
	w.trim.cursor[root] = (start + covered) % len(shards)
	w.mu.Unlock()
	if res.Freed < excess {
		res.Stopped = stopReason(ctx, p)
		if res.Stopped == "" {
			res.Stopped = fmt.Sprintf("nothing else unused for %s", b.MinAge)
		}
	}
	return res
}

// lruCutoff is the last-use time below which entries go: the end of the
// oldest bucket at which the (scaled) evictable bytes reach excess, or the
// floor when everything evictable together does not.
func lruCutoff(hist map[int64]uint64, ratio float64, excess uint64, floor time.Time) time.Time {
	keys := make([]int64, 0, len(hist))
	for k := range hist {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	var acc float64
	for _, k := range keys {
		acc += float64(hist[k]) * ratio
		if acc >= float64(excess) {
			cut := time.Unix(0, (k+1)*int64(lruBucket))
			if cut.After(floor) {
				return floor
			}
			return cut
		}
	}
	return floor
}

// walkFiles calls fn for every regular file under dir, without following
// symlinks. Unreadable entries are skipped.
func walkFiles(dir string, fn func(path string, info fs.FileInfo)) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		fn(p, info)
		return nil
	})
}

// lastUse is the later of a file's mtime and atime.
func lastUse(info fs.FileInfo) time.Time {
	m := info.ModTime()
	if a, ok := accessTime(info); ok && a.After(m) {
		return a
	}
	return m
}

// ---- Go module cache -----------------------------------------------------

// modVersion is one module version in the module cache: its download files
// (cache/download/<module>/@v/<version>.*) and its extracted tree
// (<module>@<version>).
type modVersion struct {
	key       string
	files     []string
	tree      string
	bytes     uint64
	lastUsed  time.Time
	hasUnpack bool
}

// trimGoMod measures the module cache at most once per GoModInterval (or when
// forced) and, only when it is above its ceiling, removes whole
// module versions least recently used first, never one used within MinAge.
func (w *Watcher) trimGoMod(ctx context.Context, root string, cfg CacheTrimConfig, force bool, p *pacer) *CacheTrimResult {
	if fi, err := os.Stat(filepath.Join(root, "cache", "download")); err != nil || !fi.IsDir() {
		return nil // not a module cache
	}
	now := w.now()
	w.mu.Lock()
	due := force || w.trim.lastGoMod.IsZero() || now.Sub(w.trim.lastGoMod) >= cfg.GoModInterval
	if due {
		w.trim.lastGoMod = now
	}
	w.mu.Unlock()
	if !due {
		return nil
	}
	b := cfg.GoModCeiling
	ceiling := b.bytesFor(w.volumeSize(root))
	res := &CacheTrimResult{Kind: KindGoModCache, Path: root, Budget: ceiling, Target: uint64(float64(ceiling) * b.Target)}

	versions := map[string]*modVersion{}
	get := func(key string) *modVersion {
		v := versions[key]
		if v == nil {
			v = &modVersion{key: key}
			versions[key] = v
		}
		return v
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil || rel == "." {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if d.IsDir() {
			// <module>@<version> outside cache/: one extracted tree (the
			// first path element carrying an @; one deeper is its content).
			if parts[0] != "cache" && strings.Contains(parts[len(parts)-1], "@") && !strings.Contains(strings.Join(parts[:len(parts)-1], "/"), "@") {
				v := get(rel)
				v.tree, v.hasUnpack = path, true
				if lu := lastUse(info); lu.After(v.lastUsed) {
					v.lastUsed = lu
				}
			}
			return nil
		}
		size := allocatedBytes(info)
		res.Size += size
		key := ""
		if len(parts) >= 4 && parts[0] == "cache" && parts[1] == "download" && parts[len(parts)-2] == "@v" {
			base := parts[len(parts)-1]
			ver := base
			for _, ext := range []string{".zip", ".ziphash", ".info", ".mod", ".lock", ".partial"} {
				ver = strings.TrimSuffix(ver, ext)
			}
			if ver != base && ver != "list" {
				key = strings.Join(parts[2:len(parts)-2], "/") + "@" + ver
				v := get(key)
				v.files = append(v.files, path)
			}
		} else if parts[0] != "cache" {
			for i, part := range parts[:len(parts)-1] {
				if strings.Contains(part, "@") {
					key = strings.Join(parts[:i+1], "/")
					break
				}
			}
		}
		if key != "" {
			v := get(key)
			v.bytes += size
			if lu := lastUse(info); lu.After(v.lastUsed) {
				v.lastUsed = lu
			}
		}
		return nil
	})
	if ctx.Err() != nil {
		res.Stopped = stopReason(ctx, p)
		return res
	}
	if res.Size <= ceiling {
		return res
	}
	list := make([]*modVersion, 0, len(versions))
	for _, v := range versions {
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].lastUsed.Before(list[j].lastUsed) })
	floor := now.Add(-b.MinAge)
	excess := res.Size - res.Target
	for _, v := range list {
		if res.Freed >= excess || ctx.Err() != nil || p.stopped != nil {
			break
		}
		if !v.lastUsed.Before(floor) {
			break // sorted: nothing later is old enough either
		}
		// The extracted tree first: a version with its zip but no tree is
		// re-extracted by the next build, one with a tree but no download
		// files is not trusted by go.
		var freed uint64
		if v.hasUnpack {
			n, err := p.removeTree(ctx, v.tree)
			freed += n
			if err != nil {
				res.Freed += freed
				break
			}
		}
		for _, f := range v.files {
			info, err := os.Lstat(f)
			if err != nil {
				continue
			}
			if err := p.unlink(ctx, f); err != nil {
				break
			}
			freed += allocatedBytes(info)
		}
		res.Freed += freed
		res.Removed++
	}
	if res.Freed < excess {
		res.Stopped = stopReason(ctx, p)
		if res.Stopped == "" {
			res.Stopped = fmt.Sprintf("nothing else unused for %s", b.MinAge)
		}
	}
	return res
}

// ---- go-build work dirs --------------------------------------------------

// trimGoWorkDirs removes go-build* directories in TMPDIR (and GOTMPDIR)
// whose directory and direct children are all older than age: the work dirs
// of builds that were killed before they could remove their own.
func (w *Watcher) trimGoWorkDirs(ctx context.Context, age time.Duration, p *pacer) *CacheTrimResult {
	var dirs []string
	for _, k := range []string{"GOTMPDIR", "TMPDIR"} {
		if v := strings.TrimSpace(w.env.Getenv(k)); v != "" && filepath.IsAbs(v) {
			dirs = append(dirs, filepath.Clean(v))
		}
	}
	if len(dirs) == 0 && w.env.GOOS != "windows" {
		dirs = append(dirs, "/tmp")
	}
	cutoff := w.now().Add(-age)
	res := &CacheTrimResult{Kind: "go_work_dirs"}
	seen := map[string]bool{}
	for _, dir := range dirs {
		if seen[dir] {
			continue
		}
		seen[dir] = true
		res.Path = strings.TrimPrefix(res.Path+", "+dir, ", ")
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if ctx.Err() != nil || p.stopped != nil {
				break
			}
			if !e.IsDir() || !strings.HasPrefix(e.Name(), "go-build") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			if !olderThan(path, cutoff) {
				continue
			}
			n, err := p.removeTree(ctx, path)
			res.Freed += n
			if err == nil {
				res.Removed++
			}
		}
	}
	res.Size = res.Freed
	if res.Removed == 0 && res.Freed == 0 {
		return nil
	}
	return res
}

// olderThan reports whether a directory and every direct child were last
// modified before cutoff.
func olderThan(dir string, cutoff time.Time) bool {
	info, err := os.Lstat(dir)
	if err != nil || !info.ModTime().Before(cutoff) {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		ci, err := e.Info()
		if err != nil || !ci.ModTime().Before(cutoff) {
			return false
		}
	}
	return true
}
