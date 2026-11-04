// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// CwdSnapshot lists the working directory of every live process. It must fail
// closed: an error means "could not tell", and the caller then holds every
// worktree rather than guessing nothing is in use.
type CwdSnapshot func(ctx context.Context) ([]string, error)

const (
	// DefaultIdleFloor is how long an archived worktree must be untouched before
	// it is removed automatically. A confirmed clean-up has no floor.
	DefaultIdleFloor = time.Hour
	// DefaultActiveIdleFloor is how long an ACTIVE worktree must be untouched
	// before it may be reported finished (OutcomeIdleDone).
	DefaultActiveIdleFloor = 24 * time.Hour
	// DefaultBudget stays under the server's command timeout so the reply
	// always arrives.
	DefaultBudget = 90 * time.Second

	cwdCacheTTL = 30 * time.Second
	sizeTTL     = time.Hour
)

// Reclaimer settles worktrees on this machine. Its methods serialize per
// worktree path, so a reconcile and a clean-up never act on one directory at
// once.
type Reclaimer struct {
	// Root is the only directory tree it will ever remove from
	// (<home>/.reliant/worktrees). A path outside it is refused, and a missing
	// Root means the volume is not there, not that its worktrees are gone.
	Root string
	// Cwds reports process working directories; nil means ProcessCwds.
	Cwds CwdSnapshot
	// IdleFloor applies to automatic removal only.
	IdleFloor time.Duration
	// ActiveIdleFloor is the quiet time before an active worktree is reported
	// finished; zero means DefaultActiveIdleFloor.
	ActiveIdleFloor time.Duration
	// Budget bounds one request: worktrees not reached when it is spent come
	// back OutcomeDeferred, untouched, so a machine with many large archived
	// worktrees makes progress each pass instead of timing out every one.
	Budget time.Duration
	// RootFilesCap bounds the total size of the files loose at a workspace
	// root that removal saves (rootfiles.go) rather than holds the workspace
	// for; zero means DefaultRootFilesCap.
	RootFilesCap int64
	// Workers bounds how many worktrees one request settles at once; zero
	// means the default (reconcileWorkers).
	Workers int
	// ProbeBudget bounds one "is it finished" probe (ProbeIdle), apart from
	// the locking that precedes it; zero means DefaultProbeBudget. A probe that
	// runs out of it is OutcomeProbeInconclusive, not a "no".
	ProbeBudget time.Duration
	// Acquire excludes new runtime work through the final checks and shedding.
	// Missing admission or process inspection fails closed: nothing is shed.
	Acquire func(context.Context, []string) (RecoveryLease, error)
	// ProcessUse checks executables, open and mapped files, not only cwd.
	ProcessUse func(ctx context.Context, paths []string) error
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// PRMerged and FetchBase are the network checks behind "its work was merged"
	// (see mergeProbe). NewReclaimer sets them; nil skips the check, so a
	// Reclaimer built as a literal decides from local refs alone.
	PRMerged  func(ctx context.Context, checkout, branch, head string) bool
	FetchBase func(ctx context.Context, checkout, base string)
	// RepairInterval is how often settle re-runs crash repair (RepairRepo) on
	// a parent repository before reading it; 0 means DefaultRepairInterval,
	// negative disables it.
	RepairInterval time.Duration
	// OnRepair receives every repair report that changed something or found
	// damage; the daemon logs it. Nil discards them.
	OnRepair func(RepairReport)

	mu       sync.Mutex
	netCache map[string]netEntry
	sizes    map[string]cachedSize
	paths    map[string]pathLock
	cwdCache []string
	cwdAt    time.Time
	repairs  repairState
	memo     *commitMemo
}

type cachedSize struct {
	bytes int64
	at    time.Time
}

// NewReclaimer returns a Reclaimer rooted at root.
func NewReclaimer(root string) *Reclaimer {
	return &Reclaimer{Root: root, Cwds: ProcessCwds, IdleFloor: DefaultIdleFloor, Budget: DefaultBudget,
		PRMerged: GitHubPRMerged, FetchBase: FetchBaseBranch, ProcessUse: CheckProcessUse}
}

// DefaultRoot is where reliant creates worktrees on this machine.
func DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".reliant", "worktrees"), nil
}

// netEntry is one remembered answer from the network checks.
type netEntry struct {
	at  time.Time
	yes bool
}

// netTTL is how long a network answer is reused. A sweep asks about every
// checkout of every worktree; the three repos of a workspace share one origin,
// and most worktrees share a base branch, so without this a pass paid for the
// same `git fetch` and `gh pr list` dozens of times (up to 45s each).
const netTTL = 10 * time.Minute

// probe returns the network checks, memoized: a fetch of one origin's base
// branch runs once per netTTL however many checkouts ask, and a merged-PR
// answer is kept for the same (branch, head). A "merged" answer can never
// become false for the same head, so it is kept for good.
func (r *Reclaimer) probe() mergeProbe {
	r.mu.Lock()
	if r.memo == nil {
		r.memo = &commitMemo{now: r.now}
	}
	p := mergeProbe{memo: r.memo}
	r.mu.Unlock()
	if r.PRMerged != nil {
		p.prMerged = func(ctx context.Context, checkout, branch, head string) bool {
			key := "pr\x00" + commonDirKey(ctx, checkout) + "\x00" + branch + "\x00" + head
			if e, ok := r.netGet(key); ok && (e.yes || r.now().Sub(e.at) < netTTL) {
				return e.yes
			}
			yes := r.PRMerged(ctx, checkout, branch, head)
			r.netPut(key, yes)
			return yes
		}
	}
	if r.FetchBase != nil {
		p.fetchBase = func(ctx context.Context, checkout, base string) {
			key := "fetch\x00" + commonDirKey(ctx, checkout) + "\x00" + base
			if e, ok := r.netGet(key); ok && r.now().Sub(e.at) < netTTL {
				return
			}
			r.FetchBase(ctx, checkout, base)
			r.netPut(key, true)
		}
	}
	return p
}

func (r *Reclaimer) netGet(key string) (netEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.netCache[key]
	return e, ok
}

func (r *Reclaimer) netPut(key string, yes bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.netCache == nil {
		r.netCache = map[string]netEntry{}
	}
	r.netCache[key] = netEntry{at: r.now(), yes: yes}
}

// commonDirKey names the repository a checkout belongs to, so every worktree of
// one clone shares a cache entry. Falls back to the checkout itself.
func commonDirKey(ctx context.Context, checkout string) string {
	if d, err := commonDir(ctx, checkout); err == nil && d != "" {
		return d
	}
	return checkout
}

func (r *Reclaimer) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// pathLock returns the lock for one worktree path.
func (r *Reclaimer) pathLock(path string) pathLock {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paths == nil {
		r.paths = map[string]pathLock{}
	}
	key := resolvePath(path)
	m := r.paths[key]
	if m == nil {
		m = newPathLock()
		r.paths[key] = m
	}
	return m
}

// Reconcile settles every worktree in the request: active ones are locked as
// reliant-owned; archived ones are claimed for removal when provably safe (or
// removed, when the request is the second phase) and otherwise reported held.
// It never snapshots.
func (r *Reclaimer) Reconcile(ctx context.Context, req Request) Response {
	n := len(req.Worktrees)
	deadline := r.deadline()
	var sctx context.Context
	var cancel context.CancelFunc
	if deadline.IsZero() {
		sctx, cancel = context.WithCancel(ctx)
	} else {
		sctx, cancel = context.WithDeadline(ctx, deadline)
	}
	// Cancelling on return is what stops work still running for a worktree
	// whose answer was given up on at the deadline.
	defer cancel()

	// Worktrees settle concurrently: each is dominated by git and filesystem
	// waits, and run one after another a single slow one (a big tree, a stalled
	// network probe) used up the budget that the nine behind it needed. Every
	// per-path and per-row lock still serializes work on the same worktree.
	//
	// The reply is owed at the deadline whatever the workers are doing: waiting
	// for them is what turned one wedged tree into a reply the server had given
	// up on, and a re-send of the same rows while this one still worked.
	type settled struct {
		i   int
		res Result
	}
	finished := make(chan settled, n) // buffered: a worker that outlives the reply never blocks
	started := make([]atomic.Bool, n)
	jobs := make(chan int, n)
	for i := range n {
		jobs <- i
	}
	close(jobs)
	workers := r.Workers
	if workers <= 0 {
		workers = reconcileWorkers
	}
	for range min(workers, max(n, 1)) {
		go func() {
			for i := range jobs {
				if sctx.Err() != nil {
					finished <- settled{i, Result{ID: req.Worktrees[i].ID, Outcome: OutcomeDeferred, Detail: DetailNotReached}}
					continue
				}
				started[i].Store(true)
				finished <- settled{i, r.settleWithin(sctx, req.Worktrees[i], deadline)}
			}
		}()
	}

	results := make([]Result, n)
	got := make([]bool, n)
	take := func(s settled) { results[s.i], got[s.i] = s.res, true }
collect:
	for remaining := n; remaining > 0; remaining-- {
		select {
		case s := <-finished:
			take(s)
		case <-sctx.Done():
			for {
				select {
				case s := <-finished:
					take(s)
				default:
					break collect
				}
			}
		}
	}
	for i, w := range req.Worktrees {
		switch {
		case got[i]:
		case started[i].Load():
			results[i] = Result{ID: w.ID, Outcome: OutcomeDeferred, Detail: DetailTimedOut}
		default:
			results[i] = Result{ID: w.ID, Outcome: OutcomeDeferred, Detail: DetailNotReached}
		}
	}
	return Response{Results: results}
}

// DetailTimedOut marks a deferred result whose work was started and then
// stopped by the request's deadline, as opposed to never started.
const DetailTimedOut = "out of time while checking it"

// DetailNotReached marks a deferred result whose work never started: the
// request's time ran out before the daemon got to it.
const DetailNotReached = "out of time this pass"

// reconcileWorkers bounds how many worktrees one request settles at once.
const reconcileWorkers = 4

// settleWithin settles one worktree under the request's deadline. A worktree
// not reached by then is deferred untouched. One that is stopped part-way
// reports what it had finished only when that is final (removed, claimed,
// locked, gone); anything else is deferred as timed out, because a check
// cancelled by the deadline failed for want of time, not because the worktree
// is unsafe.
func (r *Reclaimer) settleWithin(ctx context.Context, w Worktree, deadline time.Time) Result {
	if !deadline.IsZero() && time.Now().After(deadline) {
		return Result{ID: w.ID, Outcome: OutcomeDeferred, Detail: DetailNotReached}
	}
	res := r.settle(ctx, w, false)
	if ctx.Err() != nil {
		switch res.Outcome {
		case OutcomeRemoved, OutcomeClaimed, OutcomeLocked, OutcomeGone, OutcomeIdleDone, OutcomeProbeInconclusive, OutcomeForeign:
		default:
			return Result{ID: w.ID, Outcome: OutcomeDeferred, Detail: DetailTimedOut}
		}
	}
	return res
}

// SnapshotRemove is the user-confirmed path for worktrees Reconcile held. In
// the first phase it saves the work of every checkout that is not already
// preserved to a local ref, proves the saved tree equals the files, and claims
// the worktree; in the second it removes it, but only if the files still hash
// to what was saved. A worktree that is in use, holds data, holds files outside
// its checkouts, contains another repository, or whose save cannot be verified
// is left exactly as it was.
func (r *Reclaimer) SnapshotRemove(ctx context.Context, req Request) Response {
	resp := Response{}
	deadline := r.deadline()
	for _, w := range req.Worktrees {
		if !deadline.IsZero() && time.Now().After(deadline) {
			resp.Results = append(resp.Results, Result{ID: w.ID, Outcome: OutcomeDeferred, Detail: DetailNotReached})
			continue
		}
		resp.Results = append(resp.Results, r.settle(ctx, w, true))
	}
	return resp
}

func (r *Reclaimer) deadline() time.Time {
	if r.Budget <= 0 {
		return time.Time{}
	}
	return time.Now().Add(r.Budget)
}

func (r *Reclaimer) cwds(ctx context.Context, fresh bool) ([]string, error) {
	fn := r.Cwds
	if fn == nil {
		fn = ProcessCwds
	}
	r.mu.Lock()
	if !fresh && !r.cwdAt.IsZero() && r.now().Sub(r.cwdAt) < cwdCacheTTL {
		c := r.cwdCache
		r.mu.Unlock()
		return c, nil
	}
	r.mu.Unlock()
	c, err := fn(ctx)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.cwdCache, r.cwdAt = c, r.now()
	r.mu.Unlock()
	return c, nil
}

func held(w Worktree, reason HeldReason, detail string) Result {
	return Result{ID: w.ID, Outcome: OutcomeHeld, Reason: reason, Detail: detail}
}

// rootOK reports whether the worktree root exists as a directory. A missing
// root is an unmounted volume or a fresh pod, not evidence that anything is
// gone.
func (r *Reclaimer) rootOK() bool {
	fi, err := os.Stat(r.Root)
	return err == nil && fi.IsDir()
}

func (r *Reclaimer) settle(ctx context.Context, w Worktree, confirmed bool) Result {
	res := Result{ID: w.ID}
	if w.Path == "" || !within(r.Root, w.Path) || !r.rootOK() {
		res.Outcome, res.Detail = OutcomeForeign, "path is outside this machine's worktree root, or the root is not available"
		return res
	}
	// Both locks give up when ctx does: a worktree another request is still
	// working on is deferred, never waited for past the deadline.
	m := r.pathLock(w.Path)
	if m.lock(ctx) != nil {
		return Result{ID: w.ID, Outcome: OutcomeDeferred, Detail: DetailTimedOut}
	}
	defer m.unlock()
	// The path lock serializes this process; the row's file lock serializes
	// every other process sharing this root (two daemons on one $HOME).
	unlock, err := r.rowLock(ctx, w.ID)
	if err != nil {
		if ctx.Err() != nil {
			return Result{ID: w.ID, Outcome: OutcomeDeferred, Detail: DetailTimedOut}
		}
		return Result{ID: w.ID, Outcome: OutcomeError, Error: "could not lock the row: " + err.Error()}
	}
	defer unlock()

	// An ACTIVE request is a restore: bring parked checkouts home first, so the
	// directory exists before it is locked. Whatever cannot come home keeps the
	// row held, never "locked".
	if w.State == StateActive {
		if _, parked := r.recoverLocked(ctx, w.ID); len(parked) > 0 {
			return r.heldQuarantined(w, parked, "its original path is not free, or it could not be moved back; free the path and retry")
		}
	}

	// Crash damage in the parent repositories (empty objects, a torn index)
	// makes every git answer below an error; repair it before asking.
	r.ensureRepaired(ctx, w)

	// A row with checkouts parked under .reclaim is never "gone": the files
	// exist, in quarantine, still locked. Say so before anything else.
	if parked := r.quarantined(w.ID); len(parked) > 0 && w.State == StateArchived {
		return r.heldQuarantined(w, parked, "")
	}
	if _, err := os.Lstat(w.Path); os.IsNotExist(err) {
		if parked := r.quarantined(w.ID); len(parked) > 0 {
			return r.heldQuarantined(w, parked, "")
		}
		pruneRegistrations(ctx, w)
		res.Outcome = OutcomeGone
		for _, c := range w.Checkouts {
			res.SnapshotRefs = append(res.SnapshotRefs, existingSnapshotRefs(ctx, c.RepoPath, w.ID)...)
		}
		return res
	}
	if w.State == StateActive {
		return r.lockActive(ctx, w)
	}
	if w.State != StateArchived || w.Fence == "" {
		return held(w, ReasonUnverified, "the request carried no archive fence")
	}
	if confirmed {
		return r.confirmedPhase(ctx, w)
	}
	return r.autoPhase(ctx, w)
}

// ---- shared checks ------------------------------------------------------

// inUse reports whether a live process works inside the worktree. It fails
// closed.
//
// A check that need not be fresh may answer "not in use" from the cached
// listing, but never "in use": a cached hit is confirmed against a fresh one.
// The cache lives cwdCacheTTL across requests, so a hit can be a process
// that has long exited — a user's finished `git commit`, or this daemon's
// own git in the checkout for an earlier request — and it held an idle
// worktree as in use on that alone.
func (r *Reclaimer) inUse(ctx context.Context, w Worktree, fresh bool) *Result {
	if selfWithin(w.Path) {
		res := held(w, ReasonInUse, "the daemon itself runs from here")
		return &res
	}
	cwds, err := r.cwds(ctx, fresh)
	if err == nil && !fresh && cwdWithin(cwds, w.Path) {
		cwds, err = r.cwds(ctx, true)
	}
	if err != nil {
		res := held(w, ReasonUnverified, "could not list running processes: "+err.Error())
		return &res
	}
	if cwdWithin(cwds, w.Path) {
		res := held(w, ReasonInUse, "a running process has its working directory here")
		return &res
	}
	return nil
}

// cwdWithin reports whether any of cwds is path or under it.
func cwdWithin(cwds []string, path string) bool {
	for _, cwd := range cwds {
		if samePath(cwd, path) || within(path, cwd) {
			return true
		}
	}
	return false
}

// idleSince combines directory and admin HEAD mtimes with actual reflog event
// times. Index refreshes and reflog maintenance rewrites are not user activity.
// An uncertain reflog must not make a checkout look ancient.
func idleSince(ctx context.Context, path string, checkouts []string) (time.Time, error) {
	newest := time.Time{}
	bumpTime := func(t time.Time) {
		if t.After(newest) {
			newest = t
		}
	}
	bump := func(p string) {
		if fi, err := os.Stat(p); err == nil {
			bumpTime(fi.ModTime())
		}
	}
	bump(path)
	for _, c := range checkouts {
		if err := ctx.Err(); err != nil {
			return time.Time{}, err
		}
		bump(c)
		admin := adminDir(c)
		if admin == "" {
			continue
		}
		bump(filepath.Join(admin, "HEAD"))
		event, err := reflogActivity(ctx, filepath.Join(admin, "logs", "HEAD"))
		if err != nil {
			return time.Time{}, err
		}
		bumpTime(event)
	}
	return newest, ctx.Err()
}

func adminDir(checkout string) string {
	b, err := os.ReadFile(filepath.Join(checkout, ".git"))
	if err != nil {
		return ""
	}
	dir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
	if dir != "" && !filepath.IsAbs(dir) {
		dir = filepath.Join(checkout, dir)
	}
	return dir
}

// structure checks everything that makes a workspace not the daemon's to
// remove, whatever its git state: another repository inside it, files outside
// its checkouts, a lock or checkout that is not this row's, ignored data.
// It returns the checkouts when the workspace is structurally removable, and
// the scan either way.
func (r *Reclaimer) structure(ctx context.Context, w Worktree, phase2 bool) ([]string, scan, *Result) {
	sc := scanWorkspace(ctx, w.Path)
	checkouts, hold := structureOf(ctx, w, sc, phase2, r.rootFilesCap())
	return checkouts, sc, hold
}

func structureOf(ctx context.Context, w Worktree, sc scan, phase2 bool, rootCap int64) ([]string, *Result) {
	if sc.Truncated {
		res := held(w, ReasonUnverified, "the workspace is too large to inspect completely")
		return nil, &res
	}
	if len(sc.Repos) == 0 {
		if len(sc.Outside) == 0 {
			return nil, nil // an empty husk: caller removes it
		}
		res := held(w, ReasonUnverified, "no git checkout found under the workspace")
		return nil, &res
	}
	var checkouts []string
	for _, repo := range sc.Repos {
		if !isLinkedWorktree(repo) {
			if samePath(repo, w.Path) {
				res := held(w, ReasonUnmanaged, repo+" is a main checkout, not a linked worktree")
				return nil, &res
			}
			res := held(w, ReasonNestedRepo, relDetail(w.Path, repo, "contains a git repository of its own (its history is not in this worktree)"))
			return nil, &res
		}
		info, err := inspectLock(ctx, repo)
		if err != nil {
			res := held(w, ReasonUnmanaged, "cannot read git worktree state: "+err.Error())
			return nil, &res
		}
		if !info.Registered {
			res := held(w, ReasonUnmanaged, repo+" is not a registered git worktree")
			return nil, &res
		}
		if info.Locked {
			p, ok := parseLock(info.Reason)
			switch {
			case !ok || p.ID != w.ID:
				res := held(w, ReasonUnmanaged, fmt.Sprintf("%s is locked for something else (%q)", repo, info.Reason))
				return nil, &res
			case phase2 && info.Reason != ArchivedLockReason(w.ID, w.Fence):
				res := held(w, ReasonStale, "the worktree was restored or archived again since it was claimed")
				return nil, &res
			}
		} else if phase2 {
			res := held(w, ReasonStale, repo+" is no longer claimed for removal")
			return nil, &res
		}
		checkouts = append(checkouts, repo)
	}
	if len(sc.Outside) > 0 {
		if why := rootFilesUnsavable(sc.Outside, rootCap); why != "" {
			shown := relDetail(w.Path, sc.Outside[0], "")
			res := held(w, ReasonOutsideFiles, fmt.Sprintf("%d file(s) sit outside every checkout, e.g. %s (%s)", len(sc.Outside), strings.TrimPrefix(shown, ": "), why))
			return nil, &res
		}
	}
	for _, c := range checkouts {
		if v := dataHold(ctx, c, nestedOf(c, checkouts)); !v.safe() {
			res := held(w, v.Reason, relDetail(w.Path, c, v.Detail))
			return nil, &res
		}
	}
	return checkouts, nil
}

// ---- automatic path -----------------------------------------------------

func (r *Reclaimer) autoPhase(ctx context.Context, w Worktree) Result {
	if w.Remove {
		return r.autoRemove(ctx, w)
	}
	checkouts, _, hold := r.structure(ctx, w, false)
	if hold != nil {
		r.shedHeld(ctx, w, hold)
		r.measure(ctx, hold, w.Path)
		return *hold
	}
	if checkouts == nil { // empty husk
		if removeEmptyTree(w.Path) {
			return Result{ID: w.ID, Outcome: OutcomeRemoved}
		}
		return held(w, ReasonUnverified, "no git checkout found under the workspace")
	}
	if r.IdleFloor > 0 {
		last, err := idleSince(ctx, w.Path, checkouts)
		if err != nil {
			return held(w, ReasonUnverified, "could not determine quiet time: "+err.Error())
		}
		if age := r.now().Sub(last); age < r.IdleFloor {
			return held(w, ReasonRecentlyActive, fmt.Sprintf("touched %s ago", age.Round(time.Minute)))
		}
	}
	if hold := r.inUse(ctx, w, false); hold != nil {
		r.measure(ctx, hold, w.Path)
		return *hold
	}
	v := classifyAll(ctx, w, checkouts, r.probe())
	// A local ref is not a durable receipt. Dirty/private-history work is held
	// for the separate Git recovery protocol, never upgraded to safe by a local
	// saveCommits call. Explicit manual snapshots retain their existing path.
	var saved []string
	if !v.safe() {
		res := held(w, v.Reason, v.Detail)
		res.SnapshotRefs = saved
		r.shedHeld(ctx, w, &res)
		r.measure(ctx, &res, w.Path)
		return res
	}
	if w.KeepFiles {
		// Safe to remove, and the user chose to keep it. Report it as held so
		// it shows in the Inbox (where the confirmed Clean up can remove it) and
		// backs off like any other hold.
		r.keepLocked(ctx, w, checkouts)
		res := held(w, ReasonKept, `kept because your archive setting is "Keep everything"`)
		r.measure(ctx, &res, w.Path)
		return res
	}
	if res := claim(ctx, w, checkouts); res != nil {
		res.SnapshotRefs = saved
		return *res
	}
	return Result{ID: w.ID, Outcome: OutcomeClaimed, SnapshotRefs: saved}
}

func (r *Reclaimer) autoRemove(ctx context.Context, w Worktree) Result {
	checkouts, sc, hold := r.structure(ctx, w, true)
	if hold != nil {
		return *hold
	}
	if checkouts == nil {
		return held(w, ReasonStale, "nothing is claimed for removal")
	}
	if r.IdleFloor > 0 {
		last, err := idleSince(ctx, w.Path, checkouts)
		if err != nil {
			return held(w, ReasonUnverified, "could not determine quiet time: "+err.Error())
		}
		if age := r.now().Sub(last); age < r.IdleFloor {
			return held(w, ReasonRecentlyActive, fmt.Sprintf("touched %s ago", age.Round(time.Minute)))
		}
	}
	// A fresh process list, immediately before removal.
	if hold := r.inUse(ctx, w, true); hold != nil {
		return *hold
	}
	if v := classifyAll(ctx, w, checkouts, r.probe()); !v.safe() {
		return held(w, v.Reason, v.Detail)
	}
	return r.retireWorkspace(ctx, w, checkouts, sc.Outside)
}

// classifyAll classifies every checkout of a workspace, concurrently: each is
// a handful of independent git commands, and the checkouts of one workspace
// are separate repositories, so a slow one no longer queues the others behind
// it. The worst verdict wins, and ties keep checkout order.
func classifyAll(ctx context.Context, w Worktree, checkouts []string, probe mergeProbe) verdict {
	verdicts := make([]verdict, len(checkouts))
	var wg sync.WaitGroup
	for i, c := range checkouts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			verdicts[i] = classifyCheckout(ctx, c, baseFor(w, c), nestedOf(c, checkouts), w.ID, probe)
		}()
	}
	wg.Wait()
	worst := verdict{}
	for i, v := range verdicts {
		if !v.safe() {
			worst = worse(worst, verdict{v.Reason, relDetail(w.Path, checkouts[i], v.Detail)})
		}
	}
	return worst
}

// claim writes the archive fence into every checkout's lock.
func claim(ctx context.Context, w Worktree, checkouts []string) *Result {
	for _, c := range checkouts {
		info, err := inspectLock(ctx, c)
		if err != nil {
			res := held(w, ReasonUnverified, err.Error())
			return &res
		}
		if reason, detail := claimArchived(ctx, info, c, w.ID, w.Fence); reason != "" {
			res := held(w, reason, relDetail(w.Path, c, detail))
			return &res
		}
	}
	return nil
}

func (r *Reclaimer) keepLocked(ctx context.Context, w Worktree, checkouts []string) {
	for _, c := range checkouts {
		_ = LockCheckout(ctx, c, w.ID)
	}
}

// ---- confirmed path -----------------------------------------------------

func (r *Reclaimer) confirmedPhase(ctx context.Context, w Worktree) Result {
	if w.Remove {
		return r.confirmedRemove(ctx, w)
	}
	checkouts, _, hold := r.structure(ctx, w, false)
	if hold != nil {
		return *hold
	}
	if checkouts == nil {
		if removeEmptyTree(w.Path) {
			return Result{ID: w.ID, Outcome: OutcomeRemoved}
		}
		return held(w, ReasonUnverified, "no git checkout found under the workspace")
	}
	if hold := r.inUse(ctx, w, true); hold != nil {
		return *hold
	}

	res := Result{ID: w.ID, Trees: map[string]string{}}
	for _, c := range checkouts {
		nested := nestedOf(c, checkouts)
		repo := commonDirOf(ctx, c)
		v := classifyCheckout(ctx, c, baseFor(w, c), nested, w.ID, r.probe())
		switch v.Reason {
		case "", ReasonDirty, ReasonUnpushed, ReasonUnreachable:
		default:
			return held(w, v.Reason, relDetail(w.Path, c, v.Detail))
		}

		// Commits only this worktree's own state reaches are saved as refs
		// before anything else, so they survive its admin directory.
		tips, err := orphanTips(ctx, c, w.ID)
		if err != nil {
			return held(w, ReasonUnverified, relDetail(w.Path, c, err.Error()))
		}
		for i, tip := range tips {
			ref, err := createRef(ctx, c, snapshotRefDir(w.ID, relTo(w.Path, c))+"orphans/"+strconv.Itoa(i)+"-", tip)
			if err != nil {
				res.Outcome, res.Error = OutcomeError, "could not save unreachable history, nothing was removed: "+err.Error()
				res.SnapshotRefs = existingSnapshotRefs(ctx, repo, w.ID)
				return res
			}
			res.SnapshotRefs = append(res.SnapshotRefs, ref)
		}

		if v.safe() || v.Reason == ReasonUnreachable {
			continue // nothing in the files to save
		}
		ref, tree, err := Snapshot(ctx, c, snapshotRefDir(w.ID, relTo(w.Path, c)), w.ID, nested)
		switch {
		case errors.Is(err, errTooLarge):
			return held(w, ReasonTooLarge, relDetail(w.Path, c, err.Error()))
		case err != nil:
			res.Outcome, res.Error = OutcomeError, "snapshot failed, nothing was removed: "+err.Error()
			res.SnapshotRefs = existingSnapshotRefs(ctx, repo, w.ID)
			return res
		}
		res.SnapshotRefs = append(res.SnapshotRefs, ref)
		res.Trees[relTo(w.Path, c)] = tree
	}
	if hold := claim(ctx, w, checkouts); hold != nil {
		return *hold
	}
	res.Outcome = OutcomeClaimed
	return res
}

func (r *Reclaimer) confirmedRemove(ctx context.Context, w Worktree) Result {
	checkouts, sc, hold := r.structure(ctx, w, true)
	if hold != nil {
		return *hold
	}
	if checkouts == nil {
		return held(w, ReasonStale, "nothing is claimed for removal")
	}
	// A fresh process check BEFORE the quarantine move: whatever a process does
	// up to here lands at the original path, where the checks below can see it.
	if hold := r.inUse(ctx, w, true); hold != nil {
		return *hold
	}
	return r.removeWorkspace(ctx, w, checkouts, sc.Outside, true)
}

// ---- removal ------------------------------------------------------------

// removeWorkspace removes each checkout, one at a time, and only after
// quarantining it. For each checkout, immediately before its own removal:
//
//  1. the checkout is moved (git worktree move) to a sibling quarantine path
//     under <root>/.reclaim, unlocked just for the move and re-locked with the
//     same fence, so a writer that holds the old path gets ENOENT instead of
//     writing into a directory that is about to be deleted;
//  2. everything is re-checked on the QUARANTINED copy: no data, no nested
//     repository, a fresh process check, the full classification, and (on the
//     confirmed path) the saved snapshot tree;
//  3. only if all of that still holds is it removed, through git, with --force
//     only on the confirmed path, where the re-hash just proved it equals the
//     saved snapshot. On any change it is moved back and the worktree held.
//
// Then only directories left EMPTY are deleted, bottom-up.
func (r *Reclaimer) removeWorkspace(ctx context.Context, w Worktree, checkouts, outside []string, confirmed bool) Result {
	// Files loose at the workspace root are saved first, and verified, so a
	// failure here leaves every checkout exactly as it was.
	saved, err := r.saveRootFiles(w, outside)
	if err != nil {
		return held(w, ReasonOutsideFiles, "could not save the files at the workspace root, nothing was removed: "+err.Error())
	}
	ordered := append([]string(nil), checkouts...)
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	remaining := append([]string(nil), checkouts...)
	for _, c := range ordered {
		if res := r.removeOne(ctx, w, c, remaining, confirmed); res != nil {
			return *res
		}
		remaining = without(remaining, c)
	}
	saved.removeOriginals()
	if !removeEmptyDirs(emptyDirsUnder(w.Path)) {
		return Result{ID: w.ID, Outcome: OutcomeError, Error: "checkouts were removed but files remain at " + w.Path, ReclaimedTo: saved.dir}
	}
	// The row's quarantine directory is empty once every checkout is gone.
	_ = os.Remove(r.quarantineRoot(w.ID))
	return Result{ID: w.ID, Outcome: OutcomeRemoved, ReclaimedTo: saved.dir}
}

func without(list []string, drop string) []string {
	out := list[:0:0]
	for _, x := range list {
		if x != drop {
			out = append(out, x)
		}
	}
	return out
}

// quarantineRoot is where one row's parked checkouts live.
func (r *Reclaimer) quarantineRoot(id string) string {
	return filepath.Join(r.Root, ".reclaim", id)
}

// newQuarantinePath returns a FRESH path that does not exist and cannot collide
// with any other: <root>/.reclaim/<row-id>/<checkout>-<unix-nanos>-<rand>.
// git worktree move into an existing directory moves the source INSIDE it, so a
// guessable or reused name is exactly what must never happen.
func (r *Reclaimer) newQuarantinePath(w Worktree, c string) string {
	name := sanitizeRefComponent(relTo(w.Path, c))
	if name == "" {
		name = "root"
	}
	for {
		var b [4]byte
		_, _ = rand.Read(b[:])
		q := filepath.Join(r.quarantineRoot(w.ID), fmt.Sprintf("%s-%d-%s", name, time.Now().UnixNano(), hex.EncodeToString(b[:])))
		if _, err := os.Lstat(q); os.IsNotExist(err) {
			return q
		}
	}
}

// originMarker records where a parked checkout came from, so a recovery pass
// (daemon start, or a restore) can move it back without asking the server.
func originMarker(q string) string { return q + ".origin" }

// quarantined lists the checkouts parked for one row.
func (r *Reclaimer) quarantined(id string) []string {
	entries, err := os.ReadDir(r.quarantineRoot(id))
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".disposing") {
			name = strings.TrimSuffix(name, ".disposing")
		} else if !e.IsDir() {
			continue
		}
		path := filepath.Join(r.quarantineRoot(id), name)
		if !seen[path] {
			out = append(out, path)
			seen[path] = true
		}
	}
	return out
}

// heldQuarantined is the answer for a row that has checkouts parked: held, with
// every parked path named, and never "gone".
func (r *Reclaimer) heldQuarantined(w Worktree, parked []string, why string) Result {
	detail := fmt.Sprintf("%d checkout(s) are parked, still locked, at %s", len(parked), strings.Join(parked, ", "))
	if why != "" {
		detail += " (" + why + ")"
	}
	return held(w, ReasonQuarantined, detail)
}

func (r *Reclaimer) removeOne(ctx context.Context, w Worktree, c string, remaining []string, confirmed bool) *Result {
	fail := func(res Result) *Result { return &res }
	info, err := inspectLock(ctx, c)
	if err != nil {
		return fail(Result{ID: w.ID, Outcome: OutcomeError, Error: err.Error()})
	}
	claim := ArchivedLockReason(w.ID, w.Fence)
	if !info.Locked || info.Reason != claim {
		return fail(held(w, ReasonStale, "the lock changed just before removal"))
	}

	q := r.newQuarantinePath(w, c)
	if err := os.MkdirAll(filepath.Dir(q), 0o755); err != nil {
		return fail(Result{ID: w.ID, Outcome: OutcomeError, Error: err.Error()})
	}
	// The marker goes down BEFORE the move: a crash after it can always be undone.
	if err := os.WriteFile(originMarker(q), []byte(c), 0o644); err != nil {
		return fail(Result{ID: w.ID, Outcome: OutcomeError, Error: err.Error()})
	}
	dropMarker := func() { _ = os.Remove(originMarker(q)) }

	// Unlock only for the move, then take the same claim back at the new path.
	if _, err := git(ctx, "", nil, "--git-dir="+info.CommonDir, "worktree", "unlock", c); err != nil {
		dropMarker()
		return fail(Result{ID: w.ID, Outcome: OutcomeError, Error: err.Error()})
	}
	if _, err := git(ctx, "", nil, "--git-dir="+info.CommonDir, "worktree", "move", c, q); err != nil {
		_ = setLock(ctx, lockInfo{CommonDir: info.CommonDir}, c, claim)
		dropMarker()
		return fail(held(w, ReasonUnverified, relDetail(w.Path, c, "could not quarantine it: "+err.Error())))
	}
	// It must have landed EXACTLY at q, and be registered there.
	landed, lerr := inspectLock(ctx, q)
	if lerr != nil || !landed.Registered {
		// It moved somewhere we did not ask for, or git lost track of it. Do not
		// guess: leave everything as it is and say so.
		return fail(r.heldQuarantined(w, []string{q}, "it did not land where it was moved to"))
	}
	if err := setLock(ctx, lockInfo{CommonDir: info.CommonDir}, q, claim); err != nil {
		return fail(r.heldQuarantined(w, []string{q}, "it could not be re-locked: "+err.Error()))
	}

	// putBack returns the checkout to its original path ONLY when that path is
	// free. If anything exists there now, the checkout stays parked, locked with
	// its claim, and the row is held as quarantined. Nothing is ever moved into
	// an existing path and nothing is ever deleted here.
	putBack := func(reason HeldReason, detail string) *Result {
		if _, err := os.Lstat(c); err == nil {
			return fail(r.heldQuarantined(w, []string{q}, fmt.Sprintf("%s exists again, so it was not moved back; it was held for: %s: %s", relDetail(w.Path, c, ""), reason, detail)))
		}
		if _, err := git(ctx, "", nil, "--git-dir="+info.CommonDir, "worktree", "unlock", q); err != nil {
			return fail(r.heldQuarantined(w, []string{q}, "it could not be unlocked to move back: "+err.Error()))
		}
		if _, err := git(ctx, "", nil, "--git-dir="+info.CommonDir, "worktree", "move", q, c); err != nil {
			_ = setLock(ctx, lockInfo{CommonDir: info.CommonDir}, q, claim)
			return fail(r.heldQuarantined(w, []string{q}, "it could not be moved back: "+err.Error()))
		}
		if back, err := inspectLock(ctx, c); err != nil || !back.Registered {
			return fail(r.heldQuarantined(w, []string{c}, "it did not land back where it came from"))
		}
		_ = setLock(ctx, lockInfo{CommonDir: info.CommonDir}, c, claim)
		dropMarker()
		return fail(held(w, reason, relDetail(w.Path, c, detail)))
	}

	// Re-check on the quarantined copy.
	qNested := nestedUnder(q, c, remaining)
	if sc := scanWorkspace(ctx, q); sc.Truncated || len(sc.Repos) != 1+len(qNested) {
		return putBack(ReasonNestedRepo, "a git repository appeared inside it")
	}
	if v := dataHold(ctx, q, qNested); !v.safe() {
		return putBack(v.Reason, v.Detail)
	}
	if hold := r.inUse(ctx, Worktree{ID: w.ID, Path: q}, true); hold != nil {
		return putBack(hold.Reason, hold.Detail)
	}
	if hold := r.inUse(ctx, w, true); hold != nil {
		return putBack(hold.Reason, hold.Detail)
	}
	force := false
	if confirmed {
		if saved, ok := w.Trees[relTo(w.Path, c)]; ok {
			tmp, err := os.MkdirTemp("", "reliant-remove-")
			if err != nil {
				return putBack(ReasonUnverified, err.Error())
			}
			defer os.RemoveAll(tmp)
			now, err := workTree(ctx, q, tmp, qNested)
			if err != nil || now != saved {
				return putBack(ReasonDirty, "changed since it was saved; nothing was removed")
			}
			force = true // dirty by design, and just proven equal to the saved snapshot
		}
	}
	if !force {
		if v := classifyCheckout(ctx, q, baseFor(w, c), qNested, w.ID, r.probe()); !v.safe() && (!confirmed || v.Reason != ReasonUnreachable) {
			return putBack(v.Reason, "changed since it was checked: "+v.Detail)
		}
	}

	// Unlock for removal, remove, and on refusal put everything back.
	if _, err := git(ctx, "", nil, "--git-dir="+info.CommonDir, "worktree", "unlock", q); err != nil {
		return putBack(ReasonUnverified, err.Error())
	}
	args := []string{"--git-dir=" + info.CommonDir, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	if _, err := git(ctx, "", nil, append(args, q)...); err != nil {
		_ = setLock(ctx, lockInfo{CommonDir: info.CommonDir}, q, claim)
		return putBack(ReasonDirty, "git refused to remove it: "+err.Error())
	}
	dropMarker()
	return nil
}

// nestedUnder maps the checkouts that sat inside `orig` to where they now are
// inside its quarantined copy `q`.
func nestedUnder(q, orig string, all []string) []string {
	var out []string
	for _, n := range nestedOf(orig, all) {
		out = append(out, filepath.Join(q, relTo(orig, n)))
	}
	return out
}

// ---- active worktrees ---------------------------------------------------

// lockActive locks every checkout as active. It always inspects and rewrites:
// an unarchive reaches it with a fence to retire, and a cache hit there would
// leave the archive's fence valid for a removal already in flight. The sweep's
// own once-a-day recheck (LockedAt on the row) is what keeps this cheap.
func (r *Reclaimer) lockActive(ctx context.Context, w Worktree) Result {
	res := Result{ID: w.ID, Outcome: OutcomeLocked}
	checkouts := shallowCheckouts(w.Path, w.Checkouts)
	for _, c := range checkouts {
		if err := RestoreCheckout(ctx, c, w.ID, w.Retire); err != nil {
			res.Outcome, res.Error = OutcomeError, relDetail(w.Path, c, err.Error())
		}
	}
	if res.Outcome == OutcomeLocked && w.ProbeRecoverableAfterSeconds > 0 && w.Retire == "" && !w.KeepFiles && len(checkouts) > 0 {
		if r.recoverableIdle(ctx, w, time.Duration(w.ProbeRecoverableAfterSeconds)*time.Second) {
			res.RecoverableIdle = true
		}
	}
	if res.Outcome == OutcomeLocked && w.ProbeLanded && w.Retire == "" && len(checkouts) > 0 {
		r.probeLanded(ctx, w, &res)
	}
	// A landed or recoverable row is about to be retired whole; shedding it
	// first would only spend the request.
	if res.Outcome == OutcomeLocked && !res.Landed && !res.RecoverableIdle && w.ShedAfterSeconds > 0 && len(checkouts) > 0 {
		r.shedRegenerable(ctx, w, time.Duration(w.ShedAfterSeconds)*time.Second, &res)
	}
	// A landed worktree is the server's to decide on by its own rule; asking
	// whether it is also "finished" would repeat the same checks.
	if res.Outcome == OutcomeLocked && !res.Landed && w.ProbeRecoverableAfterSeconds <= 0 && w.ProbeIdle && w.Retire == "" && len(checkouts) > 0 {
		switch r.finished(ctx, w) {
		case probeFinished:
			res.Outcome = OutcomeIdleDone
		case probeInconclusive:
			// Locked all the same; only the question "is it finished" went
			// unanswered, and the server must not read that as a clean "no".
			res.Outcome = OutcomeProbeInconclusive
		}
	}
	return res
}

// probeAnswer is what finished concluded.
type probeAnswer int

const (
	// probeUnfinished: a check ran to completion and said the worktree is
	// still in use, dirty, unpushed, recently touched or otherwise not done.
	probeUnfinished probeAnswer = iota
	// probeFinished: every check passed.
	probeFinished
	// probeInconclusive: a check could not run to completion (out of time, a
	// git failure, an unreadable process list), so nothing is known.
	probeInconclusive
)

// DefaultProbeBudget bounds one finished probe: longer than the workspace scan
// (scanBudget) it starts with, so a scan that hits its own limit is reported by
// the scan, not by this.
const DefaultProbeBudget = 60 * time.Second

// finished reports whether an active worktree is done with: every check the
// automatic removal makes (structure, data, quiet time, no process, clean and
// merged or pushed) passes, with the longer ActiveIdleFloor. Read-only; it
// takes no claim and removes nothing. A check that could not be completed is
// probeInconclusive, never probeUnfinished.
func (r *Reclaimer) finished(ctx context.Context, w Worktree) probeAnswer {
	budget := r.ProbeBudget
	if budget <= 0 {
		budget = DefaultProbeBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	answer := func(a probeAnswer) probeAnswer {
		if ctx.Err() != nil && a != probeFinished {
			return probeInconclusive
		}
		return a
	}
	checkouts, _, hold := r.structure(ctx, w, false)
	if hold != nil {
		return answer(holdAnswer(hold))
	}
	if len(checkouts) == 0 {
		return probeUnfinished
	}
	floor := r.ActiveIdleFloor
	if floor <= 0 {
		floor = DefaultActiveIdleFloor
	}
	last, err := idleSince(ctx, w.Path, checkouts)
	if err != nil {
		return probeInconclusive
	}
	if r.now().Sub(last) < floor {
		return probeUnfinished
	}
	if hold := r.inUse(ctx, w, true); hold != nil {
		return answer(holdAnswer(hold))
	}
	v := classifyAll(ctx, w, checkouts, r.probe())
	switch {
	case v.safe():
		return answer(probeFinished)
	case v.Reason == ReasonUnverified:
		return probeInconclusive
	}
	return answer(probeUnfinished)
}

// holdAnswer maps a check's hold to what the probe concluded: only Unverified
// means the check could not tell.
func holdAnswer(hold *Result) probeAnswer {
	if hold.Reason == ReasonUnverified {
		return probeInconclusive
	}
	return probeUnfinished
}

func (r *Reclaimer) measure(ctx context.Context, res *Result, path string) {
	r.mu.Lock()
	c, ok := r.sizes[path]
	r.mu.Unlock()
	if ok && r.now().Sub(c.at) < sizeTTL {
		res.SizeBytes = c.bytes
		return
	}
	size, _ := DirSize(ctx, path)
	res.SizeBytes = size
	r.mu.Lock()
	if r.sizes == nil {
		r.sizes = map[string]cachedSize{}
	}
	r.sizes[path] = cachedSize{size, r.now()}
	r.mu.Unlock()
}

func pruneRegistrations(ctx context.Context, w Worktree) {
	seen := map[string]bool{}
	for _, c := range w.Checkouts {
		if c.RepoPath == "" || seen[c.RepoPath] {
			continue
		}
		seen[c.RepoPath] = true
		_, _ = git(ctx, c.RepoPath, nil, "worktree", "prune")
	}
}

func commonDirOf(ctx context.Context, checkout string) string {
	d, err := commonDir(ctx, checkout)
	if err != nil {
		return ""
	}
	return filepath.Dir(d)
}

// RecoverQuarantined moves a row's parked checkouts back to the paths they came
// from, when those paths are free. The lock (the archive claim) is kept as it
// was, so the next sweep decides afresh whether the row is still archived and
// removable or active again. A checkout whose origin is taken, or whose marker
// is missing, stays parked. It returns the paths that were put back and those
// still parked. An empty id recovers every row.
func (r *Reclaimer) RecoverQuarantined(ctx context.Context, id string) (restored, parked []string) {
	ids := []string{id}
	if id == "" {
		ids = nil
		entries, _ := os.ReadDir(filepath.Join(r.Root, ".reclaim"))
		for _, e := range entries {
			if e.IsDir() {
				ids = append(ids, e.Name())
			}
		}
	}
	for _, rowID := range ids {
		unlock, err := r.rowLock(ctx, rowID)
		if err != nil {
			parked = append(parked, r.quarantined(rowID)...)
			continue
		}
		rs, ps := r.recoverLocked(ctx, rowID)
		unlock()
		restored, parked = append(restored, rs...), append(parked, ps...)
	}
	return restored, parked
}

// rowLock takes the cross-process lock for one row: <root>/.reclaim/<id>.lock.
func (r *Reclaimer) rowLock(ctx context.Context, id string) (func(), error) {
	dir := filepath.Join(r.Root, ".reclaim")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return lockFile(ctx, filepath.Join(dir, id+".lock"))
}

// recoverLocked is RecoverQuarantined for one row whose row lock is held.
func (r *Reclaimer) recoverLocked(ctx context.Context, rowID string) (restored, parked []string) {
	r.dropStrayMarkers(rowID)
	for _, q := range r.quarantined(rowID) {
		// A partially disposed checkout is evidence/recovery state, never a
		// complete workspace that automatic revival may silently move home.
		if _, err := os.Lstat(q + ".disposing"); err == nil || !os.IsNotExist(err) {
			parked = append(parked, q)
			continue
		}
		orig, err := os.ReadFile(originMarker(q))
		if err != nil || len(orig) == 0 {
			parked = append(parked, q)
			continue
		}
		dest := string(orig)
		if _, err := os.Lstat(dest); err == nil {
			parked = append(parked, q)
			continue
		}
		info, err := inspectLock(ctx, q)
		if err != nil || !info.Registered {
			parked = append(parked, q)
			continue
		}
		if info.Locked {
			if _, err := git(ctx, "", nil, "--git-dir="+info.CommonDir, "worktree", "unlock", q); err != nil {
				parked = append(parked, q)
				continue
			}
		}
		err = os.MkdirAll(filepath.Dir(dest), 0o755)
		if err == nil {
			_, err = git(ctx, "", nil, "--git-dir="+info.CommonDir, "worktree", "move", q, dest)
		}
		if back, berr := inspectLock(ctx, dest); err != nil || berr != nil || !back.Registered {
			if info.Locked {
				_ = setLock(ctx, lockInfo{CommonDir: info.CommonDir}, q, info.Reason)
			}
			parked = append(parked, q)
			continue
		}
		if info.Locked {
			_ = setLock(ctx, lockInfo{CommonDir: info.CommonDir}, dest, info.Reason)
		}
		_ = os.Remove(originMarker(q))
		restored = append(restored, dest)
	}
	return restored, parked
}

// dropStrayMarkers deletes an .origin marker only when no checkout sits beside
// it: a crash between writing the marker and the move leaves one. A marker with
// a checkout next to it is the only way home for that checkout and is kept.
func (r *Reclaimer) dropStrayMarkers(rowID string) {
	entries, err := os.ReadDir(r.quarantineRoot(rowID))
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".origin") {
			continue
		}
		marker := filepath.Join(r.quarantineRoot(rowID), name)
		if _, err := os.Lstat(strings.TrimSuffix(marker, ".origin")); os.IsNotExist(err) {
			_ = os.Remove(marker)
		}
	}
}
