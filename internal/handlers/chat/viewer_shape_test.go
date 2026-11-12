package chat

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// What a viewer of a shared chat is answered (control-plane
// docs/design/org-sharing.md §4): the chat marked viewer_access without the
// owner's composer, workspace or machine; the transcript with every tool
// input and no tool result; and reading it changes nothing of the owner's
// (unread, last_active, updated_at) and wakes no machine.
func TestViewerReadsAreReadOnlyAndRedacted(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	router := &wakeRecordingRouter{Recorder: &toolexectest.Recorder{}}
	svc := NewTestChat(t, WithChatDeps(Deps{Repo: repo, Orgs: f.Membership, Router: router}))
	require.NoError(t, repo.UpdateChatUnread(f.Owner, f.ChatID, true, "test"))
	before, err := repo.GetChatRow(context.Background(), f.ChatID)
	require.NoError(t, err)

	got, err := svc.GetChat(f.Viewer, connect.NewRequest(&reliantv1.GetChatRequest{ChatId: f.ChatID}))
	require.NoError(t, err)
	chat := got.Msg.Chat
	assert.True(t, chat.GetViewerAccess())
	assert.Equal(t, reliantv1.ResourceVisibility_RESOURCE_VISIBILITY_ORG, chat.GetVisibility())
	assert.NotEmpty(t, chat.GetSharedAt())
	assert.Equal(t, f.OwnerID, chat.GetUserId(), "whose chat it is")
	assert.Nil(t, chat.Selection)
	assert.Nil(t, chat.Worktree)
	assert.Nil(t, chat.ActiveDaemonId)
	assert.False(t, chat.GetUnread(), "the owner's unread flag is theirs")

	own, err := svc.GetChat(f.Owner, connect.NewRequest(&reliantv1.GetChatRequest{ChatId: f.ChatID}))
	require.NoError(t, err)
	assert.False(t, own.Msg.Chat.GetViewerAccess())
	assert.True(t, own.Msg.Chat.GetUnread())
	assert.Equal(t, reliantv1.ResourceVisibility_RESOURCE_VISIBILITY_ORG, own.Msg.Chat.GetVisibility())

	for _, mode := range []*reliantv1.ToolBodyMode{nil, reliantv1.ToolBodyMode_TOOL_BODY_MODE_FULL.Enum(), reliantv1.ToolBodyMode_TOOL_BODY_MODE_STUB.Enum()} {
		viewed, err := svc.ListMessages(f.Viewer, connect.NewRequest(&reliantv1.ListMessagesRequest{ChatId: f.ChatID, ToolBodies: mode}))
		require.NoError(t, err)
		blocks := blocksByID(viewed.Msg.Messages)
		result := blocks[f.Transcript.ResultBlockID]
		require.NotNil(t, result, "the result block is listed")
		assert.Nil(t, result.Content, "a viewer never gets a tool result, mode %v", mode)
		assert.True(t, result.GetBodyElided())
		assert.Equal(t, int64(len(f.Transcript.Result)), result.GetBodySize())
		if mode == nil {
			assert.Equal(t, f.Transcript.DoneInput, blocks[f.Transcript.DoneCallBlockID].GetInput(), "a tool input is whole")
		}
	}
	owned, err := svc.ListMessages(f.Owner, connect.NewRequest(&reliantv1.ListMessagesRequest{ChatId: f.ChatID}))
	require.NoError(t, err)
	assert.NotEmpty(t, blocksByID(owned.Msg.Messages)[f.Transcript.ResultBlockID].GetContent(), "the owner reads the result")

	_, err = svc.ListChatPlans(f.Viewer, connect.NewRequest(&reliantv1.ListChatPlansRequest{ChatId: f.ChatID}))
	require.NoError(t, err)
	_, err = svc.GetWorkflowExecutions(f.Viewer, connect.NewRequest(&reliantv1.GetWorkflowExecutionsRequest{ChatId: f.ChatID}))
	require.NoError(t, err)
	_, err = svc.ListStepExecutions(f.Viewer, connect.NewRequest(&reliantv1.ListStepExecutionsRequest{ChatId: f.ChatID}))
	require.NoError(t, err)

	after, err := repo.GetChatRow(context.Background(), f.ChatID)
	require.NoError(t, err)
	assert.Equal(t, before.Unread, after.Unread, "reading does not clear the owner's unread")
	assert.True(t, before.LastActive.Equal(after.LastActive), "reading does not bump last_active")
	assert.True(t, before.UpdatedAt.Equal(after.UpdatedAt), "reading does not touch the row")
	assert.Empty(t, router.selectors, "a viewer's read never wakes a machine")

	// The bodies a viewer's transcript left out cannot be fetched either.
	_, err = svc.GetContentBlockBodies(f.Viewer, connect.NewRequest(&reliantv1.GetContentBlockBodiesRequest{
		ChatId: f.ChatID, BlockIds: []string{f.Transcript.ResultBlockID, f.Transcript.DoneCallBlockID},
	}))
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "GetContentBlockBodies: %v", err)
	_, err = svc.GetToolResult(f.Viewer, connect.NewRequest(&reliantv1.GetToolResultRequest{ChatId: f.ChatID, ToolCallId: f.Transcript.DoneCallID}))
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "GetToolResult: %v", err)
}

// A chat that is not shared, in a shared project, is the owner's alone: a
// project's sharing never shares a chat in it.
func TestUnsharedChatInASharedProjectIsPrivate(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestChat(t, WithChatDeps(Deps{Repo: repo, Orgs: f.Membership}))
	_, err := svc.GetChat(f.Viewer, connect.NewRequest(&reliantv1.GetChatRequest{ChatId: f.PrivateChatID}))
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
	_, err = svc.ListMessages(f.Viewer, connect.NewRequest(&reliantv1.ListMessagesRequest{ChatId: f.PrivateChatID}))
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
}
