package store_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

func entryIDs(es []*core.QueueEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}

func TestPipelineDecisionsInForce(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	_, recs := newPipelineRecords(t, env, "rid-d", 2)
	now := time.Now().UTC()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	rec := recs[0]

	pipelineDecision(t, env, "d-proto-a", core.PipelineDecisionSubjectPRHead, rec, "head-a", core.PipelineDecisionApproveBreakingProto, core.PipelineDecisionApproved, &future)
	pipelineDecision(t, env, "d-proto-b", core.PipelineDecisionSubjectPRHead, rec, "head-b", core.PipelineDecisionApproveBreakingProto, core.PipelineDecisionApproved, nil)
	pipelineDecision(t, env, "d-expired", core.PipelineDecisionSubjectPRHead, rec, "head-a", core.PipelineDecisionApproveBreaksRollback, core.PipelineDecisionApproved, &past)
	pipelineDecision(t, env, "d-revoked", core.PipelineDecisionSubjectPRHead, rec, "head-a", core.PipelineDecisionApproveProdFix, core.PipelineDecisionRevoked, nil)
	pipelineDecision(t, env, "d-denied", core.PipelineDecisionSubjectPRHead, rec, "head-a", core.PipelineDecisionApproveProdFix, core.PipelineDecisionDenied, nil)
	pipelineDecision(t, env, "d-priority", core.PipelineDecisionSubjectPR, rec, "", core.PipelineDecisionPriority, core.PipelineDecisionApproved, nil)

	ids := func(ds []*core.PipelineDecision) []string {
		out := make([]string, len(ds))
		for i, d := range ds {
			out[i] = d.ID
		}
		slices.Sort(out)
		return out
	}
	atA, err := store.ActivePipelineDecisionsFor(ctx, env.ORM, core.PipelineDecisionSubjectPRHead, rec, "head-a", now)
	if err != nil || !slices.Equal(ids(atA), []string{"d-proto-a"}) {
		t.Fatalf("in force at head-a: %v %v", ids(atA), err)
	}
	if atA[0].DecidedVia != core.PipelineDecidedViaWeb || atA[0].DecidedByUserID != storetest.SeedUserID || atA[0].ExpiresAt == nil {
		t.Fatalf("decision fields: %+v", atA[0])
	}
	// A push voids the head's decisions; a decision about the PR holds at
	// every head.
	if atC, err := store.ActivePipelineDecisionsFor(ctx, env.ORM, core.PipelineDecisionSubjectPRHead, rec, "head-c", now); err != nil || len(atC) != 0 {
		t.Fatalf("another head: %v %v", ids(atC), err)
	}
	if pr, err := store.ActivePipelineDecisionsFor(ctx, env.ORM, core.PipelineDecisionSubjectPR, rec, "head-c", now); err != nil || !slices.Equal(ids(pr), []string{"d-priority"}) {
		t.Fatalf("PR decisions: %v %v", ids(pr), err)
	}

	if h, err := store.ActivePipelineHold(ctx, env.ORM, rec, now); err != nil || h != nil {
		t.Fatalf("not held: %+v %v", h, err)
	}
	pipelineDecision(t, env, "d-hold-old", core.PipelineDecisionSubjectPR, rec, "", core.PipelineDecisionHold, core.PipelineDecisionApproved, &past)
	if h, err := store.ActivePipelineHold(ctx, env.ORM, rec, now); err != nil || h != nil {
		t.Fatalf("an expired hold is not in force: %+v %v", h, err)
	}
	pipelineDecision(t, env, "d-hold", core.PipelineDecisionSubjectPR, rec, "", core.PipelineDecisionHold, core.PipelineDecisionApproved, nil)
	if h, err := store.ActivePipelineHold(ctx, env.ORM, rec, now); err != nil || h == nil || h.ID != "d-hold" {
		t.Fatalf("held: %+v %v", h, err)
	}
	if h, err := store.ActivePipelineHold(ctx, env.ORM, recs[1], now); err != nil || h != nil {
		t.Fatalf("another PR is not held: %+v %v", h, err)
	}
}

func TestQueueEntriesEnqueueAndAdmission(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	repo, recs := newPipelineRecords(t, env, "rid-q", 5)
	now := time.Now().UTC()
	pass := func(i int, head string, v core.PRHeadVerdict) {
		t.Helper()
		if _, err := store.InsertPRHeadVerdict(ctx, env.ORM, core.PRHeadVerdictRecord{
			OrgID: repo.OrgID, ProjectID: repo.ProjectID, RecordID: recs[i], HeadSHA: head, Round: 1,
			Verdict: v, Rule: core.PRVerdictRuleNoModelLowRisk, RiskTier: core.PRRiskLow, ReviewerPrincipal: "server",
		}); err != nil {
			t.Fatal(err)
		}
	}
	enqueue := func(i int, head string, priority int) (*core.QueueEntry, bool, error) {
		return store.EnqueueQueueEntry(ctx, env.ORM, core.QueueEntryNew{
			OrgID: repo.OrgID, ProjectID: repo.ProjectID, RepoID: repo.ID, RecordID: recs[i], HeadSHA: head, Priority: priority,
		})
	}

	e0, created, err := enqueue(0, "head-0", 50)
	if err != nil || !created || e0.State != core.QueueEntryWaiting || e0.BatchID != "" {
		t.Fatalf("enqueue: %+v %v %v", e0, created, err)
	}
	again, created, err := enqueue(0, "head-0", 70)
	if err != nil || created || again.ID != e0.ID || again.Priority != 50 {
		t.Fatalf("a repeat enqueue returns the entry: %+v %v %v", again, created, err)
	}
	if _, _, err := enqueue(0, "head-new", 50); !errors.Is(err, core.ErrQueueEntryOtherHead) {
		t.Fatalf("another head must be ErrQueueEntryOtherHead, got %v", err)
	}

	// Two concurrent enqueues of one PR make one entry.
	var wg sync.WaitGroup
	results := make(chan string, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, _, err := enqueue(1, "head-1", 95)
			if err != nil {
				t.Errorf("concurrent enqueue: %v", err)
				return
			}
			results <- e.ID
		}()
	}
	wg.Wait()
	close(results)
	var got []string
	for id := range results {
		got = append(got, id)
	}
	if len(got) != 2 || got[0] != got[1] {
		t.Fatalf("concurrent enqueues returned %v, want one entry twice", got)
	}

	// recs[0] passed at its head, recs[1] too (and outranks it); recs[2] is
	// held; recs[3]'s latest verdict asks for changes; recs[4]'s recorded
	// head moved past the queued one.
	pass(0, "head-0", core.PRHeadVerdictPass)
	pass(1, "head-1", core.PRHeadVerdictPass)
	pass(2, "head-2", core.PRHeadVerdictPass)
	pass(3, "head-3", core.PRHeadVerdictChanges)
	pass(4, "head-4", core.PRHeadVerdictPass)
	for _, i := range []int{2, 3, 4} {
		if _, _, err := enqueue(i, "head-"+string(rune('0'+i)), 50); err != nil {
			t.Fatal(err)
		}
	}
	pipelineDecision(t, env, "hold-2", core.PipelineDecisionSubjectPR, recs[2], "", core.PipelineDecisionHold, core.PipelineDecisionApproved, nil)
	if _, err := env.SQL.Exec(`UPDATE pr_records SET head_sha = 'head-4b' WHERE id = $1`, recs[4]); err != nil {
		t.Fatal(err)
	}

	adm, err := store.ListAdmissibleQueueEntries(ctx, env.ORM, repo.ID, now, 0)
	if err != nil {
		t.Fatal(err)
	}
	var admRecs []string
	for _, e := range adm {
		admRecs = append(admRecs, e.RecordID)
	}
	if want := []string{recs[1], recs[0]}; !slices.Equal(admRecs, want) {
		t.Fatalf("admissible = %v, want %v (priority first)", admRecs, want)
	}
	if one, err := store.ListAdmissibleQueueEntries(ctx, env.ORM, repo.ID, now, 1); err != nil || len(one) != 1 || one[0].RecordID != recs[1] {
		t.Fatalf("limit 1: %v %v", one, err)
	}

	ej, err := store.EjectQueueEntry(ctx, env.ORM, e0.ID, core.QueueEjectHeadMoved)
	if err != nil || ej.State != core.QueueEntryEjected || ej.EjectReason != core.QueueEjectHeadMoved {
		t.Fatalf("eject: %+v %v", ej, err)
	}
	if _, err := store.EjectQueueEntry(ctx, env.ORM, e0.ID, core.QueueEjectClosed); !errors.Is(err, core.ErrPipelineVersionConflict) {
		t.Fatalf("ejecting twice must conflict, got %v", err)
	}
	// Ejected, the PR may be queued again at its new head.
	if e, created, err := enqueue(0, "head-new", 50); err != nil || !created || e.ID == e0.ID {
		t.Fatalf("re-enqueue after eject: %+v %v %v", e, created, err)
	}
}

func TestQueueBatchLifecycle(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	repo, recs := newPipelineRecords(t, env, "rid-b", 4)
	otherRepo, otherRecs := newPipelineRecords(t, env, "rid-b2", 1)
	var entries []*core.QueueEntry
	for i, rec := range recs {
		e, _, err := store.EnqueueQueueEntry(ctx, env.ORM, core.QueueEntryNew{
			OrgID: repo.OrgID, ProjectID: repo.ProjectID, RepoID: repo.ID, RecordID: rec, HeadSHA: "h", Priority: 50 - i,
		})
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, e)
	}
	foreign, _, err := store.EnqueueQueueEntry(ctx, env.ORM, core.QueueEntryNew{
		OrgID: otherRepo.OrgID, ProjectID: otherRepo.ProjectID, RepoID: otherRepo.ID, RecordID: otherRecs[0], HeadSHA: "h", Priority: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	newBatch := func(kind core.QueueBatchKind, parent string) *core.QueueBatch {
		t.Helper()
		b, err := store.CreateQueueBatch(ctx, env.ORM, core.QueueBatchNew{
			OrgID: repo.OrgID, ProjectID: repo.ProjectID, RepoID: repo.ID, BaseSHA: "base", Kind: kind, ParentBatchID: parent,
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	b := newBatch("", "")
	if b.State != core.QueueBatchBuilding || b.Kind != core.QueueBatchNormal || b.Attempt != 1 || b.FinishedAt != nil {
		t.Fatalf("new batch: %+v", b)
	}
	if got, err := store.GetQueueBatch(ctx, env.ORM, b.ID); err != nil || got.ID != b.ID {
		t.Fatalf("get: %+v %v", got, err)
	}
	if got, err := store.GetQueueBatch(ctx, env.ORM, "none"); err != nil || got != nil {
		t.Fatalf("missing batch: %+v %v", got, err)
	}

	// Attach is all or none: another repository's entry refuses the lot.
	if err := store.AttachQueueEntries(ctx, env.ORM, b.ID, []string{entries[0].ID, foreign.ID}); !errors.Is(err, core.ErrQueueEntryNotAttachable) {
		t.Fatalf("a foreign entry must refuse the attach, got %v", err)
	}
	if es, _ := store.ListQueueBatchEntries(ctx, env.ORM, b.ID); len(es) != 0 {
		t.Fatalf("a refused attach moves nothing: %v", entryIDs(es))
	}
	all := []string{entries[3].ID, entries[1].ID, entries[0].ID, entries[2].ID}
	if err := store.AttachQueueEntries(ctx, env.ORM, b.ID, all); err != nil {
		t.Fatal(err)
	}
	inB, err := store.ListQueueBatchEntries(ctx, env.ORM, b.ID)
	if err != nil || !slices.Equal(entryIDs(inB), entryIDs(entries)) {
		t.Fatalf("batch entries in landing order: %v %v", entryIDs(inB), err)
	}
	for _, e := range inB {
		if e.State != core.QueueEntryBatched || e.BatchID != b.ID {
			t.Fatalf("attached entry: %+v", e)
		}
	}
	// A batched entry cannot join an unrelated batch.
	stray := newBatch(core.QueueBatchNormal, "")
	if err := store.AttachQueueEntries(ctx, env.ORM, stray.ID, []string{entries[0].ID}); !errors.Is(err, core.ErrQueueEntryNotAttachable) {
		t.Fatalf("a batched entry must not join another batch, got %v", err)
	}

	// building → gating, with the candidate; an illegal jump is refused, and
	// a stale from conflicts.
	if _, err := store.SetQueueBatchState(ctx, env.ORM, b.ID, core.QueueBatchBuilding, core.QueueBatchPassed, core.QueueBatchUpdate{}); !errors.Is(err, core.ErrQueueBatchTransition) {
		t.Fatalf("building → passed must be refused, got %v", err)
	}
	gating, err := store.SetQueueBatchState(ctx, env.ORM, b.ID, core.QueueBatchBuilding, core.QueueBatchGating, core.QueueBatchUpdate{
		CandidateSHA: "cand", CandidateRef: "refs/heads/reliant-queue/" + b.ID, ExpectedTreesJSON: []byte(`["t1","t2","t3","t4"]`),
		IsolationBackend: core.QueueIsolationSysbox, GateRunner: core.QueueGateRunnerMachine, GateRunID: "run-g",
		GateMachineID: "m-1", CheckNames: []string{"lint", "test"},
	})
	if err != nil || gating.State != core.QueueBatchGating || gating.CandidateSHA != "cand" || gating.GateRunner != core.QueueGateRunnerMachine ||
		!slices.Equal(gating.CheckNames, []string{"lint", "test"}) || len(gating.ExpectedTreesJSON) == 0 {
		t.Fatalf("gating: %+v %v", gating, err)
	}
	if _, err := store.SetQueueBatchState(ctx, env.ORM, b.ID, core.QueueBatchBuilding, core.QueueBatchGating, core.QueueBatchUpdate{}); !errors.Is(err, core.ErrPipelineVersionConflict) {
		t.Fatalf("a stale from must conflict, got %v", err)
	}
	if err := store.AttachQueueEntries(ctx, env.ORM, b.ID, []string{entries[0].ID}); !errors.Is(err, core.ErrQueueEntryNotAttachable) {
		t.Fatalf("a gating batch takes no entries, got %v", err)
	}

	// unproven → gating again counts an attempt.
	unproven, err := store.SetQueueBatchState(ctx, env.ORM, b.ID, core.QueueBatchGating, core.QueueBatchUnproven, core.QueueBatchUpdate{VerdictJSON: []byte(`{"infra": true}`)})
	if err != nil || unproven.FinishedAt == nil || unproven.CandidateSHA != "cand" {
		t.Fatalf("unproven: %+v %v", unproven, err)
	}
	regate, err := store.SetQueueBatchState(ctx, env.ORM, b.ID, core.QueueBatchUnproven, core.QueueBatchGating, core.QueueBatchUpdate{GateMachineID: "m-2"})
	if err != nil || regate.Attempt != 2 || regate.FinishedAt != nil || regate.GateMachineID != "m-2" {
		t.Fatalf("regate: %+v %v", regate, err)
	}
	if _, err := store.MarkQueueBatchLanded(ctx, env.ORM, b.ID, "main-sha"); !errors.Is(err, core.ErrPipelineVersionConflict) {
		t.Fatalf("a batch lands only once passed, got %v", err)
	}
	if _, err := store.SetQueueBatchState(ctx, env.ORM, b.ID, core.QueueBatchGating, core.QueueBatchLanded, core.QueueBatchUpdate{}); !errors.Is(err, core.ErrQueueBatchTransition) {
		t.Fatalf("gating → landed must be refused, got %v", err)
	}

	// A bisect half takes its entries from the batch it splits.
	failed, err := store.SetQueueBatchState(ctx, env.ORM, b.ID, core.QueueBatchGating, core.QueueBatchFailed, core.QueueBatchUpdate{StagesJSON: []byte(`{"test": "fail"}`)})
	if err != nil || failed.State != core.QueueBatchFailed {
		t.Fatalf("failed: %+v %v", failed, err)
	}
	half := newBatch(core.QueueBatchBisect, b.ID)
	if half.ParentBatchID != b.ID {
		t.Fatalf("bisect half: %+v", half)
	}
	if err := store.AttachQueueEntries(ctx, env.ORM, half.ID, []string{entries[0].ID, entries[1].ID}); err != nil {
		t.Fatalf("a bisect half takes its parent's entries: %v", err)
	}
	if es, _ := store.ListQueueBatchEntries(ctx, env.ORM, b.ID); !slices.Equal(entryIDs(es), []string{entries[2].ID, entries[3].ID}) {
		t.Fatalf("the parent keeps the other half: %v", entryIDs(es))
	}
	if n, err := store.RequeueQueueEntries(ctx, env.ORM, b.ID); err != nil || n != 2 {
		t.Fatalf("requeue the other half: %d %v", n, err)
	}

	// The half passes and lands, entry by entry.
	if _, err := store.SetQueueBatchState(ctx, env.ORM, half.ID, core.QueueBatchBuilding, core.QueueBatchGating, core.QueueBatchUpdate{CandidateSHA: "c2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetQueueBatchState(ctx, env.ORM, half.ID, core.QueueBatchGating, core.QueueBatchPassed, core.QueueBatchUpdate{}); err != nil {
		t.Fatal(err)
	}
	for i, e := range entries[:2] {
		if i == 1 {
			// Review B1 on #3805: one entry still batched, so the batch
			// cannot land yet.
			if _, err := store.MarkQueueBatchLanded(ctx, env.ORM, half.ID, "main-a"); !errors.Is(err, core.ErrPipelineVersionConflict) {
				t.Fatalf("a batch with an entry not landed must not land, got %v", err)
			}
		}
		landed, err := store.MarkQueueEntryLanded(ctx, env.ORM, e.ID, "main-"+string(rune('a'+i)))
		if err != nil || landed.State != core.QueueEntryLanded || landed.LandedAt == nil {
			t.Fatalf("entry landed: %+v %v", landed, err)
		}
	}
	if _, err := store.MarkQueueEntryLanded(ctx, env.ORM, entries[2].ID, "main-x"); !errors.Is(err, core.ErrPipelineVersionConflict) {
		t.Fatalf("a waiting entry cannot land, got %v", err)
	}
	landedBatch, err := store.MarkQueueBatchLanded(ctx, env.ORM, half.ID, "main-b")
	if err != nil || landedBatch.State != core.QueueBatchLanded || landedBatch.LandedSHA != "main-b" {
		t.Fatalf("batch landed: %+v %v", landedBatch, err)
	}

	latest, err := store.LatestQueueBatches(ctx, env.ORM, repo.ID, 10)
	if err != nil || len(latest) != 3 {
		t.Fatalf("latest batches: %v %v", latest, err)
	}
	for i := 1; i < len(latest); i++ {
		if latest[i].StartedAt.After(latest[i-1].StartedAt) {
			t.Fatalf("latest batches are newest first: %v then %v", latest[i-1].StartedAt, latest[i].StartedAt)
		}
	}

	// main_health: inserted only, the newest per repository.
	if h, err := store.LatestMainHealth(ctx, env.ORM, repo.ID); err != nil || h != nil {
		t.Fatalf("no health yet: %+v %v", h, err)
	}
	old := time.Now().UTC().Add(-time.Hour)
	if _, err := store.InsertMainHealth(ctx, env.ORM, core.MainHealthRecord{OrgID: repo.OrgID, ProjectID: repo.ProjectID, RepoID: repo.ID,
		SHA: "main-a", Source: core.MainHealthNightly, Verdict: core.MainHealthFail, Signature: "sig", RunID: "nightly-1", ObservedAt: old}); err != nil {
		t.Fatal(err)
	}
	h, err := store.InsertMainHealth(ctx, env.ORM, core.MainHealthRecord{OrgID: repo.OrgID, ProjectID: repo.ProjectID, RepoID: repo.ID,
		SHA: "main-b", Source: core.MainHealthGate, Verdict: core.MainHealthPass, BatchID: half.ID})
	if err != nil || h.ObservedAt.IsZero() || h.BatchID != half.ID {
		t.Fatalf("insert health: %+v %v", h, err)
	}
	if latest, err := store.LatestMainHealth(ctx, env.ORM, repo.ID); err != nil || latest.ID != h.ID || latest.Verdict != core.MainHealthPass {
		t.Fatalf("latest health: %+v %v", latest, err)
	}
	if other, err := store.LatestMainHealth(ctx, env.ORM, otherRepo.ID); err != nil || other != nil {
		t.Fatalf("another repository has none: %+v %v", other, err)
	}
}
