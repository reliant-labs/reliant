// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// seedRunEvents writes n outbox rows, alternating layers, created one second
// apart so ORDER BY created_at is total.
func seedRunEvents(tb testing.TB, env *storetest.Env, prefix string, n int) []string {
	tb.Helper()
	ctx := context.Background()
	chatID := "chat-re-" + prefix
	seedChat(tb, env, chatID)
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = fmt.Sprintf("%s-%04d", prefix, i)
		ev := &core.RunEvent{
			ID: ids[i], UserID: storetest.SeedUserID, ChatID: chatID, WorkflowName: "wf",
			Outcome: core.RunEventFinished, DedupeKey: ids[i], Payload: map[string]any{"i": float64(i)},
			OccurredAt: l2Base, CreatedAt: l2Base.Add(time.Duration(i) * time.Second),
		}
		var ok bool
		var err error
		if i%2 == 0 {
			ok, err = store.InsertRunEvent(ctx, env.ORM, ev)
		} else {
			ok, err = env.Repo.CreateRunEvent(ctx, ev)
		}
		if err != nil || !ok {
			tb.Fatalf("seed run event %d: created=%v err=%v", i, ok, err)
		}
	}
	return ids
}

func runEventIDs(rows []*core.RunEvent) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	sort.Strings(out)
	return out
}

// TestRunEventReadsAndWritesAgreeWithSQLC covers the outbox row's lifecycle
// through both layers: dedupe on insert, the two reads, dispatch and prune.
func TestRunEventReadsAndWritesAgreeWithSQLC(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	ids := seedRunEvents(t, env, "rw", 4)

	// A retried transition is a no-op on either layer.
	dup := &core.RunEvent{ID: "other-id", UserID: storetest.SeedUserID, ChatID: "chat-re-rw",
		Outcome: core.RunEventFailed, DedupeKey: ids[0], OccurredAt: l2Base}
	if created, err := store.InsertRunEvent(ctx, env.ORM, dup); err != nil || created {
		t.Fatalf("port re-insert of a dedupe key: created=%v err=%v", created, err)
	}
	if created, err := env.Repo.CreateRunEvent(ctx, dup); err != nil || created {
		t.Fatalf("twin re-insert of a dedupe key: created=%v err=%v", created, err)
	}

	for _, id := range ids {
		port, err := store.LoadRunEvent(ctx, env.ORM, id)
		if err != nil {
			t.Fatal(err)
		}
		twin, err := env.Repo.GetRunEvent(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		requireSame(t, "event "+id, port, twin)
		byKey, err := store.LoadRunEventByDedupe(ctx, env.ORM, id)
		if err != nil {
			t.Fatal(err)
		}
		requireSame(t, "by dedupe "+id, byKey, port)
	}
	got, _ := store.LoadRunEvent(ctx, env.ORM, ids[1])
	if got.Outcome != core.RunEventFinished || got.Payload["i"] != float64(1) || got.WorkflowName != "wf" {
		t.Fatalf("event read back as %+v", got)
	}
	if _, err := store.LoadRunEvent(ctx, env.ORM, "missing"); !errors.Is(err, core.ErrRunEventNotFound) {
		t.Fatalf("LoadRunEvent(missing) = %v", err)
	}
	if _, err := store.LoadRunEventByDedupe(ctx, env.ORM, "missing"); !errors.Is(err, core.ErrRunEventNotFound) {
		t.Fatalf("LoadRunEventByDedupe(missing) = %v", err)
	}

	// A nil payload is stored as the empty object, and a zero CreatedAt is now.
	bare := &core.RunEvent{ID: "bare", UserID: storetest.SeedUserID, ChatID: "chat-re-rw",
		Outcome: core.RunEventBlocked, DedupeKey: "bare", OccurredAt: l2Base}
	if _, err := store.InsertRunEvent(ctx, env.ORM, bare); err != nil {
		t.Fatal(err)
	}
	twinBare, err := env.Repo.GetRunEvent(ctx, "bare")
	if err != nil || len(twinBare.Payload) != 0 || twinBare.Payload == nil || twinBare.CreatedAt.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("bare event = %+v, %v", twinBare, err)
	}

	now := l2Base.Add(time.Hour)
	if err := store.MarkRunEventDispatched(ctx, env.ORM, ids[0], now); err != nil {
		t.Fatal(err)
	}
	if err := env.Repo.MarkRunEventDispatched(ctx, ids[1], now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunEventDispatched(ctx, env.ORM, "missing", now); err != nil {
		t.Fatalf("marking a missing event is a no-op, as its twin is: %v", err)
	}
	for i, want := range []time.Time{now, now.Add(time.Minute)} {
		ev, _ := env.Repo.GetRunEvent(ctx, ids[i])
		if ev.DispatchedAt == nil || !ev.DispatchedAt.Equal(want) {
			t.Fatalf("%s dispatched_at = %v, want %v", ids[i], ev.DispatchedAt, want)
		}
	}

	// Prune: only the dispatched event older than the cutoff goes.
	n, err := store.DeleteDispatchedRunEventsBefore(ctx, env.ORM, now.Add(30*time.Second))
	if err != nil || n != 1 {
		t.Fatalf("port prune = %d, %v; want 1", n, err)
	}
	n, err = env.Repo.DeleteDispatchedRunEventsBefore(ctx, now.Add(2*time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("twin prune = %d, %v; want 1", n, err)
	}
	if _, err := store.LoadRunEvent(ctx, env.ORM, ids[2]); err != nil {
		t.Fatalf("an undispatched event was pruned: %v", err)
	}
}

// TestRunEventEmitterQueriesAgreeWithSQLC covers the three reads the emitter
// makes before writing a blocked event.
func TestRunEventEmitterQueriesAgreeWithSQLC(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedChat(t, env, "chat-blk")

	check := func(kind core.TriggerKind, want bool) {
		t.Helper()
		port, err := store.HasEnabledTriggerOfKind(ctx, env.ORM, storetest.SeedUserID, kind)
		if err != nil {
			t.Fatal(err)
		}
		twin, err := env.Repo.HasEnabledTriggerOfKind(ctx, storetest.SeedUserID, kind)
		if err != nil {
			t.Fatal(err)
		}
		if port != want || twin != want {
			t.Fatalf("HasEnabledTriggerOfKind(%s): port %v, twin %v, want %v", kind, port, twin, want)
		}
	}
	check(core.TriggerKindWorkflowEvent, false)
	mustExec(t, env, `INSERT INTO triggers (id, user_id, project_id, name, kind, workflow, enabled)
		VALUES ('trg-off', $1, $2, 'off', 'workflow_event', 'wf', false)`, storetest.SeedUserID, storetest.SeedProjectID)
	check(core.TriggerKindWorkflowEvent, false) // disabled
	mustExec(t, env, `INSERT INTO triggers (id, user_id, project_id, name, kind, workflow)
		VALUES ('trg-on', 'someone-else', $1, 'on', 'workflow_event', 'wf')`, storetest.SeedProjectID)
	check(core.TriggerKindWorkflowEvent, false) // another user's
	mustExec(t, env, `INSERT INTO triggers (id, user_id, project_id, name, kind, workflow)
		VALUES ('trg-mine', $1, $2, 'mine', 'workflow_event', 'wf')`, storetest.SeedUserID, storetest.SeedProjectID)
	check(core.TriggerKindWorkflowEvent, true)
	check(core.TriggerKindSchedule, false)

	count := func(exclude string, want int) {
		t.Helper()
		port, err := store.CountOtherPendingBlockers(ctx, env.ORM, "chat-blk", exclude)
		if err != nil {
			t.Fatal(err)
		}
		twin, err := env.Repo.CountOtherPendingBlockers(ctx, "chat-blk", exclude)
		if err != nil {
			t.Fatal(err)
		}
		if port != want || twin != want {
			t.Fatalf("CountOtherPendingBlockers(exclude %s): port %d, twin %d, want %d", exclude, port, twin, want)
		}
	}
	count("", 0)
	mustExec(t, env, `INSERT INTO approvals (id, chat_id, approval_type, entity_id, status, title, created_at)
		VALUES ('ap-1', 'chat-blk', 1, 'e-1', 1, 't', now()), ('ap-done', 'chat-blk', 1, 'e-2', 2, 't', now())`)
	mustExec(t, env, `INSERT INTO questions (id, chat_id, workflow_id) VALUES ('q-1', 'chat-blk', 'wf')`)
	count("", 2)
	count("ap-1", 1)
	count("q-1", 1)

	err := env.ORM.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		if err := store.LockChatForRunEvent(ctx, env.ORM, "chat-blk"); err != nil {
			return err
		}
		return env.Repo.LockChatForRunEvent(ctx, "chat-blk") // the same transaction: re-entrant
	})
	if err != nil {
		t.Fatalf("lock chat: %v", err)
	}
	err = env.ORM.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		return store.LockChatForRunEvent(ctx, env.ORM, "missing")
	})
	if !errors.Is(err, core.ErrChatNotFound) {
		t.Fatalf("lock of a missing chat = %v, want ErrChatNotFound", err)
	}
}

// TestLockChatForRunEventSerializesAcrossLayers: the row lock taken through
// the port blocks the twin's in another transaction until it commits.
func TestLockChatForRunEventSerializesAcrossLayers(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedChat(t, env, "chat-rowlock")

	locked, release := make(chan struct{}), make(chan struct{})
	var releasedAt time.Time
	done := make(chan error, 1)
	go func() {
		done <- env.ORM.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
			if err := store.LockChatForRunEvent(ctx, env.ORM, "chat-rowlock"); err != nil {
				return err
			}
			close(locked)
			<-release
			releasedAt = time.Now()
			return nil
		})
	}()
	<-locked
	acquired := make(chan time.Time, 1)
	go func() {
		_ = env.Repo.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
			if err := env.Repo.LockChatForRunEvent(ctx, "chat-rowlock"); err != nil {
				t.Errorf("twin lock: %v", err)
			}
			acquired <- time.Now()
			return nil
		})
	}()
	time.Sleep(100 * time.Millisecond)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if at := <-acquired; at.Before(releasedAt) {
		t.Fatalf("twin took the row lock at %v, before the port's holder committed at %v", at, releasedAt)
	}
}

// previousReleaseClaimSQL is the outbox claim exactly as the release before
// this port sends it (sqlc's ClaimRunEvents). During a rolling deploy both
// run against one outbox, so the port's claim must stay disjoint from it.
const previousReleaseClaimSQL = `UPDATE run_events SET claimed_until = $1::timestamptz
WHERE id IN (
    SELECT r.id FROM run_events r
    WHERE r.dispatched_at IS NULL
      AND (r.claimed_until IS NULL OR r.claimed_until < $2::timestamptz)
    ORDER BY r.created_at, r.id
    LIMIT $3::integer
    FOR UPDATE SKIP LOCKED
)
RETURNING id`

func previousReleaseClaim(env *storetest.Env, now, lease time.Time, max int) ([]string, error) {
	rows, err := env.SQL.Query(previousReleaseClaimSQL, lease, now, max)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, rows.Err()
}

// TestClaimRunEventsMatchesSQLC: on the same queue state — one dispatched,
// one leased into the future, one with an expired lease — both layers claim
// the same rows, oldest first, capped at max, and leave the same lease.
func TestClaimRunEventsMatchesSQLC(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	ids := seedRunEvents(t, env, "ev", 6)

	now := l2Base.Add(time.Hour)
	mustExec(t, env, `UPDATE run_events SET dispatched_at = $1 WHERE id = $2`, now, ids[0])
	mustExec(t, env, `UPDATE run_events SET claimed_until = $1 WHERE id = $2`, now.Add(time.Minute), ids[1])
	mustExec(t, env, `UPDATE run_events SET claimed_until = $1 WHERE id = $2`, now.Add(-time.Minute), ids[2])

	lease := now.Add(30 * time.Second)
	got, err := store.ClaimRunEvents(ctx, env.ORM, now, lease, 3)
	if err != nil {
		t.Fatalf("port claim: %v", err)
	}
	want := []string{ids[2], ids[3], ids[4]}
	if g := runEventIDs(got); fmt.Sprint(g) != fmt.Sprint(want) {
		t.Fatalf("port claimed %v, want %v", g, want)
	}
	var leased int
	if err := env.SQL.QueryRow(`SELECT count(*) FROM run_events WHERE claimed_until = $1`, lease).Scan(&leased); err != nil || leased != 3 {
		t.Fatalf("leases written: %d, %v; want 3", leased, err)
	}
	if again, _ := store.ClaimRunEvents(ctx, env.ORM, now, lease, 3); fmt.Sprint(runEventIDs(again)) != fmt.Sprint([]string{ids[5]}) {
		t.Fatalf("a second claim took %v, want only the unleased %s", runEventIDs(again), ids[5])
	}

	// Reset the leases and let the twin claim the same state.
	mustExec(t, env, `UPDATE run_events SET claimed_until = $1 WHERE id = ANY($2)`, now.Add(-time.Minute), orm.StringArray(append(want, ids[5])))
	twin, err := env.Repo.ClaimRunEvents(ctx, now, lease, 3)
	if err != nil {
		t.Fatalf("twin claim: %v", err)
	}
	if g := runEventIDs(twin); fmt.Sprint(g) != fmt.Sprint(want) {
		t.Fatalf("twin claimed %v, want %v", g, want)
	}
	if none, err := store.ClaimRunEvents(ctx, env.ORM, now, lease, 0); none != nil || err != nil {
		t.Fatalf("max 0 = %v, %v; want nothing", none, err)
	}
}

// TestClaimRunEventsConcurrentClaimersAreDisjoint: eight relays drain one
// outbox at once, half through the port and half through the previous
// release's statement; SKIP LOCKED must hand every row to exactly one.
func TestClaimRunEventsConcurrentClaimersAreDisjoint(t *testing.T) {
	env := storetest.New(t)
	ids := seedRunEvents(t, env, "cc", 120)
	now, lease := l2Base.Add(time.Hour), l2Base.Add(2*time.Hour)

	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for {
				var claimed []string
				if w%2 == 0 {
					rows, err := store.ClaimRunEvents(context.Background(), env.ORM, now, lease, 7)
					if err != nil {
						t.Errorf("port claimer %d: %v", w, err)
						return
					}
					claimed = runEventIDs(rows)
				} else {
					var err error
					if claimed, err = previousReleaseClaim(env, now, lease, 7); err != nil {
						t.Errorf("previous-release claimer %d: %v", w, err)
						return
					}
				}
				if len(claimed) == 0 {
					return
				}
				mu.Lock()
				for _, id := range claimed {
					seen[id]++
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	for _, id := range ids {
		if seen[id] != 1 {
			t.Fatalf("%s claimed %d times, want exactly once", id, seen[id])
		}
	}
}
