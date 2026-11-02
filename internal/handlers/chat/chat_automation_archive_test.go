package chat

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// An automation archives each run it finishes successfully. Those runs are
// history in Runs, not chats the person put away, so the sidebar's Archived
// tab — like its chat list — leaves them out. Adopting one ("Open as chat")
// brings it back into the chat list.

func archivedChatIDs(t *testing.T, ctx context.Context, service *Service) map[string]bool {
	t.Helper()
	resp, err := service.ListArchivedChats(ctx, connect.NewRequest(&reliantv1.ListArchivedChatsRequest{}))
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, c := range resp.Msg.Chats {
		ids[c.Chat.Id] = true
	}
	return ids
}

func sidebarChatIDs(t *testing.T, ctx context.Context, service *Service, projectID string) map[string]bool {
	t.Helper()
	resp, err := service.ListChats(ctx, connect.NewRequest(&reliantv1.ListChatsRequest{ProjectId: projectID}))
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, c := range resp.Msg.Chats {
		ids[c.Id] = true
	}
	return ids
}

func TestListArchivedChats_LeavesOutAutomationRuns(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	service := &Service{deps: Deps{Repo: repo}}

	ctx, run := setupAbsorbFixture(t, repo, "test-user", db.Completed())
	seedScheduledChat(t, ctx, repo, run.chatID)
	projectID := mustChatProject(t, ctx, repo, run.chatID)

	// A chat the person started and archived, in the same project.
	now := time.Now().UTC()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: "archived-by-person", UserID: "test-user", Title: "put away", ProjectID: projectID,
		State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	require.NoError(t, repo.UpdateChatState(ctx, "archived-by-person", db.ChatStateArchived, "user_archived"))
	require.NoError(t, repo.UpdateChatState(ctx, run.chatID, db.ChatStateArchived, "automation_succeeded"))

	archived := archivedChatIDs(t, ctx, service)
	assert.True(t, archived["archived-by-person"], "a chat the person archived is in the Archived tab")
	assert.False(t, archived[run.chatID], "an archived automation run is not")
	assert.False(t, sidebarChatIDs(t, ctx, service, projectID)[run.chatID], "nor in the chat list")
}

func TestAdoptChat_UnarchivesAnArchivedAutomationRun(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	service := &Service{deps: Deps{Repo: repo}}

	ctx, run := setupAbsorbFixture(t, repo, "test-user", db.Completed())
	seedScheduledChat(t, ctx, repo, run.chatID)
	require.NoError(t, repo.UpdateChatState(ctx, run.chatID, db.ChatStateArchived, "automation_succeeded"))

	resp, err := service.AdoptChat(ctx, connect.NewRequest(&reliantv1.AdoptChatRequest{ChatId: run.chatID}))
	require.NoError(t, err)
	assert.Equal(t, reliantv1.ChatState_CHAT_STATE_IDLE, resp.Msg.Chat.State, "the adopted run comes out of the archive")
	assert.True(t, sidebarChatIDs(t, ctx, service, mustChatProject(t, ctx, repo, run.chatID))[run.chatID])

	// Un-adopting is not archiving: the run stays where it is.
	un, err := service.UnadoptChat(ctx, connect.NewRequest(&reliantv1.UnadoptChatRequest{ChatId: run.chatID}))
	require.NoError(t, err)
	assert.Equal(t, reliantv1.ChatState_CHAT_STATE_IDLE, un.Msg.Chat.State)
}
