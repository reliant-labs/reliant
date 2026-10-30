package chat

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// The chat list is conversations: every surface that lists chats (ListChats,
// SearchChats, the Archived tab) keys on the chat's origin and returns a
// USER chat or a run the user adopted, never a run of any other origin. The
// PR pipeline's automations start tens of runs an hour; they belong in Runs.
// A run is still opened by id (GetChat), which is what deep links use.
func TestChatListSurfacesKeyOnOrigin(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	service := &Service{deps: Deps{Repo: repo}}
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "origin-user")
	projectID := "origin-project"
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "origin-user", Name: "origin", Path: "/p/origin",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	origins := map[string]core.ChatOrigin{
		"user":         core.ChatOriginUser,
		"automation":   core.ChatOriginAutomation,
		"agent":        core.ChatOriginAgent,
		"manual-run":   core.ChatOriginManualRun,
		"builder-test": core.ChatOriginBuilderTest,
	}
	create := func(id string, origin core.ChatOrigin, archived, adopted bool) {
		t.Helper()
		// The title carries the search term, so SearchChats matches every one.
		require.NoError(t, repo.CreateChat(ctx, &db.Chat{
			ID: id, UserID: "origin-user", Title: "needle " + id, ProjectID: projectID,
			State: db.ChatStateIdle, Origin: origin, CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
		if adopted {
			ok, err := repo.SetChatAdopted(ctx, id, "origin-user", true)
			require.NoError(t, err)
			require.True(t, ok)
		}
		if archived {
			require.NoError(t, repo.UpdateChatState(ctx, id, db.ChatStateArchived, "test"))
		}
	}
	for name, origin := range origins {
		create(name, origin, false, false)
		create(name+"-adopted", origin, false, true)
		create(name+"-archived", origin, true, false)
		create(name+"-archived-adopted", origin, true, true)
	}

	listed := map[string]bool{}
	resp, err := service.ListChats(ctx, connect.NewRequest(&reliantv1.ListChatsRequest{ProjectId: projectID}))
	require.NoError(t, err)
	for _, c := range resp.Msg.Chats {
		listed[c.Id] = true
	}
	allProjects, err := service.ListChats(ctx, connect.NewRequest(&reliantv1.ListChatsRequest{}))
	require.NoError(t, err)
	assert.Len(t, allProjects.Msg.Chats, len(resp.Msg.Chats), "the all-projects list applies the same policy")

	found := map[string]bool{}
	search, err := service.SearchChats(ctx, connect.NewRequest(&reliantv1.SearchChatsRequest{Query: "needle"}))
	require.NoError(t, err)
	for _, c := range search.Msg.Chats {
		found[c.Id] = true
	}

	archived := map[string]bool{}
	arch, err := service.ListArchivedChats(ctx, connect.NewRequest(&reliantv1.ListArchivedChatsRequest{}))
	require.NoError(t, err)
	for _, c := range arch.Msg.Chats {
		archived[c.Chat.Id] = true
	}

	for name, origin := range origins {
		isUser := origin == core.ChatOriginUser
		assert.Equal(t, isUser, listed[name], "ListChats %s", name)
		assert.Equal(t, isUser, found[name], "SearchChats %s", name)
		assert.Equal(t, isUser, found[name+"-archived"], "SearchChats %s-archived", name)
		assert.Equal(t, isUser, archived[name+"-archived"], "ListArchivedChats %s-archived", name)

		// Adopted, a run of any origin is the user's chat.
		assert.True(t, listed[name+"-adopted"], "ListChats %s-adopted", name)
		assert.True(t, found[name+"-adopted"], "SearchChats %s-adopted", name)
		assert.True(t, archived[name+"-archived-adopted"], "ListArchivedChats %s-archived-adopted", name)

		assert.False(t, listed[name+"-archived"], "ListChats never lists an archived chat")

		// Out of the list, still reachable by link, carrying its origin.
		got, err := service.GetChat(ctx, connect.NewRequest(&reliantv1.GetChatRequest{ChatId: name}))
		require.NoError(t, err, name)
		assert.Equal(t, origin, got.Msg.Chat.GetOrigin(), name)
	}
}
