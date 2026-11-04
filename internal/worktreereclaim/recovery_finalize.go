// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const RecoveryIdleFloor = 7 * 24 * time.Hour
const RecoveryAuthorizationLifetime = 30 * time.Second

// RecoveryLease excludes runtime admissions for these paths, including managed
// tools and background processes. Network operations must precede its acquisition.
type RecoveryLease interface {
	Check(context.Context) error
	Release()
}

// RecoveryFinalizeDeps names the proof boundaries. No missing boundary defaults
// to success. Verify must independently fetch the exact remote ref and closure.
type RecoveryFinalizeDeps struct {
	Verify          func(context.Context, RecoveryCapture) (RecoveryReceipt, error)
	Observe         func(context.Context, string) (RecoveryActivity, error)
	Acquire         func(context.Context, []string) (RecoveryLease, error)
	CheckProcessUse func(context.Context, []string) error
}

// RecoveryWorkspaceDigest binds authorization to the complete ordered descriptor
// set, not just the last checkout's manifest. Credentials never enter this hash.
func RecoveryWorkspaceDigest(captures []RecoveryCapture) string {
	cs := append([]RecoveryCapture(nil), captures...)
	sort.Slice(cs, func(i, j int) bool { return cs[i].RepoID < cs[j].RepoID })
	raw, _ := json.Marshal(cs)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// RecoveryIdleEligible accepts only continuous, recent, seven-day observation.
// Server generation is separate from the daemon observation generation.
func RecoveryIdleEligible(a RecoveryActivity, now time.Time) bool {
	if !a.Complete || a.ObservationID == "" || a.ObservedSince.IsZero() || a.LastActivity.IsZero() || a.ObservedThrough.IsZero() {
		return false
	}
	if a.ObservedSince.After(a.LastActivity) || a.LastActivity.After(a.ObservedThrough) || a.ObservedThrough.After(now) || now.Sub(a.ObservedThrough) > time.Minute {
		return false
	}
	return now.Sub(a.ObservedSince) >= RecoveryIdleFloor && now.Sub(a.LastActivity) >= RecoveryIdleFloor
}

// writeRecoveryOrigin orders crash repair metadata before the rename. Both the
// marker contents and its directory entry must reach stable storage first.
func writeRecoveryOrigin(q, original string) error {
	f, err := os.OpenFile(originMarker(q), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(original)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(q))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func recoveryHeld(reason HeldReason, detail string) RecoveryResponse {
	return RecoveryResponse{Outcome: OutcomeHeld, Reason: reason, Detail: detail}
}

// recoveryLayout refuses unknown/overlapping repos and loose root files. The
// Git-only envelope cannot preserve those files; the old local root copy is NOT
// a fallback. This deliberately does not call structureOf's clean-data gate.
func (r *Reclaimer) recoveryLayout(ctx context.Context, req RecoveryRequest) ([]string, error) {
	w := req.Worktree
	if _, err := uuid.Parse(w.ID); err != nil {
		return nil, errors.New("invalid worktree ID")
	}
	if !r.rootOK() || w.Path == "" || !within(r.Root, w.Path) || within(filepath.Join(r.Root, ".reclaim"), w.Path) {
		return nil, errors.New("workspace is outside the available worktree root")
	}
	if w.KeepFiles || (w.State != StateActive && (w.State != StateArchived || w.Fence == "")) {
		return nil, errors.New("workspace is not eligible for recovery capture")
	}
	if len(req.Captures) == 0 || len(w.Checkouts) != len(req.Captures) {
		return nil, errors.New("complete checkout descriptors are required")
	}
	sc := scanWorkspace(ctx, w.Path)
	if sc.Truncated || len(sc.Outside) > 0 || len(sc.Repos) != len(req.Captures) {
		return nil, errors.New("workspace contains uncaptured files or repositories")
	}
	seen, rels := map[string]bool{}, map[string]bool{}
	paths := make([]string, 0, len(req.Captures))
	for _, c := range req.Captures {
		if c.Version != 1 || c.WorktreeID != w.ID || c.CaptureID == "" || c.RepoID == "" || c.RemoteURL == "" || seen[c.RepoID] {
			return nil, errors.New("invalid or duplicate capture descriptor")
		}
		rel := filepath.FromSlash(c.Rel)
		if rel != "" && (filepath.IsAbs(rel) || filepath.Clean(rel) != rel || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return nil, errors.New("invalid checkout relative path")
		}
		if rels[rel] {
			return nil, errors.New("duplicate checkout path")
		}
		seen[c.RepoID], rels[rel] = true, true
		p := filepath.Join(w.Path, rel)
		found := false
		for _, declared := range w.Checkouts {
			if declared.Rel == c.Rel {
				found = true
			}
		}
		if !found || !isLinkedWorktree(p) {
			return nil, errors.New("capture is not a declared linked checkout")
		}
		found = false
		for _, actual := range sc.Repos {
			if samePath(actual, p) {
				found = true
			}
		}
		if !found {
			return nil, errors.New("checkout inventory differs")
		}
		for _, previous := range paths {
			if within(previous, p) || within(p, previous) {
				return nil, errors.New("nested captures are unsupported")
			}
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// FinalizeRecovery is a distinct protocol: legacy Remove/Trees never authorize
// it. All remote proofs finish before short admission; every checkout is parked
// and re-inventoried before any removal. Errors preserve or move back originals.
func (r *Reclaimer) FinalizeRecovery(ctx context.Context, req RecoveryRequest, deps RecoveryFinalizeDeps) RecoveryResponse {
	if deps.Verify == nil || deps.Observe == nil || deps.Acquire == nil || deps.CheckProcessUse == nil {
		return recoveryHeld(ReasonUnverified, "recovery safety dependencies unavailable")
	}
	a := req.Authorization
	w := req.Worktree
	if _, err := uuid.Parse(w.ID); err != nil {
		return recoveryHeld(ReasonUnverified, "invalid worktree identity")
	}
	validAuth := func() bool {
		return a != nil && a.RecoveryID != "" && a.RecoveryID == req.RecoveryID && a.WorktreeID == w.ID && a.Fence == w.Fence && w.State == StateArchived && w.Fence != "" && a.CaptureID != "" && a.ObservationID == req.Activity.ObservationID && a.ManifestDigest == RecoveryWorkspaceDigest(req.Captures) && a.ExpiresAt.After(r.now()) && a.ExpiresAt.Sub(r.now()) <= RecoveryAuthorizationLifetime
	}
	if !validAuth() {
		return recoveryHeld(ReasonStale, "missing, expired or mismatched recovery authorization")
	}
	if !RecoveryIdleEligible(req.Activity, r.now()) {
		return recoveryHeld(ReasonObservationGap, "seven days of complete inactivity evidence are required")
	}
	if len(req.Receipts) != len(req.Captures) {
		return recoveryHeld(ReasonRemoteProof, "every checkout requires a persisted receipt")
	}
	receipts := map[string]RecoveryReceipt{}
	for _, receipt := range req.Receipts {
		if _, ok := receipts[receipt.Capture.RepoID]; ok {
			return recoveryHeld(ReasonRemoteProof, "duplicate receipt")
		}
		receipts[receipt.Capture.RepoID] = receipt
	}
	for _, c := range req.Captures {
		old, ok := receipts[c.RepoID]
		if !ok || !reflect.DeepEqual(old.Capture, c) || old.VerifiedAt.IsZero() || old.VerifierVersion == "" || c.CaptureID != a.CaptureID {
			return recoveryHeld(ReasonRemoteProof, "receipt does not match the authorized capture")
		}
		fresh, err := deps.Verify(ctx, c)
		if err != nil || !reflect.DeepEqual(fresh.Capture, c) || fresh.VerifiedAt.IsZero() || fresh.VerifierVersion == "" {
			return recoveryHeld(ReasonRemoteProof, "fresh remote verification failed; originals retained")
		}
	}
	if !validAuth() {
		return recoveryHeld(ReasonStale, "authorization expired during verification")
	}
	ctx, cancelAuthorization := context.WithDeadline(ctx, a.ExpiresAt)
	defer cancelAuthorization()
	m := r.pathLock(w.Path)
	if err := m.lock(ctx); err != nil {
		return recoveryHeld(ReasonInUse, "workspace busy")
	}
	defer m.unlock()
	unlock, err := r.rowLock(ctx, w.ID)
	if err != nil {
		return recoveryHeld(ReasonInUse, "workspace row busy")
	}
	defer unlock()
	if parked := r.quarantined(w.ID); len(parked) > 0 {
		return recoveryHeld(ReasonQuarantined, "previous quarantine requires repair before retry")
	}
	paths, err := r.recoveryLayout(ctx, req)
	if err != nil {
		return recoveryHeld(ReasonUnsupportedRecovery, err.Error())
	}
	// Include the quarantine parent before moving, so new admissions cannot race
	// the rename. No transport or remote I/O occurs after this point.
	protected := append(append([]string{w.Path, r.quarantineRoot(w.ID)}, paths...), r.Root+string(filepath.Separator)+".reclaim"+string(filepath.Separator)+w.ID)
	lease, err := deps.Acquire(ctx, protected)
	if err != nil {
		return recoveryHeld(ReasonInUse, "runtime admission unavailable")
	}
	if lease == nil {
		return recoveryHeld(ReasonInUse, "runtime admission unavailable")
	}
	defer lease.Release()
	observed, err := deps.Observe(ctx, w.Path)
	if err != nil || observed.ObservationID != req.Activity.ObservationID || observed.Generation != req.Activity.Generation || !observed.LastActivity.Equal(req.Activity.LastActivity) || !RecoveryIdleEligible(observed, r.now()) {
		return recoveryHeld(ReasonObservationGap, "activity changed since capture")
	}
	check := func() error {
		if !validAuth() {
			return errors.New("authorization expired")
		}
		if err := lease.Check(ctx); err != nil {
			return err
		}
		return deps.CheckProcessUse(ctx, protected)
	}
	if err := check(); err != nil {
		return recoveryHeld(ReasonInUse, "fresh active-use check failed")
	}
	for i, p := range paths {
		digest, err := RecoveryInventoryDigest(ctx, p)
		if err != nil || digest != req.Captures[i].InventoryDigest {
			return recoveryHeld(ReasonDirty, "inventory changed since capture")
		}
	}
	if hold := claim(ctx, w, paths); hold != nil {
		return recoveryHeld(hold.Reason, hold.Detail)
	}
	type parkedCheckout struct {
		original, path, common string
		capture                RecoveryCapture
	}
	var parked []parkedCheckout
	result := RecoveryResponse{Outcome: OutcomeHeld, Reason: ReasonUnverified}
	rollback := func(detail string) RecoveryResponse {
		// A cancelled request still gets a bounded opportunity to return originals.
		repairCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, remaining := r.recoverLocked(repairCtx, w.ID)
		result.Detail = detail
		if len(remaining) > 0 {
			result.Reason = ReasonQuarantined
			result.Detail += "; originals retained in quarantine"
		}
		return result
	}
	for i, p := range paths {
		if err := check(); err != nil {
			return rollback("admission changed before quarantine")
		}
		info, err := inspectLock(ctx, p)
		if err != nil || info.Reason != ArchivedLockReason(w.ID, w.Fence) {
			return rollback("archive fence changed")
		}
		q := r.newQuarantinePath(w, p)
		if err := os.MkdirAll(filepath.Dir(q), 0700); err != nil {
			return rollback("cannot create quarantine")
		}
		if err := writeRecoveryOrigin(q, p); err != nil {
			return rollback("cannot durably record quarantine origin")
		}
		if _, err := git(ctx, "", nil, "--git-dir="+info.CommonDir, "worktree", "unlock", p); err != nil {
			return rollback("cannot unlock for quarantine")
		}
		// The registration is now unlocked. Reusing its pre-unlock state
		// makes setLock attempt a second unlock, which Git rejects; that
		// prevented every finalizer from reaching quarantine verification.
		info.Locked = false
		if _, err := git(ctx, "", nil, "--git-dir="+info.CommonDir, "worktree", "move", p, q); err != nil {
			_ = setLock(ctx, info, p, ArchivedLockReason(w.ID, w.Fence))
			return rollback("cannot quarantine checkout")
		}
		parked = append(parked, parkedCheckout{p, q, info.CommonDir, req.Captures[i]})
		if err := setLock(ctx, info, q, ArchivedLockReason(w.ID, w.Fence)); err != nil {
			return rollback("cannot lock quarantined checkout")
		}
	}
	verifyParked := func(p parkedCheckout) error {
		if err := check(); err != nil {
			return err
		}
		info, err := inspectLock(ctx, p.path)
		if err != nil || !info.Registered || info.Reason != ArchivedLockReason(w.ID, w.Fence) {
			return errors.New("quarantine fence changed")
		}
		sc := scanWorkspace(ctx, p.path)
		if sc.Truncated || len(sc.Repos) != 1 || len(sc.Outside) > 0 {
			return errors.New("quarantine layout changed")
		}
		digest, err := RecoveryInventoryDigest(ctx, p.path)
		if err != nil {
			return err
		}
		if digest != p.capture.InventoryDigest {
			return errors.New("quarantine inventory changed")
		}
		return nil
	}
	for _, p := range parked {
		if err := verifyParked(p); err != nil {
			return rollback("quarantine verification failed; nothing removed")
		}
	}
	for _, p := range parked {
		if err := verifyParked(p); err != nil {
			return rollback("inventory or active use changed before removal")
		}
		if err := check(); err != nil {
			return rollback("authorization or admission expired immediately before removal")
		}
		if _, err := git(ctx, "", nil, "--git-dir="+p.common, "worktree", "unlock", p.path); err != nil {
			return rollback("cannot unlock verified checkout")
		}
		if err := check(); err != nil {
			_ = setLock(ctx, lockInfo{CommonDir: p.common}, p.path, ArchivedLockReason(w.ID, w.Fence))
			return rollback("authorization expired before removal")
		}
		// Git owns deletion; never fall back to RemoveAll on failure.
		if _, err := git(ctx, "", nil, "--git-dir="+p.common, "worktree", "remove", "--force", p.path); err != nil {
			_ = setLock(ctx, lockInfo{CommonDir: p.common}, p.path, ArchivedLockReason(w.ID, w.Fence))
			return rollback("git refused verified removal")
		}
		_ = os.Remove(originMarker(p.path))
		result.RemovedRepoIDs = append(result.RemovedRepoIDs, p.capture.RepoID)
	}
	if !removeEmptyTree(w.Path) {
		if _, err := os.Lstat(w.Path); !os.IsNotExist(err) {
			return rollback("workspace root changed; remaining files retained")
		}
	}
	result.Outcome, result.Reason, result.Detail = OutcomeRemoved, "", ""
	return result
}

// CaptureRecoveryWorkspace captures the exact declared layout atomically with
// respect to other reclaimer requests. It never publishes or removes anything.
func (r *Reclaimer) CaptureRecoveryWorkspace(ctx context.Context, req RecoveryRequest) RecoveryResponse {
	m := r.pathLock(req.Worktree.Path)
	if err := m.lock(ctx); err != nil {
		return recoveryHeld(ReasonInUse, "workspace busy")
	}
	defer m.unlock()
	if _, err := uuid.Parse(req.Worktree.ID); err != nil {
		return recoveryHeld(ReasonUnverified, "invalid worktree identity")
	}
	unlock, err := r.rowLock(ctx, req.Worktree.ID)
	if err != nil {
		return recoveryHeld(ReasonInUse, "workspace row busy")
	}
	defer unlock()
	// Exact row serialization ensures an old remover has stopped before repair.
	// Unknown missing checkouts remain held; never infer their removal succeeded.
	if _, remaining := r.recoverLocked(ctx, req.Worktree.ID); len(remaining) > 0 {
		return recoveryHeld(ReasonQuarantined, "parked originals could not be returned; retry repair")
	}
	paths, err := r.recoveryLayout(ctx, req)
	if err != nil {
		return recoveryHeld(ReasonUnsupportedRecovery, err.Error())
	}
	out := RecoveryResponse{Outcome: OutcomeCaptured, Activity: req.Activity}
	for i, p := range paths {
		capture, err := CaptureRecovery(ctx, p, req.Captures[i])
		if err != nil {
			return recoveryHeld(ReasonUnsupportedRecovery, fmt.Sprintf("capture refused for repo %s", req.Captures[i].RepoID))
		}
		out.Captures = append(out.Captures, capture)
	}
	return out
}
