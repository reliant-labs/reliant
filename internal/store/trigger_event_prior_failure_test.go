package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// PriorTriggerFailures finds each trigger's newest FAILED firing in the
// window before its bound: a firing that could not launch, or a run that
// failed or completed only through its failure path. A success, a skip, a
// run still going, a firing outside the window and another user's firing are
// not one.
func TestPriorTriggerFailures(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	bound := time.Now().UTC().Truncate(time.Microsecond)
	for _, id := range []string{"trg-pf-a", "trg-pf-b", "trg-pf-c"} {
		if err := store.InsertTrigger(ctx, env.ORM, newStoreTrigger(id, id, bound.Add(-48*time.Hour))); err != nil {
			t.Fatalf("InsertTrigger(%s): %v", id, err)
		}
	}
	insert := func(id, triggerID, userID string, outcome core.TriggerEventOutcome, runStatus, runOutcome string, at time.Time) {
		t.Helper()
		ev := newStoreTriggerEvent(id, triggerID, outcome, at)
		ev.UserID = userID
		if created, err := env.Repo.CreateTriggerEvent(ctx, ev); err != nil || !created {
			t.Fatalf("CreateTriggerEvent(%s) = %v, %v", id, created, err)
		}
		if runStatus == "" && runOutcome == "" {
			return
		}
		if _, err := env.ORM.Bun().NewRaw(
			`UPDATE trigger_events SET run_status = NULLIF(?, ''), run_outcome = NULLIF(?, '') WHERE id = ?`,
			runStatus, runOutcome, id,
		).Exec(ctx); err != nil {
			t.Fatalf("set run of %s: %v", id, err)
		}
	}
	me := storetest.SeedUserID
	// trg-pf-a: an old failure outside the window, a launch failure inside
	// it, then a failed run, then successes, skips and a live run after.
	insert("pf-a-old", "trg-pf-a", me, core.TriggerEventFailed, "", "", bound.Add(-30*time.Hour))
	insert("pf-a-1", "trg-pf-a", me, core.TriggerEventFailed, "", "", bound.Add(-20*time.Hour))
	insert("pf-a-2", "trg-pf-a", me, core.TriggerEventLaunched, core.TriggerRunFailed, "", bound.Add(-10*time.Hour))
	insert("pf-a-ok", "trg-pf-a", me, core.TriggerEventLaunched, core.TriggerRunCompleted, "", bound.Add(-5*time.Hour))
	insert("pf-a-skip", "trg-pf-a", me, core.TriggerEventSkipped, "", "", bound.Add(-4*time.Hour))
	insert("pf-a-skiprun", "trg-pf-a", me, core.TriggerEventLaunched, core.TriggerRunCompleted, core.RunOutcomeSkipped, bound.Add(-3*time.Hour))
	insert("pf-a-live", "trg-pf-a", me, core.TriggerEventLaunched, "", "", bound.Add(-2*time.Hour))
	// The bound itself and after are not before it.
	insert("pf-a-at", "trg-pf-a", me, core.TriggerEventFailed, "", "", bound)
	// trg-pf-b: a run that completed through its failure path; another
	// user's failure, later, is not the caller's.
	insert("pf-b-1", "trg-pf-b", me, core.TriggerEventLaunched, core.TriggerRunCompleted, core.RunOutcomeFailureHandled, bound.Add(-6*time.Hour))
	insert("pf-b-other", "trg-pf-b", "someone-else", core.TriggerEventFailed, "", "", bound.Add(-time.Hour))
	// trg-pf-c: only successes and an old failure.
	insert("pf-c-old", "trg-pf-c", me, core.TriggerEventFailed, "", "", bound.Add(-25*time.Hour))
	insert("pf-c-ok", "trg-pf-c", me, core.TriggerEventLaunched, core.TriggerRunCompleted, "", bound.Add(-time.Hour))

	got, err := store.PriorTriggerFailures(ctx, env.ORM, me,
		[]string{"trg-pf-a", "trg-pf-b", "trg-pf-c", "trg-pf-a", "trg-pf-missing", "trg-pf-a"},
		[]time.Time{bound, bound, bound, bound.Add(-10 * time.Hour), bound, bound.Add(-5 * time.Hour)},
		24*time.Hour)
	if err != nil {
		t.Fatalf("PriorTriggerFailures: %v", err)
	}
	want := []*time.Time{
		// The success ends the earlier failure episode.
		nil,
		// failure_handled is a failure; the other user's is not.
		ptrTime(bound.Add(-6 * time.Hour)),
		// The old failure is outside the window.
		nil,
		// The same trigger at an earlier bound: strictly before it.
		ptrTime(bound.Add(-20 * time.Hour)),
		// No firings at all.
		nil,
		// Before the success, the failed run still counts.
		ptrTime(bound.Add(-10 * time.Hour)),
	}
	if len(got) != len(want) {
		t.Fatalf("PriorTriggerFailures returned %d results, want %d", len(got), len(want))
	}
	for i := range want {
		switch {
		case want[i] == nil && got[i] != nil:
			t.Errorf("result %d = %v, want none", i, *got[i])
		case want[i] != nil && got[i] == nil:
			t.Errorf("result %d = none, want %v", i, *want[i])
		case want[i] != nil && !got[i].Equal(*want[i]):
			t.Errorf("result %d = %v, want %v", i, *got[i], *want[i])
		}
	}

	if _, err := store.PriorTriggerFailures(ctx, env.ORM, "", []string{"trg-pf-a"}, []time.Time{bound}, time.Hour); err == nil {
		t.Error("PriorTriggerFailures with no user must fail closed")
	}
	if _, err := store.PriorTriggerFailures(ctx, env.ORM, me, []string{"trg-pf-a"}, nil, time.Hour); err == nil {
		t.Error("PriorTriggerFailures with unaligned bounds must fail")
	}
	if got, err := store.PriorTriggerFailures(ctx, env.ORM, me, nil, nil, time.Hour); err != nil || len(got) != 0 {
		t.Errorf("PriorTriggerFailures(no triggers) = %v, %v; want empty", got, err)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
