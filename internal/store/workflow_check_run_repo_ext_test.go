// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
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
	claimed, err := store.ClaimWorkflowCheckRuns(ctx, env.ORM, now, now.Add(time.Minute), 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %d rows, err=%v", len(claimed), err)
	}
	got := claimed[0]
	if got.ID != row.ID || got.Timeout != 2*time.Hour || got.HeadSHA != row.HeadSHA || got.ActingIdentity != "app" {
		t.Fatalf("claimed row differs: %+v", got)
	}
	if again, _ := store.ClaimWorkflowCheckRuns(ctx, env.ORM, now, now.Add(time.Minute), 10); len(again) != 0 {
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
	claimed, err = store.ClaimWorkflowCheckRuns(ctx, env.ORM, started, started.Add(time.Minute), 10)
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
	claimed, err = store.ClaimWorkflowCheckRuns(ctx, env.ORM, ended, ended.Add(time.Minute), 10)
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
	if rest, _ := store.ClaimWorkflowCheckRuns(ctx, env.ORM, ended.Add(24*time.Hour), ended.Add(25*time.Hour), 10); len(rest) != 0 {
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
