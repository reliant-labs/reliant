// Copyright (c) 2025 Reliant Labs
package mergequeue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/pipeline/config"
	"github.com/reliant-labs/reliant/internal/pipeline/grants"
	"github.com/reliant-labs/reliant/internal/pipeline/prpolicy"
)

// Deps is what a Scheduler is built from.
type Deps struct {
	Store  Store
	Leader LeaderLock
	// Refs reads main's tip through the GitHub API.
	Refs Refs
	// Builder runs the code operations (candidate builds, changed paths)
	// on a gate pool daemon: the queue holds no repository bytes.
	Builder Builder
	Gate    Gate
	// Authorizer decides the land capability (internal/pipeline/grants):
	// in mode on an entry is admitted, and a passed batch handed to the
	// lander, only while the run that approved each head holds land.
	// grants.Service implements it.
	Authorizer grants.Authorizer
	// Configs is nil for StoredConfig.
	// forge:optional-dep
	Configs Configs
	// Lander is nil for NoLander: a queue that only shadows.
	// forge:optional-dep
	Lander Lander
	// Logger is nil for slog.Default.
	// forge:optional-dep
	Logger *slog.Logger
	// Now is nil for time.Now.
	// forge:optional-dep
	Now func() time.Time
}

// Scheduler drives the merge queue of each repository it leads.
type Scheduler struct {
	deps Deps
	adm  admission
	// The optional deps, defaulted.
	configs Configs
	lander  Lander
	logger  *slog.Logger
	now     func() time.Time
}

// New builds a Scheduler.
func New(d Deps) (*Scheduler, error) {
	if d.Store == nil || d.Leader == nil || d.Refs == nil || d.Builder == nil || d.Gate == nil || d.Authorizer == nil {
		return nil, errors.New("mergequeue: Deps needs Store, Leader, Refs, Builder, Gate and Authorizer")
	}
	sch := &Scheduler{deps: d, configs: d.Configs, lander: d.Lander, logger: d.Logger, now: d.Now}
	if sch.configs == nil {
		sch.configs = StoredConfig{}
	}
	if sch.lander == nil {
		sch.lander = NoLander{}
	}
	if sch.logger == nil {
		sch.logger = slog.Default()
	}
	if sch.now == nil {
		sch.now = time.Now
	}
	sch.adm = admission{store: d.Store, auth: d.Authorizer, now: sch.now}
	return sch, nil
}

// ErrNotLeader is a Tick of a repository another worker leads.
var ErrNotLeader = errors.New("mergequeue: another worker leads this repository's queue")

// Report is what one Tick did.
type Report struct {
	// Idle says why nothing was picked, when nothing was.
	Idle string
	// Batches are the batches the tick created, in order.
	Batches []string
	// Landing is the batch that passed for the picked entries: landed (on)
	// or recorded as would-land (shadow). Empty when none did.
	Landing string
	// Ejected are the entries ejected, by id.
	Ejected []string
	// Degraded is set when the batch was left unproven.
	Degraded bool
}

// Drive leads the repository's queue until ctx ends, a Tick every interval.
// A worker that does not lead it keeps asking, so it takes over when the
// leader stops.
func (s *Scheduler) Drive(ctx context.Context, repoID string, every time.Duration) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := s.Tick(ctx, repoID); err != nil && !errors.Is(err, ErrNotLeader) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.logger.WarnContext(ctx, "merge queue tick failed", "repo_id", repoID, "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Tick takes the repository's lead for one round and drives it: recover what
// a stopped leader left, eject entries whose PR moved on, then pick a batch
// and take it through build, gate and verdict. It is ErrNotLeader when
// another worker leads the repository.
func (s *Scheduler) Tick(ctx context.Context, repoID string) (Report, error) {
	release, ok, err := s.deps.Leader.TryLead(ctx, repoID)
	if err != nil {
		return Report{}, fmt.Errorf("lead %s: %w", repoID, err)
	}
	if !ok {
		return Report{}, ErrNotLeader
	}
	defer release()
	return s.tick(ctx, repoID)
}

func (s *Scheduler) tick(ctx context.Context, repoID string) (Report, error) {
	repo, err := s.deps.Store.GetPipelineRepo(ctx, repoID)
	switch {
	case err != nil:
		return Report{}, err
	case repo == nil:
		return Report{Idle: "not enrolled"}, nil
	case repo.Mode == core.PipelineRepoModeOff:
		return Report{Idle: "mode off"}, nil
	case repo.QueueState == core.PipelineQueuePaused:
		return Report{Idle: "queue paused"}, nil
	}
	cfg, err := s.configs.Resolved(ctx, repo)
	if err != nil {
		return Report{}, fmt.Errorf("config of %s: %w", repo.Repository, err)
	}
	r := &round{
		s: s, repo: repo, cfg: cfg,
		shadow:  repo.Mode == core.PipelineRepoModeShadow,
		records: map[string]*core.PRRecord{},
	}
	if err := r.recover(ctx); err != nil {
		return r.report, err
	}
	if err := r.sweep(ctx); err != nil {
		return r.report, err
	}
	picked, err := r.pick(ctx)
	if err != nil {
		return r.report, err
	}
	if len(picked) == 0 {
		r.report.Idle = "nothing admissible"
		return r.report, nil
	}
	if err := r.pickBase(ctx); err != nil {
		return r.report, err
	}
	err = r.run(ctx, picked)
	// Survivors left in a failed batch wait for the next one, even when the
	// round stopped early.
	for _, id := range r.failed {
		if _, rerr := s.deps.Store.RequeueQueueEntries(context.WithoutCancel(ctx), id); rerr != nil && err == nil {
			err = rerr
		}
	}
	return r.report, err
}

// round is one Tick's state.
type round struct {
	s      *Scheduler
	repo   *core.PipelineRepo
	cfg    config.Resolved
	shadow bool
	// mainSHA is the base branch's real tip, base the commit batches build
	// on: mainSHA, or in shadow the would-be main.
	mainSHA, base string
	simulated     bool
	records       map[string]*core.PRRecord
	// failed are the round's failed batches: entries still batched in one
	// at the end are requeued.
	failed []string
	// passed are the round's passed batches: all but the landing one are
	// released at the end.
	passed []*built
	report Report
}

func (r *round) now() time.Time { return r.s.now() }

func (r *round) record(ctx context.Context, id string) (*core.PRRecord, error) {
	if rec, ok := r.records[id]; ok {
		return rec, nil
	}
	rec, err := r.s.deps.Store.GetQueuePRRecord(ctx, id)
	if err != nil {
		return nil, err
	}
	r.records[id] = rec
	return rec, nil
}

// recover settles what a leader that stopped mid-round left: a batch still
// building, gating or unproven is abandoned and its entries wait again;
// entries left batched in a failed batch wait again; a shadow batch's
// would-land entries go back to waiting once the repository is on (a shadow
// result never lands); and a passed batch of mode on goes back to the lander.
func (r *round) recover(ctx context.Context) error {
	st := r.s.deps.Store
	batches, err := st.LatestQueueBatches(ctx, r.repo.ID, recoverWindow)
	if err != nil {
		return err
	}
	for _, b := range batches {
		switch b.State {
		case core.QueueBatchUnproven:
			if b.Attempt >= 2 {
				// Retried and still unproven: the round's final record.
				continue
			}
			fallthrough
		case core.QueueBatchBuilding, core.QueueBatchGating:
			v := ParseVerdict(b.VerdictJSON)
			v.Note = "abandoned: the queue's leader stopped while it was " + string(b.State)
			if _, err := st.SetQueueBatchState(ctx, b.ID, b.State, core.QueueBatchAbandoned, core.QueueBatchUpdate{VerdictJSON: v.json()}); err != nil &&
				!errors.Is(err, core.ErrPipelineVersionConflict) {
				return err
			}
			if _, err := st.RequeueQueueEntries(ctx, b.ID); err != nil {
				return err
			}
		case core.QueueBatchFailed:
			if _, err := st.RequeueQueueEntries(ctx, b.ID); err != nil {
				return err
			}
		case core.QueueBatchPassed:
			v := ParseVerdict(b.VerdictJSON)
			switch {
			case v.Shadow && !r.shadow:
				v.Note = "abandoned: a shadow result never lands, and the repository is now " + string(r.repo.Mode)
				if _, err := st.SetQueueBatchState(ctx, b.ID, b.State, core.QueueBatchAbandoned, core.QueueBatchUpdate{VerdictJSON: v.json()}); err != nil &&
					!errors.Is(err, core.ErrPipelineVersionConflict) {
					return err
				}
				if _, err := st.RequeueQueueEntries(ctx, b.ID); err != nil {
					return err
				}
			case !v.Shadow && !r.shadow && v.Landable && (!v.UnisolatedAccepted || r.cfg.Gate.AllowUnisolated):
				if err := r.land(ctx, b, v); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// recoverWindow is how many of a repository's newest batches recover reads.
// A round leaves at most a handful open, and recover runs every tick.
const recoverWindow = 50

// sweep ejects the entries whose PR no longer holds them: closed (or
// merged, or gone) and head_moved. Each wakes its PR.
func (r *round) sweep(ctx context.Context) error {
	stale, err := r.s.deps.Store.ListStaleQueueEntries(ctx, r.repo.ID)
	if err != nil {
		return err
	}
	for _, se := range stale {
		reason := core.QueueEjectHeadMoved
		if se.RecordState != core.PRRecordOpen {
			reason = core.QueueEjectClosed
		}
		if err := r.eject(ctx, se.Entry, reason); err != nil {
			return err
		}
	}
	return nil
}

// pick takes the next batch: up to queue.batch_max admissible entries by
// priority, then enqueue time, or an urgent one alone.
func (r *round) pick(ctx context.Context) ([]*core.QueueEntry, error) {
	entries, err := r.s.deps.Store.ListAdmissibleQueueEntries(ctx, r.repo.ID, r.now(), 0)
	if err != nil {
		return nil, err
	}
	max := r.cfg.Queue.BatchMax
	if max <= 0 {
		max = config.DefaultBatchMax
	}
	var picked []*core.QueueEntry
	for _, e := range entries {
		rec, err := r.record(ctx, e.RecordID)
		if err != nil {
			return nil, err
		}
		head := e.HeadSHA
		refusal, err := r.s.adm.admit(ctx, r.repo, r.cfg, rec, head, func(ctx context.Context) ([]string, error) {
			return changedPaths(ctx, r.s.deps.Refs, r.s.deps.Builder, r.repo, head)
		})
		if err != nil {
			return nil, err
		}
		if !refusal.Admissible() {
			if refusal.Code == RefuseLandNotGranted {
				r.s.logger.InfoContext(ctx, "merge queue entry waits: its approver does not hold land",
					"repo_id", r.repo.ID, "entry_id", e.ID, "record_id", e.RecordID, "head", head, "reason", refusal.Reason)
			}
			continue
		}
		if prpolicy.IsUrgent(e.Priority) {
			if len(picked) == 0 {
				return []*core.QueueEntry{e}, nil
			}
			continue
		}
		picked = append(picked, e)
		if len(picked) == max {
			break
		}
	}
	return picked, nil
}

// pickBase reads main's tip. In shadow, the batch builds on the would-be
// main instead: the candidate of the newest shadow batch that passed, as long
// as it was picked against the main that is still the tip. Once the real
// main moves (in shadow the old pipeline still lands), the simulated chain
// starts again from it.
func (r *round) pickBase(ctx context.Context) error {
	tip, err := r.s.deps.Refs.BranchTip(ctx, r.repo, r.repo.BaseBranch)
	if err != nil {
		return fmt.Errorf("read %s: %w", r.repo.BaseBranch, err)
	}
	r.mainSHA, r.base = tip, tip
	if !r.shadow {
		return nil
	}
	batches, err := r.s.deps.Store.LatestQueueBatches(ctx, r.repo.ID, recoverWindow)
	if err != nil {
		return err
	}
	for _, b := range batches {
		if b.State != core.QueueBatchPassed {
			continue
		}
		v := ParseVerdict(b.VerdictJSON)
		if v.Shadow && v.MainSHA == tip && b.CandidateSHA != "" {
			r.base, r.simulated = b.CandidateSHA, true
		}
		break
	}
	return nil
}

// eject takes an entry out of the queue and wakes its PR. An entry already
// out is done.
func (r *round) eject(ctx context.Context, e *core.QueueEntry, reason core.QueueEjectReason) error {
	if _, err := r.s.deps.Store.EjectQueueEntry(ctx, e.ID, reason); err != nil {
		if errors.Is(err, core.ErrPipelineVersionConflict) {
			return nil
		}
		return err
	}
	r.report.Ejected = append(r.report.Ejected, e.ID)
	return r.s.wake(ctx, e.RecordID)
}

// wakeTries bounds wake's compare-and-set retries.
const wakeTries = 3

// wake asks the PR's pipeline to decide again now (next_action=decide): its
// entry left the queue, with the evidence on the entry and its batch.
func (s *Scheduler) wake(ctx context.Context, recordID string) error {
	for range wakeTries {
		p, err := s.deps.Store.GetPRPipeline(ctx, recordID)
		if err != nil || p == nil {
			return err
		}
		now := s.now().UTC()
		to := *p
		to.NextAction, to.NextActionAt = "decide", &now
		_, err = s.deps.Store.TransitionPRPipeline(ctx, recordID, p.Version, to)
		if !errors.Is(err, core.ErrPipelineVersionConflict) {
			return err
		}
	}
	return fmt.Errorf("wake %s: the row kept changing", recordID)
}

// setQueueState moves the repository's queue between running and degraded.
// A paused queue is a person's (or the lander's) to resume.
func (r *round) setQueueState(ctx context.Context, state core.PipelineQueueState) error {
	for range wakeTries {
		repo, err := r.s.deps.Store.GetPipelineRepo(ctx, r.repo.ID)
		if err != nil || repo == nil {
			return err
		}
		if repo.QueueState == state || repo.QueueState == core.PipelineQueuePaused {
			return nil
		}
		_, err = r.s.deps.Store.SetPipelineRepoQueueState(ctx, repo.ID, repo.Version, state, "")
		if !errors.Is(err, core.ErrPipelineVersionConflict) {
			return err
		}
	}
	return fmt.Errorf("set queue state of %s: the row kept changing", r.repo.ID)
}
