package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

func enqueueAdmission(t *testing.T, env *storetest.Env, id, scope, key, head string, limit int) *core.TriggerEvent {
	t.Helper()
	now := time.Now().UTC()
	ev, _, err := env.Repo.EnqueueTriggerAdmission(context.Background(), &core.TriggerEvent{ID: id, UserID: storetest.SeedUserID, Kind: core.TriggerEventKindIntegration, DedupeKey: id, OccurredAt: now, CreatedAt: now, Outcome: core.TriggerEventPending, Payload: map[string]any{}}, core.TriggerAdmissionRequest{Scope: scope, Key: key, Head: head, PendingLimit: limit})
	if err != nil {
		t.Fatal(err)
	}
	return ev
}
func reserveAdmission(env *storetest.Env, id string, n int) error {
	return env.Repo.RunTx(context.Background(), orm.ReadCommitted, func(ctx context.Context) error { return env.Repo.ReserveTriggerAdmission(ctx, id, n) })
}

// storetest gives every test a separately migrated database; no production
// databases or the control-plane real-data port are involved.
func TestTriggerAdmissionConcurrentBoundAndCapacityLowering(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	for i := 0; i < 16; i++ {
		enqueueAdmission(t, env, fmt.Sprint("e", i), "scope", "key"+fmt.Sprint(i), "head", 100)
	}
	var running atomic.Int32
	var wg sync.WaitGroup
	for pass := 0; pass < 4; pass++ {
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				err := reserveAdmission(env, fmt.Sprint("e", i), 4)
				if err != nil && !errors.Is(err, core.ErrAdmissionDeferred) {
					t.Errorf("reserve: %v", err)
				}
			}(i)
		}
		wg.Wait()
	}
	rows, err := env.Repo.ListTriggerAdmissions(ctx, 0, 1<<62, 100)
	if err != nil {
		t.Fatal(err)
	}
	seenOrder := map[int64]bool{}
	for _, r := range rows {
		if r.Order <= 0 || seenOrder[r.Order] {
			t.Fatalf("FIFO ordinal was not DB-assigned uniquely: %+v", r)
		}
		seenOrder[r.Order] = true
		if r.State == "reserved" {
			running.Add(1)
		}
	}
	if running.Load() != 4 {
		t.Fatalf("reserved=%d want 4", running.Load())
	}
	if err = reserveAdmission(env, "e4", 2); !errors.Is(err, core.ErrAdmissionDeferred) {
		t.Fatalf("lowered capacity admitted: %v", err)
	}
	// Another owner's/project's scope is independent even at capacity.
	enqueueAdmission(t, env, "isolated", "other-owner-project", "key0", "head", 100)
	if err = reserveAdmission(env, "isolated", 1); err != nil {
		t.Fatal(err)
	}
}

func TestTriggerAdmissionKeySupersessionDedupeOverloadAndGeneration(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	first := enqueueAdmission(t, env, "first", "scope", "pr", "sha1", 1)
	repeat := enqueueAdmission(t, env, "repeat", "scope", "pr", "sha1", 1)
	if repeat.ID != first.ID {
		t.Fatal("exact head did not dedupe")
	}
	if err := reserveAdmission(env, first.ID, 2); err != nil {
		t.Fatal(err)
	}
	next := enqueueAdmission(t, env, "next", "scope", "pr", "sha2", 1)
	newer := enqueueAdmission(t, env, "newer", "scope", "pr", "sha3", 1)
	old, err := env.Repo.GetTriggerAdmission(ctx, next.ID)
	if err != nil || old.State != "superseded" || old.SupersededBy != newer.ID {
		t.Fatalf("old=%+v %v", old, err)
	}
	if err = reserveAdmission(env, newer.ID, 2); !errors.Is(err, core.ErrAdmissionDeferred) {
		t.Fatalf("same key overlapped: %v", err)
	}
	overload := enqueueAdmission(t, env, "overload", "scope", "different-pr", "sha4", 1)
	if overload.Outcome != core.TriggerEventFailed {
		t.Fatal("overload was not durable explicit failure")
	}
	// Half-start has reserved capacity before any execution has been recorded.
	if ok, err := env.Repo.ReleaseTriggerAdmission(ctx, first.ID, "root", ""); err != nil || ok {
		t.Fatal("unbound half-start released")
	}
	if err = env.Repo.BindTriggerAdmissionRun(ctx, first.ID, "run1"); err != nil {
		t.Fatal(err)
	}
	if err = env.Repo.BindTriggerAdmissionRun(ctx, first.ID, "run2"); err == nil {
		t.Fatal("original run replaced")
	}
	// Associate root as launch transaction does.
	if _, err = env.SQL.ExecContext(ctx, `UPDATE trigger_events SET chat_id=NULL WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := env.Repo.ReleaseTriggerAdmission(ctx, first.ID, "root", "stale-run"); err != nil || ok {
		t.Fatal("stale run freed permit")
	}
	// Use an actual launch chat to satisfy the FK and release exact generation.
	seedRun(t, env, "admission-root", core.Active())
	if _, err = env.SQL.ExecContext(ctx, `UPDATE trigger_events SET chat_id=$2,admission_workflow_id=$2 WHERE id=$1`, first.ID, "admission-root"); err != nil {
		t.Fatal(err)
	}
	if ok, err := env.Repo.ReleaseTriggerAdmission(ctx, first.ID, "admission-root", "run1"); err != nil || !ok {
		t.Fatalf("exact release %v %v", ok, err)
	}
	if err = reserveAdmission(env, newer.ID, 2); err != nil {
		t.Fatal(err)
	}
	if ok, err := env.Repo.ReleaseTriggerAdmission(ctx, first.ID, "admission-root", "run1"); err != nil || ok {
		t.Fatal("duplicate old release succeeded")
	}
	current, _ := env.Repo.GetTriggerAdmission(ctx, newer.ID)
	if current.State != "reserved" {
		t.Fatal("stale completion freed newer generation")
	}
	// Intentional waiting never expires through the ordinary 24h redriver.
	pending, err := store.ListStalePendingTriggerEvents(ctx, env.ORM, time.Now().Add(48*time.Hour), 100)
	if err != nil || len(pending) != 0 {
		t.Fatalf("admission leaked into expiring redriver: %v %v", pending, err)
	}
}
