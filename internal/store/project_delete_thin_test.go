// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// One chat can hold more than any one transaction should delete: a project
// whose first batch of chats held 33k messages could not delete inside a 10s
// request, and every attempt rolled the whole batch back (prod, 2026-11-18).
// So the bulk under a project's chats — messages with their content blocks,
// tool calls and results, chat_updates, user_updates, step_executions — goes
// in row-bounded slices before the chat batches run, each slice its own
// transaction, and nothing of it is left behind. A thread and context window
// of ANOTHER project forked at a thinned message lose the pointer, rather
// than refusing the delete.
//
// Not parallel: it installs DeleteUserProject's package-wide hook (keyed by
// project id, so other tests' deletes pass it by).
func TestDeleteUserProjectThinsALargeChatInRowBoundedSlices(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) // fixture data, not a clock
	const (
		project, chat   = "proj-thin", "thin-chat"
		other, kept     = "proj-fork-holder", "fork-holder-chat"
		rowsPerTable    = 12
		messageSlice    = 3
		rowSlice        = 4
		thinnedMessage  = "thin-chat-msg-05"
		workflowOfChat  = "thin-chat-wf"
		messagesInChat  = 10
		minThinningTxns = 4 + 3*4 // ceil(10/3) message slices; 12/4 + one short slice per flat table
	)

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := env.SQL.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := env.SQL.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}
	seedProject := func(id string) {
		t.Helper()
		if err := store.InsertProject(ctx, env.ORM, &core.Project{
			ID: id, UserID: "owner", Name: id, Path: "/srv/" + id,
			CreatedAt: now, UpdatedAt: now, LastActive: now,
		}); err != nil {
			t.Fatalf("InsertProject: %v", err)
		}
	}
	seedChat := func(project, chat string) {
		exec(`INSERT INTO chats (id, title, project_id, user_id, created_at, updated_at, last_active)
		      VALUES ($1,$1,$2,'owner',$3,$3,$3)`, chat, project, now)
		exec(`INSERT INTO threads (id, chat_id, origin, created_at) VALUES ($1,$2,'root',$3)`, chat+"-thread", chat, now)
		exec(`INSERT INTO context_windows (id, thread_id, sequence, created_at) VALUES ($1,$2,1,$3)`, chat+"-cw", chat+"-thread", now)
	}

	seedProject(project)
	seedProject(other)
	seedChat(project, chat)
	for i := range messagesInChat {
		msg := fmt.Sprintf("%s-msg-%02d", chat, i)
		exec(`INSERT INTO messages (id, chat_id, ordinal, thread_id, context_window_id, role, created_at, updated_at, seq)
		      VALUES ($1,$2,$3,$4,$5,1,$6,$6,$3)`, msg, chat, i+1, chat+"-thread", chat+"-cw", now)
		exec(`INSERT INTO message_content_blocks (id, message_id, "position", block_type, content, created_at, updated_at)
		      VALUES ($1,$2,0,1,'hello',$3,$3)`, msg+"-block", msg, now)
		exec(`INSERT INTO tool_calls (id, chat_id, thread_id, message_id, tool_name, status, requested_at, created_at, updated_at)
		      VALUES ($1,$2,$3,$4,'bash',1,$5,$5,$5)`, msg+"-tc", chat, chat+"-thread", msg, now)
		exec(`INSERT INTO tool_call_results (tool_call_id, message_id, content, created_at, updated_at)
		      VALUES ($1,$2,'ok',$3,$3)`, msg+"-tc", msg, now)
	}
	// A delivered agent message, whose record names a message being thinned.
	exec(`INSERT INTO agent_messages (id, chat_id, from_thread_id, to_thread_id, kind, body, status, created_at, delivered_at, delivered_message_id)
	      VALUES ($1,$2,$3,$3,1,'hi',2,$4,$4,$5)`, chat+"-am", chat, chat+"-thread", now, thinnedMessage)
	// The chat's own fork pointers, RESTRICT against its messages.
	exec(`UPDATE threads SET fork_at_message_id = $2 WHERE id = $1`, chat+"-thread", chat+"-msg-00")
	exec(`UPDATE context_windows SET fork_at_message_id = $2 WHERE id = $1`, chat+"-cw", chat+"-msg-00")
	exec(`INSERT INTO workflows (id, chat_id, workflow_name, thread, created_at) VALUES ($1,$2,'agent','main',$3)`, workflowOfChat, chat, now)
	for i := range rowsPerTable {
		exec(`INSERT INTO chat_updates (id, chat_id, sequence_number, update_type, entity_id, data, created_at)
		      VALUES ($1,$2,$3,2,'ent','{}',$4)`, fmt.Sprintf("%s-upd-%02d", chat, i), chat, i+1, now)
		exec(`INSERT INTO user_updates (id, user_id, sequence_number, project_id, chat_id, update_type, entity_type, entity_id, data, created_at)
		      VALUES ($1,'owner',$2,$3,$4,1,1,$4,'{}',$5)`, fmt.Sprintf("%s-uu-%02d", chat, i), 1000+i, project, chat, now)
		exec(`INSERT INTO step_executions (id, workflow_id, step_id, activity_name, created_at)
		      VALUES ($1,$2,'step','CallLLM',$3)`, fmt.Sprintf("%s-step-%02d", chat, i), workflowOfChat, now)
	}

	// A chat of another project, forked at one of the messages being thinned.
	seedChat(other, kept)
	exec(`UPDATE threads SET fork_at_message_id = $2 WHERE id = $1`, kept+"-thread", thinnedMessage)
	exec(`UPDATE context_windows SET fork_at_message_id = $2 WHERE id = $1`, kept+"-cw", thinnedMessage)

	under := map[string]string{
		"messages":               `SELECT count(*) FROM messages WHERE chat_id = $1`,
		"message_content_blocks": `SELECT count(*) FROM message_content_blocks WHERE id LIKE $1 || '-%'`,
		"tool_calls":             `SELECT count(*) FROM tool_calls WHERE chat_id = $1`,
		"tool_call_results":      `SELECT count(*) FROM tool_call_results WHERE tool_call_id LIKE $1 || '-%'`,
		"chat_updates":           `SELECT count(*) FROM chat_updates WHERE chat_id = $1`,
		"user_updates":           `SELECT count(*) FROM user_updates WHERE chat_id = $1 OR id LIKE $1 || '-%'`,
		"step_executions":        `SELECT count(*) FROM step_executions WHERE id LIKE $1 || '-%'`,
		// The delivery record goes with its message, as in the chat batch,
		// rather than staying as a delivered record naming no message.
		"agent_messages": `SELECT count(*) FROM agent_messages WHERE chat_id = $1`,
	}

	probe := env.Probe(t)
	thinned := false
	defer store.SetProjectDeleteAfterThin(func(_ context.Context, id string) {
		if id != project {
			return
		}
		thinned = true
		// The slices removed the bulk before any chat batch ran: the chat
		// is still there, and nothing under it is.
		if n := count(`SELECT count(*) FROM chats WHERE id = $1`, chat); n != 1 {
			t.Errorf("the chat went before its chat batch: %d rows", n)
		}
		for table, query := range under {
			if n := count(query, chat); n != 0 {
				t.Errorf("%s: %d rows left for the chat batch, want the slices to take them all", table, n)
			}
		}
		if n := len(probe.Isolations()); n < minThinningTxns {
			t.Errorf("the thinning ran %d transactions, want at least %d slices", n, minThinningTxns)
		}
	})()

	probe.Reset()
	if err := store.DeleteUserProjectInSlices(ctx, probe.ORM, project, "owner", messageSlice, rowSlice); err != nil {
		t.Fatalf("DeleteUserProject: %v", err)
	}
	if !thinned {
		t.Fatal("the delete never reached its thinning hook")
	}

	if n := count(`SELECT count(*) FROM projects WHERE id = $1`, project); n != 0 {
		t.Errorf("project survived: %d", n)
	}
	for table, query := range map[string]string{
		"chats":           `SELECT count(*) FROM chats WHERE id = $1`,
		"threads":         `SELECT count(*) FROM threads WHERE chat_id = $1`,
		"context_windows": `SELECT count(*) FROM context_windows WHERE id = $1 || '-cw'`,
		"workflows":       `SELECT count(*) FROM workflows WHERE chat_id = $1`,
	} {
		under[table] = query
	}
	for table, query := range under {
		if n := count(query, chat); n != 0 {
			t.Errorf("%s: %d orphaned rows of the deleted project", table, n)
		}
	}

	// The other project is whole; its fork lost only the pointer.
	if n := count(`SELECT count(*) FROM projects WHERE id = $1`, other); n != 1 {
		t.Errorf("the other project was deleted: %d", n)
	}
	if n := count(`SELECT count(*) FROM threads WHERE id = $1 AND fork_at_message_id IS NULL`, kept+"-thread"); n != 1 {
		t.Error("the other project's thread is gone, or still forked at a deleted message")
	}
	if n := count(`SELECT count(*) FROM context_windows WHERE id = $1 AND fork_at_message_id IS NULL`, kept+"-cw"); n != 1 {
		t.Error("the other project's context window is gone, or still forked at a deleted message")
	}
}
