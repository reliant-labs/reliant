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
	"github.com/reliant-labs/reliant/internal/store"
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

	all, more, err := store.ListArchivedChats(ctx, repo.ORM(), "owner", nil, ArchivedChatPage{})
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
		chats, more, err := store.ListArchivedChats(ctx, repo.ORM(), "owner", nil, page)
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

// Every page of the archived list is the caller's alone: another user's
// archived chats, interleaved with the caller's on (updated_at, id), never
// enter a page or decide whether another page follows, and a cursor taken
// from the other user's page only positions the caller's own list.
func TestListArchivedChatsPagesStayOwnerScoped(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	base := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) // fixture data, not a clock

	users := []string{"alice", "bob"}
	for _, u := range users {
		require.NoError(t, repo.CreateProject(ctx, &Project{
			ID: "p-" + u, Name: u, Path: "/tmp/" + u, UserID: u,
			CreatedAt: base, UpdatedAt: base, LastActive: base,
		}))
	}
	// Six archived chats each, alternating owners minute by minute, with
	// every pair sharing an updated_at so ids from both users tie on time.
	// Bob also has the newest one, so alice's first page sits under it.
	for i := range 12 {
		u := users[i%2]
		at := base.Add(time.Duration(i/2) * time.Minute)
		require.NoError(t, repo.CreateChat(ctx, &Chat{
			ID: fmt.Sprintf("%s-%02d", u, i), Title: "t", ProjectID: "p-" + u, UserID: u,
			State: ChatStateArchived, CreatedAt: at, UpdatedAt: at, LastActive: at,
		}))
	}
	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID: "bob-newest", Title: "t", ProjectID: "p-bob", UserID: "bob",
		State: ChatStateArchived, CreatedAt: base, UpdatedAt: base.Add(time.Hour), LastActive: base,
	}))

	readAll := func(user string, page ArchivedChatPage) []string {
		var ids []string
		for pages := 0; ; pages++ {
			require.Less(t, pages, 10, "paging did not end")
			chats, more, err := store.ListArchivedChats(ctx, repo.ORM(), user, nil, page)
			require.NoError(t, err)
			for _, c := range chats {
				assert.Equal(t, user, c.UserID, "%s's page lists %s", user, c.ID)
				ids = append(ids, c.ID)
			}
			if !more {
				// The last page is the last of the caller's rows: a
				// continuation that only another user's rows follow would
				// say more and then return an empty page.
				assert.NotEmpty(t, chats, "%s's last page is empty", user)
				return ids
			}
			last := chats[len(chats)-1]
			page.After = &ArchivedChatCursor{UpdatedAt: last.UpdatedAt, ChatID: last.ID}
		}
	}

	for _, u := range users {
		all, more, err := store.ListArchivedChats(ctx, repo.ORM(), u, nil, ArchivedChatPage{})
		require.NoError(t, err)
		require.False(t, more)
		want := make([]string, len(all))
		for i, c := range all {
			require.Equal(t, u, c.UserID, "%s's unpaged list holds %s", u, c.ID)
			want[i] = c.ID
		}
		assert.Equal(t, want, readAll(u, ArchivedChatPage{Limit: 2}), "%s pages exactly the unpaged list", u)
	}
	assert.Len(t, readAll("alice", ArchivedChatPage{Limit: 2}), 6)
	assert.Len(t, readAll("bob", ArchivedChatPage{Limit: 2}), 7)

	// Alice reuses a cursor from bob's first page: it only positions her own
	// list (after bob-11, which ties alice-10 on updated_at), never lists
	// bob's rows below it.
	bobFirst, _, err := store.ListArchivedChats(ctx, repo.ORM(), "bob", nil, ArchivedChatPage{Limit: 2})
	require.NoError(t, err)
	require.Len(t, bobFirst, 2)
	bobCursor := &ArchivedChatCursor{UpdatedAt: bobFirst[1].UpdatedAt, ChatID: bobFirst[1].ID}
	chats, _, err := store.ListArchivedChats(ctx, repo.ORM(), "alice", nil, ArchivedChatPage{Limit: 50, After: bobCursor})
	require.NoError(t, err)
	for _, c := range chats {
		assert.Equal(t, "alice", c.UserID, "a cursor from bob's page lists %s", c.ID)
		assert.True(t, c.UpdatedAt.Before(bobCursor.UpdatedAt) ||
			(c.UpdatedAt.Equal(bobCursor.UpdatedAt) && c.ID < bobCursor.ChatID), "%s is after the cursor", c.ID)
	}
	assert.NotEmpty(t, chats)
}
