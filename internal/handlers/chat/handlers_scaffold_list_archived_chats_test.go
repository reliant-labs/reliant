package chat_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"
	"github.com/reliant-labs/forge/pkg/testkit"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
	"github.com/reliant-labs/reliant/internal/ptr"
)

func TestListArchivedChats_Generated(t *testing.T) {
	t.Parallel()

	repo := newChatRepo()
	repo.ListArchivedChatsFunc = func(_ context.Context, userID string) ([]*db.ArchivedChatInfo, error) {
		if userID != "test-user" {
			return nil, errors.New("store unavailable")
		}
		return []*db.ArchivedChatInfo{{
			Chat:         db.Chat{ID: archivedChatID, UserID: userID, ProjectID: "project-1", State: db.ChatStateArchived},
			WorktreeName: ptr.Of("feature-x"),
		}}, nil
	}
	svc := chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{Repo: repo}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListArchivedChatsRequest, pb.ListArchivedChatsResponse]{
		{
			Name: "the caller's archived chats, with their worktree",
			Req:  connect.NewRequest(&pb.ListArchivedChatsRequest{}),
			Ctx:  chat.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListArchivedChatsResponse]) {
				require.Len(t, resp.Msg.GetChats(), 1)
				require.Equal(t, archivedChatID, resp.Msg.GetChats()[0].GetChat().GetId())
				require.Equal(t, "feature-x", resp.Msg.GetChats()[0].GetWorktreeName())
			},
		},
		{
			Name:    "a store failure is internal",
			Req:     connect.NewRequest(&pb.ListArchivedChatsRequest{}),
			Ctx:     chat.AuthedContext(t, testkit.WithUserID("other-user")),
			WantErr: connect.CodeInternal,
		},
	}, svc.ListArchivedChats)
}
