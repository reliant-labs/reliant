// Copyright (c) 2025 Reliant Labs
package worktreesweep

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

const (
	cleanupBatchSize = 5
	// cleanupBudget covers a whole confirmed clean-up, run detached from the
	// request that started it.
	cleanupBudget = 30 * time.Minute
)

// ErrOffline means the machine is not connected, so nothing can be cleaned.
var ErrOffline = errors.New("machine is offline")

// StorageStore is what the storage view and cleanup need from the database.
type StorageStore interface {
	Store
	ListHeldWorktreesForUser(ctx context.Context, userID string) ([]*core.HeldWorktree, error)
	ListDaemonStorageState(ctx context.Context, userID string) (map[string]string, error)
	// MergeUnownedWorktreeCleanupMetadata is MergeWorktreeCleanupMetadata for
	// an archived row that still has no machine on record, under the same row
	// lock a machine claiming it takes; false when the row has a machine, is
	// not archived, or fn wrote nothing.
	MergeUnownedWorktreeCleanupMetadata(ctx context.Context, id string, fn func(m *core.CleanupMetadata) bool) (bool, error)
}

// Held is one archived worktree a machine kept, for display.
type Held struct {
	WorktreeID  string
	Name        string
	ProjectName string
	Path        string
	Reason      string
	Detail      string
	SizeBytes   int64
	// Removable: the confirmed Clean up may remove it. Data, nested
	// repositories and files outside every checkout never are; the user
	// resolves those by hand.
	Removable bool
	// Cleaning: a clean-up is running for it right now.
	Cleaning bool
	// Forgettable: Forget may drop it — no machine is on record for it, and
	// every connected machine said it does not have the folder.
	Forgettable bool
}

// MachineStorage is one machine's storage picture. A machine appears only when
// it has something worth showing.
type MachineStorage struct {
	// DaemonID is "" for the archived worktrees no machine is on record for:
	// no disk, no Clean up, Online when any of the user's machines is.
	DaemonID string
	Held     []Held
	// Disk is the machine's last heartbeat disk report (diskwatch), every
	// volume fullest first; nil when it has not reported one.
	Disk *reliantv1.DiskReport
	// Severity grades the disk (diskwatch.SeverityOf): None unless a volume
	// is under pressure — the daemon's own hysteresis bit, for whichever
	// volume it is. On a cloud machine that is the build-cache/docker volume,
	// which the worktree-root reading this replaced never saw.
	Severity diskwatch.Severity
	Online   bool
	// Since is when the problem began: the pressure episode's start when the
	// disk is low, otherwise when the oldest held worktree was archived.
	Since time.Time
	// Processes is set while the machine refuses background heavy commands
	// for being near its process limit (internal/procguard): the count, the
	// limit and the largest process trees. Its own inbox item.
	Processes *reliantv1.ProcessReport
}

// StorageItem reports whether the machine has a disk or kept-workspace
// problem to show (as opposed to only a process one).
func (m *MachineStorage) StorageItem() bool { return m.DiskLow() || len(m.Held) > 0 }

// DiskLow reports whether at least one volume is under pressure.
func (m *MachineStorage) DiskLow() bool { return m.Severity > diskwatch.SeverityNone }

// StorageView builds the per-machine storage picture for a user.
func StorageView(ctx context.Context, store StorageStore, userID string) ([]*MachineStorage, error) {
	held, err := store.ListHeldWorktreesForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	states, err := store.ListDaemonStorageState(ctx, userID)
	if err != nil {
		return nil, err
	}
	connected, err := store.ListAttachedDaemonIDsForUser(ctx, userID, connectedLease)
	if err != nil {
		return nil, err
	}
	online := map[string]bool{}
	for _, id := range connected {
		online[id] = true
	}

	machines := map[string]*MachineStorage{}
	get := func(id string) *MachineStorage {
		m := machines[id]
		if m == nil {
			m = &MachineStorage{DaemonID: id, Online: online[id]}
			machines[id] = m
		}
		return m
	}
	for _, h := range held {
		wt := h.Worktree
		if wt.CleanupMetadata == nil {
			continue
		}
		meta := wt.CleanupMetadata
		// A worktree with no recorded machine cannot be cleaned up: nobody is
		// known to hold its directory. The periodic sweep asks every
		// connected machine, and the one that has the folder claims it. Until
		// then it is listed under no machine (DaemonID ""), where it can be
		// forgotten once no connected machine has it.
		daemonID := ptrString(wt.DaemonID)
		m := get(daemonID)
		if daemonID == "" {
			m.Online = len(connected) > 0
		}
		m.Held = append(m.Held, Held{
			WorktreeID: wt.ID, Name: wt.Name, ProjectName: h.ProjectName, Path: wt.Path,
			Reason: meta.HeldReason, Detail: meta.HeldDetail, SizeBytes: meta.SizeBytes,
			Removable:   daemonID != "" && worktreereclaim.HeldReason(meta.HeldReason).Removable(),
			Cleaning:    meta.CleaningNow(time.Now()),
			Forgettable: daemonID == "" && forgettable(meta, connected),
		})
		if wt.DeletedAt != nil && (m.Since.IsZero() || wt.DeletedAt.Before(m.Since)) {
			m.Since = *wt.DeletedAt
		}
	}
	for id, raw := range states {
		rep := diskwatch.Decode(raw)
		if rep == nil {
			continue
		}
		diskwatch.SortFullestFirst(rep.Volumes)
		if sev := diskwatch.SeverityOf(rep); sev > diskwatch.SeverityNone {
			m := get(id)
			m.Disk, m.Severity = rep, sev
			since := rep.PressureSinceMs
			if since == 0 {
				since = rep.ReportedAtMs
			}
			m.Since = time.UnixMilli(since)
		} else if m := machines[id]; m != nil {
			m.Disk = rep
		}
		if diskwatch.RefusingForProcesses(rep) {
			get(id).Processes = rep.Processes
		}
	}

	out := make([]*MachineStorage, 0, len(machines))
	for _, m := range machines {
		sort.Slice(m.Held, func(i, j int) bool { return m.Held[i].WorktreeID < m.Held[j].WorktreeID })
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DaemonID < out[j].DaemonID })
	return out, nil
}

// forgettable reports whether an archived row held for having no machine on
// record may be forgotten: at least one of the user's machines is connected,
// and every connected one answered that it does not have the folder. With no
// machine connected nothing has been asked, so nothing is offered.
func forgettable(meta *core.CleanupMetadata, connected []string) bool {
	if meta == nil || meta.DirectoryDeleted || meta.HeldReason != string(worktreereclaim.ReasonNoMachine) || len(connected) == 0 {
		return false
	}
	for _, id := range connected {
		if !slices.Contains(meta.AbsentOn, id) {
			return false
		}
	}
	return true
}

// ErrNotForgettable means Forget was refused: the row is not one no machine
// is on record for, or a connected machine has not said it lacks the folder.
var ErrNotForgettable = errors.New("not forgettable")

// ErrNotHeld means the user has no archived worktree held with that id.
var ErrNotHeld = errors.New("no such held worktree")

// Forget drops the record of the folder of one of the user's archived
// worktrees that no machine is on record for and no connected machine has
// (forgettable), so the hold no longer sits in Storage forever. Nothing on
// any disk is touched: the row is marked DirectoryDeleted (and Forgotten),
// which takes it out of the sweep and the Storage view, and stays in the
// archive. The check is repeated under the row's lock, and the row must still
// have no machine: a machine claiming it at the same moment wins.
func Forget(ctx context.Context, store StorageStore, userID, worktreeID string) error {
	held, err := store.ListHeldWorktreesForUser(ctx, userID)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(held, func(h *core.HeldWorktree) bool { return h.Worktree.ID == worktreeID }) {
		return ErrNotHeld
	}
	connected, err := store.ListAttachedDaemonIDsForUser(ctx, userID, connectedLease)
	if err != nil {
		return err
	}
	forgot, err := store.MergeUnownedWorktreeCleanupMetadata(ctx, worktreeID, func(m *core.CleanupMetadata) bool {
		if !forgettable(m, connected) {
			return false
		}
		m.DirectoryDeleted, m.Forgotten = true, true
		m.HeldReason, m.HeldDetail, m.SizeBytes, m.Rechecks, m.Transient = "", "", 0, 0, ""
		m.Cleaning, m.CleaningBy, m.CleaningUntil = false, "", nil
		m.AbsentOn = nil
		return true
	})
	if err != nil {
		return err
	}
	if !forgot {
		return ErrNotForgettable
	}
	return nil
}

// CleanupOutcome is the per-worktree result of a confirmed cleanup.
type CleanupOutcome string

const (
	CleanupRemoved CleanupOutcome = "removed"
	CleanupSkipped CleanupOutcome = "skipped"
	CleanupFailed  CleanupOutcome = "failed"
)

// CleanupResult reports one worktree.
type CleanupResult struct {
	WorktreeID   string
	Outcome      CleanupOutcome
	SnapshotRefs []string
	Message      string
}

// StartCleanup accepts a confirmed clean-up and runs it in the background on a
// context detached from the request, returning at once with what was accepted:
// the worktrees the user confirmed that this machine still holds AND may
// remove. Everything else is reported skipped immediately. Progress lives in
// each worktree's metadata (Cleaning, then removal or a held reason), which the
// inbox item shows; snapshot refs are recorded as the daemon reports them.
func (s *Sweeper) StartCleanup(ctx context.Context, store StorageStore, userID, daemonID string, wantIDs []string) (accepted []string, skipped []CleanupResult, err error) {
	connected, err := store.ListAttachedDaemonIDsForUser(ctx, userID, connectedLease)
	if err != nil {
		return nil, nil, err
	}
	if !slices.Contains(connected, daemonID) {
		return nil, nil, ErrOffline
	}
	held, err := store.ListHeldWorktreesForUser(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	byID := map[string]*core.HeldWorktree{}
	for _, h := range held {
		if wt := h.Worktree; wt.DaemonID != nil && *wt.DaemonID == daemonID {
			byID[wt.ID] = h
		}
	}
	var cands []*core.ReclaimCandidate
	for _, id := range wantIDs {
		h, ok := byID[id]
		switch {
		case !ok:
			skipped = append(skipped, CleanupResult{WorktreeID: id, Outcome: CleanupSkipped, Message: "no longer held on this machine"})
		case h.Worktree.CleanupMetadata.CleaningNow(s.now()):
			skipped = append(skipped, CleanupResult{WorktreeID: id, Outcome: CleanupSkipped, Message: "already being cleaned up"})
		case !worktreereclaim.HeldReason(h.Worktree.CleanupMetadata.HeldReason).Removable():
			skipped = append(skipped, CleanupResult{WorktreeID: id, Outcome: CleanupSkipped, Message: notRemovableMessage(h.Worktree.CleanupMetadata)})
		default:
			cands = append(cands, &core.ReclaimCandidate{Worktree: h.Worktree, OwnerUserID: userID, ProjectPath: h.ProjectPath})
			accepted = append(accepted, id)
		}
	}
	for _, c := range cands {
		s.mergeMeta(ctx, c.Worktree, func(m *core.CleanupMetadata) { s.markCleaning(m) })
	}
	if len(cands) == 0 {
		return nil, skipped, nil
	}
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupBudget)
	s.cleanups.Add(1)
	go func() {
		defer s.cleanups.Done()
		defer cancel()
		stop := s.keepCleaningAlive(bg, cands)
		defer stop()
		s.runCleanup(bg, store, userID, daemonID, cands)
	}()
	return accepted, skipped, nil
}

// cleaningLease is how long a clean-up flag counts without being renewed. The
// process running the clean-up renews it every cleaningRenew, so one that dies
// mid-way stops counting within a lease instead of disabling Clean up until a
// sweep notices.
const (
	cleaningLease = 3 * time.Minute
	cleaningRenew = time.Minute
)

func (s *Sweeper) markCleaning(m *core.CleanupMetadata) {
	until := s.now().Add(cleaningLease).UTC()
	m.Cleaning, m.CleaningBy, m.CleaningUntil = true, s.owner, &until
}

// keepCleaningAlive renews the lease on every worktree still being cleaned, and
// returns a function that stops renewing.
func (s *Sweeper) keepCleaningAlive(ctx context.Context, cands []*core.ReclaimCandidate) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(cleaningRenew)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				for _, c := range cands {
					s.mergeMetaIf(ctx, c.Worktree, func(m *core.CleanupMetadata, _ bool) bool {
						if !m.Cleaning || m.CleaningBy != s.owner {
							return false
						}
						s.markCleaning(m)
						return true
					})
				}
			}
		}
	}()
	return func() { close(done) }
}

// Cleanup is StartCleanup run to completion, for callers that wait (tests).
func (s *Sweeper) Cleanup(ctx context.Context, store StorageStore, userID, daemonID string, wantIDs []string) ([]CleanupResult, int64, error) {
	connected, err := store.ListAttachedDaemonIDsForUser(ctx, userID, connectedLease)
	if err != nil {
		return nil, 0, err
	}
	if !slices.Contains(connected, daemonID) {
		return nil, 0, ErrOffline
	}
	held, err := store.ListHeldWorktreesForUser(ctx, userID)
	if err != nil {
		return nil, 0, err
	}
	byID := map[string]*core.HeldWorktree{}
	for _, h := range held {
		if wt := h.Worktree; wt.DaemonID != nil && *wt.DaemonID == daemonID {
			byID[wt.ID] = h
		}
	}
	var results []CleanupResult
	var cands []*core.ReclaimCandidate
	for _, id := range wantIDs {
		h, ok := byID[id]
		switch {
		case !ok:
			results = append(results, CleanupResult{WorktreeID: id, Outcome: CleanupSkipped, Message: "no longer held on this machine"})
		case !worktreereclaim.HeldReason(h.Worktree.CleanupMetadata.HeldReason).Removable():
			results = append(results, CleanupResult{WorktreeID: id, Outcome: CleanupSkipped, Message: notRemovableMessage(h.Worktree.CleanupMetadata)})
		default:
			cands = append(cands, &core.ReclaimCandidate{Worktree: h.Worktree, OwnerUserID: userID, ProjectPath: h.ProjectPath})
		}
	}
	freed := s.runCleanupCollect(ctx, store, userID, daemonID, cands, &results)
	return results, freed, nil
}

func (s *Sweeper) runCleanup(ctx context.Context, store StorageStore, userID, daemonID string, cands []*core.ReclaimCandidate) {
	var results []CleanupResult
	s.runCleanupCollect(ctx, store, userID, daemonID, cands, &results)
}

// runCleanupCollect runs the confirmed two-phase flow for cands and returns the
// bytes freed. It records every outcome, including snapshot refs, into the
// worktree metadata as it goes, so nothing depends on the caller still waiting.
func (s *Sweeper) runCleanupCollect(ctx context.Context, store StorageStore, userID, daemonID string, cands []*core.ReclaimCandidate, results *[]CleanupResult) int64 {
	var freed int64
	t := target{userID, daemonID}
	live, err := s.store.ListLiveWorktreePathsForUser(ctx, userID)
	if err != nil {
		live = nil
	}
	for start := 0; start < len(cands); start += cleanupBatchSize {
		batch := cands[start:min(start+cleanupBatchSize, len(cands))]
		freed += s.cleanBatch(ctx, t, batch, live, results)
	}
	return freed
}

func (s *Sweeper) cleanBatch(ctx context.Context, t target, batch []*core.ReclaimCandidate, live []core.WorktreePath, out *[]CleanupResult) int64 {
	fail := func(c *core.ReclaimCandidate, msg string) {
		*out = append(*out, CleanupResult{WorktreeID: c.Worktree.ID, Outcome: CleanupFailed, Message: msg})
		s.mergeMeta(ctx, c.Worktree, func(m *core.CleanupMetadata) { m.Cleaning, m.CleaningBy, m.CleaningUntil = false, "", nil })
	}
	req, shared := s.describe(ctx, batch, live, false, nil)
	byID := map[string]*core.ReclaimCandidate{}
	for _, c := range batch {
		byID[c.Worktree.ID] = c
	}
	for _, h := range shared {
		delete(byID, h.cand.Worktree.ID)
		s.recordHold(ctx, h.cand.Worktree, h.reason, h.detail, 0, nil)
		*out = append(*out, CleanupResult{WorktreeID: h.cand.Worktree.ID, Outcome: CleanupSkipped, Message: "left in place: " + h.detail})
	}
	if len(req.Worktrees) == 0 {
		return 0
	}
	resp, err := s.send(ctx, t, cmdSnapshotRemove, snapshotRemoveTimeoutMs, req)
	if err != nil {
		for _, c := range byID {
			fail(c, err.Error())
		}
		return 0
	}
	var second []worktreereclaim.Worktree
	var freed int64
	pending := map[string][]string{} // refs saved in phase one, by worktree id
	finish := func(c *core.ReclaimCandidate, r worktreereclaim.Result) {
		// Snapshot refs are recorded the moment the daemon reports them, even
		// when the removal that follows does not happen.
		if len(r.SnapshotRefs) > 0 {
			s.mergeMeta(ctx, c.Worktree, func(m *core.CleanupMetadata) { m.SnapshotRefs = union(m.SnapshotRefs, r.SnapshotRefs) })
		}
		size := int64(0)
		if c.Worktree.CleanupMetadata != nil {
			size = c.Worktree.CleanupMetadata.SizeBytes
		}
		s.apply(ctx, t.userID, t.daemonID, c.Worktree, c.ProjectPath, r, true)
		res := CleanupResult{WorktreeID: c.Worktree.ID, SnapshotRefs: r.SnapshotRefs}
		switch r.Outcome {
		case worktreereclaim.OutcomeRemoved:
			res.Outcome = CleanupRemoved
			freed += size
		case worktreereclaim.OutcomeGone:
			res.Outcome, res.Message = CleanupRemoved, "the directory was already gone"
		case worktreereclaim.OutcomeHeld:
			res.Outcome, res.Message = CleanupSkipped, heldMessage(r)
		case worktreereclaim.OutcomeForeign:
			res.Outcome, res.Message = CleanupSkipped, "not on this machine"
		default:
			res.Outcome, res.Message = CleanupFailed, r.Error
		}
		*out = append(*out, res)
		s.mergeMeta(ctx, c.Worktree, func(m *core.CleanupMetadata) { m.Cleaning, m.CleaningBy, m.CleaningUntil = false, "", nil })
	}
	for _, r := range resp.Results {
		c := byID[r.ID]
		if c == nil {
			continue
		}
		delete(byID, r.ID)
		if r.Outcome != worktreereclaim.OutcomeClaimed {
			finish(c, r)
			continue
		}
		// Re-read the row right now; an unarchive since phase one cancels it.
		cur, err := s.store.GetWorktree(ctx, r.ID)
		if err != nil || cur == nil || cur.DeletedAt == nil || Fence(cur) != fenceOf(req, r.ID) {
			s.mergeMeta(ctx, c.Worktree, func(m *core.CleanupMetadata) {
				m.SnapshotRefs = union(m.SnapshotRefs, r.SnapshotRefs)
			})
			*out = append(*out, CleanupResult{WorktreeID: r.ID, Outcome: CleanupSkipped, SnapshotRefs: r.SnapshotRefs, Message: "the workspace was restored; left in place"})
			s.mergeMeta(ctx, c.Worktree, func(m *core.CleanupMetadata) { m.Cleaning, m.CleaningBy, m.CleaningUntil = false, "", nil })
			continue
		}
		for _, w := range req.Worktrees {
			if w.ID == r.ID {
				w.Remove, w.Trees = true, r.Trees
				second = append(second, w)
			}
		}
		// Record the refs now: phase two may take a while.
		s.mergeMeta(ctx, c.Worktree, func(m *core.CleanupMetadata) { m.SnapshotRefs = union(m.SnapshotRefs, r.SnapshotRefs) })
		c.Worktree.CleanupMetadata = withRefs(c.Worktree.CleanupMetadata, r.SnapshotRefs)
		byID[r.ID] = c
		pending[r.ID] = r.SnapshotRefs
	}
	for id, c := range byID {
		if !inSecond(second, id) {
			fail(c, "the machine did not answer for this worktree")
		}
	}
	if len(second) == 0 {
		return freed
	}
	resp2, err := s.send(ctx, t, cmdSnapshotRemove, snapshotRemoveTimeoutMs, worktreereclaim.Request{Worktrees: second})
	if err != nil {
		for _, w := range second {
			if c := byID[w.ID]; c != nil {
				fail(c, "saved, but removal did not complete: "+err.Error())
			}
		}
		return freed
	}
	for _, r := range resp2.Results {
		if c := byID[r.ID]; c != nil {
			r.SnapshotRefs = union(pending[r.ID], r.SnapshotRefs)
			finish(c, r)
		}
	}
	return freed
}

func inSecond(second []worktreereclaim.Worktree, id string) bool {
	for _, w := range second {
		if w.ID == id {
			return true
		}
	}
	return false
}

func withRefs(m *core.CleanupMetadata, refs []string) *core.CleanupMetadata {
	n := core.CleanupMetadata{}
	if m != nil {
		n = *m
	}
	n.SnapshotRefs = union(n.SnapshotRefs, refs)
	return &n
}

func heldMessage(r worktreereclaim.Result) string {
	switch r.Reason {
	case worktreereclaim.ReasonInUse:
		return "left in place: a running process is using it"
	default:
		return "left in place: " + strings.TrimSpace(r.Detail)
	}
}

// notRemovableMessage says why Clean up will not touch a held worktree, in the
// words the machine gave: for a parked checkout that is the path it is parked at.
func notRemovableMessage(m *core.CleanupMetadata) string {
	if m == nil || m.HeldReason == "" {
		return "cannot be removed automatically: remove it manually"
	}
	msg := "held (" + m.HeldReason + ")"
	if m.HeldDetail != "" {
		msg += ": " + m.HeldDetail
	}
	return msg + "; Clean up will not remove it"
}
