package worktreereclaim

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// retireWorkspace is the automatic whole-worktree path. Unknown data is held,
// never upgraded to disposable by an ignored ancestor or by Git's --force.
// All checkouts are parked and verified before ANY content is removed.
func (r *Reclaimer) retireWorkspace(ctx context.Context, w Worktree, checkouts []string, outside []string) Result {
	if len(outside) != 0 {
		return held(w, ReasonOutsideFiles, "whole-worktree retirement preserves loose root files")
	}
	if r.Acquire == nil || r.ProcessUse == nil {
		return held(w, ReasonUnverified, "runtime/process safety dependencies unavailable")
	}
	paths := []string{w.Path, r.quarantineRoot(w.ID)}
	lease, err := r.Acquire(ctx, paths)
	if err != nil || lease == nil {
		return held(w, ReasonInUse, "exclusive runtime admission unavailable")
	}
	defer lease.Release()
	var inspectionFiles []*os.File
	check := func() error {
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
	if err := check(); err != nil {
		return held(w, ReasonInUse, err.Error())
	}
	type parkedTree struct {
		original, path, common string
		tree                   *retirementTree
		indexPath              string
		indexHash              [32]byte
		headPath               string
		head                   []byte
	}
	parked := []parkedTree{}
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
		inspectionFiles = append(inspectionFiles, original.inspectionFiles()...)
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
		parked = append(parked, parkedTree{original: c, path: q, common: info.CommonDir})
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
		inspectionFiles = append(inspectionFiles, tree.inspectionFiles()...)
	}
	for _, p := range parked {
		if err := check(); err != nil {
			return rollback(ReasonInUse, err.Error())
		}
		if h := r.inUse(ctx, Worktree{ID: w.ID, Path: p.path}, true); h != nil {
			return rollback(h.Reason, h.Detail)
		}
		if v := classifyCheckout(ctx, p.path, baseFor(w, p.original), nil, w.ID, r.probe()); !v.safe() {
			return rollback(v.Reason, v.Detail)
		}
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
	// Snapshot index/HEAD only AFTER classification (Git may refresh index).
	// A concurrent git-add/checkout invalidates disposal instead of silently
	// deleting something that became tracked after eligibility.
	for i := range parked {
		p := &parked[i]
		admin := adminDir(p.path)
		if admin == "" {
			return rollback(ReasonUnverified, "missing checkout administration")
		}
		p.indexPath, p.headPath = filepath.Join(admin, "index"), filepath.Join(admin, "HEAD")
		index, err := os.ReadFile(p.indexPath)
		if err != nil {
			return rollback(ReasonUnverified, err.Error())
		}
		p.indexHash = sha256.Sum256(index)
		p.head, err = os.ReadFile(p.headPath)
		if err != nil {
			return rollback(ReasonUnverified, err.Error())
		}
	}
	// From the first unlink onward errors preserve journal+remaining bytes for
	// recovery; never try to present a partially removed tree as fully restored.
	for _, p := range parked {
		if err := persistRetirementState(p.path+".disposing", []byte(w.Fence)); err != nil {
			return r.heldQuarantined(w, []string{p.path}, "could not persist disposal state: "+err.Error())
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
			index, err := os.ReadFile(p.indexPath)
			if err != nil {
				return err
			}
			head, err := os.ReadFile(p.headPath)
			if err != nil {
				return err
			}
			if sha256.Sum256(index) != p.indexHash || !bytes.Equal(head, p.head) {
				return errors.New("git index or HEAD changed during retirement")
			}
			return nil
		}
		if err := p.tree.remove(ctx, stable); err != nil {
			return r.heldQuarantined(w, []string{p.path}, "retirement stopped; remaining files retained: "+err.Error())
		}
		// The entire directory is already gone via anchored operations. Git sees a
		// missing checkout and only retires its registration, with NO --force.
		if _, err := git(ctx, "", nil, "--git-dir="+p.common, "worktree", "unlock", p.path); err != nil {
			return held(w, ReasonUnverified, "files removed; registration retained: "+err.Error())
		}
		if _, err := git(ctx, "", nil, "--git-dir="+p.common, "worktree", "remove", p.path); err != nil {
			return held(w, ReasonUnverified, "files removed; registration retained: "+err.Error())
		}
		_ = os.Remove(originMarker(p.path))
		_ = os.Remove(p.path + ".disposing")
	}
	if !removeEmptyDirs(emptyDirsUnder(w.Path)) {
		return held(w, ReasonOutsideFiles, "checkouts retired but unexpected workspace files remain")
	}
	_ = os.Remove(r.quarantineRoot(w.ID))
	return Result{ID: w.ID, Outcome: OutcomeRemoved, Detail: fmt.Sprintf("retired %d complete checkout(s)", len(parked))}
}
