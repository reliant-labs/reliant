// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// seedCheckEvent records a pending firing, and its check.
func seedCheckEvent(t *testing.T, env *storetest.Env, eventID string) *core.WorkflowCheckRun {
	t.Helper()
	ctx := context.Background()
	if ok, err := store.InsertTriggerEventIfAbsent(ctx, env.ORM, &core.TriggerEvent{
		ID: eventID, UserID: storetest.SeedUserID, Kind: core.TriggerEventKindIntegration, DedupeKey: eventID,
		OccurredAt: l2Base, Payload: map[string]any{}, Outcome: core.TriggerEventPending, CreatedAt: l2Base,
	}); err != nil || !ok {
		t.Fatalf("seed trigger event: created=%v err=%v", ok, err)
	}
	row := &core.WorkflowCheckRun{
		ID: "check-" + eventID, TriggerEventID: eventID, Name: "reliant/ci", UserID: storetest.SeedUserID,
		ActingIdentity: "app", Automation: "ci", Owner: "reliant-labs", Repo: "reliant",
		HeadSHA: "6dcb09b5b57875f334f61aebed695e2e4193db5e", Timeout: 2 * time.Hour,
		NextSyncAt: l2Base, CreatedAt: l2Base, UpdatedAt: l2Base,
	}
	ok, err := store.InsertWorkflowCheckRun(ctx, env.ORM, row)
	if err != nil || !ok {
		t.Fatalf("insert check: created=%v err=%v", ok, err)
	}
	return row
}

// TestWorkflowCheckRunLifecycle covers a check's row through its life: the
// fire's insert (idempotent), the claim and lease, the observation of a
// pending and then a launched firing, the run's two reports (each once), and
// the syncer's save.
func TestWorkflowCheckRunLifecycle(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	row := seedCheckEvent(t, env, "ev-1")

	// A retried fire adds nothing.
	dup := *row
	dup.ID = "check-dup"
	if ok, err := store.InsertWorkflowCheckRun(ctx, env.ORM, &dup); err != nil || ok {
		t.Fatalf("a second check of one name on one firing: created=%v err=%v", ok, err)
	}

	// Claimed once; leased until it is released.
	now := l2Base.Add(time.Second)
	claimed, err := store.ClaimWorkflowCheckRuns(ctx, env.ORM, "syncer-a", now, now.Add(time.Minute), 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %d rows, err=%v", len(claimed), err)
	}
	got := claimed[0]
	if got.ID != row.ID || got.Timeout != 2*time.Hour || got.HeadSHA != row.HeadSHA || got.ActingIdentity != "app" {
		t.Fatalf("claimed row differs: %+v", got)
	}
	if again, _ := store.ClaimWorkflowCheckRuns(ctx, env.ORM, "syncer-a", now, now.Add(time.Minute), 10); len(again) != 0 {
		t.Fatalf("a leased check was claimed twice")
	}

	obs, err := store.ObserveWorkflowCheckRun(ctx, env.ORM, "ev-1")
	if err != nil {
		t.Fatal(err)
	}
	requireSame(t, "pending firing", obs, &core.WorkflowCheckObservation{EventOutcome: core.TriggerEventPending})
	if _, err := store.ObserveWorkflowCheckRun(ctx, env.ORM, "no-such-event"); !errors.Is(err, core.ErrWorkflowCheckRunNotFound) {
		t.Fatalf("a missing firing: %v", err)
	}

	// The syncer saves what it wrote and releases the lease.
	id := int64(4242)
	got.GitHubCheckRunID, got.GitHubStatus, got.GitHubDigest = &id, core.CheckStatusQueued, "d1"
	got.NextSyncAt, got.Attempts, got.LastError = now.Add(15*time.Second), 0, ""
	if err := store.SaveWorkflowCheckRunSync(ctx, env.ORM, got); err != nil {
		t.Fatal(err)
	}

	// The firing launches chat-ck; its root workflow completes into a
	// failure outcome.
	seedChat(t, env, "chat-ck")
	mustExec(t, env, `UPDATE trigger_events SET outcome = 'launched', chat_id = 'chat-ck' WHERE id = 'ev-1'`)
	if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, newWorkflow("chat-ck", "chat-ck", nil, core.Active(), l2Base)); err != nil {
		t.Fatal(err)
	}

	// The run's first node starts: the check is due at once, and a second
	// stamp keeps the first.
	started := now.Add(time.Minute)
	if err := store.MarkWorkflowCheckRunsStarted(ctx, env.ORM, "chat-ck", started); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkWorkflowCheckRunsStarted(ctx, env.ORM, "chat-ck", started.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimWorkflowCheckRuns(ctx, env.ORM, "syncer-a", started, started.Add(time.Minute), 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("a started check is due at once: %d rows, err=%v", len(claimed), err)
	}
	got = claimed[0]
	if got.StartedAt == nil || !got.StartedAt.Equal(started) {
		t.Fatalf("started_at = %v, want %v", got.StartedAt, started)
	}
	if got.GitHubCheckRunID == nil || *got.GitHubCheckRunID != 4242 || got.GitHubStatus != core.CheckStatusQueued || got.GitHubDigest != "d1" {
		t.Fatalf("the save did not stick: %+v", got)
	}
	got.NextSyncAt = started.Add(15 * time.Second)
	if err := store.SaveWorkflowCheckRunSync(ctx, env.ORM, got); err != nil {
		t.Fatal(err)
	}

	// The run ends: the first report stands.
	ended := started.Add(time.Minute)
	output := map[string]any{"title": "red", "summary": "2 failures"}
	if err := store.RecordWorkflowCheckRunsEnded(ctx, env.ORM, "chat-ck", core.WorkflowCheckEnding{Status: "failed", Verdict: "failure", Output: output}, ended); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordWorkflowCheckRunsEnded(ctx, env.ORM, "chat-ck", core.WorkflowCheckEnding{Status: "completed"}, ended); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateWorkflowStatus(ctx, env.ORM, "chat-ck", core.Completed()); err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkflowOutcome(ctx, env.ORM, "chat-ck", "failure"); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimWorkflowCheckRuns(ctx, env.ORM, "syncer-a", ended, ended.Add(time.Minute), 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("an ended check is due at once: %d rows, err=%v", len(claimed), err)
	}
	got = claimed[0]
	if got.EndedStatus != "failed" || got.Verdict != "failure" {
		t.Fatalf("ending = %q/%q, want the first report", got.EndedStatus, got.Verdict)
	}
	requireSame(t, "output", got.Output, output)

	obs, err = store.ObserveWorkflowCheckRun(ctx, env.ORM, "ev-1")
	if err != nil {
		t.Fatal(err)
	}
	completed := core.Completed()
	requireSame(t, "launched firing", obs, &core.WorkflowCheckObservation{
		EventOutcome: core.TriggerEventLaunched, ChatID: "chat-ck", Root: &completed, RootVerdict: "failure",
	})

	// Finished: never claimed again.
	got.FinishedAt = &ended
	if err := store.SaveWorkflowCheckRunSync(ctx, env.ORM, got); err != nil {
		t.Fatal(err)
	}
	if rest, _ := store.ClaimWorkflowCheckRuns(ctx, env.ORM, "syncer-a", ended.Add(24*time.Hour), ended.Add(25*time.Hour), 10); len(rest) != 0 {
		t.Fatalf("a finished check was claimed")
	}
	// And a report after it finished changes nothing.
	if err := store.MarkWorkflowCheckRunsStarted(ctx, env.ORM, "chat-ck", ended.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

// A report for a chat no firing launched, or whose firing has no check, is a
// no-op.
func TestWorkflowCheckRunReportsWithoutACheck(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	seedChat(t, env, "chat-none")
	if err := store.MarkWorkflowCheckRunsStarted(ctx, env.ORM, "chat-none", l2Base); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordWorkflowCheckRunsEnded(ctx, env.ORM, "chat-none", core.WorkflowCheckEnding{Status: "completed"}, l2Base); err != nil {
		t.Fatal(err)
	}
}

// Two syncers claim one queue at once: every due check is claimed by exactly
// one of them, each under generation 1. SKIP LOCKED decides who gets which,
// so the split varies run to run; that each row is held once does not.
func TestWorkflowCheckRunLeaseRace(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	const n = 20
	for i := range n {
		seedCheckEvent(t, env, fmt.Sprintf("ev-race-%02d", i))
	}
	now := l2Base.Add(time.Second)
	start := make(chan struct{})
	got := make([][]*core.WorkflowCheckRun, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for w := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for {
				rows, err := store.ClaimWorkflowCheckRuns(ctx, env.ORM, fmt.Sprintf("syncer-%d", w), now, now.Add(time.Minute), 3)
				if err != nil {
					errs[w] = err
					return
				}
				if len(rows) == 0 {
					return
				}
				got[w] = append(got[w], rows...)
			}
		}()
	}
	close(start)
	wg.Wait()
	seen := map[string]string{}
	for w, rows := range got {
		if errs[w] != nil {
			t.Fatalf("syncer-%d: %v", w, errs[w])
		}
		for _, r := range rows {
			holder := fmt.Sprintf("syncer-%d", w)
			if prev, dup := seen[r.ID]; dup {
				t.Fatalf("check %s claimed by %s and %s", r.ID, prev, holder)
			}
			seen[r.ID] = holder
			if r.ClaimedBy != holder || r.LeaseGeneration != 1 || r.ClaimedUntil == nil || !r.ClaimedUntil.Equal(now.Add(time.Minute)) {
				t.Fatalf("check %s: lease %s/%d until %v, want %s/1", r.ID, r.ClaimedBy, r.LeaseGeneration, r.ClaimedUntil, holder)
			}
		}
	}
	if len(seen) != n {
		t.Fatalf("%d of %d checks claimed", len(seen), n)
	}
}

// The fence. A holds the lease and renews it; B can take the row only once
// the renewed lease has expired, and its claim bumps the generation. A's
// renewal and save are then refused and write nothing, and B's save lands.
func TestWorkflowCheckRunLeaseFence(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	row := seedCheckEvent(t, env, "ev-fence")
	at := func(d time.Duration) time.Time { return l2Base.Add(d) }

	claim := func(holder string, now time.Time) []*core.WorkflowCheckRun {
		t.Helper()
		rows, err := store.ClaimWorkflowCheckRuns(ctx, env.ORM, holder, now, now.Add(time.Minute), 10)
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	renew := func(r *core.WorkflowCheckRun, until time.Time) []string {
		t.Helper()
		ids, err := store.RenewWorkflowCheckRunLeases(ctx, env.ORM, r.ClaimedBy,
			[]core.WorkflowCheckLease{{ID: r.ID, Generation: r.LeaseGeneration}}, until)
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}
	stored := func() *store.WorkflowCheckRun {
		t.Helper()
		r, err := store.GetWorkflowCheckRunByID(ctx, env.ORM, row.ID)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	a := claim("syncer-a", at(time.Second))
	if len(a) != 1 || a[0].LeaseGeneration != 1 || a[0].ClaimedBy != "syncer-a" {
		t.Fatalf("A's claim: %+v", a)
	}
	if b := claim("syncer-b", at(30*time.Second)); len(b) != 0 {
		t.Fatal("B took a held lease")
	}
	// A's heartbeat extends the lease past its first expiry.
	if ids := renew(a[0], at(91*time.Second)); len(ids) != 1 || ids[0] != row.ID {
		t.Fatalf("A's renewal: %v", ids)
	}
	if b := claim("syncer-b", at(62*time.Second)); len(b) != 0 {
		t.Fatal("B took a renewed lease")
	}

	// A stalls; the renewed lease expires and B takes the row.
	b := claim("syncer-b", at(92*time.Second))
	if len(b) != 1 || b[0].LeaseGeneration != 2 || b[0].ClaimedBy != "syncer-b" {
		t.Fatalf("B's claim after expiry: %+v", b)
	}
	if ids := renew(a[0], at(3*time.Minute)); len(ids) != 0 {
		t.Fatalf("A renewed a lease B holds: %v", ids)
	}

	// A's stale save is refused and writes nothing.
	stale := *a[0]
	staleID := int64(1)
	stale.GitHubCheckRunID, stale.GitHubStatus, stale.GitHubDigest = &staleID, core.CheckStatusInProgress, "stale"
	stale.Attempts, stale.LastError, stale.UpdatedAt = 3, "stale failure", at(95*time.Second)
	if err := store.SaveWorkflowCheckRunSync(ctx, env.ORM, &stale); !errors.Is(err, core.ErrWorkflowCheckLeaseLost) {
		t.Fatalf("A's stale save: %v, want ErrWorkflowCheckLeaseLost", err)
	}
	if r := stored(); r.ClaimedBy != "syncer-b" || r.LeaseGeneration != 2 || r.GithubStatus != "" || r.GithubCheckRunID != nil || r.Attempts != 0 || r.LastError != "" {
		t.Fatalf("the refused save wrote: %+v", r)
	}

	// B's save lands and releases the lease.
	fresh := *b[0]
	freshID := int64(4242)
	finished := at(100 * time.Second)
	fresh.GitHubCheckRunID, fresh.GitHubStatus, fresh.GitHubConclusion, fresh.GitHubDigest = &freshID, core.CheckStatusCompleted, "success", "fresh"
	fresh.FinishedAt, fresh.UpdatedAt = &finished, finished
	if err := store.SaveWorkflowCheckRunSync(ctx, env.ORM, &fresh); err != nil {
		t.Fatal(err)
	}
	r := stored()
	if r.ClaimedBy != "" || r.ClaimedUntil != nil || r.LeaseGeneration != 2 || r.GithubStatus != core.CheckStatusCompleted ||
		r.GithubCheckRunID == nil || *r.GithubCheckRunID != 4242 || r.FinishedAt == nil {
		t.Fatalf("B's save: %+v", r)
	}

	// Released, the row is no one's: A's save is still refused, nobody can
	// renew it, and B cannot save twice on one claim.
	if err := store.SaveWorkflowCheckRunSync(ctx, env.ORM, &stale); !errors.Is(err, core.ErrWorkflowCheckLeaseLost) {
		t.Fatalf("A's save after B released: %v", err)
	}
	if ids := renew(b[0], at(5*time.Minute)); len(ids) != 0 {
		t.Fatalf("a released lease was renewed: %v", ids)
	}
	if err := store.SaveWorkflowCheckRunSync(ctx, env.ORM, &fresh); !errors.Is(err, core.ErrWorkflowCheckLeaseLost) {
		t.Fatalf("a second save on one claim: %v", err)
	}
	if r2 := stored(); r2.GithubDigest != "fresh" || !r2.UpdatedAt.Equal(r.UpdatedAt) {
		t.Fatalf("a refused save wrote: %+v", r2)
	}

	// A row that is gone refuses the save the same way.
	mustExec(t, env, `DELETE FROM trigger_events WHERE id = 'ev-fence'`)
	if err := store.SaveWorkflowCheckRunSync(ctx, env.ORM, &fresh); !errors.Is(err, core.ErrWorkflowCheckLeaseLost) {
		t.Fatalf("save of a deleted check: %v", err)
	}
}
