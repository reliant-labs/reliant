// Copyright (c) 2025 Reliant Labs
package accountpurge_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/accountpurge/purgetest"
	"github.com/reliant-labs/reliant/internal/db"
)

// TestPurge_RemovesTheChatPRLinks: chat_pr_links (which pull requests a
// chat produced) has no foreign key to chats, so the purge deletes it by
// hand. The purged user's links go; another user's links stay.
func TestPurge_RemovesTheChatPRLinks(t *testing.T) {
	repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	purger := purgetest.New(t, repo)
	defer cleanup()

	const victim, bystander = "user-links-deleted", "user-links-retained"
	at := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	for _, u := range []string{victim, bystander} {
		seedUser(t, rawDB, u)
		for i, source := range []string{"worktree_pr", "head_ref"} {
			if _, err := rawDB.Exec(`INSERT INTO chat_pr_links
				(id, chat_id, user_id, project_id, provider, repository, pr_number, source, created_at, updated_at)
				VALUES ($1, $2, $3, $4, 'github', 'acme/widgets', $5, $6, $7, $7)`,
				"link-"+source+"-"+u, "chat-"+u, u, "proj-"+u, 7+i, source, at); err != nil {
				t.Fatalf("seed link: %v", err)
			}
		}
	}

	if _, err := purger.Purge(context.Background(), victim); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if n := chatPRLinks(t, rawDB, victim); n != 0 {
		t.Errorf("chat_pr_links: %d of the purged user's rows survived, want 0", n)
	}
	if n := chatPRLinks(t, rawDB, bystander); n != 2 {
		t.Errorf("chat_pr_links: the bystander has %d rows, want 2", n)
	}
}

// chatPRLinks counts userID's links by their chat's id as well as their
// user_id, so a row the purge forgot is found whichever key it kept.
func chatPRLinks(t *testing.T, rawDB *sql.DB, userID string) int {
	t.Helper()
	var n int
	if err := rawDB.QueryRow(`SELECT COUNT(*) FROM chat_pr_links WHERE user_id = $1 OR chat_id = 'chat-' || $1`, userID).Scan(&n); err != nil {
		t.Fatalf("count chat_pr_links: %v", err)
	}
	return n
}
