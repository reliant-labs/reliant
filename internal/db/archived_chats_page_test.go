// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The archived list pages by keyset on (updated_at, id): reading it page by
// page returns exactly the unpaged list, in its order, ties on updated_at
// included, and says where it ends.
func TestListArchivedChatsPages(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	base := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) // fixture data, not a clock

	require.NoError(t, repo.CreateProject(ctx, &Project{
		ID: "p", Name: "p", Path: "/tmp/p", UserID: "owner",
		CreatedAt: base, UpdatedAt: base, LastActive: base,
	}))
	// Seven archived chats; three pairs share an updated_at, so the id breaks
	// the tie. An archived automation run and a live chat are not listed.
	for i := range 7 {
		at := base.Add(time.Duration(i/2) * time.Minute)
		require.NoError(t, repo.CreateChat(ctx, &Chat{
			ID: fmt.Sprintf("archived-%d", i), Title: "t", ProjectID: "p", UserID: "owner",
			State: ChatStateArchived, CreatedAt: at, UpdatedAt: at, LastActive: at,
		}))
	}
	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID: "automation-run", Title: "t", ProjectID: "p", UserID: "owner", Origin: core.ChatOriginAutomation,
		State: ChatStateArchived, CreatedAt: base, UpdatedAt: base.Add(time.Hour), LastActive: base,
	}))
	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID: "live", Title: "t", ProjectID: "p", UserID: "owner",
		State: 2, CreatedAt: base, UpdatedAt: base.Add(time.Hour), LastActive: base,
	}))

	all, more, err := repo.ListArchivedChats(ctx, "owner", nil, ArchivedChatPage{})
	require.NoError(t, err)
	assert.False(t, more, "an unpaged read lists everything")
	require.Len(t, all, 7)
	want := make([]string, len(all))
	for i, c := range all {
		want[i] = c.ID
		if i > 0 {
			prev := all[i-1]
			assert.True(t, prev.UpdatedAt.After(c.UpdatedAt) || (prev.UpdatedAt.Equal(c.UpdatedAt) && prev.ID > c.ID),
				"order: %s before %s", prev.ID, c.ID)
		}
	}

	var got []string
	page := ArchivedChatPage{Limit: 3}
	for pages := 0; ; pages++ {
		require.Less(t, pages, 5, "paging did not end")
		chats, more, err := repo.ListArchivedChats(ctx, "owner", nil, page)
		require.NoError(t, err)
		require.LessOrEqual(t, len(chats), 3)
		for _, c := range chats {
			got = append(got, c.ID)
		}
		if !more {
			break
		}
		last := chats[len(chats)-1]
		page.After = &ArchivedChatCursor{UpdatedAt: last.UpdatedAt, ChatID: last.ID}
	}
	assert.Equal(t, want, got)
}
