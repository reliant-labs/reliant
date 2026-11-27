// Copyright (c) 2025 Reliant Labs
package mergequeue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/buildjob"
)

// built is a batch with a candidate to gate.
type built struct {
	batch   *core.QueueBatch
	entries []*core.QueueEntry
	// heads are what lands for each entry, by entry id: the reviewed head,
	// or a regenerated entry's new head.
	heads map[string]string
	trees []string
	// machine is the gate pool member that built the candidate
	// (buildjob BuildResult.Machine): its gate is tried there first, where
	// the candidate is already fetched. In memory only.
	machine string
	verdict Verdict
	result  GateResult
	// rerunOfRed is the red batch this one reruns on the same tree: a pass
	// here makes that red a flake.
	rerunOfRed *built
}

func (b *built) ids() []string {
	out := make([]string, len(b.entries))
	for i, e := range b.entries {
		out[i] = e.ID
	}
	return out
}

func (r *round) newVerdict() Verdict {
	return Verdict{Mode: r.repo.Mode, Shadow: r.shadow, MainSHA: r.mainSHA, SimulatedBase: r.simulated}
}

// errRaced is a batch whose entries changed under it (one was ejected by a
// sweep or another writer): the round stops, and the next tick picks again.
var errRaced = errors.New("mergequeue: the batch's entries changed while it was built")

// build creates a batch of kind for entries, on the round's base, squashes
// them into its candidate and pushes it. Entries the builder cannot squash
// are ejected (or, for a batch conflict, left to wait). It returns nil when
// no entry remained.
func (r *round) build(ctx context.Context, entries []*core.QueueEntry, kind core.QueueBatchKind, parent string) (*built, error) {
	st := r.s.deps.Store
	batch, err := st.CreateQueueBatch(ctx, core.QueueBatchNew{
		OrgID: r.repo.OrgID, ProjectID: r.repo.ProjectID, RepoID: r.repo.ID,
		BaseSHA: r.base, ParentBatchID: parent, Kind: kind,
	})
	if err != nil {
		return nil, err
	}
	r.report.Batches = append(r.report.Batches, batch.ID)
	v := r.newVerdict()

	byNumber := map[int]*core.QueueEntry{}
	cands := make([]buildjob.Entry, 0, len(entries))
	for _, e := range entries {
		rec, err := r.record(ctx, e.RecordID)
		if err != nil {
			return nil, r.abandon(ctx, batch, v, err)
		}
		if rec == nil {
			return nil, r.abandon(ctx, batch, v, fmt.Errorf("entry %s: no PR record %s", e.ID, e.RecordID))
		}
		byNumber[rec.Number] = e
		cands = append(cands, buildjob.Entry{Number: rec.Number, Head: e.HeadSHA, Title: rec.Title})
	}
	cand, err := r.s.deps.Builder.Build(ctx, r.repo, buildjob.BuildRequest{
		Repository: r.repo.Repository, BatchID: batch.ID, Base: r.base, Entries: cands,
		Generated: slices.Clone(r.cfg.Generated), Regen: r.cfg.Regen, Shadow: r.shadow,
	})
	if err != nil {
		return nil, r.abandon(ctx, batch, v, fmt.Errorf("build batch %s: %w", batch.ID, err))
	}
	if err := checkBuild(r.base, cands, cand); err != nil {
		return nil, r.abandon(ctx, batch, v, fmt.Errorf("build batch %s: %w", batch.ID, err))
	}
	if r.shadow {
		for _, p := range cand.WouldPush {
			v.WouldPush = append(v.WouldPush, WouldPush{Ref: p.Ref, SHA: p.SHA})
		}
	}

	for _, ej := range cand.Ejections {
		e := byNumber[ej.Entry.Number]
		reason, cause, ok := ejectReason(ej.Reason)
		if !ok {
			v.Requeued = append(v.Requeued, e.ID)
			continue
		}
		v.Ejected = append(v.Ejected, EjectedEntry{
			EntryID: e.ID, RecordID: e.RecordID, Number: ej.Entry.Number, Head: e.HeadSHA,
			Reason: reason, Cause: cause, Paths: ej.Paths, Detail: ej.Detail,
		})
		if err := r.eject(ctx, e, reason); err != nil {
			return nil, err
		}
	}
	if len(cand.Included) == 0 {
		v.Note = "no entry could be squashed onto the base"
		_, err := st.SetQueueBatchState(ctx, batch.ID, core.QueueBatchBuilding, core.QueueBatchAbandoned, core.QueueBatchUpdate{VerdictJSON: v.json()})
		return nil, err
	}

	b := &built{batch: batch, heads: map[string]string{}, trees: cand.Trees, machine: cand.Machine, verdict: v}
	for _, ce := range cand.Included {
		e := byNumber[ce.Number]
		b.entries = append(b.entries, e)
		b.heads[e.ID] = ce.Head
	}
	if err := st.AttachQueueEntries(ctx, batch.ID, b.ids()); err != nil {
		if errors.Is(err, core.ErrQueueEntryNotAttachable) {
			return nil, r.abandon(ctx, batch, v, errRaced)
		}
		return nil, r.abandon(ctx, batch, v, err)
	}
	for _, rg := range cand.Regenerated {
		e := byNumber[rg.Entry.Number]
		b.verdict.Regenerated = append(b.verdict.Regenerated, RegenEntry{EntryID: e.ID, Head: rg.Entry.Head, NewHead: rg.NewHead, Conflicts: rg.Conflicts})
		if !r.shadow {
			if err := r.recordGeneratedOnly(ctx, e, rg.NewHead); err != nil {
				return nil, err
			}
		}
	}
	trees, err := json.Marshal(cand.Trees)
	if err != nil {
		return nil, err
	}
	batch, err = st.SetQueueBatchState(ctx, batch.ID, core.QueueBatchBuilding, core.QueueBatchGating, core.QueueBatchUpdate{
		CandidateSHA: cand.Commit, CandidateRef: cand.Ref, ExpectedTreesJSON: trees,
		GateRunner: core.QueueGateRunnerMachine, VerdictJSON: b.verdict.json(),
	})
	if err != nil {
		return nil, err
	}
	b.batch = batch
	return b, nil
}

// abandon gives up a batch still building, for cause, and returns cause.
func (r *round) abandon(ctx context.Context, batch *core.QueueBatch, v Verdict, cause error) error {
	v.Note = "abandoned while building: " + cause.Error()
	if _, err := r.s.deps.Store.SetQueueBatchState(context.WithoutCancel(ctx), batch.ID, core.QueueBatchBuilding, core.QueueBatchAbandoned,
		core.QueueBatchUpdate{VerdictJSON: v.json()}); err != nil {
		return errors.Join(cause, err)
	}
	if _, err := r.s.deps.Store.RequeueQueueEntries(context.WithoutCancel(ctx), batch.ID); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

// recordGeneratedOnly writes the regenerated head's verdict: the reviewed
// head's, under the rule generated_only (§3.2 step 2), so the PR at its new
// head is admissible without another review. It carries the reviewed
// verdict's activation and machine, which the land grant is judged on.
func (r *round) recordGeneratedOnly(ctx context.Context, e *core.QueueEntry, newHead string) error {
	st := r.s.deps.Store
	reviewed, err := st.LatestPRHeadVerdict(ctx, e.RecordID, e.HeadSHA)
	if err != nil {
		return err
	}
	if reviewed == nil {
		return fmt.Errorf("regenerated entry %s has no verdict on %s", e.ID, e.HeadSHA)
	}
	_, err = st.InsertPRHeadVerdict(ctx, core.PRHeadVerdictRecord{
		OrgID: r.repo.OrgID, ProjectID: r.repo.ProjectID, RecordID: e.RecordID, HeadSHA: newHead, Round: 1,
		Verdict: core.PRHeadVerdictPass, Rule: core.PRVerdictRuleGeneratedOnly, RiskTier: reviewed.RiskTier,
		RequiredDecisions: reviewed.RequiredDecisions, ReviewerPrincipal: QueuePrincipal,
		// The reviewed head's activation and machine: the land grant of a
		// regenerated head is the reviewing run's, never the queue's own.
		TriggerID: reviewed.TriggerID, DaemonID: reviewed.DaemonID,
	})
	if errors.Is(err, core.ErrPipelineVerdictExists) {
		return nil
	}
	return err
}

// QueuePrincipal is the reviewer_principal of the verdicts the queue writes
// itself (a regenerated head's generated_only).
const QueuePrincipal = "reliant-queue"

// gate runs the gate on a gating batch (attempt counts from 1) and records
// its verdict: the batch moves to passed, failed or unproven. In mode on a
// pass outside an isolation boundary is unproven: it can never land. With
// ejectOnFail a red verdict ejects every entry (gate_red), recorded in the
// failed batch's verdict as the evidence.
func (r *round) gate(ctx context.Context, b *built, attempt int, avoid []string, ejectOnFail bool) error {
	req := GateRequest{
		RepoID: r.repo.ID, BatchID: b.batch.ID, Attempt: attempt,
		CandidateRef: strings.TrimPrefix(b.batch.CandidateRef, "refs/heads/"), CandidateSHA: b.batch.CandidateSHA,
		BaseSHA: b.batch.BaseSHA, Gate: r.cfg.Gate, Shadow: r.shadow, AvoidMachines: avoid,
	}
	if !slices.Contains(avoid, b.machine) {
		req.PreferMachine = b.machine
	}
	res, err := r.s.deps.Gate.Run(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			// Left gating: the next leader abandons and requeues it.
			return ctx.Err()
		}
		res = GateResult{Class: GateUnproven, ReasonCode: "dispatch_failed", Reason: err.Error()}
	}
	if res.Class != GatePass && res.Class != GateFail {
		res.Class = GateUnproven
	}
	if res.Class == GatePass && !res.Isolated() && !r.shadow && !r.cfg.Gate.AllowUnisolated {
		res.Class, res.ReasonCode, res.Reason = GateUnproven, "not_isolated",
			fmt.Sprintf("the gate passed on %q, outside an isolation boundary: it can never land", res.Isolation)
	}
	b.result = res
	v := &b.verdict
	if v.Class != "" {
		v.Attempts = append(v.Attempts, GateAttempt{Attempt: attempt - 1, Class: v.Class, Machine: v.Machine, Isolation: v.Isolation, ReasonCode: v.ReasonCode, Reason: v.Reason})
	}
	v.Class, v.Landable, v.Isolation, v.Machine = res.Class, r.landable(res), res.Isolation, res.Machine
	v.UnisolatedAccepted = v.Landable && !res.Isolated()
	v.ReasonCode, v.Reason, v.FailingTests, v.InfraMatches = res.ReasonCode, res.Reason, res.FailingTests, res.InfraMatches
	if res.Class == GatePass && r.shadow {
		v.WouldLand = r.landEntries(b)
	}
	if res.Class == GatePass && b.rerunOfRed != nil {
		v.Flaky = b.rerunOfRed.result.FailingTests
		v.Note = "flake: batch " + b.rerunOfRed.batch.ID + " was red on this tree, which passed on the rerun"
	}
	if res.Class == GateFail && ejectOnFail {
		for _, le := range r.landEntries(b) {
			v.Ejected = append(v.Ejected, EjectedEntry{
				EntryID: le.EntryID, RecordID: le.RecordID, Number: le.Number, Head: le.Head,
				Reason: core.QueueEjectGateRed, FailingTests: res.FailingTests, Batches: r.redBatches(b),
			})
		}
	}
	to := map[GateClass]core.QueueBatchState{GatePass: core.QueueBatchPassed, GateFail: core.QueueBatchFailed, GateUnproven: core.QueueBatchUnproven}[res.Class]
	u := core.QueueBatchUpdate{VerdictJSON: v.json(), GateMachineID: res.Machine, StagesJSON: res.Stages}
	if iso := core.QueueIsolationBackend(res.Isolation); slices.Contains(core.AllQueueIsolationBackends, iso) {
		u.IsolationBackend = iso
	}
	if len(u.StagesJSON) > 0 && u.StagesJSON[0] != '{' {
		u.StagesJSON = nil
	}
	batch, err := r.s.deps.Store.SetQueueBatchState(ctx, b.batch.ID, b.batch.State, to, u)
	if err != nil {
		return err
	}
	b.batch = batch
	if to == core.QueueBatchPassed {
		r.passed = append(r.passed, b)
	}
	if to == core.QueueBatchFailed {
		r.failed = append(r.failed, batch.ID)
		if ejectOnFail {
			for _, e := range b.entries {
				if err := r.eject(ctx, e, core.QueueEjectGateRed); err != nil {
					return err
				}
			}
		}
	}
	if res.Class != GateUnproven && r.repo.QueueState == core.PipelineQueueDegraded {
		if err := r.setQueueState(ctx, core.PipelineQueueRunning); err != nil {
			return err
		}
		r.repo.QueueState = core.PipelineQueueRunning
	}
	return nil
}

// landable is the hard rule: a batch lands only on a pass produced inside an
// isolation boundary, or, in a repository whose pipeline.yaml sets
// gate.allow_unisolated, on any pass (owner, 2026-11-26: until isolated gate
// machines exist), marked verdict_json.unisolated_accepted.
func (r *round) landable(res GateResult) bool {
	return res.Landable() || (res.Class == GatePass && r.cfg.Gate.AllowUnisolated)
}

// retryGate gates an unproven batch again, on another member.
func (r *round) retryGate(ctx context.Context, b *built, ejectOnFail bool) error {
	batch, err := r.s.deps.Store.SetQueueBatchState(ctx, b.batch.ID, core.QueueBatchUnproven, core.QueueBatchGating, core.QueueBatchUpdate{})
	if err != nil {
		return err
	}
	b.batch = batch
	var avoid []string
	for _, a := range b.verdict.Attempts {
		if a.Machine != "" {
			avoid = append(avoid, a.Machine)
		}
	}
	if b.verdict.Machine != "" {
		avoid = append(avoid, b.verdict.Machine)
	}
	return r.gate(ctx, b, batch.Attempt, avoid, ejectOnFail)
}

// redBatches are the batches that judged b's entries red: b, and the batch
// b reruns.
func (r *round) redBatches(b *built) []string {
	out := []string{b.batch.ID}
	if b.batch.Kind == core.QueueBatchRerun && b.batch.ParentBatchID != "" {
		out = append([]string{b.batch.ParentBatchID}, out...)
	}
	return out
}

func (r *round) landEntries(b *built) []LandEntry {
	out := make([]LandEntry, len(b.entries))
	for i, e := range b.entries {
		n := 0
		if rec := r.records[e.RecordID]; rec != nil {
			n = rec.Number
		}
		out[i] = LandEntry{EntryID: e.ID, RecordID: e.RecordID, Number: n, Head: b.heads[e.ID], Tree: b.trees[i]}
	}
	return out
}

// finish settles the batch that passed for the round's entries. Shadow
// records it, would-land, and main_health (source gate) for its candidate,
// the would-be main the next batch builds on. On, a landable result goes to
// the lander.
func (r *round) finish(ctx context.Context, b *built) error {
	r.report.Landing = b.batch.ID
	if r.shadow {
		_, err := r.s.deps.Store.InsertMainHealth(ctx, core.MainHealthRecord{
			OrgID: r.repo.OrgID, ProjectID: r.repo.ProjectID, RepoID: r.repo.ID, SHA: b.batch.CandidateSHA,
			Source: core.MainHealthGate, Verdict: core.MainHealthPass, BatchID: b.batch.ID,
		})
		return err
	}
	if !r.landable(b.result) {
		return fmt.Errorf("batch %s passed but is not landable", b.batch.ID)
	}
	return r.land(ctx, b.batch, b.verdict)
}

// land hands a passed batch to the lander.
func (r *round) land(ctx context.Context, batch *core.QueueBatch, v Verdict) error {
	entries, err := r.s.deps.Store.ListQueueBatchEntries(ctx, batch.ID)
	if err != nil {
		return err
	}
	var trees []string
	if len(batch.ExpectedTreesJSON) > 0 {
		if err := json.Unmarshal(batch.ExpectedTreesJSON, &trees); err != nil {
			return fmt.Errorf("batch %s expected trees: %w", batch.ID, err)
		}
	}
	heads := map[string]string{}
	for _, e := range entries {
		heads[e.ID] = e.HeadSHA
	}
	for _, rg := range v.Regenerated {
		heads[rg.EntryID] = rg.NewHead
	}
	var active []*core.QueueEntry
	started := false
	for _, e := range entries {
		if e.State == core.QueueEntryBatched || e.State == core.QueueEntryLanded {
			active = append(active, e)
		}
		started = started || e.State == core.QueueEntryLanded
	}
	if !started {
		// Before the first squash merge, every head's land grant again: one
		// revoked since admission stops the whole batch. Once a merge is
		// made, the lander finishes what the gate proved.
		refused, err := r.landRefusals(ctx, active)
		if err != nil {
			return err
		}
		if len(refused) > 0 {
			return r.abandonUngranted(ctx, batch, v, refused)
		}
	}
	return r.s.lander.Land(ctx, LandRequest{Repo: r.repo, Batch: batch, Entries: active, Heads: heads, Trees: trees})
}

// landRefusals judges each entry's land grant on the verdict that admitted
// it: its reviewed head's (a regenerated entry's generated_only verdict
// carries the same activation and machine).
func (r *round) landRefusals(ctx context.Context, entries []*core.QueueEntry) ([]LandRefusal, error) {
	var out []LandRefusal
	for _, e := range entries {
		v, err := r.s.deps.Store.LatestPRHeadVerdict(ctx, e.RecordID, e.HeadSHA)
		if err != nil {
			return nil, err
		}
		if v == nil || v.Verdict != core.PRHeadVerdictPass {
			out = append(out, LandRefusal{EntryID: e.ID, Head: e.HeadSHA, Reason: "no pass verdict on " + e.HeadSHA})
			continue
		}
		ref, err := landGranted(ctx, r.s.deps.Authorizer, r.repo, v)
		if err != nil {
			return nil, err
		}
		if !ref.Admissible() {
			out = append(out, LandRefusal{EntryID: e.ID, Head: e.HeadSHA, Reason: ref.Reason})
		}
	}
	return out, nil
}

// abandonUngranted gives up a passed batch an entry of which may no longer
// land: nothing merges, the refusals are recorded in its verdict, and its
// entries wait again (where admission refuses the ungranted ones).
func (r *round) abandonUngranted(ctx context.Context, batch *core.QueueBatch, v Verdict, refused []LandRefusal) error {
	v.LandNotGranted = refused
	v.Note = fmt.Sprintf("abandoned before landing: %d of its entries' approvers no longer hold land", len(refused))
	if _, err := r.s.deps.Store.SetQueueBatchState(ctx, batch.ID, core.QueueBatchPassed, core.QueueBatchAbandoned,
		core.QueueBatchUpdate{VerdictJSON: v.json()}); err != nil {
		if errors.Is(err, core.ErrPipelineVersionConflict) {
			return nil
		}
		return err
	}
	if _, err := r.s.deps.Store.RequeueQueueEntries(ctx, batch.ID); err != nil {
		return err
	}
	r.s.logger.WarnContext(ctx, "merge queue batch abandoned before landing: a land grant was revoked",
		"repo_id", r.repo.ID, "batch_id", batch.ID, "refused", len(refused), "reason", refused[0].Reason)
	return nil
}

// QueueRefPrefix is the namespace of the queue's candidate refs, the one
// GitHub write a shadow build makes.
const QueueRefPrefix = "refs/heads/reliant-queue/"

// checkBuild holds a daemon's build report to the request: the daemon runs
// on a user's machine, so its report is checked before the queue records
// it. Every included or ejected entry is one the batch asked for, at the
// head asked (a regenerated entry's new head is reported separately), the
// candidate is on the batch's queue ref, and there is one tree per included
// entry.
func checkBuild(base string, asked []buildjob.Entry, res buildjob.BuildResult) error {
	heads := map[int]string{}
	for _, e := range asked {
		heads[e.Number] = e.Head
	}
	regen := map[int]string{}
	for _, rg := range res.Regenerated {
		if heads[rg.Entry.Number] != rg.Entry.Head {
			return fmt.Errorf("the build regenerated #%d at %s, which the batch did not ask for", rg.Entry.Number, rg.Entry.Head)
		}
		regen[rg.Entry.Number] = rg.NewHead
	}
	seen := map[int]bool{}
	for _, e := range res.Included {
		want, ok := heads[e.Number]
		if nn, regenerated := regen[e.Number]; regenerated {
			want = nn
		}
		if !ok || e.Head != want || seen[e.Number] {
			return fmt.Errorf("the build included #%d at %s, which the batch did not ask for", e.Number, e.Head)
		}
		seen[e.Number] = true
	}
	for _, ej := range res.Ejections {
		if _, ok := heads[ej.Entry.Number]; !ok || seen[ej.Entry.Number] {
			return fmt.Errorf("the build ejected #%d, which the batch did not ask for", ej.Entry.Number)
		}
		seen[ej.Entry.Number] = true
	}
	if len(res.Included) == 0 {
		return nil
	}
	switch {
	case res.Base != base:
		return fmt.Errorf("the build started from %s, not %s", res.Base, base)
	case res.Commit == "" || !strings.HasPrefix(res.Ref, QueueRefPrefix):
		return fmt.Errorf("the build pushed %q to %q, outside the queue's refs", res.Commit, res.Ref)
	case len(res.Trees) != len(res.Included):
		return fmt.Errorf("the build reported %d trees for %d entries", len(res.Trees), len(res.Included))
	}
	return nil
}
