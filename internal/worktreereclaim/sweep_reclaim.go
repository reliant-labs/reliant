// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Reclaiming one checkout (the machine sweep's deletion path):
//
//  1. Under the checkout's locks (in-process path lock; the row's file lock
//     for a Reliant workspace, so the server's reconcile waits; a per-key file
//     lock otherwise), it is evaluated again from scratch.
//  2. Capture #1, in place (capture.go). Nothing is locked or moved yet.
//  3. The daemon's admission lease is taken on the original and quarantine
//     paths, and processes are inspected (cwd, exe, open and mapped files).
//  4. Markers go down next to the quarantine path (.origin: where it came
//     from; .prelock: the lock it had; .capture: its capture), then the
//     checkout is moved to a fresh random path in a .reliant-reclaim
//     directory beside it (same directory, so the same filesystem) and
//     locked "reliant: reclaiming <key> <fence>". Its original path no longer
//     exists for anything to cd into.
//  5. The parked copy is inventoried by descriptor and VERIFIED against the
//     capture: same HEAD, same staged tree, same files, no private history
//     the capture misses; an auto-safe one must still be clean and pushed,
//     an approved one must still have the approved fingerprint. Any
//     difference moves it back and holds it.
//  6. Deletion is fd-anchored (retirementTree.remove): every entry is checked
//     against the inventory before it is unlinked, and the lease and process
//     inspection run again at least every fullCheckEvery. A change stops it,
//     keeping what remains and the capture.
//  7. `git worktree remove` (never --force) retires the registration. The
//     branch is never deleted.
//
// A crash at any step is recovered on the next pass (recoverReclaim): a
// parked checkout without .disposing goes back to its path, its old lock
// restored; one with .disposing is held and reported, its capture intact.

const (
	// reclaimQuarantineDir is the directory, beside a checkout, it is parked
	// in while it is verified and deleted.
	reclaimQuarantineDir = ".reliant-reclaim"
	reclaimingLockPrefix = "reliant: reclaiming "
	// fullCheckEvery bounds how long deletion goes between full checks (the
	// lease and every process's cwd, files and maps). Each entry is still
	// checked against its inventory immediately before it is unlinked. A
	// full check per entry cost ~20ms × 100k entries for one checkout with
	// its node_modules, which outlived the two-minute lease mid-deletion.
	fullCheckEvery = 250 * time.Millisecond
	// leaseRenewAfter renews the admission lease before its two minutes end.
	leaseRenewAfter = 90 * time.Second
)

// reclaimingLock is the lock reason of a checkout being reclaimed.
func reclaimingLock(key, fence string) string { return reclaimingLockPrefix + key + " " + fence }

type reclaimingLockInfo struct{ Key, Fence string }

func parseReclaimingLock(reason string) (reclaimingLockInfo, bool) {
	rest, ok := strings.CutPrefix(reason, reclaimingLockPrefix)
	if !ok {
		return reclaimingLockInfo{}, false
	}
	f := strings.Fields(rest)
	if len(f) != 2 {
		return reclaimingLockInfo{}, false
	}
	return reclaimingLockInfo{Key: f[0], Fence: f[1]}, true
}

func isReclaimingLock(reason string) bool { return strings.HasPrefix(reason, reclaimingLockPrefix) }

// inQuarantine reports whether path is a parked checkout: under a
// .reliant-reclaim directory, under the archive protocol's <root>/.reclaim,
// or forge's reaper's quarantine.
func inQuarantine(root, path string) bool {
	if root != "" && within(filepath.Join(root, ".reclaim"), path) {
		return true
	}
	for _, seg := range strings.Split(filepath.ToSlash(path), "/") {
		if seg == reclaimQuarantineDir || strings.HasPrefix(seg, ".forge-reclaim-snap-") {
			return true
		}
	}
	return false
}

// sweepQuarantinePath is a fresh path that cannot collide:
// <dir of checkout>/.reliant-reclaim/<key>/<unix-nanos>-<rand>.
func sweepQuarantinePath(orig, key string) string {
	for {
		var b [4]byte
		_, _ = rand.Read(b[:])
		q := filepath.Join(filepath.Dir(orig), reclaimQuarantineDir, sanitizeRefComponent(key), fmt.Sprintf("%d-%s", time.Now().UnixNano(), hex.EncodeToString(b[:])))
		if _, err := os.Lstat(q); os.IsNotExist(err) {
			return q
		}
	}
}

// reclaimMarker is what .capture records.
type reclaimMarker struct {
	Ref       string `json:"ref"`
	CommonDir string `json:"common_dir"`
	Orig      string `json:"orig"`
}

// Tombstone is written beside a reclaimed checkout that lived outside the
// worktree root (<path>.reliant-reclaimed), for whoever finds the directory
// gone.
type Tombstone struct {
	Path        string    `json:"path"`
	Ref         string    `json:"ref"`
	CommonDir   string    `json:"common_dir"`
	Repo        string    `json:"repo,omitempty"`
	Branch      string    `json:"branch,omitempty"`
	Reason      string    `json:"reason"`
	Restore     string    `json:"restore"`
	ReclaimedAt time.Time `json:"reclaimed_at"`
}

// TombstoneSuffix names a tombstone: <reclaimed path><suffix>.
const TombstoneSuffix = ".reliant-reclaimed"

// keyLock takes the cross-process lock for one key.
func (r *Reclaimer) keyLock(ctx context.Context, dir string, e *SweepEntry) (func(), error) {
	if e.RowID != "" && r.Root != "" {
		return r.rowLock(ctx, e.RowID)
	}
	locks := filepath.Join(dir, "locks")
	if err := os.MkdirAll(locks, 0o700); err != nil {
		return nil, err
	}
	return lockFile(ctx, filepath.Join(locks, sanitizeRefComponent(e.Key)+".lock"))
}

// reclaimEntry reclaims one evaluated entry. approved is the fingerprint a
// person approved ("" for an auto-safe entry). It sets the entry's outcome.
func (r *Reclaimer) reclaimEntry(ctx context.Context, e *SweepEntry, opts SweepOptions, dir, approved string) {
	m := r.pathLock(e.Path)
	if m.lock(ctx) != nil {
		e.Outcome, e.Detail = OutcomeSweepDeferred, DetailTimedOut
		return
	}
	defer m.unlock()
	unlock, err := r.keyLock(ctx, dir, e)
	if err != nil {
		e.Outcome, e.Detail = OutcomeSweepDeferred, "could not take its lock: "+err.Error()
		return
	}
	defer unlock()

	// 1. Again, from scratch, under the locks.
	class := e.Class
	fresh := SweepEntry{Key: e.Key, Path: e.Path, CommonDir: e.CommonDir, Repo: e.Repo, Branch: e.Branch, Head: e.Head, Base: e.Base, Owner: e.Owner, RowID: e.RowID}
	if info, err := inspectLock(ctx, e.Path); err == nil && info.Locked {
		fresh.Lock = info.Reason
	} else if err != nil {
		e.hold(ReasonUnverified, err.Error())
		return
	}
	r.evaluate(ctx, &fresh, opts)
	if fresh.Class != class || (approved != "" && fresh.Fingerprint != approved) {
		*e = fresh
		if fresh.Class == ClassNeedsApproval {
			e.Outcome = OutcomeAwaiting
		} else if e.Outcome == "" {
			e.Outcome = OutcomeSweepHeld
		}
		if approved != "" && fresh.Fingerprint != approved {
			e.Detail = "changed since it was approved; " + e.Detail
		}
		return
	}
	if r.Acquire == nil || r.ProcessUse == nil {
		e.hold(ReasonUnverified, "runtime admission or process inspection is unavailable")
		return
	}

	// 2. Capture, in place.
	meta := CaptureMeta{Path: e.Path, Branch: e.Branch, RowID: e.RowID, Class: string(class), IdleSeconds: e.IdleSeconds, Version: opts.Version, CapturedAt: r.now().UTC()}
	capt, err := captureCheckout(ctx, e.Path, e.Key, meta, opts.Capture)
	if err != nil {
		if errors.Is(err, errCaptureEnvelope) {
			e.hold(ReasonTooLarge, err.Error())
		} else {
			e.hold(ReasonUnverified, "capture failed, nothing was removed: "+err.Error())
		}
		return
	}
	e.CaptureRef = capt.Ref
	if class == ClassAutoSafe && !(capt.DiskTree == headTree(ctx, e.Path) && capt.IndexTree == capt.DiskTree && len(capt.Orphans) == 0) {
		e.hold(ReasonDirty, "it changed before it could be reclaimed; it needs approval now")
		return
	}

	orig := e.Path
	q := sweepQuarantinePath(orig, e.Key)
	paths := []string{orig, q}
	lease, err := r.Acquire(ctx, paths)
	if err != nil || lease == nil {
		e.hold(ReasonInUse, "daemon work is running in it")
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
				return errors.New("daemon work started in it")
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
		e.hold(ReasonInUse, err.Error())
		return
	}

	// 4. Markers, then park.
	prior, err := inspectLock(ctx, orig)
	if err != nil || !prior.Registered {
		e.hold(ReasonUnverified, "its registration could not be read")
		return
	}
	priorReason := ""
	if prior.Locked {
		priorReason = prior.Reason
	}
	if err := os.MkdirAll(filepath.Dir(q), 0o700); err != nil {
		e.hold(ReasonUnverified, err.Error())
		return
	}
	marker, _ := json.Marshal(reclaimMarker{Ref: capt.Ref, CommonDir: capt.CommonDir, Orig: orig})
	dropMarkers := func() {
		for _, s := range []string{".origin", ".prelock", ".capture", ".disposing"} {
			_ = os.Remove(q + s)
		}
		removeEmptyQuarantine(q)
	}
	for _, f := range []struct{ suffix, data string }{{".capture", string(marker)}, {".prelock", priorReason}, {".origin", orig}} {
		if err := persistRetirementState(q+f.suffix, []byte(f.data)); err != nil {
			dropMarkers()
			e.hold(ReasonUnverified, "could not record the reclaim: "+err.Error())
			return
		}
	}
	common := prior.CommonDir
	if prior.Locked {
		if _, err := git(ctx, "", nil, "--git-dir="+common, "worktree", "unlock", orig); err != nil {
			dropMarkers()
			e.hold(ReasonUnverified, err.Error())
			return
		}
	}
	relock := func(path string) {
		if priorReason != "" {
			_ = setLock(context.WithoutCancel(ctx), lockInfo{CommonDir: common}, path, priorReason)
		}
	}
	if _, err := git(ctx, "", nil, "--git-dir="+common, "worktree", "move", orig, q); err != nil {
		relock(orig)
		dropMarkers()
		e.hold(ReasonUnverified, "could not park it: "+err.Error())
		return
	}
	fence := strconv.FormatInt(time.Now().UnixNano(), 10)
	putBack := func(reason HeldReason, detail string) {
		bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if _, err := os.Lstat(orig); !os.IsNotExist(err) {
			e.hold(ReasonQuarantined, fmt.Sprintf("parked at %s and kept: its original path is occupied again (%s: %s)", q, reason, detail))
			return
		}
		_, _ = git(bctx, "", nil, "--git-dir="+common, "worktree", "unlock", q)
		if _, err := git(bctx, "", nil, "--git-dir="+common, "worktree", "move", q, orig); err != nil {
			_ = setLock(bctx, lockInfo{CommonDir: common}, q, reclaimingLock(e.Key, fence))
			e.hold(ReasonQuarantined, fmt.Sprintf("parked at %s and kept: it could not be moved back: %v", q, err))
			return
		}
		relock(orig)
		dropMarkers()
		e.hold(reason, detail)
	}
	if err := setLock(ctx, lockInfo{CommonDir: common}, q, reclaimingLock(e.Key, fence)); err != nil {
		putBack(ReasonUnverified, err.Error())
		return
	}

	// 5. Inventory and verify the parked copy.
	tree, err := inspectRetirementTree(ctx, q)
	if err != nil {
		putBack(ReasonUnverified, "parked checkout cannot be inventoried: "+err.Error())
		return
	}
	defer tree.close()
	inspection = append(inspection, tree.inspectionFiles()...)
	if err := check(true); err != nil {
		putBack(ReasonInUse, err.Error())
		return
	}
	if r.verifyHook != nil {
		r.verifyHook(q)
	}
	if err := verifyCapture(ctx, q, capt); err != nil {
		putBack(ReasonRecentlyActive, "changed after it was captured; nothing was removed: "+err.Error())
		return
	}
	switch {
	case class == ClassAutoSafe:
		if v := classifyCheckout(ctx, q, e.Base, nil, e.RowID, r.sweepProbe(opts)); !v.safe() {
			putBack(v.Reason, v.Detail)
			return
		}
	case approved != "":
		if fp, err := fingerprint(ctx, q, orig, scanCheckout(ctx, q).Newest); err != nil || fp != approved {
			putBack(ReasonRecentlyActive, "changed since it was approved")
			return
		}
	}
	gitState, err := inspectRetirementGitState(ctx, q)
	if err != nil {
		putBack(ReasonUnverified, err.Error())
		return
	}
	if err := tree.validate(ctx); err != nil {
		putBack(ReasonRecentlyActive, "changed while it was verified: "+err.Error())
		return
	}
	if err := check(true); err != nil {
		putBack(ReasonInUse, err.Error())
		return
	}
	if _, err := os.Lstat(orig); !os.IsNotExist(err) {
		putBack(ReasonInUse, "its original path became occupied")
		return
	}

	// 6. Delete.
	if err := persistRetirementState(q+".disposing", []byte(fence)); err != nil {
		putBack(ReasonUnverified, "could not record the disposal: "+err.Error())
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
		e.hold(ReasonQuarantined, fmt.Sprintf("deletion stopped at %s; what remains is kept, and all of it is in %s: %v", q, capt.Ref, err))
		return
	}
	// 7. The directory is gone; git only retires the registration. Never a
	// repository-wide prune: that drops other missing checkouts' reflogs.
	rctx := context.WithoutCancel(ctx)
	_, _ = git(rctx, "", nil, "--git-dir="+common, "worktree", "unlock", q)
	if _, err := git(rctx, "", nil, "--git-dir="+common, "worktree", "remove", q); err != nil {
		e.Class, e.Outcome, e.FreedBytes, e.Reason = class, OutcomeReclaimed, tree.bytes, ""
		e.Detail = fmt.Sprintf("files removed (captured at %s); its git registration was kept: %v", capt.Ref, err)
		return
	}
	dropMarkers()
	e.Class, e.Outcome, e.FreedBytes = class, OutcomeReclaimed, tree.bytes
	e.Detail = "captured at " + capt.Ref + "; restore with: reliant worktree restore " + capt.Ref + " --repo " + capt.CommonDir
	if r.Root == "" || !within(r.Root, orig) {
		ts, _ := json.MarshalIndent(Tombstone{Path: orig, Ref: capt.Ref, CommonDir: capt.CommonDir, Repo: e.Repo, Branch: e.Branch,
			Reason: string(class), Restore: "reliant worktree restore " + capt.Ref + " --repo " + capt.CommonDir, ReclaimedAt: r.now().UTC()}, "", "  ")
		_ = os.WriteFile(orig+TombstoneSuffix, append(ts, '\n'), 0o600)
	}
}

// headTree is HEAD's tree, or "".
func headTree(ctx context.Context, checkout string) string {
	t, _ := git(ctx, checkout, nil, "rev-parse", "--verify", "HEAD^{tree}")
	return t
}

// removeEmptyQuarantine removes q's key directory and the .reliant-reclaim
// directory above it, when empty.
func removeEmptyQuarantine(q string) {
	keyDir := filepath.Dir(q)
	if os.Remove(keyDir) == nil {
		_ = os.Remove(filepath.Dir(keyDir))
	}
}

// recoverReclaim settles a worktree whose lock says a reclaim was
// interrupted (the daemon died mid-way). A parked copy with no .disposing
// marker goes back to its original path, with the lock it had; one being
// disposed of is held, its capture named. A checkout at its own path with a
// reclaiming lock gets its old lock back.
func (r *Reclaimer) recoverReclaim(ctx context.Context, e *SweepEntry) {
	k, ok := parseReclaimingLock(e.Lock)
	if !ok {
		e.hold(ReasonUnmanaged, "unrecognized reclaim lock")
		return
	}
	common := e.CommonDir
	readMarker := func(q string) (orig, prelock string, m reclaimMarker, ok bool) {
		b, err := os.ReadFile(q + ".origin")
		if err != nil || len(b) == 0 {
			return "", "", m, false
		}
		p, _ := os.ReadFile(q + ".prelock")
		c, _ := os.ReadFile(q + ".capture")
		_ = json.Unmarshal(c, &m)
		return string(b), string(p), m, true
	}
	if inQuarantine("", e.Path) {
		q := e.Path
		orig, prelock, m, ok := readMarker(q)
		if !ok {
			e.hold(ReasonQuarantined, "parked at "+q+" with no record of where it came from; kept")
			return
		}
		if _, err := os.Lstat(q + ".disposing"); err == nil {
			e.Path = orig
			e.CaptureRef = m.Ref
			e.hold(ReasonQuarantined, fmt.Sprintf("a deletion was interrupted at %s; what remains is kept there, and all of it is in %s", q, m.Ref))
			return
		}
		if _, err := os.Lstat(orig); !os.IsNotExist(err) {
			e.Path = orig
			e.hold(ReasonQuarantined, fmt.Sprintf("parked at %s; its original path is occupied, so it was kept", q))
			return
		}
		if _, err := git(ctx, "", nil, "--git-dir="+common, "worktree", "unlock", q); err == nil {
			if _, err := git(ctx, "", nil, "--git-dir="+common, "worktree", "move", q, orig); err != nil {
				_ = setLock(ctx, lockInfo{CommonDir: common}, q, e.Lock)
				e.hold(ReasonQuarantined, fmt.Sprintf("parked at %s and could not be moved back: %v", q, err))
				return
			}
		}
		if prelock != "" {
			_ = setLock(ctx, lockInfo{CommonDir: common}, orig, prelock)
		}
		for _, s := range []string{".origin", ".prelock", ".capture"} {
			_ = os.Remove(q + s)
		}
		removeEmptyQuarantine(q)
		e.Path, e.Lock = orig, prelock
		e.Class, e.Outcome, e.Detail = ClassHeld, OutcomeMovedBack, "an interrupted reclaim was undone; it is back at its path"
		return
	}
	// At its own path: the reclaim stopped before the move or after moving
	// back. Its old lock is in the marker beside the parking spot, if any.
	prelock := ""
	if !strings.HasPrefix(k.Key, "p-") {
		prelock = LockReason(k.Key)
	}
	pattern := filepath.Join(filepath.Dir(e.Path), reclaimQuarantineDir, sanitizeRefComponent(k.Key), "*.origin")
	matches, _ := filepath.Glob(pattern)
	for _, mk := range matches {
		q := strings.TrimSuffix(mk, ".origin")
		if orig, p, _, ok := readMarker(q); ok && samePath(orig, e.Path) {
			if _, err := os.Lstat(q); os.IsNotExist(err) {
				prelock = p
				for _, s := range []string{".origin", ".prelock", ".capture", ".disposing"} {
					_ = os.Remove(q + s)
				}
				removeEmptyQuarantine(q)
			}
		}
	}
	if prelock != "" {
		_ = setLock(ctx, lockInfo{CommonDir: common}, e.Path, prelock)
	} else {
		_, _ = git(ctx, "", nil, "--git-dir="+common, "worktree", "unlock", e.Path)
	}
	e.Lock = prelock
	e.Class, e.Outcome, e.Detail = ClassHeld, OutcomeMovedBack, "an interrupted reclaim was undone"
}
