// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"slices"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// TestProjectDeleteTakesTheChatPRLinks: chat_pr_links has no foreign key, so
// project delete removes it by hand: the links of the project's chats, and
// any that name the project for a chat that is gone. A chat that moved out
// of the project keeps its links, another project's links stay, and a link
// written after the delete finds no chat and writes nothing.
func TestProjectDeleteTakesTheChatPRLinks(t *testing.T) {
	t.Parallel()
	f := newLinkFixture(t)
	f.project("proj-del", "alice", "https://github.com/acme/widgets")
	f.project("proj-keep", "alice", "https://github.com/acme/gadgets")
	f.chat("chat-del", "alice", "proj-del", "")
	f.chat("chat-moved", "alice", "proj-del", "")
	f.chat("chat-keep", "alice", "proj-keep", "")
	f.write(7, core.ChatPRLinkWorktreePR, "", "chat-del", "chat-moved", "chat-keep")
	// chat-moved's link was written in proj-del; the chat then moved. An
	// orphan names proj-del for a chat that no longer exists.
	if _, err := f.env.SQL.Exec(`UPDATE chats SET project_id = 'proj-keep' WHERE id = 'chat-moved'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.SQL.Exec(`INSERT INTO chat_pr_links
		(id, chat_id, user_id, project_id, provider, repository, pr_number, source, created_at, updated_at)
		VALUES ('orphan', 'chat-gone', 'alice', 'proj-del', 'github', 'acme/widgets', 9, 'head_ref', $1, $1)`, linkAt); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteUserProject(f.ctx, f.env.ORM, "proj-del", "alice"); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	var left []string
	if err := f.env.ORM.Bun().NewRaw(`SELECT chat_id FROM chat_pr_links ORDER BY chat_id`).Scan(f.ctx, &left); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(left, []string{"chat-keep", "chat-moved"}) {
		t.Fatalf("links after the delete = %v, want chat-keep's and chat-moved's", left)
	}
	if n := f.write(8, core.ChatPRLinkIntegration, "", "chat-del"); n != 0 {
		t.Fatalf("a link to a deleted chat wrote %d rows, want 0", n)
	}
}
