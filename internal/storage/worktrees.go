package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/openfiles"
)

// minWorktreeIdle is the floor on how long a worktree must be untouched before
// it can be removed, whatever the caller asks for.
const minWorktreeIdle = 24 * time.Hour

// Hold reasons: why the reaper deliberately left a worktree alone.
const (
	HoldLocked    = "locked"
	HoldActive    = "recently active"
	HoldInUse     = "in use"
	HoldDirty     = "dirty"
	HoldData      = "unrecognized ignored data"
	HoldUnpushed  = "unpushed"
	HoldRefused   = "git refused removal"
	HoldUnchecked = "could not be checked"
	HoldHidden    = "hidden-changes"
	HoldNested    = "nested-repository"
	HoldOrphans   = "unreachable-commits"
	// HoldQuarantined is a worktree a previous pass moved aside and could not
	// move back. It is still a valid worktree and is never deleted by forge.
	HoldQuarantined = "quarantined"
	// HoldNotLinked is anything that is not plainly a linked worktree of the
	// repository: its main checkout, a directory containing it or the home
	// directory, or a path whose .git is not a gitdir file naming one of the
	// repository's worktrees (a stale registration now holding a clone).
	HoldNotLinked = "not a linked worktree"
	// HoldManaged is a worktree Reliant has locked as its own (a lock whose
	// reason starts with reliantLockPrefix). Reliant knows which chat each is
	// bound to and reclaims them itself. Where a worktree lives says nothing:
	// agents also run plain `git worktree add` under Reliant's worktree root,
	// and nothing but this reaper ever reclaims those.
	HoldManaged = "managed by reliant"
	// HoldDeferred is a worktree whose removal would overrun what is left of
	// the pass's delete budget; a later pass takes it.
	HoldDeferred = "deferred"
)

// defaultWorktreeRebuildable lists ignored paths that a build recreates.
// Built from a survey of the ignored content of every reliant, forge and
// control-plane worktree on the author's machine. An entry is one or more
// path segments (globs allowed per segment) matched anywhere in the ignored
// path, or, with a leading "/", only from the worktree's root. Data-bearing
// paths never belong here, and worktreeNeverRebuildable vetoes them even if a
// policy lists them.
var defaultWorktreeRebuildable = []string{
	"node_modules", "dist", "bin", ".gocache", ".next", ".next-prod", ".turbo",
	"coverage", "coverage.out", "*.tsbuildinfo", "next-env.d.ts",
	"test-results", "playwright-report", "__pycache__", ".DS_Store",
	"kcl.mod.lock", "go.work", "go.work.sum",
	".forge/generating-build", ".forge/forge.lock", ".forge/render",
	".forge/logs", ".forge/workspace-base.tag",
	// forge's own scratch and its generate fixed-point cache.
	".forge/tmp", ".forge/generate-cache.json",
	// forge's build/push state records and the temp files its atomic writes
	// leave behind when interrupted.
	".forge/state", ".forge/.forge-tmp-*",
	"target", ".venv", "venv", ".pytest_cache", ".mypy_cache", ".ruff_cache", ".gradle",
	// Pinned tools a project installs into its checkout (bin, stamps, tmp,
	// pre-commit's hook clones): `task tools` puts them back.
	"/.tools",
	// The checkout's scratch directory. A repository inside it is still a
	// nested repository, and data under it is still data (the never-list wins).
	"/tmp",
}

// worktreeNeverRebuildable are paths that hold application data. They are
// held no matter what the policy says.
var worktreeNeverRebuildable = []string{
	"data", ".env", ".env.*", ".forge/hostinfra", "*.db", "*.sqlite", "secrets",
}

// DefaultWorktreeRebuildable returns a copy of the built-in allowlist.
func DefaultWorktreeRebuildable() []string {
	return append([]string(nil), defaultWorktreeRebuildable...)
}

// WorktreeHold is one worktree the reaper left alone, and why.
type WorktreeHold struct {
	Path, Repo, Reason, Detail string
}

// WorktreeReport is the outcome of one classification pass.
type WorktreeReport struct {
	// Removable are worktrees that passed every check (removed when applied).
	Removable []string
	// Removed is the subset actually removed.
	Removed []string
	Held    []WorktreeHold
}

// HeldByReason counts held worktrees per reason.
func (w WorktreeReport) HeldByReason() map[string]int {
	counts := map[string]int{}
	for _, h := range w.Held {
		counts[h.Reason]++
	}
	return counts
}

type reapOptions struct {
	repos []string
	// base overrides the repository's default base branch for the merged test.
	base  string
	idle  time.Duration
	apply bool
}

// reliantLockPrefix starts the reason Reliant records on `git worktree lock`
// for every worktree it manages ("reliant: worktree <id>", and the restored
// and archived variants). It is a cross-tool contract, mirrored by
// LockReason in reliant's internal/worktreereclaim.
const reliantLockPrefix = "reliant: worktree "

// Worktrees is the explicit `forge storage worktrees` entry point. It runs the
// same classifier the GC layer does, over one repository.
func (r Runner) Worktrees(ctx context.Context, repo, base string, idle time.Duration, apply bool) error {
	if idle < minWorktreeIdle {
		return fmt.Errorf("worktree idle time must be at least %s", minWorktreeIdle)
	}
	_, err := r.reapWorktrees(ctx, reapOptions{repos: []string{repo}, base: base, idle: idle, apply: apply})
	return err
}

// worktreeLayer is the GC layer. It removes only in an explicit `forge
// storage gc --apply` (Runner.Explicit) under a policy that opts in
// (worktree_reap); otherwise it previews, whatever apply says.
func (r Runner) worktreeLayer(apply bool) error {
	repos := r.worktreeRepos()
	if len(repos) == 0 {
		return nil
	}
	remove := apply && r.Explicit && r.Policy.WorktreeReap
	switch {
	case !apply || remove:
	case !r.Policy.WorktreeReap:
		r.print("worktrees: preview only (set \"worktree_reap\": true in the storage policy, then run `forge storage gc --apply`, to remove)\n")
	default:
		r.print("worktrees: preview only (scheduled and automatic passes never remove worktrees; an explicit `forge storage gc --apply` does)\n")
	}
	_, err := r.reapWorktrees(r.hostCtx(), reapOptions{repos: repos, idle: minWorktreeIdle, apply: remove})
	return err
}

// PrintHeldWorktrees reports the worktrees the reaper will not touch, by
// reason, for `forge storage status`.
func (r Runner) PrintHeldWorktrees(ctx context.Context) error {
	repos := r.worktreeRepos()
	if len(repos) == 0 {
		return nil
	}
	quiet := r
	quiet.Out = nil
	report, err := quiet.reapWorktrees(ctx, reapOptions{repos: repos, idle: minWorktreeIdle})
	reasons := make([]string, 0)
	counts := report.HeldByReason()
	for reason := range counts {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	r.print("worktrees: %d removable, %d held\n", len(report.Removable), len(report.Held))
	for _, reason := range reasons {
		r.print("  held (%s): %d\n", reason, counts[reason])
		for _, h := range report.Held {
			if h.Reason == reason {
				r.print("    %s\n", h.Path)
			}
		}
	}
	if err != nil {
		r.print("worktrees: %v\n", err)
	}
	return nil
}

// worktreeRepos is every repository whose worktrees are in scope: the policy's
// recorded repos plus the git toplevel of each registered project.
func (r Runner) worktreeRepos() []string {
	seen := map[string]bool{}
	var repos []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			repos = append(repos, p)
		}
	}
	// Repos were filtered when recorded; a project under the temp dir is a
	// fixture or scaffold and is never a worktree owner.
	candidates := append([]string(nil), r.Policy.Repos...)
	for _, project := range r.Policy.Projects {
		if !underTempDir(project) {
			candidates = append(candidates, project)
		}
	}
	for _, dir := range candidates {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		if b, err := r.readGit(r.hostCtx(), dir, "rev-parse", "--show-toplevel"); err == nil {
			add(strings.TrimSpace(string(b)))
		}
	}
	return repos
}

func (r Runner) rebuildable() []string {
	if r.Policy.WorktreeRebuildable != nil {
		return r.Policy.WorktreeRebuildable
	}
	return defaultWorktreeRebuildable
}

type worktreeEntry struct {
	path, head       string
	locked, prunable bool
	// lockReason is the reason recorded with the lock, "" when none was.
	lockReason string
	bare       bool
}

func parseWorktrees(porcelain string) []worktreeEntry {
	var entries []worktreeEntry
	for _, block := range strings.Split(strings.TrimSpace(porcelain), "\n\n") {
		var e worktreeEntry
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				e.path = strings.TrimPrefix(line, "worktree ")
			case strings.HasPrefix(line, "HEAD "):
				e.head = strings.TrimPrefix(line, "HEAD ")
			case line == "locked" || strings.HasPrefix(line, "locked "):
				e.locked = true
				e.lockReason = strings.TrimPrefix(strings.TrimPrefix(line, "locked"), " ")
			case line == "prunable" || strings.HasPrefix(line, "prunable "):
				e.prunable = true
			case line == "bare":
				e.bare = true
			}
		}
		if e.path != "" {
			entries = append(entries, e)
		}
	}
	return entries
}

func (r Runner) reapWorktrees(ctx context.Context, o reapOptions) (WorktreeReport, error) {
	var report WorktreeReport
	var failures []error
	wd, _ := os.Getwd()
	self := openfiles.FromPaths([]string{wd})
	inUse := lazyOpenFiles{take: r.openSnapshot}
	seenCommon := map[string]bool{}

	for _, repo := range o.repos {
		if err := ctx.Err(); err != nil {
			return report, errors.Join(append(failures, err)...)
		}
		// One repository may be reached through several registered paths (a
		// project and its worktrees); classify its worktrees once. A common
		// dir that cannot be read leaves rules.common empty, which holds
		// every worktree of the repository as not provably linked.
		var common string
		if b, err := r.readGit(ctx, repo, "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil {
			common = canonicalPath(strings.TrimSpace(string(b)))
			if seenCommon[common] {
				continue
			}
			seenCommon[common] = true
		}
		if o.apply {
			// Drops only registrations whose directory is gone AND whose
			// admin index has been untouched for the idle floor: a worktree
			// on a volume that is briefly unmounted is not pruned while it is
			// in use, which would orphan its reflog and detached HEAD.
			expire := fmt.Sprintf("%d.seconds.ago", int(o.idle.Seconds()))
			if _, err := r.command(ctx, "git", "-C", repo, "worktree", "prune", "--expire", expire); err != nil {
				failures = append(failures, fmt.Errorf("worktree prune %s: %w", repo, err))
				continue
			}
		}
		b, err := r.readGit(ctx, repo, "worktree", "list", "--porcelain")
		if err != nil {
			failures = append(failures, fmt.Errorf("list worktrees of %s: %w", repo, err))
			continue
		}
		base := o.base
		if base == "" {
			base = r.defaultBase(ctx, repo)
		}
		entries := parseWorktrees(string(b))
		rules := worktreeRules{base: base, idle: o.idle, common: common, self: self}
		if len(entries) > 0 {
			rules.main = canonicalPath(entries[0].path)
			if !entries[0].bare {
				rules.source = rules.main
			}
		}
		for index, e := range entries {
			if index == 0 || e.bare || e.head == "" || e.prunable {
				continue
			}
			if strings.HasPrefix(filepath.Base(e.path), quarantinePrefix) {
				detail := "left by an interrupted cleanup; move it back with `git worktree move`"
				report.Held = append(report.Held, WorktreeHold{Path: e.path, Repo: repo, Reason: HoldQuarantined, Detail: detail})
				r.print("keep worktree %s: %s (%s)\n", e.path, HoldQuarantined, detail)
				continue
			}
			reason, detail, skip := r.classifyWorktree(ctx, e, rules, func(p string) (bool, error) { return inUse.holds(ctx, p) })
			if skip {
				continue
			}
			if reason != "" {
				report.Held = append(report.Held, WorktreeHold{Path: e.path, Repo: repo, Reason: reason, Detail: detail})
				r.print("keep worktree %s: %s%s\n", e.path, reason, suffix(detail))
				continue
			}
			report.Removable = append(report.Removable, e.path)
			if !o.apply {
				r.print("removable worktree: %s (%s)\n", e.path, e.head)
				continue
			}
			// Named before anything is touched, by its canonical path, so the
			// output says exactly what a removal is about to delete.
			r.print("removing worktree %s (repository %s, HEAD %s)\n", canonicalPath(e.path), repo, e.head)
			// The batch classification can be minutes old, and anything that
			// writes between a re-check and a remove would be deleted. So the
			// worktree is moved aside first (a path-based writer then gets
			// ENOENT), re-classified at the new path, and only then removed.
			reason, detail, removed := r.reclaimWorktree(ctx, repo, e, rules)
			if !removed {
				report.Removable = report.Removable[:len(report.Removable)-1]
				report.Held = append(report.Held, WorktreeHold{Path: e.path, Repo: repo, Reason: reason, Detail: detail})
				r.print("keep worktree %s: %s%s\n", e.path, reason, suffix(detail))
				continue
			}
			report.Removed = append(report.Removed, e.path)
			r.print("removed worktree: %s (%s)\n", e.path, e.head)
		}
	}
	if inUse.err != nil {
		failures = append(failures, inUse.err)
	}
	return report, errors.Join(failures...)
}

// worktreeRules is what decides a worktree beyond its own entry. The batch
// classification and the re-check before removal share it.
type worktreeRules struct {
	base string
	// idle is the activity floor. Zero skips the activity checks: that is the
	// re-check after the quarantine move, which itself rewrites the
	// worktree's .git file and admin directory (activity was judged on the
	// original path, just before the move).
	idle time.Duration
	// common is the repository's git common dir, canonical. A linked
	// worktree's .git file names <common>/worktrees/<id>.
	common string
	// main is the repository's main checkout, canonical.
	main string
	// source is main when it is a working tree an ignored file can be copied
	// from; "" for a bare repository.
	source string
	self   openfiles.Snapshot
}

// refuse holds what is not plainly a linked worktree this reaper may judge:
// the main checkout, a directory containing it or the home directory,
// anything whose .git is not a gitdir FILE naming one of this repository's
// worktree admin directories. These are
// identity checks, so they come before any question of age or cleanliness.
func (rules worktreeRules) refuse(path string) (reason, detail string) {
	canonical := canonicalPath(path)
	if rules.main != "" && (canonical == rules.main || within(canonical, rules.main)) {
		return HoldNotLinked, "it is, or contains, the repository's main checkout"
	}
	if home, err := os.UserHomeDir(); err == nil {
		if h := canonicalPath(home); canonical == h || within(canonical, h) {
			return HoldNotLinked, "it is, or contains, the home directory"
		}
	}
	info, err := os.Lstat(filepath.Join(path, ".git"))
	switch {
	case err != nil:
		return HoldNotLinked, "it has no .git file"
	case info.IsDir():
		return HoldNotLinked, ".git is a directory: a checkout of its own, not a linked worktree"
	case !info.Mode().IsRegular():
		return HoldNotLinked, ".git is not a regular file"
	}
	admin := worktreeAdminDir(path)
	if admin == "" || rules.common == "" || canonicalPath(filepath.Dir(admin)) != filepath.Join(rules.common, "worktrees") {
		return HoldNotLinked, fmt.Sprintf(".git names %q, not a worktree of this repository", admin)
	}
	return "", ""
}

// within reports whether child lies strictly inside parent. Both canonical.
func within(parent, child string) bool {
	return strings.HasPrefix(child, strings.TrimSuffix(parent, string(filepath.Separator))+string(filepath.Separator))
}

// canonicalPath is path made absolute with every symlink resolved, which is
// how git reports worktrees and how lsof reports open files. A path that does
// not resolve (it is gone) is only cleaned.
func canonicalPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

// classifyWorktree decides one linked worktree: "" reason means removable.
// skip means it is not a candidate at all (gone from disk).
func (r Runner) classifyWorktree(ctx context.Context, e worktreeEntry, rules worktreeRules, holds func(path string) (bool, error)) (reason, detail string, skip bool) {
	if e.locked {
		if strings.HasPrefix(e.lockReason, reliantLockPrefix) {
			return HoldManaged, "locked by Reliant (" + e.lockReason + "); Reliant reclaims its own worktrees", false
		}
		return HoldLocked, "", false
	}
	info, err := os.Stat(e.path)
	if err != nil {
		return "", "", true
	}
	if reason, detail := rules.refuse(e.path); reason != "" {
		return reason, detail, false
	}
	if rules.self.Holds(e.path) {
		return HoldInUse, "this process's working directory", false
	}
	if rules.idle > 0 {
		if since := time.Since(worktreeActivity(e.path, info)); since < rules.idle {
			return HoldActive, fmt.Sprintf("touched %s ago", since.Round(time.Minute)), false
		}
	}
	b, err := r.readGit(ctx, e.path, "status", "--porcelain", "-z", "--ignored", "--untracked-files=normal")
	if err != nil {
		return HoldUnchecked, err.Error(), false
	}
	dirty, data := r.classifyStatus(e.path, rules.source, string(b))
	if dirty {
		return HoldDirty, "", false
	}
	if len(data) > 0 {
		detail := strings.Join(data, ", ")
		if rules.source != "" {
			// Say how to free it: a copy identical to the main checkout's
			// would not hold the worktree.
			detail += " (not in " + rules.source + ", or different there)"
		}
		return HoldData, detail, false
	}
	// Edits to skip-worktree / assume-unchanged files are invisible to status
	// and to `git worktree remove`'s own check.
	b, err = r.readGit(ctx, e.path, "ls-files", "-v", "-z")
	if err != nil {
		return HoldUnchecked, err.Error(), false
	}
	if hidden := hiddenChanges(string(b)); len(hidden) > 0 {
		return HoldHidden, strings.Join(hidden, ", "), false
	}
	// One walk of the whole tree. Anything in it modified within the idle
	// floor is activity — a build writing node_modules or bin/ leaves the
	// git admin files and a clean status alone. And a nested repository
	// inside an allowlisted ignored dir (node_modules/…) would be deleted
	// with its unpushed commits.
	scan, err := scanTree(ctx, e.path, activityCutoff(rules.idle))
	if err != nil {
		return HoldUnchecked, err.Error(), false
	}
	if scan.recent != "" {
		return HoldActive, fmt.Sprintf("%s modified %s ago", scan.recent, scan.age.Round(time.Minute)), false
	}
	if nested := r.withoutToolClones(ctx, e.path, scan.nested); len(nested) > 0 {
		return HoldNested, strings.Join(nested, ", "), false
	}
	// Commits only this worktree can reach (its reflog, per-worktree refs,
	// detached HEAD) are garbage-collected once the worktree is removed.
	orphaned, err := r.unreachableTips(ctx, e)
	if err != nil {
		return HoldUnchecked, err.Error(), false
	}
	if orphaned {
		return HoldOrphans, "commits reachable only from this worktree's reflog, refs or detached HEAD", false
	}
	if !r.pushed(ctx, e.path, e.head, rules.base) {
		return HoldUnpushed, "", false
	}
	held, err := holds(e.path)
	if err != nil {
		return HoldUnchecked, "in-use state unknown", false
	}
	if held {
		return HoldInUse, "a running process uses it", false
	}
	return "", "", false
}

const quarantinePrefix = ".forge-reclaim-"

// reclaimWorktree removes a worktree through a quarantine move. removed is
// false when it was kept, with the reason; the worktree is then back at its
// original path, or — if the move back failed — left in quarantine, still a
// valid worktree and never deleted.
func (r Runner) reclaimWorktree(ctx context.Context, repo string, e worktreeEntry, rules worktreeRules) (reason, detail string, removed bool) {
	// The move rewrites the worktree's .git file and admin gitdir, which would
	// read as fresh activity, so idleness is judged here, on the original path,
	// and not again after the move.
	info, err := os.Stat(e.path)
	if err != nil {
		return HoldUnchecked, "disappeared during cleanup", false
	}
	if since := time.Since(worktreeActivity(e.path, info)); since < rules.idle {
		return HoldActive, fmt.Sprintf("touched %s ago", since.Round(time.Minute)), false
	}
	scan, err := scanTree(ctx, e.path, activityCutoff(rules.idle))
	if err != nil {
		return HoldUnchecked, err.Error(), false
	}
	if scan.recent != "" {
		return HoldActive, fmt.Sprintf("%s modified %s ago", scan.recent, scan.age.Round(time.Minute)), false
	}
	// Inside a maintenance pass, git's unlinks count against the pass's
	// delete budget like forge's own; a worktree that does not fit waits.
	if r.governor.pass != nil {
		if err := r.governor.reserve(scan.entries); err != nil {
			var backoff *BackedOffError
			if errors.As(err, &backoff) {
				return HoldUnchecked, err.Error(), false
			}
			return HoldDeferred, err.Error(), false
		}
	}
	q := filepath.Join(filepath.Dir(e.path), fmt.Sprintf("%s%s-%d", quarantinePrefix, filepath.Base(e.path), time.Now().Unix()))
	if _, err := os.Lstat(q); err == nil {
		return HoldRefused, q + " already exists", false
	}
	if _, err := r.command(ctx, "git", "-C", repo, "worktree", "move", e.path, q); err != nil {
		return HoldRefused, err.Error(), false
	}
	if r.afterQuarantine != nil {
		r.afterQuarantine(e.path, q)
	}
	moved := e
	moved.path = q
	recheck := rules
	recheck.idle = 0
	reason, detail = r.recheckWorktree(ctx, repo, moved, recheck)
	if reason == "" {
		// No --force: git refuses what it considers unsafe.
		if _, err := r.command(ctx, "git", "-C", repo, "worktree", "remove", q); err == nil {
			return "", "", true
		} else {
			reason, detail = HoldRefused, err.Error()
		}
	}
	// git moves a worktree INTO an existing destination directory, like mv,
	// which would nest it instead of failing; so an occupied path is a failure.
	var moveErr error
	if _, statErr := os.Lstat(e.path); statErr == nil {
		moveErr = fmt.Errorf("%s exists again", e.path)
	} else {
		_, moveErr = r.command(ctx, "git", "-C", repo, "worktree", "move", q, e.path)
	}
	if err := moveErr; err != nil {
		r.print("WARNING: worktree %s could not be moved back from quarantine %s: %v (it is still a valid worktree; nothing was deleted)\n", e.path, q, err)
		return HoldQuarantined, fmt.Sprintf("left at %s (%v); was kept because: %s", q, err, reason), false
	}
	return reason, detail, false
}

// maxOrphanTips bounds the argv handed to git; a worktree with more distinct
// commits in its reflog than this is held rather than guessed at.
const maxOrphanTips = 4000

// unreachableTips reports whether any commit this worktree alone can reach is
// absent from every branch, remote-tracking ref and tag.
func (r Runner) unreachableTips(ctx context.Context, e worktreeEntry) (bool, error) {
	tips := map[string]bool{e.head: true}
	if admin := worktreeAdminDir(e.path); admin != "" {
		if data, err := os.ReadFile(filepath.Join(admin, "logs", "HEAD")); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				// "<old-sha> <new-sha> <ident>\t<message>": the new-sha is the tip.
				if fields := strings.Fields(line); len(fields) >= 2 && !isZeroSHA(fields[1]) {
					tips[fields[1]] = true
				}
			}
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}
	b, err := r.readGit(ctx, e.path, "for-each-ref", "--format=%(objectname)", "refs/worktree", "refs/bisect")
	if err != nil {
		return false, err
	}
	for _, sha := range strings.Fields(string(b)) {
		tips[sha] = true
	}
	if len(tips) > maxOrphanTips {
		return false, fmt.Errorf("worktree reflog names more than %d distinct commits; cannot check for orphans", maxOrphanTips)
	}
	args := []string{"rev-list", "--max-count=1", "--missing=allow-any"}
	for sha := range tips {
		args = append(args, sha)
	}
	args = append(args, "--not", "--branches", "--remotes", "--tags")
	out, err := r.readGit(ctx, e.path, args...)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func isZeroSHA(sha string) bool { return strings.Trim(sha, "0") == "" }

// recheckWorktree re-reads the worktree's registration and re-runs the full
// classification with a fresh lsof snapshot. Any failure to re-check holds.
func (r Runner) recheckWorktree(ctx context.Context, repo string, e worktreeEntry, rules worktreeRules) (string, string) {
	b, err := r.readGit(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return HoldUnchecked, err.Error()
	}
	var fresh *worktreeEntry
	for _, cur := range parseWorktrees(string(b)) {
		if cur.path == e.path {
			c := cur
			fresh = &c
			break
		}
	}
	if fresh == nil || fresh.head == "" {
		return HoldUnchecked, "no longer registered"
	}
	// A fresh snapshot on purpose: this re-check exists to catch what changed
	// since the pass's snapshot, so it must not reuse it.
	var fresher lazyOpenFiles
	reason, detail, skip := r.classifyWorktree(ctx, *fresh, rules, func(p string) (bool, error) { return fresher.holds(ctx, p) })
	if skip {
		return HoldUnchecked, "disappeared during cleanup"
	}
	return reason, detail
}

// hiddenChanges lists files git was told to ignore changes to. -v tags: H
// tracked, S skip-worktree, lowercase = assume-unchanged (or both).
func hiddenChanges(lsFiles string) []string {
	var out []string
	for _, entry := range strings.Split(lsFiles, "\x00") {
		if len(entry) < 3 {
			continue
		}
		if tag := entry[0]; tag == 'S' || (tag >= 'a' && tag <= 'z') {
			out = append(out, entry[2:])
		}
	}
	return out
}

// maxNestedWalkEntries bounds the walk; a tree too large to inspect is held.
const maxNestedWalkEntries = 1_000_000

// activityCutoff is the modification time a worktree's contents must be older
// than; zero (no activity check) when idle is zero.
func activityCutoff(idle time.Duration) time.Time {
	if idle <= 0 {
		return time.Time{}
	}
	return time.Now().Add(-idle)
}

// treeScan is one walk of a worktree.
type treeScan struct {
	// nested lists every `.git` under the worktree other than its own.
	nested []string
	// recent is an entry modified after the cutoff (relative), and age how
	// long ago; the walk stops at the first one.
	recent string
	age    time.Duration
	// entries counts what the walk visited: what removing the tree unlinks.
	entries int
}

// scanTree walks the whole worktree, including allowlisted ignored dirs. It
// finds nested repositories (a submodule has a `.git` too, and is held with
// everything else — the safe direction) and, when cutoff is set, the first
// entry modified after it. The root directory and its .git file are left to
// worktreeActivity: a quarantine move rewrites both. An entry that vanishes
// mid-walk is activity too — something is deleting in there.
func scanTree(ctx context.Context, worktree string, cutoff time.Time) (treeScan, error) {
	var scan treeScan
	gitFile := filepath.Join(worktree, ".git")
	rel := func(p string) string { r, _ := filepath.Rel(worktree, p); return r }
	err := filepath.WalkDir(worktree, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if scan.entries++; scan.entries%4096 == 0 {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
		}
		if scan.entries > maxNestedWalkEntries {
			return fmt.Errorf("worktree has more than %d entries; cannot check it", maxNestedWalkEntries)
		}
		if !cutoff.IsZero() && p != worktree && p != gitFile {
			info, err := d.Info()
			if errors.Is(err, fs.ErrNotExist) {
				scan.recent = rel(p) + " (removed during the scan)"
				return filepath.SkipAll
			}
			if err != nil {
				return err
			}
			if info.ModTime().After(cutoff) {
				scan.recent, scan.age = rel(p), time.Since(info.ModTime())
				return filepath.SkipAll
			}
		}
		if d.Name() != ".git" || p == gitFile {
			return nil
		}
		scan.nested = append(scan.nested, rel(p))
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	return scan, err
}

// toolCloneDirs are directories, relative to a worktree's root, whose direct
// children are git clones a tool made and can make again from their remote.
// pre-commit clones each hook repository at a pinned rev into
// $PRE_COMMIT_HOME/repo*, and control-plane's CI gate points PRE_COMMIT_HOME at
// <checkout>/.tools/pre-commit. Any other repository inside a worktree stays a
// nested repository: its history may exist nowhere else.
var toolCloneDirs = []string{".tools/pre-commit"}

// withoutToolClones drops from nested (relative `.git` paths from scanTree)
// the clones a tool regenerates. A clone qualifies only when its `.git` is a
// directory, it is a direct child of a toolCloneDirs entry, AND the worktree's
// own git ignores it: a tracked or unignored clone is the user's.
func (r Runner) withoutToolClones(ctx context.Context, worktree string, nested []string) []string {
	var out []string
	for _, gitPath := range nested {
		if !r.toolClone(ctx, worktree, gitPath) {
			out = append(out, gitPath)
		}
	}
	return out
}

func (r Runner) toolClone(ctx context.Context, worktree, gitPath string) bool {
	segs := splitSegments(gitPath)
	if len(segs) < 2 || segs[len(segs)-1] != ".git" {
		return false
	}
	clone := segs[:len(segs)-1]
	listed := false
	for _, dir := range toolCloneDirs {
		want := splitSegments(dir)
		if len(clone) == len(want)+1 && strings.Join(clone[:len(want)], "/") == strings.Join(want, "/") {
			listed = true
		}
	}
	if !listed {
		return false
	}
	if info, err := os.Lstat(filepath.Join(worktree, filepath.FromSlash(gitPath))); err != nil || !info.IsDir() {
		return false
	}
	_, err := r.readGit(ctx, worktree, "check-ignore", "-q", "--", strings.Join(clone, "/"))
	return err == nil
}

// lazyOpenFiles answers "does a running process use this path?" from one
// open-files snapshot. lsof costs seconds, so the snapshot is taken on the
// first question — only once a worktree has otherwise earned removal — and
// never again; a failure to take it is kept, so every later question gets the
// same answer and the caller reports it once.
type lazyOpenFiles struct {
	snap *openfiles.Snapshot
	err  error
	// take supplies the snapshot; the pass's shared one in a GC pass.
	take func(context.Context) (openfiles.Snapshot, error)
}

func (l *lazyOpenFiles) holds(ctx context.Context, path string) (bool, error) {
	if l.snap == nil && l.err == nil {
		take := l.take
		if take == nil {
			take = openfiles.Take
		}
		s, err := take(ctx)
		if err != nil {
			l.err = fmt.Errorf("refusing worktree cleanup: cannot determine which worktrees are in use: %w", err)
		} else {
			l.snap = &s
		}
	}
	if l.err != nil {
		return false, l.err
	}
	return l.snap.Holds(path), nil
}

func suffix(detail string) string {
	if detail == "" {
		return ""
	}
	return " (" + detail + ")"
}

// defaultBase is origin/HEAD's target, else origin/main, else "".
func (r Runner) defaultBase(ctx context.Context, repo string) string {
	if b, err := r.readGit(ctx, repo, "symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if ref := strings.TrimSpace(string(b)); ref != "" {
			return ref
		}
	}
	if _, err := r.readGit(ctx, repo, "rev-parse", "--verify", "--quiet", "origin/main^{commit}"); err == nil {
		return "origin/main"
	}
	return ""
}

// pushed reports whether head is safe on a remote: reachable from some
// remote-tracking ref, or an ancestor of the default base.
func (r Runner) pushed(ctx context.Context, worktree, head, base string) bool {
	if b, err := r.readGit(ctx, worktree, "for-each-ref", "--contains", head, "refs/remotes"); err == nil && strings.TrimSpace(string(b)) != "" {
		return true
	}
	if base == "" {
		return false
	}
	_, err := r.readGit(ctx, worktree, "merge-base", "--is-ancestor", head, base)
	return err == nil
}

// classifyStatus splits `git status --porcelain -z --ignored` into whether
// anything is modified or untracked, and the ignored paths that are the user's
// data (see dataJudge). source is the repository's main checkout, "" if none.
func (r Runner) classifyStatus(worktree, source, porcelain string) (dirty bool, data []string) {
	judge := dataJudge{checkout: worktree, source: source, allow: r.rebuildable()}
	for _, entry := range strings.Split(porcelain, "\x00") {
		entry = strings.TrimRight(entry, "\n")
		if strings.TrimSpace(entry) == "" {
			continue
		}
		if !strings.HasPrefix(entry, "!! ") {
			dirty = true
			continue
		}
		data = append(data, judge.visit(strings.TrimPrefix(entry, "!! "))...)
	}
	return dirty, data
}

func splitSegments(p string) []string {
	p = strings.Trim(filepath.ToSlash(p), "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func matchesRun(segments []string, pattern string) bool {
	want := splitSegments(pattern)
	if len(want) == 0 {
		return false
	}
	last := len(segments) - len(want)
	if strings.HasPrefix(pattern, "/") {
		last = min(last, 0) // anchored: only a run starting at the root
	}
	for start := 0; start <= last; start++ {
		ok := true
		for i, w := range want {
			if m, err := path.Match(w, segments[start+i]); err != nil || !m {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// rebuildablePath reports whether an ignored path is recreated by a build.
// The never-list is checked first so a policy cannot make data deletable.
func rebuildablePath(p string, allow []string) bool {
	if neverRebuildablePath(p) {
		return false
	}
	segments := splitSegments(p)
	for _, pattern := range allow {
		if matchesRun(segments, pattern) {
			return true
		}
	}
	return false
}

// worktreeActivity is the newest sign of life in a worktree: its directory,
// and the index, HEAD and reflog in its git admin directory. HEAD commit age
// would call a long-lived branch someone is actively editing "old".
func worktreeActivity(worktree string, info os.FileInfo) time.Time {
	newest := info.ModTime()
	admin := worktreeAdminDir(worktree)
	if admin == "" {
		return newest
	}
	for _, name := range []string{"index", "HEAD", filepath.Join("logs", "HEAD")} {
		if fi, err := os.Stat(filepath.Join(admin, name)); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	return newest
}

// worktreeAdminDir reads the `.git` file of a linked worktree.
func worktreeAdminDir(worktree string) string {
	b, err := os.ReadFile(filepath.Join(worktree, ".git"))
	if err != nil {
		return ""
	}
	dir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
	if dir != "" && !filepath.IsAbs(dir) {
		dir = filepath.Join(worktree, dir)
	}
	return dir
}

// readGit runs a read-only git command. --no-optional-locks matters: `git
// status` otherwise rewrites <admin>/index when its stat cache is stale, which
// would reset the very idle clock worktreeActivity reads.
func (r Runner) readGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return r.command(ctx, "git", append([]string{"--no-optional-locks", "-C", dir}, args...)...)
}
