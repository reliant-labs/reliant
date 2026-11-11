package worktreereclaim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// persistRetirementState orders recovery state before rename/destruction.
func persistRetirementState(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// archiveTime is the moment a row was archived, read from its fence: the
// server writes the archive time in nanoseconds (worktreesweep.Fence). A fence
// that is not such a time (before 2020) carries none.
func archiveTime(fence string) (time.Time, bool) {
	ns, err := strconv.ParseInt(fence, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	at := time.Unix(0, ns)
	return at, at.After(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
}

// DefaultArchiveClockSkew is how far the daemon's clock may trail the
// database's before the veto misses real use: an archive is decided only after
// hours of quiet, so holding on a change in the few minutes before it costs
// nothing and catches use on a machine whose clock is behind.
const DefaultArchiveClockSkew = 10 * time.Minute

// useCountsAfter is the moment after which a change in w's checkouts is use the
// archive did not see: the archive time (less the allowed clock skew), or the
// end of the reclaimer's own last shedding in it, whichever is later. Shedding
// regenerable output from a worktree held for its work moves directory mtimes
// after the archive; that is the daemon, not a user, and must not hold the
// worktree forever once its hold clears. false when the fence carries no time.
func (r *Reclaimer) useCountsAfter(w Worktree) (time.Time, bool) {
	at, ok := archiveTime(w.Fence)
	if !ok {
		return time.Time{}, false
	}
	at = at.Add(-r.ArchiveClockSkew)
	if own, ok := r.lastOwnWrite(w.ID); ok && own.After(at) {
		at = own
	}
	return at, true
}

// ownWritePath records, per row, when this reclaimer last changed the
// worktree itself (shedding): <root>/.reclaim/<id>.shed.
func (r *Reclaimer) ownWritePath(id string) string {
	return filepath.Join(r.Root, ".reclaim", id+".shed")
}

// recordOwnWrite stamps the end of a shedding pass in w. The stamp is the
// machine's wall clock, the clock the file times it is compared with use.
func (r *Reclaimer) recordOwnWrite(id string) {
	if r.Root == "" || id == "" {
		return
	}
	p := r.ownWritePath(id)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(time.Now().UnixNano(), 10)), 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

func (r *Reclaimer) lastOwnWrite(id string) (time.Time, bool) {
	if r.Root == "" || id == "" {
		return time.Time{}, false
	}
	b, err := os.ReadFile(r.ownWritePath(id))
	if err != nil {
		return time.Time{}, false
	}
	ns, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(0, ns), true
}

// usedAfterArchive is the hold for a checkout something changed after its
// row was archived: the archive decided on activity the server could see (the
// chats and threads bound to the row), and this shows use it could not, such
// as an agent working in the checkout by path, or a chat paused mid-run. Such
// a worktree is held, never retired on that archive; using it through a bound
// chat revives the row (ReviveArchivedWorktree), and a later archive carries a
// later fence (Reliant bug 7576a754, 2026-11-10).
func usedAfterArchive(last, archivedAt time.Time) string {
	return fmt.Sprintf("used after it was archived: changed %s, archived %s",
		last.UTC().Format(time.RFC3339), archivedAt.UTC().Format(time.RFC3339))
}

// DefaultDisposalCheckEvery paces the full admission and process check while a
// parked checkout is unlinked. That check walks every process's cwd, exe and
// descriptors: ~50ms with 165 processes, ~0.5s on a busy agent machine. Run
// before every one of a checkout's 10k-100k entries, it made a disposal take
// hours, so the 90s request budget stopped every one of them part-way and left
// it parked for good (144 rows, 38 GiB on one machine, 2026-11-10). Paced, a
// process that starts using the parked copy is still seen within one interval
// plus one scan, which is the same window a back-to-back scan already had.
const DefaultDisposalCheckEvery = 250 * time.Millisecond

// pacedCheck runs check on its first call and then at most once per every.
type pacedCheck struct {
	check func() error
	every time.Duration
	last  time.Time
	ran   bool
}

func (p *pacedCheck) run() error {
	if p.ran && time.Since(p.last) < p.every {
		return nil
	}
	if err := p.check(); err != nil {
		return err
	}
	p.ran, p.last = true, time.Now()
	return nil
}

// parkedCheckout is one checkout moved into quarantine for retirement.
type parkedCheckout struct {
	original, path, common string
	tree                   *retirementTree
	gitState               retirementGitState
}

// retirementAdmission takes the runtime admission lease over the workspace and
// its quarantine, and returns the check every retirement step re-runs: the
// lease is still held, and no process uses either path. pin exempts the
// retirement's own inspection descriptors from the process check.
func (r *Reclaimer) retirementAdmission(ctx context.Context, w Worktree) (check func() error, pin func(...*os.File), release func(), hold *Result) {
	if r.Acquire == nil || r.ProcessUse == nil {
		res := held(w, ReasonUnverified, "runtime/process safety dependencies unavailable")
		return nil, nil, nil, &res
	}
	paths := []string{w.Path, r.quarantineRoot(w.ID)}
	lease, err := r.Acquire(ctx, paths)
	if err != nil || lease == nil {
		res := held(w, ReasonInUse, "exclusive runtime admission unavailable")
		return nil, nil, nil, &res
	}
	var inspectionFiles []*os.File
	check = func() error {
		probeCtx := withInspectionFiles(ctx, inspectionFiles)
		if err := lease.Check(probeCtx); err != nil {
			return err
		}
		if err := r.ProcessUse(probeCtx, paths); err != nil {
			return err
		}
		for _, path := range paths {
			if h := r.inUse(ctx, Worktree{ID: w.ID, Path: path}, true); h != nil {
				return errors.New(h.Detail)
			}
		}
		return nil
	}
	pin = func(files ...*os.File) { inspectionFiles = append(inspectionFiles, files...) }
	return check, pin, lease.Release, nil
}

// retireWorkspace is the automatic whole-worktree path. Unknown data is held,
// never upgraded to disposable by an ignored ancestor or by Git's --force.
// All checkouts are parked and verified before ANY content is removed.
func (r *Reclaimer) retireWorkspace(ctx context.Context, w Worktree, checkouts []string, outside []string) Result {
	if len(outside) != 0 {
		return held(w, ReasonOutsideFiles, "whole-worktree retirement preserves loose root files")
	}
	check, pin, release, hold := r.retirementAdmission(ctx, w)
	if hold != nil {
		return *hold
	}
	defer release()
	if err := check(); err != nil {
		return held(w, ReasonInUse, err.Error())
	}
	parked := []parkedCheckout{}
	defer func() {
		for _, p := range parked {
			if p.tree != nil {
				p.tree.close()
			}
		}
	}()
	rollback := func(reason HeldReason, detail string) Result {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		for i := len(parked) - 1; i >= 0; i-- {
			p := parked[i]
			if _, err := os.Lstat(p.original); !os.IsNotExist(err) {
				return r.heldQuarantined(w, []string{p.path}, "original path is occupied; "+detail)
			}
			if _, err := git(restoreCtx, "", nil, "--git-dir="+p.common, "worktree", "unlock", p.path); err != nil {
				return r.heldQuarantined(w, []string{p.path}, detail)
			}
			if _, err := git(restoreCtx, "", nil, "--git-dir="+p.common, "worktree", "move", p.path, p.original); err != nil {
				_ = setLock(restoreCtx, lockInfo{CommonDir: p.common}, p.path, ArchivedLockReason(w.ID, w.Fence))
				return r.heldQuarantined(w, []string{p.path}, detail)
			}
			_ = setLock(restoreCtx, lockInfo{CommonDir: p.common}, p.original, ArchivedLockReason(w.ID, w.Fence))
			_ = os.Remove(originMarker(p.path))
		}
		return held(w, reason, detail)
	}
	// No arbitrary nested checkout choreography: retain nested layouts whose
	// moves cannot be staged independently. Sibling multi-repo roots work.
	for _, c := range checkouts {
		for _, other := range checkouts {
			if c != other && within(c, other) {
				return held(w, ReasonNestedRepo, "nested checkout retirement requires explicit recovery")
			}
		}
	}
	for _, c := range checkouts {
		original, err := inspectRetirementTree(ctx, c)
		if err != nil {
			return rollback(ReasonUnverified, "unsafe retirement inventory: "+err.Error())
		}
		if r.IdleFloor > 0 && r.now().Sub(original.newest) < r.IdleFloor {
			original.close()
			return rollback(ReasonRecentlyActive, "a file in the checkout changed recently")
		}
		if at, ok := r.useCountsAfter(w); ok && original.newest.After(at) {
			original.close()
			return rollback(ReasonUsedAfterArchive, usedAfterArchive(original.newest, at))
		}
		if err := original.validate(ctx); err != nil {
			original.close()
			return rollback(ReasonUnverified, err.Error())
		}
		info, err := inspectLock(ctx, c)
		if err != nil || !info.Locked || info.Reason != ArchivedLockReason(w.ID, w.Fence) {
			original.close()
			return rollback(ReasonStale, "archive lock changed before retirement")
		}
		q := r.newQuarantinePath(w, c)
		if err := os.MkdirAll(filepath.Dir(q), 0700); err != nil {
			original.close()
			return rollback(ReasonUnverified, err.Error())
		}
		if err := persistRetirementState(originMarker(q), []byte(c)); err != nil {
			original.close()
			return rollback(ReasonUnverified, err.Error())
		}
		pin(original.inspectionFiles()...)
		if err := check(); err != nil {
			original.close()
			_ = os.Remove(originMarker(q))
			return rollback(ReasonInUse, err.Error())
		}
		if err := original.validate(ctx); err != nil {
			original.close()
			_ = os.Remove(originMarker(q))
			return rollback(ReasonUnverified, err.Error())
		}
		if _, err := git(ctx, "", nil, "--git-dir="+info.CommonDir, "worktree", "unlock", c); err != nil {
			original.close()
			_ = os.Remove(originMarker(q))
			return rollback(ReasonUnverified, err.Error())
		}
		if _, err := git(ctx, "", nil, "--git-dir="+info.CommonDir, "worktree", "move", c, q); err != nil {
			original.close()
			_ = setLock(ctx, info, c, ArchivedLockReason(w.ID, w.Fence))
			_ = os.Remove(originMarker(q))
			return rollback(ReasonUnverified, "could not park checkout: "+err.Error())
		}
		// Compare the moved directory to the descriptor we inspected before
		// parking. A pathname replacement may move, but must never authorize
		// disposal of a different tree.
		moved, identityErr := retirementMatchesPath(original, q)
		original.close()
		parked = append(parked, parkedCheckout{original: c, path: q, common: info.CommonDir})
		if identityErr != nil || !moved {
			return rollback(ReasonUnverified, "checkout changed while parking")
		}
		if err := setLock(ctx, lockInfo{CommonDir: info.CommonDir}, q, ArchivedLockReason(w.ID, w.Fence)); err != nil {
			return rollback(ReasonUnverified, err.Error())
		}
		tree, err := inspectRetirementTree(ctx, q)
		if err != nil {
			return rollback(ReasonUnverified, "parked checkout is unsafe: "+err.Error())
		}
		parked[len(parked)-1].tree = tree
		pin(tree.inspectionFiles()...)
	}
	for i := range parked {
		p := &parked[i]
		if err := check(); err != nil {
			return rollback(ReasonInUse, err.Error())
		}
		if h := r.inUse(ctx, Worktree{ID: w.ID, Path: p.path}, true); h != nil {
			return rollback(h.Reason, h.Detail)
		}
		// Bind BEFORE the first clean/index/history predicate. Snapshotting
		// after classification would legitimize mutations in its late seams.
		before, err := inspectRetirementGitState(ctx, p.path)
		if err != nil {
			return rollback(ReasonUnverified, err.Error())
		}
		if v := classifyCheckout(ctx, p.path, baseFor(w, p.original), nil, w.ID, r.probe()); !v.safe() {
			return rollback(v.Reason, v.Detail)
		}
		after, err := inspectRetirementGitState(ctx, p.path)
		if err != nil || before != after {
			return rollback(ReasonUnverified, "Git state changed during classification")
		}
		p.gitState = before
		if r.IdleFloor > 0 && r.now().Sub(p.tree.newest) < r.IdleFloor {
			return rollback(ReasonRecentlyActive, "parked checkout changed recently")
		}
		if err := p.tree.validate(ctx); err != nil {
			return rollback(ReasonUnverified, err.Error())
		}
	}
	// A new occupant at the old path is a revival/independent writer, not a
	// reason to delete its parked predecessor. Preserve both for recovery.
	for _, p := range parked {
		if _, err := os.Lstat(p.original); !os.IsNotExist(err) {
			return rollback(ReasonInUse, "original checkout path became occupied")
		}
	}
	if res := r.disposeParked(ctx, w, parked, check); res != nil {
		return *res
	}
	if !removeEmptyDirs(emptyDirsUnder(w.Path)) {
		return held(w, ReasonOutsideFiles, "checkouts retired but unexpected workspace files remain")
	}
	_ = os.Remove(r.quarantineRoot(w.ID))
	_ = os.Remove(r.ownWritePath(w.ID))
	return Result{ID: w.ID, Outcome: OutcomeRemoved, Detail: fmt.Sprintf("retired %d complete checkout(s)", len(parked))}
}

// disposeParked unlinks verified parked checkouts one at a time. From the first
// unlink onward errors preserve the .disposing journal and the remaining bytes;
// a partially removed tree is never presented as restored. A later pass resumes
// it (resumeRetirement).
func (r *Reclaimer) disposeParked(ctx context.Context, w Worktree, parked []parkedCheckout, check func() error) *Result {
	every := r.DisposalCheckEvery
	if every <= 0 {
		every = DefaultDisposalCheckEvery
	}
	paced := &pacedCheck{every: every}
	for _, p := range parked {
		if err := persistRetirementState(p.path+".disposing", []byte(w.Fence)); err != nil && !disposingUnder(p.path, w.Fence) {
			res := r.heldQuarantined(w, []string{p.path}, "could not persist disposal state: "+err.Error())
			return &res
		}
		stable := func() error {
			for _, original := range parked {
				if _, err := os.Lstat(original.original); !os.IsNotExist(err) {
					return errors.New("original path became occupied during retirement")
				}
			}
			if err := check(); err != nil {
				return err
			}
			info, err := inspectLock(ctx, p.path)
			if err != nil || !info.Locked || info.Reason != ArchivedLockReason(w.ID, w.Fence) {
				return errors.New("archive lock changed during retirement")
			}
			state, err := inspectRetirementGitState(ctx, p.path)
			if err != nil {
				return err
			}
			if state != p.gitState {
				return errors.New("authorized Git index/history/ref state changed during retirement")
			}
			return nil
		}
		paced.check = stable
		paced.ran = false // every checkout starts with a full check
		if err := p.tree.remove(ctx, paced.run); err != nil {
			res := r.heldQuarantined(w, []string{p.path}, "retirement stopped; remaining files retained: "+err.Error())
			return &res
		}
		// The entire directory is already gone via anchored operations. Git sees a
		// missing checkout and only retires its registration, with NO --force.
		if _, err := git(ctx, "", nil, "--git-dir="+p.common, "worktree", "unlock", p.path); err != nil {
			res := held(w, ReasonUnverified, "files removed; registration retained: "+err.Error())
			return &res
		}
		if _, err := git(ctx, "", nil, "--git-dir="+p.common, "worktree", "remove", p.path); err != nil {
			res := held(w, ReasonUnverified, "files removed; registration retained: "+err.Error())
			return &res
		}
		_ = os.Remove(originMarker(p.path))
		_ = os.Remove(p.path + ".disposing")
	}
	return nil
}

// disposingUnder reports whether q carries a disposal journal for fence.
func disposingUnder(q, fence string) bool {
	b, err := os.ReadFile(q + ".disposing")
	return err == nil && fence != "" && string(b) == fence
}

// resumable reports whether an archived row's parked checkouts are a
// retirement of THIS archive that stopped part-way: at least one carries a
// .disposing journal naming the row's fence, and none carries another's.
func (r *Reclaimer) resumable(w Worktree, parked []string) bool {
	if w.State != StateArchived || w.Fence == "" {
		return false
	}
	found := false
	for _, q := range parked {
		if _, err := os.Lstat(q + ".disposing"); err == nil {
			if !disposingUnder(q, w.Fence) {
				return false
			}
			found = true
		}
	}
	return found
}

// verifyParked re-establishes, for one checkout parked by retirement, every
// fact the original disposal was authorized on. A checkout whose disposal had
// begun may only miss tracked files (classifyDisposing); its untouched
// siblings must be exactly as clean as before.
func (r *Reclaimer) verifyParked(ctx context.Context, w Worktree, q string) (origin string, v verdict) {
	b, err := os.ReadFile(originMarker(q))
	if err != nil || len(b) == 0 {
		return "", verdict{ReasonUnverified, "its origin marker is missing"}
	}
	origin = string(b)
	if !within(w.Path, origin) && !samePath(w.Path, origin) {
		return origin, verdict{ReasonStale, "its origin is outside the workspace"}
	}
	if _, err := os.Lstat(origin); !os.IsNotExist(err) {
		return origin, verdict{ReasonInUse, "its original path is occupied again"}
	}
	info, err := inspectLock(ctx, q)
	if err != nil || !info.Registered || !info.Locked || info.Reason != ArchivedLockReason(w.ID, w.Fence) {
		return origin, verdict{ReasonStale, "its lock is not this archive's claim"}
	}
	if h := r.inUse(ctx, Worktree{ID: w.ID, Path: q}, true); h != nil {
		return origin, verdict{h.Reason, h.Detail}
	}
	if v := dataHold(ctx, q, nil); !v.safe() {
		return origin, v
	}
	if disposingUnder(q, w.Fence) {
		return origin, classifyDisposing(ctx, q, baseFor(w, origin), w.ID, r.probe())
	}
	return origin, classifyCheckout(ctx, q, baseFor(w, origin), nil, w.ID, r.probe())
}

// resumeRetirement finishes a whole-worktree retirement that stopped after its
// checkouts were parked: a request deadline, a daemon restart, a process that
// appeared mid-disposal and has since gone. Before this, such a row was held as
// quarantined forever (settle never retried it and RecoverQuarantined refuses a
// partial tree), so every interrupted retirement leaked its whole size.
//
// It follows the two-phase protocol the sweep already speaks. Phase one
// re-verifies every parked checkout and answers Claimed; phase two (Remove,
// sent after the server re-read the row as still archived under this fence)
// verifies again under the admission lease and disposes. Anything that fails
// a check stays parked and is reported held, never moved or deleted.
func (r *Reclaimer) resumeRetirement(ctx context.Context, w Worktree, parked []string) Result {
	if !w.Remove {
		for _, q := range parked {
			if _, v := r.verifyParked(ctx, w, q); !v.safe() {
				return r.heldQuarantined(w, parked, fmt.Sprintf("its interrupted retirement cannot resume: %s: %s", v.Reason, v.Detail))
			}
		}
		if hasEntries(w.Path) {
			// Checkouts still at home join the retirement on their own terms.
			return r.autoPhase(ctx, w)
		}
		return Result{ID: w.ID, Outcome: OutcomeClaimed, Detail: "resuming an interrupted retirement"}
	}
	check, pin, release, hold := r.retirementAdmission(ctx, w)
	if hold != nil {
		return r.heldQuarantined(w, parked, hold.Detail)
	}
	defer release()
	if err := check(); err != nil {
		return r.heldQuarantined(w, parked, err.Error())
	}
	cos := make([]parkedCheckout, 0, len(parked))
	defer func() {
		for _, p := range cos {
			if p.tree != nil {
				p.tree.close()
			}
		}
	}()
	for _, q := range parked {
		info, err := inspectLock(ctx, q)
		if err != nil {
			return r.heldQuarantined(w, parked, err.Error())
		}
		tree, err := inspectRetirementTree(ctx, q)
		if err != nil {
			return r.heldQuarantined(w, parked, "parked checkout is unsafe: "+err.Error())
		}
		cos = append(cos, parkedCheckout{path: q, common: info.CommonDir, tree: tree})
		pin(tree.inspectionFiles()...)
		before, err := inspectRetirementGitState(ctx, q)
		if err != nil {
			return r.heldQuarantined(w, parked, err.Error())
		}
		origin, v := r.verifyParked(ctx, w, q)
		if !v.safe() {
			return r.heldQuarantined(w, parked, fmt.Sprintf("its interrupted retirement cannot resume: %s: %s", v.Reason, v.Detail))
		}
		after, err := inspectRetirementGitState(ctx, q)
		if err != nil || before != after {
			return r.heldQuarantined(w, parked, "Git state changed during verification")
		}
		// A checkout whose disposal had not begun keeps the idle floor; one that
		// had has its directories' mtimes moved by the unlinks themselves.
		if !disposingUnder(q, w.Fence) && r.IdleFloor > 0 && r.now().Sub(tree.newest) < r.IdleFloor {
			return r.heldQuarantined(w, parked, "a parked checkout changed recently")
		}
		if at, ok := r.useCountsAfter(w); ok && !disposingUnder(q, w.Fence) && tree.newest.After(at) {
			return r.heldQuarantined(w, parked, usedAfterArchive(tree.newest, at))
		}
		if err := tree.validate(ctx); err != nil {
			return r.heldQuarantined(w, parked, err.Error())
		}
		cos[len(cos)-1].original, cos[len(cos)-1].gitState = origin, before
	}
	if res := r.disposeParked(ctx, w, cos, check); res != nil {
		return *res
	}
	if hasEntries(w.Path) {
		return r.autoRemove(ctx, w)
	}
	_ = removeEmptyDirs(emptyDirsUnder(w.Path))
	_ = os.Remove(r.quarantineRoot(w.ID))
	return Result{ID: w.ID, Outcome: OutcomeRemoved, Detail: fmt.Sprintf("resumed an interrupted retirement of %d checkout(s)", len(cos))}
}

// hasEntries reports whether dir exists and holds anything but empty
// directories.
func hasEntries(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
