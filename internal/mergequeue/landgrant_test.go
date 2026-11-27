// Copyright (c) 2025 Reliant Labs
package mergequeue_test

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue"
	"github.com/reliant-labs/reliant/internal/mergequeue/buildjob"
)

// In mode on an entry is admitted only while the run that approved its head
// holds land: a refused one waits unbuilt (no batch, no gate) instead of
// being rebuilt every tick, and queue.enqueue refuses it with the same code.
func TestOnModeAdmitsOnlyHeadsWhoseApproverHoldsLand(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeOn)
	a := f.queue("a", 50, nil)
	f.auth.revoke(testTrigger)
	s := f.scheduler()

	rep := f.tick(s)
	if rep.Idle != "nothing admissible" || len(rep.Batches) != 0 || len(f.gate.gated()) != 0 || len(f.land.lands) != 0 {
		t.Fatalf("an ungranted head is not built: %+v", rep)
	}
	if e := f.entry(a); e.State != core.QueueEntryWaiting {
		t.Fatalf("the entry waits: %+v", e)
	}
	if res := f.enqueue(a, nil); res["queued"] != false || res["code"] != mergequeue.RefuseLandNotGranted ||
		!strings.Contains(res["reason"].(string), "revoked") {
		t.Fatalf("queue.enqueue refuses it, with the reason: %v", res)
	}
	if len(f.auth.asked) == 0 || f.auth.asked[0].TriggerID != testTrigger || f.auth.asked[0].DaemonID != testDaemon || f.auth.asked[0].RepoID != f.repo.ID {
		t.Fatalf("asked about the approving run: %+v", f.auth.asked)
	}
}

// A pass verdict that names no activation and machine (a person's run, or a
// verdict from before they were recorded) cannot land.
func TestOnModeRefusesAVerdictWithNoActivation(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeOn)
	a := f.open("a", 50, nil)
	// A later round on the same head, by a run of no activation.
	if _, err := f.db.InsertPRHeadVerdict(f.ctx, core.PRHeadVerdictRecord{
		OrgID: testOrg, ProjectID: testProject, RecordID: a.recordID, HeadSHA: a.head, Round: 2,
		Verdict: core.PRHeadVerdictPass, Rule: core.PRVerdictRuleNoModelLowRisk, RiskTier: "low", ReviewerPrincipal: "test",
	}); err != nil {
		t.Fatal(err)
	}
	if res := f.enqueue(a, nil); res["queued"] != false || res["code"] != mergequeue.RefuseLandNotGranted {
		t.Fatalf("refused: %v", res)
	}
}

// Shadow never lands, so it asks for no grant.
func TestShadowAsksForNoLandGrant(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	f.queue("a", 50, nil)
	f.auth.revoke(testTrigger)
	if rep := f.tick(f.scheduler()); rep.Landing == "" {
		t.Fatalf("shadow records its would-land: %+v", rep)
	}
	if len(f.auth.asked) != 0 {
		t.Fatalf("shadow asked for land: %+v", f.auth.asked)
	}
}

// A grant revoked between admission and landing stops the batch before its
// first squash merge: nothing lands, the batch is abandoned with the reason
// in its verdict, and its entries wait again, where admission refuses them.
func TestARevokedLandGrantAbandonsTheBatchBeforeItLands(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeOn)
	a := f.queue("a", 50, nil)
	f.gate.judge = func(mergequeue.GateRequest, []string, int) mergequeue.GateResult {
		f.auth.revoke(testTrigger) // revoked while the batch gates
		return pass()
	}
	s := f.scheduler()
	rep := f.tick(s)
	if len(f.land.lands) != 0 {
		t.Fatalf("nothing lands: %+v", f.land.lands)
	}
	b := f.batch(rep.Landing)
	v := f.verdict(rep.Landing)
	if b.State != core.QueueBatchAbandoned || len(v.LandNotGranted) != 1 || v.LandNotGranted[0].EntryID != a.entryID ||
		!strings.Contains(v.LandNotGranted[0].Reason, "revoked") {
		t.Fatalf("abandoned with the refusal recorded: %+v %+v", b, v)
	}
	if e := f.entry(a); e.State != core.QueueEntryWaiting {
		t.Fatalf("the entry waits again: %+v", e)
	}
	if again := f.tick(s); again.Idle != "nothing admissible" || len(f.land.lands) != 0 {
		t.Fatalf("and is not admitted again: %+v", again)
	}
}

// machineBuilder reports the member that built the candidate, as jobrun's
// Builder does.
type machineBuilder struct{ localBuilder }

func (b machineBuilder) Build(ctx context.Context, repo *core.PipelineRepo, req buildjob.BuildRequest) (buildjob.BuildResult, error) {
	res, err := b.localBuilder.Build(ctx, repo, req)
	res.Machine = "builder-1"
	return res, err
}

// The gate is tried first on the member that built the candidate; a retry
// after that member left it unproven asks for no preference.
func TestTheGatePrefersTheMemberThatBuilt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	f.queue("a", 50, nil)
	f.gate.judge = func(_ mergequeue.GateRequest, _ []string, prior int) mergequeue.GateResult {
		if prior == 0 {
			return mergequeue.GateResult{Class: mergequeue.GateUnproven, Machine: "builder-1", ReasonCode: "lost"}
		}
		return pass()
	}
	s := f.schedulerWith(machineBuilder{f.wd})
	f.tick(s)
	reqs := f.gate.requests()
	if len(reqs) != 2 || reqs[0].PreferMachine != "builder-1" || reqs[1].PreferMachine != "" {
		t.Fatalf("gate requests %+v", reqs)
	}
}
