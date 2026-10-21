// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The P1 benchmarks (research/forge-migration/08-p5b-spike.md, "Performance")
// time the message streaming hot path through the Repository API every
// caller uses: one SaveMessageAtomic, one streamed AppendToContentBlock delta,
// and the read path's batch block load. They drive the public Repo methods,
// not a layer underneath them, so the same benchmark measures the path before
// and after the L1a port onto the forge ORM.
//
//	DATABASE_URL=… go test ./internal/store/ -run '^$' -bench P1 -benchmem

// seedBenchChat writes a chat, its root thread and the thread's first
// context window, and returns the context window's id.
func seedBenchChat(tb testing.TB, env *storetest.Env, chatID string) string {
	tb.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := env.Repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "p1 bench", ProjectID: storetest.SeedProjectID, UserID: storetest.SeedUserID,
		State: reliantv1.ChatState_CHAT_STATE_IDLE, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}); err != nil {
		tb.Fatalf("seed chat %s: %v", chatID, err)
	}
	if _, err := env.Repo.CreateThread(ctx, &db.Thread{ID: chatID, ChatID: chatID, CreatedAt: now}); err != nil {
		tb.Fatalf("seed thread %s: %v", chatID, err)
	}
	cw, err := env.Repo.CreateContextWindow(ctx, &db.ContextWindow{ID: "cw-" + chatID, ThreadID: chatID, CreatedAt: now})
	if err != nil {
		tb.Fatalf("seed context window %s: %v", chatID, err)
	}
	return cw.ID
}

func benchStr(s string) *string { return &s }

// benchWrite is one assistant message with nBlocks text blocks.
func benchWrite(chatID, cwID, id string, nBlocks int) db.AtomicMessageWrite {
	blocks := make([]db.MessageContentBlock, nBlocks)
	for i := range blocks {
		blocks[i] = db.MessageContentBlock{
			ID: fmt.Sprintf("%s-b%d", id, i), MessageID: id, Position: i,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Content:   benchStr(fmt.Sprintf("block %d of %s", i, id)),
		}
	}
	return db.AtomicMessageWrite{
		MessageID: id, ChatID: chatID, ThreadID: chatID, ContextWindowID: cwID,
		Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, CreatedAt: time.Now().UTC(), Blocks: blocks,
		ChatUpdateType: db.UpdateTypeMessage, ChatUpdateEntity: id,
		ChatUpdateData: func(o, s, _ int64) (string, error) { return fmt.Sprintf(`{"ordinal":%d,"seq":%d}`, o, s), nil },
	}
}

// BenchmarkP1SaveMessageAtomic: one 3-block message per op into one chat.
func BenchmarkP1SaveMessageAtomic(b *testing.B) {
	env := storetest.New(b)
	ctx := context.Background()
	cw := seedBenchChat(b, env, "chat-p1-save")
	var n atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := env.Repo.SaveMessageAtomic(ctx, benchWrite("chat-p1-save", cw, fmt.Sprintf("s%d", n.Add(1)), 3)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkP1AppendDelta: one ~60-byte streamed delta per op.
func BenchmarkP1AppendDelta(b *testing.B) {
	env := storetest.New(b)
	ctx := context.Background()
	cw := seedBenchChat(b, env, "chat-p1-append")
	if _, err := env.Repo.SaveMessageAtomic(ctx, benchWrite("chat-p1-append", cw, "bap", 1)); err != nil {
		b.Fatal(err)
	}
	// Every append rewrites the whole (growing) text value, so each run
	// starts from an empty block.
	if _, err := env.SQL.ExecContext(ctx, `UPDATE message_content_blocks SET content = '' WHERE id = 'bap-b0'`); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		delta := fmt.Sprintf("token %08d of a streamed assistant reply, quoted 'like this'", i)
		if err := env.Repo.AppendToContentBlock(ctx, "bap-b0", delta); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkP1ListBlocks: the batch block read for 20 messages x 3 blocks.
func BenchmarkP1ListBlocks(b *testing.B) {
	env := storetest.New(b)
	ctx := context.Background()
	cw := seedBenchChat(b, env, "chat-p1-list")
	ids := make([]string, 20)
	for i := range ids {
		ids[i] = fmt.Sprintf("ls%d", i)
		if _, err := env.Repo.SaveMessageAtomic(ctx, benchWrite("chat-p1-list", cw, ids[i], 3)); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if rows, err := env.Repo.ListContentBlocksForMessages(ctx, ids); err != nil || len(rows) != 60 {
			b.Fatal(err, len(rows))
		}
	}
}
