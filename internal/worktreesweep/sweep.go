// Copyright (c) 2025 Reliant Labs

// Package worktreesweep is the server half of worktree disk hygiene. It never
// touches a path: it describes the worktree rows a machine owns, asks that
// machine's daemon (the only thing that can see the disk) to settle them, and
// records what came back.
//
// Removal is two requests. The first asks the daemon to decide and, when the
// worktree is safe, to claim it by writing the archive's fence into each
// checkout's lock. The server then re-reads the row; only if it is still
// archived with the same fence does it send the second request, which the
// daemon honors only while its lock still carries that fence. Unarchiving
// re-locks the checkouts as active, so no earlier request can remove them.
//
//forge:exclude-contract: orchestration over a narrow store and a daemon command sender, both declared here; the daemon-side behavior is in worktreereclaim
package worktreesweep

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

const (
	cmdReconcile      = "worktree.reconcile"
	cmdSnapshotRemove = "worktree.snapshot_remove"

	// batchSize bounds worktrees per daemon command, so one slow worktree
	// cannot hold the rest behind it past the command timeout.
	batchSize = 10

	reconcileTimeoutMs      int32 = 120_000
	snapshotRemoveTimeoutMs int32 = 300_000
	connectedLease                = 90 * time.Second

	// sweepWorkers bounds how many daemons one pass talks to at once.
	sweepWorkers = 8
	// settleSlots bounds the goroutines archiving may start at once.
	settleSlots = 8

	// A held worktree is asked about again after holdBackoffMin, doubling up to
	// holdBackoffMax, unless it changes state first.
	holdBackoffMin = time.Hour
	holdBackoffMax = 24 * time.Hour
	// A locked live worktree is re-checked once a day.
	lockRecheck = 24 * time.Hour

	sweepLockKey int64 = 0x7265_6c69_616e_7401 // "reliant\x01"
)

// KeepFilesSetting says whether the user chose "Keep everything" on archive.
// Nothing is removed automatically for a user who did.
type KeepFilesSetting interface {
	KeepFiles(ctx context.Context, userID string) bool
}

// Store is what the sweep needs from the database.
type Store interface {
	ListWorktreesForReclaim(ctx context.Context) ([]*core.ReclaimCandidate, error)
	ListLiveWorktreePathsForUser(ctx context.Context, userID string) ([]core.WorktreePath, error)
	MergeWorktreeCleanupMetadata(ctx context.Context, id string, fn func(m *core.CleanupMetadata, archived bool) bool) error
	GetWorktree(ctx context.Context, id string) (*core.Worktree, error)
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)
	UpdateWorktreeCleanupMetadata(ctx context.Context, id string, metadata *core.CleanupMetadata) error
	AdoptWorktreeDaemon(ctx context.Context, id, daemonID string) error
	SetDaemonStorageState(ctx context.Context, daemonID string, stateJSON string) error
	ListAttachedDaemonIDsForUser(ctx context.Context, userID string, staleThreshold time.Duration) ([]string, error)
	TryAdvisoryLock(ctx context.Context, key int64) (release func(), ok bool, err error)
}

// Sender runs a command on ONE named daemon of a user.
type Sender interface {
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// Sweeper settles worktree directories through their daemons.
type Sweeper struct {
	store  Store
	sender Sender
	keep   KeepFilesSetting
	now    func() time.Time
	slots  chan struct{}
	// owner names this process in a clean-up's metadata, so a flag left by a
	// process that died is recognizable as not ours and expires on its own.
	owner string
}

// New returns a Sweeper.
func New(store Store, sender Sender) *Sweeper {
	return &Sweeper{store: store, sender: sender, now: time.Now, slots: make(chan struct{}, settleSlots), owner: uuid.NewString()}
}

// WithKeepFiles makes the sweep honor the user's "Keep everything" setting.
func (s *Sweeper) WithKeepFiles(k KeepFilesSetting) *Sweeper {
	s.keep = k
	return s
}

// StorageState is what a daemon last reported about its disk, as stored in
// daemons.storage_state.
type StorageState struct {
	Path       string    `json:"path"`
	FreeBytes  int64     `json:"free_bytes"`
	TotalBytes int64     `json:"total_bytes"`
	ReportedAt time.Time `json:"reported_at"`
}

// Fence is the archive token for a row: its archive time in nanoseconds.
// It is read from the database clock, so a clock that steps backwards makes
// newer claims compare as older and be refused as stale. That fails safe
// (nothing is removed), and is accepted rather than guarded against.
func Fence(wt *core.Worktree) string {
	if wt.DeletedAt == nil {
		return ""
	}
	return strconv.FormatInt(wt.DeletedAt.UnixNano(), 10)
}

// Run sweeps every interval until ctx ends. Only the replica holding the
// Postgres advisory lock sweeps; the others skip the pass. The first pass waits
// a minute so a restarting server is not hammering daemons still reconnecting.
func (s *Sweeper) Run(ctx context.Context, interval time.Duration) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := s.SweepAsLeader(ctx); err != nil {
				logging.Warn("[WorktreeSweep] pass failed", "error", err)
			}
			timer.Reset(interval)
		}
	}
}

// SweepAsLeader runs one pass if this replica wins the advisory lock.
func (s *Sweeper) SweepAsLeader(ctx context.Context) error {
	release, ok, err := s.store.TryAdvisoryLock(ctx, sweepLockKey)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	defer release()
	return s.Sweep(ctx)
}

type target struct{ userID, daemonID string }

// Sweep reconciles every reclaim candidate against the daemon that owns it,
// with at most sweepWorkers daemons in flight.
func (s *Sweeper) Sweep(ctx context.Context) error {
	cands, err := s.store.ListWorktreesForReclaim(ctx)
	if err != nil {
		return err
	}
	byUser := map[string][]*core.ReclaimCandidate{}
	for _, c := range cands {
		byUser[c.OwnerUserID] = append(byUser[c.OwnerUserID], c)
	}
	groups := map[target][]*core.ReclaimCandidate{}
	liveByUser := map[string][]core.WorktreePath{}
	for userID, userCands := range byUser {
		live, err := s.store.ListLiveWorktreePathsForUser(ctx, userID)
		if err != nil {
			logging.Warn("[WorktreeSweep] could not list live worktree paths", "userID", userID, "error", err)
			continue
		}
		liveByUser[userID] = live
		connected, err := s.store.ListAttachedDaemonIDsForUser(ctx, userID, connectedLease)
		if err != nil {
			logging.Warn("[WorktreeSweep] could not list connected daemons", "userID", userID, "error", err)
			continue
		}
		isUp := map[string]bool{}
		for _, id := range connected {
			isUp[id] = true
		}
		for _, c := range userCands {
			if !s.due(c.Worktree) {
				continue
			}
			if c.Worktree.DaemonID != nil && *c.Worktree.DaemonID != "" {
				if isUp[*c.Worktree.DaemonID] {
					t := target{userID, *c.Worktree.DaemonID}
					groups[t] = append(groups[t], c)
				}
				continue
			}
			// No recorded owner: ask every connected daemon. Only a daemon that
			// finds the directory on its own disk claims the row.
			for _, id := range connected {
				t := target{userID, id}
				groups[t] = append(groups[t], c)
			}
		}
	}
	sem := make(chan struct{}, sweepWorkers)
	var wg sync.WaitGroup
	for t, group := range groups {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			s.reconcileGroup(ctx, t, group, liveByUser[t.userID])
		}()
	}
	wg.Wait()
	return nil
}

// due says whether a row is worth asking its daemon about now. A row that was
// locked recently, or held and re-checked recently, is skipped: its state has
// not changed, and every check costs the daemon a git status and a tree walk.
func (s *Sweeper) due(wt *core.Worktree) bool {
	meta := wt.CleanupMetadata
	if meta == nil || meta.CheckedAt == nil || meta.RetireFence != "" {
		return true // a pending retirement is delivered at once
	}
	age := s.now().Sub(*meta.CheckedAt)
	if wt.DeletedAt == nil {
		return meta.LockedAt == nil || s.now().Sub(*meta.LockedAt) >= lockRecheck
	}
	if meta.HeldReason == "" {
		return true
	}
	return age >= holdBackoff(meta)
}

// holdBackoff grows with how long a worktree has stayed held: an hour at
// first, up to a day.
func holdBackoff(meta *core.CleanupMetadata) time.Duration {
	d := holdBackoffMin
	for i := 0; i < meta.Rechecks && d < holdBackoffMax; i++ {
		d *= 2
	}
	return min(d, holdBackoffMax)
}

func (s *Sweeper) reconcileGroup(ctx context.Context, t target, group []*core.ReclaimCandidate, live []core.WorktreePath) {
	keep := s.keep != nil && s.keep.KeepFiles(ctx, t.userID)
	for start := 0; start < len(group); start += batchSize {
		end := min(start+batchSize, len(group))
		batch := group[start:end]
		if !s.settleBatch(ctx, t, batch, live, keep) {
			return
		}
	}
}

// settleBatch runs the two phases for one batch. It reports false when the
// daemon could not be reached, so the caller stops asking it this pass.
func (s *Sweeper) settleBatch(ctx context.Context, t target, batch []*core.ReclaimCandidate, live []core.WorktreePath, keep bool) bool {
	byID := map[string]*core.ReclaimCandidate{}
	req := s.describe(ctx, batch, live, keep)
	for _, c := range batch {
		byID[c.Worktree.ID] = c
	}
	resp, err := s.send(ctx, t, cmdReconcile, reconcileTimeoutMs, req)
	if err != nil {
		logging.Warn("[WorktreeSweep] reconcile failed", "daemonID", t.daemonID, "error", err)
		return false
	}
	s.recordDisk(ctx, t.daemonID, resp.Disk)

	var second []worktreereclaim.Worktree
	for _, r := range resp.Results {
		c := byID[r.ID]
		if c == nil {
			continue
		}
		if r.Outcome == worktreereclaim.OutcomeClaimed {
			// Re-read the row NOW. Unarchived or re-archived since the first
			// request was built, and the claim is stale: do not remove.
			cur, err := s.store.GetWorktree(ctx, r.ID)
			if err != nil || cur == nil || cur.DeletedAt == nil || Fence(cur) != fenceOf(req, r.ID) {
				continue
			}
			for _, w := range req.Worktrees {
				if w.ID == r.ID {
					w.Remove = true
					second = append(second, w)
				}
			}
			continue
		}
		s.apply(ctx, t.userID, t.daemonID, c.Worktree, c.ProjectPath, r, false)
	}
	if len(second) == 0 {
		return true
	}
	resp2, err := s.send(ctx, t, cmdReconcile, reconcileTimeoutMs, worktreereclaim.Request{Worktrees: second})
	if err != nil {
		logging.Warn("[WorktreeSweep] removal request failed", "daemonID", t.daemonID, "error", err)
		return false
	}
	for _, r := range resp2.Results {
		if c := byID[r.ID]; c != nil {
			s.apply(ctx, t.userID, t.daemonID, c.Worktree, c.ProjectPath, r, false)
		}
	}
	return true
}

func fenceOf(req worktreereclaim.Request, id string) string {
	for _, w := range req.Worktrees {
		if w.ID == id {
			return w.Fence
		}
	}
	return ""
}

// Settle runs one pass for a worktree that was just archived. A daemon that is
// offline or slow is not an error: the periodic sweep picks it up later.
func (s *Sweeper) Settle(ctx context.Context, userID string, cand *core.ReclaimCandidate) {
	if cand.Worktree.DaemonID == nil || *cand.Worktree.DaemonID == "" || cand.Worktree.Path == "" {
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return // plenty already running; the sweep will get this one
	}
	s.settleNow(ctx, userID, cand)
}

// SettleBlocking settles a worktree and waits, for callers that must know the
// outcome (permanent delete). It returns the stored metadata afterwards.
func (s *Sweeper) SettleBlocking(ctx context.Context, userID string, cand *core.ReclaimCandidate) *core.CleanupMetadata {
	if cand.Worktree.DaemonID == nil || *cand.Worktree.DaemonID == "" || cand.Worktree.Path == "" {
		return cand.Worktree.CleanupMetadata
	}
	s.settleNow(ctx, userID, cand)
	if cur, err := s.store.GetWorktree(ctx, cand.Worktree.ID); err == nil && cur != nil {
		return cur.CleanupMetadata
	}
	return cand.Worktree.CleanupMetadata
}

func (s *Sweeper) settleNow(ctx context.Context, userID string, cand *core.ReclaimCandidate) {
	live, err := s.store.ListLiveWorktreePathsForUser(ctx, userID)
	if err != nil {
		return
	}
	keep := s.keep != nil && s.keep.KeepFiles(ctx, userID)
	s.settleBatch(ctx, target{userID, *cand.Worktree.DaemonID}, []*core.ReclaimCandidate{cand}, live, keep)
}

// describe turns rows into the daemon's description of each workspace. An
// archived row is described as archived only when no live row's path equals,
// contains or sits inside its own; otherwise the daemon is told it is active,
// which locks it and removes nothing.
func (s *Sweeper) describe(ctx context.Context, cands []*core.ReclaimCandidate, live []core.WorktreePath, keep bool) worktreereclaim.Request {
	req := worktreereclaim.Request{}
	repoCache := map[string][]*core.Repo{}
	for _, c := range cands {
		wt := c.Worktree
		state := worktreereclaim.StateActive
		if wt.DeletedAt != nil && !sharesPathWithLive(wt, live) {
			state = worktreereclaim.StateArchived
		}
		w := worktreereclaim.Worktree{ID: wt.ID, Path: wt.Path, State: state, BaseBranch: wt.BaseBranch, Fence: Fence(wt), KeepFiles: keep}
		if state == worktreereclaim.StateActive && wt.CleanupMetadata != nil {
			// An unarchive that could not reach the machine left a fence to retire.
			w.Retire = wt.CleanupMetadata.RetireFence
		}
		repos, ok := repoCache[wt.ProjectID]
		if !ok {
			var err error
			if repos, err = s.store.ListReposByProject(ctx, wt.ProjectID); err != nil {
				logging.Warn("[WorktreeSweep] could not list repos", "projectID", wt.ProjectID, "error", err)
			}
			repoCache[wt.ProjectID] = repos
		}
		for _, repo := range repos {
			w.Checkouts = append(w.Checkouts, worktreereclaim.Checkout{
				RepoPath:   filepath.Join(c.ProjectPath, repo.RelativePath),
				Rel:        filepath.ToSlash(repo.RelativePath),
				BaseBranch: wt.BaseBranches[repo.ID],
			})
		}
		req.Worktrees = append(req.Worktrees, w)
	}
	return req
}

// sharesPathWithLive reports whether any OTHER unarchived row's path equals,
// contains or is contained by this row's.
func sharesPathWithLive(wt *core.Worktree, live []core.WorktreePath) bool {
	mine := filepath.Clean(wt.Path)
	for _, l := range live {
		if l.ID == wt.ID {
			continue
		}
		other := filepath.Clean(l.Path)
		if other == mine || strings.HasPrefix(other, mine+string(filepath.Separator)) || strings.HasPrefix(mine, other+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (s *Sweeper) send(ctx context.Context, t target, cmd string, timeoutMs int32, req worktreereclaim.Request) (*worktreereclaim.Response, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	raw, err := s.sender.SendDaemonCommandToDaemon(ctx, t.userID, t.daemonID, cmd, payload, timeoutMs)
	if err != nil {
		return nil, err
	}
	var resp worktreereclaim.Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", cmd, err)
	}
	return &resp, nil
}

func (s *Sweeper) recordDisk(ctx context.Context, daemonID string, d *worktreereclaim.Disk) {
	if d == nil {
		return
	}
	state, _ := json.Marshal(StorageState{Path: d.Path, FreeBytes: d.FreeBytes, TotalBytes: d.TotalBytes, ReportedAt: s.now().UTC()})
	if err := s.store.SetDaemonStorageState(ctx, daemonID, string(state)); err != nil {
		logging.Warn("[WorktreeSweep] could not record disk state", "daemonID", daemonID, "error", err)
	}
}

// apply records one worktree's outcome. It re-reads the row and merges into
// whatever metadata is stored NOW, so it never overwrites a concurrent update
// and never records an outcome against a row that is no longer archived.
func (s *Sweeper) apply(ctx context.Context, userID, daemonID string, seen *core.Worktree, projectPath string, r worktreereclaim.Result, forced bool) {
	switch r.Outcome {
	case worktreereclaim.OutcomeForeign, worktreereclaim.OutcomeDeferred:
		return
	case worktreereclaim.OutcomeError:
		if r.Error != "" {
			logging.Warn("[WorktreeSweep] daemon could not settle worktree", "worktreeID", seen.ID, "error", r.Error)
		}
		return
	}
	wt, err := s.store.GetWorktree(ctx, seen.ID)
	if err != nil || wt == nil {
		return
	}
	// The daemon found this directory on its own disk, which is the proof of
	// ownership an unattributed row never had. "gone" proves nothing.
	if wt.DaemonID == nil && r.Outcome != worktreereclaim.OutcomeGone {
		if err := s.store.AdoptWorktreeDaemon(ctx, wt.ID, daemonID); err != nil {
			logging.Warn("[WorktreeSweep] could not record worktree owner", "worktreeID", wt.ID, "error", err)
		} else {
			wt.DaemonID = &daemonID
		}
	}
	if wt.DeletedAt == nil {
		if r.Outcome == worktreereclaim.OutcomeLocked {
			s.mergeMeta(ctx, wt, func(m *core.CleanupMetadata) {
				now := s.now().UTC()
				m.LockedAt, m.CheckedAt, m.RetireFence = &now, &now, ""
			})
		}
		return // an unarchived row never records a removal
	}
	switch r.Outcome {
	case worktreereclaim.OutcomeRemoved, worktreereclaim.OutcomeGone:
		if r.Outcome == worktreereclaim.OutcomeGone && wt.DaemonID == nil {
			return
		}
		s.mergeMetaIf(ctx, wt, func(m *core.CleanupMetadata, archived bool) bool {
			if !archived {
				return false // restored while the answer was in flight: never record a removal
			}
			m.DirectoryDeleted = true
			m.Cleaning, m.CleaningBy, m.CleaningUntil = false, "", nil
			m.HeldReason, m.HeldDetail, m.SizeBytes, m.Rechecks = "", "", 0, 0
			m.SnapshotRefs = union(m.SnapshotRefs, r.SnapshotRefs)
			return true
		})
		s.deleteBranchIfAsked(ctx, userID, daemonID, wt, projectPath)
	case worktreereclaim.OutcomeHeld:
		if r.Reason == worktreereclaim.ReasonRecentlyActive || r.Reason == worktreereclaim.ReasonStale {
			return // transient: not a held worktree, ask again next pass
		}
		s.mergeMetaIf(ctx, wt, func(m *core.CleanupMetadata, archived bool) bool {
			if !archived {
				return false
			}
			if m.HeldReason == string(r.Reason) && m.HeldDetail == r.Detail {
				m.Rechecks++
			} else {
				m.Rechecks = 0
			}
			m.HeldReason, m.HeldDetail = string(r.Reason), r.Detail
			m.Cleaning, m.CleaningBy, m.CleaningUntil = false, "", nil
			if r.SizeBytes > 0 {
				m.SizeBytes = r.SizeBytes
			}
			m.SnapshotRefs = union(m.SnapshotRefs, r.SnapshotRefs)
			now := s.now().UTC()
			m.CheckedAt = &now
			return true
		})
	}
}

// deleteBranchIfAsked deletes the worktree's branch once its directory is gone,
// when the user asked for that. It runs here, after removal, because git
// refuses to delete a branch that is still checked out.
func (s *Sweeper) deleteBranchIfAsked(ctx context.Context, userID, daemonID string, wt *core.Worktree, projectPath string) {
	cur, err := s.store.GetWorktree(ctx, wt.ID)
	if err != nil || cur == nil || cur.CleanupMetadata == nil || !cur.CleanupMetadata.DeleteBranch || cur.CleanupMetadata.BranchDeleted || cur.Branch == "" || projectPath == "" {
		return
	}
	payload, _ := json.Marshal(map[string]string{"project_path": projectPath, "branch": cur.Branch})
	raw, err := s.sender.SendDaemonCommandToDaemon(ctx, userID, daemonID, "worktree.delete_branch", payload, reconcileTimeoutMs)
	if err != nil {
		logging.Warn("[WorktreeSweep] could not delete branch", "worktreeID", wt.ID, "error", err)
		return
	}
	var resp struct {
		Deleted bool `json:"deleted"`
	}
	if json.Unmarshal(raw, &resp) == nil && resp.Deleted {
		s.mergeMeta(ctx, wt, func(m *core.CleanupMetadata) { m.BranchDeleted, m.DeleteBranch = true, false })
	}
}

// mergeMeta applies fn to the CURRENT stored metadata under a row lock, so
// settle, sweep and clean-up running on different replicas never lose each
// other's updates.
func (s *Sweeper) mergeMeta(ctx context.Context, wt *core.Worktree, fn func(*core.CleanupMetadata)) {
	s.mergeMetaIf(ctx, wt, func(m *core.CleanupMetadata, _ bool) bool { fn(m); return true })
}

func (s *Sweeper) mergeMetaIf(ctx context.Context, wt *core.Worktree, fn func(*core.CleanupMetadata, bool) bool) {
	err := s.store.MergeWorktreeCleanupMetadata(ctx, wt.ID, func(m *core.CleanupMetadata, archived bool) bool {
		if !fn(m, archived) {
			return false
		}
		if m.CheckedAt == nil {
			now := s.now().UTC()
			m.CheckedAt = &now
		}
		return true
	})
	if err != nil {
		logging.Warn("[WorktreeSweep] could not record cleanup state", "worktreeID", wt.ID, "error", err)
	}
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range append(append([]string(nil), a...), b...) {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
