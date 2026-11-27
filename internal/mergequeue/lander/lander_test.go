// Copyright (c) 2025 Reliant Labs
package lander_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/fakegithub"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
	"github.com/reliant-labs/reliant/internal/mergequeue/lander"
)

func TestLandsThreeInOrderCheckingEveryTree(t *testing.T) {
	f := newFixture(t, core.PipelineRepoModeOn)
	a, b, c := f.queue("a"), f.queue("b"), f.queue("c")
	batch := f.batch(core.QueueIsolationSysbox, a, b, c)
	trees := f.request(batch).Trees

	res := f.land(batch)
	if res.Outcome != lander.OutcomeLanded || !res.TreeChecked || res.Unenforced {
		t.Fatalf("result = %+v", res)
	}
	main := f.r.Head("main")
	if res.FinalSHA != main || f.r.Tree(main) != trees[2] {
		t.Fatalf("main %s (tree %s), want final %s with tree T_3 %s", main, f.r.Tree(main), res.FinalSHA, trees[2])
	}
	// One squash commit per PR, in order, each on the previous one.
	previous := batch.BaseSHA
	for i, p := range []*testPR{a, b, c} {
		e := f.entry(p)
		if e.State != core.QueueEntryLanded {
			t.Fatalf("#%d is %s", p.number, e.State)
		}
		cm := f.commit(e.LandedSHA)
		if len(cm.Parents) != 1 || cm.Parents[0] != previous || cm.TreeSHA != trees[i] {
			t.Fatalf("#%d landed as %+v; want parent %s and tree %s", p.number, cm, previous, trees[i])
		}
		if !strings.Contains(cm.Message, "Reliant-Batch: "+batch.ID) || !strings.HasPrefix(cm.Message, "Change "+p.branch) {
			t.Fatalf("#%d message %q", p.number, cm.Message)
		}
		previous = e.LandedSHA
		pr, err := f.g.GetPullRequest(f.ctx, f.r.ID(), p.number)
		if err != nil || !pr.Merged {
			t.Fatalf("#%d merged = %v (%v)", p.number, pr.Merged, err)
		}
		if f.r.Head(p.branch) != "" {
			t.Fatalf("#%d's branch was not deleted", p.number)
		}
		if run := f.check(p); run.Conclusion != ghport.ConclusionSuccess || run.Output.Title != "Landed" {
			t.Fatalf("#%d check = %+v", p.number, run)
		}
		if st := f.pipelineState(p); st != core.PRPipelineLanded {
			t.Fatalf("#%d pipeline is %s", p.number, st)
		}
	}
	for i, call := range f.g.CallsTo(fakegithub.MethodMergePullRequest) {
		if call.SHA != []*testPR{a, b, c}[i].head {
			t.Fatalf("merge %d named sha %q", i, call.SHA)
		}
	}
	got := f.reload(batch)
	if got.State != core.QueueBatchLanded || got.LandedSHA != main {
		t.Fatalf("batch %s landed_sha %s", got.State, got.LandedSHA)
	}
	if v := f.verdict(batch); string(v["unenforced"]) != "false" || v["landing"] == nil || v["class"] == nil {
		t.Fatalf("verdict = %s", f.reload(batch).VerdictJSON)
	}
	h, err := f.st.LatestMainHealth(f.ctx, f.repo.ID)
	if err != nil || h == nil || h.SHA != main || h.Verdict != core.MainHealthPass || h.Source != core.MainHealthGate {
		t.Fatalf("main health = %+v (%v)", h, err)
	}
	if f.queueState() != core.PipelineQueueRunning {
		t.Fatalf("queue is %s", f.queueState())
	}
}

func TestForeignWriteBetweenMergesPausesAndRecordsAVerifyBatch(t *testing.T) {
	f := newFixture(t, core.PipelineRepoModeOn)
	a, b, c := f.queue("a"), f.queue("b"), f.queue("c")
	batch := f.batch(core.QueueIsolationSysbox, a, b, c)
	// Another writer commits to main just before the second merge, after
	// the lander's own re-read of main: only the post-merge check sees it.
	merges := 0
	f.g.BeforeCall(fakegithub.MethodMergePullRequest, func(fakegithub.Call) {
		if merges++; merges == 2 {
			f.r.Commit("main", "foreign", map[string]string{"foreign.txt": "x\n"})
		}
	})

	res := f.land(batch)
	if res.Outcome != lander.OutcomePaused || res.Integrity == nil || res.Integrity.Cause != "parent_mismatch" || res.Integrity.Number != b.number {
		t.Fatalf("result = %+v integrity %+v", res, res.Integrity)
	}
	if f.queueState() != core.PipelineQueuePaused {
		t.Fatalf("queue is %s", f.queueState())
	}
	if e := f.entry(a); e.State != core.QueueEntryLanded {
		t.Fatalf("a is %s", e.State)
	}
	if e := f.entry(b); e.State != core.QueueEntryLanded {
		t.Fatalf("b merged on GitHub, so it is recorded landed; it is %s", e.State)
	}
	if e := f.entry(c); e.State != core.QueueEntryWaiting {
		t.Fatalf("c is %s, want waiting", e.State)
	}
	if got := f.reload(batch); got.State != core.QueueBatchAbandoned {
		t.Fatalf("batch is %s", got.State)
	}
	v := f.verdict(batch)
	if !strings.Contains(string(v["integrity"]), "parent_mismatch") {
		t.Fatalf("verdict integrity = %s", v["integrity"])
	}
	verify := f.batches(core.QueueBatchVerify)
	if len(verify) != 1 || verify[0].BaseSHA != f.r.Head("main") || verify[0].ParentBatchID != batch.ID || res.VerifyBatchID != verify[0].ID {
		t.Fatalf("verify batches = %+v", verify)
	}
	if len(f.g.CallsTo(fakegithub.MethodMergePullRequest)) != 2 {
		t.Fatal("landing must stop at the mismatch")
	}
	// A paused queue lands nothing more.
	if _, err := f.lander().Land(f.ctx, f.request(f.batch(core.QueueIsolationSysbox, f.queue("d")))); !errors.Is(err, lander.ErrNotLandable) {
		t.Fatalf("land while paused: %v", err)
	}
}

func TestForeignWriteBeforeTheFirstMergeAbandonsQuietly(t *testing.T) {
	f := newFixture(t, core.PipelineRepoModeOn)
	a, b := f.queue("a"), f.queue("b")
	batch := f.batch(core.QueueIsolationSysbox, a, b)
	f.r.Commit("main", "foreign", map[string]string{"foreign.txt": "x\n"})

	res := f.land(batch)
	if res.Outcome != lander.OutcomeStaleBase {
		t.Fatalf("result = %+v", res)
	}
	if f.queueState() != core.PipelineQueueRunning || len(f.batches(core.QueueBatchVerify)) != 0 {
		t.Fatal("nothing of the batch reached main: no pause, no verify")
	}
	for _, p := range []*testPR{a, b} {
		if e := f.entry(p); e.State != core.QueueEntryWaiting {
			t.Fatalf("#%d is %s", p.number, e.State)
		}
	}
	if len(f.g.CallsTo(fakegithub.MethodMergePullRequest)) != 0 {
		t.Fatal("nothing may merge onto a main the batch was not gated on")
	}
}

func TestSquashTreeDiffersPausesAndARedVerifyRevertsNewestFirst(t *testing.T) {
	f := newFixture(t, core.PipelineRepoModeOn)
	ver := &verifier{class: lander.VerifyFail}
	f.ver = ver
	a, b := f.queue("a"), f.queue("b")
	batch := f.batch(core.QueueIsolationSysbox, a, b)
	baseTree := f.r.Tree(batch.BaseSHA)
	f.r.OverrideSquash(func(c fakegithub.SquashCall) string {
		if c.PR == b.number {
			return f.r.TreeWithFile(c.Tree, "surprise.txt", "github merged differently\n")
		}
		return c.Tree
	})

	res := f.land(batch)
	if res.Outcome != lander.OutcomePaused || res.Integrity.Cause != "tree_mismatch" {
		t.Fatalf("result = %+v integrity %+v", res, res.Integrity)
	}
	if len(ver.got) != 1 || ver.got[0] != f.entry(b).LandedSHA {
		t.Fatalf("verify gated %v, want main after b (%s)", ver.got, f.entry(b).LandedSHA)
	}
	verify := f.batches(core.QueueBatchVerify)
	if len(verify) != 1 || verify[0].State != core.QueueBatchFailed {
		t.Fatalf("verify = %+v", verify)
	}
	// b reverted, then a: main is back to the tree the batch was built on.
	if len(res.Reverted) != 2 || f.r.Head("main") != res.Reverted[1] || f.r.Tree("main") != baseTree {
		t.Fatalf("reverted %v; main %s tree %s, want base tree %s", res.Reverted, f.r.Head("main"), f.r.Tree("main"), baseTree)
	}
	first := f.commit(res.Reverted[0])
	if first.Parents[0] != f.entry(b).LandedSHA || first.TreeSHA != f.commit(f.entry(a).LandedSHA).TreeSHA {
		t.Fatalf("the first revert %+v must restore the tree a left", first)
	}
	for _, p := range []*testPR{a, b} {
		if st := f.pipelineState(p); st != core.PRPipelineNeedsPerson {
			t.Fatalf("#%d pipeline is %s", p.number, st)
		}
		if len(f.r.Comments(p.number)) != 1 {
			t.Fatalf("#%d has %d comments, want the one needs-person comment", p.number, len(f.r.Comments(p.number)))
		}
	}
	if f.queueState() != core.PipelineQueueRunning {
		t.Fatalf("main is back on a tested tree, so the queue resumes; it is %s", f.queueState())
	}
}

func TestGreenVerifyResumesWithoutReverting(t *testing.T) {
	f := newFixture(t, core.PipelineRepoModeOn)
	f.ver = &verifier{class: lander.VerifyPass}
	a, b := f.queue("a"), f.queue("b")
	batch := f.batch(core.QueueIsolationSysbox, a, b)
	f.r.DivergeNextSquash("surprise.txt", "x\n")

	res := f.land(batch)
	if res.Outcome != lander.OutcomePaused || res.Integrity.Cause != "tree_mismatch" || res.Integrity.Number != a.number {
		t.Fatalf("result = %+v integrity %+v", res, res.Integrity)
	}
	if len(res.Reverted) != 0 || f.queueState() != core.PipelineQueueRunning {
		t.Fatalf("reverted %v, queue %s", res.Reverted, f.queueState())
	}
	if e := f.entry(b); e.State != core.QueueEntryWaiting {
		t.Fatalf("b is %s", e.State)
	}
	if v := f.batches(core.QueueBatchVerify); len(v) != 1 || v[0].State != core.QueueBatchPassed || v[0].IsolationBackend != core.QueueIsolationSysbox {
		t.Fatalf("verify = %+v", v)
	}
}

func TestPRClosedMidLandingIsEjectedAndPauses(t *testing.T) {
	f := newFixture(t, core.PipelineRepoModeOn)
	a, b, c := f.queue("a"), f.queue("b"), f.queue("c")
	batch := f.batch(core.QueueIsolationSysbox, a, b, c)
	merges := 0
	f.g.BeforeCall(fakegithub.MethodMergePullRequest, func(fakegithub.Call) {
		if merges++; merges == 2 {
			f.r.ClosePR(b.number)
		}
	})

	res := f.land(batch)
	if res.Outcome != lander.OutcomePaused || res.Integrity.Cause != "not_mergeable_closed" {
		t.Fatalf("result = %+v integrity %+v", res, res.Integrity)
	}
	if e := f.entry(b); e.State != core.QueueEntryEjected || e.EjectReason != core.QueueEjectClosed {
		t.Fatalf("b = %+v", e)
	}
	if e := f.entry(c); e.State != core.QueueEntryWaiting {
		t.Fatalf("c is %s", e.State)
	}
	if run := f.check(b); run.Conclusion != ghport.ConclusionFailure {
		t.Fatalf("b's check = %+v", run)
	}
	comments := f.r.Comments(b.number)
	if len(comments) != 1 || !strings.Contains(comments[0].Body, "closed") {
		t.Fatalf("b's comments = %+v", comments)
	}
	if f.queueState() != core.PipelineQueuePaused || len(f.batches(core.QueueBatchVerify)) != 1 {
		t.Fatalf("queue %s, verify batches %d", f.queueState(), len(f.batches(core.QueueBatchVerify)))
	}
	row, _ := f.st.GetPRPipeline(f.ctx, b.recordID)
	if row.NextAction != "decide" || row.NextActionAt == nil {
		t.Fatalf("b's pipeline was not woken: %+v", row)
	}
}

func TestFirstPRClosedEjectsWithoutPausing(t *testing.T) {
	f := newFixture(t, core.PipelineRepoModeOn)
	a, b := f.queue("a"), f.queue("b")
	batch := f.batch(core.QueueIsolationSysbox, a, b)
	f.r.ClosePR(a.number)

	res := f.land(batch)
	if res.Outcome != lander.OutcomeEjected {
		t.Fatalf("result = %+v", res)
	}
	if e := f.entry(a); e.State != core.QueueEntryEjected || e.EjectReason != core.QueueEjectClosed {
		t.Fatalf("a = %+v", e)
	}
	if e := f.entry(b); e.State != core.QueueEntryWaiting {
		t.Fatalf("b is %s", e.State)
	}
	if f.queueState() != core.PipelineQueueRunning || f.r.Head("main") != batch.BaseSHA {
		t.Fatal("nothing landed: main unchanged and the queue running")
	}
}

func TestRateLimitedAndLostMergesRetryIdempotently(t *testing.T) {
	f := newFixture(t, core.PipelineRepoModeOn)
	a, b := f.queue("a"), f.queue("b")
	batch := f.batch(core.QueueIsolationSysbox, a, b)
	f.g.Inject(fakegithub.Fault{Method: fakegithub.MethodMergePullRequest, Err: fakegithub.RateLimit(time.Now().Add(2 * time.Second))})
	// The second merge happens but its response is lost: the retry reads it
	// as already merged and must not merge twice.
	f.g.Inject(fakegithub.Fault{Method: fakegithub.MethodMergePullRequest, Err: fakegithub.ServerError(502), AfterEffect: true})

	res := f.land(batch)
	if res.Outcome != lander.OutcomeLanded {
		t.Fatalf("result = %+v integrity %+v", res, res.Integrity)
	}
	if len(f.waits) != 2 || f.waits[0] <= 0 {
		t.Fatalf("waits = %v", f.waits)
	}
	// main is base + exactly two squash commits.
	if c := f.commit(f.r.Head("main")); c.Parents[0] != f.entry(a).LandedSHA || f.commit(c.Parents[0]).Parents[0] != batch.BaseSHA {
		t.Fatalf("main's history is not base, a, b")
	}
	// A second Land of the same (now landed) batch writes nothing.
	calls := len(f.g.CallsTo(fakegithub.MethodMergePullRequest))
	if _, err := f.lander().Land(f.ctx, f.request(batch)); !errors.Is(err, lander.ErrNotLandable) {
		t.Fatalf("relanding a landed batch: %v", err)
	}
	if len(f.g.CallsTo(fakegithub.MethodMergePullRequest)) != calls {
		t.Fatal("a landed batch merged again")
	}
}

func TestAlreadyMergedPRIsTreatedAsDone(t *testing.T) {
	f := newFixture(t, core.PipelineRepoModeOn)
	a, b := f.queue("a"), f.queue("b")
	batch := f.batch(core.QueueIsolationSysbox, a, b)
	// A landing that stopped after GitHub merged a but before it recorded
	// it: main is at a's squash commit, a's entry still batched.
	if _, err := f.g.MergePullRequest(f.ctx, f.r.ID(), a.number, ghport.MergeRequest{Method: ghport.MergeSquash, ExpectedHeadSHA: a.head}); err != nil {
		t.Fatal(err)
	}

	res := f.land(batch)
	if res.Outcome != lander.OutcomeLanded {
		t.Fatalf("result = %+v integrity %+v", res, res.Integrity)
	}
	if got := len(f.g.CallsTo(fakegithub.MethodMergePullRequest)); got != 2 {
		t.Fatalf("%d merge calls, want a's (the test's) and b's", got)
	}
	if e := f.entry(a); e.State != core.QueueEntryLanded {
		t.Fatalf("a is %s", e.State)
	}
	// A retried merge call that GitHub answers "already merged" at the same
	// head is done too.
	f2 := newFixture(t, core.PipelineRepoModeOn)
	c := f2.queue("c")
	b2 := f2.batch(core.QueueIsolationSysbox, c)
	f2.g.Inject(fakegithub.Fault{Method: fakegithub.MethodMergePullRequest, Err: fakegithub.ServerError(500), AfterEffect: true})
	if res := f2.land(b2); res.Outcome != lander.OutcomeLanded {
		t.Fatalf("result = %+v", res)
	}
}

func TestUnenforcedFallbackMarksTheBatch(t *testing.T) {
	f := newFixture(t, core.PipelineRepoModeOn)
	f.id = lander.Identity{Unenforced: true, Login: "reliant-labs[bot]"}
	batch := f.batch(core.QueueIsolationSysbox, f.queue("a"))

	res := f.land(batch)
	if !res.Unenforced || res.Outcome != lander.OutcomeLanded {
		t.Fatalf("result = %+v", res)
	}
	v := f.verdict(batch)
	if string(v["unenforced"]) != "true" {
		t.Fatalf("verdict = %s", f.reload(batch).VerdictJSON)
	}
	var landing lander.Result
	if err := json.Unmarshal(v["landing"], &landing); err != nil || landing.Identity != "reliant-labs[bot]" {
		t.Fatalf("landing = %s (%v)", v["landing"], err)
	}
}

func TestRefusesShadowAndOff(t *testing.T) {
	for _, mode := range []core.PipelineRepoMode{core.PipelineRepoModeShadow, core.PipelineRepoModeOff} {
		f := newFixture(t, mode)
		batch := f.batch(core.QueueIsolationSysbox, f.queue("a"))
		if _, err := f.lander().Land(f.ctx, f.request(batch)); !errors.Is(err, lander.ErrNotOn) {
			t.Fatalf("mode %s: %v", mode, err)
		}
		if len(f.g.Calls()) != 0 {
			t.Fatalf("mode %s called GitHub", mode)
		}
	}
}

func TestRefusesBatchesGatedOutsideAnIsolationBoundary(t *testing.T) {
	for _, iso := range []core.QueueIsolationBackend{core.QueueIsolationRunc, core.QueueIsolationChecks} {
		f := newFixture(t, core.PipelineRepoModeOn)
		batch := f.batch(iso, f.queue("a"))
		if _, err := f.lander().Land(f.ctx, f.request(batch)); !errors.Is(err, lander.ErrNotLandable) {
			t.Fatalf("isolation %s: %v", iso, err)
		}
		if len(f.g.Calls()) != 0 {
			t.Fatalf("isolation %s called GitHub", iso)
		}
	}
}
