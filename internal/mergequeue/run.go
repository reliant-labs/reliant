// Copyright (c) 2025 Reliant Labs
package mergequeue

import (
	"context"
	"errors"
	"slices"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// run takes the picked entries through a batch and its verdicts (§3.2 steps
// 5-8): the batch that passes for them lands (on) or is recorded as
// would-land (shadow); a red one is rerun or bisected until each culprit is
// ejected; an unproven one is retried once on another member, and if still
// unproven nothing lands, every entry waits again and the queue is degraded.
func (r *round) run(ctx context.Context, picked []*core.QueueEntry) error {
	b, err := r.build(ctx, picked, core.QueueBatchNormal, "")
	if errors.Is(err, errRaced) {
		return nil
	}
	if err != nil || b == nil {
		return err
	}
	land, err := r.judge(ctx, b, true)
	switch {
	case errors.Is(err, errUnproven):
		return r.degrade(ctx)
	case errors.Is(err, errRaced):
		return r.settle(ctx, nil)
	case err != nil:
		return err
	}
	if err := r.settle(ctx, land); err != nil {
		return err
	}
	if land == nil {
		return nil
	}
	return r.finish(ctx, land)
}

// errUnproven is a round that met a gate unproven twice.
var errUnproven = errors.New("mergequeue: the gate stayed unproven")

// judge gates b and resolves its verdict into the batch that lands for b's
// entries (b itself, a rerun of it, or a bisect half), or nil when none
// does. wholeRerun allows the one rerun of the whole batch when every half
// of its bisection passes.
func (r *round) judge(ctx context.Context, b *built, wholeRerun bool) (*built, error) {
	if err := r.gateProven(ctx, b, false); err != nil {
		return nil, err
	}
	switch {
	case b.result.Class == GatePass:
		return b, nil
	case len(b.entries) == 1:
		// Red alone: rerun the same tree once. A pass is a flake; a second
		// red ejects the entry (gate_red), with both runs as evidence.
		rr, err := r.rerunOf(ctx, b)
		if err != nil {
			return nil, err
		}
		if err := r.gateProven(ctx, rr, true); err != nil {
			return nil, err
		}
		if rr.result.Class == GatePass {
			return rr, nil
		}
		return nil, nil
	}
	return r.bisect(ctx, b, wholeRerun)
}

// bisect gates b's halves as candidates on b's base (kind bisect, parent b),
// each judged as a batch of its own: a red half is rerun (one entry) or
// bisected further, so each culprit is ejected. When exactly one passed
// batch holds every survivor, it lands. When the halves' passes are spread
// over several batches, the survivors wait for a batch of them all. When
// every half passed and nothing was ejected, b's red did not reproduce: b is
// rerun once, and a pass lands it as a flake.
func (r *round) bisect(ctx context.Context, b *built, wholeRerun bool) (*built, error) {
	cut := (len(b.entries) + 1) / 2
	halves := [][]*core.QueueEntry{b.entries[:cut], b.entries[cut:]}
	var passed []*built
	everyHalfPassed := true
	for _, half := range halves {
		hb, err := r.build(ctx, half, core.QueueBatchBisect, b.batch.ID)
		if err != nil {
			return nil, err
		}
		if hb == nil || len(hb.entries) < len(half) {
			everyHalfPassed = false
		}
		if hb == nil {
			continue
		}
		got, err := r.judge(ctx, hb, wholeRerun)
		if err != nil {
			return nil, err
		}
		if got != hb {
			everyHalfPassed = false
		}
		if got != nil {
			passed = append(passed, got)
		}
	}

	if everyHalfPassed && wholeRerun {
		return r.rerunWhole(ctx, b, passed)
	}
	survivors := r.survivors(b)
	if len(passed) == 1 && sameEntries(passed[0].entries, survivors) {
		return passed[0], nil
	}
	return nil, r.release(ctx, passed, "its entries wait for one batch of every survivor")
}

// rerunWhole reruns b's tree once after every half of it passed. A pass is a
// flake and lands b's entries. A second red, with every half green, is the
// halves interacting: the first half lands alone (gated again as a child of
// the rerun), and the rest wait for the next batch, built on the main it
// makes.
func (r *round) rerunWhole(ctx context.Context, b *built, halves []*built) (*built, error) {
	if err := r.release(ctx, halves, "superseded by the whole batch's rerun"); err != nil {
		return nil, err
	}
	rr, err := r.rerunOf(ctx, b)
	if err != nil {
		return nil, err
	}
	if err := r.gateProven(ctx, rr, false); err != nil {
		return nil, err
	}
	if rr.result.Class == GatePass {
		return rr, nil
	}
	first := halves[0].entries
	fb, err := r.build(ctx, first, core.QueueBatchBisect, rr.batch.ID)
	if err != nil || fb == nil {
		return nil, err
	}
	return r.judge(ctx, fb, false)
}

// gateProven gates b, and once more on another member when the first gate is
// unproven. Still unproven, it is errUnproven.
func (r *round) gateProven(ctx context.Context, b *built, ejectOnFail bool) error {
	if err := r.gate(ctx, b, b.batch.Attempt, nil, ejectOnFail); err != nil {
		return err
	}
	if b.result.Class != GateUnproven {
		return nil
	}
	if err := r.retryGate(ctx, b, ejectOnFail); err != nil {
		return err
	}
	if b.result.Class == GateUnproven {
		return errUnproven
	}
	return nil
}

// rerunOf is a batch that gates b's candidate again, the same tree at the
// same sha (kind rerun, parent b), with b's entries.
func (r *round) rerunOf(ctx context.Context, b *built) (*built, error) {
	st := r.s.deps.Store
	batch, err := st.CreateQueueBatch(ctx, core.QueueBatchNew{
		OrgID: r.repo.OrgID, ProjectID: r.repo.ProjectID, RepoID: r.repo.ID,
		BaseSHA: b.batch.BaseSHA, ParentBatchID: b.batch.ID, Kind: core.QueueBatchRerun,
	})
	if err != nil {
		return nil, err
	}
	r.report.Batches = append(r.report.Batches, batch.ID)
	rr := &built{batch: batch, entries: b.entries, heads: b.heads, trees: b.trees, verdict: r.newVerdict(), rerunOfRed: b}
	rr.verdict.Regenerated = b.verdict.Regenerated
	if err := st.AttachQueueEntries(ctx, batch.ID, rr.ids()); err != nil {
		if errors.Is(err, core.ErrQueueEntryNotAttachable) {
			err = errRaced
		}
		return nil, r.abandon(ctx, batch, rr.verdict, err)
	}
	if rr.batch, err = st.SetQueueBatchState(ctx, batch.ID, core.QueueBatchBuilding, core.QueueBatchGating, core.QueueBatchUpdate{
		CandidateSHA: b.batch.CandidateSHA, CandidateRef: b.batch.CandidateRef, ExpectedTreesJSON: b.batch.ExpectedTreesJSON,
		GateRunner: core.QueueGateRunnerMachine, VerdictJSON: rr.verdict.json(),
	}); err != nil {
		return nil, err
	}
	return rr, nil
}

// survivors are b's entries the round has not ejected.
func (r *round) survivors(b *built) []*core.QueueEntry {
	var out []*core.QueueEntry
	for _, e := range b.entries {
		if !slices.Contains(r.report.Ejected, e.ID) {
			out = append(out, e)
		}
	}
	return out
}

func sameEntries(a, b []*core.QueueEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}

// release gives up passed batches that are not the round's landing: each is
// abandoned (its would-land record withdrawn) and its entries wait again.
func (r *round) release(ctx context.Context, batches []*built, why string) error {
	st := r.s.deps.Store
	for _, b := range batches {
		if b.batch.State != core.QueueBatchPassed {
			continue
		}
		v := b.verdict
		v.WouldLand, v.Note = nil, "abandoned after passing: "+why
		batch, err := st.SetQueueBatchState(ctx, b.batch.ID, core.QueueBatchPassed, core.QueueBatchAbandoned, core.QueueBatchUpdate{VerdictJSON: v.json()})
		if err != nil {
			return err
		}
		b.batch, b.verdict = batch, v
		if _, err := st.RequeueQueueEntries(ctx, b.batch.ID); err != nil {
			return err
		}
	}
	return nil
}

// settle releases the round's passed batches other than land.
func (r *round) settle(ctx context.Context, land *built) error {
	var others []*built
	for _, b := range r.passed {
		if b != land {
			others = append(others, b)
		}
	}
	return r.release(ctx, others, "not the batch that lands for its entries")
}

// degrade leaves the round unproven: nothing lands, every entry the round
// batched waits again, and the queue is degraded until a gate is proven.
func (r *round) degrade(ctx context.Context) error {
	if err := r.settle(ctx, nil); err != nil {
		return err
	}
	for _, id := range r.report.Batches {
		if _, err := r.s.deps.Store.RequeueQueueEntries(ctx, id); err != nil {
			return err
		}
	}
	r.report.Degraded = true
	return r.setQueueState(ctx, core.PipelineQueueDegraded)
}
