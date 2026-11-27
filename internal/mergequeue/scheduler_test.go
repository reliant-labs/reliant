// Copyright (c) 2025 Reliant Labs
package mergequeue_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue"
)

func TestShadowCleanBatchRecordsWouldLand(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	main := f.r.Head("main")
	a := f.queue("a", 50, nil)
	b := f.queue("b", 50, nil)
	s := f.scheduler()

	rep := f.tick(s)
	if rep.Landing == "" || len(rep.Batches) != 1 || rep.Landing != rep.Batches[0] {
		t.Fatalf("report: %+v", rep)
	}
	batch := f.batch(rep.Landing)
	v := mergequeue.ParseVerdict(batch.VerdictJSON)
	if batch.State != core.QueueBatchPassed || batch.Kind != core.QueueBatchNormal || batch.BaseSHA != main ||
		!v.Shadow || v.MainSHA != main || !v.Landable || batch.IsolationBackend != core.QueueIsolationSysbox {
		t.Fatalf("batch: %+v verdict %+v", batch, v)
	}
	if got := f.branches(v.WouldLand); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("would land %v", got)
	}
	if v.WouldLand[0].Head != a.head || v.WouldLand[1].Head != b.head || v.WouldLand[1].Tree != f.r.Tree(batch.CandidateSHA) {
		t.Fatalf("would-land entries: %+v", v.WouldLand)
	}
	if got := f.gate.requests(); len(got) != 1 || !got[0].Shadow || got[0].CandidateRef != "reliant-queue/"+batch.ID || got[0].Attempt != 1 ||
		got[0].BaseSHA != main || got[0].CandidateSHA != batch.CandidateSHA {
		t.Fatalf("gate requests: %+v", got)
	}
	// Shadow lands nothing: main and the PRs are untouched, the entries
	// stay in the batch, and the would-be main's health is recorded.
	if f.r.Head("main") != main || len(f.land.lands) != 0 {
		t.Fatalf("shadow moved main or landed: %s %d", f.r.Head("main"), len(f.land.lands))
	}
	if e := f.entry(a); e.State != core.QueueEntryBatched || e.BatchID != batch.ID {
		t.Fatalf("entry a: %+v", e)
	}
	h, err := f.db.LatestMainHealth(f.ctx, f.repo.ID)
	if err != nil || h == nil || h.SHA != batch.CandidateSHA || h.Source != core.MainHealthGate || h.Verdict != core.MainHealthPass || h.BatchID != batch.ID {
		t.Fatalf("main health: %+v %v", h, err)
	}
	// Nothing is left to pick.
	if rep := f.tick(s); rep.Idle == "" {
		t.Fatalf("second tick: %+v", rep)
	}
}

func TestShadowAdvancesItsSimulatedBase(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	s := f.scheduler()
	f.queue("a", 50, nil)
	first := f.batch(f.tick(s).Landing)

	f.queue("b", 50, nil)
	second := f.batch(f.tick(s).Landing)
	if second.BaseSHA != first.CandidateSHA || !mergequeue.ParseVerdict(second.VerdictJSON).SimulatedBase {
		t.Fatalf("the next batch builds on the would-be main %s, got base %s", first.CandidateSHA, second.BaseSHA)
	}
	if got := f.gate.gated(); !slices.Equal(got[1], []string{"a", "b"}) {
		t.Fatalf("the second candidate holds the first's PRs too: %v", got)
	}

	// The real main moves (the old pipeline still lands in shadow): the
	// simulated chain starts again from it.
	moved := f.r.Commit("main", "landed elsewhere", map[string]string{"other.txt": "x\n"})
	f.queue("c", 50, nil)
	third := f.batch(f.tick(s).Landing)
	if third.BaseSHA != moved || mergequeue.ParseVerdict(third.VerdictJSON).SimulatedBase {
		t.Fatalf("after main moved the base is main %s, got %s", moved, third.BaseSHA)
	}
}

func TestUrgentEntryGoesAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	f.queue("a", 50, nil)
	f.queue("urgent", 95, nil)
	f.queue("b", 50, nil)
	s := f.scheduler()

	v := f.verdict(f.tick(s).Landing)
	if got := f.branches(v.WouldLand); !slices.Equal(got, []string{"urgent"}) {
		t.Fatalf("urgent batch %v", got)
	}
	v = f.verdict(f.tick(s).Landing)
	if got := f.branches(v.WouldLand); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("then the rest: %v", got)
	}
}

func TestBatchMaxAndAdmission(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	f.setConfig(`{"version": 1, "gate": "task gate", "queue": {"batch_max": 2}}`)
	f.queue("a", 50, nil)
	held := f.queue("b", 50, nil)
	f.queue("c", 50, nil)
	f.queue("d", 50, nil)
	if _, err := f.raw.ExecContext(f.ctx, `INSERT INTO pipeline_decisions (id, org_id, project_id, subject_kind, subject_id,
		kind, state, reason, decided_by_user_id, decided_via, decided_at) VALUES ('hold-b', $1, $2, 'pr', $3, 'hold', 'approved', 'wait', $4, 'web', now())`,
		testOrg, testProject, held.recordID, testUser); err != nil {
		t.Fatal(err)
	}
	s := f.scheduler()
	v := f.verdict(f.tick(s).Landing)
	if got := f.branches(v.WouldLand); !slices.Equal(got, []string{"a", "c"}) {
		t.Fatalf("batch_max 2, held b skipped: %v", got)
	}
	if e := f.entry(held); e.State != core.QueueEntryWaiting {
		t.Fatalf("a held entry waits: %+v", e)
	}
}

func TestConflictEjectsAndWakesThePR(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	a := f.queue("a", 50, map[string]string{"shared.txt": "from a\n"})
	b := f.queue("b", 50, map[string]string{"shared.txt": "from b\n"})
	c := f.open("c", 50, map[string]string{"shared.txt": "from c\n"})
	// c conflicts with main itself.
	f.r.Commit("main", "moved", map[string]string{"shared.txt": "main moved\n"})
	e, _, err := f.db.EnqueueQueueEntry(f.ctx, core.QueueEntryNew{OrgID: testOrg, ProjectID: testProject, RepoID: f.repo.ID, RecordID: c.recordID, HeadSHA: c.head, Priority: 50})
	if err != nil {
		t.Fatal(err)
	}
	c.entryID = e.ID
	s := f.scheduler()

	rep := f.tick(s)
	// All three conflict with the moved main.
	if len(rep.Ejected) != 3 || rep.Landing != "" {
		t.Fatalf("report %+v", rep)
	}
	for _, p := range []*testPR{a, b, c} {
		if e := f.entry(p); e.State != core.QueueEntryEjected || e.EjectReason != core.QueueEjectConflict {
			t.Fatalf("entry %s: %+v", p.branch, e)
		}
		if row := f.pipeline(p); row.NextAction != "decide" || row.NextActionAt == nil {
			t.Fatalf("pr %s not woken: %+v", p.branch, row)
		}
	}
	v := f.verdict(rep.Batches[0])
	if f.batch(rep.Batches[0]).State != core.QueueBatchAbandoned || len(v.Ejected) != 3 || v.Ejected[0].Paths[0] != "shared.txt" {
		t.Fatalf("evidence: %+v", v)
	}
}

func TestBatchConflictWaitsForTheNextBatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	f.queue("a", 50, map[string]string{"shared.txt": "from a\n"})
	b := f.queue("b", 50, map[string]string{"shared.txt": "from b\n"})
	s := f.scheduler()

	rep := f.tick(s)
	v := f.verdict(rep.Landing)
	if got := f.branches(v.WouldLand); !slices.Equal(got, []string{"a"}) || !slices.Equal(v.Requeued, []string{b.entryID}) {
		t.Fatalf("would land %v, requeued %v", got, v.Requeued)
	}
	if e := f.entry(b); e.State != core.QueueEntryWaiting {
		t.Fatalf("b waits: %+v", e)
	}
}

func TestStaleEntriesAreEjected(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	a := f.queue("a", 50, nil)
	b := f.queue("b", 50, nil)
	if _, err := f.raw.ExecContext(f.ctx, `UPDATE pr_records SET head_sha = 'f00d' WHERE id = $1`, a.recordID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.raw.ExecContext(f.ctx, `UPDATE pr_records SET state = 'closed' WHERE id = $1`, b.recordID); err != nil {
		t.Fatal(err)
	}
	rep := f.tick(f.scheduler())
	if rep.Idle == "" || len(rep.Ejected) != 2 {
		t.Fatalf("report %+v", rep)
	}
	if e := f.entry(a); e.EjectReason != core.QueueEjectHeadMoved {
		t.Fatalf("a: %+v", e)
	}
	if e := f.entry(b); e.EjectReason != core.QueueEjectClosed {
		t.Fatalf("b: %+v", e)
	}
}

func TestOnModeLandsOnlyLandableResults(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeOn)
	a := f.queue("a", 50, nil)
	s := f.scheduler()

	rep := f.tick(s)
	if len(f.land.lands) != 1 || f.land.lands[0].Batch.ID != rep.Landing {
		t.Fatalf("lander not handed the batch: %+v %+v", rep, f.land.lands)
	}
	req := f.land.lands[0]
	batch := f.batch(rep.Landing)
	if len(req.Entries) != 1 || req.Entries[0].ID != a.entryID || req.Heads[a.entryID] != a.head ||
		len(req.Trees) != 1 || req.Trees[0] != f.r.Tree(batch.CandidateSHA) {
		t.Fatalf("land request: %+v", req)
	}
	if v := f.verdict(rep.Landing); v.Shadow || len(v.WouldLand) != 0 {
		t.Fatalf("an on batch is no would-land record: %+v", v)
	}
}

func TestOnModeNeverLandsAGateOutsideIsolation(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeOn)
	a := f.queue("a", 50, nil)
	f.gate.judge = func(mergequeue.GateRequest, []string, int) mergequeue.GateResult {
		r := pass()
		r.Isolation = "runc"
		return r
	}
	rep := f.tick(f.scheduler())
	if len(f.land.lands) != 0 || rep.Landing != "" || !rep.Degraded {
		t.Fatalf("a runc pass must not land: %+v %+v", rep, f.land.lands)
	}
	if v := f.verdict(rep.Batches[0]); v.Class != mergequeue.GateUnproven || v.ReasonCode != "not_isolated" || v.Landable {
		t.Fatalf("verdict %+v", v)
	}
	if e := f.entry(a); e.State != core.QueueEntryWaiting {
		t.Fatalf("entry waits again: %+v", e)
	}
	if st := f.queueState(); st != core.PipelineQueueDegraded {
		t.Fatalf("queue %s", st)
	}
}

func TestShadowRecordsARuncPassAsNotLandable(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	f.queue("a", 50, nil)
	f.gate.judge = func(mergequeue.GateRequest, []string, int) mergequeue.GateResult {
		r := pass()
		r.Isolation = "runc"
		return r
	}
	rep := f.tick(f.scheduler())
	v := f.verdict(rep.Landing)
	b := f.batch(rep.Landing)
	if b.State != core.QueueBatchPassed || v.Landable || v.Isolation != "runc" || len(v.WouldLand) != 1 {
		t.Fatalf("shadow records a runc pass, never landable: %+v %+v", b, v)
	}
}

func TestLeaderExcludesASecondWorker(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	f.queue("a", 50, nil)
	f.gate.block = make(chan struct{})
	f.gate.started = make(chan struct{}, 4)
	one, two := f.scheduler(), f.scheduler()

	done := make(chan error, 1)
	go func() {
		_, err := one.Tick(f.ctx, f.repo.ID)
		done <- err
	}()
	select {
	case <-f.gate.started:
	case <-time.After(30 * time.Second):
		t.Fatal("the first worker never gated")
	}
	if _, err := two.Tick(f.ctx, f.repo.ID); !errors.Is(err, mergequeue.ErrNotLeader) {
		t.Fatalf("the second worker must not lead: %v", err)
	}
	close(f.gate.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// The lead was released: the second worker leads now.
	if rep, err := two.Tick(f.ctx, f.repo.ID); err != nil || rep.Idle == "" {
		t.Fatalf("second worker after release: %+v %v", rep, err)
	}
}

func TestRecoverAbandonsWhatAStoppedLeaderLeft(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeShadow)
	a := f.queue("a", 50, nil)
	f.gate.block = make(chan struct{})
	f.gate.started = make(chan struct{}, 4)
	s := f.scheduler()

	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan error, 1)
	go func() {
		_, err := s.Tick(ctx, f.repo.ID)
		done <- err
	}()
	<-f.gate.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("a stopped tick: %v", err)
	}
	left := f.entry(a)
	if left.State != core.QueueEntryBatched || f.batch(left.BatchID).State != core.QueueBatchGating {
		t.Fatalf("the stopped leader left the batch gating: %+v", left)
	}

	f.gate.block, f.gate.started = nil, nil
	rep := f.tick(s)
	if f.batch(left.BatchID).State != core.QueueBatchAbandoned || rep.Landing == "" || rep.Landing == left.BatchID {
		t.Fatalf("recover: %+v (old batch %s)", rep, f.batch(left.BatchID).State)
	}
	if !strings.Contains(f.verdict(left.BatchID).Note, "leader stopped") {
		t.Fatalf("note: %q", f.verdict(left.BatchID).Note)
	}
}

func TestModeOffDoesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t, core.PipelineRepoModeOff)
	f.queue("a", 50, nil)
	if rep := f.tick(f.scheduler()); rep.Idle != "mode off" || len(f.gate.gated()) != 0 {
		t.Fatalf("report %+v", rep)
	}
}
