package chat_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
)

// acmeOnly: test-user is a live member of acme, and of no other org.
type acmeOnly struct{}

func (acmeOnly) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	return userID == "test-user" && orgID == "acme", true, nil
}

// A member removed from an org loses that org's chats from every collection,
// not only from GetChat: the list, the search and the archive, whether or not
// the request names an org. Chats with no org, and those of an org they are
// still in, stay.
func TestChatCollections_HideChatsOfAnOrgTheCallerLeft(t *testing.T) {
	t.Parallel()
	org := func(s string) *string { return &s }
	rows := func(state db.ChatState) []db.Chat {
		return []db.Chat{
			{ID: "unassigned", UserID: "test-user", ProjectID: "project-1", State: state},
			{ID: "in-acme", UserID: "test-user", ProjectID: "project-1", OrgID: org("acme"), State: state},
			{ID: "in-left-org", UserID: "test-user", ProjectID: "project-1", OrgID: org("globex"), State: state},
		}
	}
	ptrs := func(cs []db.Chat) []*db.Chat {
		out := make([]*db.Chat, len(cs))
		for i := range cs {
			out[i] = &cs[i]
		}
		return out
	}

	repo := newChatRepo()
	repo.ListChatsFunc = func(context.Context, db.ChatFilters) ([]*db.Chat, error) {
		return ptrs(rows(db.ChatStateIdle)), nil
	}
	repo.SearchChatsFunc = func(context.Context, db.ChatSearchFilters) ([]*db.Chat, error) {
		return ptrs(rows(db.ChatStateIdle)), nil
	}
	repo.ListArchivedChatsFunc = func(context.Context, string) ([]*db.ArchivedChatInfo, error) {
		var out []*db.ArchivedChatInfo
		for _, c := range rows(db.ChatStateArchived) {
			out = append(out, &db.ArchivedChatInfo{Chat: c})
		}
		return out, nil
	}
	repo.GetLatestUserUpdateSequenceFunc = func(context.Context, string) (int64, error) { return 0, nil }
	svc := chat.NewTestChat(t, chat.WithChatDeps(chat.Deps{Repo: repo}))
	ctx := authz.WithMembership(chat.AuthedContext(t), acmeOnly{})

	want := []string{"unassigned", "in-acme"}
	ids := func(cs []*pb.Chat) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.GetId())
		}
		return out
	}

	listed, err := svc.ListChats(ctx, connect.NewRequest(&pb.ListChatsRequest{}))
	require.NoError(t, err)
	require.Equal(t, want, ids(listed.Msg.GetChats()), "ListChats with no org_id")
	require.EqualValues(t, len(want), listed.Msg.GetTotal())

	found, err := svc.SearchChats(ctx, connect.NewRequest(&pb.SearchChatsRequest{Query: "x"}))
	require.NoError(t, err)
	require.Equal(t, want, ids(found.Msg.GetChats()), "SearchChats")

	archived, err := svc.ListArchivedChats(ctx, connect.NewRequest(&pb.ListArchivedChatsRequest{}))
	require.NoError(t, err)
	var archivedIDs []string
	for _, ac := range archived.Msg.GetChats() {
		archivedIDs = append(archivedIDs, ac.GetChat().GetId())
	}
	require.Equal(t, want, archivedIDs, "ListArchivedChats")
}
