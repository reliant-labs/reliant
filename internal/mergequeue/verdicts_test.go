// Copyright (c) 2025 Reliant Labs
package mergequeue_test

import (
	"slices"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue"
)

func fail(tests ...string) mergequeue.GateResult {
	return mergequeue.GateResult{Class: mergequeue.GateFail, Isolation: "sysbox", Machine: "m1", ReasonCode: "stage", FailingTests: tests}
}

func unproven(machine string) mergequeue.GateResult {
	return mergequeue.GateResult{Class: mergequeue.GateUnproven, Isolation: "sysbox", Machine: machine, ReasonCode: "backend_failed"}
}

func TestRedSingleThatPassesOnRerunIsAFlake(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	p := f.queue("flaky", 50, nil)
	f.gate.judge = func(_ mergequeue.GateRequest, _ []string, prior int) mergequeue.GateResult {
		if prior == 0 {
			return fail("TestSometimes")
		}
		return pass()
	}
	rep := f.tick(f.scheduler())
	if len(rep.Batches) != 2 || rep.Landing != rep.Batches[1] || len(rep.Ejected) != 0 {
		t.Fatalf("report %+v", rep)
	}
	red, rerun := f.batch(rep.Batches[0]), f.batch(rep.Batches[1])
	if red.State != core.QueueBatchFailed || rerun.Kind != core.QueueBatchRerun || rerun.ParentBatchID != red.ID ||
		rerun.CandidateSHA != red.CandidateSHA || rerun.State != core.QueueBatchPassed {
		t.Fatalf("the rerun gates the same tree: %+v / %+v", red, rerun)
	}
	v := f.verdict(rerun.ID)
	if !slices.Equal(v.Flaky, []string{"TestSometimes"}) || !slices.Equal(f.branches(v.WouldLand), []string{"flaky"}) {
		t.Fatalf("flake verdict %+v", v)
	}
	if e := f.entry(p); e.State != core.QueueEntryBatched || e.BatchID != rerun.ID {
		t.Fatalf("entry %+v", e)
	}
}

func TestRedSingleTwiceIsEjectedWithEvidence(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	p := f.queue("bad", 50, nil)
	f.gate.judge = func(mergequeue.GateRequest, []string, int) mergequeue.GateResult { return fail("TestBroken") }
	rep := f.tick(f.scheduler())
	if rep.Landing != "" || !slices.Equal(rep.Ejected, []string{p.entryID}) || len(rep.Batches) != 2 {
		t.Fatalf("report %+v", rep)
	}
	if e := f.entry(p); e.State != core.QueueEntryEjected || e.EjectReason != core.QueueEjectGateRed || e.BatchID != rep.Batches[1] {
		t.Fatalf("entry %+v", e)
	}
	v := f.verdict(rep.Batches[1])
	if len(v.Ejected) != 1 || !slices.Equal(v.Ejected[0].FailingTests, []string{"TestBroken"}) ||
		!slices.Equal(v.Ejected[0].Batches, rep.Batches) || v.Ejected[0].Reason != core.QueueEjectGateRed {
		t.Fatalf("evidence %+v", v.Ejected)
	}
	if row := f.pipeline(p); row.NextAction != "decide" {
		t.Fatalf("not woken: %+v", row)
	}
}

func TestRedPairBisectsToTheCulprit(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	a := f.queue("a", 50, nil)
	bad := f.queue("bad", 50, nil)
	f.gate.judge = func(_ mergequeue.GateRequest, branches []string, _ int) mergequeue.GateResult {
		if slices.Contains(branches, "bad") {
			return fail("TestBroken")
		}
		return pass()
	}
	rep := f.tick(f.scheduler())
	want := [][]string{{"a", "bad"}, {"a"}, {"bad"}, {"bad"}}
	if got := f.gate.gated(); !slices.EqualFunc(got, want, slices.Equal[[]string]) {
		t.Fatalf("gated %v, want %v", got, want)
	}
	landing := f.batch(rep.Landing)
	if landing.Kind != core.QueueBatchBisect || landing.ParentBatchID != rep.Batches[0] || landing.State != core.QueueBatchPassed {
		t.Fatalf("the green half lands: %+v", landing)
	}
	if got := f.branches(f.verdict(landing.ID).WouldLand); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("would land %v", got)
	}
	if e := f.entry(bad); e.EjectReason != core.QueueEjectGateRed {
		t.Fatalf("culprit %+v", e)
	}
	if e := f.entry(a); e.State != core.QueueEntryBatched || e.BatchID != landing.ID {
		t.Fatalf("a %+v", e)
	}
	h, err := f.db.LatestMainHealth(f.ctx, f.repo.ID)
	if err != nil || h == nil || h.SHA != landing.CandidateSHA {
		t.Fatalf("main health %+v %v", h, err)
	}
	// The next batch builds on the half that would have landed.
	f.queue("c", 50, nil)
	next := f.batch(f.tick(f.scheduler()).Landing)
	if next.BaseSHA != landing.CandidateSHA {
		t.Fatalf("next base %s, want %s", next.BaseSHA, landing.CandidateSHA)
	}
}

func TestEveryHalfPassingRerunsTheWholeBatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	f.queue("a", 50, nil)
	f.queue("b", 50, nil)
	f.gate.judge = func(_ mergequeue.GateRequest, branches []string, prior int) mergequeue.GateResult {
		if len(branches) == 2 && prior == 0 {
			return fail("TestRace")
		}
		return pass()
	}
	rep := f.tick(f.scheduler())
	want := [][]string{{"a", "b"}, {"a"}, {"b"}, {"a", "b"}}
	if got := f.gate.gated(); !slices.EqualFunc(got, want, slices.Equal[[]string]) {
		t.Fatalf("gated %v, want %v", got, want)
	}
	rerun := f.batch(rep.Landing)
	v := f.verdict(rerun.ID)
	if rerun.Kind != core.QueueBatchRerun || rerun.ParentBatchID != rep.Batches[0] ||
		!slices.Equal(v.Flaky, []string{"TestRace"}) || !slices.Equal(f.branches(v.WouldLand), []string{"a", "b"}) {
		t.Fatalf("the whole batch lands as a flake: %+v %+v", rerun, v)
	}
	for _, id := range rep.Batches[1:3] {
		if b := f.batch(id); b.State != core.QueueBatchAbandoned || len(f.verdict(id).WouldLand) != 0 {
			t.Fatalf("a superseded half is withdrawn: %+v", b)
		}
	}
}

func TestHalvesThatOnlyFailTogetherLandTheFirstHalf(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	f.queue("a", 50, nil)
	b := f.queue("b", 50, nil)
	f.gate.judge = func(_ mergequeue.GateRequest, branches []string, _ int) mergequeue.GateResult {
		if len(branches) == 2 {
			return fail("TestTogether")
		}
		return pass()
	}
	rep := f.tick(f.scheduler())
	want := [][]string{{"a", "b"}, {"a"}, {"b"}, {"a", "b"}, {"a"}}
	if got := f.gate.gated(); !slices.EqualFunc(got, want, slices.Equal[[]string]) {
		t.Fatalf("gated %v, want %v", got, want)
	}
	if got := f.branches(f.verdict(rep.Landing).WouldLand); !slices.Equal(got, []string{"a"}) || len(rep.Ejected) != 0 {
		t.Fatalf("would land %v, ejected %v", got, rep.Ejected)
	}
	if e := f.entry(b); e.State != core.QueueEntryWaiting {
		t.Fatalf("b waits for the next batch: %+v", e)
	}
}

func TestUnprovenRetriesOnAnotherMember(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	f.queue("a", 50, nil)
	f.gate.judge = func(_ mergequeue.GateRequest, _ []string, prior int) mergequeue.GateResult {
		if prior == 0 {
			return unproven("m1")
		}
		r := pass()
		r.Machine = "m2"
		return r
	}
	rep := f.tick(f.scheduler())
	reqs := f.gate.requests()
	if len(reqs) != 2 || reqs[1].Attempt != 2 || !slices.Equal(reqs[1].AvoidMachines, []string{"m1"}) || reqs[1].BatchID != reqs[0].BatchID {
		t.Fatalf("retry: %+v", reqs)
	}
	b := f.batch(rep.Landing)
	v := f.verdict(b.ID)
	if b.State != core.QueueBatchPassed || b.Attempt != 2 || b.GateMachineID != "m2" || len(v.Attempts) != 1 || v.Attempts[0].Machine != "m1" {
		t.Fatalf("batch %+v verdict %+v", b, v)
	}
	if rep.Degraded || f.queueState() != core.PipelineQueueRunning {
		t.Fatalf("a proven retry does not degrade: %+v", rep)
	}
}

func TestUnprovenTwiceDegradesAndLandsNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeOn)
	a := f.queue("a", 50, nil)
	f.gate.judge = func(mergequeue.GateRequest, []string, int) mergequeue.GateResult { return unproven("m1") }
	s := f.scheduler()
	rep := f.tick(s)
	if !rep.Degraded || rep.Landing != "" || len(f.land.lands) != 0 {
		t.Fatalf("report %+v lands %d", rep, len(f.land.lands))
	}
	b := f.batch(rep.Batches[0])
	if b.State != core.QueueBatchUnproven || b.Attempt != 2 {
		t.Fatalf("batch %+v", b)
	}
	if e := f.entry(a); e.State != core.QueueEntryWaiting {
		t.Fatalf("entry waits again: %+v", e)
	}
	if st := f.queueState(); st != core.PipelineQueueDegraded {
		t.Fatalf("queue %s", st)
	}

	// A proven gate brings the queue back; the unproven batch stays the record.
	f.gate.judge = nil
	rep = f.tick(s)
	if rep.Landing == "" || len(f.land.lands) != 1 || f.queueState() != core.PipelineQueueRunning {
		t.Fatalf("recovered: %+v", rep)
	}
	if b := f.batch(b.ID); b.State != core.QueueBatchUnproven {
		t.Fatalf("the unproven record is kept: %+v", b)
	}
}
