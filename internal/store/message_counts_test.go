// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// chat_thread_message_counts (migration 20261119173231) is what a chat's
// message totals read: ListMessages' count outside the main thread and the
// snapshot's chat total. The triggers on messages keep it, so every test here
// writes through a different writer and then holds the counts to the
// count(*) they replace, which is what "exact and identical to today's"
// means.

// requireCountsExact fails unless every count the store reads for chatID
// equals the count(*) over messages it replaces, wantTotal is the chat's
// total, and the whole table is exactly messages grouped by (chat_id,
// thread_id) — a row for every pair with messages and none for any other.
func requireCountsExact(t *testing.T, env *storetest.Env, chatID string, wantTotal int, threads ...string) {
	t.Helper()
	ctx := context.Background()

	var raw int
	if err := env.SQL.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE chat_id = $1`, chatID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != wantTotal {
		t.Fatalf("fixture: chat %s holds %d messages, want %d", chatID, raw, wantTotal)
	}
	if n, err := store.CountMessagesInChat(ctx, env.ORM, chatID); err != nil || n != raw {
		t.Fatalf("CountMessagesInChat(%s) = %d, %v; count(*) is %d", chatID, n, err, raw)
	}
	if n, err := env.Repo.CountMessagesInChat(ctx, chatID); err != nil || n != raw {
		t.Fatalf("Repo.CountMessagesInChat(%s) = %d, %v; count(*) is %d", chatID, n, err, raw)
	}
	for _, thread := range append(threads, "no-such-thread") {
		var outside int
		if err := env.SQL.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE chat_id = $1 AND thread_id <> $2`, chatID, thread).Scan(&outside); err != nil {
			t.Fatal(err)
		}
		if n, err := store.CountMessagesInChatOutsideThread(ctx, env.ORM, chatID, thread); err != nil || n != outside {
			t.Fatalf("CountMessagesInChatOutsideThread(%s, %s) = %d, %v; count(*) is %d", chatID, thread, n, err, outside)
		}
		if n, err := env.Repo.CountMessagesInChatOutsideThread(ctx, chatID, thread); err != nil || n != outside {
			t.Fatalf("Repo.CountMessagesInChatOutsideThread(%s, %s) = %d, %v; count(*) is %d", chatID, thread, n, err, outside)
		}
	}

	want := groupedRows(t, env, `SELECT chat_id, thread_id, count(*) FROM messages GROUP BY chat_id, thread_id ORDER BY chat_id, thread_id`)
	got := groupedRows(t, env, `SELECT chat_id, thread_id, message_count FROM chat_thread_message_counts ORDER BY chat_id, thread_id`)
	if got != want {
		t.Fatalf("chat_thread_message_counts =\n%s\nmessages grouped by (chat_id, thread_id) =\n%s", got, want)
	}
}

// groupedRows renders a (chat_id, thread_id, n) result one row per line.
func groupedRows(t *testing.T, env *storetest.Env, query string) string {
	t.Helper()
	rows, err := env.SQL.QueryContext(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var chat, thread string
		var n int64
		if err := rows.Scan(&chat, &thread, &n); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s %s %d\n", chat, thread, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestMessageCounts_EveryInsertWriter: each way a message is written — the
// Repository's CreateMessage, InsertMessage, InsertMessageOnce (and its
// absorbed repeat), the atomic save, and a multi-row INSERT nobody has a Go
// function for — moves the counts by exactly the rows it wrote.
func TestMessageCounts_EveryInsertWriter(t *testing.T) {
	t.Parallel()
	f := newMsgFixture(t, "chat-count-insert")
	ctx := context.Background()
	requireCountsExact(t, f.env, f.chat, 0, f.main)

	f.seedTranscript(t) // CreateMessage and InsertMessage, three threads
	requireCountsExact(t, f.env, f.chat, 8, f.main, f.node, f.spawn)

	once := f.msg("o8", f.node, f.nodeCW, 8, reliantv1.MessageRole_MESSAGE_ROLE_USER)
	once.Ordinal = 2
	for i, want := range []bool{true, false} {
		if inserted, err := store.InsertMessageOnce(ctx, f.env.ORM, once); err != nil || inserted != want {
			t.Fatalf("InsertMessageOnce #%d = %v, %v; want %v", i, inserted, err, want)
		}
	}
	requireCountsExact(t, f.env, f.chat, 9, f.main, f.node, f.spawn)

	for i := 0; i < 2; i++ {
		if _, err := f.env.Repo.SaveMessageAtomic(ctx, atomicWrite(f, fmt.Sprintf("a%d", i), 2)); err != nil {
			t.Fatalf("SaveMessageAtomic: %v", err)
		}
	}
	requireCountsExact(t, f.env, f.chat, 11, f.main, f.node, f.spawn)

	// One statement over two threads: one grouped upsert per thread.
	if _, err := f.env.SQL.ExecContext(ctx, `
		INSERT INTO messages (id, chat_id, ordinal, seq, thread_id, context_window_id, role, created_at, updated_at)
		VALUES ('r1', $1, 100, 100, $2, $3, 1, $6, $6),
		       ('r2', $1, 101, 101, $2, $3, 1, $6, $6),
		       ('r3', $1, 100, 102, $4, $5, 1, $6, $6)`,
		f.chat, f.spawn, f.spawnCW, f.node, f.nodeCW, t0); err != nil {
		t.Fatal(err)
	}
	requireCountsExact(t, f.env, f.chat, 14, f.main, f.node, f.spawn)
}

// TestMessageCounts_RolledBackWriteCountsNothing: the counts move in the
// transaction of the write, so a write that rolls back leaves them as they
// were.
func TestMessageCounts_RolledBackWriteCountsNothing(t *testing.T) {
	t.Parallel()
	f := newMsgFixture(t, "chat-count-rollback")
	ctx := context.Background()
	f.seedTranscript(t)

	errRollback := errors.New("roll back")
	err := f.env.ORM.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		m := f.msg("x9", f.spawn, f.spawnCW, 9, reliantv1.MessageRole_MESSAGE_ROLE_USER)
		m.Ordinal = 2
		if err := store.InsertMessage(ctx, f.env.ORM, m); err != nil {
			return err
		}
		// Inside the transaction its own write is already counted.
		if n, err := store.CountMessagesInChat(ctx, f.env.ORM, f.chat); err != nil || n != 9 {
			return fmt.Errorf("in-transaction count = %d, %v; want 9", n, err)
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("RunTx = %v; want the rollback", err)
	}
	requireCountsExact(t, f.env, f.chat, 8, f.main, f.node, f.spawn)
}

// TestMessageCounts_ConcurrentWriters: savers racing on one chat, across its
// threads, leave the counts exact.
func TestMessageCounts_ConcurrentWriters(t *testing.T) {
	t.Parallel()
	f := newMsgFixture(t, "chat-count-race")
	ctx := context.Background()

	threads := []struct{ thread, cw string }{{f.main, f.mainCW0}, {f.node, f.nodeCW}, {f.spawn, f.spawnCW}}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := atomicWrite(f, fmt.Sprintf("race%d", i), 1)
			w.ThreadID, w.ContextWindowID = threads[i%3].thread, threads[i%3].cw
			if _, err := f.env.Repo.SaveMessageAtomic(ctx, w); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("SaveMessageAtomic: %v", err)
	}
	requireCountsExact(t, f.env, f.chat, 12, f.main, f.node, f.spawn)
}

// TestMessageCounts_Deletes: deleting one message, a thread's last one (its
// row goes: a row exists exactly while its count is above zero), and a whole
// project's chats leaves the counts exact and no row behind.
func TestMessageCounts_Deletes(t *testing.T) {
	t.Parallel()
	f := newMsgFixture(t, "chat-count-delete")
	ctx := context.Background()
	f.seedTranscript(t)

	if err := f.env.Repo.DeleteMessage(ctx, "m7"); err != nil {
		t.Fatal(err)
	}
	requireCountsExact(t, f.env, f.chat, 7, f.main, f.node, f.spawn)

	if _, err := f.env.SQL.ExecContext(ctx, `DELETE FROM messages WHERE thread_id = $1`, f.spawn); err != nil {
		t.Fatal(err)
	}
	requireCountsExact(t, f.env, f.chat, 5, f.main, f.node, f.spawn)

	// The project purge deletes the chats' messages in bulk, then the chats.
	if err := store.DeleteUserProject(ctx, f.env.ORM, storetest.SeedProjectID, storetest.SeedUserID); err != nil {
		t.Fatalf("DeleteUserProject: %v", err)
	}
	requireCountsExact(t, f.env, f.chat, 0, f.main, f.node, f.spawn)
}

// TestMessageCounts_MovedMessageFollows: nothing moves a message to another
// thread today, but if something does the counts follow it.
func TestMessageCounts_MovedMessageFollows(t *testing.T) {
	t.Parallel()
	f := newMsgFixture(t, "chat-count-move")
	ctx := context.Background()
	f.seedTranscript(t)

	// m7 is the main thread's ordinal 3; the node thread has 0 and 1.
	if _, err := f.env.SQL.ExecContext(ctx, `UPDATE messages SET thread_id = $1 WHERE id = 'm7'`, f.node); err != nil {
		t.Fatal(err)
	}
	requireCountsExact(t, f.env, f.chat, 8, f.main, f.node, f.spawn)
	// An update that keeps both keys moves nothing.
	if _, err := f.env.SQL.ExecContext(ctx, `UPDATE messages SET thread_id = thread_id, token_count = 7 WHERE chat_id = $1`, f.chat); err != nil {
		t.Fatal(err)
	}
	requireCountsExact(t, f.env, f.chat, 8, f.main, f.node, f.spawn)
}

// TestMessageCounts_BranchCountsOnlyItsOwnRows: a branched chat inherits its
// parent's history through the context-window chain, not by copying rows, so
// its counts are its own rows only — what count(*) by chat_id reports — and
// the parent's are untouched by the branch's writes.
func TestMessageCounts_BranchCountsOnlyItsOwnRows(t *testing.T) {
	t.Parallel()
	f := newMsgFixture(t, "chat-count-fork")
	ctx := context.Background()
	f.seedTranscript(t)

	branch := "chat-count-fork-branch"
	if err := f.env.Repo.CreateChat(ctx, &db.Chat{
		ID: branch, Title: branch, ProjectID: storetest.SeedProjectID, UserID: storetest.SeedUserID,
		State: reliantv1.ChatState_CHAT_STATE_IDLE, CreatedAt: t0, UpdatedAt: t0, LastActive: t0,
	}); err != nil {
		t.Fatal(err)
	}
	m7 := "m7"
	if _, err := f.env.Repo.CreateThread(ctx, &db.Thread{ID: branch, ChatID: branch, Origin: core.ThreadOriginFork, ParentThreadID: &f.main, ForkAtMessageID: &m7, CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.Repo.CreateContextWindow(ctx, &db.ContextWindow{ID: branch + ":cw", ThreadID: branch, Sequence: 1, ParentContextWindowID: &f.mainCW1, ForkAtMessageID: &m7, CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedSeqCounterForFork(ctx, f.env.ORM, branch, branch+":cw"); err != nil {
		t.Fatal(err)
	}
	requireCountsExact(t, f.env, branch, 0, branch)

	for i := 0; i < 3; i++ {
		w := atomicWrite(f, fmt.Sprintf("b%d", i), 1)
		w.ChatID, w.ThreadID, w.ContextWindowID = branch, branch, branch+":cw"
		if _, err := f.env.Repo.SaveMessageAtomic(ctx, w); err != nil {
			t.Fatalf("SaveMessageAtomic(branch): %v", err)
		}
	}
	requireCountsExact(t, f.env, branch, 3, branch)
	requireCountsExact(t, f.env, f.chat, 8, f.main, f.node, f.spawn)
}

// TestMessageCounts_ReadFromTheCounts proves the reads of an initialized chat
// come from the counts table and not from a count over messages: a bumped row
// shows through.
func TestMessageCounts_ReadFromTheCounts(t *testing.T) {
	t.Parallel()
	f := newMsgFixture(t, "chat-count-source")
	ctx := context.Background()
	f.seedTranscript(t)

	if _, err := f.env.SQL.ExecContext(ctx, `UPDATE chat_thread_message_counts SET message_count = message_count + 100 WHERE chat_id = $1 AND thread_id = $2`, f.chat, f.node); err != nil {
		t.Fatal(err)
	}
	if n, err := store.CountMessagesInChat(ctx, f.env.ORM, f.chat); err != nil || n != 108 {
		t.Fatalf("CountMessagesInChat = %d, %v; want 108, the counts table's sum", n, err)
	}
	if n, err := store.CountMessagesInChatOutsideThread(ctx, f.env.ORM, f.chat, f.main); err != nil || n != 104 {
		t.Fatalf("CountMessagesInChatOutsideThread = %d, %v; want 104, the counts table's", n, err)
	}
}

// installCountsOverExistingMessages puts the database in the state the
// migration leaves on prod: messages written before the triggers existed are
// in no row, and the migration's own last statement marks every chat
// pending.
func installCountsOverExistingMessages(t *testing.T, env *storetest.Env) {
	t.Helper()
	ctx := context.Background()
	migration, err := os.ReadFile("../../db/migrations/20261119173231_chat_thread_message_counts.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(migration)
	i := strings.LastIndex(text, "INSERT INTO chat_message_counts_pending (chat_id)")
	if i < 0 {
		t.Fatal("the migration's pending statement is not where this test looks for it")
	}
	if _, err := env.SQL.ExecContext(ctx, `DELETE FROM chat_thread_message_counts`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.SQL.ExecContext(ctx, text[i:]); err != nil {
		t.Fatalf("pending statement: %v\n%s", err, text[i:])
	}
}

func requirePending(t *testing.T, env *storetest.Env, chatID string, want bool) {
	t.Helper()
	var pending bool
	if err := env.SQL.QueryRowContext(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM chat_message_counts_pending WHERE chat_id = $1)`, chatID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != want {
		t.Fatalf("chat %s pending = %v; want %v", chatID, pending, want)
	}
}

// TestMessageCounts_PendingChatInitializesOnFirstRead: a chat whose messages
// predate the triggers is pending. Writes after the triggers only record
// their change (a negative row for a deleted older message); a read inside a
// transaction counts the messages and leaves the chat pending; the first read
// outside one initializes it, after which the rows are its counts.
func TestMessageCounts_PendingChatInitializesOnFirstRead(t *testing.T) {
	t.Parallel()
	f := newMsgFixture(t, "chat-count-pending")
	ctx := context.Background()
	f.seedTranscript(t)
	installCountsOverExistingMessages(t, f.env)
	requirePending(t, f.env, f.chat, true)

	// After the triggers: an older node message goes, a new main one comes.
	if err := f.env.Repo.DeleteMessage(ctx, "n2"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.Repo.SaveMessageAtomic(ctx, atomicWrite(f, "after", 1)); err != nil {
		t.Fatal(err)
	}
	if got := groupedRows(t, f.env, `SELECT chat_id, thread_id, message_count FROM chat_thread_message_counts ORDER BY chat_id, thread_id`); got != fmt.Sprintf("%s %s 1\n%s %s -1\n", f.chat, f.main, f.chat, f.node) {
		t.Fatalf("rows of a pending chat = %q; want only the changes since the triggers", got)
	}

	if err := f.env.ORM.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		n, err := store.CountMessagesInChat(ctx, f.env.ORM, f.chat)
		if err != nil || n != 8 {
			return fmt.Errorf("in-transaction count = %d, %v; want 8, counted", n, err)
		}
		n, err = store.CountMessagesInChatOutsideThread(ctx, f.env.ORM, f.chat, f.main)
		if err != nil || n != 3 {
			return fmt.Errorf("in-transaction count outside main = %d, %v; want 3, counted", n, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	requirePending(t, f.env, f.chat, true)

	requireCountsExact(t, f.env, f.chat, 8, f.main, f.node, f.spawn)
	requirePending(t, f.env, f.chat, false)

	// Initialized: writes now keep counts.
	if err := f.env.Repo.DeleteMessage(ctx, "s3"); err != nil {
		t.Fatal(err)
	}
	requireCountsExact(t, f.env, f.chat, 7, f.main, f.node, f.spawn)
}

// TestMessageCounts_ConcurrentInitialization: many reads initializing one
// pending chat at once, while it is written to, add its older messages once.
func TestMessageCounts_ConcurrentInitialization(t *testing.T) {
	t.Parallel()
	f := newMsgFixture(t, "chat-count-init-race")
	ctx := context.Background()
	f.seedTranscript(t)
	installCountsOverExistingMessages(t, f.env)

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := store.CountMessagesInChatOutsideThread(ctx, f.env.ORM, f.chat, f.main); err != nil {
				errs <- err
			}
		}()
		go func(i int) {
			defer wg.Done()
			w := atomicWrite(f, fmt.Sprintf("init-race%d", i), 1)
			w.ThreadID, w.ContextWindowID = f.spawn, f.spawnCW
			if _, err := f.env.Repo.SaveMessageAtomic(ctx, w); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	requireCountsExact(t, f.env, f.chat, 16, f.main, f.node, f.spawn)
	requirePending(t, f.env, f.chat, false)
}

// TestMessageCounts_TruncateLeavesNothingToCount: emptying messages leaves no
// counts and no pending chat behind.
func TestMessageCounts_TruncateLeavesNothingToCount(t *testing.T) {
	t.Parallel()
	f := newMsgFixture(t, "chat-count-truncate")
	ctx := context.Background()
	f.seedTranscript(t)
	installCountsOverExistingMessages(t, f.env)
	if err := f.env.Repo.DeleteMessage(ctx, "n2"); err != nil {
		t.Fatal(err)
	}

	if _, err := f.env.SQL.ExecContext(ctx, `TRUNCATE messages CASCADE`); err != nil {
		t.Fatal(err)
	}
	var rows, pending int
	if err := f.env.SQL.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM chat_thread_message_counts), (SELECT count(*) FROM chat_message_counts_pending)`).Scan(&rows, &pending); err != nil {
		t.Fatal(err)
	}
	if rows != 0 || pending != 0 {
		t.Fatalf("after TRUNCATE: %d count rows, %d pending chats; want none", rows, pending)
	}
}
