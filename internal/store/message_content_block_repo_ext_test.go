// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

const (
	textBlock   = reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT
	toolCall    = reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL
	toolResultT = reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT
)

func bp(b bool) *bool { return &b }

// block is a content block of msgID at position, with every column set.
func block(msgID string, position int, typ reliantv1.ContentBlockType) *core.MessageContentBlock {
	id := fmt.Sprintf("%s-b%d", msgID, position)
	return &core.MessageContentBlock{
		ID: id, MessageID: msgID, Position: position, BlockType: typ,
		Content: sp("content " + id), ToolName: sp("shell"), ToolInput: sp(`{"cmd":"ls"}`), ToolCallID: sp("call-" + id),
		ThoughtSignature: sp("sig"), Phase: sp("final_answer"), IsError: bp(false), Version: ip(1),
		ActivityID: sp("act"), WorkflowRunID: sp("run"), AttemptNumber: 2,
		CreatedAt: t0, UpdatedAt: t0,
	}
}

func requireSameBlock(t *testing.T, label string, want, got *core.MessageContentBlock) {
	t.Helper()
	if want == nil || got == nil {
		if want != got {
			t.Fatalf("%s: got %+v, want %+v", label, got, want)
		}
		return
	}
	render := func(b core.MessageContentBlock) string {
		s := func(p *string) any {
			if p == nil {
				return nil
			}
			return *p
		}
		var isErr, version any
		if b.IsError != nil {
			isErr = *b.IsError
		}
		if b.Version != nil {
			version = *b.Version
		}
		return fmt.Sprint(b.ID, b.MessageID, b.Position, b.BlockType, s(b.Content), s(b.ToolName), s(b.ToolInput), s(b.ToolCallID),
			s(b.ThoughtSignature), s(b.Phase), isErr, version, b.NodeID, b.NodePath, s(b.ActivityID), s(b.WorkflowRunID), b.AttemptNumber)
	}
	if render(*want) != render(*got) || !want.CreatedAt.Equal(got.CreatedAt) {
		t.Fatalf("%s:\n got %s %v\nwant %s %v", label, render(*got), got.CreatedAt, render(*want), want.CreatedAt)
	}
}

func TestContentBlocks_RoundTripAcrossLayers(t *testing.T) {
	f := newMsgFixture(t, "chat-blocks")
	ctx := context.Background()
	o := f.env.ORM
	msgs := f.seedTranscript(t)

	// Three blocks on each of the first three messages, alternating layers.
	var all []*core.MessageContentBlock
	for i, m := range msgs[:3] {
		for p, typ := range []reliantv1.ContentBlockType{textBlock, toolCall, toolResultT} {
			b := block(m.ID, p, typ)
			all = append(all, b)
			var err error
			if (i+p)%2 == 0 {
				err = f.env.Repo.CreateContentBlock(ctx, b)
			} else {
				err = store.InsertContentBlock(ctx, o, b)
			}
			if err != nil {
				t.Fatalf("create %s: %v", b.ID, err)
			}
		}
	}
	for _, want := range all {
		got, err := store.FindContentBlock(ctx, o, want.ID)
		if err != nil {
			t.Fatal(err)
		}
		requireSameBlock(t, "FindContentBlock "+want.ID, want, got)
		twin, err := f.env.Repo.GetContentBlock(ctx, want.ID)
		if err != nil {
			t.Fatal(err)
		}
		requireSameBlock(t, "Repo.GetContentBlock "+want.ID, want, twin)
	}
	if got, err := store.FindContentBlock(ctx, o, "absent"); err != nil || got != nil {
		t.Fatalf("FindContentBlock(absent) = %v, %v", got, err)
	}

	// Invalid UTF-8 is repaired, in the caller's block too.
	bad := block(msgs[3].ID, 0, textBlock)
	bad.Content = sp("bad \xff byte")
	if err := store.InsertContentBlock(ctx, o, bad); err != nil {
		t.Fatal(err)
	}
	if *bad.Content != "bad \uFFFD byte" {
		t.Fatalf("caller's block content = %q; want it repaired in place", *bad.Content)
	}
	if got, _ := f.env.Repo.GetContentBlock(ctx, bad.ID); *got.Content != "bad \uFFFD byte" {
		t.Fatalf("stored content = %q", *got.Content)
	}

	// Once: the existing row is left as it is.
	again := block(msgs[0].ID, 0, textBlock)
	again.Content = sp("rewritten")
	if err := store.InsertContentBlockOnce(ctx, o, again); err != nil {
		t.Fatal(err)
	}
	if err := f.env.Repo.CreateContentBlockIfNotExists(ctx, again); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.FindContentBlock(ctx, o, again.ID); *got.Content != "content "+again.ID {
		t.Fatalf("Once overwrote the block: %q", *got.Content)
	}

	// Tool blocks by call id.
	tc, err := store.FindToolBlock(ctx, o, "call-m1-b1", toolCall)
	if err != nil || tc == nil || tc.ID != "m1-b1" {
		t.Fatalf("FindToolBlock(call) = %v, %v; want m1-b1", tc, err)
	}
	if none, err := store.FindToolBlock(ctx, o, "call-m1-b1", toolResultT); err != nil || none != nil {
		t.Fatalf("FindToolBlock(call id, result type) = %v, %v; want nil", none, err)
	}
	if twin, err := f.env.Repo.GetContentBlockByToolCallID(ctx, "call-m1-b1"); err != nil || twin.ID != "m1-b1" {
		t.Fatalf("Repo.GetContentBlockByToolCallID = %v, %v", twin, err)
	}
	if _, err := f.env.Repo.GetContentBlockByToolCallID(ctx, "call-absent"); err == nil {
		t.Fatal("Repo.GetContentBlockByToolCallID(absent) = nil error; want not found")
	}
	if res, err := f.env.Repo.GetToolResultBlock(ctx, "call-n2-b2"); err != nil || res == nil || res.ID != "n2-b2" {
		t.Fatalf("Repo.GetToolResultBlock = %+v, %v; want n2-b2", res, err)
	}

	// The node columns: "" on the core type is NULL.
	noded := block(msgs[3].ID, 1, toolResultT)
	noded.NodeID, noded.NodePath = "node", "a/b"
	if err := store.InsertContentBlock(ctx, o, noded); err != nil {
		t.Fatal(err)
	}
	if res, err := f.env.Repo.GetToolResultBlock(ctx, *noded.ToolCallID); err != nil || res == nil || res.NodeID != "node" || res.NodePath != "a/b" {
		t.Fatalf("Repo.GetToolResultBlock(noded) = %+v, %v; want its node", res, err)
	}
	var nullNodes int
	if err := f.env.SQL.QueryRowContext(ctx, `SELECT count(*) FROM message_content_blocks WHERE message_id = 'm0' AND node_id IS NULL`).Scan(&nullNodes); err != nil || nullNodes != 3 {
		t.Fatalf("blocks with NULL node_id = %d, %v; want 3", nullNodes, err)
	}

	// Per-message and batch reads, zero / one / many ids.
	list, err := store.ListContentBlocks(ctx, o, "m1")
	if err != nil || len(list) != 3 || list[0].Position != 0 || list[2].Position != 2 {
		t.Fatalf("ListContentBlocks = %d blocks, %v", len(list), err)
	}
	for _, tc := range []struct {
		ids  []string
		want int
	}{{nil, 0}, {[]string{"m0"}, 3}, {[]string{"n2", "m0", "m1", "absent"}, 9}} {
		got, err := store.ListContentBlocksForMessages(ctx, o, tc.ids)
		if err != nil || len(got) != tc.want {
			t.Fatalf("ListContentBlocksForMessages(%v) = %d, %v; want %d", tc.ids, len(got), err, tc.want)
		}
		twin, err := f.env.Repo.ListContentBlocksForMessages(ctx, tc.ids)
		if err != nil || len(twin) != tc.want {
			t.Fatalf("Repo.ListContentBlocksForMessages(%v) = %d, %v; want %d", tc.ids, len(twin), err, tc.want)
		}
		for i := range got {
			requireSameBlock(t, "batch read", twin[i], got[i])
			if i > 0 && (got[i-1].MessageID > got[i].MessageID || (got[i-1].MessageID == got[i].MessageID && got[i-1].Position >= got[i].Position)) {
				t.Fatalf("batch read out of (message_id, position) order at %d", i)
			}
		}
	}

	// UpdateContentBlockBody writes NULL for a nil field.
	upd := &core.MessageContentBlock{ID: "m0-b0", Content: sp("new"), Version: ip(9)}
	if err := store.UpdateContentBlockBody(ctx, o, upd); err != nil {
		t.Fatal(err)
	}
	got, _ := f.env.Repo.GetContentBlock(ctx, "m0-b0")
	if *got.Content != "new" || got.ToolInput != nil || *got.Version != 9 || *got.ToolName != "shell" {
		t.Fatalf("after UpdateContentBlockBody: %+v", got)
	}
	if err := store.UpdateContentBlockBody(ctx, o, &core.MessageContentBlock{ID: "absent"}); err != nil {
		t.Fatalf("UpdateContentBlockBody(absent) = %v", err)
	}

	// Streamed deltas through both layers concatenate in order; only
	// AppendToContentBlock counts a version. Quotes, backslashes and `?`
	// survive bun's interpolation.
	if _, err := f.env.SQL.ExecContext(ctx, `UPDATE message_content_blocks SET content = NULL, version = 0 WHERE id = 'm1-b0'`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		delta := fmt.Sprintf("<%d '\\ ?>", i)
		if i%2 == 0 {
			err = f.env.Repo.AppendToContentBlock(ctx, "m1-b0", delta)
		} else {
			err = store.AppendToContentBlock(ctx, o, "m1-b0", delta)
		}
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := store.AppendContentDelta(ctx, o, "m1-b0", "!"); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendToContentBlock(ctx, o, "m1-b0", ""); err != nil {
		t.Fatal(err)
	}
	got, _ = store.FindContentBlock(ctx, o, "m1-b0")
	if want := "<0 '\\ ?><1 '\\ ?><2 '\\ ?><3 '\\ ?><4 '\\ ?><5 '\\ ?>!"; *got.Content != want || *got.Version != 6 {
		t.Fatalf("content %q version %d; want %q version 6", *got.Content, *got.Version, want)
	}
	if err := f.env.Repo.AppendToolInputDelta(ctx, f.chat, "m1-b1", `,"x":1}`); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendToolInputDelta(ctx, o, "m1-b1", "?"); err != nil {
		t.Fatal(err)
	}
	if got, _ = store.FindContentBlock(ctx, o, "m1-b1"); *got.ToolInput != `{"cmd":"ls"},"x":1}?` || *got.Version != 1 {
		t.Fatalf("tool_input %q version %d", *got.ToolInput, *got.Version)
	}

	// Streaming finished: every block of the message is stamped.
	n, err := store.TouchMessageBlocks(ctx, o, "n2")
	if err != nil || n != 3 {
		t.Fatalf("TouchMessageBlocks = %d, %v; want 3", n, err)
	}
	if got, _ = store.FindContentBlock(ctx, o, "n2-b0"); !got.UpdatedAt.After(t0) {
		t.Fatalf("n2-b0 updated_at %v not stamped", got.UpdatedAt)
	}
}

// atomicWrite is one message with nBlocks text blocks (the last with an
// invalid UTF-8 byte and an empty phase).
func atomicWrite(f *msgFixture, id string, nBlocks int) store.AtomicMessageWrite {
	blocks := make([]core.MessageContentBlock, nBlocks)
	for i := range blocks {
		blocks[i] = core.MessageContentBlock{ID: fmt.Sprintf("%s-b%d", id, i), MessageID: id, Position: i,
			BlockType: textBlock, Content: sp(fmt.Sprintf("block %d of %s", i, id)), Phase: sp("final_answer")}
	}
	blocks[nBlocks-1].Content = sp("bad \xff byte")
	blocks[nBlocks-1].Phase = sp("")
	return store.AtomicMessageWrite{MessageID: id, ChatID: f.chat, ThreadID: f.main, ContextWindowID: f.mainCW0,
		Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, Model: sp(""), ProviderAccountID: sp("acct-1"), CreatedAt: t0, Blocks: blocks,
		ChatUpdateType: db.UpdateTypeMessage, ChatUpdateEntity: id,
		ChatUpdateData: func(o, s, u int64) (string, error) {
			return fmt.Sprintf(`{"ordinal":%d,"seq":%d,"u":%d}`, o, s, u), nil
		}}
}

// TestSaveMessageAtomic_InterleavesAcrossLayers: messages written alternately
// through the Repository and the store share one ordinal/seq/update-sequence
// allocator, and read back identically.
func TestSaveMessageAtomic_InterleavesAcrossLayers(t *testing.T) {
	f := newMsgFixture(t, "chat-atomic")
	ctx := context.Background()
	var ids []string
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("a%d", i)
		ids = append(ids, id)
		var r *store.AtomicMessageResult
		var err error
		if i%2 == 0 {
			r, err = f.env.Repo.SaveMessageAtomic(ctx, atomicWrite(f, id, 3))
		} else {
			r, err = store.SaveMessageAtomic(ctx, f.env.ORM, atomicWrite(f, id, 3))
		}
		if err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
		if r.Ordinal != int64(i) || r.Seq != int64(i) || r.UpdateSeq != int64(i+1) {
			t.Fatalf("%s: ordinal/seq/update %d/%d/%d, want %d/%d/%d", id, r.Ordinal, r.Seq, r.UpdateSeq, i, i, i+1)
		}
		if r.ChatUpdate.ID != fmt.Sprintf("%s-%d", f.chat, i+1) || r.ChatUpdateBytes != fmt.Sprintf(`{"ordinal":%d,"seq":%d,"u":%d}`, i, i, i+1) {
			t.Fatalf("%s: chat update %+v / %s", id, r.ChatUpdate, r.ChatUpdateBytes)
		}
	}

	msg, err := store.FindMessage(ctx, f.env.ORM, "a1")
	if err != nil || msg.Seq != 1 || msg.Model != nil || msg.ProviderAccountID == nil || *msg.ProviderAccountID != "acct-1" || !msg.CreatedAt.Equal(t0) {
		t.Fatalf("a1 = %+v, %v", msg, err)
	}
	blocks, err := store.ListContentBlocksForMessages(ctx, f.env.ORM, ids)
	if err != nil || len(blocks) != 12 {
		t.Fatalf("blocks = %d, %v; want 12", len(blocks), err)
	}
	for i, b := range blocks {
		last := i%3 == 2
		if last && (*b.Content != "bad \uFFFD byte" || b.Phase != nil) {
			t.Fatalf("block %s: content %q phase %v; want repaired content and a NULL phase", b.ID, *b.Content, b.Phase)
		}
		if !last && (b.Phase == nil || *b.Phase != "final_answer") {
			t.Fatalf("block %s: phase %v", b.ID, b.Phase)
		}
	}
	updates, err := store.ListChatUpdate(ctx, f.env.ORM, orm.WhereEq(store.ChatUpdateFieldChatID, f.chat), orm.WithOrderBy(store.ChatUpdateFieldSequenceNumber, orm.Asc))
	if err != nil || len(updates) != 4 || updates[3].SequenceNumber != 4 || updates[3].UpdateType != db.UpdateTypeMessage || updates[3].EntityID != "a3" {
		t.Fatalf("chat updates = %+v, %v", updates, err)
	}
}

// TestSaveMessageAtomic_RollsBack: a failing payload builder rolls the whole
// write back — message, blocks and counters.
func TestSaveMessageAtomic_RollsBack(t *testing.T) {
	f := newMsgFixture(t, "chat-atomic-rb")
	ctx := context.Background()
	boom := errors.New("boom")
	w := atomicWrite(f, "rb", 2)
	w.ChatUpdateData = func(int64, int64, int64) (string, error) { return "", boom }
	if _, err := f.env.Repo.SaveMessageAtomic(ctx, w); !errors.Is(err, boom) {
		t.Fatalf("Repo.SaveMessageAtomic = %v; want boom", err)
	}
	if _, err := store.SaveMessageAtomic(ctx, f.env.ORM, w); !errors.Is(err, boom) {
		t.Fatalf("store.SaveMessageAtomic = %v; want boom", err)
	}
	if n, err := store.CountMessage(ctx, f.env.ORM, orm.WhereEq(store.MessageFieldChatID, f.chat)); err != nil || n != 0 {
		t.Fatalf("messages after rollback = %d, %v", n, err)
	}
	if n, _ := store.CountMessageContentBlock(ctx, f.env.ORM, orm.WhereEq(store.MessageContentBlockFieldMessageID, "rb")); n != 0 {
		t.Fatalf("blocks after rollback = %d", n)
	}
	if _, err := store.GetUpdateStreamCounterByPK(ctx, f.env.ORM, "chat", f.chat); err == nil {
		t.Fatal("update stream counter survived the rollback")
	}
}

// TestSaveMessageAtomic_ConcurrentWritersAcrossLayers: 8 writers, half through
// the Repository and half through the store, 10 messages each into one chat:
// no unique violation, and seq and the update sequence are both gapless.
func TestSaveMessageAtomic_ConcurrentWritersAcrossLayers(t *testing.T) {
	f := newMsgFixture(t, "chat-atomic-cc")
	var (
		mu         sync.Mutex
		seqs, upds []int
		wg         sync.WaitGroup
	)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				id := fmt.Sprintf("cc-%d-%d", w, i)
				var seq, upd int64
				if w%2 == 0 {
					r, err := f.env.Repo.SaveMessageAtomic(context.Background(), atomicWrite(f, id, 2))
					if err != nil {
						t.Errorf("repo %s: %v", id, err)
						return
					}
					seq, upd = r.Seq, r.UpdateSeq
				} else {
					r, err := store.SaveMessageAtomic(context.Background(), f.env.ORM, atomicWrite(f, id, 2))
					if err != nil {
						t.Errorf("store %s: %v", id, err)
						return
					}
					seq, upd = r.Seq, r.UpdateSeq
				}
				mu.Lock()
				seqs, upds = append(seqs, int(seq)), append(upds, int(upd))
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	sort.Ints(seqs)
	sort.Ints(upds)
	if len(seqs) != 80 {
		t.Fatalf("%d writes landed, want 80", len(seqs))
	}
	for i := range seqs {
		if seqs[i] != i || upds[i] != i+1 {
			t.Fatalf("position %d: seq %d update %d (want gapless 0..79 / 1..80)", i, seqs[i], upds[i])
		}
	}
}
