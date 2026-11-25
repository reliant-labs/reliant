// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Finishing an interrupted deletion.
//
// reclaimEntry writes .disposing beside a parked checkout only after the
// checkout was captured, parked, locked "reliant: reclaiming <key> <fence>"
// and verified against its capture. A deletion that stopped after that (the
// daemon restarted; a process check or a changed entry stopped the unlink
// loop) left what remained parked and held, and no later pass ever finished
// it: on one machine 323 such remains held 36G (2026-11-25), every byte of
// them in a capture ref.
//
// finishDisposal finishes one, under the same key lock and the same
// admission lease and process checks as the first attempt, once it has proved
// the remains are a part of the capture:
//
//   - the capture ref named in .capture resolves, in the checkout's own
//     repository, to a reclaim capture of this checkout's HEAD;
//   - the lock is still the interrupted reclaim's, with the fence .disposing
//     recorded;
//   - HEAD, the staged index and the hidden-flag set are as captured, and
//     every commit only this checkout reaches is reachable from the capture;
//   - every file git can see in the remains is in the capture with the same
//     content: compared with the capture's tree of the files on disk, the
//     remains may only lack files (the ones already unlinked), never add or
//     change one;
//   - no ignored file in the remains is the user's data (dataHold), and the
//     inventory finds nothing it would refuse (a protected name, a nested
//     repository, a mount).
//
// Anything else keeps the remains, parked and locked, and says why. A missing
// capture ref always keeps them.

// finishDisposal finishes the interrupted deletion of the checkout parked at q
// (from orig), whose markers are m. The caller holds the key lock, has read
// e.Lock afresh, and has the daemon's admission (r.Acquire is set).
func (r *Reclaimer) finishDisposal(ctx context.Context, e *SweepEntry, q, orig, common string, m reclaimMarker) {
	keep := func(reason HeldReason, why string) {
		e.hold(reason, fmt.Sprintf("a deletion was interrupted at %s; what remains is kept there (%s), and all of it is in %s", q, why, m.Ref))
	}
	b, err := os.ReadFile(q + ".disposing")
	fence := strings.TrimSpace(string(b))
	if err != nil || fence == "" || e.Lock != reclaimingLock(e.Key, fence) {
		keep(ReasonQuarantined, "its lock is not the interrupted reclaim's")
		return
	}
	if m.Ref == "" || !strings.HasPrefix(m.Ref, captureRefDir(e.Key)) || m.CommonDir == "" || !samePath(m.CommonDir, common) {
		keep(ReasonCaptureMissing, "its capture is not recorded for this checkout")
		return
	}
	capt, err := ReadCapture(ctx, common, m.Ref)
	if err != nil {
		keep(ReasonCaptureMissing, "its capture ref cannot be read: "+err.Error())
		return
	}
	if _, err := os.Lstat(orig); !os.IsNotExist(err) {
		keep(ReasonInUse, "its original path is occupied again")
		return
	}
	if r.ProcessUse == nil {
		keep(ReasonUnverified, "process inspection is unavailable")
		return
	}

	paths := []string{q, orig}
	lease, err := r.Acquire(ctx, paths)
	if err != nil || lease == nil {
		keep(ReasonInUse, admissionRefused("daemon work is running in it", err))
		return
	}
	acquired := time.Now()
	defer func() { lease.Release() }()
	var lastFull time.Time
	var inspection []*os.File
	check := func(force bool) error {
		if !force && time.Since(lastFull) < fullCheckEvery {
			return nil
		}
		if time.Since(acquired) > leaseRenewAfter {
			lease.Release()
			next, err := r.Acquire(ctx, paths)
			if err != nil || next == nil {
				return errors.New(admissionRefused("daemon work started in it", err))
			}
			lease, acquired = next, time.Now()
		}
		pctx := withInspectionFiles(ctx, inspection)
		if err := lease.Check(pctx); err != nil {
			return err
		}
		if err := r.ProcessUse(pctx, paths); err != nil {
			return err
		}
		for _, p := range paths {
			if h := r.inUse(ctx, Worktree{ID: e.Key, Path: p}, true); h != nil {
				return errors.New(h.Detail)
			}
		}
		lastFull = time.Now()
		return nil
	}
	if err := check(true); err != nil {
		keep(ReasonInUse, err.Error())
		return
	}

	if reason, why := remainsOfCapture(ctx, q, capt); reason != "" {
		keep(reason, why)
		return
	}
	tree, err := inspectRetirementTree(ctx, q)
	if err != nil {
		keep(ReasonUnverified, "what remains cannot be inventoried: "+err.Error())
		return
	}
	defer tree.close()
	inspection = append(inspection, tree.inspectionFiles()...)
	gitState, err := inspectRetirementGitState(ctx, q)
	if err != nil {
		keep(ReasonUnverified, err.Error())
		return
	}
	if err := tree.validate(ctx); err != nil {
		keep(ReasonRecentlyActive, "it changed while it was verified: "+err.Error())
		return
	}
	if err := check(true); err != nil {
		keep(ReasonInUse, err.Error())
		return
	}
	stable := func() error {
		if _, err := os.Lstat(orig); !os.IsNotExist(err) {
			return errors.New("original path became occupied")
		}
		if time.Since(lastFull) < fullCheckEvery {
			return nil // each entry is still checked against its inventory
		}
		if err := check(true); err != nil {
			return err
		}
		info, err := inspectLock(ctx, q)
		if err != nil || !info.Locked || info.Reason != reclaimingLock(e.Key, fence) {
			return errors.New("the reclaim lock changed")
		}
		if s, err := inspectRetirementGitState(ctx, q); err != nil || s != gitState {
			return errors.New("its git state changed")
		}
		return nil
	}
	if err := tree.remove(ctx, stable); err != nil {
		keep(ReasonQuarantined, "deletion stopped again: "+err.Error())
		return
	}
	rctx := context.WithoutCancel(ctx)
	_, _ = git(rctx, "", nil, "--git-dir="+common, "worktree", "unlock", q)
	// The class the reclaim was decided on, as its capture records it.
	e.Class, e.Outcome, e.FreedBytes, e.Reason = SweepClass(capt.Meta.Class), OutcomeReclaimed, tree.bytes, ""
	if _, err := git(rctx, "", nil, "--git-dir="+common, "worktree", "remove", q); err != nil {
		e.Detail = fmt.Sprintf("an interrupted deletion was finished (captured at %s); its git registration was kept: %v", m.Ref, err)
		return
	}
	for _, s := range []string{".origin", ".prelock", ".capture", ".disposing"} {
		_ = os.Remove(q + s)
	}
	removeEmptyQuarantine(q)
	e.Detail = "an interrupted deletion was finished; captured at " + m.Ref + "; restore with: reliant worktree restore " + m.Ref + " --repo " + common
	r.leaveTombstone(orig, e, m.Ref, common, "interrupted reclaim finished")
}

// remainsOfCapture checks that the partly deleted checkout q holds nothing its
// capture c does not: the same HEAD and staged state, no hidden index
// entries, no private commit the capture misses, every file git sees equal to
// the capture's (files may only be missing), and no ignored data. It returns
// the hold, or "" when the remains are all in the capture.
func remainsOfCapture(ctx context.Context, q string, c CaptureInfo) (HeldReason, string) {
	head, err := git(ctx, q, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return ReasonUnverified, "its HEAD cannot be read: " + err.Error()
	}
	if head != c.Head {
		return ReasonRecentlyActive, fmt.Sprintf("HEAD moved after the capture (%s, captured %s)", short(head), short(c.Head))
	}
	if hidden := hiddenFlags(ctx, q); hidden != "" {
		return ReasonDirty, hidden
	}
	tmp, err := os.MkdirTemp("", "reliant-finish-")
	if err != nil {
		return ReasonUnverified, err.Error()
	}
	defer os.RemoveAll(tmp)
	index, err := stagedTree(ctx, q, tmp)
	if err != nil {
		return ReasonUnverified, "its staged state cannot be read: " + err.Error()
	}
	if index != c.IndexTree {
		return ReasonRecentlyActive, "its staged state changed after the capture"
	}
	disk, err := diskTree(ctx, q, tmp)
	if err != nil {
		return ReasonUnverified, "its files cannot be read: " + err.Error()
	}
	out, err := git(ctx, q, nil, "diff-tree", "-r", "--no-renames", "--name-status", c.DiskTree, disk)
	if err != nil {
		return ReasonUnverified, "its files cannot be compared with the capture: " + err.Error()
	}
	var changed []string
	for _, line := range strings.Split(out, "\n") {
		status, path, ok := strings.Cut(line, "\t")
		if ok && status != "D" {
			changed = append(changed, path)
		}
	}
	if len(changed) > 0 {
		return ReasonDirty, "changed after its deletion began: " + changedSummary(strings.Join(changed, "\n"))
	}
	tips, err := orphanTips(ctx, q, "")
	if err != nil {
		return ReasonUnverified, "its private history cannot be read: " + err.Error()
	}
	for _, tip := range tips {
		if _, err := git(ctx, q, nil, "merge-base", "--is-ancestor", tip, c.Commit); err != nil {
			return ReasonUnreachable, fmt.Sprintf("commit %s is reachable only from this checkout and not from its capture", short(tip))
		}
	}
	if v := dataHold(ctx, q, nil); !v.safe() {
		return v.Reason, v.Detail
	}
	return "", ""
}
