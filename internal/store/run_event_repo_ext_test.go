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

// seedRunEvents writes n outbox rows created one second apart, so ORDER BY
// created_at is total, with ids "<prefix>-0000", "<prefix>-0001", ….
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
		if ok, err := store.InsertRunEvent(ctx, env.ORM, ev); err != nil || !ok {
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

// TestRunEventLifecycle covers the outbox row's life: insert with dedupe, the
// two reads, a nil payload and a zero CreatedAt, dispatch and prune.
func TestRunEventLifecycle(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	ids := seedRunEvents(t, env, "rw", 4)

	wantEvent := func(i int) *core.RunEvent {
		return &core.RunEvent{
			ID: ids[i], UserID: storetest.SeedUserID, ChatID: "chat-re-rw", WorkflowName: "wf",
			Outcome: core.RunEventFinished, DedupeKey: ids[i], Payload: map[string]any{"i": float64(i)},
			OccurredAt: l2Base, CreatedAt: l2Base.Add(time.Duration(i) * time.Second),
		}
	}
	for i, id := range ids {
		byID, err := store.LoadRunEvent(ctx, env.ORM, id)
		if err != nil {
			t.Fatal(err)
		}
		requireSame(t, "event "+id, byID, wantEvent(i))
		byKey, err := store.LoadRunEventByDedupe(ctx, env.ORM, id)
		if err != nil {
			t.Fatal(err)
		}
		requireSame(t, "by dedupe "+id, byKey, wantEvent(i))
	}

	// A retried transition is a no-op that leaves the first row as the record.
	dup := &core.RunEvent{ID: "other-id", UserID: storetest.SeedUserID, ChatID: "chat-re-rw",
		Outcome: core.RunEventFailed, DedupeKey: ids[0], OccurredAt: l2Base}
	if created, err := store.InsertRunEvent(ctx, env.ORM, dup); err != nil || created {
		t.Fatalf("re-insert of a dedupe key: created=%v err=%v; want false, nil", created, err)
	}
	if _, err := store.LoadRunEvent(ctx, env.ORM, "other-id"); !errors.Is(err, core.ErrRunEventNotFound) {
		t.Fatalf("the retried insert left a row behind: %v", err)
	}
	kept, err := store.LoadRunEventByDedupe(ctx, env.ORM, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	requireSame(t, "the first row stays the record", kept, wantEvent(0))

	if _, err := store.LoadRunEvent(ctx, env.ORM, "missing"); !errors.Is(err, core.ErrRunEventNotFound) {
		t.Fatalf("LoadRunEvent(missing) = %v, want ErrRunEventNotFound", err)
	}
	if _, err := store.LoadRunEventByDedupe(ctx, env.ORM, "missing"); !errors.Is(err, core.ErrRunEventNotFound) {
		t.Fatalf("LoadRunEventByDedupe(missing) = %v, want ErrRunEventNotFound", err)
	}

	// A nil payload is stored as the empty object, and a zero CreatedAt is now.
	bare := &core.RunEvent{ID: "bare", UserID: storetest.SeedUserID, ChatID: "chat-re-rw",
		Outcome: core.RunEventBlocked, DedupeKey: "bare", OccurredAt: l2Base}
	if created, err := store.InsertRunEvent(ctx, env.ORM, bare); err != nil || !created {
		t.Fatalf("insert bare: created=%v err=%v", created, err)
	}
	var stored string
	if err := env.SQL.QueryRow(`SELECT payload::text FROM run_events WHERE id = 'bare'`).Scan(&stored); err != nil || stored != "{}" {
		t.Fatalf("stored payload = %q, %v; want {}", stored, err)
	}
	gotBare, err := store.LoadRunEvent(ctx, env.ORM, "bare")
	if err != nil {
		t.Fatal(err)
	}
	if gotBare.Payload == nil || len(gotBare.Payload) != 0 {
		t.Fatalf("bare payload = %#v, want an empty non-nil map", gotBare.Payload)
	}
	requireRecent(t, "bare CreatedAt", gotBare.CreatedAt)

	// Dispatch: the stamp is the caller's, the last one wins, a missing id is a no-op.
	now := l2Base.Add(time.Hour)
	if err := store.MarkRunEventDispatched(ctx, env.ORM, ids[0], now); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunEventDispatched(ctx, env.ORM, ids[1], now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunEventDispatched(ctx, env.ORM, ids[1], now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunEventDispatched(ctx, env.ORM, "missing", now); err != nil {
		t.Fatalf("marking a missing event is a no-op: %v", err)
	}
	for id, want := range map[string]*time.Time{
		ids[0]: &now, ids[1]: ptr(now.Add(2 * time.Minute)), ids[2]: nil, ids[3]: nil,
	} {
		ev, err := store.LoadRunEvent(ctx, env.ORM, id)
		if err != nil {
			t.Fatal(err)
		}
		requireSame(t, id+" dispatched_at", ev.DispatchedAt, want)
	}

	// Prune: only dispatched events strictly before the cutoff go.
	prune := func(cutoff time.Time, want int64) {
		t.Helper()
		n, err := store.DeleteDispatchedRunEventsBefore(ctx, env.ORM, cutoff)
		if err != nil || n != want {
			t.Fatalf("prune before %v = %d, %v; want %d", cutoff, n, err, want)
		}
	}
	prune(now, 0) // ids[0] was dispatched AT the cutoff: not before it
	prune(now.Add(30*time.Second), 1)
	prune(now.Add(30*time.Second), 0) // nothing left to prune
	prune(now.Add(2*time.Minute), 0)  // ids[1] again sits exactly at the cutoff
	prune(now.Add(3*time.Minute), 1)
	prune(now.Add(24*time.Hour), 0) // the undispatched ones never go
	var left []string
	rows, err := env.SQL.Query(`SELECT id FROM run_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		left = append(left, id)
	}
	if want := []string{"bare", ids[2], ids[3]}; fmt.Sprint(left) != fmt.Sprint(want) {
		t.Fatalf("rows left after pruning = %v, want %v", left, want)
	}
}

// TestRunEventEmitterQueries covers the reads the emitter makes before
// writing a blocked event.
func TestRunEventEmitterQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedChat(t, env, "chat-blk")
	seedChat(t, env, "chat-elsewhere")

	check := func(kind core.TriggerKind, want bool) {
		t.Helper()
		got, err := store.HasEnabledTriggerOfKind(ctx, env.ORM, storetest.SeedUserID, kind)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("HasEnabledTriggerOfKind(%s) = %v, want %v", kind, got, want)
		}
	}
	check(core.TriggerKindWorkflowEvent, false) // no triggers at all
	mustExec(t, env, `INSERT INTO triggers (id, user_id, project_id, name, kind, workflow, enabled)
		VALUES ('trg-off', $1, $2, 'off', 'workflow_event', 'wf', false)`, storetest.SeedUserID, storetest.SeedProjectID)
	check(core.TriggerKindWorkflowEvent, false) // disabled
	mustExec(t, env, `INSERT INTO triggers (id, user_id, project_id, name, kind, workflow)
		VALUES ('trg-on', 'someone-else', $1, 'on', 'workflow_event', 'wf')`, storetest.SeedProjectID)
	check(core.TriggerKindWorkflowEvent, false) // another user's
	mustExec(t, env, `INSERT INTO triggers (id, user_id, project_id, name, kind, workflow)
		VALUES ('trg-mine', $1, $2, 'mine', 'workflow_event', 'wf')`, storetest.SeedUserID, storetest.SeedProjectID)
	check(core.TriggerKindWorkflowEvent, true)
	check(core.TriggerKindSchedule, false) // another kind

	count := func(chat, exclude string, want int) {
		t.Helper()
		got, err := store.CountOtherPendingBlockers(ctx, env.ORM, chat, exclude)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("CountOtherPendingBlockers(%s, exclude %q) = %d, want %d", chat, exclude, got, want)
		}
	}
	count("chat-blk", "", 0)
	mustExec(t, env, `INSERT INTO approvals (id, chat_id, approval_type, entity_id, status, title, created_at)
		VALUES ('ap-1', 'chat-blk', 1, 'e-1', 1, 't', now()), ('ap-done', 'chat-blk', 1, 'e-2', 2, 't', now()),
		       ('ap-elsewhere', 'chat-elsewhere', 1, 'e-3', 1, 't', now())`)
	mustExec(t, env, `INSERT INTO questions (id, chat_id, workflow_id) VALUES ('q-1', 'chat-blk', 'wf')`)
	count("chat-blk", "", 2)           // the pending approval and the question; not the answered one, not the other chat's
	count("chat-blk", "ap-1", 1)       // excluding the approval being created
	count("chat-blk", "q-1", 1)        // excluding the question being created
	count("chat-blk", "no-such-id", 2) // excluding something that is not there
	count("chat-elsewhere", "", 1)     // blockers are per chat
	count("chat-none", "", 0)

	lockTwice := env.ORM.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		if err := store.LockChatForRunEvent(ctx, env.ORM, "chat-blk"); err != nil {
			return err
		}
		return store.LockChatForRunEvent(ctx, env.ORM, "chat-blk") // the same transaction: re-entrant
	})
	if lockTwice != nil {
		t.Fatalf("lock chat twice in one transaction: %v", lockTwice)
	}
	err := env.ORM.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		return store.LockChatForRunEvent(ctx, env.ORM, "missing")
	})
	if !errors.Is(err, core.ErrChatNotFound) {
		t.Fatalf("lock of a missing chat = %v, want ErrChatNotFound", err)
	}
}

// TestLockChatForRunEventBlocksOtherTransactions: the row lock one transaction
// takes holds a second transaction's lock on the same chat until it commits.
func TestLockChatForRunEventBlocksOtherTransactions(t *testing.T) {
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
		_ = env.ORM.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
			if err := store.LockChatForRunEvent(ctx, env.ORM, "chat-rowlock"); err != nil {
				t.Errorf("second lock: %v", err)
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
		t.Fatalf("second transaction took the row lock at %v, before the holder committed at %v", at, releasedAt)
	}
}

// previousReleaseClaimSQL is the outbox claim exactly as the release before
// this port sends it. During a rolling deploy both run against one outbox, so
// the port's claim must stay disjoint from it.
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

// TestClaimRunEvents: on a queue holding a dispatched event, one leased into
// the future, one whose lease expired, one leased exactly now, and unclaimed
// ones (two of them tied on created_at), a claim takes the claimable rows
// oldest first, capped at max, and leases them.
func TestClaimRunEvents(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	ids := seedRunEvents(t, env, "ev", 8)
	// ids[6] and ids[7] share a created_at; the id breaks the tie.
	mustExec(t, env, `UPDATE run_events SET created_at = $1 WHERE id = ANY($2)`, l2Base.Add(6*time.Second), orm.StringArray{ids[6], ids[7]})

	now := l2Base.Add(time.Hour)
	mustExec(t, env, `UPDATE run_events SET dispatched_at = $1 WHERE id = $2`, now, ids[0])
	mustExec(t, env, `UPDATE run_events SET claimed_until = $1 WHERE id = $2`, now.Add(time.Minute), ids[1])  // leased
	mustExec(t, env, `UPDATE run_events SET claimed_until = $1 WHERE id = $2`, now.Add(-time.Minute), ids[2]) // lease expired
	mustExec(t, env, `UPDATE run_events SET claimed_until = $1 WHERE id = $2`, now, ids[3])                   // lease ends exactly now: still held

	lease := now.Add(30 * time.Second)
	claim := func(max int) []string {
		t.Helper()
		rows, err := store.ClaimRunEvents(ctx, env.ORM, now, lease, max)
		if err != nil {
			t.Fatalf("claim(%d): %v", max, err)
		}
		ids := make([]string, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
			if r.ChatID != "chat-re-ev" || r.Outcome != core.RunEventFinished || r.DispatchedAt != nil {
				t.Fatalf("claimed row %+v is not the stored event", r)
			}
		}
		sort.Strings(ids)
		return ids
	}
	leasedCount := func() int {
		t.Helper()
		var n int
		if err := env.SQL.QueryRow(`SELECT count(*) FROM run_events WHERE claimed_until = $1`, lease).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if got := claim(0); len(got) != 0 {
		t.Fatalf("max 0 claimed %v, want nothing", got)
	}
	if got := claim(-1); len(got) != 0 {
		t.Fatalf("max -1 claimed %v, want nothing", got)
	}
	if n := leasedCount(); n != 0 {
		t.Fatalf("a claim of nothing leased %d rows", n)
	}

	// Oldest first among the claimable: ids[2] (expired lease), ids[4], ids[5].
	if got, want := claim(3), []string{ids[2], ids[4], ids[5]}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("first claim = %v, want %v", got, want)
	}
	if n := leasedCount(); n != 3 {
		t.Fatalf("leases written: %d, want 3", n)
	}
	// The tied pair is next; the id orders them, and max 1 takes only the first.
	if got, want := claim(1), []string{ids[6]}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("claim of the tie = %v, want %v", got, want)
	}
	if got, want := claim(10), []string{ids[7]}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("last claim = %v, want %v", got, want)
	}
	if got := claim(10); len(got) != 0 {
		t.Fatalf("a drained queue claimed %v", got)
	}

	// The untouched rows kept their state.
	var dispatched, held int
	if err := env.SQL.QueryRow(`SELECT count(*) FILTER (WHERE dispatched_at IS NOT NULL), count(*) FILTER (WHERE claimed_until <> $1) FROM run_events`, lease).Scan(&dispatched, &held); err != nil {
		t.Fatal(err)
	}
	if dispatched != 1 || held != 2 {
		t.Fatalf("dispatched=%d other leases=%d, want 1 and 2 (ids[1], ids[3])", dispatched, held)
	}

	// Once a lease runs out the event is claimable again.
	later := now.Add(2 * time.Minute) // past ids[1]'s lease as well as the new one
	rows, err := store.ClaimRunEvents(ctx, env.ORM, later, later.Add(time.Minute), 100)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := runEventIDs(rows), []string{ids[1], ids[2], ids[3], ids[4], ids[5], ids[6], ids[7]}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("claim after the leases lapsed = %v, want %v", got, want)
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
