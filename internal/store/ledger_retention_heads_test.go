// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// Compaction on top of chat_update_heads (dev-docs/design/chat-update-heads.md,
// "Retention of chat_updates"): compaction keeps every row SnapshotHeadsSQL
// reads, so it never depends on the log fallback being retired. Every head a
// complete chat's table points at survives, a complete chat is served from the table with the
// log's heads, an incomplete chat's log fallback reads the same heads, and a
// backfill after compaction builds the heads it would have built before.

// ageChatAndHeads backdates a chat's updates at or below seq, and the heads
// that copy them, to the aged instant.
func ageChatAndHeads(t *testing.T, env *storetest.Env, chatID string, seq int64) {
	t.Helper()
	ageChatUpdates(t, env, chatID, seq, retentionAged)
	mustExec(t, env, `UPDATE chat_update_heads SET created_at = $1 WHERE chat_id = $2 AND sequence_number <= $3`, retentionAged, chatID, seq)
}

// requireHeadRowsInLog fails if a chat_update_heads row points at a log row
// compaction deleted.
func requireHeadRowsInLog(t *testing.T, env *storetest.Env, chatID, label string) {
	t.Helper()
	var missing int
	if err := env.SQL.QueryRowContext(context.Background(), `
SELECT count(*) FROM chat_update_heads h
 WHERE h.chat_id = $1
   AND NOT EXISTS (SELECT 1 FROM chat_updates u WHERE u.id = h.update_id)`, chatID).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing != 0 {
		t.Fatalf("%s: %d heads point at a deleted log row", label, missing)
	}
}

func TestCompactChatUpdates_KeepsEveryUpdateHead(t *testing.T) {
	env, ctx := storetest.New(t), context.Background()
	var deleted int64
	for seed := int64(1); seed <= 3; seed++ {
		w := &headsWorkload{rng: rand.New(rand.NewSource(seed))}

		// A chat this release wrote: complete, served from the table.
		complete := fmt.Sprintf("chat-compact-heads-%d", seed)
		for i := 0; i < 150; i++ {
			typ, entity, data := w.next()
			if err := env.Repo.CreateChatUpdate(ctx, complete, typ, entity, data); err != nil {
				t.Fatal(err)
			}
		}
		ageChatAndHeads(t, env, complete, 140)
		requireHeadsFromTable(t, env, complete, fmt.Sprintf("seed %d before compaction", seed))
		before, _, err := store.SnapshotUpdateHeads(ctx, env.ORM, complete)
		if err != nil {
			t.Fatal(err)
		}

		// A chat the previous release also wrote: incomplete, served from the
		// log until a backfill.
		mixed := fmt.Sprintf("chat-compact-heads-mixed-%d", seed)
		for i := 0; i < 150; i++ {
			typ, entity, data := w.next()
			if w.rng.Intn(3) == 0 {
				if err := env.Repo.CreateChatUpdate(ctx, mixed, typ, entity, data); err != nil {
					t.Fatal(err)
				}
				continue
			}
			previousReleaseWrite(t, env, mixed, typ, entity, data)
		}
		ageChatAndHeads(t, env, mixed, 140)
		requireHeadsFromLog(t, env, mixed, fmt.Sprintf("seed %d mixed before compaction", seed))
		mixedBefore, err := store.LatestNonMessageUpdatesPerEntity(ctx, env.ORM, mixed)
		if err != nil {
			t.Fatal(err)
		}

		res, err := env.Repo.CompactChatUpdates(ctx, retentionCutoff, 0)
		if err != nil {
			t.Fatal(err)
		}
		deleted += res.Deleted

		// Only a complete chat's heads are vouched for. An incomplete chat's
		// table can hold a head a previous-release write superseded without
		// touching the table; compaction may delete the log row it points at.
		// The table is never served for that chat, and the backfill below
		// replaces the head with the newer one the log kept.
		requireHeadRowsInLog(t, env, complete, fmt.Sprintf("seed %d", seed))
		requireHeadsFromTable(t, env, complete, fmt.Sprintf("seed %d after compaction", seed))
		after, _, err := store.SnapshotUpdateHeads(ctx, env.ORM, complete)
		if err != nil {
			t.Fatal(err)
		}
		requireSameHeads(t, fmt.Sprintf("seed %d table heads across compaction", seed), after, before)

		requireHeadsFromLog(t, env, mixed, fmt.Sprintf("seed %d mixed after compaction", seed))
		mixedAfter, err := store.LatestNonMessageUpdatesPerEntity(ctx, env.ORM, mixed)
		if err != nil {
			t.Fatal(err)
		}
		requireSameHeads(t, fmt.Sprintf("seed %d log fallback across compaction", seed), mixedAfter, mixedBefore)

		// The backfill reads the compacted log and still builds the heads the
		// uncompacted log had.
		if did, err := store.BackfillChatUpdateHeads(ctx, env.ORM, mixed); err != nil || !did {
			t.Fatalf("seed %d: BackfillChatUpdateHeads = %v, %v; want it to backfill", seed, did, err)
		}
		requireHeadsFromTable(t, env, mixed, fmt.Sprintf("seed %d mixed after backfill", seed))
		requireHeadRowsInLog(t, env, mixed, fmt.Sprintf("seed %d mixed after backfill", seed))
		backfilled, _, err := store.SnapshotUpdateHeads(ctx, env.ORM, mixed)
		if err != nil {
			t.Fatal(err)
		}
		requireSameHeads(t, fmt.Sprintf("seed %d backfill after compaction", seed), backfilled, mixedBefore)
	}
	if deleted == 0 {
		t.Fatal("compaction deleted nothing: the test proves nothing about what it keeps")
	}
}
