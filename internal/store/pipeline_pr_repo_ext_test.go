package store_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

func newPRPipeline(repo *core.PipelineRepo, recordID, head string, due *time.Time) core.PRPipelineNew {
	p := core.PRPipelineNew{RecordID: recordID, OrgID: repo.OrgID, ProjectID: repo.ProjectID, RepoID: repo.ID, StateHeadSHA: head}
	if due != nil {
		p.NextAction, p.NextActionAt = "review", due
	}
	return p
}

func TestPRPipelineEnsureResetTransition(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	repo, recs := newPipelineRecords(t, env, "rid-pp", 2)

	if p, err := store.GetPRPipeline(ctx, env.ORM, recs[0]); err != nil || p != nil {
		t.Fatalf("no row yet: %+v %v", p, err)
	}
	p, created, err := store.EnsurePRPipeline(ctx, env.ORM, newPRPipeline(repo, recs[0], "head-0", nil))
	if err != nil || !created {
		t.Fatalf("ensure: %+v %v %v", p, created, err)
	}
	if p.State != core.PRPipelineAwaitingReview || p.Priority != 50 || p.PrioritySource != core.PRPriorityDefault ||
		p.Version != 0 || p.NextActionAt != nil || p.StateHeadSHA != "head-0" {
		t.Fatalf("new row: %+v", p)
	}
	again, created, err := store.EnsurePRPipeline(ctx, env.ORM, newPRPipeline(repo, recs[0], "head-other", nil))
	if err != nil || created || again.Version != 0 || again.StateHeadSHA != "head-0" {
		t.Fatalf("a second ensure returns the row unchanged: %+v %v %v", again, created, err)
	}

	// A transition writes the caller's modified copy at the version it read.
	to := *p
	to.State, to.ReviewRound, to.OwnerRunID, to.FixSignature = core.PRPipelineChangesRequested, 1, "run-1", "sig-1"
	to.FixAttempts = 2
	moved, err := store.TransitionPRPipeline(ctx, env.ORM, p.RecordID, p.Version, to)
	if err != nil || moved.State != core.PRPipelineChangesRequested || moved.Version != 1 || moved.OwnerRunID != "run-1" ||
		moved.ReviewRound != 1 || moved.FixAttempts != 2 || moved.FixSignature != "sig-1" {
		t.Fatalf("transition: %+v %v", moved, err)
	}
	if _, err := store.TransitionPRPipeline(ctx, env.ORM, p.RecordID, p.Version, to); !errors.Is(err, core.ErrPipelineVersionConflict) {
		t.Fatalf("a superseded writer must conflict, got %v", err)
	}
	if _, err := store.TransitionPRPipeline(ctx, env.ORM, "rec-none", 0, to); !errors.Is(err, core.ErrPipelineVersionConflict) {
		t.Fatalf("a missing row conflicts, got %v", err)
	}
	bad := *moved
	bad.State = "merged"
	if _, err := store.TransitionPRPipeline(ctx, env.ORM, p.RecordID, moved.Version, bad); err == nil {
		t.Fatal("an unknown state is refused")
	}

	since := time.Now().UTC().Truncate(time.Second)
	np := *moved
	np.State, np.NeedsPersonReason, np.NeedsPersonSince, np.OwnerRunID = core.PRPipelineNeedsPerson, "3 fixes failed", &since, ""
	np2, err := store.TransitionPRPipeline(ctx, env.ORM, p.RecordID, moved.Version, np)
	if err != nil || np2.State != core.PRPipelineNeedsPerson || np2.NeedsPersonSince == nil || np2.OwnerRunID != "" {
		t.Fatalf("needs person: %+v %v", np2, err)
	}

	// A new head starts the PR over and keeps its priority.
	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	reset, err := store.ResetPRPipelineForHead(ctx, env.ORM, p.RecordID, np2.Version, "head-1", core.PRPipelineNext{Action: "review", At: &due})
	if err != nil {
		t.Fatal(err)
	}
	if reset.State != core.PRPipelineAwaitingReview || reset.StateHeadSHA != "head-1" || reset.ReviewRound != 0 ||
		reset.FixAttempts != 0 || reset.FixSignature != "" || reset.NeedsPersonSince != nil || reset.NeedsPersonReason != "" ||
		reset.NextAction != "review" || reset.NextActionAt == nil || !reset.NextActionAt.Equal(due) || reset.Version != np2.Version+1 {
		t.Fatalf("reset: %+v", reset)
	}
	if _, err := store.ResetPRPipelineForHead(ctx, env.ORM, p.RecordID, np2.Version, "head-2", core.PRPipelineNext{}); !errors.Is(err, core.ErrPipelineVersionConflict) {
		t.Fatalf("a stale reset must conflict, got %v", err)
	}

	// Two concurrent ensures of one new PR make one row.
	var wg sync.WaitGroup
	createdN := make(chan bool, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, c, err := store.EnsurePRPipeline(ctx, env.ORM, newPRPipeline(repo, recs[1], "head-1", nil))
			if err != nil {
				t.Errorf("concurrent ensure: %v", err)
			}
			createdN <- c
		}()
	}
	wg.Wait()
	close(createdN)
	n := 0
	for c := range createdN {
		if c {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("concurrent ensures created %d rows, want 1", n)
	}
}

func TestListDuePRPipelinesOrdersAndSkipsLocked(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	repo, recs := newPipelineRecords(t, env, "rid-due", 6)
	offRepo := pipelineRepo(t, env, "rid-off", core.PipelineRepoModeOff)
	pipelineRecord(t, env, offRepo, "off-rec", 1, "head-off")

	now := time.Now().UTC().Truncate(time.Second)
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	// recs[0..3] are due, oldest first 3, 1, 0, 2; recs[4] is not yet due;
	// recs[5] waits for nothing; the off repository's row is due but its
	// repository acts on nothing.
	dues := []*time.Time{at(-2 * time.Minute), at(-3 * time.Minute), at(0), at(-4 * time.Minute), at(time.Hour), nil}
	for i, d := range dues {
		if _, _, err := store.EnsurePRPipeline(ctx, env.ORM, newPRPipeline(repo, recs[i], "head", d)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.EnsurePRPipeline(ctx, env.ORM, newPRPipeline(offRepo, "off-rec", "head", at(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	ids := func(ps []*core.PRPipeline) []string {
		out := make([]string, len(ps))
		for i, p := range ps {
			out[i] = p.RecordID
		}
		return out
	}

	all, err := store.ListDuePRPipelines(ctx, env.ORM, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{recs[3], recs[1], recs[0], recs[2]}; !slices.Equal(ids(all), want) {
		t.Fatalf("due scan = %v, want %v", ids(all), want)
	}
	if first, err := store.ListDuePRPipelines(ctx, env.ORM, now, 2); err != nil || !slices.Equal(ids(first), []string{recs[3], recs[1]}) {
		t.Fatalf("limit 2 = %v %v", ids(first), err)
	}

	// Two workers: the first holds its two rows in an open transaction; the
	// second, concurrently, gets the other two without waiting.
	held := make(chan []string)
	release := make(chan struct{})
	done := make(chan error)
	go func() {
		done <- env.ORM.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
			ps, err := store.ListDuePRPipelines(ctx, env.ORM, now, 2)
			if err != nil {
				return err
			}
			held <- ids(ps)
			<-release
			return nil
		})
	}()
	first := <-held
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var second []string
	err = env.ORM.RunTx(waitCtx, orm.ReadCommitted, func(ctx context.Context) error {
		ps, err := store.ListDuePRPipelines(ctx, env.ORM, now, 10)
		second = ids(ps)
		return err
	})
	close(release)
	if err != nil {
		t.Fatalf("the second worker: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("the first worker: %v", err)
	}
	if !slices.Equal(first, []string{recs[3], recs[1]}) || !slices.Equal(second, []string{recs[0], recs[2]}) {
		t.Fatalf("workers claimed %v and %v, want disjoint halves", first, second)
	}
}

func TestPRHeadVerdictsAppendOnly(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	repo, recs := newPipelineRecords(t, env, "rid-v", 1)
	verdict := func(round int, v core.PRHeadVerdict) core.PRHeadVerdictRecord {
		return core.PRHeadVerdictRecord{
			OrgID: repo.OrgID, ProjectID: repo.ProjectID, RecordID: recs[0], HeadSHA: "head-0", Round: round,
			Verdict: v, Rule: core.PRVerdictRuleModel, RiskTier: core.PRRiskMedium, RunID: "run-1", NodeID: "review",
			ReviewerPrincipal: "reviewer",
		}
	}

	if v, err := store.LatestPRHeadVerdict(ctx, env.ORM, recs[0], "head-0"); err != nil || v != nil {
		t.Fatalf("no verdict yet: %+v %v", v, err)
	}
	first, err := store.InsertPRHeadVerdict(ctx, env.ORM, verdict(1, core.PRHeadVerdictChanges))
	if err != nil || first.ID == "" || string(first.FindingsJSON) != "[]" || len(first.RequiredDecisions) != 0 {
		t.Fatalf("insert: %+v %v", first, err)
	}
	// A repeat of the round is refused, without aborting the transaction it
	// ran in.
	err = env.ORM.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		if _, err := store.InsertPRHeadVerdict(ctx, env.ORM, verdict(1, core.PRHeadVerdictPass)); !errors.Is(err, core.ErrPipelineVerdictExists) {
			t.Errorf("a repeated round must be ErrPipelineVerdictExists, got %v", err)
		}
		_, err := store.LatestPRHeadVerdict(ctx, env.ORM, recs[0], "head-0")
		return err
	})
	if err != nil {
		t.Fatalf("the transaction survives the refusal: %v", err)
	}

	second := verdict(2, core.PRHeadVerdictPass)
	second.FindingsJSON = []byte(`[{"id":"b1"}]`)
	second.RequiredDecisions = []core.PipelineDecisionKind{core.PipelineDecisionApproveBreakingProto}
	if _, err := store.InsertPRHeadVerdict(ctx, env.ORM, second); err != nil {
		t.Fatal(err)
	}
	bogus := verdict(3, core.PRHeadVerdictPass)
	bogus.RequiredDecisions = []core.PipelineDecisionKind{"approve_everything"}
	if _, err := store.InsertPRHeadVerdict(ctx, env.ORM, bogus); err == nil {
		t.Fatal("an unknown required decision is refused")
	}
	latest, err := store.LatestPRHeadVerdict(ctx, env.ORM, recs[0], "head-0")
	if err != nil || latest.Round != 2 || latest.Verdict != core.PRHeadVerdictPass ||
		!slices.Equal(latest.RequiredDecisions, second.RequiredDecisions) || latest.NodeID != "review" {
		t.Fatalf("latest: %+v %v", latest, err)
	}
	if v, err := store.LatestPRHeadVerdict(ctx, env.ORM, recs[0], "head-other"); err != nil || v != nil {
		t.Fatalf("another head has no verdict: %+v %v", v, err)
	}

	_, err = env.SQL.Exec(`UPDATE pr_head_verdicts SET verdict = 'close' WHERE id = $1`, first.ID)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "55000" {
		t.Fatalf("an UPDATE must be refused by the trigger, got %v", err)
	}
}

func TestNextPipelineAttemptNumbersConcurrently(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	_, recs := newPipelineRecords(t, env, "rid-a", 1)
	attempt := func(sig string) core.PipelineAttempt {
		return core.PipelineAttempt{OrgID: pipelineOrg, ProjectID: storetest.SeedProjectID, SubjectKind: core.PipelineAttemptSubjectPR,
			SubjectID: recs[0], HeadSHA: "head-0", Stage: core.PipelineAttemptFix, Signature: sig, RunID: "run"}
	}

	a1, err := store.NextPipelineAttempt(ctx, env.ORM, attempt("sig-1"))
	if err != nil || a1.Attempt != 1 || a1.Outcome != "" || a1.FinishedAt != nil {
		t.Fatalf("first: %+v %v", a1, err)
	}
	a2, err := store.NextPipelineAttempt(ctx, env.ORM, attempt("sig-1"))
	if err != nil || a2.Attempt != 2 {
		t.Fatalf("second: %+v %v", a2, err)
	}
	if b1, err := store.NextPipelineAttempt(ctx, env.ORM, attempt("sig-2")); err != nil || b1.Attempt != 1 {
		t.Fatalf("a new signature starts at 1: %+v %v", b1, err)
	}

	done, err := store.FinishPipelineAttempt(ctx, env.ORM, a1.ID, core.PipelineAttemptFailed, []byte(`{"tests": 3}`))
	if err != nil || done.Outcome != core.PipelineAttemptFailed || done.FinishedAt == nil {
		t.Fatalf("finish: %+v %v", done, err)
	}
	if _, err := store.FinishPipelineAttempt(ctx, env.ORM, a1.ID, core.PipelineAttemptSucceeded, nil); !errors.Is(err, core.ErrPipelineVersionConflict) {
		t.Fatalf("finishing twice must conflict, got %v", err)
	}

	// Concurrent starts get distinct numbers, or a typed conflict.
	const workers = 6
	var wg sync.WaitGroup
	got := make(chan int, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := store.NextPipelineAttempt(ctx, env.ORM, attempt("sig-race"))
			switch {
			case err == nil:
				got <- a.Attempt
			case !errors.Is(err, core.ErrPipelineAttemptConflict):
				t.Errorf("a concurrent start failed with %v, want a number or ErrPipelineAttemptConflict", err)
			}
		}()
	}
	wg.Wait()
	close(got)
	var numbers []int
	for n := range got {
		numbers = append(numbers, n)
	}
	slices.Sort(numbers)
	if len(numbers) == 0 || len(slices.Compact(slices.Clone(numbers))) != len(numbers) {
		t.Fatalf("concurrent attempt numbers %v must be distinct", numbers)
	}
}
