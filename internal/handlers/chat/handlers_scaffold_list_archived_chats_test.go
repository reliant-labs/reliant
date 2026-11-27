package chat_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"
	"github.com/reliant-labs/forge/pkg/testkit"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/chataccess/chataccesstest"
	"github.com/reliant-labs/reliant/internal/chatselection"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
	"github.com/reliant-labs/reliant/internal/ptr"
)

func TestListArchivedChats_Generated(t *testing.T) {
	t.Parallel()

	updated := time.Date(2026, 11, 18, 20, 0, 0, 123456000, time.UTC)
	// The page the handler asked the store for, per caller.
	var mu sync.Mutex
	asked := map[string]db.ArchivedChatPage{}
	repo := newChatRepo()
	repo.ListArchivedChatsFunc = func(_ context.Context, userID string, _ *core.OrgScope, page db.ArchivedChatPage) ([]*db.ArchivedChatInfo, bool, error) {
		if userID == "other-user" {
			return nil, false, errors.New("store unavailable")
		}
		mu.Lock()
		asked[userID] = page
		mu.Unlock()
		return []*db.ArchivedChatInfo{{
			Chat:         db.Chat{ID: archivedChatID, UserID: userID, ProjectID: "project-1", State: db.ChatStateArchived, UpdatedAt: updated},
			WorktreeName: ptr.Of("feature-x"),
		}}, page.Limit > 0, nil
	}
	pageOf := func(userID string) db.ArchivedChatPage {
		mu.Lock()
		defer mu.Unlock()
		return asked[userID]
	}
	svc := chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{TriggerReads: &chat.MockTriggerReads{}, Repo: repo, Chats: chataccesstest.For(repo), Questions: &chat.MockQuestions{}, Spawns: &chat.MockSpawns{}, Selections: &chatselection.MockStore{}, Mailbox: &chat.MockMailbox{}, AgentMessages: &chat.MockAgentMessages{}, Messages: &chat.MockMessages{}, ToolBlocks: &chat.MockToolBlocks{}, Senders: &chat.MockSenders{}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListArchivedChatsRequest, pb.ListArchivedChatsResponse]{
		{
			Name: "the caller's archived chats, with their worktree",
			Req:  connect.NewRequest(&pb.ListArchivedChatsRequest{}),
			Ctx:  chat.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListArchivedChatsResponse]) {
				require.Len(t, resp.Msg.GetChats(), 1)
				require.Equal(t, archivedChatID, resp.Msg.GetChats()[0].GetChat().GetId())
				require.Equal(t, "feature-x", resp.Msg.GetChats()[0].GetWorktreeName())
				// No page size and no token: the unpaged list older clients read.
				require.Equal(t, db.ArchivedChatPage{}, pageOf("test-user"))
				require.Empty(t, resp.Msg.GetNextPageToken())
			},
		},
		{
			Name: "a page size pages, and the token names the page's last chat",
			Req:  connect.NewRequest(&pb.ListArchivedChatsRequest{PageSize: ptr.Of[int32](1)}),
			Ctx:  chat.AuthedContext(t, testkit.WithUserID("paging-user")),
			Check: func(t *testing.T, resp *connect.Response[pb.ListArchivedChatsResponse]) {
				require.Equal(t, db.ArchivedChatPage{Limit: 1}, pageOf("paging-user"))
				require.NotEmpty(t, resp.Msg.GetNextPageToken())
			},
		},
		{
			Name: "a page size past the cap is clamped",
			Req:  connect.NewRequest(&pb.ListArchivedChatsRequest{PageSize: ptr.Of[int32](100_000)}),
			Ctx:  chat.AuthedContext(t, testkit.WithUserID("greedy-user")),
			Check: func(t *testing.T, _ *connect.Response[pb.ListArchivedChatsResponse]) {
				require.Equal(t, 200, pageOf("greedy-user").Limit)
			},
		},
		{
			Name:    "a malformed page token is invalid",
			Req:     connect.NewRequest(&pb.ListArchivedChatsRequest{PageToken: ptr.Of("not a token")}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a store failure is internal",
			Req:     connect.NewRequest(&pb.ListArchivedChatsRequest{}),
			Ctx:     chat.AuthedContext(t, testkit.WithUserID("other-user")),
			WantErr: connect.CodeInternal,
		},
	}, svc.ListArchivedChats)

	// The token continues after the chat it was cut from, at the default page
	// size when the request names none.
	first, err := svc.ListArchivedChats(chat.AuthedContext(t, testkit.WithUserID("first-user")),
		connect.NewRequest(&pb.ListArchivedChatsRequest{PageSize: ptr.Of[int32](1)}))
	require.NoError(t, err)
	_, err = svc.ListArchivedChats(chat.AuthedContext(t, testkit.WithUserID("next-user")),
		connect.NewRequest(&pb.ListArchivedChatsRequest{PageToken: ptr.Of(first.Msg.GetNextPageToken())}))
	require.NoError(t, err)
	next := pageOf("next-user")
	require.Equal(t, 50, next.Limit)
	require.NotNil(t, next.After)
	require.Equal(t, archivedChatID, next.After.ChatID)
	require.True(t, updated.Equal(next.After.UpdatedAt), "cursor time %v, want %v", next.After.UpdatedAt, updated)
}
