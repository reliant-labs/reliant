// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Deleting a project deletes its chats and worktrees (FK ON DELETE CASCADE),
// so no surface can list a chat whose project is gone.
func TestDeleteProjectLeavesNoOrphanedChatsOrWorktrees(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now()

	for _, id := range []string{"doomed", "survivor"} {
		require.NoError(t, repo.CreateProject(ctx, &Project{
			ID: id, Name: id, Path: "/tmp/" + id, UserID: "owner",
			CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
		require.NoError(t, repo.CreateChat(ctx, &Chat{
			ID: "chat-" + id, Title: id, ProjectID: id, UserID: "owner",
			State: 2, CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
		require.NoError(t, repo.CreateWorktree(ctx, &Worktree{
			ID: "wt-" + id, Name: id, Path: "/tmp/wt-" + id, Branch: "b", BaseBranch: "main",
			ProjectID: id, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
	}

	require.NoError(t, repo.DeleteProject(ctx, "doomed", "owner"))

	chats, err := repo.ListChats(ctx, ChatFilters{UserID: "owner", Limit: 50})
	require.NoError(t, err)
	require.Len(t, chats, 1)
	assert.Equal(t, "chat-survivor", chats[0].ID)

	archived, err := repo.ListArchivedChats(ctx, "owner", nil)
	require.NoError(t, err)
	assert.Empty(t, archived)

	wts, err := repo.ListWorktrees(ctx, WorktreeFilters{IncludeArchived: true, Limit: 50})
	require.NoError(t, err)
	for _, w := range wts {
		assert.NotEqual(t, "doomed", w.ProjectID)
	}
}

// Inserting a chat for a project that does not exist is now rejected, so the
// orphan state cannot be re-created.
func TestCreateChatRejectsMissingProject(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()
	now := time.Now()
	err := repo.CreateChat(context.Background(), &Chat{
		ID: "ghost", Title: "ghost", ProjectID: "no-such-project", UserID: "owner",
		State: 2, CreatedAt: now, UpdatedAt: now, LastActive: now,
	})
	require.Error(t, err)
}

// A project's chats carry rows that RESTRICT each other's deletion (messages
// against threads, fork pointers against messages) and rows nothing cascades
// (chat_updates and its heads). DeleteProject removes all of it, across more
// than one batch of chats, and leaves every other project alone — including a
// chat elsewhere that was forked from one of the deleted messages, whose fork
// pointer would otherwise refuse the delete for good.
func TestDeleteProjectRemovesChatContentInBatches(t *testing.T) {
	t.Parallel()

	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	exec := func(query string, args ...any) {
		t.Helper()
		_, err := raw.ExecContext(ctx, query, args...)
		require.NoError(t, err, query)
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		require.NoError(t, raw.QueryRowContext(ctx, query, args...).Scan(&n), query)
		return n
	}

	// seedChat writes a chat with one of everything stored under a chat.
	seedChat := func(project, chat string) {
		exec(`INSERT INTO chats (id, title, project_id, user_id, created_at, updated_at, last_active)
		      VALUES ($1,$1,$2,'owner',$3,$3,$3)`, chat, project, now)
		exec(`INSERT INTO threads (id, chat_id, origin, created_at) VALUES ($1,$2,'root',$3)`, chat+"-thread", chat, now)
		exec(`INSERT INTO context_windows (id, thread_id, sequence, created_at) VALUES ($1,$2,1,$3)`, chat+"-cw", chat+"-thread", now)
		exec(`INSERT INTO messages (id, chat_id, ordinal, thread_id, context_window_id, role, created_at, updated_at, seq)
		      VALUES ($1,$2,1,$3,$4,1,$5,$5,1)`, chat+"-msg", chat, chat+"-thread", chat+"-cw", now)
		exec(`INSERT INTO message_content_blocks (id, message_id, "position", block_type, content, created_at, updated_at)
		      VALUES ($1,$2,0,1,'hello',$3,$3)`, chat+"-block", chat+"-msg", now)
		exec(`INSERT INTO tool_calls (id, chat_id, thread_id, message_id, tool_name, status, requested_at, created_at, updated_at)
		      VALUES ($1,$2,$3,$4,'bash',1,$5,$5,$5)`, chat+"-tc", chat, chat+"-thread", chat+"-msg", now)
		exec(`INSERT INTO tool_call_results (tool_call_id, message_id, content, created_at, updated_at)
		      VALUES ($1,$2,'ok',$3,$3)`, chat+"-tc", chat+"-msg", now)
		exec(`INSERT INTO approvals (id, chat_id, thread_id, approval_type, entity_id, status, title, created_at)
		      VALUES ($1,$2,$3,1,$4,1,'approve me',$5)`, chat+"-appr", chat, chat+"-thread", chat+"-tc", now)
		exec(`INSERT INTO chat_updates (id, chat_id, sequence_number, update_type, entity_id, data, created_at)
		      VALUES ($1,$2,1,2,$3,'{}',$4)`, chat+"-upd", chat, chat+"-appr", now)
		exec(`INSERT INTO chat_update_heads (chat_id, head_key, update_id, sequence_number, update_type, entity_id, data, created_at)
		      VALUES ($1,'e:'||$2,$3,1,2,$2,'{}',$4)`, chat, chat+"-appr", chat+"-upd", now)
		// The fork pointers within the chat: RESTRICT against its own message.
		exec(`UPDATE context_windows SET fork_at_message_id = $2 WHERE id = $1`, chat+"-cw", chat+"-msg")
		exec(`UPDATE threads SET fork_at_message_id = $2 WHERE id = $1`, chat+"-thread", chat+"-msg")
	}

	for _, id := range []string{"doomed", "survivor"} {
		require.NoError(t, repo.CreateProject(ctx, &Project{
			ID: id, Name: id, Path: "/tmp/" + id, UserID: "owner",
			CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
		exec(`INSERT INTO worktrees (id, project_id, name, path, branch, base_branch, status, created_at, updated_at, last_active)
		      VALUES ($1,$2,$1,$3,'b','main',1,$4,$4,$4)`, "wt-"+id, id, "/tmp/wt-"+id, now)
		exec(`INSERT INTO user_updates (id, user_id, sequence_number, project_id, worktree_id, update_type, entity_type, entity_id, data, created_at)
		      VALUES ($1,'owner',$2,$3,$4,1,1,$3,'{}',$5)`, "uu-wt-"+id, len(id), id, "wt-"+id, now)
		exec(`INSERT INTO user_updates (id, user_id, sequence_number, project_id, update_type, entity_type, entity_id, data, created_at)
		      VALUES ($1,'owner',$2,$3,1,1,$3,'{}',$4)`, "uu-"+id, 100+len(id), id, now)
	}

	// More chats than one batch removes, so the loop is what deletes them.
	const doomedChats = 60
	for i := range doomedChats {
		seedChat("doomed", fmt.Sprintf("doomed-chat-%02d", i))
	}
	seedChat("survivor", "kept-chat")
	// A chat in the surviving project, forked at a message of the doomed one.
	exec(`UPDATE threads SET fork_at_message_id = 'doomed-chat-00-msg' WHERE id = 'kept-chat-thread'`)
	exec(`UPDATE context_windows SET fork_at_message_id = 'doomed-chat-00-msg' WHERE id = 'kept-chat-cw'`)

	// Another user's delete of it is a no-op, not an error.
	require.NoError(t, repo.DeleteProject(ctx, "doomed", "someone-else"))
	require.Equal(t, doomedChats, count(`SELECT COUNT(*) FROM chats WHERE project_id = 'doomed'`))

	require.NoError(t, repo.DeleteProject(ctx, "doomed", "owner"))

	for table, query := range map[string]string{
		"projects":               `SELECT COUNT(*) FROM projects WHERE id = 'doomed'`,
		"chats":                  `SELECT COUNT(*) FROM chats WHERE project_id = 'doomed'`,
		"threads":                `SELECT COUNT(*) FROM threads WHERE chat_id LIKE 'doomed-chat-%'`,
		"context_windows":        `SELECT COUNT(*) FROM context_windows WHERE id LIKE 'doomed-chat-%'`,
		"messages":               `SELECT COUNT(*) FROM messages WHERE chat_id LIKE 'doomed-chat-%'`,
		"message_content_blocks": `SELECT COUNT(*) FROM message_content_blocks WHERE id LIKE 'doomed-chat-%'`,
		"tool_calls":             `SELECT COUNT(*) FROM tool_calls WHERE chat_id LIKE 'doomed-chat-%'`,
		"tool_call_results":      `SELECT COUNT(*) FROM tool_call_results WHERE tool_call_id LIKE 'doomed-chat-%'`,
		"approvals":              `SELECT COUNT(*) FROM approvals WHERE chat_id LIKE 'doomed-chat-%'`,
		"chat_updates":           `SELECT COUNT(*) FROM chat_updates WHERE chat_id LIKE 'doomed-chat-%'`,
		"chat_update_heads":      `SELECT COUNT(*) FROM chat_update_heads WHERE chat_id LIKE 'doomed-chat-%'`,
		"worktrees":              `SELECT COUNT(*) FROM worktrees WHERE project_id = 'doomed'`,
		"user_updates":           `SELECT COUNT(*) FROM user_updates WHERE project_id = 'doomed' OR worktree_id = 'wt-doomed'`,
	} {
		assert.Zero(t, count(query), "%s rows of the deleted project remain", table)
	}

	// The other project is whole, and its fork lost only the pointer.
	for table, query := range map[string]string{
		"projects":          `SELECT COUNT(*) FROM projects WHERE id = 'survivor'`,
		"chats":             `SELECT COUNT(*) FROM chats WHERE id = 'kept-chat'`,
		"messages":          `SELECT COUNT(*) FROM messages WHERE id = 'kept-chat-msg'`,
		"chat_updates":      `SELECT COUNT(*) FROM chat_updates WHERE chat_id = 'kept-chat'`,
		"worktrees":         `SELECT COUNT(*) FROM worktrees WHERE id = 'wt-survivor'`,
		"thread, unforked":  `SELECT COUNT(*) FROM threads WHERE id = 'kept-chat-thread' AND fork_at_message_id IS NULL`,
		"context, unforked": `SELECT COUNT(*) FROM context_windows WHERE id = 'kept-chat-cw' AND fork_at_message_id IS NULL`,
	} {
		assert.Equal(t, 1, count(query), "%s of the surviving project", table)
	}
	assert.Equal(t, 2, count(`SELECT COUNT(*) FROM user_updates WHERE project_id = 'survivor'`))
}
