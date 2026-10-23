package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/commitpolicy"
	"github.com/reliant-labs/forge/internal/openfiles"
)

// Orphaned private Go caches.
//
// Agents ran builds with GOCACHE=<fresh dir> (and sometimes GOMODCACHE=<dir>),
// so every task left a cold 5-14 GB cache nobody reused or deleted: 283 GB in
// 48 directories on one machine. A directory is reaped only when ALL hold:
//
//   - it is a recognised Go build cache (README sentinel) or module cache
//     (cache/download layout), found by a depth-limited scan of known roots;
//   - it is not, and does not contain or sit inside, the shared GOCACHE/GOMODCACHE;
//   - no live process names it: the environment/arguments of every process of
//     this user are searched for its path, and an unreadable listing skips the
//     whole layer (fail closed);
//   - no process holds a file inside it open (lsof snapshot; fails closed);
//   - the newest FILE mtime anywhere inside is older than 24h. Directory
//     mtimes are ignored on purpose: a partially deleted tree has fresh
//     directory mtimes but must stay eligible for the next pass.

const (
	orphanIdle     = 24 * time.Hour
	orphanMaxDepth = 3
)

type orphanCandidate struct {
	path   string // as discovered
	real   string // symlinks resolved
	kind   string // "build" or "mod"
	size   int64
	newest time.Time
}

// scanRoots returns the directories searched for private caches.
func (r Runner) orphanScanRoots(ctx context.Context) []string {
	seen := map[string]bool{}
	var roots []string
	add := func(p string) {
		if p == "" {
			return
		}
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		if !seen[p] {
			seen[p] = true
			roots = append(roots, p)
		}
	}
	for _, project := range append(append([]string(nil), r.Policy.Projects...), r.Policy.Repos...) {
		out, err := r.command(ctx, "git", "--no-optional-locks", "-C", project, "worktree", "list", "--porcelain")
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			if wt, ok := strings.CutPrefix(line, "worktree "); ok {
				add(wt)
				add(filepath.Dir(wt))
			}
		}
	}
	if !testing.Testing() {
		add(os.TempDir())
		add("/tmp")
	}
	for _, p := range r.Policy.HostPaths {
		add(p)
	}
	return roots
}

func isGoModCacheDir(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "cache", "download"))
	return err == nil && info.IsDir()
}

func goCacheKind(dir string) string {
	switch {
	case commitpolicy.IsGoBuildCacheDir(dir):
		return "build"
	case isGoModCacheDir(dir):
		return "mod"
	}
	return ""
}

// findPrivateGoCaches scans roots to a bounded depth. It never follows
// symlinks and never descends into node_modules, .git, or a cache it found.
func findPrivateGoCaches(ctx context.Context, roots []string) []orphanCandidate {
	seen := map[string]bool{}
	var found []orphanCandidate
	var scan func(dir string, depth int)
	consider := func(dir string) bool {
		kind := goCacheKind(dir)
		if kind == "" {
			return false
		}
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			real = dir
		}
		if !seen[real] {
			seen[real] = true
			found = append(found, orphanCandidate{path: dir, real: real, kind: kind})
		}
		return true
	}
	scan = func(dir string, depth int) {
		if ctx.Err() != nil || depth > orphanMaxDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() { // symlinks report as non-dirs here and are never followed
				continue
			}
			switch e.Name() {
			case "node_modules", ".git":
				continue
			}
			child := filepath.Join(dir, e.Name())
			if consider(child) {
				continue
			}
			scan(child, depth+1)
		}
	}
	for _, root := range roots {
		if consider(root) {
			continue
		}
		scan(root, 1)
	}
	return found
}

// measure sizes a candidate and finds its newest file mtime, stopping early
// once a file younger than idle proves it is in use.
func (c *orphanCandidate) measure(ctx context.Context, now time.Time, idle time.Duration) (idleEnough bool, err error) {
	cutoff := now.Add(-idle)
	young := errors.New("young")
	err = filepath.WalkDir(c.path, func(p string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		c.size += info.Size()
		if filepath.Dir(p) == c.path {
			return nil // README/trim.txt are bookkeeping, not use
		}
		if info.ModTime().After(c.newest) {
			c.newest = info.ModTime()
		}
		if info.ModTime().After(cutoff) {
			return young
		}
		return nil
	})
	if errors.Is(err, young) {
		return false, nil
	}
	return err == nil, err
}

// OrphanedGoCaches reaps abandoned private Go caches. Dry-run unless apply.
func (r Runner) OrphanedGoCaches(apply bool) error {
	ctx := r.hostCtx()
	sharedBuild, sharedMod, err := r.goEnv(ctx)
	if err != nil && !(testing.Testing() && r.GoCacheRoot != "") {
		r.print("skip orphaned go caches (cannot resolve the shared go cache: %v)\n", err)
		return nil
	}
	protected := resolvedAll(sharedBuild, sharedMod)

	found := findPrivateGoCaches(ctx, r.orphanScanRoots(ctx))
	var cands []orphanCandidate
	for _, c := range found {
		if overlapsAny(c.real, protected) {
			continue
		}
		cands = append(cands, c)
	}
	if len(cands) == 0 {
		return ctx.Err()
	}

	// Fail closed on both liveness signals before measuring anything.
	procs, err := r.processEnvText(ctx)
	if err != nil {
		r.print("orphaned go caches skipped: cannot determine which caches live processes use (%v)\n", err)
		return nil
	}
	open, err := r.goCacheOpenPaths(ctx)
	if err != nil {
		r.print("orphaned go caches skipped: cannot determine which caches are open (%v)\n", err)
		return nil
	}

	now := time.Now()
	var reap []orphanCandidate
	var skippedLive, skippedYoung int
	for _, c := range cands {
		if ctx.Err() != nil {
			break
		}
		if strings.Contains(procs, c.path) || strings.Contains(procs, c.real) || open.Holds(c.real) {
			skippedLive++
			continue
		}
		idle, err := c.measure(ctx, now, orphanIdle)
		if err != nil {
			if fsDistress(err) {
				err = r.deletes().observe(c.path, err)
				r.print("orphaned go caches: stopped at %s (%v)\n", c.path, err)
				return err
			}
			break
		}
		if !idle {
			skippedYoung++
			continue
		}
		reap = append(reap, c)
	}
	sort.Slice(reap, func(i, j int) bool { return reap[i].newest.Before(reap[j].newest) })

	var total int64
	for _, c := range reap {
		total += c.size
	}
	r.print("orphaned private go caches: %d candidates found, %d in use, %d touched within %s, %d idle (%.1f GiB)\n",
		len(cands), skippedLive, skippedYoung, orphanIdle, len(reap), float64(total)/float64(GiB))
	for _, c := range reap {
		r.print("  %s %s cache %s (%d bytes, last used %s)\n", verb(apply), c.kind, c.path, c.size, c.newest.Format(time.RFC3339))
	}
	if err := ctx.Err(); err != nil {
		r.print("orphaned go caches: stopped at the deadline; the rest is retained for the next pass\n")
	}
	if !apply {
		return nil
	}

	scanRoots := map[string]bool{}
	for _, root := range r.orphanScanRoots(ctx) {
		scanRoots[root] = true
	}
	del := r.deletes()
	var freed int64
	for _, c := range reap {
		if ctx.Err() != nil {
			break
		}
		// The marker that makes this a recognised cache goes last, so a
		// cache the pass stops partway through is still found next pass.
		marker := "README"
		if c.kind == "mod" {
			marker = "cache"
		}
		if err := del.removeTree(ctx, c.path, marker); err != nil {
			if stopsPass(err) {
				r.print("  stopped in %s (%v); it and the rest are retained for the next pass\n", c.path, err)
				r.print("orphaned go caches: freed %.1f GiB\n", float64(freed)/float64(GiB))
				return err
			}
			r.print("  keep %s: %v\n", c.path, err)
			continue
		}
		freed += c.size
		r.print("  removed %s\n", c.path)
		// A `.gocache-x/{build,mod}` holder is removed once it is empty.
		if parent := filepath.Dir(c.path); !scanRoots[parent] && !scanRoots[filepath.Dir(c.real)] && strings.Contains(strings.ToLower(filepath.Base(parent)), "gocache") {
			if entries, err := os.ReadDir(parent); err == nil && len(entries) == 0 && del.unlink(ctx, parent) == nil {
				r.print("  removed empty %s\n", parent)
			}
		}
	}
	r.print("orphaned go caches: freed %.1f GiB\n", float64(freed)/float64(GiB))
	return nil
}

func resolvedAll(paths ...string) []string {
	var out []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		out = append(out, filepath.Clean(p))
		if real, err := filepath.EvalSymlinks(p); err == nil {
			out = append(out, real)
		}
	}
	return out
}

// overlapsAny reports whether path equals, contains, or lies inside any protected path.
func overlapsAny(path string, protected []string) bool {
	for _, p := range protected {
		if path == p || strings.HasPrefix(path, p+string(filepath.Separator)) || strings.HasPrefix(p, path+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (r Runner) goCacheOpenPaths(ctx context.Context) (openfiles.Snapshot, error) {
	if r.OpenPaths == nil && r.openShared == nil && testing.Testing() {
		return openfiles.Snapshot{}, fmt.Errorf("storage.Runner.OpenPaths is unset under test")
	}
	return r.openSnapshot(ctx)
}

// processEnvText returns the arguments and environment of every process of
// this user as one searchable blob. A candidate cache whose path appears in it
// is in use (GOCACHE/GOMODCACHE, or anything else naming it). The match is a
// plain substring, deliberately broader than "GOCACHE=<path>": over-matching
// keeps a cache for a day, under-matching breaks a running build.
func (r Runner) processEnvText(ctx context.Context) (string, error) {
	if r.ProcessEnv != nil {
		return r.ProcessEnv(ctx)
	}
	if testing.Testing() {
		return "", fmt.Errorf("storage.Runner.ProcessEnv is unset under test")
	}
	return readProcessEnv(ctx)
}

func readProcessEnv(ctx context.Context) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		var out, stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, "ps", "axeww", "-o", "command=")
		cmd.Stdout, cmd.Stderr = &out, &stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("ps: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		if out.Len() == 0 {
			return "", fmt.Errorf("ps returned nothing")
		}
		return out.String(), nil
	case "linux":
		dirs, err := filepath.Glob("/proc/[0-9]*")
		if err != nil || len(dirs) == 0 {
			return "", fmt.Errorf("cannot list /proc")
		}
		var sb strings.Builder
		readable := 0
		for _, d := range dirs {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			for _, name := range []string{"environ", "cmdline"} {
				if b, err := os.ReadFile(filepath.Join(d, name)); err == nil {
					readable++
					sb.WriteString(strings.ReplaceAll(string(b), "\x00", "\n"))
					sb.WriteByte('\n')
				}
			}
		}
		if readable == 0 {
			return "", fmt.Errorf("no readable /proc/<pid>/environ")
		}
		return sb.String(), nil
	}
	return "", fmt.Errorf("process environment listing is not supported on %s", runtime.GOOS)
}
