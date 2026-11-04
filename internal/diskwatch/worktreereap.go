// Copyright (c) 2025 Reliant Labs

package diskwatch

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// The worktree reap: the daemon reclaims the git worktrees nobody registered
// with Reliant.
//
// Reliant's own sweep (worktreesweep, worktreereclaim) only ever sees the
// worktree rows in its database. Agents also run plain `git worktree add`
// (into /tmp, ~/src/..., or straight under ~/.reliant/worktrees), and those
// checkouts, each with its own node_modules and build output, are never
// reclaimed by anything: on one machine they were most of 1,126 registered
// worktrees when its disk reached 96%.
//
// Every such worktree is still registered in its repository's `git worktree
// list`, and the repositories are the ones Reliant's own workspaces were made
// from, so the daemon finds them through the worktree root: each checkout
// there names its repository's git dir. For each repository it runs forge's
// worktree reaper, `<this binary> forge storage worktrees --repo <r> --apply
// --reliant-managed`, which removes a linked worktree only when it is
// unlocked, clean, holds no ignored file that is not rebuildable output, has
// had no activity for 24h (any file in it, build output included), has no
// nested repository or commits only it can reach, has its HEAD on a remote,
// and no process uses it; it moves the worktree aside and re-checks it before
// `git worktree remove` (never --force, never a branch deletion).
//
// --reliant-managed lets it judge the agent-made checkouts under the worktree
// root too. Reliant's own checkouts are protected there by their lock: the
// sweep locks every live and archived one (worktreereclaim.LockReason), and
// forge holds every locked worktree. One the sweep has not locked yet is at
// worst recreated from its branch by worktree.ensure on its chat's next tool
// call, since forge only removes what is clean, pushed and idle.
//
// It rides the storage reaper's schedule and single-flight
// (RELIANT_STORAGE_REAPER), at its own slower cadence: one pass runs git over
// every worktree of every repository.

const (
	// WorktreeReapInterval is how often the worktree reap runs.
	WorktreeReapInterval = 6 * time.Hour
	// WorktreeReapPressureInterval is how soon it repeats while a watched
	// volume is under pressure.
	WorktreeReapPressureInterval = 30 * time.Minute
	// worktreeReapRepoWall bounds one repository's pass, for a wedged child.
	worktreeReapRepoWall = 30 * time.Minute
	// worktreeRootDepth bounds the discovery walk: a Reliant workspace is
	// <root>/<project>/<name>-<id>/<repo>; agent checkouts sit higher.
	worktreeRootDepth = 4
	// worktreeRootMaxEntries bounds the directories discovery visits.
	worktreeRootMaxEntries = 20_000
)

// worktreeReapRunner reaps one repository's worktrees and returns once it has
// exited; like reaperRunner, a non-zero exit is a result, not an error.
type worktreeReapRunner func(ctx context.Context, repo string) (reaperRun, error)

// worktreeReapState is the worktree reap's state on a Watcher.
type worktreeReapState struct {
	run worktreeReapRunner
	// guarded by Watcher.mu
	last time.Time
}

// worktreeReapCommandArgs is the argument list after the binary.
func worktreeReapCommandArgs(repo string) []string {
	return []string{"forge", "storage", "worktrees", "--repo", repo, "--apply", "--reliant-managed"}
}

// execWorktreeReap runs `<this binary> forge storage worktrees ...` for repo.
func execWorktreeReap(ctx context.Context, repo string) (reaperRun, error) {
	if testing.Testing() {
		return reaperRun{}, errReaperUnderTest
	}
	self, err := os.Executable()
	if err != nil {
		return reaperRun{}, fmt.Errorf("resolve own executable: %w", err)
	}
	return runReaperProcess(ctx, self, worktreeReapCommandArgs(repo))
}

// worktreeReapDue reports whether a worktree reap should run now.
func (w *Watcher) worktreeReapDue(now time.Time, pressure bool) bool {
	interval := WorktreeReapInterval
	if pressure {
		interval = WorktreeReapPressureInterval
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.worktreeReap.last.IsZero() || now.Sub(w.worktreeReap.last) >= interval
}

// worktreePressure reports whether a volume holding the home directory or the
// worktree root, where worktrees live, is under pressure.
func (w *Watcher) worktreePressure() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.valid {
		return false
	}
	for _, v := range w.vols {
		if w.pressure[v.key] && (v.has(RoleWorktrees) || v.has(RoleHome)) {
			return true
		}
	}
	return false
}

// worktreeReapTick runs a worktree reap if one is due and no reaper pass is
// running. It reports whether it reaped any repository.
func (w *Watcher) worktreeReapTick(ctx context.Context) bool {
	pressure := w.worktreePressure()
	if !w.worktreeReapDue(w.now(), pressure) || !w.tryAcquireReaper() {
		return false
	}
	defer w.releaseReaper()
	trigger := "scheduled"
	if pressure {
		trigger = "pressure"
	}
	return w.worktreeReapPass(ctx, trigger) > 0
}

// worktreeReapPass reaps the worktrees of every repository the worktree root
// leads to, and returns how many repositories it ran over. The caller holds
// the reaper single-flight. A failure is a WARN line: a failed clean-up must
// not take the daemon down.
func (w *Watcher) worktreeReapPass(ctx context.Context, trigger string) (reaped int) {
	defer func() {
		if r := recover(); r != nil {
			logging.WarnContext(ctx, "[diskwatch] Worktree reap panicked", "trigger", trigger, "panic", fmt.Sprint(r))
		}
	}()
	w.mu.Lock()
	w.worktreeReap.last = w.now()
	w.mu.Unlock()

	var repos []string
	for _, t := range w.targetList() {
		if t.Kind == KindWorktrees {
			repos = append(repos, worktreeRepos(t.Path)...)
		}
	}
	if len(repos) == 0 {
		return 0
	}
	logging.InfoContext(ctx, "[diskwatch] Worktree reap starting", "trigger", trigger, "repos", repos)
	for _, repo := range repos {
		if ctx.Err() != nil {
			return reaped
		}
		rctx, cancel := context.WithTimeout(ctx, worktreeReapRepoWall)
		run, err := w.worktreeReap.run(rctx, repo)
		cancel()
		reaped++
		removed := strings.Count(run.Stdout, "removed worktree: ")
		switch {
		case err != nil:
			logging.WarnContext(ctx, "[diskwatch] Worktree reap failed", "repo", repo, "removed", removed, "error", err, "stderr", run.Stderr)
		case run.ExitCode != 0:
			logging.WarnContext(ctx, "[diskwatch] Worktree reap exited non-zero", "repo", repo, "removed", removed, "exit_code", run.ExitCode, "stderr", run.Stderr)
		default:
			logging.InfoContext(ctx, "[diskwatch] Worktree reap finished", "repo", repo, "removed", removed)
		}
	}
	return reaped
}

// worktreeRepos is the main checkout of every repository a linked worktree
// under root belongs to, sorted. It reads each checkout's `.git` file
// ("gitdir: <common>/worktrees/<name>") and its admin dir's commondir; it
// never runs git and never descends into a checkout.
func worktreeRepos(root string) []string {
	root = filepath.Clean(root)
	seen := map[string]bool{}
	entries := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		entries++
		if err != nil || entries > worktreeRootMaxEntries {
			if entries > worktreeRootMaxEntries {
				return fs.SkipAll
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if p != root && (d.Name() == "node_modules" || d.Name() == ".git") {
			return fs.SkipDir
		}
		if repo := linkedWorktreeRepo(p); repo != "" {
			seen[repo] = true
			return fs.SkipDir
		}
		if rel, rerr := filepath.Rel(root, p); rerr == nil && rel != "." && strings.Count(rel, string(filepath.Separator)) >= worktreeRootDepth-1 {
			return fs.SkipDir
		}
		return nil
	})
	repos := make([]string, 0, len(seen))
	for r := range seen {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	return repos
}

// linkedWorktreeRepo is the main checkout of the repository dir is a linked
// worktree of, or "" when dir is not one (no .git file, a bare repository, a
// gitdir that does not resolve).
func linkedWorktreeRepo(dir string) string {
	info, err := os.Lstat(filepath.Join(dir, ".git"))
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, ".git"))
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(b))
	if !strings.HasPrefix(line, "gitdir:") {
		return ""
	}
	admin := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if !filepath.IsAbs(admin) {
		admin = filepath.Join(dir, admin)
	}
	common := filepath.Dir(filepath.Dir(admin)) // <common>/worktrees/<name>
	if c, err := os.ReadFile(filepath.Join(admin, "commondir")); err == nil {
		common = strings.TrimSpace(string(c))
		if !filepath.IsAbs(common) {
			common = filepath.Join(admin, common)
		}
	}
	common = filepath.Clean(common)
	if filepath.Base(common) != ".git" {
		return "" // a bare or separate git dir: no main checkout to run from
	}
	repo := filepath.Dir(common)
	if fi, err := os.Stat(common); err != nil || !fi.IsDir() {
		return ""
	}
	if real, err := filepath.EvalSymlinks(repo); err == nil {
		repo = real
	}
	return repo
}
