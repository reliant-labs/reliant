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
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/diskwatch"
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

	// A pass that neither removed nor held a worktree (it was touched
	// recently, its claim went stale, the daemon timed out on it) is not asked
	// again for transientBackoffMin, doubling per repeat up to
	// transientBackoffMax. Without this the row stays "due" and is retried at
	// the front of every pass.
	transientBackoffMin = 15 * time.Minute
	transientBackoffMax = 6 * time.Hour
	// A worktree the daemon could not settle this many times in a row is held
	// as ReasonError, which the Inbox shows.
	transientToError = 3
	// maxFailedSends bounds the requests a group may fail (a batch and its
	// bisected halves) before the daemon is left alone until the next pass.
	maxFailedSends = 8

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
	AdoptWorktreeDaemon(ctx context.Context, id, daemonID string) (string, error)
	ListAttachedDaemonIDsForUser(ctx context.Context, userID string, staleThreshold time.Duration) ([]string, error)
	TryAdvisoryLock(ctx context.Context, key int64) (release func(), ok bool, err error)
	// ListDaemonStorageState is each of a user's daemons' stored disk
	// report (diskwatch), read for its disk-guard episode.
	ListDaemonStorageState(ctx context.Context, userID string) (map[string]string, error)
}

// Sender runs a command on ONE named daemon of a user.
type Sender interface {
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// AutoArchiveMode says what the sweep does with an active worktree its daemon
// reports finished (clean, merged or pushed, idle, not in use).
type AutoArchiveMode string

const (
	// AutoArchiveOff never asks the daemon and never archives.
	AutoArchiveOff AutoArchiveMode = "off"
	// AutoArchiveDryRun asks and logs the candidates, and archives nothing.
	AutoArchiveDryRun AutoArchiveMode = "dry-run"
	// AutoArchiveOn archives them; the ordinary two-phase removal follows.
	AutoArchiveOn AutoArchiveMode = "on"
)

// ParseAutoArchiveMode reads the config value; empty means the default,
// dry-run: the candidates are logged and nothing is archived until someone has
// read the count and set "on". An unrecognized value is off: a typo must not
// start archiving.
func ParseAutoArchiveMode(v string) AutoArchiveMode {
	switch AutoArchiveMode(strings.ToLower(strings.TrimSpace(v))) {
	case "", AutoArchiveDryRun, "dryrun", "dry_run":
		return AutoArchiveDryRun
	case AutoArchiveOn, "true", "1":
		return AutoArchiveOn
	}
	return AutoArchiveOff
}

// AutoArchiveStore is what archiving a finished worktree needs.
type AutoArchiveStore interface {
	GetLiveChatIDForWorktree(ctx context.Context, worktreeID string) (string, error)
	ArchiveWorktree(ctx context.Context, id string) error
}

type autoArchive struct {
	mode   AutoArchiveMode
	store  AutoArchiveStore
	notify func(ctx context.Context, userID, projectID, worktreeID string)
}

// Sweeper settles worktree directories through their daemons.
type Sweeper struct {
	store    Store
	sender   Sender
	keep     KeepFilesSetting
	auto     *autoArchive
	recovery *recoveryEngine
	// candidates and archived count this pass's finished active worktrees.
	idleDone      atomic.Int64
	blockedByChat atomic.Int64
	archiveErrors atomic.Int64
	candidates    atomic.Int64
	archived      atomic.Int64
	now           func() time.Time
	slots         chan struct{}
	// inflight is the rows with a request out to a daemon.
	inflight leases
	// owner names this process in a clean-up's metadata, so a flag left by a
	// process that died is recognizable as not ours and expires on its own.
	owner string
	// cleanups counts the clean-ups StartCleanup runs detached (WaitCleanups).
	cleanups sync.WaitGroup
}

// New returns a Sweeper.
func New(store Store, sender Sender) *Sweeper {
	return &Sweeper{store: store, sender: sender, now: time.Now, slots: make(chan struct{}, settleSlots), owner: uuid.NewString()}
}

// WaitCleanups returns once every clean-up StartCleanup has started so far
// has finished, its result recorded: for a process draining before exit, and
// for a test that asserts on the outcome instead of guessing how long a real
// git snapshot takes on a loaded machine.
func (s *Sweeper) WaitCleanups() {
	s.cleanups.Wait()
}

// WithKeepFiles makes the sweep honor the user's "Keep everything" setting.
func (s *Sweeper) WithKeepFiles(k KeepFilesSetting) *Sweeper {
	s.keep = k
	return s
}

// WithAutoArchive makes the sweep archive finished active worktrees, so the
// removal path (which only ever handled archived rows) reaches them. notify
// tells the user's clients the list changed; it may be nil.
func (s *Sweeper) WithAutoArchive(mode AutoArchiveMode, store AutoArchiveStore, notify func(ctx context.Context, userID, projectID, worktreeID string)) *Sweeper {
	if mode == AutoArchiveOff || store == nil {
		s.auto = nil
		return s
	}
	s.auto = &autoArchive{mode: mode, store: store, notify: notify}
	return s
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
				logging.WarnContext(ctx, "[WorktreeSweep] pass failed", "error", err)
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
	s.idleDone.Store(0)
	s.blockedByChat.Store(0)
	s.archiveErrors.Store(0)
	s.candidates.Store(0)
	s.archived.Store(0)
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
	guardSince := map[string]time.Time{}
	for userID, userCands := range byUser {
		s.reconcileRecoveryJournals(ctx, userID)
		live, err := s.store.ListLiveWorktreePathsForUser(ctx, userID)
		if err != nil {
			logging.WarnContext(ctx, "[WorktreeSweep] could not list live worktree paths", "userID", userID, "error", err)
			continue
		}
		liveByUser[userID] = live
		connected, err := s.store.ListAttachedDaemonIDsForUser(ctx, userID, connectedLease)
		if err != nil {
			logging.WarnContext(ctx, "[WorktreeSweep] could not list connected daemons", "userID", userID, "error", err)
			continue
		}
		isUp := map[string]bool{}
		for _, id := range connected {
			isUp[id] = true
		}
		// A machine whose disk guard is acting (its volume past the reclaim
		// threshold) has its kept workspaces re-checked now, once per
		// episode, instead of after their back-off: space is needed, and a
		// workspace held as in use or recently active may be free by now.
		if states, err := s.store.ListDaemonStorageState(ctx, userID); err != nil {
			logging.WarnContext(ctx, "[WorktreeSweep] could not read disk reports", "userID", userID, "error", err)
		} else {
			for id, raw := range states {
				if ms := diskwatch.Decode(raw).GetGuardSinceMs(); ms > 0 {
					guardSince[id] = time.UnixMilli(ms)
				}
			}
		}
		for _, c := range userCands {
			var since time.Time
			if c.Worktree.DaemonID != nil {
				since = guardSince[*c.Worktree.DaemonID]
			}
			if !s.due(c.Worktree, since) {
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
		sortForFairness(group)
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			s.reconcileGroup(ctx, t, group, liveByUser[t.userID])
		}()
	}
	wg.Wait()
	if s.auto != nil {
		logging.InfoContext(ctx, "[WorktreeSweep] auto-archive", "mode", string(s.auto.mode), "idle_done", s.idleDone.Load(), "blocked_by_chat", s.blockedByChat.Load(), "errors", s.archiveErrors.Load(), "finished", s.candidates.Load(), "archived", s.archived.Load())
	}
	return nil
}

// sortForFairness orders one daemon's queue: archived rows (the ones holding
// disk that is owed back) before live ones, then the row asked about longest
// ago first. The store lists by last_active, so without this a few rows at the
// front were asked about first every pass and the rest were never reached.
func sortForFairness(group []*core.ReclaimCandidate) {
	checked := func(c *core.ReclaimCandidate) time.Time {
		if m := c.Worktree.CleanupMetadata; m != nil && m.CheckedAt != nil {
			return *m.CheckedAt
		}
		return time.Time{}
	}
	sort.SliceStable(group, func(i, j int) bool {
		ai, aj := group[i].Worktree.DeletedAt != nil, group[j].Worktree.DeletedAt != nil
		if ai != aj {
			return ai
		}
		return checked(group[i]).Before(checked(group[j]))
	})
}

// due says whether a row is worth asking its daemon about now. A row that was
// locked recently, or held and re-checked recently, is skipped: its state has
// not changed, and every check costs the daemon a git status and a tree walk.
// guardSince, when set, is when its machine's disk-guard episode began: a held
// row last checked before it is due at once, whatever its back-off.
func (s *Sweeper) due(wt *core.Worktree, guardSince time.Time) bool {
	meta := wt.CleanupMetadata
	if meta == nil || meta.CheckedAt == nil || meta.RetireFence != "" {
		return true // a pending retirement is delivered at once
	}
	age := s.now().Sub(*meta.CheckedAt)
	if meta.Transient != "" && age < transientBackoff(meta) {
		return false
	}
	if wt.DeletedAt == nil {
		return meta.LockedAt == nil || s.now().Sub(*meta.LockedAt) >= lockRecheck
	}
	if meta.HeldReason == "" {
		return true
	}
	if !guardSince.IsZero() && meta.CheckedAt.Before(guardSince) {
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

// transientBackoff grows with how many times in a row the last non-answer
// repeated.
func transientBackoff(meta *core.CleanupMetadata) time.Duration {
	d := transientBackoffMin
	for i := 0; i < meta.Rechecks && d < transientBackoffMax; i++ {
		d *= 2
	}
	return min(d, transientBackoffMax)
}

func (s *Sweeper) reconcileGroup(ctx context.Context, t target, group []*core.ReclaimCandidate, live []core.WorktreePath) {
	keep := s.keep != nil && s.keep.KeepFiles(ctx, t.userID)
	failedSends := 0
	// run settles a batch. When the request fails (the daemon's command
	// timeout, a dropped connection) it is split and the halves retried, so one
	// slow worktree costs its own row and not the nine behind it; a single
	// worktree that still fails is recorded so it backs off. false means stop
	// asking this daemon this pass.
	var run func(batch []*core.ReclaimCandidate) bool
	run = func(batch []*core.ReclaimCandidate) bool {
		switch s.settleBatch(ctx, t, batch, live, keep) {
		case batchDone:
			return true
		case batchStalled:
			// The daemon did not answer in time and may still be working on
			// these rows. They are leased and recorded as failed; sending
			// them again, whole or in halves, would only stack more work.
			return false
		}
		failedSends++
		if ctx.Err() != nil || failedSends >= maxFailedSends {
			return false
		}
		if len(batch) == 1 {
			s.recordFailure(ctx, batch[0].Worktree, "the machine did not answer in time")
			return true
		}
		mid := len(batch) / 2
		return run(batch[:mid]) && run(batch[mid:])
	}
	for start := 0; start < len(group); start += batchSize {
		end := min(start+batchSize, len(group))
		if !run(group[start:end]) {
			return
		}
	}
}

// recordFailure notes that the daemon could not settle a worktree this pass, so
// the row waits instead of being retried at the front of the next one. The
// third failure in a row becomes a visible hold.
func (s *Sweeper) recordFailure(ctx context.Context, seen *core.Worktree, detail string) {
	s.recordTransient(ctx, seen, "error", func(m *core.CleanupMetadata, archived bool) {
		if archived && m.Rechecks >= transientToError {
			m.HeldReason, m.HeldDetail = string(worktreereclaim.ReasonError), detail+" ("+strconv.Itoa(m.Rechecks+1)+" attempts)"
		}
	})
}

// recordTransient stores an outcome that is neither a removal nor a hold. It
// sets CheckedAt, which orders the queue, and Transient, which makes due() wait.
func (s *Sweeper) recordTransient(ctx context.Context, seen *core.Worktree, kind string, extra func(m *core.CleanupMetadata, archived bool)) {
	s.mergeMetaIf(ctx, seen, func(m *core.CleanupMetadata, archived bool) bool {
		if m.Transient == kind {
			m.Rechecks++
		} else {
			m.Rechecks = 0
		}
		m.Transient = kind
		now := s.now().UTC()
		m.CheckedAt = &now
		if extra != nil {
			extra(m, archived)
		}
		return true
	})
}

// batchResult is how asking the daemon about one batch ended.
type batchResult int

const (
	// batchDone: every row that could be asked was asked and answered.
	batchDone batchResult = iota
	// batchFailed: the request failed outright (the connection dropped, the
	// daemon refused it). Splitting the batch and asking again is safe.
	batchFailed
	// batchStalled: the request ran out of time. The daemon may still be
	// working on its rows, so they are neither split nor sent again.
	batchStalled
)

// stalled reports whether err is a deadline: the daemon was reached and did not
// answer, as opposed to an error that says it was not working on the request.
func stalled(err error) bool {
	return svcerr.IsDeadlineExceeded(err) || errors.Is(err, context.DeadlineExceeded)
}

// settleBatch runs the two phases for one batch. A row already out to this
// daemon (a request that timed out and may still be running, a settle in
// progress) is skipped: the next pass asks about it once the lease is free.
func (s *Sweeper) settleBatch(ctx context.Context, t target, batch []*core.ReclaimCandidate, live []core.WorktreePath, keep bool) (result batchResult) {
	keys := make([]leaseKey, len(batch))
	for i, c := range batch {
		keys[i] = leaseKey{t.daemonID, c.Worktree.ID}
	}
	got := s.inflight.acquire(s.now(), keys)
	if len(got) == 0 {
		return batchDone
	}
	if len(got) < len(batch) {
		free := map[string]bool{}
		for _, k := range got {
			free[k.worktreeID] = true
		}
		kept := batch[:0:0]
		for _, c := range batch {
			if free[c.Worktree.ID] {
				kept = append(kept, c)
			}
		}
		batch = kept
	}
	defer func() {
		if result == batchStalled {
			s.inflight.holdUntil(got, s.now().Add(inFlightHold))
			return
		}
		s.inflight.release(got)
	}()

	byID := map[string]*core.ReclaimCandidate{}
	req, shared := s.describe(ctx, batch, live, keep)
	for _, h := range shared {
		s.recordHold(ctx, h.cand.Worktree, worktreereclaim.ReasonSharedWithLive, h.detail, 0, nil)
	}
	if len(req.Worktrees) == 0 {
		return batchDone
	}
	for _, c := range batch {
		byID[c.Worktree.ID] = c
	}
	resp, err := s.send(ctx, t, cmdReconcile, reconcileTimeoutMs, req)
	if err != nil {
		logging.WarnContext(ctx, "[WorktreeSweep] reconcile failed", "daemonID", t.daemonID, "error", err)
		if stalled(err) {
			for _, w := range req.Worktrees {
				if c := byID[w.ID]; c != nil {
					s.recordFailure(ctx, c.Worktree, "the machine did not answer in time")
				}
			}
			return batchStalled
		}
		return batchFailed
	}

	var second []worktreereclaim.Worktree
	for _, r := range resp.Results {
		c := byID[r.ID]
		if c == nil {
			continue
		}
		if c.Worktree.DeletedAt == nil && s.recovery != nil && s.recovery.enabled && s.auto != nil && s.auto.mode == AutoArchiveOn {
			for _, w := range req.Worktrees {
				if w.ID == r.ID {
					s.recoverHeld(ctx, t, c, w)
					break
				}
			}
			// Apply the lock/probe reply normally; metadata merge checks current
			// archive state so an enrollment cannot resurrect the source row.
		}
		if r.Outcome == worktreereclaim.OutcomeClaimed {
			// Commits saved before the claim are recorded whatever happens
			// next: the refs exist either way.
			if len(r.SnapshotRefs) > 0 {
				s.mergeMeta(ctx, c.Worktree, func(m *core.CleanupMetadata) { m.SnapshotRefs = union(m.SnapshotRefs, r.SnapshotRefs) })
			}
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
		handledRecovery := false
		if r.Outcome == worktreereclaim.OutcomeHeld && (r.Reason == worktreereclaim.ReasonDirty || r.Reason == worktreereclaim.ReasonUnpushed || r.Reason == worktreereclaim.ReasonUnreachable) {
			for _, w := range req.Worktrees {
				if w.ID == r.ID {
					handledRecovery = s.recoverHeld(ctx, t, c, w)
					break
				}
			}
		}
		if handledRecovery {
			continue
		}
		s.apply(ctx, t.userID, t.daemonID, c.Worktree, c.ProjectPath, r, false)
	}
	if len(second) == 0 {
		return batchDone
	}
	resp2, err := s.send(ctx, t, cmdReconcile, reconcileTimeoutMs, worktreereclaim.Request{Worktrees: second})
	if err != nil {
		logging.WarnContext(ctx, "[WorktreeSweep] removal request failed", "daemonID", t.daemonID, "error", err)
		if stalled(err) {
			return batchStalled
		}
		return batchFailed
	}
	for _, r := range resp2.Results {
		if c := byID[r.ID]; c != nil {
			s.apply(ctx, t.userID, t.daemonID, c.Worktree, c.ProjectPath, r, false)
		}
	}
	return batchDone
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

// sharedHold is an archived row kept without asking its daemon, because
// removing its directory would remove a live row's directory or checkout.
type sharedHold struct {
	cand   *core.ReclaimCandidate
	detail string
}

// describe turns rows into the daemon's description of each workspace. An
// archived row is left out, and returned as a hold, when a live row's
// directory or checkout is at or under its own (see sharedWithLive); every
// other archived row is described as archived.
func (s *Sweeper) describe(ctx context.Context, cands []*core.ReclaimCandidate, live []core.WorktreePath, keep bool) (worktreereclaim.Request, []sharedHold) {
	req := worktreereclaim.Request{}
	var holds []sharedHold
	repoCache := map[string][]*core.Repo{}
	repoFailed := map[string]bool{}
	reposOf := func(projectID string) ([]*core.Repo, bool) {
		if repos, ok := repoCache[projectID]; ok {
			return repos, !repoFailed[projectID]
		}
		repos, err := s.store.ListReposByProject(ctx, projectID)
		if err != nil {
			logging.WarnContext(ctx, "[WorktreeSweep] could not list repos", "projectID", projectID, "error", err)
			repoFailed[projectID] = true
		}
		repoCache[projectID] = repos
		return repos, err == nil
	}
	var liveDirs []occupied // built on the first archived row
	for _, c := range cands {
		wt := c.Worktree
		state := worktreereclaim.StateActive
		if wt.DeletedAt != nil {
			if liveDirs == nil {
				liveDirs = occupiedBy(live, reposOf)
			}
			if detail, shared := sharedWithLive(wt, liveDirs); shared {
				holds = append(holds, sharedHold{cand: c, detail: detail})
				continue
			}
			state = worktreereclaim.StateArchived
		}
		w := worktreereclaim.Worktree{ID: wt.ID, Path: wt.Path, State: state, BaseBranch: wt.BaseBranch, Fence: Fence(wt), KeepFiles: keep}
		if state == worktreereclaim.StateActive && wt.CleanupMetadata != nil {
			// An unarchive that could not reach the machine left a fence to retire.
			w.Retire = wt.CleanupMetadata.RetireFence
		}
		// A user who chose "Keep everything" keeps their workspaces visible too.
		w.ProbeIdle = state == worktreereclaim.StateActive && s.auto != nil && w.Retire == "" && !keep && wt.DeletedAt == nil
		repos, _ := reposOf(wt.ProjectID)
		for _, repo := range repos {
			w.Checkouts = append(w.Checkouts, worktreereclaim.Checkout{
				RepoPath:   filepath.Join(c.ProjectPath, repo.RelativePath),
				Rel:        filepath.ToSlash(repo.RelativePath),
				BaseBranch: wt.BaseBranches[repo.ID],
			})
		}
		req.Worktrees = append(req.Worktrees, w)
	}
	return req, holds
}

// occupied is what one unarchived row has on disk: its directory and the
// checkout its project puts under it for each repo. complete is false when the
// repos could not be listed, so the checkouts are unknown.
type occupied struct {
	id, path string
	dirs     []string
	complete bool
}

func occupiedBy(live []core.WorktreePath, reposOf func(projectID string) ([]*core.Repo, bool)) []occupied {
	out := make([]occupied, 0, len(live))
	for _, l := range live {
		o := occupied{id: l.ID, path: filepath.Clean(l.Path)}
		o.dirs = append(o.dirs, o.path)
		repos, ok := reposOf(l.ProjectID)
		o.complete = ok
		for _, repo := range repos {
			if d := filepath.Join(o.path, repo.RelativePath); d != o.path {
				o.dirs = append(o.dirs, d)
			}
		}
		out = append(out, o)
	}
	return out
}

// sharedWithLive reports whether removing this archived row's directory would
// remove some OTHER unarchived row's directory or checkout: one of them is at
// or under it. It says why, in words for the Inbox.
//
// A live row whose directory merely CONTAINS this one does not keep it: the
// archived worktree is its own checkout, and removing it leaves the live row's
// checkouts alone. (A project rooted at a home directory contains every
// worktree on the machine; letting it shield them kept every archived
// directory forever.) Only when a live row's repos could not be listed, so its
// checkouts are unknown, does containment count, failing closed.
func sharedWithLive(wt *core.Worktree, live []occupied) (string, bool) {
	mine := filepath.Clean(wt.Path)
	for _, l := range live {
		if l.id == wt.ID {
			continue
		}
		for _, d := range l.dirs {
			switch {
			case d == mine && d == l.path:
				return "an open workspace uses the same directory: " + l.path, true
			case d == mine:
				return "it is a checkout of the open workspace at " + l.path, true
			case pathUnder(d, mine):
				return "the open workspace at " + l.path + " has a checkout inside it: " + d, true
			}
		}
		if !l.complete && pathUnder(mine, l.path) {
			return "it is inside the open workspace at " + l.path + ", whose checkouts could not be listed", true
		}
	}
	return "", false
}

// pathUnder reports whether p lies strictly beneath dir.
func pathUnder(p, dir string) bool {
	return strings.HasPrefix(p, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
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

// apply records one worktree's outcome. It re-reads the row and merges into
// whatever metadata is stored NOW, so it never overwrites a concurrent update
// and never records an outcome against a row that is no longer archived.
func (s *Sweeper) apply(ctx context.Context, userID, daemonID string, seen *core.Worktree, projectPath string, r worktreereclaim.Result, forced bool) {
	switch r.Outcome {
	case worktreereclaim.OutcomeForeign:
		return
	case worktreereclaim.OutcomeDeferred:
		// Never reached: ask again next pass. Reached and stopped by the
		// daemon's deadline: back off like any other non-answer, so one tree
		// that is too slow to check does not take the front of every pass.
		if r.Detail == worktreereclaim.DetailTimedOut {
			s.recordTransient(ctx, seen, "timeout", nil)
		}
		return
	case worktreereclaim.OutcomeError:
		if r.Error != "" {
			logging.WarnContext(ctx, "[WorktreeSweep] daemon could not settle worktree", "worktreeID", seen.ID, "error", r.Error)
		}
		// Recorded as a hold, so the Inbox shows it and it backs off; it used to
		// be dropped, which left the row due at the front of every pass.
		r = worktreereclaim.Result{ID: r.ID, Outcome: worktreereclaim.OutcomeHeld, Reason: worktreereclaim.ReasonError, Detail: r.Error, SnapshotRefs: r.SnapshotRefs}
		if seen.DeletedAt == nil {
			s.recordTransient(ctx, seen, "error", nil)
			return
		}
	}
	wt, err := s.store.GetWorktree(ctx, seen.ID)
	if err != nil || wt == nil {
		return
	}
	// The daemon found this checkout on its own disk, which is the proof of
	// ownership an unattributed row never had. Recording it also moves the
	// workspace's chats pinned elsewhere onto it (store.AdoptWorktreeDaemon),
	// so only an outcome that found the checkout counts (foundCheckout).
	if wt.DaemonID == nil && foundCheckout(r) {
		if recorded, err := s.store.AdoptWorktreeDaemon(ctx, wt.ID, daemonID); err != nil {
			logging.WarnContext(ctx, "[WorktreeSweep] could not record worktree owner", "worktreeID", wt.ID, "error", err)
		} else if recorded != "" {
			// The persisted owner, which another resolver may have set first.
			wt.DaemonID = &recorded
		}
	}
	if wt.DeletedAt == nil {
		if r.Outcome == worktreereclaim.OutcomeProbeInconclusive {
			// Locked, but whether it is finished is unknown. LockedAt is not
			// stamped (that is a 24h silence) and the row is not marked
			// checked-and-found-busy: it waits the short transient back-off.
			s.recordTransient(ctx, wt, "probe_inconclusive", nil)
			return
		}
		if r.Outcome == worktreereclaim.OutcomeLocked || r.Outcome == worktreereclaim.OutcomeIdleDone {
			s.mergeMeta(ctx, wt, func(m *core.CleanupMetadata) {
				now := s.now().UTC()
				m.LockedAt, m.CheckedAt, m.RetireFence = &now, &now, ""
				m.Transient, m.Rechecks = "", 0
			})
		}
		if r.Outcome == worktreereclaim.OutcomeIdleDone {
			s.archiveFinished(ctx, userID, wt)
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
			m.HeldReason, m.HeldDetail, m.SizeBytes, m.Rechecks, m.Transient = "", "", 0, 0, ""
			m.SnapshotRefs = union(m.SnapshotRefs, r.SnapshotRefs)
			return true
		})
		s.deleteBranchIfAsked(ctx, userID, daemonID, wt, projectPath)
	case worktreereclaim.OutcomeHeld:
		if r.Reason == worktreereclaim.ReasonRecentlyActive || r.Reason == worktreereclaim.ReasonStale {
			// Not a held worktree, so nothing for the Inbox, but the row waits
			// before it is asked again rather than staying due.
			s.recordTransient(ctx, wt, string(r.Reason), nil)
			return
		}
		s.recordHold(ctx, wt, r.Reason, r.Detail, r.SizeBytes, r.SnapshotRefs)
		s.recordStripped(ctx, wt, r)
	}
}

// recordHold records why an archived row's directory was kept, and backs off
// re-checking it while the reason stays the same.
func (s *Sweeper) recordHold(ctx context.Context, wt *core.Worktree, reason worktreereclaim.HeldReason, detail string, size int64, refs []string) {
	s.mergeMetaIf(ctx, wt, func(m *core.CleanupMetadata, archived bool) bool {
		if !archived {
			return false
		}
		if m.HeldReason == string(reason) && m.HeldDetail == detail {
			m.Rechecks++
		} else {
			m.Rechecks = 0
		}
		m.HeldReason, m.HeldDetail, m.Transient = string(reason), detail, ""
		m.Cleaning, m.CleaningBy, m.CleaningUntil = false, "", nil
		if size > 0 {
			m.SizeBytes = size
		}
		m.SnapshotRefs = union(m.SnapshotRefs, refs)
		now := s.now().UTC()
		m.CheckedAt = &now
		return true
	})
}

// archiveFinished archives an active worktree its daemon reports finished, so
// the same fenced two-phase removal that handles every archived row reaches
// it. The daemon has already checked the files; the server checks that no
// chat still uses it. In dry-run mode it only counts and logs.
func (s *Sweeper) archiveFinished(ctx context.Context, userID string, wt *core.Worktree) {
	s.idleDone.Add(1)
	a := s.auto
	reason := ""
	switch {
	case a == nil:
		reason = "auto-archive disabled"
	case wt.IsMain:
		reason = "main checkout"
	case wt.DeletedAt != nil:
		reason = "already archived"
	case s.keep != nil && s.keep.KeepFiles(ctx, userID):
		reason = "keep files enabled"
	}
	if reason != "" {
		logging.DebugContext(ctx, "[WorktreeSweep] skipped auto-archive", "worktreeID", wt.ID, "reason", reason)
		return
	}
	chatID, err := a.store.GetLiveChatIDForWorktree(ctx, wt.ID)
	if err != nil {
		s.archiveErrors.Add(1)
		logging.WarnContext(ctx, "[WorktreeSweep] could not check bound chat", "worktreeID", wt.ID, "error", err)
		return
	}
	if chatID != "" {
		s.blockedByChat.Add(1)
		logging.DebugContext(ctx, "[WorktreeSweep] skipped auto-archive", "worktreeID", wt.ID, "reason", "bound to live chat", "chatID", chatID)
		return
	}
	s.candidates.Add(1)
	if a.mode != AutoArchiveOn {
		logging.InfoContext(ctx, "[WorktreeSweep] would auto-archive finished worktree", "worktreeID", wt.ID, "name", wt.Name, "path", wt.Path)
		return
	}
	if err := a.store.ArchiveWorktree(ctx, wt.ID); err != nil {
		s.archiveErrors.Add(1)
		logging.WarnContext(ctx, "[WorktreeSweep] could not auto-archive worktree", "worktreeID", wt.ID, "error", err)
		return
	}
	s.archived.Add(1)
	logging.InfoContext(ctx, "[WorktreeSweep] auto-archived finished worktree", "worktreeID", wt.ID, "name", wt.Name)
	if a.notify != nil {
		a.notify(ctx, userID, wt.ProjectID, wt.ID)
	}
}

// maxStrippedPaths bounds the stripped paths a row remembers.
const maxStrippedPaths = 20

// recordStripped adds what the daemon shed from a held directory to the row.
func (s *Sweeper) recordStripped(ctx context.Context, wt *core.Worktree, r worktreereclaim.Result) {
	if r.StrippedBytes == 0 && len(r.Stripped) == 0 {
		return
	}
	s.mergeMetaIf(ctx, wt, func(m *core.CleanupMetadata, archived bool) bool {
		if !archived {
			return false
		}
		m.StrippedBytes += r.StrippedBytes
		m.StrippedPaths = union(m.StrippedPaths, r.Stripped)
		if len(m.StrippedPaths) > maxStrippedPaths {
			m.StrippedPaths = m.StrippedPaths[:maxStrippedPaths]
		}
		return true
	})
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
		logging.WarnContext(ctx, "[WorktreeSweep] could not delete branch", "worktreeID", wt.ID, "error", err)
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
		logging.WarnContext(ctx, "[WorktreeSweep] could not record cleanup state", "worktreeID", wt.ID, "error", err)
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

// foundCheckout reports whether a reclaim result proves the daemon found the
// worktree's checkout on its disk. "gone" (no directory), "foreign" (not under
// this machine's worktree root; the server must conclude nothing), "deferred"
// (never reached) and a failed pass prove nothing.
func foundCheckout(r worktreereclaim.Result) bool {
	switch r.Outcome {
	case worktreereclaim.OutcomeGone, worktreereclaim.OutcomeForeign, worktreereclaim.OutcomeDeferred, worktreereclaim.OutcomeError:
		return false
	case worktreereclaim.OutcomeHeld:
		return r.Reason != worktreereclaim.ReasonError
	}
	return true
}
