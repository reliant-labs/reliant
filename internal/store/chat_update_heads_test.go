// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// chat_update_heads is the snapshot's update heads, materialized in the
// transaction of every log write (dev-docs/design/chat-update-heads.md). The
// property every test here holds is the one the snapshot depends on: when
// SnapshotUpdateHeads serves from the table, it returns exactly what
// SnapshotHeadsSQL computes from the log, row for row; when it cannot vouch
// for the table, it reads the log.

// requireSameHeads fails unless got and want are the same heads in the same
// order, every column equal.
func requireSameHeads(t *testing.T, label string, got, want []core.ChatUpdate) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d heads, the log has %d\n got: %v\nwant: %v", label, len(got), len(want), headSeqs(got), headSeqs(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.ID != w.ID || g.ChatID != w.ChatID || g.SequenceNumber != w.SequenceNumber || g.UpdateType != w.UpdateType ||
			g.EntityID != w.EntityID || string(g.Data) != string(w.Data) || !g.CreatedAt.Equal(w.CreatedAt) {
			t.Fatalf("%s: head %d is %+v, the log's is %+v", label, i, g, w)
		}
	}
}

func headSeqs(us []core.ChatUpdate) []int64 {
	seqs := make([]int64, len(us))
	for i, u := range us {
		seqs[i] = u.SequenceNumber
	}
	return seqs
}

// requireHeadsFromTable asserts the chat's heads are served from the table
// and equal the log's.
func requireHeadsFromTable(t *testing.T, env *storetest.Env, chatID, label string) {
	t.Helper()
	ctx := context.Background()
	got, source, err := store.SnapshotUpdateHeads(ctx, env.ORM, chatID)
	if err != nil {
		t.Fatalf("%s: SnapshotUpdateHeads: %v", label, err)
	}
	if source != store.HeadsSourceTable {
		t.Fatalf("%s: heads served from %q, want %q", label, source, store.HeadsSourceTable)
	}
	want, err := store.LatestNonMessageUpdatesPerEntity(ctx, env.ORM, chatID)
	if err != nil {
		t.Fatalf("%s: LatestNonMessageUpdatesPerEntity: %v", label, err)
	}
	requireSameHeads(t, label, got, want)
}

// requireHeadsFromLog asserts the chat's heads fall back to the log.
func requireHeadsFromLog(t *testing.T, env *storetest.Env, chatID, label string) {
	t.Helper()
	ctx := context.Background()
	got, source, err := store.SnapshotUpdateHeads(ctx, env.ORM, chatID)
	if err != nil {
		t.Fatalf("%s: SnapshotUpdateHeads: %v", label, err)
	}
	if source != store.HeadsSourceLogFallback {
		t.Fatalf("%s: heads served from %q, want %q", label, source, store.HeadsSourceLogFallback)
	}
	want, err := store.LatestNonMessageUpdatesPerEntity(ctx, env.ORM, chatID)
	if err != nil {
		t.Fatalf("%s: LatestNonMessageUpdatesPerEntity: %v", label, err)
	}
	requireSameHeads(t, label, got, want)
}

// headsWorkload draws random updates over every update type, with entity ids
// and payloads chosen to collide: questions that re-key onto one another,
// thread payloads with and without `thread` (and with an empty one), and
// entity ids equal to a re-key key ("thread:th-a", "question-q1"), which
// SnapshotHeadsSQL keeps as separate heads.
type headsWorkload struct {
	rng *rand.Rand
	n   int
}

var (
	headsTypes = func() []reliantv1.ChatUpdateType {
		var ts []reliantv1.ChatUpdateType
		for v := range reliantv1.ChatUpdateType_name {
			if v != 0 {
				ts = append(ts, reliantv1.ChatUpdateType(v))
			}
		}
		// Weight the re-keyed types: their keys are the subtle ones.
		for i := 0; i < 4; i++ {
			ts = append(ts, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION)
		}
		return ts
	}()
	headsEntities = []string{
		"ap-1", "ap-2", "wf-1", "wf-2", "thread:th-a", "question-q1",
		"question-q1-100", "question-q1-200", "question-q2-300", "nodash",
	}
	headsThreadPayloads = []string{
		`{"thread":"th-a"}`, `{"thread":"th-b"}`, `{"thread":""}`, `{}`, `{"thread":"wf-1"}`, `{"thread":"th-a","status":"done"}`,
	}
)

func (w *headsWorkload) next() (reliantv1.ChatUpdateType, string, string) {
	w.n++
	typ := headsTypes[w.rng.Intn(len(headsTypes))]
	entity := headsEntities[w.rng.Intn(len(headsEntities))]
	data := fmt.Sprintf(`{"n":%d}`, w.n)
	if typ == reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD {
		data = headsThreadPayloads[w.rng.Intn(len(headsThreadPayloads))]
	}
	return typ, entity, data
}

// TestChatUpdateHeads_MatchTheLog is the differential test: randomized update
// sequences, written through every writer this release has (Repo.
// CreateChatUpdate, store.AppendChatUpdate in a transaction of the caller's,
// SaveMessageAtomic for messages and for a head type), checked
// against SnapshotHeadsSQL after every few writes.
func TestChatUpdateHeads_MatchTheLog(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	for seed := int64(1); seed <= 4; seed++ {
		chatID := fmt.Sprintf("chat-heads-diff-%d", seed)
		cw := seedBenchChat(t, env, chatID)
		w := &headsWorkload{rng: rand.New(rand.NewSource(seed))}
		for i := 0; i < 120; i++ {
			typ, entity, data := w.next()
			var err error
			switch w.rng.Intn(4) {
			case 0:
				err = env.Repo.CreateChatUpdate(ctx, chatID, typ, entity, data)
			case 1:
				err = env.ORM.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
					_, err := store.AppendChatUpdate(ctx, env.ORM, chatID, typ, entity, data)
					return err
				})
			case 2:
				write := benchWrite(chatID, cw, fmt.Sprintf("%s-m%d", chatID, i), 1)
				_, err = env.Repo.SaveMessageAtomic(ctx, write)
			default:
				// The message writer with a head type: it must apply the
				// head as AppendChatUpdate does.
				write := benchWrite(chatID, cw, fmt.Sprintf("%s-m%d", chatID, i), 1)
				write.ChatUpdateType, write.ChatUpdateEntity = typ, entity
				write.ChatUpdateData = func(int64, int64, int64) (string, error) { return data, nil }
				_, err = env.Repo.SaveMessageAtomic(ctx, write)
			}
			if err != nil {
				t.Fatalf("seed %d write %d (%v %s %s): %v", seed, i, typ, entity, data, err)
			}
			if i%10 == 9 {
				requireHeadsFromTable(t, env, chatID, fmt.Sprintf("seed %d after %d writes", seed, i+1))
			}
		}
		requireHeadsFromTable(t, env, chatID, fmt.Sprintf("seed %d", seed))
	}
}

// TestChatUpdateHeads_ConcurrentWritersMatchTheLog: writers racing on one
// chat (the counter row serializes them) leave
// the same heads the log does.
func TestChatUpdateHeads_ConcurrentWritersMatchTheLog(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	const chatID = "chat-heads-race"
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			w := &headsWorkload{rng: rand.New(rand.NewSource(int64(100 + g)))}
			for i := 0; i < 15; i++ {
				typ, entity, data := w.next()
				if err := env.Repo.CreateChatUpdate(ctx, chatID, typ, entity, data); err != nil {
					errs <- fmt.Errorf("writer %d write %d: %w", g, i, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	requireHeadsFromTable(t, env, chatID, "after the race")
}

// TestChatUpdateHeads_KeepTheNewestWhateverTheOrder: a head only ever moves to
// a higher sequence. The backfill and a live writer can apply rows in either
// order; an older row arriving second must not replace the newer.
func TestChatUpdateHeads_KeepTheNewestWhateverTheOrder(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	const chatID = "chat-heads-order"
	at := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	row := func(seq int64, data string) *store.ChatUpdate {
		return &store.ChatUpdate{
			ID: fmt.Sprintf("%s-%d", chatID, seq), ChatID: chatID, SequenceNumber: seq,
			UpdateType: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION,
			EntityID:   fmt.Sprintf("question-q1-%d", seq), Data: data, CreatedAt: at.Add(time.Duration(seq) * time.Second),
		}
	}
	for _, r := range []*store.ChatUpdate{row(7, `{"s":"resolved"}`), row(3, `{"s":"pending"}`), row(7, `{"s":"resolved"}`)} {
		if err := store.ApplyChatUpdateHead(ctx, env.ORM, r); err != nil {
			t.Fatal(err)
		}
	}
	var seq int64
	var data, key string
	if err := env.SQL.QueryRowContext(ctx,
		`SELECT head_key, sequence_number, data FROM chat_update_heads WHERE chat_id = $1`, chatID,
	).Scan(&key, &seq, &data); err != nil {
		t.Fatal(err)
	}
	if key != "r:question-q1" || seq != 7 || data != `{"s":"resolved"}` {
		t.Fatalf("head = %s seq %d %s; want r:question-q1 at 7, resolved", key, seq, data)
	}
}

// previousReleaseWrite is a chat update written exactly as the previous
// release writes one: its allocation statement, which knows nothing of the
// heads watermark, and the log insert, with no head.
func previousReleaseWrite(t *testing.T, env *storetest.Env, chatID string, typ reliantv1.ChatUpdateType, entity, data string) {
	t.Helper()
	if err := previousReleaseWriteErr(env, chatID, typ, entity, data); err != nil {
		t.Fatal(err)
	}
}

// previousReleaseWriteErr is previousReleaseWrite for a goroutine, which may
// not call t.Fatal.
func previousReleaseWriteErr(env *storetest.Env, chatID string, typ reliantv1.ChatUpdateType, entity, data string) error {
	ctx := context.Background()
	tx, err := env.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var seq int64
	if err := tx.QueryRowContext(ctx, `
INSERT INTO update_stream_counters (stream_kind, stream_id, last_assigned_seq)
VALUES ($1, $2, 1)
ON CONFLICT (stream_kind, stream_id) DO UPDATE
SET last_assigned_seq = update_stream_counters.last_assigned_seq + 1
RETURNING last_assigned_seq
`, "chat", chatID).Scan(&seq); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO chat_updates (id, chat_id, sequence_number, update_type, entity_id, data, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		fmt.Sprintf("%s-%d", chatID, seq), chatID, seq, int32(typ), entity, data, time.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

// TestChatUpdateHeads_PreviousReleaseWritesFallBackToTheLog: during a rolling
// deploy or after a rollback, the previous release writes updates and no
// heads. The first such write must stop the table being trusted for that
// chat, for good — even though this release's later writes maintain the
// heads again — because the head that write carried is missing from it.
func TestChatUpdateHeads_PreviousReleaseWritesFallBackToTheLog(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	ap := reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_APPROVAL

	// A chat the previous release created: incomplete from its first update.
	const old = "chat-heads-old"
	previousReleaseWrite(t, env, old, ap, "ap-1", `{"v":1}`)
	requireHeadsFromLog(t, env, old, "chat created by the previous release")
	if err := env.Repo.CreateChatUpdate(ctx, old, ap, "ap-1", `{"v":2}`); err != nil {
		t.Fatal(err)
	}
	requireHeadsFromLog(t, env, old, "this release writing to it afterwards")

	// A chat this release created is served from the table until the previous
	// release writes to it once (here: answering the question).
	const mixed = "chat-heads-mixed"
	q := reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION
	if err := env.Repo.CreateChatUpdate(ctx, mixed, q, "question-q1-100", `{"s":"pending"}`); err != nil {
		t.Fatal(err)
	}
	requireHeadsFromTable(t, env, mixed, "chat created by this release")
	previousReleaseWrite(t, env, mixed, q, "question-q1-200", `{"s":"resolved"}`)
	requireHeadsFromLog(t, env, mixed, "after the previous release answered the question")
	if err := env.Repo.CreateChatUpdate(ctx, mixed, ap, "ap-9", `{}`); err != nil {
		t.Fatal(err)
	}
	requireHeadsFromLog(t, env, mixed, "this release writing again")

	got, _, err := store.SnapshotUpdateHeads(ctx, env.ORM, mixed)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range got {
		if u.UpdateType == q && string(u.Data) != `{"s":"resolved"}` {
			t.Fatalf("the snapshot shows the question %s; the previous release resolved it", u.Data)
		}
	}

	// A chat with no updates at all has no counter: the log answers, empty.
	requireHeadsFromLog(t, env, "chat-heads-none", "chat with no updates")
}

// TestChatUpdateHeads_RolledBackWritesLeaveNoHead: the head is written in the
// log row's transaction, so a write that rolls back leaves neither.
func TestChatUpdateHeads_RolledBackWritesLeaveNoHead(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	const chatID = "chat-heads-rollback"
	ap := reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_APPROVAL
	if err := env.Repo.CreateChatUpdate(ctx, chatID, ap, "ap-1", `{"v":1}`); err != nil {
		t.Fatal(err)
	}
	boom := fmt.Errorf("caller failed after the write")
	if err := env.Repo.RunTx(ctx, orm.Serializable.WithMaxAttempts(1), func(ctx context.Context) error {
		if err := env.Repo.CreateChatUpdate(ctx, chatID, ap, "ap-1", `{"v":2}`); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("RunTx = %v, want the caller's error", err)
	}
	requireHeadsFromTable(t, env, chatID, "after a rolled-back write")
	got, _, err := store.SnapshotUpdateHeads(ctx, env.ORM, chatID)
	if err != nil || len(got) != 1 || string(got[0].Data) != `{"v":1}` {
		t.Fatalf("heads = %+v, %v; want ap-1 at v1", got, err)
	}
}

// TestChatUpdateHeads_BackfillCatchesUpTheLog: a chat whose history the
// previous release wrote (the log only) falls back to the log until it is
// backfilled, and is then served from the table with exactly the log's heads.
// A second backfill has nothing to do.
func TestChatUpdateHeads_BackfillCatchesUpTheLog(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	for seed := int64(1); seed <= 3; seed++ {
		chatID := fmt.Sprintf("chat-heads-backfill-%d", seed)
		w := &headsWorkload{rng: rand.New(rand.NewSource(seed))}
		for i := 0; i < 80; i++ {
			typ, entity, data := w.next()
			if w.rng.Intn(3) == 0 {
				if err := env.Repo.CreateChatUpdate(ctx, chatID, typ, entity, data); err != nil {
					t.Fatal(err)
				}
				continue
			}
			previousReleaseWrite(t, env, chatID, typ, entity, data)
		}
		requireHeadsFromLog(t, env, chatID, fmt.Sprintf("seed %d before the backfill", seed))

		did, err := store.BackfillChatUpdateHeads(ctx, env.ORM, chatID)
		if err != nil || !did {
			t.Fatalf("seed %d: BackfillChatUpdateHeads = %v, %v; want it to backfill", seed, did, err)
		}
		requireHeadsFromTable(t, env, chatID, fmt.Sprintf("seed %d after the backfill", seed))
		if did, err := store.BackfillChatUpdateHeads(ctx, env.ORM, chatID); err != nil || did {
			t.Fatalf("seed %d: second BackfillChatUpdateHeads = %v, %v; want nothing to do", seed, did, err)
		}

		// This release's writes keep it complete from here.
		for i := 0; i < 20; i++ {
			typ, entity, data := w.next()
			if err := env.Repo.CreateChatUpdate(ctx, chatID, typ, entity, data); err != nil {
				t.Fatal(err)
			}
		}
		requireHeadsFromTable(t, env, chatID, fmt.Sprintf("seed %d after more writes", seed))
	}
	if did, err := store.BackfillChatUpdateHeads(ctx, env.ORM, "chat-heads-no-counter"); err != nil || did {
		t.Fatalf("BackfillChatUpdateHeads(no counter) = %v, %v; want nothing to do", did, err)
	}
}

// TestChatUpdateHeads_BackfillBesideLiveWriters: writes of both releases race
// the backfill. Whatever it interleaved with, once the writers stop, one more
// backfill leaves the chat complete, with exactly the log's heads: no head a
// racing writer applied, or that the backfill applied around it, is lost or
// left stale.
func TestChatUpdateHeads_BackfillBesideLiveWriters(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	const chatID = "chat-heads-backfill-race"
	seedW := &headsWorkload{rng: rand.New(rand.NewSource(7))}
	for i := 0; i < 60; i++ {
		typ, entity, data := seedW.next()
		previousReleaseWrite(t, env, chatID, typ, entity, data)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			w := &headsWorkload{rng: rand.New(rand.NewSource(int64(200 + g)))}
			for i := 0; i < 20; i++ {
				typ, entity, data := w.next()
				if g == 0 && i%5 == 4 {
					if err := previousReleaseWriteErr(env, chatID, typ, entity, data); err != nil {
						errs <- err
						return
					}
					continue
				}
				if err := env.Repo.CreateChatUpdate(ctx, chatID, typ, entity, data); err != nil {
					errs <- err
					return
				}
			}
		}(g)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			if _, err := store.BackfillChatUpdateHeads(ctx, env.ORM, chatID); err != nil {
				errs <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if _, err := store.BackfillChatUpdateHeads(ctx, env.ORM, chatID); err != nil {
		t.Fatal(err)
	}
	requireHeadsFromTable(t, env, chatID, "after the writers stopped and a final backfill")
}

// TestChatsWithIncompleteHeads: the backfill's work list is every chat whose
// counter does not vouch for its heads, in id order, resumable from an id.
func TestChatsWithIncompleteHeads(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	ap := reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_APPROVAL
	for _, c := range []string{"chat-inc-a", "chat-inc-c", "chat-inc-e"} {
		previousReleaseWrite(t, env, c, ap, "ap-1", `{}`)
	}
	for _, c := range []string{"chat-inc-b", "chat-inc-d"} {
		if err := env.Repo.CreateChatUpdate(ctx, c, ap, "ap-1", `{}`); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.ChatsWithIncompleteHeads(ctx, env.ORM, "chat-inc-", 2)
	if err != nil || fmt.Sprint(got) != "[chat-inc-a chat-inc-c]" {
		t.Fatalf("first page = %v, %v", got, err)
	}
	got, err = store.ChatsWithIncompleteHeads(ctx, env.ORM, got[len(got)-1], 2)
	if err != nil || fmt.Sprint(got) != "[chat-inc-e]" {
		t.Fatalf("second page = %v, %v", got, err)
	}
	if _, err := store.BackfillChatUpdateHeads(ctx, env.ORM, "chat-inc-c"); err != nil {
		t.Fatal(err)
	}
	got, err = store.ChatsWithIncompleteHeads(ctx, env.ORM, "chat-inc-", 10)
	if err != nil || fmt.Sprint(got) != "[chat-inc-a chat-inc-e]" {
		t.Fatalf("after backfilling c = %v, %v", got, err)
	}
}
